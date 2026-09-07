package aresrecovery

import (
	"sync"
	"time"
)

// Human feedback loop: humans rate / approve evolution
// candidates, and the feedback is combined with the automatic score so the
// Evolution system can weigh human judgment in fitness. The store records
// feedback entries; the Evolution system reads them when scoring candidates.

// HumanFeedback is one human review of an evolution candidate.
type HumanFeedback struct {
	// CandidateID is the reviewed strategy/candidate id.
	CandidateID string `json:"candidate_id"`
	// Rating is the human rating (1-5 scale; 0 = unrated).
	Rating float64 `json:"rating"`
	// Comments is free-form human commentary.
	Comments string `json:"comments,omitempty"`
	// Approved is the human approval decision.
	Approved bool `json:"approved"`
	// Reason explains the approval/denial.
	Reason string `json:"reason,omitempty"`
	// At is when the feedback was recorded.
	At time.Time `json:"at"`
}

// CombinedFitness blends the automatic score with the human rating
// The weights favor human judgment early and automatic scoring
// later — the default 0.3/0.7 split is the roadmap recommendation.
func CombinedFitness(autoScore, humanRating float64) float64 {
	return 0.3*autoScore + 0.7*humanRating
}

// FeedbackStore records human feedback entries and serves them to the
// Evolution system (thread-safe; history capped by WithMaxEntries).
type FeedbackStore struct {
	mu       sync.Mutex
	entries  []HumanFeedback
	max      int            // 0 = unlimited
	byCandID map[string]int // candidate id → latest entry index (for updates)
}

// NewFeedbackStore creates an empty feedback store.
func NewFeedbackStore() *FeedbackStore {
	return &FeedbackStore{byCandID: make(map[string]int)}
}

// WithMaxEntries caps the retained history to the most recent n entries
// (0 = unlimited). Returns the store for chaining.
func (s *FeedbackStore) WithMaxEntries(n int) *FeedbackStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.max = n
	if n > 0 && len(s.entries) > n {
		s.entries = append([]HumanFeedback(nil), s.entries[len(s.entries)-n:]...)
	}
	return s
}

// Add records one human feedback entry. A re-review of the same candidate
// replaces the previous entry (latest wins) so the store never grows with
// stale duplicate reviews.
//
// Args:
//   - fb: the feedback; At is filled with time.Now when zero.
func (s *FeedbackStore) Add(fb HumanFeedback) {
	if fb.At.IsZero() {
		fb.At = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx, ok := s.byCandID[fb.CandidateID]; ok && idx < len(s.entries) {
		s.entries[idx] = fb
		return
	}
	s.byCandID[fb.CandidateID] = len(s.entries)
	s.entries = append(s.entries, fb)
	if s.max > 0 && len(s.entries) > s.max {
		drop := len(s.entries) - s.max
		s.entries = append([]HumanFeedback(nil), s.entries[drop:]...)
		// Rebuild the candidate index after trimming.
		s.byCandID = make(map[string]int, len(s.entries))
		for i := range s.entries {
			s.byCandID[s.entries[i].CandidateID] = i
		}
	}
}

// All returns a copy of the recorded feedback (oldest first).
func (s *FeedbackStore) All() []HumanFeedback {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]HumanFeedback, len(s.entries))
	copy(out, s.entries)
	return out
}

// ForCandidate returns the latest feedback for a candidate, or nil when the
// candidate has not been reviewed.
func (s *FeedbackStore) ForCandidate(candidateID string) *HumanFeedback {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byCandID[candidateID]
	if !ok || idx >= len(s.entries) {
		return nil
	}
	latest := s.entries[idx]
	return &latest
}

// Count returns the number of recorded feedback entries.
func (s *FeedbackStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}
