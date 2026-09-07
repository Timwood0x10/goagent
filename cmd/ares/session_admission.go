package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Timwood0x10/ares/internal/fabric/agent"
	"github.com/Timwood0x10/ares/internal/fabric/planprojection"
	"github.com/Timwood0x10/ares/internal/fabric/task"
	"github.com/Timwood0x10/ares/internal/fabric/task/workflow/engine"
)

// ensureSessionAdmission admits one L2 session before its first task is
// created (M4-B2): register the session graph, subscribe it to the shared
// incremental compiler, and compile the root task the planner's first
// quantum falls back to.
//
// The caller is submitPeerTask, and only when the request carries a
// session_id AND the gate wired a registry (nil registry = gate off =
// legacy path, session payloads stay envelope-only). Admission is idempotent:
// resubmitting into a live session is a multi-turn continuation, not an
// error — the existing session is reused and no duplicate root is compiled.
//
// Failures are fail-fast (nothing half-created): a session the caller asked
// for but we cannot admit must not silently degrade into an unrunnable
// task. Anything InitSession registered before the failure is released
// again, so a retry starts clean.
func ensureSessionAdmission(ctx context.Context, kernel *kernelHandle, sessionID, prompt string) error {
	if kernel == nil || sessionID == "" {
		return nil
	}
	// M4-D: single execution path. A session that cannot be admitted must
	// fail fast — a session-scoped task without a live graph is unrunnable.
	// (The old gate-off silent skip is gone with the gate.)
	if kernel.sessionReg == nil {
		return fmt.Errorf("peer mode: cannot admit session %q without a session registry", sessionID)
	}
	// P0-1b: a session ID containing "/" breaks the reaper keep-set —
	// SessionIDFromNode reverse-parses at the first slash, so "a/b" maps
	// its tasks back to a session "a" that is not live, and the reaper
	// harvests a LIVE session's readable history once the grace window
	// passes (the exact decision-C accident, triggered by pure client
	// input). Reject at the admission boundary, same level as the empty
	// ID; the registry enforces the same contract as a backstop.
	if strings.Contains(sessionID, "/") {
		return fmt.Errorf("peer mode: session id %q must not contain a slash", sessionID)
	}
	if _, err := kernel.sessionReg.GetSession(sessionID); err == nil {
		return nil
	} else if !errors.Is(err, agentfabric.ErrSessionNotFound) {
		return fmt.Errorf("peer mode: look up session %q: %w", sessionID, err)
	}
	if kernel.compileCoord == nil || kernel.fabric == nil {
		return fmt.Errorf("peer mode: cannot admit session %q without compile coordinator and fabric", sessionID)
	}

	// The compile subscription must outlive the submission request: tying it
	// to the request context would kill the projection the moment the HTTP
	// handler returns, while the session lives on.
	liveCtx := context.WithoutCancel(ctx)
	g, err := kernel.sessionReg.InitSession(liveCtx, sessionID, prompt, nil,
		func(subCtx context.Context, dag *engine.MutableDAG) (stop func()) {
			return kernel.compileCoord.SubscribeGraphEvents(subCtx, dag)
		})
	if err != nil {
		// A concurrent admitter may have won the race between our
		// GetSession and InitSession — re-check before failing.
		if errors.Is(err, agentfabric.ErrSessionAlreadyExists) {
			if _, err2 := kernel.sessionReg.GetSession(sessionID); err2 == nil {
				return nil
			}
		}
		return fmt.Errorf("peer mode: init session %q: %w", sessionID, err)
	}

	// Compile the root task the planner's first quantum reads (or falls
	// back to the payload input when still pending). An already-compiled
	// root means a retried admission after a partial failure — adopt it,
	// but ONLY while that root is still live (P0-1c below).
	rootStep := g.DAG().StepIndex()[g.Root()]
	if _, err := kernel.fabric.CompileNode(liveCtx, planprojection.ProjectStep(rootStep)); err != nil {
		if !errors.Is(err, taskfabric.ErrTaskExists) {
			releaseSessionQuietly(kernel, sessionID)
			return fmt.Errorf("peer mode: compile session %q root: %w", sessionID, err)
		}
		// P0-1c: an existing TERMINAL root does not belong to a retry — it
		// belongs to a previous session that already released under this
		// same ID (the natural client "continue the chat" behavior after
		// an answer). Adopting it would hand the new turn the old prompt
		// (rootCognition wrote its input into the envelope output) and let
		// same-named node tasks resolve to old tool outputs read as fresh
		// results — silently, with the keep-set then protecting the stale
		// tasks forever. The registry just told us this session is NOT
		// live, so no planner is reading those envelopes: harvest them
		// (the reaper's job, done early) and recompile clean.
		if stale, terr := kernel.fabric.Task(g.Root()); terr == nil &&
			(stale.State == taskfabric.StateCompleted || stale.State == taskfabric.StateFailed) {
			n := harvestReleasedSession(kernel.fabric, sessionID)
			slog.InfoContext(liveCtx, "peer mode: session re-admitted after release, harvested stale tasks before recompiling root",
				"session_id", sessionID, "harvested", n)
			if _, err := kernel.fabric.CompileNode(liveCtx, planprojection.ProjectStep(rootStep)); err != nil {
				releaseSessionQuietly(kernel, sessionID)
				return fmt.Errorf("peer mode: recompile session %q root: %w", sessionID, err)
			}
		}
	}
	slog.InfoContext(liveCtx, "peer mode: admitted L2 session",
		"session_id", sessionID, "root", g.Root())
	return nil
}

// harvestReleasedSession deletes every harvestable task under a released
// session's ID prefix (P0-1c): terminal (COMPLETED/FAILED) and READY tasks
// go; in-flight ones (LEASED/RUNNING/SUSPENDED) are refused by Delete and
// left for the reaper — they belong to work genuinely still running.
// Returns the number of tasks removed.
func harvestReleasedSession(fabric *taskfabric.Fabric, sessionID string) int {
	prefix := agentfabric.SessionTaskPrefix(sessionID)
	removed := 0
	for _, id := range fabric.IDs() {
		if !strings.HasPrefix(id, prefix) {
			continue
		}
		if fabric.Delete(id) == nil {
			removed++
		}
	}
	return removed
}

// releaseSessionQuietly drops a half-admitted session during failure
// cleanup. The release itself is best-effort: the admission already failed,
// and a release miss only leaves a normal session behind for the reaper.
func releaseSessionQuietly(kernel *kernelHandle, sessionID string) {
	_ = kernel.sessionReg.ReleaseSession(sessionID)
}

// sessionKeepSet builds the reaper's keep predicate from the session
// registry (P0-1): a task is kept while its owning session is still live.
// The registry is the single authority — an ID that parses as a session
// task but has no live session (released, or never admitted by this
// process) is harvestable once the grace window passes.
func sessionKeepSet(reg *agentfabric.SessionRegistry) func(taskID string) bool {
	return func(taskID string) bool {
		sid, ok := agentfabric.SessionIDFromNode(taskID)
		if !ok {
			return false
		}
		_, err := reg.GetSession(sid)
		return err == nil
	}
}
