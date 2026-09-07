package ares_skills

import (
	"sort"
	"strings"
)

// Discovery searches the metadata index by keyword matching and returns
// top-K entries. It only ever sees Level-0 metadata: bodies are never loaded
// here. The scoring is deliberately simple (design §5: keyword match, FTS5 is
// a future extension point). When an FTS5 index is attached (SetFTS5),
// Search prefers it and falls back to keyword matching on any FTS5 failure.
type Discovery struct {
	entries []SkillIndexEntry
	fts     *FTS5Index
}

// NewDiscovery wraps an indexed entry set.
//
// Args:
//   - entries: metadata-only index entries.
//
// Returns:
//   - *Discovery: ready for keyword search.
func NewDiscovery(entries []SkillIndexEntry) *Discovery {
	return &Discovery{entries: entries}
}

// SetFTS5 attaches an FTS5 full-text index. When present, Search runs FTS5
// first and falls back to keyword matching if the query is not FTS5-safe.
//
// Args:
//   - idx: the FTS5 index over the same entry set (may be nil to detach).
func (d *Discovery) SetFTS5(idx *FTS5Index) {
	d.fts = idx
}

// Search returns the top-K entries matching the query. With an attached FTS5
// index, FTS5 MATCH is preferred; any FTS5 failure (unavailable or unsafe
// query) degrades to keyword matching.
//
// Args:
//   - query: free-text query (case-insensitive, whitespace-split).
//   - limit: maximum results to return (<= 0 means all).
//
// Returns:
//   - []SkillIndexEntry: ranked matches (may be empty).
func (d *Discovery) Search(query string, limit int) []SkillIndexEntry {
	if d.fts != nil && strings.TrimSpace(query) != "" {
		if ranked, err := d.fts.Search(query, limit, d.entries); err == nil && len(ranked) > 0 {
			return ranked
		}
		// FTS5 unavailable or no match: fall through to keyword matching.
	}
	return d.keywordSearch(query, limit)
}

// keywordSearch is the original term-count ranking matcher.
//
// Args:
//   - query: free-text query.
//   - limit: maximum results.
//
// Returns:
//   - []SkillIndexEntry: ranked matches (may be empty).
func (d *Discovery) keywordSearch(query string, limit int) []SkillIndexEntry {
	terms := splitTerms(query)
	if len(terms) == 0 {
		return nil
	}

	type scored struct {
		entry SkillIndexEntry
		score int
	}
	var scoredList []scored
	for _, e := range d.entries {
		score := matchScore(e, terms)
		if score > 0 {
			scoredList = append(scoredList, scored{entry: e, score: score})
		}
	}

	// Rank: score desc, then ID asc for determinism.
	sort.SliceStable(scoredList, func(i, j int) bool {
		if scoredList[i].score != scoredList[j].score {
			return scoredList[i].score > scoredList[j].score
		}
		return scoredList[i].entry.ID < scoredList[j].entry.ID
	})

	if limit > 0 && len(scoredList) > limit {
		scoredList = scoredList[:limit]
	}
	out := make([]SkillIndexEntry, 0, len(scoredList))
	for _, s := range scoredList {
		out = append(out, s.entry)
	}
	return out
}

// All returns the full index (for callers that need an unfiltered listing,
// e.g. envcap aggregation or admin surfaces).
//
// Returns:
//   - []SkillIndexEntry: all entries, sorted by ID.
func (d *Discovery) All() []SkillIndexEntry {
	out := make([]SkillIndexEntry, len(d.entries))
	copy(out, d.entries)
	return out
}

// Count returns the number of indexed skills.
//
// Returns:
//   - int: index size.
func (d *Discovery) Count() int {
	return len(d.entries)
}

// closeFTS5 releases the attached FTS5 index backing store.
//
// Returns:
//   - error: wrapped close error, or nil.
func (d *Discovery) closeFTS5() error {
	if d.fts == nil {
		return nil
	}
	return d.fts.Close()
}

// splitTerms lower-cases and whitespace-splits a query into unique terms.
//
// Args:
//   - query: raw query text.
//
// Returns:
//   - []string: normalized terms (may be empty).
func splitTerms(query string) []string {
	seen := make(map[string]bool)
	var terms []string
	for _, raw := range strings.Fields(strings.ToLower(query)) {
		term := strings.Trim(raw, ".,;:!?()[]{}")
		if term == "" || seen[term] {
			continue
		}
		seen[term] = true
		terms = append(terms, term)
	}
	return terms
}

// matchScore counts how many query terms hit a skill's searchable fields.
//
// Args:
//   - e: the candidate entry.
//   - terms: normalized query terms.
//
// Returns:
//   - int: matched-term count (0 means no match).
func matchScore(e SkillIndexEntry, terms []string) int {
	haystack := make([]string, 0, 2+len(e.Keywords)+len(e.Capabilities))
	haystack = append(haystack, strings.ToLower(e.ID), strings.ToLower(e.Name))
	haystack = append(haystack, lowerAll(e.Keywords)...)
	haystack = append(haystack, lowerAll(e.Capabilities)...)
	desc := strings.ToLower(e.Description)

	hits := 0
	for _, term := range terms {
		if containsAny(haystack, term) || strings.Contains(desc, term) {
			hits++
		}
	}
	return hits
}

// lowerAll lower-cases every element of a string slice.
//
// Args:
//   - in: source slice.
//
// Returns:
//   - []string: lower-cased copy.
func lowerAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(s)
	}
	return out
}

// containsAny reports whether any haystack element equals the needle.
//
// Args:
//   - haystack: candidate strings.
//   - needle: term to match.
//
// Returns:
//   - bool: true on any exact match.
func containsAny(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
