// Command akg-build — builds an AKG knowledge graph from knowledge_chunks_1024
// using the existing PGProvider + KnowledgeRuntime pipeline. No custom provider.
//
// Usage:
//
//	go run examples/11-knowledge-import/akg/
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/linker"
	"github.com/Timwood0x10/ares/internal/knowledge/planner"
	"github.com/Timwood0x10/ares/internal/knowledge/provider"
	pg "github.com/Timwood0x10/ares/internal/knowledge/provider/postgres"
	khruntime "github.com/Timwood0x10/ares/internal/knowledge/runtime"
)

// postEnricher wraps PGProvider to add type inference and tag extraction.
type postEnricher struct {
	inner *pg.PGProvider
}

func (e *postEnricher) Name() string                           { return e.inner.Name() }
func (e *postEnricher) Close() error                           { return e.inner.Close() }
func (e *postEnricher) IntentMatch(i knowledge.Intent) float64 { return e.inner.IntentMatch(i) }

func (e *postEnricher) Stream(ctx context.Context, intent knowledge.Intent) (<-chan *knowledge.KnowledgeObject, <-chan error) {
	objCh, errCh := e.inner.Stream(ctx, intent)
	out := make(chan *knowledge.KnowledgeObject, 128)
	go func() {
		defer close(out)
		for obj := range objCh {
			if obj == nil {
				continue
			}
			e.enrich(obj)
			if !isGarbage(obj) {
				out <- obj
			}
		}
		// Drain errCh.
		for range errCh {
		}
	}()
	return out, nil
}

func (e *postEnricher) enrich(obj *knowledge.KnowledgeObject) {
	source := ""
	if len(obj.Tags) > 0 {
		source = obj.Tags[0]
	}
	obj.Type = typeFromSource(source)
	obj.Confidence = confidenceFromSource(source)
	obj.Tags = tagsFromSource(source)
	if len(obj.Summary) > 500 {
		obj.Summary = obj.Summary[:500] + "..."
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	dsn := "postgres://postgres:postgres@127.0.0.1:5433/ARES?sslmode=disable"

	// ── 1. Use existing PGProvider ─────────────────────────────
	inner, err := pg.NewPGProvider(dsn, provider.ProviderConfig{
		Name:       "zkp-notes",
		Table:      "knowledge_chunks_1024",
		Namespace:  "zkp",
		IntentTags: []string{"zkp", "snark", "stark", "proof"},
	}, provider.ColumnMapping{
		IDColumn:      "id",
		SummaryColumn: "content",
		ContentColumn: "content",
		TagColumn:     "source",
		TimeColumn:    "created_at",
	})
	if err != nil {
		return fmt.Errorf("PGProvider: %w", err)
	}
	defer func() { _ = inner.Close() }()

	reg := provider.NewProviderRegistry()
	if err := reg.Register(&postEnricher{inner: inner}); err != nil {
		return fmt.Errorf("register: %w", err)
	}

	// Pipeline without LLM-dependent stages.
	pipe := knowledge.NewKnowledgePipeline(nil, nil, nil, nil)

	rt := khruntime.New(
		planner.NewKnowledgePlanner(),
		planner.NewSourceDiscovery(reg, planner.NewQueryPlanner()),
		reg,
		pipe,
		[]khruntime.Linker{
			&khruntime.DefaultLinker{},
			&linker.DecisionLinker{},
			&linker.ArchitectureLinker{},
			&linker.TimelineLinker{},
			&linker.SimilarityLinker{},
		},
		[]khruntime.Reducer{&khruntime.DefaultReducer{}},
	)

	start := time.Now()
	fmt.Println("Building AKG from knowledge_chunks_1024...")
	graph, err := rt.Execute(ctx, "zero knowledge proof",
		knowledge.TokenBudget{MaxTokens: 50000, ForGraph: 30000},
		&khruntime.Config{MaxConcurrentProviders: 10},
	)
	if err != nil {
		return fmt.Errorf("execute: %w", err)
	}
	elapsed := time.Since(start)

	// ── Output ───────────────────────────────────────────────
	typeCount := make(map[knowledge.ObjectType]int)
	for _, obj := range graph.Nodes {
		typeCount[obj.Type]++
	}
	edgeTypes := make(map[string]int)
	for _, e := range graph.Edges {
		edgeTypes[e.Name]++
	}

	fmt.Printf("\n═══ AKG Graph ═══\n")
	fmt.Printf("  Nodes: %d, Edges: %d  (%v)\n", len(graph.Nodes), len(graph.Edges), elapsed)
	fmt.Println()

	fmt.Println("Node types:")
	for t, c := range typeCount {
		fmt.Printf("  %-20s %4d\n", t, c)
	}
	fmt.Println()
	fmt.Println("Edge types:")
	for name, count := range edgeTypes {
		fmt.Printf("  %-20s %4d\n", name, count)
	}
	fmt.Println()

	// Show sample relations.
	fmt.Println("═══ Sample (first 6 nodes) ═══")
	displayed := 0
	for id, obj := range graph.Nodes {
		if displayed >= 6 {
			break
		}
		summary := obj.Summary
		if len(summary) > 60 {
			summary = summary[:60] + "..."
		}
		fmt.Printf("\n◉ %s (%s, conf=%.2f) [%s]\n  %s\n", id, obj.Type, obj.Confidence, strings.Join(obj.Tags, ","), summary)
		edgeCount := 0
		for _, e := range graph.Edges {
			if edgeCount >= 8 {
				break
			}
			if e.From == id || e.To == id {
				other := e.To
				dir := "▶"
				if e.To == id {
					other = e.From
					dir = "◀"
				}
				if o, ok := graph.Nodes[other]; ok && o != nil {
					oS := o.Summary
					if len(oS) > 40 {
						oS = oS[:40] + "..."
					}
					fmt.Printf("  %s──[%s/%.2f]──%s %s\n", dir, e.Name, e.Score, other, oS)
				}
				edgeCount++
			}
		}
		displayed++
	}

	return nil
}

// ── helpers ──────────────────────────────────────────────────────────────

func typeFromSource(source string) knowledge.ObjectType {
	lower := strings.ToLower(source)
	switch {
	case matchAny(lower, "snark-relation", "groth16", "plonk", "spartan", "bulletproof", "bellman", "zk-snark"):
		return knowledge.ObjectArchitecture
	case matchAny(lower, "stark-relation", "zk-stark", "folding", "binius", "lookup"):
		return knowledge.ObjectDecision
	case matchAny(lower, "hardware", "fpga", "gpu", "asic", "icicle", "vpu"):
		return knowledge.ObjectArchitecture
	case matchAny(lower, "basic-math", "elliptic", "polynomial", "finite-field",
		"number-theory", "lattice", "information-theory", "hash-function",
		"commitment", "pairing", "fft", "group-theory", "complexity"):
		return knowledge.ObjectMemory
	case matchAny(lower, "emerging", "advanced-protocol", "zkml", "ai"):
		return knowledge.ObjectDocument
	case matchAny(lower, "personally", "think"):
		return knowledge.ObjectDecision
	case matchAny(lower, "protocol", "another-things", "signature", "hardware"):
		return knowledge.ObjectDocument
	default:
		return knowledge.ObjectMemory
	}
}

func matchAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if matchWord(s, sub) {
			return true
		}
	}
	return false
}

