// Package mutation ...
package mutation

import "errors"

var (
	ErrNilParent    = errors.New("parent strategy must not be nil")
	ErrInvalidCount = errors.New("mutation count must be positive")
)
