package ares_skills

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ExperienceStore persists relevance priors (design §11: reuse the
// refine.Store style — Get/Set by key). A nil store keeps Experience purely
// in memory.
type ExperienceStore interface {
	// Load returns all persisted records (empty slice when none).
	Load(ctx context.Context) ([]ExperienceRecord, error)
	// Save atomically persists the full record set.
	Save(ctx context.Context, records []ExperienceRecord) error
}

// Experience is the Learned Source: it records task-pattern -> skill relevance
// priors (design §11). It never invokes skills — it only biases future
// discovery ranking. A learned skill is indexable but never auto-executed.
type Experience struct {
	mu      sync.RWMutex
	records []ExperienceRecord
	// maxRecords caps the in-memory record set.
	maxRecords int
	// store, when non-nil, persists records across restarts.
	store ExperienceStore
}

// NewExperience creates an in-memory Experience store.
//
// Returns:
//   - *Experience: ready to record and query relevance priors.
func NewExperience() *Experience {
	return &Experience{maxRecords: 1000}
}

// NewExperienceWithStore creates an Experience backed by a persistent store
// and pre-loads any previously saved records. Load errors are non-fatal: the
// store starts empty and the next Record retries persistence.
//
// Args:
//   - ctx: context for the initial load.
//   - store: the persistent store (nil means in-memory only).
//
// Returns:
//   - *Experience: ready to use, populated from the store when possible.
func NewExperienceWithStore(ctx context.Context, store ExperienceStore) *Experience {
	e := NewExperience()
	e.store = store
	if store != nil {
		if loaded, err := store.Load(ctx); err == nil {
			e.records = loaded
		}
	}
	return e
}

// Record stores or updates a {skill, task_pattern, success_rate} prior.
// Re-recording the same (skill, pattern) pair replaces its success rate.
//
// Args:
//   - skill: the skill ID.
//   - taskPattern: the task pattern.
//   - successRate: observed success rate, clamped to [0,1].
//
// Returns:
//   - error: wrapped error when arguments are empty.
//
// maxPatternLength caps stored experience task patterns (in runes) so an
// overlong precise task description cannot bloat experience.json or slow down
// BestMatch matching. It is the single length standard shared with the
// outcome recorder's skillTaskPattern (256 chars ≈ 256 runes for typical
// text; rune-safe truncation never breaks UTF-8).
const maxPatternLength = 256

// capPatternLength truncates a task pattern to maxPatternLength runes,
// preserving the prefix — the part short follow-up queries most often match.
// Returns the input unchanged when already within the bound.
//
// Args:
//   - pattern: the raw task pattern.
//
// Returns:
//   - string: the bounded pattern (never longer than maxPatternLength runes).
func capPatternLength(pattern string) string {
	if len(pattern) <= maxPatternLength {
		return pattern
	}
	runes := []rune(pattern)
	if len(runes) <= maxPatternLength {
		return pattern
	}
	return string(runes[:maxPatternLength])
}

func (e *Experience) Record(skill, taskPattern string, successRate float64) error {
	if skill == "" || taskPattern == "" {
		return errors.New("ares_skills: experience record needs skill and task pattern")
	}
	if successRate < 0 {
		successRate = 0
	}
	if successRate > 1 {
		successRate = 1
	}
	// Cap the pattern length: precise task descriptions (task_desc) may be
	// arbitrarily long; a bounded pattern keeps experience.json compact and
	// BestMatch matching fast. Truncation keeps the prefix, which is the part
	// short follow-up queries most often match.
	taskPattern = capPatternLength(taskPattern)
	rec := ExperienceRecord{
		Skill:       skill,
		TaskPattern: taskPattern,
		SuccessRate: successRate,
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	for i, r := range e.records {
		if r.Skill == skill && r.TaskPattern == taskPattern {
			e.records[i] = rec
			return e.persistLocked()
		}
	}
	if len(e.records) >= e.maxRecords {
		// Drop the oldest record to bound memory.
		e.records = append(e.records[1:], rec)
		return e.persistLocked()
	}
	e.records = append(e.records, rec)
	return e.persistLocked()
}

// persistLocked writes the current record set to the store when one is
// attached. Caller must hold the write lock.
//
// Returns:
//   - error: wrapped store error, or nil (no store = no-op).
func (e *Experience) persistLocked() error {
	if e.store == nil {
		return nil
	}
	records := make([]ExperienceRecord, len(e.records))
	copy(records, e.records)
	if err := e.store.Save(context.Background(), records); err != nil {
		return fmt.Errorf("ares_skills: persist experience: %w", err)
	}
	return nil
}

// BestMatch returns the highest-success-rate skill for a task pattern, or
// ok=false when nothing matches. Matching is keyword-overlap based:
//
//   - short patterns (a few tokens, e.g. the fallback "agent_top") use plain
//     substring containment, preserving the original coarse-match semantics;
//   - long patterns (full task descriptions) are split into keywords and
//     scored by the ratio of overlapping tokens, so two verbose descriptions
//     that merely share a common word do not spuriously match each other.
//
// Args:
//   - taskPattern: the task pattern to match.
//
// Returns:
//   - ExperienceRecord: the best prior.
//   - bool: true when a match exists.
func (e *Experience) BestMatch(taskPattern string) (ExperienceRecord, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	needle := strings.ToLower(strings.TrimSpace(taskPattern))
	needleTokens := tokenize(needle)
	best := ExperienceRecord{}
	bestScore := 0.0
	found := false
	for _, r := range e.records {
		score := patternMatchScore(r.TaskPattern, needle, needleTokens)
		if score < matchScoreThreshold {
			continue
		}
		if !found || score > bestScore {
			best = r
			bestScore = score
			found = true
		}
	}
	return best, found
}

// matchScoreThreshold is the minimum keyword-overlap score for a long-pattern
// match. A score below it means the stored prior and the query share only
// incidental words (e.g. one common token in a six-word query), which must not
// count as a match.
const matchScoreThreshold = 0.5

// tokenize splits a lowercased pattern into significant keyword tokens. Runs
// of separators (space, punctuation) split tokens; tokens of pure punctuation
// are dropped.
//
// Args:
//   - s: the lowercased pattern.
//
// Returns:
//   - []string: the token list (may be empty).
func tokenize(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			return false
		default:
			return true
		}
	})
	if len(fields) == 0 {
		return nil
	}
	tokens := make([]string, 0, len(fields))
	for _, f := range fields {
		if f != "" {
			tokens = append(tokens, f)
		}
	}
	return tokens
}

