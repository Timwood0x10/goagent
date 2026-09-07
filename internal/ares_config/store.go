package ares_config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ConfigStore holds the current runtime configuration and a bounded history
// of reload attempts. It is the single source of truth for "what config is
// the process running with", readable by any consumer and updated by the
// file watcher.
//
// Design intent (keep it simple):
//   - Current() returns the last successfully loaded config (never a partial
//     or failed reload);
//   - Reload() re-reads the file, validates it, and only replaces the current
//     config on success (failed reloads are recorded in history, not applied);
//   - Watch() drives Reload from fsnotify events with a debounce, mirroring
//     the proven MCP config watcher pattern.
//
// The store does NOT push changes to subsystems: consumers poll Current() at
// their own cadence (e.g. the kernel loop interval). That keeps the surface
// small — four methods, no callbacks, no dependency graph.
type ConfigStore struct {
	mu      sync.RWMutex
	current *Config
	history []ConfigChange
	maxHist int
}

// ConfigChange records one reload attempt: success, message, and the config
// path involved.
type ConfigChange struct {
	Time    time.Time `json:"time"`
	OK      bool      `json:"ok"`
	Message string    `json:"message"`
}

// NewConfigStore creates a store seeded with the initial config (usually the
// one loaded at process start). history is bounded to 20 entries; older
// entries are dropped.
func NewConfigStore(initial *Config) *ConfigStore {
	if initial == nil {
		initial = &Config{}
	}
	return &ConfigStore{
		current: initial,
		history: make([]ConfigChange, 0, 20),
		maxHist: 20,
	}
}

// Current returns the last successfully loaded config. The returned pointer
// is the store's internal copy; callers must treat it as read-only.
func (s *ConfigStore) Current() *Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// History returns a copy of the reload-attempt history (newest last).
func (s *ConfigStore) History() []ConfigChange {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ConfigChange, len(s.history))
	copy(out, s.history)
	return out
}

// Reload re-reads and validates the config file, atomically replacing the
// current config on success. A failed reload is recorded but does not touch
// the current config — the process keeps running with the last good config.
func (s *ConfigStore) Reload(ctx context.Context, path string) error {
	cfg, err := Load(path)
	if err != nil {
		s.record(false, fmt.Sprintf("reload failed: %v", err))
		return fmt.Errorf("reload %s: %w", path, err)
	}
	// Publish the new config and record the history entry under one lock so
	// readers never observe the new config before its history entry exists.
	s.mu.Lock()
	s.current = cfg
	s.appendRecord(true, fmt.Sprintf("reloaded %s", filepath.Base(path)))
	s.mu.Unlock()
	return nil
}

// Watch starts an fsnotify loop on the config file (and its directory, to
// catch atomic-save renames). It blocks until ctx is cancelled; run it in a
// goroutine. Events are debounced 200ms so editor partial-writes coalesce
// into a single reload: a burst of config-targeted events (e.g. an
// atomic-save Write→Rename chain) resets the timer and, once it fires, any
// events that queued up during the window are drained before the single
// reload runs.
func (s *ConfigStore) Watch(ctx context.Context, path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create fsnotify watcher: %w", err)
	}
	defer func() { _ = w.Close() }()
	if err := w.Add(absPath); err != nil {
		return fmt.Errorf("watch config file: %w", err)
	}
	dir := filepath.Dir(absPath)
	if err := w.Add(dir); err != nil {
		return fmt.Errorf("watch config dir: %w", err)
	}

	const debounceDelay = 200 * time.Millisecond
	var debounce *time.Timer
	stopDebounce := func() {
		if debounce != nil {
			debounce.Stop()
		}
	}
	defer stopDebounce()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-w.Events:
			if !ok {
				return nil
			}
			if !configFileEvent(event, absPath) {
				continue
			}
			// (Re)arm the debounce window. A nil channel (no timer yet)
			// blocks the firing case below, so the timer only fires after
			// at least one config event arrived.
			if debounce == nil {
				debounce = time.NewTimer(debounceDelay)
			} else {
				// Always stop + drain + reset, regardless of Stop()'s return:
				// Stop()==true means the timer was still pending and would
				// otherwise be cancelled without a reload (events silently
				// dropped on editor fast-write bursts).
				if !debounce.Stop() {
					select {
					case <-debounce.C:
					default:
					}
				}
				debounce.Reset(debounceDelay)
			}
		case <-debounceC(debounce):
			// Debounce window elapsed: drain any events that queued up
			// during the window (non-blocking) so a rename chain coalesces
			// into one reload instead of N.
			for {
				select {
				case ev, ok := <-w.Events:
					if !ok {
						return nil
					}
					_ = ev // consumed for coalescing; already filtered above
				default:
					goto reload
				}
			}
		reload:
			_ = s.Reload(ctx, absPath)
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			s.record(false, fmt.Sprintf("watcher error: %v", err))
		}
	}
}

// debounceC returns the timer's channel, or a nil channel when there is no
// armed timer. A nil channel never fires, so the Watch select only reacts to
// timer expiry after a config event armed it.
func debounceC(t *time.Timer) <-chan time.Time {
	if t == nil {
		return nil
	}
	return t.C
}

// configFileEvent reports whether a fsnotify event targets the config file:
// direct write/create, or a rename/create on the directory that makes the
// file exist (atomic-save pattern).
func configFileEvent(event fsnotify.Event, cfgAbs string) bool {
	evAbs, _ := filepath.Abs(event.Name)
	if evAbs == cfgAbs {
		return event.Has(fsnotify.Write) || event.Has(fsnotify.Create)
	}
	if event.Has(fsnotify.Rename) || event.Has(fsnotify.Create) {
		if _, err := os.Stat(cfgAbs); err == nil {
			return true
		}
	}
	return false
}

func (s *ConfigStore) record(ok bool, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendRecord(ok, msg)
}

// appendRecord appends a history entry under the caller-held lock, trimming
// to the bounded history size.
func (s *ConfigStore) appendRecord(ok bool, msg string) {
	s.history = append(s.history, ConfigChange{Time: time.Now(), OK: ok, Message: msg})
	if len(s.history) > s.maxHist {
		s.history = s.history[len(s.history)-s.maxHist:]
	}
}
