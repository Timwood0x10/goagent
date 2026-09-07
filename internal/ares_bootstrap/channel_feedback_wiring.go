// channel_feedback_wiring.go builds the OBSERVE stage for the two perception
// channels evolution was blind to: cross-agent
// collaboration receipts and tool-call outcomes.
//
// The recorder is constructed here but ATTACHED by the wiring layer (cmd/ares):
// the producers are the IPC bus and the tool binder, both of which are created
// in serve, and neither imports the evolution layer. Bootstrap owns the
// lifecycle (start/stop with the component graph); serve owns the attachment.
package ares_bootstrap

import (
	"context"

	"github.com/Timwood0x10/ares/internal/ares_config"
	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
)

// activeStrategyIDFunc adapts an ActiveStrategyManager into the "who is active
// right now" resolver both observers need. It calls Current() on every
// invocation rather than capturing a snapshot: the ASM promotes and rolls back
// at runtime, and a frozen ID would attribute new evidence to a strategy that
// stopped being active (the same defect fixed on the baseline side of
// deployment staging).
//
// Args:
//   - asm: the active strategy manager (must be non-nil).
//
// Returns:
//   - func() string: the active strategy ID, empty when none is active.
func activeStrategyIDFunc(asm *evolution.ActiveStrategyManager) func() string {
	return func() string {
		if cur := asm.Current(); cur != nil {
			return cur.ID
		}
		return ""
	}
}

// startChannelFeedback builds and starts the channel feedback recorder when at
// least one channel is armed. It returns nil — an explicit "not wired" — in
// every case where the recorder could not do real work:
//
//   - no channel enabled (the default): nothing would ever be recorded.
//   - no evidence store: nowhere to write.
//   - no ActiveStrategyManager: nothing to attribute records to, so every
//     record would be dropped. A recorder that drops 100% of its input while
//     reporting itself as wired is the exact failure class this wiring
//     exists to remove, so it is refused loudly in the log instead.
//
// Args:
//   - ctx: the bootstrap context; cancelling it stops the drain goroutine.
//   - comp: the component graph (owns the background goroutine group).
//   - newEvol: the evolution components (evidence store).
//   - asm: the active strategy manager, may be nil.
//   - cfg: the evolution.channel_feedback YAML block.
//
// Returns:
//   - *evolution.ChannelFeedbackRecorder: the started recorder, or nil.
func startChannelFeedback(
	ctx context.Context,
	comp *Components,
	newEvol *NewEvolutionComponents,
	asm *evolution.ActiveStrategyManager,
	cfg ares_config.ChannelFeedbackConfig,
) *evolution.ChannelFeedbackRecorder {
	if !cfg.AnyEnabled() {
		return nil
	}
	if newEvol == nil || newEvol.EvidenceStore == nil {
		log.WarnContext(ctx, "bootstrap: channel feedback disabled — no evidence store")
		return nil
	}
	if asm == nil {
		log.WarnContext(ctx, "bootstrap: channel feedback disabled — no active strategy manager, "+
			"every record would be unattributable")
		return nil
	}
	rec, err := evolution.NewChannelFeedbackRecorder(
		newEvol.EvidenceStore,
		activeStrategyIDFunc(asm),
		evolution.ChannelFeedbackChannels{
			Collaboration: cfg.CollabEnabled,
			ToolCalls:     cfg.ToolEnabled,
		},
	)
	if err != nil {
		log.WarnContext(ctx, "bootstrap: channel feedback disabled", "error", err)
		return nil
	}
	rec.Start(ctx)
	comp.bgGroup.Go(func() error {
		<-ctx.Done()
		rec.Stop()
		return nil
	})
	log.InfoContext(ctx, "bootstrap: channel feedback wired",
		"collaboration", cfg.CollabEnabled, "collaboration_weight", cfg.CollabWeight,
		"tool_call", cfg.ToolEnabled, "tool_call_weight", cfg.ToolWeight)
	return rec
}
