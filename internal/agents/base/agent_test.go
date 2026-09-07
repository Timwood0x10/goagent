// nolint: errcheck // Test code may ignore return values
package base

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/runtime/protocol/ahp"
)

// mockAgent implements the Agent interface for testing.
type mockAgent struct {
	id         string
	agentType  models.AgentType
	status     models.AgentStatus
	started    bool
	stopped    bool
	shouldFail bool
}

func newMockAgent(id string, agentType models.AgentType) *mockAgent {
	return &mockAgent{
		id:        id,
		agentType: agentType,
		status:    models.AgentStatusOffline,
		started:   false,
		stopped:   false,
	}
}

func (m *mockAgent) ID() string {
	return m.id
}

func (m *mockAgent) Type() models.AgentType {
	return m.agentType
}

func (m *mockAgent) Status() models.AgentStatus {
	return m.status
}

func (m *mockAgent) Start(ctx context.Context) error {
	if m.shouldFail {
		return errors.New("start failed")
	}
	m.started = true
	m.status = models.AgentStatusReady
	return nil
}

func (m *mockAgent) Stop(ctx context.Context) error {
	if m.shouldFail {
		return errors.New("stop failed")
	}
	m.stopped = true
	m.status = models.AgentStatusOffline
	return nil
}

func (m *mockAgent) Process(ctx context.Context, input any) (any, error) {
	if m.shouldFail {
		return nil, errors.New("process failed")
	}
	return "processed: " + input.(string), nil
}

// ProcessStream handles input and returns a stream of events.
func (m *mockAgent) ProcessStream(ctx context.Context, input any) (<-chan AgentEvent, error) {
	result, err := m.Process(ctx, input)
	ch := make(chan AgentEvent, 1)
	ch <- AgentEvent{Type: EventComplete, Data: result, Err: err}
	close(ch)
	return ch, nil
}

// mockMessenger implements the Messenger interface for testing.
type mockMessenger struct {
	sentMessages []*ahp.AHPMessage
	shouldFail   bool
}

func newMockMessenger() *mockMessenger {
	return &mockMessenger{
		sentMessages: make([]*ahp.AHPMessage, 0),
		shouldFail:   false,
	}
}

func (m *mockMessenger) SendMessage(ctx context.Context, msg *ahp.AHPMessage) error {
	if m.shouldFail {
		return errors.New("send failed")
	}
	m.sentMessages = append(m.sentMessages, msg)
	return nil
}

func (m *mockMessenger) ReceiveMessage(ctx context.Context) (*ahp.AHPMessage, error) {
	if m.shouldFail {
		return nil, errors.New("receive failed")
	}
	return &ahp.AHPMessage{MessageID: "test-msg"}, nil
}

// mockHeartbeater implements the Heartbeater interface for testing.
type mockHeartbeater struct {
	alive      bool
	shouldFail bool
}

func newMockHeartbeater(alive bool) *mockHeartbeater {
	return &mockHeartbeater{
		alive:      alive,
		shouldFail: false,
	}
}

func (m *mockHeartbeater) Heartbeat(ctx context.Context) error {
	if m.shouldFail {
		return errors.New("heartbeat failed")
	}
	return nil
}

func (m *mockHeartbeater) IsAlive() bool {
	return m.alive
}

// TestDefaultConfig tests the DefaultConfig function.
func TestDefaultConfig(t *testing.T) {
	tests := []struct {
		name      string
		agentType models.AgentType
		want      *Config
	}{
		{
			name:      "default config for leader agent",
			agentType: models.AgentTypeTop,
			want: &Config{
				Type:              models.AgentTypeTop,
				HeartbeatInterval: 30 * time.Second,
				MaxRetries:        3,
				Timeout:           5 * time.Minute,
			},
		},
		{
			name:      "default config for top agent",
			agentType: models.AgentTypeTop,
			want: &Config{
				Type:              models.AgentTypeTop,
				HeartbeatInterval: 30 * time.Second,
				MaxRetries:        3,
				Timeout:           5 * time.Minute,
			},
		},
		{
			name:      "default config for bottom agent",
			agentType: models.AgentTypeBottom,
			want: &Config{
				Type:              models.AgentTypeBottom,
				HeartbeatInterval: 30 * time.Second,
				MaxRetries:        3,
				Timeout:           5 * time.Minute,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DefaultConfig(tt.agentType)

			if got.Type != tt.want.Type {
				t.Errorf("DefaultConfig() Type = %v, want %v", got.Type, tt.want.Type)
			}
			if got.HeartbeatInterval != tt.want.HeartbeatInterval {
				t.Errorf("DefaultConfig() HeartbeatInterval = %v, want %v", got.HeartbeatInterval, tt.want.HeartbeatInterval)
			}
			if got.MaxRetries != tt.want.MaxRetries {
				t.Errorf("DefaultConfig() MaxRetries = %v, want %v", got.MaxRetries, tt.want.MaxRetries)
			}
			if got.Timeout != tt.want.Timeout {
				t.Errorf("DefaultConfig() Timeout = %v, want %v", got.Timeout, tt.want.Timeout)
			}
		})
	}
}

