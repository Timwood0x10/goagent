package ahp

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"sync/atomic"
	"time"

	"github.com/Timwood0x10/ares/internal/core/models"
)

// messageIDCounter is used to generate unique message IDs.
var messageIDCounter uint64

// AHPMethod represents the type of AHP message.
type AHPMethod string

const (
	AHPMethodTask      AHPMethod = "TASK"
	AHPMethodResult    AHPMethod = "RESULT"
	AHPMethodProgress  AHPMethod = "PROGRESS"
	AHPMethodACK       AHPMethod = "ACK"
	AHPMethodHeartbeat AHPMethod = "HEARTBEAT"
)

// AHPMessage represents the message structure for Agent communication.
type AHPMessage struct {
	MessageID   string         `json:"message_id"`
	Method      AHPMethod      `json:"method"`
	AgentID     string         `json:"agent_id"`
	TargetAgent string         `json:"target_agent"`
	TaskID      string         `json:"task_id"`
	SessionID   string         `json:"session_id"`
	Payload     map[string]any `json:"payload"`
	Timestamp   time.Time      `json:"timestamp"`
}

// Clone returns a deep copy of the message. The Payload map is copied so
// mutating the clone (or the original) never affects the other. Used by the
// peer registry to stamp TargetAgent without mutating the caller's message.
func (m *AHPMessage) Clone() *AHPMessage {
	if m == nil {
		return nil
	}
	c := *m
	if m.Payload != nil {
		c.Payload = make(map[string]any, len(m.Payload))
		for k, v := range m.Payload {
			c.Payload[k] = v
		}
	}
	return &c
}

// NewMessage creates a new AHPMessage.
func NewMessage(method AHPMethod, agentID, targetAgent, taskID, sessionID string) *AHPMessage {
	return &AHPMessage{
		MessageID:   generateMessageID(),
		Method:      method,
		AgentID:     agentID,
		TargetAgent: targetAgent,
		TaskID:      taskID,
		SessionID:   sessionID,
		Payload:     make(map[string]any),
		Timestamp:   time.Now(),
	}
}

// NewTaskMessage creates a new TASK message.
// If payload is nil, the message keeps its default empty map.
func NewTaskMessage(agentID, targetAgent, taskID, sessionID string, payload map[string]any) *AHPMessage {
	msg := NewMessage(AHPMethodTask, agentID, targetAgent, taskID, sessionID)
	if payload != nil {
		msg.Payload = payload
	}
	return msg
}

// NewResultMessage creates a new RESULT message.
func NewResultMessage(agentID, targetAgent, taskID, sessionID string, result *models.TaskResult) *AHPMessage {
	msg := NewMessage(AHPMethodResult, agentID, targetAgent, taskID, sessionID)
	msg.Payload = map[string]any{
		"result": result,
	}
	return msg
}

// NewProgressMessage creates a new PROGRESS message.
func NewProgressMessage(agentID, targetAgent, taskID, sessionID string, progress float64) *AHPMessage {
	msg := NewMessage(AHPMethodProgress, agentID, targetAgent, taskID, sessionID)
	msg.Payload = map[string]any{
		"progress": progress,
	}
	return msg
}

// NewACKMessage creates a new ACK message.
func NewACKMessage(agentID, targetAgent, taskID, sessionID string) *AHPMessage {
	return NewMessage(AHPMethodACK, agentID, targetAgent, taskID, sessionID)
}

// NewHeartbeatMessage creates a new HEARTBEAT message.
func NewHeartbeatMessage(agentID string) *AHPMessage {
	return &AHPMessage{
		MessageID: generateMessageID(),
		Method:    AHPMethodHeartbeat,
		AgentID:   agentID,
		Timestamp: time.Now(),
	}
}

// IsTask checks if the message is a TASK message.
func (m *AHPMessage) IsTask() bool {
	return m.Method == AHPMethodTask
}

// IsResult checks if the message is a RESULT message.
func (m *AHPMessage) IsResult() bool {
	return m.Method == AHPMethodResult
}

// IsHeartbeat checks if the message is a HEARTBEAT message.
func (m *AHPMessage) IsHeartbeat() bool {
	return m.Method == AHPMethodHeartbeat
}

// GetResult extracts TaskResult from payload.
func (m *AHPMessage) GetResult() (*models.TaskResult, bool) {
	if m.Method != AHPMethodResult {
		return nil, false
	}

	// Try direct type assertion first (for in-memory objects)
	if result, ok := m.Payload["result"].(*models.TaskResult); ok {
		return result, true
	}

	// Handle JSON deserialized map[string]interface{}
	if resultMap, ok := m.Payload["result"].(map[string]any); ok {
		result := reconstructTaskResult(resultMap)
		if result != nil {
			return result, true
		}
	}

	return nil, false
}

// reconstructTaskResult reconstructs TaskResult from map after JSON deserialization.
func reconstructTaskResult(m map[string]any) *models.TaskResult {
	if m == nil {
		return nil
	}

	result := &models.TaskResult{
		Items: make([]*models.RecommendItem, 0),
	}

	// Reconstruct fields from map
	if v, ok := m["task_id"].(string); ok {
		result.TaskID = v
	}
	if v, ok := m["success"].(bool); ok {
		result.Success = v
	}
	if v, ok := m["error"].(string); ok {
		result.Error = v
	}
	if v, ok := m["reason"].(string); ok {
		result.Reason = v
	}

	// Reconstruct items
	if items, ok := m["items"].([]any); ok {
		for _, item := range items {
			if itemMap, ok := item.(map[string]any); ok {
				item := &models.RecommendItem{}
				if id, ok := itemMap["item_id"].(string); ok {
					item.ItemID = id
				}
				if name, ok := itemMap["name"].(string); ok {
					item.Name = name
				}
				if category, ok := itemMap["category"].(string); ok {
					item.Category = category
				}
				if price, ok := itemMap["price"].(float64); ok {
					item.Price = price
				}
				result.Items = append(result.Items, item)
			}
		}
	}

	return result
}

// GetProgress extracts progress from payload.
func (m *AHPMessage) GetProgress() (float64, bool) {
	if m.Method != AHPMethodProgress {
		return 0, false
	}
	progress, ok := m.Payload["progress"].(float64)
	return progress, ok
}

// getRandomSuffix returns a random numeric suffix for extra uniqueness.
// Returns "000000" if the crypto/rand read fails.
func getRandomSuffix() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "000000"
	}
	return fmt.Sprintf("%06d", n.Int64())
}

func generateMessageID() string {
	id := atomic.AddUint64(&messageIDCounter, 1)
	randSuffix := getRandomSuffix()
	return fmt.Sprintf("%s.%d.%s", time.Now().Format("20060102150405.000000"), id, randSuffix)
}

// MarshalJSON implements custom JSON marshaling.
func (m *AHPMessage) MarshalJSON() ([]byte, error) {
	type Alias AHPMessage
	return json.Marshal(&struct {
		*Alias
	}{
		Alias: (*Alias)(m),
	})
}
