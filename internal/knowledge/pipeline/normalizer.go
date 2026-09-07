package pipeline

//nolint: errcheck // best-effort operations: ResponseWriter writes, cleanup Close/Wait, deferred shutdown
import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Timwood0x10/ares/internal/knowledge"
)

// ── DefaultNormalizer ───────────────────────────────────────────────────────

// DefaultNormalizer implements knowledge.Normalizer by converting Raw bytes
// into clean, standardized Normalized text. It strips control characters,
// normalizes whitespace, and trims leading/trailing whitespace.
type DefaultNormalizer struct {
	// MaxRawBytes sets the maximum Raw content length to process (0 = no limit).
	MaxRawBytes int
}

// Name returns the normalizer identifier.
func (n *DefaultNormalizer) Name() string { return "default-normalizer" }

// Normalize converts Raw bytes into clean Normalized text.
// If Raw is empty, it falls back to the existing Normalized or Summary text.
func (n *DefaultNormalizer) Normalize(_ context.Context, obj *knowledge.KnowledgeObject) (*knowledge.KnowledgeObject, error) {
	if obj == nil {
		return obj, nil
	}

	var raw string
	switch {
	case len(obj.Raw) > 0:
		if n.MaxRawBytes > 0 && len(obj.Raw) > n.MaxRawBytes {
			raw = truncateUTF8Safe(obj.Raw, n.MaxRawBytes)
		} else {
			raw = string(obj.Raw)
		}
	case obj.Normalized != "":
		// Already normalized, skip.
		return obj, nil
	case obj.Summary != "":
		raw = obj.Summary
	default:
		return obj, nil
	}

	normalized := cleanText(raw)
	obj.Normalized = normalized
	return obj, nil
}

// cleanText strips control characters, collapses whitespace, and trims.
func cleanText(s string) string {
	// Strip non-printable characters (keep newlines and tabs).
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\n' || r == '\t' || unicode.IsPrint(r) {
			b.WriteRune(r)
		} else if unicode.IsControl(r) {
			b.WriteRune(' ')
		}
	}
	cleaned := b.String()

	// Collapse multiple spaces/newlines into single space.
	segments := strings.Fields(cleaned)
	return strings.Join(segments, " ")
}

// ── DefaultSummarizer ───────────────────────────────────────────────────────

// DefaultSummarizer implements knowledge.Summarizer by taking the first N
// characters of Normalized text as the Summary. For production use, replace
// with an LLM-based summarizer.
type DefaultSummarizer struct {
	// MaxSummaryLen is the maximum length of the generated summary in characters.
	MaxSummaryLen int
}

// Name returns the summarizer identifier.
func (s *DefaultSummarizer) Name() string { return "default-summarizer" }

// Summarize generates a Summary from the Normalized text.
func (s *DefaultSummarizer) Summarize(_ context.Context, obj *knowledge.KnowledgeObject) (*knowledge.KnowledgeObject, error) {
	if obj == nil {
		return obj, nil
	}

	source := obj.Normalized
	if source == "" {
		source = obj.Summary
	}
	if source == "" && len(obj.Raw) > 0 {
		source = string(obj.Raw)
	}
	if source == "" {
		return obj, nil
	}

	maxLen := s.MaxSummaryLen
	if maxLen <= 0 {
		maxLen = 200
	}

	if len(source) <= maxLen {
		obj.Summary = source
	} else {
		// Truncate by RUNES, not bytes: source[:maxLen] on byte bounds
		// splits multi-byte UTF-8 sequences (CJK is 3 bytes/char) producing
		// invalid summaries. The word-boundary trim only applies when a space
		// exists well inside the truncated head (CJK has none).
		runes := []rune(source)
		if len(runes) <= maxLen {
			obj.Summary = source
			return obj, nil
		}
		head := string(runes[:maxLen])
		if idx := strings.LastIndex(head, " "); idx > maxLen/2 {
			head = head[:idx]
		}
		obj.Summary = head + "..."
	}

	return obj, nil
}

// ── DefaultEntityMatcher ────────────────────────────────────────────────────

