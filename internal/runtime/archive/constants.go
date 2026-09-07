package archive

// This file collects the repeated string literals used across the archive
// package into unexported constants. goconst requires that any string
// literal appearing min-len or more times (and min-occurrences or more
// times) be extracted to a constant; centralising them here keeps the
// declarations in one place and ensures the constants themselves hold the
// only copies of each literal.

// Round actions recognised by the archive (see allowedActions in record.go).
const (
	actionPlan      = "plan"
	actionImplement = "implement"
	actionFix       = "fix"
	actionReview    = "review"
)

// Verdict field values used across extraction and tests.
const (
	verdictPass = "pass"
	verdictFail = "fail"
	verdictSkip = "skip"
)

// Tool names matched by the sub-extractors.
const (
	toolCodeRunner = "code_runner"
	toolFileTools  = "file_tools"
)

// File operation values produced by file_tools events.
const (
	opWrite = "write"
)

// Event payload keys that are scanned or read by the extractors.
const (
	keyContent = "content"
)

// Identifier role strings used as map keys, case labels, and struct field
// values throughout ProtectIdentifiers and ExtractIdentifiers.
const (
	roleCommit    = "commit"
	rolePR        = "pr"
	roleIssue     = "issue"
	roleIPPort    = "ip_port"
	roleOwnerRepo = "owner_repo"
	roleGoCmd     = "go_cmd"
	roleVerdict   = "verdict"
	roleGitRev    = "git_rev"
	roleIP        = "ip"
	roleAddr      = "addr"
	roleRepo      = "repo"
)
