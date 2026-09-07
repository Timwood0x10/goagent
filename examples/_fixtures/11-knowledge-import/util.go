// This file holds small shared helpers used across the example.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// wrapf wraps an error with a formatted, context-rich message while preserving
// the error chain via %w.
//
// Args:
//
//	err    - the underlying error, must be non-nil.
//	format - a printf-style context message.
//	args   - message arguments.
//
// Returns:
//
//	a wrapped error whose Unwrap yields err.
func wrapf(err error, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	return fmt.Errorf("%s: %w", msg, err)
}

// sha256Hex returns the hex-encoded SHA-256 digest of s.

// hashWithIndex combines content with chunk index to ensure unique hashes
// within a batch, avoiding PostgreSQL ON CONFLICT errors when two chunks
// have identical content (e.g. empty sections).
func hashWithIndex(content string, index int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", index, content)))
	return hex.EncodeToString(h[:])
}
