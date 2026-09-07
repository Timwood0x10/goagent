package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_security"
	"github.com/Timwood0x10/ares/internal/evidence"
	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// evolutionApprovePath is the write endpoint under test.
const evolutionApprovePath = "/api/evolution/approve"

// newApproveHandler builds an actionHandler with JWT + audit wired and the
// given lifecycle attached, mirroring the production wiring in serve_routine.
//
// inner returns 404 rather than 200: unmatched paths and methods fall through
// to it, and a 200 stub would make "the route did not match" indistinguishable
// from "the route matched and succeeded".
func newApproveHandler(t *testing.T, lc *evolution.StrategyLifecycle) (*actionHandler, *bytes.Buffer) {
	t.Helper()
	var auditBuf bytes.Buffer
	audit := ares_security.NewAuditLogger(slog.New(slog.NewTextHandler(&auditBuf, nil)))
	auth := ares_security.NewAuthMiddleware([]byte(testActionJWTSecret), ares_security.PermWrite,
		ares_security.WithAudit(audit))
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	return &actionHandler{
		inner:     inner,
		auth:      auth,
		audit:     audit,
		lifecycle: lc,
	}, &auditBuf
}

// newPendingLifecycle builds a real StrategyLifecycle holding a candidate in
// SHADOW awaiting manual approval.
//
// Two details of Submit's semantics drive this construction, and both are easy
// to get wrong:
//
//  1. The FIRST Submit is the seed deploy: with no active strategy it promotes
//     unconditionally, bypassing every gate (lifecycle.go's one-shot `seeded`
//     flag). A second Submit is therefore required before anything can be held.
//  2. No ShadowEvaluator is wired here, so NewStrategyLifecycle registers no G2
//     gate and the pipeline is empty. That is deliberate: with G2 present the
//     default configuration is fail-closed (zero shadow comparisons ⇒ reject),
//     so the candidate would be rejected before ever reaching the manual hold.
func newPendingLifecycle(t *testing.T) *evolution.StrategyLifecycle {
	t.Helper()
	ctx := context.Background()
	store := evidence.NewMemoryStore()
	asm, err := evolution.NewActiveStrategyManager(
		evolution.NewMemoryStrategyStore(0),
		evolution.NewRollbackPolicy(),
	)
	require.NoError(t, err)

	agg := evolution.NewRuntimeFitnessAggregator(store, evolution.DefaultAggregatorConfig())
	lc := evolution.NewStrategyLifecycle(asm, agg, evolution.LifecycleConfig{
		Enabled: true,
		Gates:   evolution.GateConfig{RequireManualApproval: true},
	}, evolution.WithLifecycleEvidenceStore(store))

	// (1) seed deploy — promotes without gates and consumes the exemption.
	lc.Submit(ctx, &mutation.Strategy{ID: "seed-v1", Score: 0.5}, 1)
	// (2) the candidate that actually gets held.
	lc.Submit(ctx, &mutation.Strategy{ID: "cand-v2", Score: 0.9}, 2)

	require.True(t, lc.Snapshot().PendingApproval,
		"fixture must leave a candidate pending; otherwise the 200 case proves nothing")
	return lc
}

// postApprove issues a POST against the approve endpoint. token == "" sends no
// Authorization header at all.
func postApprove(t *testing.T, h *actionHandler, token string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, evolutionApprovePath, bytes.NewReader([]byte("{}")))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body
}

// TestEvolutionApproveUnauthenticatedReturns401 covers the deny-by-default path
// (B3). The endpoint mutates the active strategy, so an unauthenticated caller
// must never reach the lifecycle.
func TestEvolutionApproveUnauthenticatedReturns401(t *testing.T) {
	lc := newPendingLifecycle(t)
	h, _ := newApproveHandler(t, lc)

	code, body := postApprove(t, h, "")
	assert.Equal(t, http.StatusUnauthorized, code)
	assert.NotEmpty(t, body["error"])

	// The candidate must still be pending: a rejected request must not have
	// side effects.
	assert.True(t, lc.Snapshot().PendingApproval,
		"a 401 must not consume the pending candidate")
}

// TestEvolutionApproveInsufficientRoleReturns403 pins the authenticated-but-
// unauthorized case, which checkAuth deliberately distinguishes from 401.
func TestEvolutionApproveInsufficientRoleReturns403(t *testing.T) {
	lc := newPendingLifecycle(t)
	h, _ := newApproveHandler(t, lc)

	// RoleAgent carries read permission only.
	code, _ := postApprove(t, h, testActionJWT(t, ares_security.RoleAgent))
	assert.Equal(t, http.StatusForbidden, code)
	assert.True(t, lc.Snapshot().PendingApproval,
		"a 403 must not consume the pending candidate")
}

