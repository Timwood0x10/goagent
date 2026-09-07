// DAG workflow — dynamic orchestration with the peer Runtime (sdk.Graph).
//
// The legacy internal/workflow spec/Runner engine was retired (fusion plan
// Phase B): the core patterns — conditional edge, linear chain, fan-out +
// join, bounded loop — plus the THREE collaboration modes (delegate /
// pipeline / orchestrate, formerly the agentipc collaboration APIs) are all
// expressed with sdk.Graph on the single kernel execution path.
//
// Ops alternative: any of these shapes can also be submitted over HTTP via
// POST /api/graphs — see examples/28-collab-graphs and
// docs/cookbook/orchestration-modes.md.
//
// Core APIs used (with package paths):
//   - sdk.NewGraph / (*Graph).AddNode / AddEdge / SetRouter / MaxRoundConcurrency — sdk
//   - (*Runtime).RunGraph / GraphResult                                          — sdk
//
// Run:
//
//	go run examples/03-dag-workflow/main.go
package main

import (
	"context"
	"fmt"

	"github.com/Timwood0x10/ares/sdk"
)

func main() {
	ctx := context.Background()
	rt := sdk.NewRuntime(sdk.WithOllama("llama3.2"), sdk.WithTrace(false))
	defer rt.Close()

	conditionalEdge(ctx, rt)
	linearChain(ctx, rt)
	fanOutJoin(ctx, rt)
	boundedLoop(ctx, rt)
	collaborationModes(ctx, rt)

	fmt.Println("\n✅ All DAG workflow demos completed")
}

// conditionalEdge: an edge whose condition is false kills that branch while
// the sibling continues — data-driven routing at the edge level.
func conditionalEdge(ctx context.Context, rt *sdk.Runtime) {
	fmt.Println("\n═══ Conditional Edge (sdk.Graph) ═══")
	g := sdk.NewGraph("cond").
		AddNode("ingest", func(_ context.Context, st map[string]any) error {
			st["large"] = true
			return nil
		}).
		AddNode("skip-me", func(_ context.Context, _ map[string]any) error {
			fmt.Println("  [skip-me] should not run")
			return nil
		}).
		AddNode("chunk", func(_ context.Context, st map[string]any) error {
			fmt.Println("  [chunk] large input → split path taken")
			st["chunks"] = 3
			return nil
		}).
		AddNode("report", func(_ context.Context, st map[string]any) error {
			fmt.Printf("  [report] chunks=%v\n", st["chunks"])
			return nil
		}).
		AddEdge("ingest", "skip-me", func(st map[string]any) bool { return !st["large"].(bool) }).
		AddEdge("ingest", "chunk", nil).
		AddEdge("chunk", "report", nil)
	if _, err := rt.RunGraph(ctx, g); err != nil {
		fmt.Println("  error:", err)
	}
}

// linearChain: strict A→B→C ordering through shared state.
func linearChain(ctx context.Context, rt *sdk.Runtime) {
	fmt.Println("\n═══ Linear DAG (sdk.Graph) ═══")
	g := sdk.NewGraph("chain").
		AddNode("a", appendStep("a")).
		AddNode("b", appendStep("b")).
		AddNode("c", appendStep("c")).
		AddEdge("a", "b", nil).
		AddEdge("b", "c", nil)
	res, err := rt.RunGraph(ctx, g)
	if err != nil {
		fmt.Println("  error:", err)
		return
	}
	order, _ := res.State["order"].([]string)
	fmt.Println("  order:", order)
}

func appendStep(id string) func(context.Context, map[string]any) error {
	return func(_ context.Context, st map[string]any) error {
		prev, _ := st["order"].([]string)
		st["order"] = append(prev, id)
		return nil
	}
}

// fanOutJoin: one root fans out to parallel branches; a join node runs only
// after ALL branches settle (round barrier semantics).
func fanOutJoin(ctx context.Context, rt *sdk.Runtime) {
	fmt.Println("\n═══ Fan-out + Join (sdk.Graph) ═══")
	var done int
	g := sdk.NewGraph("fanout").
		AddNode("root", func(_ context.Context, _ map[string]any) error { return nil }).
		AddNode("b1", func(_ context.Context, _ map[string]any) error { done++; return nil }).
		AddNode("b2", func(_ context.Context, _ map[string]any) error { done++; return nil }).
		AddNode("b3", func(_ context.Context, _ map[string]any) error { done++; return nil }).
		AddNode("join", func(_ context.Context, _ map[string]any) error {
			fmt.Printf("  [join] all %d branches settled\n", done)
			return nil
		}).
		AddEdge("root", "b1", nil).
		AddEdge("root", "b2", nil).
		AddEdge("root", "b3", nil).
		AddEdge("b1", "join", nil).
		AddEdge("b2", "join", nil).
		AddEdge("b3", "join", nil)
	if _, err := rt.RunGraph(ctx, g); err != nil {
		fmt.Println("  error:", err)
	}
}

