// Package evolution provides error definitions for the evolution service.
package evolution

import "errors"

var (
	// ErrNilConfig is returned when a nil config is passed to NewService.
	ErrNilConfig = errors.New("evolution config must not be nil")

	// ErrNilBaseStrategy is returned when base strategy in config is nil.
	ErrNilBaseStrategy = errors.New("base strategy must not be nil")

	// ErrInvalidRate is returned when a rate parameter is outside [0, 1].
	ErrInvalidRate = errors.New("rate must be between 0 and 1")

	// ErrNotInitialized is returned when the system has not been initialized.
	ErrNotInitialized = errors.New("evolution system not initialized")
)
