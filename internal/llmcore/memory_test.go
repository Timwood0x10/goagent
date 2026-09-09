package llmcore

import (
	"testing"
	"time"
)

// TestMessageRole tests MessageRole constants.
func TestMessageRole(t *testing.T) {
	tests := []struct {
		name string
		role MessageRole
		want string
	}{
		{
			name: "system role",
			role: MessageRoleSystem,
			want: "system",
		},
		{
			name: "user role",
			role: MessageRoleUser,
			want: "user",
		},
		{
			name: "assistant role",
			role: MessageRoleAssistant,
			want: "assistant",
		},
		{
			name: "tool role",
			role: MessageRoleTool,
			want: "tool",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(tt.role); got != tt.want {
				t.Errorf("MessageRole = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestMessageRoleUniqueness ensures each role constant maps to a distinct value.
func TestMessageRoleUniqueness(t *testing.T) {
	seen := make(map[MessageRole]bool)
	for _, role := range []MessageRole{MessageRoleSystem, MessageRoleUser, MessageRoleAssistant, MessageRoleTool} {
		if seen[role] {
			t.Errorf("duplicate MessageRole value: %q", role)
		}
		seen[role] = true
	}
}

// TestMessage verifies Message struct construction and field access.
func TestMessage(t *testing.T) {
	ts := time.Now()
	m := Message{
		ID:        "msg-1",
		SessionID: "sess-1",
		Role:      MessageRoleUser,
		Content:   "hello",
		Time:      ts,
		TurnID:    "turn-1",
		ParentID:  "tool-call-1",
	}

	if m.ID != "msg-1" || m.SessionID != "sess-1" || m.Role != MessageRoleUser {
		t.Errorf("Message fields not set correctly: %+v", m)
	}
	if m.Content != "hello" {
		t.Errorf("Content = %q, want hello", m.Content)
	}
	if !m.Time.Equal(ts) {
		t.Errorf("Time not preserved")
	}
}
