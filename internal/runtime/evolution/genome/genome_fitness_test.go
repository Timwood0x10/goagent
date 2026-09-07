package genome

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/workflow/engine"
)

// buildGenome constructs a fresh genome of the given name wired to the
// provided evidence store. The store may be nil to exercise the nil-store
// fallback path.
func buildGenome(t *testing.T, name string, store evidence.Store) Genome {
	t.Helper()
	switch name {
	case WorkflowGenomeName:
		dag := buildTestDAG(t)
		cfg := DefaultWorkflowGenomeConfig()
		cfg.EvidenceStore = store
		return NewWorkflowGenome(dag, cfg)
	case RecoveryGenomeName:
		cfg := DefaultRecoveryGenomeConfig()
		cfg.EvidenceStore = store
		return NewRecoveryGenome(&engine.RecoveryPolicy{Strategy: engine.RecoveryRetry}, cfg)
	case KnowledgeGenomeName:
		cfg := DefaultKnowledgeGenomeConfig()
		cfg.EvidenceStore = store
		return NewKnowledgeGenome(nil, cfg)
	case MemoryGenomeName:
		cfg := DefaultMemoryGenomeConfig()
		cfg.EvidenceStore = store
		return NewMemoryGenome(cfg)
	default:
		t.Fatalf("unknown genome %q", name)
		return nil
	}
}

// sourceFor returns the evidence Source filter each genome queries.
func sourceFor(name string) string {
	switch name {
	case RecoveryGenomeName:
		return "recovery"
	default:
		return name
	}
}

func TestGenomeFitness_NoEvidence_Neutral(t *testing.T) {
	for _, name := range []string{
		WorkflowGenomeName, RecoveryGenomeName,
		KnowledgeGenomeName, MemoryGenomeName,
	} {
		t.Run(name, func(t *testing.T) {
			g := buildGenome(t, name, evidence.NewMemoryStore())
			score, err := g.(FitnessGenome).Fitness(context.Background())
			require.NoError(t, err)
			assert.Equal(t, 0.5, score, "no evidence should yield neutral 0.5 fitness")
		})
	}
}

func TestGenomeFitness_NilStore_Neutral(t *testing.T) {
	for _, name := range []string{
		WorkflowGenomeName, RecoveryGenomeName,
		KnowledgeGenomeName, MemoryGenomeName,
	} {
		t.Run(name, func(t *testing.T) {
			g := buildGenome(t, name, nil)
			score, err := g.(FitnessGenome).Fitness(context.Background())
			require.NoError(t, err)
			assert.Equal(t, 0.5, score, "nil store should yield neutral 0.5 fitness")
		})
	}
}

func TestGenomeFitness_AggregatesEvidence(t *testing.T) {
	for _, name := range []string{
		WorkflowGenomeName, RecoveryGenomeName,
		KnowledgeGenomeName, MemoryGenomeName,
	} {
		t.Run(name, func(t *testing.T) {
			store := evidence.NewMemoryStore()
			source := sourceFor(name)

			// Three records under the genome's source → mean 0.6.
			appendFitness(t, store, source, 0.4)
			appendFitness(t, store, source, 0.6)
			appendFitness(t, store, source, 0.8)

			// Evidence from another source must not leak into this genome.
			appendFitness(t, store, "chaos", 0.0)

			g := buildGenome(t, name, store)
			score, err := g.(FitnessGenome).Fitness(context.Background())
			require.NoError(t, err)
			assert.InDelta(t, 0.6, score, 0.0001)
		})
	}
}

// FitnessGenome is implemented by all registered genomes; the assertion
// keeps the cast in tests honest without an extra import.
var _ = FitnessGenome(nil)