// DefaultEntityMatcher implements knowledge.EntityMatcher by performing
// case-insensitive name/summary matching. It checks if the object's Normalized
// text or Summary overlaps significantly with existing entities.
type DefaultEntityMatcher struct {
	// MatchThreshold is the minimum similarity score [0, 1] to consider a match.
	MatchThreshold float64
}

// Name returns the matcher identifier.
func (m *DefaultEntityMatcher) Name() string { return "default-entity-matcher" }

// Match tries to match an object to an existing entity by comparing Normalized
// text and Summary overlap using Jaccard-like word overlap.
func (m *DefaultEntityMatcher) Match(_ context.Context, obj *knowledge.KnowledgeObject, candidates []*knowledge.KnowledgeObject) (*knowledge.ResolveResult, error) {
	if obj == nil || len(candidates) == 0 {
		return &knowledge.ResolveResult{IsNew: true}, nil
	}

	threshold := m.MatchThreshold
	if threshold <= 0 {
		threshold = 0.7
	}

	objWords := tokenize(obj.Normalized + " " + obj.Summary)

	var bestMatch string
	var bestScore float64

	for _, candidate := range candidates {
		if candidate.ID == obj.ID {
			continue
		}
		candWords := tokenize(candidate.Normalized + " " + candidate.Summary)
		score := jaccardOverlap(objWords, candWords)
		if score > bestScore {
			bestScore = score
			bestMatch = candidate.ID
		}
	}

	if bestScore >= threshold {
		return &knowledge.ResolveResult{
			MatchedObjectID: bestMatch,
			Confidence:      bestScore,
			IsNew:           false,
		}, nil
	}

	return &knowledge.ResolveResult{IsNew: true}, nil
}

// ── DefaultValidator ────────────────────────────────────────────────────────

// DefaultValidator implements knowledge.Validator by checking for basic
// conflicts (empty ID, mismatched types) and adjusting confidence.
type DefaultValidator struct{}

// Name returns the validator identifier.
func (v *DefaultValidator) Name() string { return "default-validator" }

// Validate checks the merged object for obvious conflicts.
func (v *DefaultValidator) Validate(_ context.Context, merged *knowledge.KnowledgeObject, sources []*knowledge.KnowledgeObject) (*knowledge.ValidationResult, error) {
	if merged == nil {
		return &knowledge.ValidationResult{Confidence: 0}, nil
	}

	result := &knowledge.ValidationResult{
		Confidence: merged.Confidence,
	}

	if merged.ID == "" {
		result.Confidence = 0
		result.Conflicts = append(result.Conflicts, knowledge.Conflict{
			Field:    "id",
			ValueA:   "",
			Strategy: "manual",
		})
	}

	// Check for type conflicts across sources.
	if len(sources) > 1 {
		firstType := sources[0].Type
		for i, src := range sources[1:] {
			if src.Type != "" && src.Type != firstType {
				result.Confidence *= 0.8
				result.Conflicts = append(result.Conflicts, knowledge.Conflict{
					Field:    "type",
					ValueA:   firstType,
					ValueB:   src.Type,
					Strategy: "take_higher_confidence",
				})
				_ = i
			}
		}
	}

	return result, nil
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// tokenize splits text into lowercase word tokens.
func tokenize(text string) map[string]int {
	tokens := make(map[string]int)
	for _, word := range strings.Fields(strings.ToLower(text)) {
		// Strip punctuation.
		word = strings.TrimFunc(word, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
		if word != "" {
			tokens[word]++
		}
	}
	return tokens
}

// jaccardOverlap computes the Jaccard similarity coefficient between two
// token sets. Returns a value in [0, 1].
func jaccardOverlap(a, b map[string]int) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1.0
	}
	intersection := 0
	for token := range a {
		if _, ok := b[token]; ok {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// truncateUTF8Safe cuts b to at most max bytes without splitting a UTF-8
// sequence: it walks back from the cut point to the last rune start so
// the result is always valid UTF-8.
func truncateUTF8Safe(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(b[cut]) {
		cut--
	}
	return string(b[:cut])
}
