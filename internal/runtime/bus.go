package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

const (
	eventChanBufferSize = 64
)

// subscriber holds a channel and filter for event distribution.
type subscriber struct {
	ch     chan *ares_events.Event
	filter ares_events.EventFilter
}

// namedHook pairs a WorkflowHook with its plugin name for diagnostics.
type namedHook struct {
	pluginName string
	hook       WorkflowHook
}

// PluginBus manages plugin registration, lifecycle, and hook invocation.
// It provides the EventBus interface to plugins and coordinates BeforeStep/
// AfterStep hook calls with timeout and panic recovery.
type PluginBus struct {
	plugins       []RuntimePlugin
	hooks         []namedHook
	caps          map[Capability][]RuntimePlugin
	subscribers   []*subscriber
	mu            sync.RWMutex
	started       bool
	pluginTimeout time.Duration
	logger        *slog.Logger
	droppedEvents atomic.Int64
}

// NewPluginBus creates a PluginBus with the given options.
func NewPluginBus(opts ...PluginBusOption) *PluginBus {
	b := &PluginBus{
		caps:          make(map[Capability][]RuntimePlugin),
		pluginTimeout: defaultPluginTimeout,
		logger:        slog.Default(),
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// Register adds a plugin to the bus. Returns ErrDuplicatePlugin if a plugin
// with the same name is already registered. Returns ErrBusAlreadyStarted if
// called after Start.
// If the plugin also implements WorkflowHook, it is automatically registered
// as a hook.
func (b *PluginBus) Register(plugin RuntimePlugin) error {
	if plugin == nil {
		return errors.New("runtime: cannot register nil plugin")
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.started {
		return ErrBusAlreadyStarted
	}
	for _, p := range b.plugins {
		if p.Name() == plugin.Name() {
			return fmt.Errorf("runtime: %w: %s", ErrDuplicatePlugin, plugin.Name())
		}
	}
	b.plugins = append(b.plugins, plugin)
	for _, cap := range plugin.Capabilities() {
		b.caps[cap] = append(b.caps[cap], plugin)
	}
	// Auto-register as WorkflowHook if the plugin implements it.
	if hook, ok := plugin.(WorkflowHook); ok {
		b.hooks = append(b.hooks, namedHook{pluginName: plugin.Name(), hook: hook})
	}
	return nil
}

// Start initializes all registered plugins. If a plugin fails to start,
// the error is logged but Start continues with remaining plugins.
// Returns a combined error if any plugin failed.
func (b *PluginBus) Start(ctx context.Context) error {
	b.mu.Lock()
	b.started = true
	b.mu.Unlock()

	var errs []error
	for _, p := range b.plugins {
		if err := b.invokeStart(ctx, p); err != nil {
			b.Emit(ctx, p.Name(), EventPluginFailed, "runtime", map[string]any{
				PayloadKeyPluginName: p.Name(),
				PayloadKeyError:      err.Error(),
			})
			errs = append(errs, err)
		} else {
			b.Emit(ctx, p.Name(), EventPluginStarted, "runtime", map[string]any{
				PayloadKeyPluginName:         p.Name(),
				PayloadKeyPluginCapabilities: fmt.Sprintf("%v", p.Capabilities()),
			})
		}
	}
	return errors.Join(errs...)
}

// Stop shuts down all plugins in reverse registration order.
func (b *PluginBus) Stop(ctx context.Context) error {
	b.mu.Lock()
	b.started = false
	// B22: Clean up all subscribers to prevent leaked goroutines.
	// Close subscriber channels and clear the slice so Emit (which still
	// holds RLock) can no longer send to stale channels after Stop.
	for _, s := range b.subscribers {
		close(s.ch)
	}
	b.subscribers = b.subscribers[:0]
	b.mu.Unlock()

	var errs []error
	for i := len(b.plugins) - 1; i >= 0; i-- {
		p := b.plugins[i]
		if err := invokeWithTimeout(ctx, b.pluginTimeout, p.Name(), func(sctx context.Context) error {
			return p.Stop(sctx)
		}); err != nil {
			b.logger.Error("runtime: plugin stop failed",
				"plugin", p.Name(),
				"error", err,
			)
			b.Emit(ctx, p.Name(), EventPluginFailed, "runtime", map[string]any{
				PayloadKeyPluginName: p.Name(),
				PayloadKeyError:      err.Error(),
			})
			errs = append(errs, err)
		} else {
			b.Emit(ctx, p.Name(), EventPluginStopped, "runtime", map[string]any{
				PayloadKeyPluginName: p.Name(),
			})
		}
	}
	return errors.Join(errs...)
}

// RegisterHook adds a named WorkflowHook to be called before and after each step.
// The name is used in logs and error messages to identify the hook.
func (b *PluginBus) RegisterHook(name string, hook WorkflowHook) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.hooks = append(b.hooks, namedHook{pluginName: name, hook: hook})
}

// BeforeStep calls all registered hooks before a step executes.
// Each hook is invoked sequentially. If a hook fails (error, panic, or
// timeout), the error is logged and the remaining hooks still execute.
// Hooks are observational and therefore use a log-and-continue contract.
func (b *PluginBus) BeforeStep(ctx context.Context, executionID string, step *Step) error {
	b.mu.RLock()
	hooks := make([]namedHook, len(b.hooks))
	copy(hooks, b.hooks)
	b.mu.RUnlock()

	var errs []error
	for _, nh := range hooks {
		if err := invokeWithTimeout(ctx, b.pluginTimeout, nh.pluginName+":beforeStep", func(sctx context.Context) error {
			return nh.hook.BeforeStep(sctx, executionID, step)
		}); err != nil {
			b.logger.Warn("runtime: before step hook failed (continuing)",
				"plugin", nh.pluginName,
				"error", err,
			)
			errs = append(errs, fmt.Errorf("runtime: before step hook %s: %w", nh.pluginName, err))
		}
	}
	return errors.Join(errs...)
}

// AfterStep calls all registered hooks after a step completes.
// Each hook is invoked sequentially. If a hook fails, the error is logged
// and the remaining hooks still execute.
func (b *PluginBus) AfterStep(ctx context.Context, executionID string, result *StepResult) error {
	b.mu.RLock()
	hooks := make([]namedHook, len(b.hooks))
	copy(hooks, b.hooks)
	b.mu.RUnlock()

	var errs []error
	for _, nh := range hooks {
		if err := invokeWithTimeout(ctx, b.pluginTimeout, nh.pluginName+":afterStep", func(sctx context.Context) error {
			return nh.hook.AfterStep(sctx, executionID, result)
		}); err != nil {
			b.logger.Warn("runtime: after step hook failed (continuing)",
				"plugin", nh.pluginName,
				"error", err,
			)
			errs = append(errs, fmt.Errorf("runtime: after step hook %s: %w", nh.pluginName, err))
		}
	}
	return errors.Join(errs...)
}

// Emit publishes an event with the given stream ID to all matching
// subscribers. Non-blocking; ares_events are dropped for subscribers whose
// channel buffer is full.
//
// PERF: Holds RLock during the entire dispatch to prevent close-channel race
// with Subscribe's cleanup goroutine. The subscriber list copy + sends are
// fast (buffered channel sends); holding RLock is negligible.
func (b *PluginBus) Emit(ctx context.Context, streamID string, eventType ares_events.EventType, moduleName string, payload map[string]any) {
	evt := &ares_events.Event{
		ID:         ares_events.NewEventID(),
		StreamID:   streamID,
		Type:       eventType,
		ModuleName: moduleName,
		Payload:    payload,
		Timestamp:  time.Now(),
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, s := range b.subscribers {
		if !matchFilter(evt, s.filter) {
			continue
		}
		select {
		case s.ch <- evt:
		case <-ctx.Done():
			return
		default:
			b.droppedEvents.Add(1)
			b.logger.Warn("plugin bus: event dropped, subscriber buffer full",
				"event_type", evt.Type, "stream_id", evt.StreamID)
		}
	}
}

// Subscribe returns a channel that receives ares_events matching the given filter.
// The channel must be drained to prevent backpressure; when ctx is cancelled
// the subscription is automatically removed and the channel is closed.
//
// SAFETY: Emit holds RLock during dispatch, and the cleanup goroutine holds
// exclusive Lock when closing the channel, so close() and send() never race.
func (b *PluginBus) Subscribe(ctx context.Context, filter ares_events.EventFilter) (<-chan *ares_events.Event, error) {
	ch := make(chan *ares_events.Event, eventChanBufferSize)

	sub := &subscriber{
		ch:     ch,
		filter: filter,
	}

	b.mu.Lock()
	b.subscribers = append(b.subscribers, sub)
	b.mu.Unlock()

	go func() {
		<-ctx.Done()
		b.mu.Lock()
		defer b.mu.Unlock()
		for i, s := range b.subscribers {
			if s == sub {
				b.subscribers = append(b.subscribers[:i], b.subscribers[i+1:]...)
				close(ch)
				return
			}
		}
	}()

	return ch, nil
}

// Stats returns runtime metrics for the bus. Currently exposes the count of
// events dropped because a subscriber's channel buffer was full, enabling
// monitoring/debugging of data loss in the event pipeline.
func (b *PluginBus) Stats() map[string]int64 {
	return map[string]int64{
		"dropped_events": b.droppedEvents.Load(),
	}
}

// PluginsByCap returns a copy of the registered plugins with the given capability.
func (b *PluginBus) PluginsByCap(cap Capability) []RuntimePlugin {
	b.mu.RLock()
	defer b.mu.RUnlock()
	plugins := b.caps[cap]
	if len(plugins) == 0 {
		return nil
	}
	result := make([]RuntimePlugin, len(plugins))
	copy(result, plugins)
	return result
}

func (b *PluginBus) invokeStart(ctx context.Context, p RuntimePlugin) error {
	err := invokeWithTimeout(ctx, b.pluginTimeout, p.Name(), func(sctx context.Context) error {
		return p.Start(sctx, b)
	})
	if err != nil {
		b.logger.Error("runtime: plugin start failed",
			"plugin", p.Name(),
			"error", err,
		)
		return err
	}
	b.logger.Info("runtime: plugin started", "plugin", p.Name())
	return nil
}

// invokeWithTimeout runs fn with a timeout derived from the parent context.
// pluginName is attached to any panic error for diagnostics.
func invokeWithTimeout(ctx context.Context, timeout time.Duration, pluginName string, fn func(context.Context) error) (err error) {
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Default().Error("plugin panicked",
					"plugin", pluginName,
					"panic_value", fmt.Sprintf("%v", r),
					"panic_type", fmt.Sprintf("%T", r),
				)
				done <- &PluginError{
					PluginName: pluginName,
					Err:        ErrPluginPanic,
					Recovered:  r,
				}
			}
		}()
		done <- fn(callCtx)
	}()

	select {
	case err = <-done:
		return err
	case <-callCtx.Done():
		return fmt.Errorf("%w: %w", ErrPluginTimeout, callCtx.Err())
	}
}

func matchFilter(evt *ares_events.Event, filter ares_events.EventFilter) bool {
	if len(filter.Types) > 0 {
		matched := false
		for _, t := range filter.Types {
			if evt.Type == t {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(filter.StreamIDs) > 0 {
		matched := false
		for _, sid := range filter.StreamIDs {
			if evt.StreamID == sid {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
