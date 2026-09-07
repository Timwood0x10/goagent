package models

import (
	"time"

	"github.com/Timwood0x10/ares/internal/errors"
)

// UserProfile represents user profile information.
type UserProfile struct {
	UserID      string         `json:"user_id"`
	Name        string         `json:"name"`
	Gender      Gender         `json:"gender"`
	Age         int            `json:"age"`
	Occupation  string         `json:"occupation"`
	Style       []StyleTag     `json:"style"`
	Budget      *PriceRange    `json:"budget"`
	Colors      []string       `json:"colors"`
	Occasions   []Occasion     `json:"occasions"`
	BodyType    string         `json:"body_type"`
	Preferences map[string]any `json:"preferences"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// NewUserProfile creates a new UserProfile.
func NewUserProfile(userID, name string) *UserProfile {
	now := time.Now()
	return &UserProfile{
		UserID:      userID,
		Name:        name,
		Preferences: make(map[string]any),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// Validate validates the user profile.
func (p *UserProfile) Validate() error {
	if p == nil {
		return errors.ErrInvalidUserID
	}
	if p.UserID == "" {
		return errors.ErrInvalidUserID
	}
	if p.Age < 0 || p.Age > 150 {
		return errors.ErrInvalidAge
	}
	if p.Budget != nil && !p.Budget.IsValid() {
		return errors.ErrInvalidBudget
	}
	return nil
}

// HasStyle, HasOccasion removed as dead code (only tests referenced them).

// UserFeedback represents user feedback on recommendations.
type UserFeedback struct {
	Liked   bool   `json:"liked"`
	Comment string `json:"comment"`
	Rating  int    `json:"rating"`
}

// IsValid checks if the rating is valid.
func (f *UserFeedback) IsValid() bool {
	return f != nil && f.Rating >= 1 && f.Rating <= 5
}

// SetRating removed as dead code (only tests referenced it).