// TestEvolutionApproveLifecycleAbsentReturns503 covers the not-wired case: an
// authenticated operator on a build without the evolution control plane must
// get an honest "unavailable", not a silent success.
func TestEvolutionApproveLifecycleAbsentReturns503(t *testing.T) {
	h, _ := newApproveHandler(t, nil)

	code, body := postApprove(t, h, testActionJWT(t, ares_security.RoleOperator))
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.Equal(t, "evolution lifecycle not active", body["error"])
}

// TestEvolutionApproveNoPendingCandidateReturns409 covers the conflict case.
// Approve() is a no-op when nothing is held, so without the 409 an operator
// would receive 200 for an approval that approved nothing.
func TestEvolutionApproveNoPendingCandidateReturns409(t *testing.T) {
	store := evidence.NewMemoryStore()
	asm, err := evolution.NewActiveStrategyManager(
		evolution.NewMemoryStrategyStore(0),
		evolution.NewRollbackPolicy(),
	)
	require.NoError(t, err)
	agg := evolution.NewRuntimeFitnessAggregator(store, evolution.DefaultAggregatorConfig())
	// Manual approval enabled but nothing submitted → nothing pending.
	lc := evolution.NewStrategyLifecycle(asm, agg, evolution.LifecycleConfig{
		Enabled: true,
		Gates:   evolution.GateConfig{RequireManualApproval: true},
	}, evolution.WithLifecycleEvidenceStore(store))
	require.False(t, lc.Snapshot().PendingApproval)

	h, _ := newApproveHandler(t, lc)
	code, body := postApprove(t, h, testActionJWT(t, ares_security.RoleOperator))
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "no candidate pending manual approval", body["error"])
}

// TestEvolutionApprovePendingCandidateReturns200 is the success path: the held
// candidate is promoted, the response reports the newly-active strategy, and
// the action lands in the audit log.
func TestEvolutionApprovePendingCandidateReturns200(t *testing.T) {
	lc := newPendingLifecycle(t)
	h, auditBuf := newApproveHandler(t, lc)

	code, body := postApprove(t, h, testActionJWT(t, ares_security.RoleOperator))
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, "approved", body["status"])
	assert.Equal(t, true, body["pending_before"])
	assert.Equal(t, false, body["pending_after"],
		"approval must clear the hold")
	assert.Equal(t, "cand-v2", body["active_id"],
		"the approved candidate must become the active strategy")

	// Promote runs synchronously inside Approve, so the snapshot is already
	// settled — no polling needed.
	assert.False(t, lc.Snapshot().PendingApproval)

	audit := auditBuf.String()
	assert.Contains(t, audit, "action=evolution_approve",
		"an approval mutates the active strategy and must be audited")
	assert.Contains(t, audit, "subject=ops-user")
}

// TestEvolutionApproveIsIdempotentAfterSuccess locks the sequence an operator
// is most likely to trip over: a double-click. The second call must be a 409,
// not a second promote.
func TestEvolutionApproveIsIdempotentAfterSuccess(t *testing.T) {
	lc := newPendingLifecycle(t)
	h, _ := newApproveHandler(t, lc)
	token := testActionJWT(t, ares_security.RoleOperator)

	code, _ := postApprove(t, h, token)
	require.Equal(t, http.StatusOK, code)

	code, body := postApprove(t, h, token)
	assert.Equal(t, http.StatusConflict, code,
		"a repeated approval must conflict, not promote twice")
	assert.Equal(t, "no candidate pending manual approval", body["error"])
}

// TestEvolutionApproveRejectsNonPost guards the method contract: the route is a
// mutator and must not be reachable by a GET (which a browser or a link
// preview could issue).
func TestEvolutionApproveRejectsNonPost(t *testing.T) {
	lc := newPendingLifecycle(t)
	h, _ := newApproveHandler(t, lc)

	req := httptest.NewRequest(http.MethodGet, evolutionApprovePath, nil)
	req.Header.Set("Authorization", "Bearer "+testActionJWT(t, ares_security.RoleOperator))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.NotEqual(t, http.StatusOK, w.Code,
		"GET must not perform an approval")
	assert.True(t, lc.Snapshot().PendingApproval,
		"a non-POST must not consume the pending candidate")
}
