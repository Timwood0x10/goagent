package builtin

import (
	"context"
	"fmt"
	"regexp"

	"github.com/Timwood0x10/ares/internal/tools/resources/base"
	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// RegexTool provides regex operations for text processing.
type RegexTool struct {
	*base.BaseTool
}

// NewRegexTool creates a new RegexTool.
func NewRegexTool() *RegexTool {
	params := &core.ParameterSchema{
		Type: "object",
		Properties: map[string]*core.Parameter{
			"operation": {
				Type:        "string",
				Description: "Operation to perform (match, extract, replace)",
				Enum:        []interface{}{"match", "extract", "replace"},
			},
			"text": {
				Type:        "string",
				Description: "Text to process",
			},
			"pattern": {
				Type:        "string",
				Description: "Regex pattern to use",
			},
			"flags": {
				Type:        "array",
				Description: "Regex flags (i: case-insensitive, m: multiline, s: dotall)",
			},
			"replacement": {
				Type:        "string",
				Description: "Replacement string (required for replace operation)",
			},
			"max_results": {
				Type:        "integer",
				Description: "Maximum number of results to return (default: unlimited)",
			},
		},
		Required: []string{"operation", "text", "pattern"},
	}

	return &RegexTool{
		BaseTool: base.NewBaseToolWithCapabilities("regex_tool", "Perform regex match, extract, and replace operations", core.CategoryCore, []core.Capability{core.CapabilityText}, params),
	}
}

// maxRegexInputSize limits the input text size for regex operations to prevent
// ReDoS attacks via catastrophic backtracking on very large inputs.
const maxRegexInputSize = 10 * 1024 * 1024 // 10MB

// defaultMaxRegexResults caps the number of matches returned when the caller
// does not specify max_results. -1 (unlimited) on a broad pattern over large
// input can produce millions of matches and exhaust memory.
const defaultMaxRegexResults = 1000

// Execute performs the regex operation.
func (t *RegexTool) Execute(ctx context.Context, params map[string]interface{}) (core.Result, error) {
	operation, ok := params["operation"].(string)
	if !ok || operation == "" {
		return core.NewErrorResult("operation is required"), nil
	}

	text, ok := params["text"].(string)
	if !ok || text == "" {
		return core.NewErrorResult("text is required"), nil
	}

	// Limit input size to prevent ReDoS via catastrophic backtracking.
	if len(text) > maxRegexInputSize {
		return core.NewErrorResult(fmt.Sprintf("input text exceeds maximum size of %d bytes", maxRegexInputSize)), nil
	}

	pattern, ok := params["pattern"].(string)
	if !ok || pattern == "" {
		return core.NewErrorResult("pattern is required"), nil
	}

	// Limit pattern complexity to prevent ReDoS.
	if len(pattern) > 1000 {
		return core.NewErrorResult("regex pattern exceeds maximum length of 1000 characters"), nil
	}

	// Parse flags
	flags := getStringSlice(params, "flags")
	re, err := t.compileRegex(pattern, flags)
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("invalid regex pattern: %v", err)), nil
	}

	switch operation {
	case "match":
		maxResults := getInt(params, "max_results", defaultMaxRegexResults)
		return t.match(ctx, text, re, maxResults)
	case "extract":
		maxResults := getInt(params, "max_results", defaultMaxRegexResults)
		return t.extract(ctx, text, re, maxResults)
	case "replace":
		replacement, ok := params["replacement"].(string)
		if !ok || replacement == "" {
			return core.NewErrorResult("replacement is required for replace operation"), nil
		}
		return t.replace(ctx, text, re, replacement)
	default:
		return core.NewErrorResult(fmt.Sprintf("unsupported operation: %s", operation)), nil
	}
}

// compileRegex compiles a regex pattern with flags.
func (t *RegexTool) compileRegex(pattern string, flags []string) (*regexp.Regexp, error) {
	// Build regex flags
	var regexFlags string
	for _, flag := range flags {
		regexFlags += "(?" + flag + ")"
	}

	// If flags are specified, prepend them to pattern
	if regexFlags != "" {
		pattern = regexFlags + pattern
	}

	return regexp.Compile(pattern)
}

// match checks if the pattern matches the text.
func (t *RegexTool) match(ctx context.Context, text string, re *regexp.Regexp, maxResults int) (core.Result, error) {
	matches := re.FindAllString(text, maxResults)

	// Get all match positions
	matchPositions := re.FindAllStringIndex(text, maxResults)

	results := make([]map[string]interface{}, 0, len(matches))
	for i, match := range matches {
		result := map[string]interface{}{
			"match": match,
		}

		if i < len(matchPositions) {
			result["start"] = matchPositions[i][0]
			result["end"] = matchPositions[i][1]
		}

		results = append(results, result)
	}

	return core.NewResult(true, map[string]interface{}{
		"operation":   "match",
		"pattern":     re.String(),
		"matched":     len(matches) > 0,
		"matches":     results,
		"match_count": len(matches),
	}), nil
}

// extract extracts all matches using capturing groups.
func (t *RegexTool) extract(ctx context.Context, text string, re *regexp.Regexp, maxResults int) (core.Result, error) {
	allMatches := re.FindAllStringSubmatch(text, maxResults)

	if len(allMatches) == 0 {
		return core.NewResult(true, map[string]interface{}{
			"operation":   "extract",
			"pattern":     re.String(),
			"matched":     false,
			"extracted":   []interface{}{},
			"match_count": 0,
		}), nil
	}

	// Extract capturing groups
	extracted := make([]map[string]interface{}, 0, len(allMatches))
	for _, match := range allMatches {
		groups := make([]string, 0, len(match))
		for i, group := range match {
			groups = append(groups, fmt.Sprintf("group_%d: %s", i, group))
		}

		extracted = append(extracted, map[string]interface{}{
			"full_match": match[0],
			"groups":     groups,
			"count":      len(match),
		})
	}

	return core.NewResult(true, map[string]interface{}{
		"operation":   "extract",
		"pattern":     re.String(),
		"matched":     true,
		"extracted":   extracted,
		"match_count": len(extracted),
	}), nil
}

// replace replaces all matches with the replacement string.
func (t *RegexTool) replace(ctx context.Context, text string, re *regexp.Regexp, replacement string) (core.Result, error) {
	// Count matches before replacement
	matchCount := len(re.FindAllString(text, -1))

	// Perform replacement
	result := re.ReplaceAllString(text, replacement)

	return core.NewResult(true, map[string]interface{}{
		"operation":    "replace",
		"pattern":      re.String(),
		"replacement":  replacement,
		"original":     text,
		"result":       result,
		"match_count":  matchCount,
		"replacements": matchCount,
	}), nil
}

func (t *RegexTool) IsIdempotent() bool { return true }
