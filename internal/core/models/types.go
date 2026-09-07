package models

import (
	"time"
)

// Gender represents user gender.
type Gender string

const (
	GenderMale   Gender = "male"
	GenderFemale Gender = "female"
	GenderOther  Gender = "other"
)

// StyleTag represents user preference style tags.
type StyleTag string

const (
	Sporty          StyleTag = "sporty"
	StyleMinimalist StyleTag = "minimalist"
	StyleVintage    StyleTag = "vintage"
	StyleBohemian   StyleTag = "bohemian"
)

// Occasion represents usage scenarios.
type Occasion string

const (
	OccasionWork     Occasion = "work"
	OccasionSports   Occasion = "sports"
	OccasionFormal   Occasion = "formal"
	OccasionVacation Occasion = "vacation"
)

// SessionStatus represents session state.
type SessionStatus string

const (
	SessionStatusPending    SessionStatus = "pending"
	SessionStatusProcessing SessionStatus = "processing"
	SessionStatusCompleted  SessionStatus = "completed"
	SessionStatusFailed     SessionStatus = "failed"
	SessionStatusExpired    SessionStatus = "expired"
)

// AgentType represents agent types.
type AgentType string

const (
	AgentTypeTop    AgentType = "agent_top"
	AgentTypeBottom AgentType = "agent_bottom"

	// Travel agent types
	AgentTypeDestination AgentType = "destination"
	AgentTypeFood        AgentType = "food"
	AgentTypeHotel       AgentType = "hotel"
	AgentTypeItinerary   AgentType = "itinerary"
)

// AgentStatus represents agent running state.
type AgentStatus string

const (
	AgentStatusStarting AgentStatus = "starting"
	AgentStatusReady    AgentStatus = "ready"
	AgentStatusBusy     AgentStatus = "busy"
	AgentStatusStopping AgentStatus = "stopping"
	AgentStatusOffline  AgentStatus = "offline"
)

// ParseAgentStatus removed as dead code (only tests referenced it).
// AgentStatus constants are retained for SQL scan compatibility.

// PriceRange represents budget range.
type PriceRange struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// NewPriceRange removed as dead code (only tests referenced it).
// PriceRange type retained for SQL scan compatibility.

// IsValid checks if the price range is valid.
func (p *PriceRange) IsValid() bool {
	return p != nil && p.Min >= 0 && p.Max >= p.Min
}

// Contains checks if the price is within range.
func (p *PriceRange) Contains(price float64) bool {
	if !p.IsValid() {
		return false
	}
	return price >= p.Min && price <= p.Max
}

// Default TTL durations for session and task lifecycle.
const (
	DefaultSessionTTL = 24 * time.Hour
	DefaultTaskTTL    = 1 * time.Hour
)
