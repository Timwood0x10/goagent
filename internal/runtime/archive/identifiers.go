package archive

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Compiled regexes for P3 identifier protection (see
// plan/context_compression_strategy.md). These are package-level and
// immutable after init, so they are safe for concurrent use.
var (
	// reCommitHash matches abbreviated (7+) or full (40) lowercase hex commit
	// hashes. The word boundary prevents matching hex-like substrings inside
	// longer non-hex tokens.
	reCommitHash = regexp.MustCompile(`\b[a-f0-9]{7,40}\b`)
	// rePRNumber matches GitHub-style PR/issue references like "#142".
	rePRNumber = regexp.MustCompile(`#\d+`)
	// reIPPort matches IPv4 address:port pairs like "10.0.0.1:8080".
	// Note: it does not validate octet ranges (0-255), so "999.0.0.1:80" also
	// matches. This is an accepted limitation: identifier protection
	// prioritises recall (never lose a real IP) over precision.
	reIPPort = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}:\d+\b`)
	// reIPv4 matches a bare IPv4 address like "10.0.0.1" (no port). Used for
	// the "ip" role so a plain IP is accepted, while "ip_port"/"addr" keep
	// requiring the port form.
	reIPv4 = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	// reOwnerRepo matches GitHub owner/repo slugs like "TimWood0x10/ares".
	// Only one slash is allowed, so "a/b/c" yields just "a/b".
	reOwnerRepo = regexp.MustCompile(`\b[a-zA-Z0-9_-]+/[a-zA-Z0-9_-]+\b`)
	// reGoCommand matches common go subcommand invocations.
	reGoCommand = regexp.MustCompile(`\bgo (build|test|vet|bench|fmt|mod)\b`)
	// reVerdictToken matches Go toolchain verdict tokens: PASS, FAIL, ok, and
	// "exit code: N" / "exit code N" lines emitted by code_runner.
	reVerdictToken = regexp.MustCompile(`\b(PASS|FAIL|ok)\b|\bexit code[: ]+\d+\b`)
)

// ProtectIdentifiers validates and copies caller-supplied identifier refs.
// Each value is checked against the pattern declared by its key role:
//   - "commit", "git_rev"  → reCommitHash
//   - "pr", "issue"        → rePRNumber
//   - "ip", "ip_port", "addr" → reIPPort
//   - "repo", "owner_repo" → reOwnerRepo
//   - any other key        → accepted as-is (trimmed only)
//
// The input map is never mutated; a new map is returned. This enforces the
// P3 "must not truncate" guarantee: a silently-truncated hash (e.g. "abc12"
// which is only 5 hex chars) is rejected rather than archived.
//
// Args:
//   - refs: caller-supplied identifier map (role → value). May be nil.
//
// Returns:
//   - map[string]string: a new validated copy. An empty (non-nil) map when
//     input is nil.
//   - error: a wrapped ErrInvalidIdentifier when a value is empty or fails
//     its pattern.
func ProtectIdentifiers(refs map[string]string) (map[string]string, error) {
	if refs == nil {
		return make(map[string]string), nil
	}
	out := make(map[string]string, len(refs))
	for key, val := range refs {
		trimmed := strings.TrimSpace(val)
		if trimmed == "" {
			return nil, fmt.Errorf("protect identifier %q: value %q: %w", key, val, ErrInvalidIdentifier)
		}
		re := patternForRole(key)
		if re == nil {
			out[key] = trimmed
			continue
		}
		if !re.MatchString(trimmed) {
			return nil, fmt.Errorf("protect identifier %q: value %q: %w", key, val, ErrInvalidIdentifier)
		}
		out[key] = trimmed
	}
	return out, nil
}

// patternForRole returns the compiled regex for the given identifier role,
// or nil when the role is unrecognised (accept-as-is).
func patternForRole(key string) *regexp.Regexp {
	switch key {
	case roleCommit, roleGitRev:
		return reCommitHash
	case rolePR, roleIssue:
		return rePRNumber
	case roleIP:
		return reIPv4
	case roleIPPort, roleAddr:
		return reIPPort
	case roleRepo, roleOwnerRepo:
		return reOwnerRepo
	default:
		return nil
	}
}

// ExtractIdentifiers scans free-form text for all known identifier patterns
// and returns them keyed by role. Matches are deduplicated within each role,
// preserving first-seen order.
//
// The returned map always contains all six roles ("commit", "pr", "ip_port",
// "owner_repo", "go_cmd", "verdict") as non-nil (possibly empty) slices, so
// callers can range without a nil check.
//
// Args:
//   - text: the text to scan. Empty or whitespace-only input yields a non-nil
//     map with empty slices.
//
// Returns:
//   - map[string][]string: role → deduplicated matches. Never nil.
func ExtractIdentifiers(text string) map[string][]string {
	out := map[string][]string{
		roleCommit:    {},
		rolePR:        {},
		roleIPPort:    {},
		roleOwnerRepo: {},
		roleGoCmd:     {},
		roleVerdict:   {},
	}
	if strings.TrimSpace(text) == "" {
		return out
	}
	patterns := []struct {
		role string
		re   *regexp.Regexp
	}{
		{roleCommit, reCommitHash},
		{rolePR, rePRNumber},
		{roleIPPort, reIPPort},
		{roleOwnerRepo, reOwnerRepo},
		{roleGoCmd, reGoCommand},
		{roleVerdict, reVerdictToken},
	}
	for _, p := range patterns {
		matches := p.re.FindAllString(text, -1)
		for _, m := range matches {
			if !slices.Contains(out[p.role], m) {
				out[p.role] = append(out[p.role], m)
			}
		}
	}
	return out
}
