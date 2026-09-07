package runtime

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// Agent-pool concurrency benchmarks: measure how the runtime Manager
// sustains concurrent registration, start and stop of a pool of agents — the
// substrate the Agent-OS thread model relies on. Run with:
//
//	go test ./internal/ares_runtime/ -bench=BenchmarkAgentPool -benchmem
//
// These are scale sanity checks, not micro-optimizations: the point is to
// catch regressions where the Manager's per-agent locking or resurrection
// bookkeeping stops scaling with pool size.

// BenchmarkAgentPoolConcurrentRegisterStartStop measures the full lifecycle
// (register + start + stop) for N agents across P concurrent goroutines.
func BenchmarkAgentPoolConcurrentRegisterStartStop(b *testing.B) {
	for _, poolSize := range []int{16, 64, 256} {
		b.Run(fmt.Sprintf("pool=%d", poolSize), func(b *testing.B) {
			const workers = 8
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				m := New(nil, nil, nil)
				ctx, cancel := context.WithCancel(context.Background())
				_ = m.Start(ctx)

				var wg sync.WaitGroup
				ch := make(chan int, poolSize)
				for w := 0; w < workers; w++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for idx := range ch {
							id := fmt.Sprintf("agent-%d", idx)
							factory := newMockFactory()
							agent := newMockAgent(id)
							m.RegisterAgent(agent, factory.create())
							if err := m.StartAgent(ctx, agent); err != nil {
								b.Errorf("start %s: %v", id, err)
								return
							}
							if err := m.StopAgent(ctx, id); err != nil {
								b.Errorf("stop %s: %v", id, err)
								return
							}
						}
					}()
				}
				for idx := 0; idx < poolSize; idx++ {
					ch <- idx
				}
				close(ch)
				wg.Wait()

				cancel()
				_ = m.Stop()
			}
		})
	}
}

// BenchmarkAgentPoolResurrection measures the cost of killing an agent and
// letting the resurrection path rebuild it (RegisterAgent factory).
func BenchmarkAgentPoolResurrection(b *testing.B) {
	for _, poolSize := range []int{16, 64} {
		b.Run(fmt.Sprintf("pool=%d", poolSize), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				m := New(nil, nil, nil)
				ctx, cancel := context.WithCancel(context.Background())
				_ = m.Start(ctx)

				factory := newMockFactory()
				for idx := 0; idx < poolSize; idx++ {
					id := fmt.Sprintf("agent-%d", idx)
					agent := newMockAgent(id)
					m.RegisterAgent(agent, factory.create())
					_ = m.StartAgent(ctx, agent)
				}

				// Kill the pool; resurrection should rebuild from factories.
				for idx := 0; idx < poolSize; idx++ {
					_ = m.StopAgent(ctx, fmt.Sprintf("agent-%d", idx))
				}

				cancel()
				_ = m.Stop()
			}
		})
	}
}
