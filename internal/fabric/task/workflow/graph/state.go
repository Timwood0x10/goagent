// package graph - provides dynamic agent orchestration with pluggable scheduling.

package graph

// State represents the shared runtime state for graph execution.
// It is lock-free as graph execution is single-threaded by default.
type State struct {
	values map[string]any
}

// NewState creates a new empty state instance.
func NewState() *State {
	return &State{
		values: make(map[string]any),
	}
}

// NewStateFromValues creates an independent graph state from the provided values.
func NewStateFromValues(values map[string]any) *State {
	state := NewState()
	for key, value := range values {
		state.values[key] = value
	}
	return state
}

// Get retrieves a value from the state by key.
// Returns the value and a boolean indicating whether the key exists.
func (s *State) Get(key string) (any, bool) {
	if s == nil {
		return nil, false
	}
	val, ok := s.values[key]
	return val, ok
}

// Set stores a value in the state with the given key.
func (s *State) Set(key string, val any) {
	if s == nil {
		return
	}
	s.values[key] = val
}

// ToParams converts the state to a map for tool parameter passing.
func (s *State) ToParams() map[string]any {
	if s == nil {
		return make(map[string]any)
	}
	result := make(map[string]any, len(s.values))
	for k, v := range s.values {
		result[k] = v
	}
	return result
}
