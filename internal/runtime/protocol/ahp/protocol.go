package ahp

import (
	"context"
	"errors"
	"fmt"

	"github.com/Timwood0x10/ares/internal/core/models"
	apperrors "github.com/Timwood0x10/ares/internal/errors"
)

// Protocol represents the AHP protocol manager.
type Protocol struct {
	registry  *QueueRegistry
	dlq       *DLQ
	codec     Codec
	heartbeat *HeartbeatMonitor
	config    *ProtocolConfig
}

// ProtocolConfig holds the configuration for the protocol.
type ProtocolConfig struct {
	QueueSize       int
	HeartbeatConfig *HeartbeatConfig
	EnableDLQ       bool
	DLQSize         int
}

// DefaultProtocolConfig returns the default protocol configuration.
func DefaultProtocolConfig() *ProtocolConfig {
	return &ProtocolConfig{
		QueueSize:       1000,
		HeartbeatConfig: DefaultHeartbeatConfig(),
		EnableDLQ:       true,
		DLQSize:         10000,
	}
}

// NewProtocol creates a new Protocol instance.
func NewProtocol(config *ProtocolConfig) *Protocol {
	if config == nil {
		config = DefaultProtocolConfig()
	}

	registry := NewQueueRegistry(&QueueOptions{
		MaxSize: config.QueueSize,
	})

	var dlq *DLQ
	if config.EnableDLQ {
		dlq = NewDLQ(config.DLQSize)
	}

	heartbeat := NewHeartbeatMonitor(config.HeartbeatConfig)

	codecRegistry := NewCodecRegistry()
	codecRegistry.InitDefaultCodecs()

	return &Protocol{
		registry:  registry,
		dlq:       dlq,
		codec:     codecRegistry.Default(),
		heartbeat: heartbeat,
		config:    config,
	}
}

// GetQueue returns the message queue for an agent.
func (p *Protocol) GetQueue(agentID string) *MessageQueue {
	return p.registry.GetOrCreate(agentID)
}

// SendMessage sends a message to a target agent.
func (p *Protocol) SendMessage(ctx context.Context, msg *AHPMessage) error {
	if msg == nil {
		return apperrors.ErrInvalidMessage
	}

	queue := p.GetQueue(msg.TargetAgent)

	// Try Enqueue directly — do NOT check IsFull first to avoid TOCTOU race
	// where the queue fills up between the check and the actual enqueue.
	err := queue.Enqueue(ctx, msg)
	if err != nil {
		reason := classifyEnqueueError(err)
		if p.dlq != nil {
			p.dlq.Add(msg, err, reason)
		}
		return fmt.Errorf("send message to %s: %w", msg.TargetAgent, err)
	}

	return nil
}

// ReceiveMessage receives a message for an agent.
func (p *Protocol) ReceiveMessage(ctx context.Context, agentID string) (*AHPMessage, error) {
	queue, ok := p.registry.Get(agentID)
	if !ok {
		return nil, apperrors.ErrAgentNotFound
	}

	return queue.Dequeue(ctx)
}

// SendTask sends a task to a sub-agent.
func (p *Protocol) SendTask(ctx context.Context, targetAgent, taskID, sessionID string, payload map[string]any) error {
	msg := NewTaskMessage("leader", targetAgent, taskID, sessionID, payload)
	return p.SendMessage(ctx, msg)
}

// SendResult sends a task result back to leader.
func (p *Protocol) SendResult(ctx context.Context, targetAgent, taskID, sessionID string, result *models.TaskResult) error {
	msg := NewResultMessage(targetAgent, "leader", taskID, sessionID, result)
	return p.SendMessage(ctx, msg)
}

// RecordHeartbeat records a heartbeat from an agent.
func (p *Protocol) RecordHeartbeat(agentID string) {
	p.heartbeat.RecordHeartbeat(agentID)
}

// GetAgentStatus returns the status of an agent.
func (p *Protocol) GetAgentStatus(agentID string) (models.AgentStatus, bool) {
	return p.heartbeat.GetStatus(agentID)
}

// CheckTimeouts checks for agents that have timed out.
func (p *Protocol) CheckTimeouts() []string {
	return p.heartbeat.CheckTimeouts()
}

// GetDLQ returns the dead letter queue.
func (p *Protocol) GetDLQ() *DLQ {
	return p.dlq
}

// EncodeMessage encodes a message using the configured codec.
func (p *Protocol) EncodeMessage(msg *AHPMessage) ([]byte, error) {
	return p.codec.Encode(msg)
}

// DecodeMessage decodes a message using the configured codec.
func (p *Protocol) DecodeMessage(data []byte) (*AHPMessage, error) {
	return p.codec.Decode(data)
}

// Close closes all agent queues managed by this protocol.
func (p *Protocol) Close() {
	for _, agentID := range p.registry.ListAgents() {
		p.registry.Delete(agentID)
	}
}

// classifyEnqueueError maps an enqueue error to a short DLQ reason string.
func classifyEnqueueError(err error) string {
	switch {
	case errors.Is(err, apperrors.ErrQueueClosed):
		return "queue_closed"
	case errors.Is(err, apperrors.ErrQueueFull):
		return "queue_full"
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "context_deadline"
	default:
		return "unknown"
	}
}

// Stats returns protocol statistics.
func (p *Protocol) Stats() *ProtocolStats {
	dlqSize := 0
	if p.dlq != nil {
		dlqSize = p.dlq.Size()
	}

	return &ProtocolStats{
		TotalQueues:     len(p.registry.ListAgents()),
		TotalMessages:   p.registry.Size(),
		DLQSize:         dlqSize,
		MonitoredAgents: len(p.heartbeat.ListAgents()),
	}
}

// ProtocolStats holds protocol statistics.
type ProtocolStats struct {
	TotalQueues     int
	TotalMessages   int
	DLQSize         int
	MonitoredAgents int
}

func (s *ProtocolStats) String() string {
	return fmt.Sprintf("Queues: %d, Messages: %d, DLQ: %d, Agents: %d",
		s.TotalQueues, s.TotalMessages, s.DLQSize, s.MonitoredAgents)
}
