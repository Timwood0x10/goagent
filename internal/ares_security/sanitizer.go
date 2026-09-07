package ares_security

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// SensitiveFieldType represents different types of sensitive fields.
type SensitiveFieldType string

const (
	// SensitiveFieldTypeAPIKey represents API keys
	SensitiveFieldTypeAPIKey SensitiveFieldType = "api_key"
	// SensitiveFieldTypePassword represents passwords
	SensitiveFieldTypePassword SensitiveFieldType = "password"
	// SensitiveFieldTypeToken represents tokens
	SensitiveFieldTypeToken SensitiveFieldType = "token"
	// SensitiveFieldTypeSecret represents secrets
	SensitiveFieldTypeSecret SensitiveFieldType = "secret"
	// SensitiveFieldTypeEmail represents email addresses
	SensitiveFieldTypeEmail SensitiveFieldType = "email"
	// SensitiveFieldTypePhone represents phone numbers
	SensitiveFieldTypePhone SensitiveFieldType = "phone"
	// SensitiveFieldTypeSSN represents social security numbers
	SensitiveFieldTypeSSN SensitiveFieldType = "ssn"
	// SensitiveFieldTypeCreditCard represents credit card numbers
	SensitiveFieldTypeCreditCard SensitiveFieldType = "credit_card"
	// SensitiveFieldTypePersonalInfo represents personal information
	SensitiveFieldTypePersonalInfo SensitiveFieldType = "personal_info"
)

// Sanitizer handles sensitive information redaction.
type Sanitizer struct {
	patterns []SensitivePattern
	options  SanitizeOptions
}

// SanitizeOptions controls sanitization behavior.
//
// All three fields are honored by Sanitizer.Sanitize: MaskChar replaces
// the default '*' in masked output, PreserveLengthFor overrides the preserved
// visible prefix for a field type, and KeepLength pads masked output to the
// original length. Zero-value fields fall back to the built-in mask behavior.
type SanitizeOptions struct {
	// KeepLength pads the masked output with MaskChar so its rune length
	// matches the original input. Masks that are already longer than the
	// original (fixed-format masks such as email) are left unchanged.
	KeepLength bool
	// MaskChar is the character used for masking; zero value means '*'.
	MaskChar rune
	// PreserveLengthFor keeps exactly N leading characters of the original
	// match visible for the given field type, replacing the mask function's
	// built-in preserve behavior. Absent entries keep the defaults baked
	// into each mask function. Negative values are ignored.
	PreserveLengthFor map[SensitiveFieldType]int
}

// SensitivePattern defines a pattern for detecting sensitive information.
type SensitivePattern struct {
	Type        SensitiveFieldType
	Pattern     *regexp.Regexp
	MaskFunc    func(string) string
	Description string
}

// NewSanitizer creates a new sanitizer with default patterns.
func NewSanitizer() *Sanitizer {
	return &Sanitizer{
		patterns: defaultSensitivePatterns(),
		options:  DefaultSanitizeOptions(),
	}
}

// DefaultSanitizeOptions returns default sanitization options.
// DefaultSanitizeOptions returns default sanitization options.
//
// PreserveLengthFor is intentionally empty: the per-type preserved-character
// defaults live in the mask functions themselves (maskAPIKey, maskPhone, …).
// An empty map keeps "map entry present" meaning an explicit caller override
// in applyMaskOptions.
func DefaultSanitizeOptions() SanitizeOptions {
	return SanitizeOptions{
		KeepLength: false,
		MaskChar:   '*',
	}
}

