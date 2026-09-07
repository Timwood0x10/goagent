// Package evolution provides the Candidate → Verify → Promote pipeline
// for safe, auditable agent evolution in ARES 0.3.0.
//
// Design principle (from AI Agents in Depth Ch.8):
// "All modifications must first become candidates, pass verification,
// and only then can they change the running system. The verifier,
// test harness, and release gate must be outside the agent's own
// modification authority."
package evolution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/evidence"
)

// CandidateKind identifies what type of change a candidate represents.
type CandidateKind int

const (
	CandidateInstruction CandidateKind = iota // Modifies AgentProfile.Instructions
	CandidateSkill                            // Adds/modifies a Skill
	CandidateTool                             // Adds a new tool definition
)

func (k CandidateKind) String() string {
	switch k {
	case CandidateInstruction:
		return "instruction"
	case CandidateSkill:
		return "skill"
	case CandidateTool:
		return "tool"
	default:
		return "unknown"
	}
}

// CandidateStatus tracks the lifecycle of an evolution candidate.
type CandidateStatus string

const (
	StatusCandidate CandidateStatus = "candidate" // Generated, awaiting verification
	StatusVerified  CandidateStatus = "verified"  // Passed all checks
	StatusRejected  CandidateStatus = "rejected"  // Failed verification
	StatusPromoted  CandidateStatus = "promoted"  // Deployed to stable profile
)

func (s CandidateStatus) String() string { return string(s) }

// Candidate is a proposed modification to an agent's behavior.
// It represents the smallest possible change that addresses a diagnosed failure.
type Candidate struct {
	// ID is a unique identifier for this candidate.
	ID string

	// Kind identifies what kind of change this is.
	Kind CandidateKind

	// TargetRole is the agent role this candidate affects.
	TargetRole string

	// Diff describes the minimal change. For instructions, it's a text diff.
	// For skills, it's the skill definition. For tools, it's the tool spec.
	Diff string

	// Reason explains why this change is needed, referencing evidence.
	Reason string

	// EvidenceIDs references the Trace/Evidence records that triggered this candidate.
	EvidenceIDs []string

	// CreatedAt records when the candidate was generated.
	CreatedAt time.Time

	// Status tracks the candidate's lifecycle.
	Status CandidateStatus

	// RejectionReason records why a previous verification attempt failed.
	RejectionReason string

	// PromotedAt records when this candidate was promoted to stable.
	PromotedAt *time.Time
}

// NewCandidate creates a new candidate in the initial state.
func NewCandidate(kind CandidateKind, targetRole, diff, reason string, evidenceIDs []string) *Candidate {
	return &Candidate{
		ID:          generateID(),
		Kind:        kind,
		TargetRole:  targetRole,
		Diff:        diff,
		Reason:      reason,
		EvidenceIDs: evidenceIDs,
		CreatedAt:   time.Now(),
		Status:      StatusCandidate,
	}
}

// Verify marks the candidate as verified after passing all checks.
func (c *Candidate) Verify() {
	c.Status = StatusVerified
	c.RejectionReason = ""
}

// Reject marks the candidate as rejected with a reason.
func (c *Candidate) Reject(reason string) {
	c.Status = StatusRejected
	c.RejectionReason = reason
}

// Promote marks the candidate as deployed to the stable profile.
func (c *Candidate) Promote() {
	now := time.Now()
	c.Status = StatusPromoted
	c.PromotedAt = &now
}

// String returns a human-readable summary.
// The ID is truncated to 8 characters for readability; short IDs are kept intact.
func (c *Candidate) String() string {
	id := c.ID
	if len(id) > 8 {
		id = id[:8]
	}
	return fmt.Sprintf("Candidate{%s %s→%s status=%s}", c.Kind, id, c.TargetRole, c.Status)
}

// CandidateVerifier runs the three-gate verification process.
// It also advances the candidate state machine: a passing verification marks
// the candidate Verified, a failing one marks it Rejected with a reason.
//
// Gate 2 (failure replay) verifies the referenced failure evidence against the
// injected evidence store when present; without a store it falls back to the
// non-empty assertion so un-wired callers still fail loudly on empty IDs.
// Gate 3 (regression) delegates to an optional injected checker.
type CandidateVerifier struct {
	// evidenceStore verifies failure-evidence references (gate 2); nil means
	// the gate only asserts non-empty EvidenceIDs.
	evidenceStore evidence.Store

	// regressionCheck verifies preserved cases when set; nil means the
	// regression gate is skipped (v1 placeholder until ares_arena wiring).
	regressionCheck func(c *Candidate) error
}

