package evolution

import (
	"context"
	"testing"

	"github.com/Timwood0x10/ares/internal/agents"
	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// newGATestProfileStore seeds a profile store whose stable instructions for
// "coder" are the GA parent.
func newGATestProfileStore(t *testing.T, instructions string) *ProfileStore {
	t.Helper()
	store := NewProfileStore()
	profile := &agents.AgentProfile{
		Role:         "coder",
		Instructions: instructions,
	}
	if err := store.Update(profile); err != nil {
		t.Fatalf("update profile: %v", err)
	}
	if err := store.SetStable("coder", profile); err != nil {
		t.Fatalf("set stable profile: %v", err)
	}
	return store
}

// newGATestGenerator builds a GA generator with a deterministic mutator over
// a fixed prompt pool so tests do not depend on random seeding.
func newGATestGenerator(t *testing.T, profileStore *ProfileStore) *GAGenerator {
	t.Helper()
	pool := []string{
		"Solve the task step by step and verify the result.",
		"Write clean code and double-check edge cases.",
		"Explain your reasoning before giving the final answer.",
	}
	m, err := mutation.NewMutator(
		mutation.WithPromptPool(pool),
		mutation.WithSeed(42),
	)
	if err != nil {
		t.Fatalf("build mutator: %v", err)
	}
	g, err := NewGAGenerator(profileStore, WithGAMutator(m), WithGAMaxAttempts(200))
	if err != nil {
		t.Fatalf("NewGAGenerator: %v", err)
	}
	return g
}

func TestNewGAGenerator_Validation(t *testing.T) {
	t.Run("nil profile store rejected", func(t *testing.T) {
		_, err := NewGAGenerator(nil, WithGAPromptPool([]string{"p1"}))
		if err == nil {
			t.Fatal("NewGAGenerator with nil profile store should error")
		}
	})

	t.Run("no pool and no mutator rejected", func(t *testing.T) {
		_, err := NewGAGenerator(newGATestProfileStore(t, "stable"))
		if err == nil {
			t.Fatal("NewGAGenerator without pool or mutator should error")
		}
	})

	t.Run("prompt pool builds a mutator", func(t *testing.T) {
		g, err := NewGAGenerator(newGATestProfileStore(t, "stable"), WithGAPromptPool([]string{"p1"}))
		if err != nil {
			t.Fatalf("NewGAGenerator with prompt pool: %v", err)
		}
		if g.mutator == nil {
			t.Fatal("prompt pool should build a mutator")
		}
	})
}

func TestGAGenerator_Generate(t *testing.T) {
	t.Run("produces distinct mutated candidates", func(t *testing.T) {
		store := newGATestProfileStore(t, "Add the numbers precisely and return the numeric result only.")
		g := newGATestGenerator(t, store)

		candidates, err := g.Generate(context.Background(), "coder", []string{"ev-1", "ev-2"}, 3)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if len(candidates) == 0 {
			t.Fatal("Generate returned no candidates")
		}
		if len(candidates) > 3 {
			t.Errorf("Generate returned %d candidates, want <= 3", len(candidates))
		}

		seen := make(map[string]bool, len(candidates))
		for i, c := range candidates {
			if c.Kind != CandidateInstruction {
				t.Errorf("candidate %d kind = %v, want CandidateInstruction", i, c.Kind)
			}
			if c.TargetRole != "coder" {
				t.Errorf("candidate %d role = %q, want coder", i, c.TargetRole)
			}
			if c.Diff == "" || c.Diff == "Add the numbers precisely and return the numeric result only." {
				t.Errorf("candidate %d diff did not mutate the stable instructions: %q", i, c.Diff)
			}
			if c.Reason == "" {
				t.Errorf("candidate %d has empty reason", i)
			}
			if len(c.EvidenceIDs) != 2 {
				t.Errorf("candidate %d evidence IDs = %v, want the 2 provided", i, c.EvidenceIDs)
			}
			if seen[c.Diff] {
				t.Errorf("candidate %d duplicate diff: %q", i, c.Diff)
			}
			seen[c.Diff] = true
		}
	})

	t.Run("no stable profile errors", func(t *testing.T) {
		store := NewProfileStore() // no stable profile for coder
		g := newGATestGenerator(t, store)
		_, err := g.Generate(context.Background(), "coder", []string{"ev-1", "ev-2"}, 2)
		if err == nil {
			t.Fatal("Generate without stable profile should error")
		}
	})

	t.Run("empty evidence rejected", func(t *testing.T) {
		g := newGATestGenerator(t, newGATestProfileStore(t, "stable"))
		_, err := g.Generate(context.Background(), "coder", nil, 2)
		if err == nil {
			t.Fatal("Generate with empty evidence should error")
		}
	})

	t.Run("empty role rejected", func(t *testing.T) {
		g := newGATestGenerator(t, newGATestProfileStore(t, "stable"))
		_, err := g.Generate(context.Background(), "", []string{"ev-1"}, 2)
		if err == nil {
			t.Fatal("Generate with empty role should error")
		}
	})

	t.Run("non-positive count rejected", func(t *testing.T) {
		g := newGATestGenerator(t, newGATestProfileStore(t, "stable"))
		_, err := g.Generate(context.Background(), "coder", []string{"ev-1"}, 0)
		if err == nil {
			t.Fatal("Generate with count 0 should error")
		}
	})
}

// seedFailureEvidenceWithIDs appends failure-cluster evidence records with
// explicit IDs for a role (distinct from seedFailureEvidence, which generates
// n anonymous records).
func seedFailureEvidenceWithIDs(t *testing.T, store evidence.Store, role string, ids []string) {
	t.Helper()
	for _, id := range ids {
		rec := evidence.NewEvidence("result_verifier", evidence.KindDimensionEval,
			map[string]any{"verdict": "fail"},
			evidence.WithMetadata("role", role),
		)
		rec.ID = id
		if err := store.Append(context.Background(), rec); err != nil {
			t.Fatalf("append evidence %s: %v", id, err)
		}
	}
}

func TestDiagnoser_GenerateGA(t *testing.T) {
	t.Run("cluster below threshold returns nil", func(t *testing.T) {
		evStore := evidence.NewMemoryStore()
		seedFailureEvidenceWithIDs(t, evStore, "coder", []string{"ev-1"}) // only 1 failure

		g := newGATestGenerator(t, newGATestProfileStore(t, "stable"))
		d := NewDiagnoserWithOptions(evStore, WithGAGenerator(g))

		candidates, err := d.GenerateGA(context.Background(), "coder", 2)
		if err != nil {
			t.Fatalf("GenerateGA: %v", err)
		}
		if candidates != nil {
			t.Errorf("GenerateGA with 1 failure should return nil, got %d candidates", len(candidates))
		}
	})

	t.Run("generates GA candidates on a real cluster", func(t *testing.T) {
		evStore := evidence.NewMemoryStore()
		seedFailureEvidenceWithIDs(t, evStore, "coder", []string{"ev-1", "ev-2"})

		store := newGATestProfileStore(t, "Add the numbers precisely and return the numeric result only.")
		g := newGATestGenerator(t, store)
		d := NewDiagnoserWithOptions(evStore, WithGAGenerator(g))

		candidates, err := d.GenerateGA(context.Background(), "coder", 2)
		if err != nil {
			t.Fatalf("GenerateGA: %v", err)
		}
		if len(candidates) == 0 {
			t.Fatal("GenerateGA returned no candidates for a valid cluster")
		}
		for _, c := range candidates {
			if len(c.EvidenceIDs) != 2 {
				t.Errorf("candidate evidence IDs = %v, want the 2 cluster IDs", c.EvidenceIDs)
			}
			if c.Diff == "" || c.Diff == "Add the numbers precisely and return the numeric result only." {
				t.Errorf("candidate diff did not mutate stable: %q", c.Diff)
			}
		}
	})

	t.Run("missing GA generator errors", func(t *testing.T) {
		evStore := evidence.NewMemoryStore()
		seedFailureEvidenceWithIDs(t, evStore, "coder", []string{"ev-1", "ev-2"})

		d := NewDiagnoser(evStore) // no GA generator
		_, err := d.GenerateGA(context.Background(), "coder", 2)
		if err == nil {
			t.Fatal("GenerateGA without a GA generator should error")
		}
	})

	t.Run("non-positive count rejected regardless of cluster size", func(t *testing.T) {
		// n <= 0 must error consistently at the boundary: the small-cluster
		// path must NOT mask an invalid argument as a successful no-candidate
		// outcome.
		for name, ids := range map[string][]string{
			"small cluster": {"ev-1"},
			"valid cluster": {"ev-1", "ev-2"},
		} {
			t.Run(name, func(t *testing.T) {
				evStore := evidence.NewMemoryStore()
				seedFailureEvidenceWithIDs(t, evStore, "coder", ids)

				g := newGATestGenerator(t, newGATestProfileStore(t, "stable"))
				d := NewDiagnoserWithOptions(evStore, WithGAGenerator(g))

				_, err := d.GenerateGA(context.Background(), "coder", 0)
				if err == nil {
					t.Fatal("GenerateGA with count 0 should error")
				}
			})
		}
	})

	t.Run("filters evidence by role", func(t *testing.T) {
		evStore := evidence.NewMemoryStore()
		seedFailureEvidenceWithIDs(t, evStore, "coder", []string{"ev-1", "ev-2"})
		seedFailureEvidenceWithIDs(t, evStore, "planner", []string{"ev-3"})

		g := newGATestGenerator(t, newGATestProfileStore(t, "stable"))
		d := NewDiagnoserWithOptions(evStore, WithGAGenerator(g))

		candidates, err := d.GenerateGA(context.Background(), "planner", 2)
		if err != nil {
			t.Fatalf("GenerateGA: %v", err)
		}
		if candidates != nil {
			t.Errorf("planner has only 1 failure, want nil candidates, got %d", len(candidates))
		}
	})
}
