// Package llmcore provides core abstractions for memory operations.
package llmcore

import "time"

// MessageRole represents the role of a message sender.
type MessageRole string

const (
	// MessageRoleSystem represents a system message.
	MessageRoleSystem MessageRole = "system"
	// MessageRoleUser represents a user message.
	MessageRoleUser MessageRole = "user"
	// MessageRoleAssistant represents an assistant message.
	MessageRoleAssistant MessageRole = "assistant"
	// MessageRoleTool represents a tool/function call message.
	MessageRoleTool MessageRole = "tool"
)

// Message represents a conversation message.
type Message struct {
	// ID is the unique identifier for the message.
	ID string
	// SessionID is the session this message belongs to.
	SessionID string
	// Role is the role of the message sender.
	Role MessageRole
	// Content is the message content.
	Content string
	// Time is the timestamp when the message was created.
	Time time.Time
	// Metadata is optional metadata.
	Metadata Metadata

	// TurnID identifies the ReAct turn this message belongs to for causal tracing.
	TurnID string
	// EventKind categorises the message for event-sourcing reconstruction.
	EventKind string
	// ParentID references the tool_call_id or message this is a response to.
	ParentID string
	// ArtifactRefs lists external artifact references produced by this message.
	ArtifactRefs []string
}
