// Package memory provides unified memory management for the StyleAgent framework.
package memory

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

const errPrefix = "memory: "

// rollbackReasonMemoryConfig is the human-readable reason stamped on every
// rollback patch that restores the previous memory configuration. Kept as a
// constant so goconst stays quiet and the value is grep-able.
const rollbackReasonMemoryConfig = "rollback: restore previous memory config"

// MemoryConfigStore is the contract MemoryPatchExecutor depends on.
// Any memory manager that exposes a mutable, lockable MemoryConfig
// can implement this interface, decoupling the patch executor from a
// concrete struct (ProductionMemoryManager or memoryManager).
//
// Implementations must guarantee:
//   - GetConfig returns a non-nil pointer under the caller's lock.
//   - Lock/Unlock serialize config mutations.
type MemoryConfigStore interface {
	// GetConfig returns the current MemoryConfig pointer.
	// The caller must hold the lock before reading the returned config.
	GetConfig() *MemoryConfig
	// Lock acquires the exclusive lock protecting the config.
	Lock()
	// Unlock releases the exclusive lock protecting the config.
	Unlock()
}

// ── MemoryPatchExecutor ────────────────────────────────────

// MemoryPatchExecutor implements patch.RuntimeComponent for the Memory subsystem,
// enabling runtime evolution of memory configuration (history depth, session TTL,
// distilled task limits, etc.).
type MemoryPatchExecutor struct {
	store MemoryConfigStore
}

// NewMemoryPatchExecutor creates a RuntimeComponent adapter that reads and
// writes the MemoryConfig exposed by store.
//
// Args:
//   - store - the MemoryConfigStore backing the patch executor (must not be nil).
//
// Returns:
//   - *MemoryPatchExecutor - the configured executor.
func NewMemoryPatchExecutor(store MemoryConfigStore) *MemoryPatchExecutor {
	return &MemoryPatchExecutor{store: store}
}

// NewMinimalMemoryManager creates a lightweight ProductionMemoryManager that
// works without a database pool or embedding client. The MemoryPatchExecutor
// only needs the config field — it reads/writes memory configuration values
// without touching the database. Use this when the full ProductionMemoryManager
// is not available (e.g., default bootstrap path).
//
// WARNING: The returned *ProductionMemoryManager has nil dbPool,
// embeddingClient, ctxCleaner, and conversationRepository fields. Calling any
// method other than config-related accessors (e.g., AddMessage, BuildPromptMessages)
// will nil-panic. This is acceptable for the config-only use case but callers
// must not use it for full memory operations.
func NewMinimalMemoryManager() *ProductionMemoryManager {
	return &ProductionMemoryManager{
		config: DefaultMemoryConfig(),
	}
}

// Name returns the component identifier.
func (e *MemoryPatchExecutor) Name() string { return StorageMemory }

// Snapshot returns the current memory config as a snapshot.
func (e *MemoryPatchExecutor) Snapshot(_ context.Context) (any, error) {
	if e.store == nil {
		return nil, errors.New(errPrefix + "no config store available")
	}
	e.store.Lock()
	defer e.store.Unlock()

	cfg := e.store.GetConfig()
	if cfg == nil {
		return nil, errors.New(errPrefix + "no config available")
	}
	// Return a copy to avoid mutation via the snapshot.
	out := *cfg
	return &out, nil
}