// patternMatchScore scores how well a stored pattern matches the needle.
// When the needle is short (fewer than 4 tokens, i.e. a coarse fallback like
// "agent_top" or a keyword query), substring containment is the test —
// mirroring the legacy BestMatch behavior and returning 1.0 (or 0). For
// longer needles, the score is the fraction of needle tokens present in the
// stored pattern's token set; BestMatch compares this against
// matchScoreThreshold (0.5), so a shared incidental word yields a low score
// that is rejected, while a genuine overlap yields a high score that matches.
//
// Args:
//   - stored: the stored record's TaskPattern.
//   - needle: the lowercased query.
//   - needleTokens: pre-tokenized needle (avoids recomputation per record).
//
// Returns:
//   - float64: match score, 0 means no match.
func patternMatchScore(stored, needle string, needleTokens []string) float64 {
	storedLower := strings.ToLower(stored)
	if len(needleTokens) < 4 {
		if strings.Contains(storedLower, needle) || strings.Contains(needle, storedLower) {
			return 1.0
		}
		return 0
	}
	storedTokens := tokenize(storedLower)
	if len(storedTokens) == 0 {
		return 0
	}
	set := make(map[string]struct{}, len(storedTokens))
	for _, tok := range storedTokens {
		set[tok] = struct{}{}
	}
	hit := 0
	for _, tok := range needleTokens {
		if _, ok := set[tok]; ok {
			hit++
		}
	}
	return float64(hit) / float64(len(needleTokens))
}

// List returns a deterministic snapshot of all records, sorted by skill then
// task pattern.
//
// Returns:
//   - []ExperienceRecord: a copy of the record set.
func (e *Experience) List() []ExperienceRecord {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]ExperienceRecord, len(e.records))
	copy(out, e.records)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Skill != out[j].Skill {
			return out[i].Skill < out[j].Skill
		}
		return out[i].TaskPattern < out[j].TaskPattern
	})
	return out
}

// Count returns the number of recorded priors.
//
// Returns:
//   - int: record count.
func (e *Experience) Count() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.records)
}