func matchWord(s, sub string) bool {
	idx := strings.Index(s, sub)
	if idx < 0 {
		return false
	}
	if idx > 0 {
		c := s[idx-1]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			return false
		}
	}
	end := idx + len(sub)
	if end < len(s) {
		c := s[end]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			return false
		}
	}
	return true
}

func tagsFromSource(source string) []string {
	parts := strings.Split(strings.ToLower(strings.Trim(source, "/")), "/")
	var tags []string
	seen := make(map[string]bool)
	for _, p := range parts {
		p = strings.TrimSuffix(p, ".md")
		p = strings.TrimSuffix(p, ".markdown")
		if p == "" || seen[p] {
			continue
		}
		if isGeneric(p) {
			continue
		}
		seen[p] = true
		tags = append(tags, p)
		if len(tags) >= 4 {
			break
		}
	}
	return tags
}

func isGeneric(p string) bool {
	return map[string]bool{
		"notes_for_latest": true, "documents": true, "notes": true,
		"protocol": true, "zkp": true, "users": true, "scc": true,
	}[p]
}

func confidenceFromSource(source string) float64 {
	parts := strings.Split(strings.Trim(source, "/"), "/")
	switch {
	case len(parts) >= 5:
		return 0.90
	case len(parts) >= 4:
		return 0.85
	case len(parts) >= 3:
		return 0.80
	case len(parts) >= 2:
		return 0.75
	default:
		return 0.70
	}
}

func isGarbage(obj *knowledge.KnowledgeObject) bool {
	if obj == nil || obj.Summary == "" || len(obj.Summary) < 10 {
		return true
	}
	lower := strings.ToLower(obj.Summary)
	switch {
	case strings.Contains(lower, "read_directory"), strings.Contains(lower, "import_knowledge"),
		strings.HasPrefix(lower, "error:"), strings.HasPrefix(lower, "[tool"):
		return true
	}
	return false
}
