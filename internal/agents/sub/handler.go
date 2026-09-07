package sub

import (
	"context"

	"github.com/Timwood0x10/ares/internal/errors"
	"github.com/Timwood0x10/ares/internal/runtime/protocol/ahp"
)

// messageHandler handles incoming AHP messages.
type messageHandler struct {
	agentID string
}

// NewMessageHandler creates a new MessageHandler.
func NewMessageHandler(agentID string) MessageHandler {
	return &messageHandler{
		agentID: agentID,
	}
}

// Handle processes an incoming message.
func (h *messageHandler) Handle(ctx context.Context, msg *ahp.AHPMessage) error {
	if msg == nil {
		return errors.ErrNilPointer
	}

	switch msg.Method {
	case ahp.AHPMethodTask:
		return h.handleTaskMessage(ctx, msg)
	case ahp.AHPMethodACK:
		return h.handleAckMessage(ctx, msg)
	case ahp.AHPMethodHeartbeat:
		return nil // Heartbeat acknowledged
	default:
		return errors.ErrInvalidMessage
	}
}

func (h *messageHandler) handleTaskMessage(ctx context.Context, msg *ahp.AHPMessage) error {
	// Protocol-level acknowledgment only: actual task execution is driven
	// by the Kernel scheduler via taskfabric → ExecuteStep. This stub
	// acknowledges receipt so the sender can mark delivery; the executor
	// picks up the task from the shared queue / fabric.
	return nil
}

func (h *messageHandler) handleAckMessage(ctx context.Context, msg *ahp.AHPMessage) error {
	// Protocol-level ACK: no-op in the Kernel-scheduled model. The task
	// fabric tracks completion via TaskCompleted events, not message ACKs.
	return nil
}
