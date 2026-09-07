package models

import "time"

// Session represents a user conversation session.
// The struct fields are preserved for SQL scan compatibility; the behavior
// methods (NewSession/IsExpired/IsCompleted/AddTask/Progress/SetStatus) were
// removed as dead code — only tests referenced them.
type Session struct {
	SessionID   string           `json:"session_id"`
	UserID      string           `json:"user_id"`
	UserProfile *UserProfile     `json:"user_profile"`
	Input       string           `json:"input"`
	Status      SessionStatus    `json:"status"`
	Tasks       []*Task          `json:"tasks"`
	Results     []*TaskResult    `json:"results"`
	FinalOutput *RecommendResult `json:"final_output"`
	Metadata    map[string]any   `json:"metadata"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	ExpiredAt   time.Time        `json:"expired_at"`
}

// NewSession, IsExpired, IsCompleted, AddTask, AddResult, SetStatus,
// Progress removed as dead code (only tests referenced them).
