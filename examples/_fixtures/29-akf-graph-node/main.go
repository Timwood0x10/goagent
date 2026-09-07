// AKF as a sdk.Graph node (BETA) — run ARES Knowledge Fabric steps inside a
// DAG without an LLM call.
//
// The retired workflow engine expected base.Agent steps (NewKnowledgeAgent).
// The current DAG surface is sdk.Graph, whose nodes are *sdk.Agent, pure
// func(ctx, state) error nodes, or nested graphs. KnowledgeAgent.AsGraphNode()
// adapts the AKF build_graph / compile steps to the pure-function node shape:
// it reads state["input"] and writes the step result to state["output"], so an
// AKF step composes with other graph nodes on the single orchestration path.
//
// This is a BETA integration (internal/knowledge/workflow): the adapter API may
// change between releases.
//
// Core APIs used (with package paths):
//   - workflow.NewKnowledgeAgent / (*KnowledgeAgent).AsGraphNode — internal/knowledge/workflow
//   - sdk.NewGraph / AddNode / AddEdge / (*Runtime).RunGraph      — sdk
//
// Run:
//
//	go run examples/29-akf-graph-node/main.go
package main

import (
	"context"
	"fmt"

	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/compiler"
	"github.com/Timwood0x10/ares/internal/knowledge/planner"
	"github.com/Timwood0x10/ares/internal/knowledge/provider"
	"github.com/Timwood0x10/ares/internal/knowledge/runtime"
	"github.com/Timwood0x10/ares/internal/knowledge/workflow"
	"github.com/Timwood0x10/ares/sdk"
)

func main() {
	ctx := context.Background()

	// Build a minimal KnowledgeRuntime with a stub provider so the demo runs
	// without a real knowledge base or LLM.
	reg := provider.NewProviderRegistry()
	_ = reg.Register(&stubProvider{objects: []string{
		"Chose PostgreSQL for persistence",
		"Redis used as cache layer",
	}})
	sd := planner.NewSourceDiscovery(reg, &stubPlanner{})
	rt := runtime.New(planner.NewKnowledgePlanner(), sd, reg, nil, nil, nil)
	comp := compiler.NewDefaultCompiler()

	// 1. AKF build_graph step as a graph node.
	buildAgent := workflow.NewKnowledgeAgent("akf-build", rt, comp, workflow.StepConfig{
		Step: workflow.StepBuildGraph,
		Goal: "pick a persistence layer",
	})

	// 2. A downstream pure-function node consumes the AKF output.
	graph := sdk.NewGraph("akf-demo").
		AddNode("akf", buildAgent.AsGraphNode()).
		AddNode("report", func(_ context.Context, st map[string]any) error {
			if _, ok := st["output"]; ok {
				fmt.Println("  [report] AKF graph produced a WorkingGraph")
			} else {
				fmt.Println("  [report] no AKF output present")
			}
			return nil
		}).
		AddEdge("akf", "report", nil)

	// Function-node graphs need no Runtime wiring for the kernel path; RunGraph
	// executes them inline. An sdk.Runtime is still required to call RunGraph.
	sdkRt := sdk.NewRuntime(sdk.WithOllama("llama3.2"), sdk.WithTrace(false))
	defer sdkRt.Close()

	res, err := sdkRt.RunGraph(ctx, graph)
	if err != nil {
		fmt.Printf("❌ RunGraph: %v\n", err)
		return
	}
	// The meaningful signal is whether AKF actually wrote state["output"], not
	// that the akf node "ran" (every completed fn node is recorded in
	// NodeResults regardless of what it produced).
	_, akfProduced := res.State["output"]
	_, reportRan := res.NodeResults["report"]
	fmt.Printf("✅ AKF graph node executed: akf_output=%v report=%v\n", akfProduced, reportRan)
}

// stubProvider streams canned knowledge objects so Execute has something to
// link; enough to exercise the graph-node path without a real KB.
type stubProvider struct{ objects []string }

func (p *stubProvider) Name() string { return "stub" }
func (p *stubProvider) IntentMatch(knowledge.Intent) float64 {
	return 0.9
}
func (p *stubProvider) Stream(ctx context.Context, _ knowledge.Intent) (<-chan *knowledge.KnowledgeObject, <-chan error) {
	ch := make(chan *knowledge.KnowledgeObject, len(p.objects))
	errCh := make(chan error, 1)
	go func() {
		defer close(ch)
		defer close(errCh)
		for _, o := range p.objects {
			select {
			case <-ctx.Done():
				return
			case ch <- &knowledge.KnowledgeObject{ID: o, Summary: o}:
			}
		}
	}()
	return ch, errCh
}

type stubPlanner struct{}

func (s *stubPlanner) PlanQuery(_ context.Context, req planner.KnowledgeRequirement, _, _ string) (*planner.QueryPlan, error) {
	return &planner.QueryPlan{Query: req.Description, QueryType: planner.QuerySQL, MaxResults: 5}, nil
}
