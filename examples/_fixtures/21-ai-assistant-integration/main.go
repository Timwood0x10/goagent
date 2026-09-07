// Command 21-ai-assistant-integration demonstrates a complete AI assistant
// knowledge integration using the public api/knowledge packages and the
// KnowledgeService exposed by the service adapter.
//
// Purpose:
//
//	Show how an AI assistant connects to the Ares Knowledge Fabric — building
//	a cognitive graph from a user intent, compiling it into markdown for LLM
//	consumption, and distilling raw conversation memory into structured
//	KnowledgeObjects. Every step uses only the public api/knowledge surface;
//	the single internal import (service.ServiceAdapter) is the sanctioned
//	bridge from the public interface to the internal KnowledgeRuntime.
//
// Learning objectives:
//   - Understand the four core operations of KnowledgeService: BuildGraph,
//     CompileContext, Query, and Distill.
//   - See how Intent + TokenBudget drive graph construction.
//   - Observe how a manually constructed WorkingGraph is compiled into
//     token-efficient markdown.
//   - Watch raw bytes being distilled into a typed KnowledgeObject.
//   - Learn the guard errors: ErrNilIntent, ErrNilGraph, ErrEmptyTenantID.
//
// Core APIs used:
//   - github.com/Timwood0x10/ares/api/knowledge
//     Intent, TokenBudget, WorkingGraph, KnowledgeObject, ObjectType
//   - github.com/Timwood0x10/ares/internal/knowledge/runtime
//     runtime.New() — constructs a KnowledgeRuntime (planner, discovery,
//     registry, pipeline, linkers, reducers; all nil for this demo).
//   - github.com/Timwood0x10/ares/internal/knowledge/service
//     service.NewServiceAdapter(rt) — adapts the internal runtime to the
//     public KnowledgeService interface.
//
// Run:
//
//	go run examples/21-ai-assistant-integration/main.go
//
// Expected output (order is deterministic):
//
//	BuildGraph returned (expected with nil planner): runtime: planner is not configured
//	Compiled context:
//	- decision-42 (decision): Redis chosen for sub-ms latency and TTL eviction.
//	Distilled 1 KnowledgeObject(s)
//	  - id=distilled-<n> type=memory ns=tenant-1 raw_len=<n>
//	AI assistant integration example completed.
//
// Configuration points to try:
//   - Set intent.Goal to "" to trigger ErrNilIntent.
//   - Change intent.Budget.MaxTokens / ForGraph to see different token
//     allocations (no visible effect until a real planner is wired).
//   - Add more nodes to demoGraph and observe the compiled markdown growing.
//   - Pass an empty tenantID ("") to Distill to trigger ErrEmptyTenantID.
//   - Pass empty rawMemory ([]byte{}) to Distill — it returns nil, nil.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	apiknowledge "github.com/Timwood0x10/ares/api/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/runtime"
	"github.com/Timwood0x10/ares/internal/knowledge/service"
)

// exitf logs a formatted message and exits with code 1, canceling the
// context first to avoid the gocritic exitAfterDefer warning.
func exitf(cancel context.CancelFunc, format string, args ...any) {
	cancel()
	log.Printf(format+"\n", args...)
	os.Exit(1)
}

