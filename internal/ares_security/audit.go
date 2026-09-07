package ares_security

import (
	"log/slog"
)

// AuditLogger is the modular audit sink for security-relevant events: auth
// decisions (allow/deny on protected endpoints) and destructive actions
// (kill/resume/retry an agent, call an MCP tool). It wraps an *slog.Logger
// with a fixed set of structured fields so every audit record has the same
// shape, independent of which component wrote it.
//
// The token itself is never logged — only the decoded identity (subject,
// role) and the decision. This is the "complete, modular audit logging":
// previously the auth decision was the only audited event,
// and it was hard-wired inside AuthMiddleware.
type AuditLogger struct {
	l *slog.Logger
}

// NewAuditLogger builds an audit sink. A nil logger is allowed and disables
// audit output (callers may pass slog.Default() when they have no logger).
func NewAuditLogger(l *slog.Logger) *AuditLogger {
	return &AuditLogger{l: l}
}

// Auth records an authentication decision on a protected endpoint. decision
// is one of "allowed", "missing bearer token", "invalid token",
// "unknown role in token", "insufficient role"; status is the HTTP status
// that was (or would be) returned.
func (a *AuditLogger) Auth(decision, subject, role, method, path string, status int) {
	if a == nil || a.l == nil {
		return
	}
	a.l.Info("auth",
		"decision", decision,
		"subject", subject,
		"role", role,
		"method", method,
		"path", path,
		"status", status,
	)
}

// Action records a destructive (or privileged) action executed on behalf of
// an authenticated principal: which action, against which target, who did it,
// and whether it succeeded. This is the audit trail for "who changed what".
func (a *AuditLogger) Action(action, subject, target string, ok bool) {
	if a == nil || a.l == nil {
		return
	}
	a.l.Info("action",
		"action", action,
		"subject", subject,
		"target", target,
		"ok", ok,
	)
}