// CandidateVerifierOption configures a CandidateVerifier.
type CandidateVerifierOption func(*CandidateVerifier)

// WithEvidenceStore injects the universal evidence store so gate 2 can verify
// that the referenced failure evidence actually exists (no fabricated IDs).
func WithEvidenceStore(store evidence.Store) CandidateVerifierOption {
	return func(v *CandidateVerifier) {
		v.evidenceStore = store
	}
}

// WithRegressionCheck injects the preserved-case regression checker (gate 3).
// It is the ares_arena preserved-case replay mount point (Ch.8 release gate);
// when unset the gate is skipped by design.
func WithRegressionCheck(check func(c *Candidate) error) CandidateVerifierOption {
	return func(v *CandidateVerifier) {
		v.regressionCheck = check
	}
}

// NewCandidateVerifier creates a verifier with an optional regression check.
// Args:
//
//	regressionCheck - verifies preserved cases (regression gate); may be nil
//	  to skip the gate in the first version.
//
// Returns:
//
//	verifier - the ready-to-use verifier.
func NewCandidateVerifier(regressionCheck func(c *Candidate) error) *CandidateVerifier {
	return &CandidateVerifier{regressionCheck: regressionCheck}
}

// NewCandidateVerifierWithOptions creates a verifier configured by functional
// options (e.g. WithEvidenceStore), with no regression checker by default.
// Args:
//
//	opts - verifier configuration options.
//
// Returns:
//
//	verifier - the ready-to-use verifier.
func NewCandidateVerifierWithOptions(opts ...CandidateVerifierOption) *CandidateVerifier {
	v := &CandidateVerifier{}
	for _, opt := range opts {
		opt(v)
	}
	return v
}

// VerifyResult holds the outcome of candidate verification.
type VerifyResult struct {
	Success bool
	Reason  string // Empty if Success is true
}

// Verify runs the three verification gates and advances the candidate state:
//   - all gates pass -> candidate becomes StatusVerified;
//   - any gate fails -> candidate becomes StatusRejected with RejectionReason.
//
// Args:
//
//	candidate - the candidate to verify; must be non-nil.
//
// Returns:
//
//	result - the gate outcome; Success is true only when all gates passed.
func (v *CandidateVerifier) Verify(candidate *Candidate) *VerifyResult {
	if candidate == nil {
		return &VerifyResult{Success: false, Reason: "candidate is nil"}
	}

	// Gate 1: Static validation
	if err := v.staticCheck(candidate); err != nil {
		candidate.Reject("static check: " + err.Error())
		return &VerifyResult{Success: false, Reason: fmt.Sprintf("static check: %v", err)}
	}

	// Gate 2: Verify the referenced failure evidence actually exists.
	if err := v.replayFailureCases(candidate); err != nil {
		candidate.Reject("failure replay: " + err.Error())
		return &VerifyResult{Success: false, Reason: fmt.Sprintf("failure replay: %v", err)}
	}

	// Gate 3: Check for regressions against preserved cases.
	if err := v.checkRegression(candidate); err != nil {
		candidate.Reject("regression check: " + err.Error())
		return &VerifyResult{Success: false, Reason: fmt.Sprintf("regression check: %v", err)}
	}

	candidate.Verify()
	return &VerifyResult{Success: true}
}

// staticCheck validates the candidate's structural integrity.
func (v *CandidateVerifier) staticCheck(c *Candidate) error {
	if c.TargetRole == "" {
		return errors.New("target role is empty")
	}
	if c.Diff == "" {
		return errors.New("diff is empty")
	}
	if c.Reason == "" {
		return errors.New("reason is empty")
	}
	// For instruction candidates, check for obviously dangerous patterns
	if c.Kind == CandidateInstruction && containsDangerousPattern(c.Diff) {
		return errors.New("dangerous pattern detected in diff")
	}
	return nil
}