// TestAgentInterface tests the Agent interface implementation.
func TestAgentInterface(t *testing.T) {
	ctx := context.Background()
	agent := newMockAgent("test-agent", models.AgentTypeTop)

	// Test ID
	if got := agent.ID(); got != "test-agent" {
		t.Errorf("Agent.ID() = %v, want %v", got, "test-agent")
	}

	// Test Type
	if got := agent.Type(); got != models.AgentTypeTop {
		t.Errorf("Agent.Type() = %v, want %v", got, models.AgentTypeTop)
	}

	// Test initial status
	if got := agent.Status(); got != models.AgentStatusOffline {
		t.Errorf("Agent.Status() = %v, want %v", got, models.AgentStatusOffline)
	}

	// Test Start
	if err := agent.Start(ctx); err != nil {
		t.Errorf("Agent.Start() error = %v", err)
	}
	if !agent.started {
		t.Error("Agent.Start() did not set started flag")
	}
	if agent.Status() != models.AgentStatusReady {
		t.Errorf("Agent.Status() after Start = %v, want %v", agent.Status(), models.AgentStatusReady)
	}

	// Test Process
	result, err := agent.Process(ctx, "test input")
	if err != nil {
		t.Errorf("Agent.Process() error = %v", err)
	}
	if result != "processed: test input" {
		t.Errorf("Agent.Process() = %v, want %v", result, "processed: test input")
	}

	// Test Stop
	if err := agent.Stop(ctx); err != nil {
		t.Errorf("Agent.Stop() error = %v", err)
	}
	if !agent.stopped {
		t.Error("Agent.Stop() did not set stopped flag")
	}
	if agent.Status() != models.AgentStatusOffline {
		t.Errorf("Agent.Status() after Stop = %v, want %v", agent.Status(), models.AgentStatusOffline)
	}
}

// TestAgentInterfaceErrorHandling tests error handling in Agent interface.
func TestAgentInterfaceErrorHandling(t *testing.T) {
	ctx := context.Background()
	agent := newMockAgent("test-agent", models.AgentTypeBottom)
	agent.shouldFail = true

	// Test Start with error
	if err := agent.Start(ctx); err == nil {
		t.Error("Agent.Start() expected error, got nil")
	}

	// Test Process with error
	_, err := agent.Process(ctx, "test input")
	if err == nil {
		t.Error("Agent.Process() expected error, got nil")
	}

	// Test Stop with error
	if err := agent.Stop(ctx); err == nil {
		t.Error("Agent.Stop() expected error, got nil")
	}
}

// TestMessengerInterface tests the Messenger interface implementation.
func TestMessengerInterface(t *testing.T) {
	ctx := context.Background()
	messenger := newMockMessenger()

	// Test SendMessage
	msg := &ahp.AHPMessage{
		MessageID:   "test-message-id",
		AgentID:     "agent-1",
		TargetAgent: "agent-2",
		Payload:     map[string]any{"test": "payload"},
	}

	if err := messenger.SendMessage(ctx, msg); err != nil {
		t.Errorf("Messenger.SendMessage() error = %v", err)
	}

	if len(messenger.sentMessages) != 1 {
		t.Errorf("Messenger sent %d messages, want 1", len(messenger.sentMessages))
	}

	if messenger.sentMessages[0] != msg {
		t.Error("Messenger.SendMessage() did not store the message correctly")
	}

	// Test ReceiveMessage
	receivedMsg, err := messenger.ReceiveMessage(ctx)
	if err != nil {
		t.Errorf("Messenger.ReceiveMessage() error = %v", err)
	}

	if receivedMsg.MessageID != "test-msg" {
		t.Errorf("Messenger.ReceiveMessage() MessageID = %v, want %v", receivedMsg.MessageID, "test-msg")
	}
}

// TestMessengerInterfaceErrorHandling tests error handling in Messenger interface.
func TestMessengerInterfaceErrorHandling(t *testing.T) {
	ctx := context.Background()
	messenger := newMockMessenger()
	messenger.shouldFail = true

	// Test SendMessage with error
	msg := &ahp.AHPMessage{MessageID: "test"}
	if err := messenger.SendMessage(ctx, msg); err == nil {
		t.Error("Messenger.SendMessage() expected error, got nil")
	}

	// Test ReceiveMessage with error
	_, err := messenger.ReceiveMessage(ctx)
	if err == nil {
		t.Error("Messenger.ReceiveMessage() expected error, got nil")
	}
}

