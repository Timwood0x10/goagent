// Package llmcore provides core abstractions and interfaces for the ARES API layer.
package llmcore

import "time"

// BaseConfig represents base configuration for all API services.
type BaseConfig struct {
	// RequestTimeout is the default timeout for API requests.
	RequestTimeout time.Duration
	// MaxRetries is the maximum number of retry attempts.
	MaxRetries int
	// RetryDelay is the delay between retry attempts.
	RetryDelay time.Duration
}

// Metadata represents optional metadata for API requests and responses.
type Metadata map[string]interface{}
