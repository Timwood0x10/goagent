package ares_skills

// IndexChange describes the difference between two index generations
// (design §5: the index entry carries a content Hash for change detection;
// MCP listChanged and skill version upgrades rebuild incrementally).
type IndexChange struct {
	// Added are skills present in the new index but absent in the old one.
	Added []SkillIndexEntry
	// Modified are skills present in both whose content hash changed.
	Modified []SkillIndexEntry
	// Removed are skills present in the old index but absent in the new one.
	Removed []SkillIndexEntry
}

// DetectIndexChanges diffs two index generations by (ID, Source, Hash).
// A skill is Modified when its ID+Source match but its Hash differs;
// otherwise it is Added or Removed. The result is deterministic.
//
// Args:
//   - prev: the previous index generation (may be empty).
//   - next: the new index generation.
//
// Returns:
//   - IndexChange: the computed diff.
func DetectIndexChanges(prev, next []SkillIndexEntry) IndexChange {
	keyOf := func(e SkillIndexEntry) string {
		return string(e.Source) + "\x00" + e.ID
	}

	prevByKey := make(map[string]SkillIndexEntry, len(prev))
	for _, e := range prev {
		prevByKey[keyOf(e)] = e
	}
	seen := make(map[string]bool, len(next))

	var change IndexChange
	for _, e := range next {
		key := keyOf(e)
		seen[key] = true
		if old, ok := prevByKey[key]; ok {
			if old.Hash != e.Hash {
				change.Modified = append(change.Modified, e)
			}
		} else {
			change.Added = append(change.Added, e)
		}
	}
	for key, old := range prevByKey {
		if !seen[key] {
			change.Removed = append(change.Removed, old)
		}
	}
	return change
}
