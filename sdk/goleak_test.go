package sdk

import (
	"context"
	"testing"

	"go.uber.org/goleak"

	"github.com/Timwood0x10/ares/internal/agentsyscall"
)

// goleakOptions lists the goroutines that legitimately outlive a Runtime.
//
// Every entry is a library-owned goroutine whose lifetime is not tied to our
// Close(), verified by reading the leak report's stacks. The list is
// deliberately enumerated rather than collapsed into goleak.IgnoreCurrent():
// IgnoreCurrent would snapshot whatever is running at test start and thereby
// whitelist a real leak introduced by earlier code in the same test binary.
//
// TODO(tech-debt): four hand-written runtime.NumGoroutine() comparisons predate
// this file (internal/ares_bootstrap/closure_lifecycle_test.go,
// internal/ares_events/memory_store_close_test.go,
// internal/ares_mcp/transport_test.go). They only produce a count, never a
// stack, and are sensitive to unrelated parallel tests. Migrate them to goleak.
func goleakOptions() []goleak.Option {
	return []goleak.Option{
		// modernc.org/sqlite (via the knowledge sqlite store) keeps a package
		// -level worker alive for the process lifetime.
		goleak.IgnoreTopFunction("modernc.org/sqlite.(*worker).run"),
		// database/sql's pool goroutines exit on db.Close(), which the pool
		// owner performs asynchronously.
		goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"),
		goleak.IgnoreTopFunction("database/sql.(*DB).connectionResetter"),
		// OpenTelemetry's batch span processor flushes on its own schedule.
		goleak.IgnoreTopFunction("go.opentelemetry.io/otel/sdk/trace.(*batchSpanProcessor).processQueue"),
		// net/http keeps idle-connection readers around until the transport is
		// closed; the LLM client's transport is shared process-wide.
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
	}
}

// TestRuntimeCloseLeaksNoGoroutines is the Close() acceptance: after Close() the
// Runtime must leave no goroutine of its own behind.
//
// It covers the scheduler drain loop, the syscall kernel, and the background
// errgroup — ensureScheduler starts all three, and Close() is the only thing
// that stops them. Before this test the guarantee was asserted only by prose
// in the docs; the repo had no goleak dependency at all.
func TestRuntimeCloseLeaksNoGoroutines(t *testing.T) {
	defer goleak.VerifyNone(t, goleakOptions()...)

	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	// ensureScheduler is what spawns the drain loop (sdk/scheduler.go) and
	// wires the syscall kernel; without it Close() would have nothing to stop
	// and the test would pass vacuously.
	rt.ensureScheduler()
	if rt.syscallKernel == nil {
		t.Fatal("ensureScheduler must wire the syscall kernel")
	}
	rt.Close()
}

// TestPlanLoopCloseLeaksNoGoroutines covers the plan-loop driver specifically.
//
// A plan loop is the one Runtime-owned goroutine a user can start indirectly
// (through the create_plan tool's `loop` option) and therefore the easiest to
// leak: it outlives the call that created it by design. The serve/SDK
// lifecycle contract required this to be
// asserted with goleak or equivalent; this is that assertion.
func TestPlanLoopCloseLeaksNoGoroutines(t *testing.T) {
	defer goleak.VerifyNone(t, goleakOptions()...)

	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	rt.ensureScheduler()

	// A loop plan with no capable executor still starts its driver goroutine —
	// which is exactly the goroutine under test. CreatePlan may return an error
	// from the scheduling stage; that is irrelevant here, the leak check is.
	_, _ = rt.syscallKernel.CreatePlan(context.Background(), agentsyscall.CreatePlanArgs{
		Steps: []agentsyscall.PlanStepArgs{{ID: "s1", Capability: "analysis"}},
		Loop:  &agentsyscall.PlanLoopArgs{MaxRounds: 3, Until: "all_succeeded"},
	})

	rt.Close()
}

// TestStopPlanLoopLeaksNoGoroutines asserts the explicit control surface, not
// just the blanket Close(): an embedder that stops one loop and keeps running
// must not accumulate goroutines.
func TestStopPlanLoopLeaksNoGoroutines(t *testing.T) {
	defer goleak.VerifyNone(t, goleakOptions()...)

	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	rt.ensureScheduler()

	_, _ = rt.syscallKernel.CreatePlan(context.Background(), agentsyscall.CreatePlanArgs{
		Steps: []agentsyscall.PlanStepArgs{{ID: "s1", Capability: "analysis"}},
		Loop:  &agentsyscall.PlanLoopArgs{MaxRounds: 3, Until: "all_succeeded"},
	})
	for _, id := range rt.LivePlanLoops() {
		if err := rt.StopPlanLoop(id); err != nil {
			t.Fatalf("StopPlanLoop(%q): %v", id, err)
		}
	}
	rt.Close()
}