// replayFailureCases verifies that every referenced failure-evidence ID exists
// in the evidence store and carries the dimension_eval kind. Without an
// injected store it degrades to the non-empty assertion.
// Args:
//
//	c - the candidate whose EvidenceIDs reference failure evidence.
//
// Returns:
//
//	err - an error describing a missing or wrong-kind evidence record.
func (v *CandidateVerifier) replayFailureCases(c *Candidate) error {
	if len(c.EvidenceIDs) == 0 {
		return errors.New("no evidence IDs referenced")
	}
	if v.evidenceStore == nil {
		// No store wired: assert the reference is non-empty only.
		// Callers should inject WithEvidenceStore for the real existence check.
		return nil
	}

	// Use a detached context: evidence lookup is a bounded, local read and
	// the verifier has no user-facing cancellation scope. Query all records
	// (no kind filter) so a wrong-kind record can be distinguished from a
	// missing one.
	ctx := context.Background()
	records, err := v.evidenceStore.Query(ctx, evidence.Filter{})
	if err != nil {
		return fmt.Errorf("query failure evidence: %w", err)
	}
	existing := make(map[string]evidence.Evidence, len(records))
	for _, rec := range records {
		existing[rec.ID] = rec
	}
	for _, id := range c.EvidenceIDs {
		rec, ok := existing[id]
		if !ok {
			return fmt.Errorf("evidence %q not found in store", id)
		}
		if rec.Kind != evidence.KindDimensionEval {
			return fmt.Errorf("evidence %q has kind %q, want dimension_eval", id, rec.Kind)
		}
	}
	return nil
}

// checkRegression ensures the candidate doesn't break previously working cases.
// It delegates to the injected regression checker when present; the gate is
// skipped when no checker is configured (v1 placeholder until ares_arena
// preserved-case wiring).
func (v *CandidateVerifier) checkRegression(c *Candidate) error {
	if v.regressionCheck == nil {
		return nil
	}
	return v.regressionCheck(c)
}

// containsDangerousPattern checks for potentially harmful instructions.
func containsDangerousPattern(text string) bool {
	// Simple heuristic — production should use more robust checks.
	// Match case-insensitively so capitalized variants ("IGNORE ALL SAFETY")
	// are not trivially able to bypass the gate.
	lower := strings.ToLower(text)
	dangerousPatterns := []string{
		"ignore all safety",
		"bypass authentication",
		"delete all data",
		"don't verify",
	}
	for _, p := range dangerousPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// CandidateStore manages candidate persistence and lifecycle.
// It is safe for concurrent use: all reads and writes are guarded by a
// RWMutex so that Submit/Get/List can run from concurrent goroutines
// (Ch.10 failure mode 1: concurrent conflicts).
type CandidateStore struct {
	mu         sync.RWMutex
	candidates []*Candidate
	nextID     int
}

// NewCandidateStore creates a new store.
func NewCandidateStore() *CandidateStore {
	return &CandidateStore{
		candidates: make([]*Candidate, 0),
		nextID:     1,
	}
}

// Submit adds a new candidate and assigns it a stable sequential ID.
// The candidate pointer is stored as-is; callers must not mutate it after
// submission while other goroutines may read it.
func (s *CandidateStore) Submit(c *Candidate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.ID = fmt.Sprintf("cand-%d", s.nextID)
	s.nextID++
	s.candidates = append(s.candidates, c)
}

// Get returns a candidate by ID, or nil when not found.
func (s *CandidateStore) Get(id string) *Candidate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.candidates {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// ListByStatus returns all candidates with the given status.
// The returned slice is a copy; callers may mutate it without affecting the store.
func (s *CandidateStore) ListByStatus(status CandidateStatus) []*Candidate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*Candidate, 0, len(s.candidates))
	for _, c := range s.candidates {
		if c.Status == status {
			result = append(result, c)
		}
	}
	return result
}

// ListByRole returns all candidates affecting a specific role.
// The returned slice is a copy; callers may mutate it without affecting the store.
func (s *CandidateStore) ListByRole(role string) []*Candidate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*Candidate, 0, len(s.candidates))
	for _, c := range s.candidates {
		if c.TargetRole == role {
			result = append(result, c)
		}
	}
	return result
}

// generateID creates a unique candidate ID.
func generateID() string {
	return fmt.Sprintf("cand-%d", time.Now().UnixNano())
}
