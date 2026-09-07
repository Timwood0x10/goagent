package engine

import (
	"context"
	"fmt"
	"sync"

	"github.com/Timwood0x10/ares/internal/agents/base"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/errors"
)

// AgentFactory creates agent instances.
type AgentFactory func(ctx context.Context, config interface{}) (base.Agent, error)

// AgentRegistry manages agent type registrations.
type AgentRegistry struct {
	factories map[string]AgentFactory
	mu        sync.RWMutex
}

// NewAgentRegistry creates a new AgentRegistry.
func NewAgentRegistry() *AgentRegistry {
	return &AgentRegistry{
		factories: make(map[string]AgentFactory),
	}
}

// Register registers an agent factory for a type.
func (r *AgentRegistry) Register(agentType string, factory AgentFactory) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.factories[agentType]; exists {
		return ErrAgentTypeRegistered
	}

	r.factories[agentType] = factory
	return nil
}

// Unregister removes an agent factory.
func (r *AgentRegistry) Unregister(agentType string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.factories, agentType)
}

// GetFactory returns an agent factory by type.
func (r *AgentRegistry) GetFactory(agentType string) (AgentFactory, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	factory, exists := r.factories[agentType]
	return factory, exists
}

// CreateAgent creates an agent instance by type.
func (r *AgentRegistry) CreateAgent(ctx context.Context, agentType string, config interface{}) (base.Agent, error) {
	r.mu.RLock()
	factory, exists := r.factories[agentType]
	r.mu.RUnlock()

	if !exists {
		return nil, errors.Wrap(ErrAgentTypeNotFound, agentType)
	}

	return factory(ctx, config)
}

// ListTypes returns all registered agent types.
func (r *AgentRegistry) ListTypes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	types := make([]string, 0, len(r.factories))
	for agentType := range r.factories {
		types = append(types, agentType)
	}

	return types
}

// AgentExecutor executes tasks using registered agents.
type AgentExecutor struct {
	registry *AgentRegistry
}

// NewAgentExecutor creates a new AgentExecutor.
func NewAgentExecutor(registry *AgentRegistry) *AgentExecutor {
	return &AgentExecutor{
		registry: registry,
	}
}

// Execute executes a step using the appropriate agent.
func (e *AgentExecutor) Execute(ctx context.Context, step *Step, input string, taskCtx *models.TaskContext) (string, error) {
	agent, err := e.registry.CreateAgent(ctx, step.AgentType, step.Input)
	if err != nil {
		return "", err
	}

	result, err := agent.Process(ctx, input)
	if err != nil {
		return "", err
	}

	if result == nil {
		return "", ErrAgentResultNil
	}

	return agentResultToString(result), nil
}

// agentResultToString converts an agent.Process result to a string.
// Previously this method hardcoded a *models.RecommendResult type
// assertion, which made the registry unusable for any other result type.
// The conversion now handles common types explicitly and falls back to
// a best-effort string representation for everything else.
func agentResultToString(result any) string {
	switch v := result.(type) {
	case string:
		return v
	case *models.RecommendResult:
		if v != nil && len(v.Items) > 0 {
			return v.Items[0].Description
		}
		return ""
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

// StepOutput stores the output of a step for dependency resolution.
type StepOutput struct {
	StepID    string
	Output    string
	Variables map[string]interface{}
}

// OutputStore stores step outputs.
type OutputStore struct {
	outputs map[string]*StepOutput
	mu      sync.RWMutex
}

// NewOutputStore creates a new OutputStore.
func NewOutputStore() *OutputStore {
	return &OutputStore{
		outputs: make(map[string]*StepOutput),
	}
}

// Set stores output for a step.
func (s *OutputStore) Set(stepID string, output *StepOutput) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.outputs[stepID] = output
}

// Get retrieves output for a step.
func (s *OutputStore) Get(stepID string) (*StepOutput, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	output, exists := s.outputs[stepID]
	return output, exists
}

// GetMultiple retrieves outputs for multiple steps.
func (s *OutputStore) GetMultiple(stepIDs []string) map[string]*StepOutput {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]*StepOutput, len(stepIDs))
	for _, id := range stepIDs {
		if output, exists := s.outputs[id]; exists {
			result[id] = output
		}
	}

	return result
}

// Clear removes all outputs.
func (s *OutputStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.outputs = make(map[string]*StepOutput)
}

// Close releases resources held by the output store.
// After Close, subsequent Get/Set calls will return zero values safely
// due to the mutex protection, but no stored data will be accessible.
func (s *OutputStore) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.outputs = nil
}
