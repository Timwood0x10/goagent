package archive

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Potential bug scenarios tested below:
//  1. Commit hash regex matching non-hex chars like "abcdefg" (has 'g') — the
//     character class [a-f0-9] prevents the match. TestProtectIdentifiers
//     rejects "abc12" (too short), and TestExtractIdentifiers_NoNonHexMatch
//     asserts "abcdefg" produces no commit match.
//  2. IP regex matching invalid octets like "999.0.0.1:80" — the regex \d{1,3}
//     matches "999" without validating the 0-255 range. This is an accepted
//     limitation (recall over precision); TestExtractIdentifiers_IPRegexLimitation
//     documents it.
//  3. Owner/repo regex over-matching "a/b/c" — the regex matches only "a/b"
//     (first owner/repo pair) because the pattern allows exactly one slash.
//     TestExtractIdentifiers_OwnerRepoSinglePair asserts only "a/b" is captured.

func TestProtectIdentifiers(t *testing.T) {
	fullHash := "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0" // 40 hex chars

	tests := []struct {
		name      string
		refs      map[string]string
		wantOut   map[string]string
		wantErrIs error
		wantErr   bool
	}{
		{
			name:    "nil input returns empty map",
			refs:    nil,
			wantOut: map[string]string{},
			wantErr: false,
		},
		{
			name: "valid 7-char commit hash preserved",
			refs: map[string]string{roleCommit: "abc1234"},
			wantOut: map[string]string{
				roleCommit: "abc1234",
			},
			wantErr: false,
		},
		{
			name: "valid 40-char commit hash preserved exactly",
			refs: map[string]string{roleCommit: fullHash},
			wantOut: map[string]string{
				roleCommit: fullHash,
			},
			wantErr: false,
		},
		{
			name: "valid git_rev key accepts hash",
			refs: map[string]string{roleGitRev: "deadbef00d"},
			wantOut: map[string]string{
				roleGitRev: "deadbef00d",
			},
			wantErr: false,
		},
		{
			name:      "truncated 5-char hash rejected",
			refs:      map[string]string{roleCommit: "abc12"},
			wantErrIs: ErrInvalidIdentifier,
			wantErr:   true,
		},
		{
			name:      "non-hex chars rejected",
			refs:      map[string]string{roleCommit: "abcdefg"},
			wantErrIs: ErrInvalidIdentifier,
			wantErr:   true,
		},
		{
			name: "valid IP:port preserved",
			refs: map[string]string{roleIP: "10.0.0.1:8080"},
			wantOut: map[string]string{
				roleIP: "10.0.0.1:8080",
			},
			wantErr: false,
		},
		{
			name: "valid ip_port key accepts IP",
			refs: map[string]string{roleIPPort: "192.168.1.1:3000"},
			wantOut: map[string]string{
				roleIPPort: "192.168.1.1:3000",
			},
			wantErr: false,
		},
		{
			name: "valid PR number preserved",
			refs: map[string]string{rolePR: "#142"},
			wantOut: map[string]string{
				rolePR: "#142",
			},
			wantErr: false,
		},
		{
			name: "valid issue key accepts PR number",
			refs: map[string]string{roleIssue: "#9999"},
			wantOut: map[string]string{
				roleIssue: "#9999",
			},
			wantErr: false,
		},
		{
			name: "valid owner/repo preserved",
			refs: map[string]string{roleRepo: "TimWood0x10/ares"},
			wantOut: map[string]string{
				roleRepo: "TimWood0x10/ares",
			},
			wantErr: false,
		},
		{
			name: "valid owner_repo key accepts slug",
			refs: map[string]string{roleOwnerRepo: "golang/go"},
			wantOut: map[string]string{
				roleOwnerRepo: "golang/go",
			},
			wantErr: false,
		},
		{
			name:      "empty value rejected",
			refs:      map[string]string{roleCommit: ""},
			wantErrIs: ErrInvalidIdentifier,
			wantErr:   true,
		},
		{
			name:      "whitespace-only value rejected",
			refs:      map[string]string{roleCommit: "   "},
			wantErrIs: ErrInvalidIdentifier,
			wantErr:   true,
		},
		{
			name: "unknown key accepted as-is (trimmed)",
			refs: map[string]string{"custom": "  any-value  "},
			wantOut: map[string]string{
				"custom": "any-value",
			},
			wantErr: false,
		},
		{
			name: "multiple keys validated together",
			refs: map[string]string{
				roleCommit: "abc1234",
				rolePR:     "#142",
				"custom":   "anything",
			},
			wantOut: map[string]string{
				roleCommit: "abc1234",
				rolePR:     "#142",
				"custom":   "anything",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ProtectIdentifiers(tt.refs)
			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrIs != nil {
					assert.ErrorIs(t, err, tt.wantErrIs)
				}
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantOut, got)
		})
	}
}

