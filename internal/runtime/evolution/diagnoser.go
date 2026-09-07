package evolution

import (
	"context"
	"errors"
	"fmt"

	"github.com/Timwood0x10/ares/internal/evidence"
)

// MinFailureClusterSize is the minimum number of same-role failure records
// required before a candidate is generated (Ch.8: repeated failure patterns
// indicate a systemic capability gap, not a one-off).
const MinFailureClusterSize = 2

// ErrEvidenceStoreNil is returned when the diagnoser has no evidence store.
var ErrEvidenceStoreNil = errors.New("evolution: diagnoser has nil evidence store")

// Diagnoser generates evolution candidates from failure evidence clusters.
// It answers "which role repeatedly fails, and how" and packages that into a
// Candidate for the verification pipeline. Candidate content (diff/reason) is
// provided by a developer or human reviewer — no automatic LLM generation in
// v1 (Ch.8: candidate generation must stay within a bounded harness).
//
// When a GAGenerator is attached (WithGAGenerator), GenerateGA additionally
// produces candidates by mutating the role's stable instructions, so GA is a
// first-class candidate source in the same verification pipeline.
type Diagnoser struct {
	store evidence.Store
	ga    *GAGenerator // optional GA-based candidate generation.
}

// DiagnoserOption configures a Diagnoser during construction.
type DiagnoserOption func(*Diagnoser)

// WithGAGenerator attaches a GA generator so GenerateGA can produce candidates.
func WithGAGenerator(g *GAGenerator) DiagnoserOption {
	return func(d *Diagnoser) {
		d.ga = g
	}
}

// NewDiagnoser creates a diagnoser that queries the given evidence store for
// failure clusters.
// Args:
//
//	store - the universal evidence store; must be non-nil.
//
// Returns:
//
//	diagnoser - the ready-to-use diagnoser.
func NewDiagnoser(store evidence.Store) *Diagnoser {
	return NewDiagnoserWithOptions(store)
}

// NewDiagnoserWithOptions creates a diagnoser with optional GA generation.
// Args:
//
//	store - the universal evidence store; must be non-nil.
//	opts - optional configuration (e.g. WithGAGenerator).
//
// Returns:
//
//	diagnoser - the ready-to-use diagnoser.
func NewDiagnoserWithOptions(store evidence.Store, opts ...DiagnoserOption) *Diagnoser {
	d := &Diagnoser{store: store}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// GenerateRequest carries the human-confirmed candidate content and the role
// whose failures triggered the candidate.
type GenerateRequest struct {
	// Role is the agent role to improve, e.g. "coder".
	Role string

	// Diff is the new Instructions text (candidate change), confirmed by a
	// developer or reviewer.
	Diff string

	// Reason explains why this change is needed; references the failure pattern.
	Reason string
}

// Generate queries failure evidence for the role and produces a Candidate when
// at least MinFailureClusterSize failing records exist.
//
// Args:
//
//	ctx - timeout and cancellation context.
//	req - human-confirmed candidate content; Role and Diff must be non-empty.
//
// Returns:
//
//	candidate - a new candidate in StatusCandidate, or nil when the failure
//	  cluster is smaller than MinFailureClusterSize.
//	err - ErrEvidenceStoreNil when the store is nil, or a validation error.
func (d *Diagnoser) Generate(ctx context.Context, req GenerateRequest) (*Candidate, error) {
	if d.store == nil {
		return nil, ErrEvidenceStoreNil
	}
	if req.Role == "" {
		return nil, errors.New("evolution: diagnose role must not be empty")
	}
	if req.Diff == "" {
		return nil, errors.New("evolution: diagnose diff must not be empty")
	}

	// Query failure-cluster evidence for the role. The dimension_eval kind is
	// produced by the three-layer verifiers via the evidence bridge (P0-4).
	evidenceIDs, err := d.failureEvidenceIDs(ctx, req.Role)
	if err != nil {
		return nil, err
	}

	if len(evidenceIDs) < MinFailureClusterSize {
		//nolint:nilnil // nil candidate + nil error is the documented "no systemic pattern yet" contract.
		return nil, nil
	}

	return NewCandidate(CandidateInstruction, req.Role, req.Diff, req.Reason, evidenceIDs), nil
}

// GenerateGA produces candidates for a role by GA-mutating its stable
// instructions, but only when a failure cluster of at least MinFailureClusterSize
// exists (the same systemic-pattern gate as Generate). The GA generator reads
// the stable instructions from its profile store and returns up to n distinct
// mutated instruction candidates, each carrying the failure evidence IDs.
//
// Args:
//
//	ctx - timeout and cancellation context.
//	role - the agent role to improve, e.g. "coder".
//	n - the maximum number of candidates to produce; must be > 0.
//
// Returns:
//
//	candidates - GA-generated candidates, or nil when the failure cluster is
//	  smaller than MinFailureClusterSize.
//	err - ErrEvidenceStoreNil, ErrGAGeneratorNoEvidence (via Generate), a
//	  missing-GA-generator error, or a mutation error.
func (d *Diagnoser) GenerateGA(ctx context.Context, role string, n int) ([]*Candidate, error) {
	if d.store == nil {
		return nil, ErrEvidenceStoreNil
	}
	if d.ga == nil {
		return nil, errors.New("evolution: diagnose GA generator not configured (use WithGAGenerator)")
	}
	// Validate the count at the boundary so an invalid argument errors
	// consistently regardless of the failure-cluster size.
	if n <= 0 {
		return nil, errors.New("evolution: diagnose GA count must be positive")
	}

	evidenceIDs, err := d.failureEvidenceIDs(ctx, role)
	if err != nil {
		return nil, err
	}
	if len(evidenceIDs) < MinFailureClusterSize {
		//nolint:nilnil // nil candidates + nil error is the documented "no systemic pattern yet" contract.
		return nil, nil
	}
	return d.ga.Generate(ctx, role, evidenceIDs, n)
}

// failureEvidenceIDs queries failure-cluster evidence for the role. The
// dimension_eval kind is produced by the three-layer verifiers via the
// evidence bridge (P0-4); the role is carried in the evidence metadata.
func (d *Diagnoser) failureEvidenceIDs(ctx context.Context, role string) ([]string, error) {
	if role == "" {
		return nil, errors.New("evolution: diagnose role must not be empty")
	}
	records, err := d.store.Query(ctx, evidence.Filter{
		Kind:   evidence.KindDimensionEval,
		Source: "result_verifier",
	})
	if err != nil {
		return nil, fmt.Errorf("evolution: query failure evidence: %w", err)
	}

	// Count failures belonging to the target role. The role is carried in the
	// evidence metadata written by the bridge (role=...).
	var evidenceIDs []string
	for _, record := range records {
		if record.Metadata["role"] != role {
			continue
		}
		evidenceIDs = append(evidenceIDs, record.ID)
	}
	return evidenceIDs, nil
}