func main() {
	// Create a context with a 30-second timeout so no demo step can hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// ── Step 1: Construct the internal KnowledgeRuntime ──
	// runtime.New() takes six optional dependencies:
	// planner, discovery, registry, pipeline, linkers, reducers.
	// Passing all nil means BuildGraph will fail (planner is nil), but
	// CompileContext and Distill do not depend on the planner and work fine.
	// This is the shallowest way to obtain a runtime for adapter wiring.
	rt := runtime.New(nil, nil, nil, nil, nil, nil)

	// ── Step 2: Adapt the runtime to the public KnowledgeService ──
	// service.NewServiceAdapter(rt) wraps the internal runtime in a
	// KnowledgeService-compatible adapter. It returns an error only when
	// rt is nil — here rt is non-nil so the call always succeeds.
	// After adapting, the four public operations become available:
	// svc.BuildGraph / CompileContext / Query / Distill.
	svc, err := service.NewServiceAdapter(rt)
	if err != nil {
		exitf(cancel, "create knowledge service: %v", err)
	}

	// ── Step 3: Define a user intent with a token budget ──
	// Intent describes "what knowledge the user wants" plus constraints.
	// Goal is the query target; an empty Goal triggers ErrNilIntent.
	// TokenBudget splits token usage: MaxTokens is the total ceiling and
	// ForGraph is the sub-budget allocated to the graph context.
	intent := apiknowledge.Intent{
		Goal: "Why did we choose Redis for caching?", // non-empty → passes guard
		Budget: apiknowledge.TokenBudget{
			MaxTokens: 4096, // total token budget for this intent
			ForGraph:  2048, // tokens reserved for the graph context
		},
	}

	// ── Step 4: Build a knowledge graph from the intent ──
	// svc.BuildGraph(ctx, intent) delegates to runtime.Execute(goal, budget).
	// With a nil planner, runtime.Execute returns "planner is not configured".
	// This step intentionally uses a stub runtime so the error propagation
	// path of BuildGraph is visible. BuildGraph also validates intent.Goal
	// before calling Execute, returning ErrNilIntent when Goal is empty.
	graph, err := svc.BuildGraph(ctx, intent)
	if err != nil {
		// Expected in this stub: the runtime has no planner wired.
		fmt.Printf("BuildGraph returned (expected with nil planner): %v\n", err)
	} else {
		fmt.Printf("Built graph with %d nodes\n", len(graph.Nodes))
	}

	// ── Step 5: Compile a manually constructed graph into markdown ──
	// Since Step 4 produced no real graph (no planner), construct a
	// WorkingGraph by hand to demonstrate CompileContext.
	// demoGraph holds one decision-typed KnowledgeObject with ID
	// "decision-42" and a summary explaining the Redis choice.
	demoGraph := &apiknowledge.WorkingGraph{
		Nodes: map[string]*apiknowledge.KnowledgeObject{
			"decision-42": {
				ID:      "decision-42",
				Type:    apiknowledge.ObjectDecision, // decision object type
				Summary: "Redis chosen for sub-ms latency and TTL eviction.",
			},
		},
	}

	// svc.CompileContext(ctx, demoGraph) renders the graph as markdown,
	// emitting one bullet per node: "- <id> (<type>): <summary>".
	// Passing a nil graph returns ErrNilGraph.
	compiled, err := svc.CompileContext(ctx, demoGraph)
	if err != nil {
		exitf(cancel, "compile context: %v", err)
	}
	fmt.Println("Compiled context:")
	fmt.Println(compiled)

	// ── Step 6: Distill raw conversation memory into KnowledgeObjects ──
	// svc.Distill(ctx, rawMemory, tenantID) converts raw bytes into a
	// structured KnowledgeObject. The current implementation wraps the bytes
	// into a single ObjectMemory-typed object: ID is "distilled-<raw_len>",
	// Namespace is the tenantID, and Raw preserves the original bytes.
	// Guard behavior: empty tenantID → ErrEmptyTenantID; empty rawMemory →
	// (nil, nil).
	rawMemory := []byte("User asked why we use Redis. Answer: latency + TTL.")
	objs, err := svc.Distill(ctx, rawMemory, "tenant-1") // tenant scoping
	if err != nil {
		exitf(cancel, "distill: %v", err)
	}
	fmt.Printf("Distilled %d KnowledgeObject(s)\n", len(objs))
	for _, o := range objs {
		// Print the key fields of each distilled KnowledgeObject.
		fmt.Printf("  - id=%s type=%s ns=%s raw_len=%d\n",
			o.ID, o.Type, o.Namespace, len(o.Raw))
	}

	fmt.Println("AI assistant integration example completed.")
}