func TestProtectIdentifiers_DoesNotMutateInput(t *testing.T) {
	original := map[string]string{roleCommit: "abc1234"}
	originalCopy := map[string]string{roleCommit: "abc1234"}

	out, err := ProtectIdentifiers(original)
	require.NoError(t, err)
	require.NotNil(t, out)

	// Mutating the output must not affect the input.
	out[roleCommit] = "modified"
	assert.Equal(t, originalCopy, original, "input map must not be mutated")
}

func TestProtectIdentifiers_HashNotTruncated(t *testing.T) {
	// The P3 guarantee: a full 40-char hash and a valid 7-char hash both
	// round-trip exactly — no truncation, no padding.
	fullHash := "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"
	shortHash := "abc1234"

	tests := []struct {
		name string
		hash string
	}{
		{"40-char hash", fullHash},
		{"7-char hash", shortHash},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := ProtectIdentifiers(map[string]string{roleCommit: tt.hash})
			require.NoError(t, err)
			require.NotNil(t, out)
			assert.Equal(t, tt.hash, out[roleCommit], "hash must round-trip exactly")
		})
	}
}

func TestExtractIdentifiers(t *testing.T) {
	text := "Fixed in commit abc1234 and def5678. See PR #142 and #143. " +
		"Server at 10.0.0.1:8080 and 192.168.1.1:3000. " +
		"Run go test and go vet. Result: PASS."

	result := ExtractIdentifiers(text)

	assert.NotNil(t, result, "result map must be non-nil")
	assert.Equal(t, []string{"abc1234", "def5678"}, result[roleCommit])
	assert.Equal(t, []string{"#142", "#143"}, result[rolePR])
	assert.Equal(t, []string{"10.0.0.1:8080", "192.168.1.1:3000"}, result[roleIPPort])
	assert.Contains(t, result[roleGoCmd], "go test")
	assert.Contains(t, result[roleGoCmd], "go vet")
	assert.Contains(t, result[roleVerdict], "PASS")
}

func TestExtractIdentifiers_DedupAndOrder(t *testing.T) {
	// The same commit appears twice; the result must contain it once, in
	// first-seen position.
	text := "commit def5678 then abc1234 and again def5678"
	result := ExtractIdentifiers(text)

	assert.Equal(t, []string{"def5678", "abc1234"}, result[roleCommit],
		"deduped and first-seen order preserved")
}

func TestExtractIdentifiers_EmptyText(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{"empty string", ""},
		{"whitespace only", "   \t\n  "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractIdentifiers(tt.text)
			require.NotNil(t, result, "result must be non-nil even for empty input")
			for _, role := range []string{roleCommit, rolePR, roleIPPort, roleOwnerRepo, roleGoCmd, roleVerdict} {
				assert.NotNil(t, result[role], "role %q must have a non-nil slice", role)
				assert.Empty(t, result[role], "role %q must be empty for blank input", role)
			}
		})
	}
}

func TestExtractIdentifiers_NoNonHexMatch(t *testing.T) {
	// Bug scenario 1: "abcdefg" contains 'g' which is not in [a-f0-9].
	// The regex must not match it or any 7-char substring of it.
	result := ExtractIdentifiers("the token is abcdefg here")
	assert.Empty(t, result[roleCommit], "non-hex token must not produce a commit match")
}

func TestExtractIdentifiers_IPRegexLimitation(t *testing.T) {
	// Bug scenario 2: the IP regex matches invalid octets like "999.0.0.1:80"
	// because \d{1,3} does not validate the 0-255 range. This is an accepted
	// limitation — we prioritise recall (never lose a real IP) over precision.
	result := ExtractIdentifiers("bad IP 999.0.0.1:80 and good IP 10.0.0.1:8080")
	assert.Contains(t, result[roleIPPort], "999.0.0.1:80",
		"invalid octets are matched (accepted limitation)")
	assert.Contains(t, result[roleIPPort], "10.0.0.1:8080",
		"valid IP is also matched")
}

func TestExtractIdentifiers_OwnerRepoSinglePair(t *testing.T) {
	// Bug scenario 3: "a/b/c" must yield only "a/b" (first owner/repo pair)
	// because the regex allows exactly one slash.
	result := ExtractIdentifiers("path a/b/c here")
	assert.Equal(t, []string{"a/b"}, result[roleOwnerRepo],
		"only the first owner/repo pair is captured from a/b/c")
}

func TestExtractIdentifiers_AllRolesPresent(t *testing.T) {
	// Even when the text has no matches, all six roles must be present as
	// non-nil empty slices.
	result := ExtractIdentifiers("no identifiers here at all")
	for _, role := range []string{roleCommit, rolePR, roleIPPort, roleOwnerRepo, roleGoCmd, roleVerdict} {
		assert.NotNil(t, result[role], "role %q must be present in the map", role)
	}
}

func TestErrors(t *testing.T) {
	// Smoke-test the sentinel error identity.
	assert.True(t, errors.Is(ErrInvalidIdentifier, ErrInvalidIdentifier))
	assert.True(t, errors.Is(ErrInvalidRound, ErrInvalidRound))
	assert.True(t, errors.Is(ErrInvalidAction, ErrInvalidAction))
}