// Apply patches the memory configuration. Supported patch types:
//   - PatchChangePlanner — change max_history or max_tasks
//   - PatchChangeBudget  — change max_distilled_tasks or session_ttl
//   - PatchChangeReducer — change clean_options
//
// Apply validates the patch before mutating config, so a malformed patch (e.g.
// an unparseable session_ttl) leaves the config untouched instead of applying a
// partial change. The returned rollback patch carries the previous values as a
// map[string]any in the same shape the forward Apply consumes, so re-applying
// the rollback actually restores the prior config.
func (e *MemoryPatchExecutor) Apply(ctx context.Context, p patch.RuntimePatch) (*patch.RuntimePatch, error) {
	if e.store == nil {
		return nil, errors.New(errPrefix + "no config store available")
	}

	e.store.Lock()
	defer e.store.Unlock()

	cfg := e.store.GetConfig()
	if cfg == nil {
		return nil, errors.New(errPrefix + "no config available")
	}
	// Snapshot the previous config so we can build a rollback and so a
	// validation failure leaves the config untouched (validate-before-mutate).
	prev := *cfg

	switch p.Type {
	case patch.PatchChangePlanner:
		vals, ok := p.Value.(map[string]any)
		if !ok {
			return nil, errors.New(errPrefix + "PatchChangePlanner value must be map[string]any")
		}
		rollback := map[string]any{}
		if h, ok := vals["max_history"].(int); ok && h > 0 {
			rollback["max_history"] = prev.MaxHistory
			cfg.MaxHistory = h
		}
		if t, ok := vals["max_tasks"].(int); ok && t > 0 {
			rollback["max_tasks"] = prev.MaxTasks
			cfg.MaxTasks = t
		}
		if s, ok := vals["max_sessions"].(int); ok && s > 0 {
			rollback["max_sessions"] = prev.MaxSessions
			cfg.MaxSessions = s
		}
		return &patch.RuntimePatch{
			Type:   p.Type,
			Target: p.Target,
			Value:  rollback,
			Reason: rollbackReasonMemoryConfig,
		}, nil

	case patch.PatchChangeBudget:
		vals, ok := p.Value.(map[string]any)
		if !ok {
			return nil, errors.New(errPrefix + "PatchChangeBudget value must be map[string]any")
		}
		// Validate all values before mutating, so a malformed field (e.g. an
		// unparseable session_ttl) does not partially apply the patch.
		rollback := map[string]any{}
		var newDistilled int
		distilledSet := false
		if d, ok := vals["max_distilled_tasks"].(int); ok && d > 0 {
			newDistilled = d
			distilledSet = true
		}
		var newTTL time.Duration
		ttlSet := false
		if t, ok := vals["session_ttl"].(string); ok && t != "" {
			dur, err := fmtDuration(t)
			if err != nil {
				return nil, fmt.Errorf(errPrefix+"invalid session_ttl: %w", err)
			}
			newTTL = dur
			ttlSet = true
		}
		if distilledSet {
			rollback["max_distilled_tasks"] = prev.MaxDistilledTasks
			cfg.MaxDistilledTasks = newDistilled
		}
		if ttlSet {
			rollback["session_ttl"] = prev.SessionTTL.String()
			cfg.SessionTTL = newTTL
		}
		return &patch.RuntimePatch{
			Type:   p.Type,
			Target: p.Target,
			Value:  rollback,
			Reason: rollbackReasonMemoryConfig,
		}, nil

	case patch.PatchChangeReducer:
		vals, ok := p.Value.(map[string]any)
		if !ok {
			return nil, errors.New(errPrefix + "PatchChangeReducer value must be map[string]any")
		}
		rollback := map[string]any{}
		if s, ok := vals["use_structured_cleaning"].(bool); ok {
			rollback["use_structured_cleaning"] = prev.UseStructuredCleaning
			cfg.UseStructuredCleaning = s
		}
		return &patch.RuntimePatch{
			Type:   p.Type,
			Target: p.Target,
			Value:  rollback,
			Reason: rollbackReasonMemoryConfig,
		}, nil

	default:
		return nil, fmt.Errorf(errPrefix+"unsupported patch type %s", p.Type)
	}
}

// CanApply returns nil if the patch type is supported.
func (e *MemoryPatchExecutor) CanApply(_ context.Context, p patch.RuntimePatch) error {
	switch p.Type {
	case patch.PatchChangePlanner, patch.PatchChangeBudget, patch.PatchChangeReducer:
		return nil
	default:
		return fmt.Errorf(errPrefix+"unsupported patch type %s", p.Type)
	}
}

// Ensure MemoryPatchExecutor implements patch.RuntimeComponent.
var _ patch.RuntimeComponent = (*MemoryPatchExecutor)(nil)

// fmtDuration parses a duration string like "24h", "30m", "7d".
// Supports days (d) in addition to Go's standard duration units.
func fmtDuration(s string) (time.Duration, error) {
	if len(s) > 1 && s[len(s)-1] == 'd' {
		days := 0
		if _, err := fmt.Sscanf(s, "%dd", &days); err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour, nil
		}
	}
	return time.ParseDuration(s)
}
