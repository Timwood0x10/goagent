// Package ares_mcp ...
package ares_mcp

import "errors"

var (
	ErrDuplicateRegistration = errors.New("duplicate registration")
	ErrEmptyName             = errors.New("name must not be empty")
)
