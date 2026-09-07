package evaluation

import (
	"context"
	"fmt"
	"time"
)

// Runner executes a single evaluation task and returns metrics.
type Runner interface {
	// Run executes one evaluation iteration.
	Run(ctx context.Context, task string) (*Metrics, error)
}

// RunnerFunc is a convenience adapter that turns a function into a Runner.
type RunnerFunc func(ctx context.Context, task string) (*Metrics, error)

func (f RunnerFunc) Run(ctx context.Context, task string) (*Metrics, error) {
	return f(ctx, task)
}

// runWithTimeout executes a runner with a timeout guard.
func runWithTimeout(ctx context.Context, runner Runner, task string, timeout time.Duration) (*Metrics, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan struct {
		m   *Metrics
		err error
	}, 1)

	go func() {
		m, err := runner.Run(ctx, task)
		done <- struct {
			m   *Metrics
			err error
		}{m, err}
	}()

	select {
	case r := <-done:
		return r.m, r.err
	case <-ctx.Done():
		// Join the runner goroutine with a bounded grace period so a
		// non-cooperative runner (one that ignores ctx) cannot hang the
		// caller forever; the goroutine is still reaped when it finishes.
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		return nil, fmt.Errorf("task %q timed out after %v", task, timeout)
	}
}