// defaultSensitivePatterns returns default patterns for detecting sensitive information.
func defaultSensitivePatterns() []SensitivePattern {
	return []SensitivePattern{
		{
			Type:        SensitiveFieldTypeAPIKey,
			Pattern:     regexp.MustCompile(`(?i)(api[_-]?key|apikey|secret[_-]?key|token[_-]?key)[:\s]+["']?([a-zA-Z0-9_\-\.]+)["']?`),
			MaskFunc:    maskAPIKey,
			Description: "API keys (api_key, secret_key, token_key)",
		},
		{
			Type:        SensitiveFieldTypePassword,
			Pattern:     regexp.MustCompile(`(?i)\b(password|passwd|pwd)\b[=:\s]+["']?([^"'\s]+)["']?`),
			MaskFunc:    maskPassword,
			Description: "Passwords",
		},
		{
			Type:        SensitiveFieldTypeToken,
			Pattern:     regexp.MustCompile(`(?i)(token|bearer[:\s]+|authorization[:\s]+bearer)[:\s]+["']?([a-zA-Z0-9_\-\.]+)["']?`),
			MaskFunc:    maskToken,
			Description: "Authentication tokens",
		},
		{
			Type:        SensitiveFieldTypeEmail,
			Pattern:     regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`),
			MaskFunc:    maskEmail,
			Description: "Email addresses",
		},
		{
			Type:        SensitiveFieldTypePhone,
			Pattern:     regexp.MustCompile(`(\+?\d{1,3}[-.\s]?)?\(?\d{3}\)?[-.\s]?\d{3}[-.\s]?\d{4}`),
			MaskFunc:    maskPhone,
			Description: "Phone numbers",
		},
		{
			Type:        SensitiveFieldTypeCreditCard,
			Pattern:     regexp.MustCompile(`\b(?:\d[ -]*?){13,16}\b`),
			MaskFunc:    maskCreditCard,
			Description: "Credit card numbers",
		},
		{
			Type:        SensitiveFieldTypeSSN,
			Pattern:     regexp.MustCompile(`\b\d{3}[-.]?\d{2}[-.]?\d{4}\b`),
			MaskFunc:    maskSSN,
			Description: "Social security numbers",
		},
	}
}

// Sanitize removes sensitive information from the input string.
func (s *Sanitizer) Sanitize(input string) string {
	if input == "" {
		return input
	}

	result := input
	for _, pattern := range s.patterns {
		result = pattern.Pattern.ReplaceAllStringFunc(result, func(match string) string {
			return s.applyMaskOptions(pattern.Type, match, pattern.MaskFunc(match))
		})
	}

	return result
}

// applyMaskOptions adapts a mask function's output to the configured
// SanitizeOptions: an explicit PreserveLengthFor entry re-masks the
// original match with exactly N leading visible characters, MaskChar swaps
// the default '*', and KeepLength pads short masks up to the original length.
func (s *Sanitizer) applyMaskOptions(t SensitiveFieldType, orig, masked string) string {
	mc := s.maskChar()

	if n, ok := s.options.PreserveLengthFor[t]; ok && n >= 0 {
		o := []rune(orig)
		if n > len(o) {
			n = len(o)
		}
		masked = string(o[:n]) + strings.Repeat(string(mc), len(o)-n)
	} else if mc != '*' {
		masked = strings.ReplaceAll(masked, "*", string(mc))
	}

	if s.options.KeepLength {
		diff := len([]rune(orig)) - len([]rune(masked))
		if diff > 0 {
			masked += strings.Repeat(string(mc), diff)
		}
	}
	return masked
}

// maskChar returns the configured masking character, defaulting to '*'.
func (s *Sanitizer) maskChar() rune {
	if s.options.MaskChar != 0 {
		return s.options.MaskChar
	}
	return '*'
}

// SanitizeJSON sanitizes a JSON string, preserving its structure. It parses
// the JSON, applies Sanitize to each string value, and re-serializes. This
// avoids corrupting JSON structure (quotes, braces, keys) that a raw regex
// pass over the full string would cause.
func (s *Sanitizer) SanitizeJSON(jsonStr string) string {
	if jsonStr == "" {
		return jsonStr
	}

	// Use json.Number: without UseNumber the decoder converts all
	// numbers to float64, making the json.Number case in sanitizeValue
	// unreachable and losing precision for large integers.
	dec := json.NewDecoder(strings.NewReader(jsonStr))
	dec.UseNumber()
	var data interface{}
	if err := dec.Decode(&data); err != nil {
		// Not valid JSON; fall back to plain string sanitization.
		return s.Sanitize(jsonStr)
	}

	sanitized := s.sanitizeValue(data)
	result, err := json.Marshal(sanitized)
	if err != nil {
		// Should not happen for data we just unmarshaled; fall back.
		return s.Sanitize(jsonStr)
	}
	return string(result)
}

// sanitizeValue recursively walks a decoded JSON value and sanitizes strings.
// Numeric values are checked too: a number whose digit form matches a
// sensitive pattern (card/phone/SSN) is degraded to its masked string form —
// masking wins over type fidelity.
func (s *Sanitizer) sanitizeValue(v interface{}) interface{} {
	switch val := v.(type) {
	case string:
		return s.Sanitize(val)
	case json.Number:
		return s.maybeMaskNumeric(val.String(), val)
	case float64:
		return s.maybeMaskNumeric(strconv.FormatFloat(val, 'f', -1, 64), val)
	case map[string]interface{}:
		result := make(map[string]interface{}, len(val))
		for k, vv := range val {
			result[k] = s.sanitizeValue(vv)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(val))
		for i, vv := range val {
			result[i] = s.sanitizeValue(vv)
		}
		return result
	default:
		// Booleans and nil cannot carry sensitive digit runs.
		return v
	}
}

// maybeMaskNumeric sanitizes the decimal string form of a JSON number. An
// unchanged value is returned as-is so the caller re-emits it as a number;
// a hit returns the masked string (quoted in the re-serialized JSON).
// maybeMaskNumeric sanitizes a numeric digit run, preserving the ORIGINAL JSON
// value — and hence its numeric type — when no sensitive pattern matches:
// returning the original float64/json.Number keeps benign numbers like
// {"count": 42} as JSON numbers instead of degrading them to masked strings.
func (s *Sanitizer) maybeMaskNumeric(digits string, original interface{}) interface{} {
	masked := s.Sanitize(digits)
	if masked == digits {
		return original
	}
	return masked
}

// maskAPIKey masks an API key while preserving some context.
func maskAPIKey(match string) string {
	// Extract the actual key value using the same pattern as the detection
	re := regexp.MustCompile(`(?i)(api[_-]?key|apikey|secret[_-]?key|token[_-]?key)[:\s]+["']?([a-zA-Z0-9_\-\.]+)["']?`)
	matches := re.FindStringSubmatch(match)
	if len(matches) > 2 {
		// matches[1] is the keyword, matches[2] is the actual key
		keyValue := matches[2]
		return strings.Replace(match, keyValue, maskString(keyValue, 4), 1)
	}
	// Fallback: try to find any long alphanumeric string
	re2 := regexp.MustCompile(`[a-zA-Z0-9_\-\.]+`)
	allMatches := re2.FindAllString(match, -1)
	if len(allMatches) == 0 {
		return maskString(match, 4)
	}

	// Prefer the match nearest to a known key-related keyword.
	kwRe := regexp.MustCompile(`(?i)(key|secret|token|credential|auth)`)
	bestScore := -1
	best := allMatches[0]
	for _, m := range allMatches {
		score := len(m)
		// Bonus for being near a keyword — boost score by proximity.
		if loc := kwRe.FindStringIndex(match); loc != nil {
			// Find match position in match string.
			if idx := strings.Index(match, m); idx >= 0 {
				dist := idx - loc[1]
				if dist < 0 {
					dist = -dist
				}
				if dist < 20 {
					score += 100 - dist
				}
			}
		}
		if score > bestScore {
			bestScore = score
			best = m
		}
	}

	return strings.Replace(match, best, maskString(best, 4), 1)
}

// maskPassword masks a password completely.
func maskPassword(match string) string {
	// Extract the password value: find the part after password/passwd/pwd keyword
	re := regexp.MustCompile(`(?i)(password|passwd|pwd)[:\s]+["']?([^"'\s]+)["']?`)
	matches := re.FindStringSubmatch(match)
	if len(matches) > 2 {
		// matches[1] is the keyword (password/passwd/pwd), matches[2] is the actual password
		passwordValue := matches[2]
		return strings.Replace(match, passwordValue, maskString(passwordValue, 0), 1)
	}
	return maskString(match, 0)
}

// maskToken masks a token while preserving some context.
func maskToken(match string) string {
	// Extract the actual token value using the same pattern as the detection
	re := regexp.MustCompile(`(?i)(token|bearer[:\s]+|authorization[:\s]+bearer)[:\s]+["']?([a-zA-Z0-9_\-\.]+)["']?`)
	matches := re.FindStringSubmatch(match)
	if len(matches) > 2 {
		// matches[1] is the keyword, matches[2] is the actual token
		tokenValue := matches[2]
		return strings.Replace(match, tokenValue, maskString(tokenValue, 4), 1)
	}
	// Fallback: try to find any long alphanumeric string
	re2 := regexp.MustCompile(`[a-zA-Z0-9_\-\.]+`)
	allMatches := re2.FindAllString(match, -1)
	if len(allMatches) == 0 {
		return maskString(match, 4)
	}

	// Prefer the match nearest to a known token-related keyword.
	kwRe := regexp.MustCompile(`(?i)(token|bearer|auth|credential|jwt)`)
	bestScore := -1
	best := allMatches[0]
	for _, m := range allMatches {
		score := len(m)
		if loc := kwRe.FindStringIndex(match); loc != nil {
			if idx := strings.Index(match, m); idx >= 0 {
				dist := idx - loc[1]
				if dist < 0 {
					dist = -dist
				}
				if dist < 20 {
					score += 100 - dist
				}
			}
		}
		if score > bestScore {
			bestScore = score
			best = m
		}
	}

	return strings.Replace(match, best, maskString(best, 4), 1)
}

// maskEmail masks an email address.
func maskEmail(match string) string {
	parts := strings.Split(match, "@")
	if len(parts) != 2 {
		return "***@***.***"
	}

	// For email, preserve first 2 chars of username (clamped to length) and domain
	username := parts[0]
	domain := parts[1]

	preserve := 2
	if len(username) < preserve {
		preserve = len(username)
	}
	maskedUsername := maskString(username, preserve)
	maskedDomain := maskString(domain, 2)

	return maskedUsername + "@" + maskedDomain
}

// maskPhone masks a phone number.
func maskPhone(match string) string {
	// Remove common formatting characters
	cleaned := regexp.MustCompile(`[^\d]`).ReplaceAllString(match, "")
	if len(cleaned) < 3 {
		return maskString(match, 3)
	}

	// For phone, preserve first 3 and last 4 digits
	if len(cleaned) >= 7 {
		maskLen := len(cleaned) - 7
		if maskLen == 0 {
			return maskString(cleaned, 3)
		}
		return cleaned[:3] + strings.Repeat("*", maskLen) + cleaned[len(cleaned)-4:]
	}

	return maskString(cleaned, 3)
}

// maskCreditCard masks a credit card number.
func maskCreditCard(match string) string {
	// Remove spaces and dashes
	cleaned := regexp.MustCompile(`[^\d]`).ReplaceAllString(match, "")
	if len(cleaned) < 4 {
		return maskString(match, 4)
	}

	// For credit card, preserve first 4 and last 4 digits
	if len(cleaned) >= 8 {
		maskLen := len(cleaned) - 8
		if maskLen == 0 {
			return maskString(cleaned, 4)
		}
		return cleaned[:4] + strings.Repeat("*", maskLen) + cleaned[len(cleaned)-4:]
	}

	return maskString(cleaned, 4)
}

// maskSSN masks a social security number, preserving the original separator format.
func maskSSN(match string) string {
	cleaned := regexp.MustCompile(`[^\d]`).ReplaceAllString(match, "")
	if len(cleaned) != 9 {
		return "***-**-****"
	}
	// Determine separator from the original input.
	sep := "-"
	if strings.Contains(match, ".") {
		sep = "."
	}
	return "***" + sep + "**" + sep + "****"
}

// maskString masks a string, preserving n characters from the beginning and end.
func maskString(s string, preserveLength int) string {
	if s == "" {
		return s
	}

	runes := []rune(s)
	length := len(runes)

	if length <= preserveLength {
		return strings.Repeat("*", length)
	}

	if length <= preserveLength*2 {
		prefix := string(runes[:preserveLength])
		return prefix + strings.Repeat("*", length-preserveLength)
	}

	prefix := string(runes[:preserveLength])
	suffix := string(runes[length-preserveLength:])
	maskLength := length - preserveLength*2

	return prefix + strings.Repeat(string('*'), maskLength) + suffix
}