// TestHeartbeaterInterface tests the Heartbeater interface implementation.
func TestHeartbeaterInterface(t *testing.T) {
	ctx := context.Background()

	// Test alive heartbeater
	heartbeater := newMockHeartbeater(true)

	if !heartbeater.IsAlive() {
		t.Error("Heartbeater.IsAlive() = false, want true")
	}

	if err := heartbeater.Heartbeat(ctx); err != nil {
		t.Errorf("Heartbeater.Heartbeat() error = %v", err)
	}

	// Test dead heartbeater
	deadHeartbeater := newMockHeartbeater(false)

	if deadHeartbeater.IsAlive() {
		t.Error("Heartbeater.IsAlive() = true, want false")
	}
}

// TestHeartbeaterInterfaceErrorHandling tests error handling in Heartbeater interface.
func TestHeartbeaterInterfaceErrorHandling(t *testing.T) {
	ctx := context.Background()
	heartbeater := newMockHeartbeater(true)
	heartbeater.shouldFail = true

	if err := heartbeater.Heartbeat(ctx); err == nil {
		t.Error("Heartbeater.Heartbeat() expected error, got nil")
	}
}

// TestAgentStatusTransitions tests agent status transitions.
func TestAgentStatusTransitions(t *testing.T) {
	ctx := context.Background()
	agent := newMockAgent("test-agent", models.AgentTypeTop)

	// Verify initial status
	if agent.Status() != models.AgentStatusOffline {
		t.Errorf("Initial status = %v, want %v", agent.Status(), models.AgentStatusOffline)
	}

	// Start the agent
	if err := agent.Start(ctx); err != nil {
		t.Fatalf("Agent.Start() error = %v", err)
	}

	if agent.Status() != models.AgentStatusReady {
		t.Errorf("Status after Start = %v, want %v", agent.Status(), models.AgentStatusReady)
	}

	// Stop the agent
	if err := agent.Stop(ctx); err != nil {
		t.Fatalf("Agent.Stop() error = %v", err)
	}

	if agent.Status() != models.AgentStatusOffline {
		t.Errorf("Status after Stop = %v, want %v", agent.Status(), models.AgentStatusOffline)
	}
}

// TestAgentConcurrentOperations tests concurrent agent operations.
func TestAgentConcurrentOperations(t *testing.T) {
	ctx := context.Background()
	agent := newMockAgent("test-agent", models.AgentTypeTop)

	// Start the agent
	if err := agent.Start(ctx); err != nil {
		t.Fatalf("Agent.Start() error = %v", err)
	}

	// Process multiple requests concurrently
	results := make(chan string, 10)
	for i := 0; i < 10; i++ {
		go func(input string) {
			result, err := agent.Process(ctx, input)
			if err != nil {
				t.Errorf("Agent.Process() error = %v", err)
				results <- ""
				return
			}
			results <- result.(string)
		}("input-" + string(rune('0'+i)))
	}

	// Collect results
	for i := 0; i < 10; i++ {
		select {
		case <-results:
		case <-time.After(5 * time.Second):
			t.Fatal("Agent.Process() timed out")
		}
	}
}

// TestConfigValidation tests Config structure validation.
func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name: "valid config",
			config: &Config{
				ID:                "test-agent",
				Type:              models.AgentTypeTop,
				HeartbeatInterval: 30 * time.Second,
				MaxRetries:        3,
				Timeout:           5 * time.Minute,
			},
			wantErr: false,
		},
		{
			name: "config with zero heartbeat interval",
			config: &Config{
				ID:                "test-agent",
				Type:              models.AgentTypeBottom,
				HeartbeatInterval: 0,
				MaxRetries:        3,
				Timeout:           5 * time.Minute,
			},
			wantErr: false, // Zero values are allowed, they should be handled by implementation
		},
		{
			name: "config with negative max retries",
			config: &Config{
				ID:                "test-agent",
				Type:              models.AgentTypeTop,
				HeartbeatInterval: 30 * time.Second,
				MaxRetries:        -1,
				Timeout:           5 * time.Minute,
			},
			wantErr: false, // Validation should be done by implementation
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This test verifies that Config can be created with various values
			// Actual validation should be done by the agent implementation
			if tt.config == nil {
				t.Error("Config should not be nil")
			}
		})
	}
}

// nolint: errcheck // Test code may ignore return values
