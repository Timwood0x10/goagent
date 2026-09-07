package evolution

import (
	"context"
	"errors"
	"fmt"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// ErrNilGAGeneratorProfileStore is returned when a GA generator is built
// without a profile store to read the stable instructions from.
var ErrNilGAGeneratorProfileStore = errors.New("evolution: GA generator has nil profile store")

// ErrGAGeneratorNoPool is returned when a GA generator has neither a prompt
// pool nor a custom mutator: without a pool there is nothing to mutate the
// stable instructions into, so every child would be a no-op copy.
var ErrGAGeneratorNoPool = errors.New("evolution: GA generator needs a prompt pool or custom mutator")

// ErrGAGeneratorNoStable is returned when the target role has no stable
// profile to use as the mutation parent.
var ErrGAGeneratorNoStable = errors.New("evolution: GA generator has no stable profile for role")

// ErrGAGeneratorNoEvidence is returned when no failure evidence IDs are given:
// a candidate must always be justified by evidence (Ch.8 candidate contract).
var ErrGAGeneratorNoEvidence = errors.New("evolution: GA generator needs at least one evidence ID")

// defaultGAMaxAttempts bounds the mutation loop when collecting distinct
// candidates: with a 20% prompt-mutation probability, several rounds may be
// needed before a child differs from the stable instructions.
const defaultGAMaxAttempts = 64

// GAGenerator produces candidate instruction diffs by GA-mutating the target
// role's stable instructions (Ch.8: candidate generation within a bounded
// harness). The stable instructions become the parent mutation.Strategy; each
// child whose PromptTemplate differs from the parent is a candidate whose Diff
// is that template — flowing through the same verifier/coordinator/deployment
// pipeline as any other candidate.
type GAGenerator struct {
	profileStore *ProfileStore
	mutator      *mutation.Mutator
	promptPool   []string
	maxAttempts  int
}

// GAGeneratorOption configures a GAGenerator during construction.
type GAGeneratorOption func(*GAGenerator)

// WithGAPromptPool sets the prompt template pool used to mutate the stable
// instructions. Without a pool (or a custom mutator) the generator refuses to
// build, because prompt mutation is what produces genuinely different
// instruction text.
func WithGAPromptPool(pool []string) GAGeneratorOption {
	return func(g *GAGenerator) {
		g.promptPool = pool
	}
}

// WithGAMutator replaces the default mutator with a fully custom one.
func WithGAMutator(m *mutation.Mutator) GAGeneratorOption {
	return func(g *GAGenerator) {
		g.mutator = m
	}
}

// WithGAMaxAttempts bounds how many mutation rounds the generator runs when
// collecting distinct candidates (default 64).
func WithGAMaxAttempts(n int) GAGeneratorOption {
	return func(g *GAGenerator) {
		if n > 0 {
			g.maxAttempts = n
		}
	}
}

// NewGAGenerator creates a GA candidate generator for the given profile store.
//
// Args:
//
//	profileStore - reads the stable instructions used as the mutation parent;
//	  must be non-nil.
//	opts - optional configuration (prompt pool or custom mutator, max attempts).
//
// Returns:
//
//	generator - the ready-to-use generator.
//	err - ErrNilGAGeneratorProfileStore, ErrGAGeneratorNoPool, or a mutator
//	  construction error.
func NewGAGenerator(profileStore *ProfileStore, opts ...GAGeneratorOption) (*GAGenerator, error) {
	if profileStore == nil {
		return nil, ErrNilGAGeneratorProfileStore
	}
	g := &GAGenerator{
		profileStore: profileStore,
		maxAttempts:  defaultGAMaxAttempts,
	}
	for _, opt := range opts {
		opt(g)
	}
	if g.mutator == nil {
		if len(g.promptPool) == 0 {
			return nil, ErrGAGeneratorNoPool
		}
		m, err := mutation.NewMutator(mutation.WithPromptPool(g.promptPool))
		if err != nil {
			return nil, fmt.Errorf("evolution: build GA mutator: %w", err)
		}
		g.mutator = m
	}
	return g, nil
}

// Generate produces up to n candidate instruction diffs for a role by mutating
// its stable instructions. Only children whose PromptTemplate actually differs
// from the stable text are kept (a parameter-only mutation changes no text and
// is a no-op candidate). Duplicate templates are collapsed.
//
// Args:
//
//	ctx - timeout and cancellation context.
//	role - the target agent role, e.g. "coder".
//	evidenceIDs - failure evidence justifying the candidate; must be non-empty.
//	n - the maximum number of candidates to produce; must be > 0.
//
// Returns:
//
//	candidates - up to n distinct candidates, each carrying the mutated
//	  instruction text as Diff.
//	err - ErrGAGeneratorNoStable, ErrGAGeneratorNoEvidence, or a mutation error.
func (g *GAGenerator) Generate(ctx context.Context, role string, evidenceIDs []string, n int) ([]*Candidate, error) {
	if role == "" {
		return nil, errors.New("evolution: GA generator role must not be empty")
	}
	if n <= 0 {
		return nil, errors.New("evolution: GA generator count must be positive")
	}
	if len(evidenceIDs) == 0 {
		return nil, ErrGAGeneratorNoEvidence
	}
	stable := g.profileStore.GetStable(role)
	if stable == nil {
		return nil, fmt.Errorf("%w: %s", ErrGAGeneratorNoStable, role)
	}

	parent := &mutation.Strategy{
		PromptTemplate: stable.Instructions,
	}

	candidates := make([]*Candidate, 0, n)
	seen := make(map[string]bool, n)
	for attempts := 0; len(candidates) < n && attempts < g.maxAttempts; attempts++ {
		select {
		case <-ctx.Done():
			return candidates, ctx.Err()
		default:
		}

		children, err := g.mutator.Mutate(ctx, parent, 1)
		if err != nil {
			return candidates, fmt.Errorf("evolution: GA mutate: %w", err)
		}
		child := children[0]
		if child.PromptTemplate == "" ||
			child.PromptTemplate == stable.Instructions ||
			seen[child.PromptTemplate] {
			continue
		}
		seen[child.PromptTemplate] = true

		reason := child.MutationDesc
		if reason == "" {
			reason = "GA mutation of stable instructions"
		}
		candidates = append(candidates, NewCandidate(
			CandidateInstruction, role, child.PromptTemplate, reason, evidenceIDs,
		))
	}
	return candidates, nil
}
