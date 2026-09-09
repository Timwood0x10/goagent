// Package llm is the DEPRECATED public alias of internal/llmsvcapi (M5).
// New code MUST import internal/llmsvcapi; this package exists only for
// external consumers and is scheduled for removal.
package llm

import (
	"github.com/Timwood0x10/ares/internal/llmsvcapi"
)

// Config holds configuration for the LLM service.
// This is a public type that wraps the internal Config to avoid
// leaking internal package types into the public API.
type Config = llmsvcapi.Config

// Service wraps internal/llmservice.Service for public consumption.
type Service = llmsvcapi.Service

// NewService creates a new LLM service with the given config.
func NewService(cfg *Config) (*Service, error) { return llmsvcapi.NewService(cfg) }
