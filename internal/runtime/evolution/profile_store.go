package evolution

import (
	"errors"
	"sync"

	"github.com/Timwood0x10/ares/internal/agents"
)

// ErrInvalidProfile is returned when a profile or role argument is invalid.
var ErrInvalidProfile = errors.New("invalid profile: role must not be empty")

// ProfileStore manages agent profiles with separate candidate and stable
// regions. The candidate region is the writable workspace during evolution;
// the stable region holds verified profiles and is only replaced atomically
// after a candidate passes verification and release.
type ProfileStore struct {
	mu        sync.RWMutex
	candidate map[string]*agents.AgentProfile
	stable    map[string]*agents.AgentProfile
}

// NewProfileStore creates an empty profile store.
// Both candidate and stable regions start empty.
func NewProfileStore() *ProfileStore {
	return &ProfileStore{
		candidate: make(map[string]*agents.AgentProfile),
		stable:    make(map[string]*agents.AgentProfile),
	}
}

// Get returns the candidate profile for the given role.
// Args:
//
//	role - the agent role identifier, e.g. "coder".
//
// Returns:
//
//	profile - nil when the role has no candidate profile yet.
func (s *ProfileStore) Get(role string) *agents.AgentProfile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.candidate[role]
}

// Update stores or replaces the candidate profile for its role.
// Args:
//
//	profile - the profile to persist; Role must be non-empty.
//
// Returns:
//
//	err - ErrInvalidProfile when profile is nil or Role is empty.
func (s *ProfileStore) Update(profile *agents.AgentProfile) error {
	if profile == nil || profile.Role == "" {
		return ErrInvalidProfile
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.candidate[profile.Role] = profile
	return nil
}

// GetStable returns the stable (verified and released) profile for the role.
// Stable profiles are read-only from the perspective of candidate generation.
// Args:
//
//	role - the agent role identifier, e.g. "coder".
//
// Returns:
//
//	profile - nil when the role has no stable profile yet.
func (s *ProfileStore) GetStable(role string) *agents.AgentProfile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stable[role]
}

// SetStable atomically promotes a profile to the stable region.
// Args:
//
//	role    - the agent role identifier, must be non-empty.
//	profile - the verified profile to promote, must be non-nil.
//
// Returns:
//
//	err - ErrInvalidProfile when role is empty or profile is nil.
func (s *ProfileStore) SetStable(role string, profile *agents.AgentProfile) error {
	if role == "" || profile == nil {
		return ErrInvalidProfile
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stable[role] = profile
	return nil
}
