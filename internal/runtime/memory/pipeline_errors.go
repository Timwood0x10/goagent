// Package memory provides pipeline coordinator error definitions.
package memory

import "errors"

// ErrInvalidPipelineConfig is returned when a required Pipeline dependency is nil.
var ErrInvalidPipelineConfig = errors.New("invalid pipeline configuration")
