package introspect

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/fabric/task"
	"github.com/Timwood0x10/ares/internal/kernel"
)

// TestChaosReporterSnapshot verifies the reporter merges config, shadow and
// live state into one coherent frame (monitoring.md #12 Phase 3).
func TestChaosReporterSnapshot(t *testing.T) {
	r := NewChaosReporter()
	// Not yet configured: everything zero.
	cs := r.Snapshot()
	if cs.Enabled || cs.Mode != "" || !cs.Shadow.LastRun.IsZero() || cs.Live.Injections != 0 {
		t.Fatalf("initial snapshot = %+v", cs)
	}

	r.SetConfig(true, "shadow")
	r.RecordShadow(ShadowResult{LastRun: time.Unix(10, 0), Events: 6, Recovered: true})
	cs = r.Snapshot()
	if !cs.Enabled || cs.Mode != "shadow" {
		t.Fatalf("config not recorded: %+v", cs)
	}
	if !cs.Shadow.Recovered || cs.Shadow.Events != 6 || cs.Shadow.LastRun.Unix() != 10 {
		t.Fatalf("shadow not recorded: %+v", cs.Shadow)
	}

	// Live flags + counter merge independently: AddInjection must not clobber
	// the flags written by SetLive, and the counter must accumulate.
	r.SetLive(LiveChaosState{Active: true, FailSafeTripped: true})
	r.AddInjection(time.Unix(20, 0))
	r.AddInjection(time.Unix(21, 0))
	cs = r.Snapshot()
	if !cs.Live.Active || !cs.Live.FailSafeTripped {
		t.Fatalf("live flags clobbered: %+v", cs.Live)
	}
	if cs.Live.Injections != 2 || cs.Live.LastInjection.Unix() != 21 {
		t.Fatalf("live counter/last: %+v", cs.Live)
	}
}

// TestChaosReporterConcurrent hammers the reporter from multiple goroutines to
// lock the atomic-write contract under -race (chaos loops report while the
// collector reads).
func TestChaosReporterConcurrent(t *testing.T) {
	r := NewChaosReporter()
	r.SetConfig(true, "live")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				r.SetLive(LiveChaosState{Active: true})
				r.RecordShadow(ShadowResult{Recovered: true, LastRun: time.Now()})
				r.AddInjection(time.Now())
				_ = r.Snapshot()
			}
		}()
	}
	wg.Wait()

	cs := r.Snapshot()
	if cs.Live.Injections != 8*500 {
		t.Fatalf("injections = %d, want %d", cs.Live.Injections, 8*500)
	}
}

// TestCollectorChaosSource verifies the chaos source lands in the snapshot
// when present and is omitted when nil.
func TestCollectorChaosSource(t *testing.T) {
	withChaos := NewCollector(Sources{
		Kernel: func() kernel.SchedulerSnapshot { return kernel.SchedulerSnapshot{} },
		Fabric: func() []taskfabric.LeaseEntry { return nil },
		Chaos:  func() ChaosStatus { return ChaosStatus{Enabled: true, Mode: "shadow"} },
	})
	snap := withChaos.Collect()
	if snap.Chaos == nil || !snap.Chaos.Enabled || snap.Chaos.Mode != "shadow" {
		t.Fatalf("chaos domain not mapped: %+v", snap.Chaos)
	}

	noChaos := NewCollector(Sources{
		Kernel: func() kernel.SchedulerSnapshot { return kernel.SchedulerSnapshot{} },
		Fabric: func() []taskfabric.LeaseEntry { return nil },
	})
	if noChaos.Collect().Chaos != nil {
		t.Fatal("nil chaos source must omit the field")
	}
}

// TestRunShadowSandboxRecovers verifies the shadow sandbox replay records a
// recovery outcome. The scratch-fabric chain kill→lease-expire→recover must
// leave the task recovered, not errored. (Moved from dashboard_test.go in
// M4-D with runShadowSandbox; the Dashboard runtime is gone.)
func TestRunShadowSandboxRecovers(t *testing.T) {
	res := runShadowSandbox(context.Background())
	if res.Errored {
		t.Fatalf("shadow sandbox errored: %+v", res)
	}
	if !res.Recovered {
		t.Fatalf("shadow sandbox did not recover: %+v", res)
	}
	if res.LastRun.IsZero() {
		t.Fatalf("shadow sandbox did not stamp LastRun: %+v", res)
	}
}

// TestDashboardChaosReporterWired verifies the chaos reporter feeds the
// collector Sources, so shadow-loop RecordShadow output stays observable
// (P0-2 regression guard, kept from dashboard_test.go in M4-D).
func TestDashboardChaosReporterWired(t *testing.T) {
	r := NewChaosReporter()
	r.SetConfig(true, "shadow")
	c := NewCollector(Sources{Chaos: r.Snapshot})
	r.RecordShadow(ShadowResult{LastRun: time.Now(), Recovered: true})
	snap := c.Collect()
	if snap.Chaos == nil || !snap.Chaos.Enabled || !snap.Chaos.Shadow.Recovered {
		t.Fatalf("chaos source not wired into collector: %+v", snap.Chaos)
	}
}
