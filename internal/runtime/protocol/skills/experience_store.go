package ares_skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// JSONExperienceStore persists ExperienceRecord sets as a JSON file. Writes
// are atomic: data is written to a temp file in the same directory and
// renamed over the target, so a crash never leaves a truncated store.
type JSONExperienceStore struct {
	path string
}

// NewJSONExperienceStore creates a JSON-file-backed Experience store.
//
// Args:
//   - path: the store file path (parent directory must be creatable).
//
// Returns:
//   - *JSONExperienceStore: ready to Load/Save.
func NewJSONExperienceStore(path string) *JSONExperienceStore {
	return &JSONExperienceStore{path: path}
}

// Load reads the persisted record set. A missing file yields an empty set.
//
// Args:
//   - ctx: unused (reserved for future cancellation).
//
// Returns:
//   - []ExperienceRecord: the persisted records (may be empty).
//   - error: wrapped error on read/decode failure, or nil.
func (s *JSONExperienceStore) Load(ctx context.Context) ([]ExperienceRecord, error) {
	data, err := os.ReadFile(s.path) //nolint:gosec // explicit store path from config
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("ares_skills: read experience store %s: %w", s.path, err)
	}
	var records []ExperienceRecord
	if len(data) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("ares_skills: decode experience store %s: %w", s.path, err)
	}
	return records, nil
}

// Save atomically persists the full record set as JSON.
//
// Args:
//   - ctx: unused (reserved for future cancellation).
//   - records: the record set to persist.
//
// Returns:
//   - error: wrapped error on encode/write failure, or nil.
func (s *JSONExperienceStore) Save(ctx context.Context, records []ExperienceRecord) error {
	if records == nil {
		records = []ExperienceRecord{}
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("ares_skills: encode experience store: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("ares_skills: mkdir experience store: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("ares_skills: write experience store: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("ares_skills: commit experience store: %w", err)
	}
	return nil
}
