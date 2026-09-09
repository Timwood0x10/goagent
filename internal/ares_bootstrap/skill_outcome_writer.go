// Package ares_bootstrap — skill experience WRITE side (M4.4).
//
// The retired SkillOutcomeRecorder starved on EventSubTaskResult: its only
// emitter (the ReAct tool loop) never produced the conforming payload shape,
// and the shape it read (Payload["task"]/["success"], pattern=task_desc)
// never matched what the READ side queries — Fabric.Schedule resolves the
// prior by Confidence(t.Capability), so even a fed recorder would have
// recorded priors under keys the scheduler never looks up.
//
// This writer closes the loop at the join the read side actually uses:
//
//	taskfabric task.completed / task.failed events (capability rides on
//	every persisted event — recordLocked stamps restoreKeyCapability)
//	  → Experience.Record(skill=capability, pattern=capability, rate)
//	  → ExperienceConfidenceSource.Confidence(taskPattern=capability)
//	  → Fabric.Schedule fills history-less candidates (M4.4 read-side fix)
//
// The {skill, pattern} pair is the task CAPABILITY on both sides — the one
// key the writer can read off the event and the scheduler provably queries.
// Success rate is 1.0/0.0 per terminal event (the same binarized evidence
// the RuntimeObserver scores); Experience.Record replaces the prior on
// re-record, so a flapping capability converges to its last outcome —
// deliberately the same recency semantics the retired recorder had.
package ares_bootstrap

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Timwood0x10/ares/internal/ares_events"
	ares_skills "github.com/Timwood0x10/ares/internal/runtime/protocol/skills"
)

// skillOutcomeWriter consumes taskfabric terminal events and records the
// outcome as a {capability → success-rate} experience prior on the catalog's
// Experience store. It is deliberately passive: recording is best-effort,
// never blocks, and a failure logs without retrying — a lost record means
// one less prior, never a broken task path (same failure policy as the
// retired recorder).
type skillOutcomeWriter struct {
	exp    *ares_skills.Experience
	store  ares_events.EventStore
	record func(skill, taskPattern string, successRate float64) error
	logger *slog.Logger
	cancel context.CancelFunc
}

// startSkillOutcomeWriter subscribes the writer to the store's terminal
// task events and records outcomes until ctx is cancelled. A nil catalog
// (skills disabled), a nil experience handle, or a nil store is a no-op —
// offline wiring, not an error. The subscription runs detached; it stops
// when the bootstrap ctx ends or the store closes the channel.
// startSkillOutcomeWriter subscribes to terminal task events and records
// outcomes until the returned cancel is invoked or ctx ends. The caller
// MUST register the cancel in the bootstrap cleanups slice so a failed
// bootstrap does not leave the writer running.
func startSkillOutcomeWriter(ctx context.Context, store ares_events.EventStore, exp *ares_skills.Experience) func() {
	if exp == nil || store == nil {
		return func() {}
	}
	w := &skillOutcomeWriter{
		exp:    exp,
		store:  store,
		record: exp.Record,
		logger: slog.Default().With("component", "skill_outcome_writer"),
	}
	if err := w.start(ctx); err != nil {
		w.logger.Warn("bootstrap: skill outcome writer start failed", "error", err)
		return func() {}
	}
	return w.cancel
}

// start subscribes and launches the consume loop. Package-level (not on the
// instance) ownership: the returned error path needs no cleanup because a
// Subscribe failure leaves nothing to stop.
func (w *skillOutcomeWriter) start(ctx context.Context) error {
	subCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	ch, err := w.store.Subscribe(subCtx, ares_events.EventFilter{
		Types: []ares_events.EventType{
			ares_events.EventTaskCompleted,
			ares_events.EventTaskFailed,
		},
	})
	if err != nil {
		cancel()
		return fmt.Errorf("skill outcome writer: subscribe: %w", err)
	}
	go w.consume(subCtx, ch)
	w.logger.Info("bootstrap: skill outcome writer started (taskfabric terminal events)")
	return nil
}

// consume reads events and records outcomes until ctx is done or the
// channel closes. A panic in one record is recovered so a malformed event
// can never take the consumer down (production background goroutines must
// not die silently or take the process down on a bug).
func (w *skillOutcomeWriter) consume(ctx context.Context, ch <-chan *ares_events.Event) {
	defer func() {
		if r := recover(); r != nil {
			w.logger.Error("skill outcome writer: consumer panicked",
				"panic", fmt.Sprintf("%v", r))
		}
	}()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev == nil {
				continue
			}
			w.consumeOne(ev)
		case <-ctx.Done():
			return
		}
	}
}

// consumeOne records a single terminal outcome. Events without a capability
// (pre-M4.4 writers, envelope-less tasks) are skipped: a "" pattern can
// never match the read side's Confidence(t.Capability) query, so recording
// it would only bloat the store. task.failed with state READY (the requeue
// branch of fabric.Fail) also records — a failed attempt is real evidence
// about the capability, and the fabric's retry budget bounds the record
// churn.
func (w *skillOutcomeWriter) consumeOne(ev *ares_events.Event) {
	capability, _ := ev.Payload["capability"].(string)
	if capability == "" {
		return
	}
	rate := 0.0
	if ev.Type == ares_events.EventTaskCompleted {
		// A completed task whose state says otherwise (e.g. hand-edited
		// payloads) still counts as success: the event type is the
		// terminal verdict the fabric's state machine emitted.
		rate = 1.0
	}
	if err := w.record(capability, capability, rate); err != nil {
		w.logger.Warn("skill outcome writer: record failed",
			"capability", capability, "error", err)
	}
}