// boundedLoop: the router re-enters a DONE node as a loop; MaxIterations and
// the router's own counter bound it, then static edges finish the graph.
func boundedLoop(ctx context.Context, rt *sdk.Runtime) {
	fmt.Println("\n═══ Controlled Loop (sdk.Graph router) ═══")
	g := sdk.NewGraph("loop").
		AddNode("start", appendStep("start")).
		AddNode("iter", func(_ context.Context, st map[string]any) error {
			n, _ := st["n"].(int)
			st["n"] = n + 1
			return appendStep(fmt.Sprintf("iter-%d", n))(nil, st)
		}).
		AddNode("exit", appendStep("exit")).
		AddEdge("start", "iter", nil).
		AddEdge("iter", "exit", nil).
		SetRouter(func(_ context.Context, current string, st map[string]any) string {
			if current == "iter" {
				if n, _ := st["n"].(int); n < 2 {
					return "iter"
				}
			}
			return ""
		})
	g.MaxIterations = 8

	res, err := rt.RunGraph(ctx, g)
	if err != nil {
		fmt.Println("  error:", err)
		return
	}
	if n, _ := res.State["n"].(int); n != 2 {
		t2 := fmt.Sprintf("loop ran %d times, want 2", n)
		fmt.Println("  " + t2)
	} else {
		fmt.Println("  loop ran exactly 2 times (router-bounded)")
	}
}

// collaborationModes — the three M1 patterns (ex- agentipc collaboration
// APIs) as pure sdk.Graph shapes:
//
//	delegate    leader → specialists → aggregate   (fan-out + fan-in)
//	pipeline    fetch → transform → store          (linear chain)
//	orchestrate coordinator → workers → join        (fan-out + join)
func collaborationModes(ctx context.Context, rt *sdk.Runtime) {
	fmt.Println("\n═══ Collaboration Modes (delegate / pipeline / orchestrate) ═══")

	// ── Delegate ──
	g := sdk.NewGraph("delegate").
		AddNode("leader", func(_ context.Context, st map[string]any) error {
			st["task"] = "analyze codebase"
			return nil
		}).
		AddNode("spec-a", func(_ context.Context, st map[string]any) error {
			st["result-a"] = "arch: clean"
			return nil
		}).
		AddNode("spec-b", func(_ context.Context, st map[string]any) error {
			st["result-b"] = "tests: 80% coverage"
			return nil
		}).
		AddNode("aggregate", func(_ context.Context, st map[string]any) error {
			a, _ := st["result-a"].(string)
			b, _ := st["result-b"].(string)
			fmt.Printf("  [delegate] %s | %s\n", a, b)
			return nil
		}).
		AddEdge("leader", "spec-a", nil).
		AddEdge("leader", "spec-b", nil).
		AddEdge("spec-a", "aggregate", nil).
		AddEdge("spec-b", "aggregate", nil)
	if _, err := rt.RunGraph(ctx, g); err != nil {
		fmt.Println("  error:", err)
	}

	// ── Pipeline ──
	g = sdk.NewGraph("pipeline").
		AddNode("fetch", func(_ context.Context, st map[string]any) error {
			st["raw"] = "resp-body"
			return nil
		}).
		AddNode("transform", func(_ context.Context, st map[string]any) error {
			raw, _ := st["raw"].(string)
			st["parsed"] = len(raw)
			return nil
		}).
		AddNode("store", func(_ context.Context, st map[string]any) error {
			fmt.Printf("  [pipeline] stored %v bytes\n", st["parsed"])
			return nil
		}).
		AddEdge("fetch", "transform", nil).
		AddEdge("transform", "store", nil)
	if _, err := rt.RunGraph(ctx, g); err != nil {
		fmt.Println("  error:", err)
	}

	// ── Orchestrate ──
	g = sdk.NewGraph("orchestrate").
		AddNode("coordinator", func(_ context.Context, _ map[string]any) error { return nil }).
		AddNode("worker-1", func(_ context.Context, st map[string]any) error {
			st["w1"] = true
			return nil
		}).
		AddNode("worker-2", func(_ context.Context, st map[string]any) error {
			st["w2"] = true
			return nil
		}).
		AddNode("join", func(_ context.Context, st map[string]any) error {
			w1, _ := st["w1"].(bool)
			w2, _ := st["w2"].(bool)
			fmt.Printf("  [orchestrate] join after workers: w1=%v w2=%v\n", w1, w2)
			return nil
		}).
		AddEdge("coordinator", "worker-1", nil).
		AddEdge("coordinator", "worker-2", nil).
		AddEdge("worker-1", "join", nil).
		AddEdge("worker-2", "join", nil)
	if _, err := rt.RunGraph(ctx, g); err != nil {
		fmt.Println("  error:", err)
	}
}
