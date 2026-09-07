package evolution

import (
	"context"
	"fmt"
	"log/slog"

	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
)

// wireAfterRunReport configures the AfterRun hook on system when a report path
// is specified. The hook generates a final evolution report including promotion
// evidence and saves it to the configured path.
//
// Args:
//
//	system - the wired evolution system to attach the hook to.
//	cfg - system configuration (uses ReportPath).
//	evidenceAgg - resolved evidence aggregator (may be nil).
//	promoter - resolved promotion logic (may be nil).
func wireAfterRunReport(
	system *evolution.WiredEvolutionSystem,
	cfg *SystemConfig,
	evidenceAgg func(ctx context.Context, strategyID string) (Evidence, error),
	promoter func(ctx context.Context, strategyID string, ev Evidence) (string, string, error),
) {
	if cfg.ReportPath == "" {
		return
	}

	system.AfterRun = func(ctx context.Context, sys *evolution.WiredEvolutionSystem) error {
		best := sys.Population.Best()
		if best == nil {
			return nil
		}

		// Register strategy evidence key for phenotype fallback resolution.
		if reg, ok := cfg.EvidenceAggregator.(interface{ RegisterStrategyKey(string, string) }); ok {
			reg.RegisterStrategyKey(best.ID, best.ComputeEvidenceKey())
		}

		var ev Evidence
		var state, reason string
		if evidenceAgg != nil {
			var err error
			ev, err = evidenceAgg(ctx, best.ID)
			if err != nil {
				slog.WarnContext(ctx, "after-run: evidence aggregation failed",
					"strategy_id", best.ID, "error", err)
			}
		}
		if promoter != nil && ev.SampleCount > 0 {
			var err error
			state, reason, err = promoter(ctx, best.ID, ev)
			if err != nil {
				slog.WarnContext(ctx, "after-run: promotion evaluation failed",
					"strategy_id", best.ID, "error", err)
			}
		}

		report, err := evolution.GenerateReport(ctx, sys)
		if err != nil {
			return fmt.Errorf("generate report: %w", err)
		}

		report.WinnerStrategyID = best.ID
		report.WinnerScore = best.Score
		if ev.SampleCount > 0 {
			report.PromotionState = state
			report.PromotionReason = reason
			report.SuccessRate = ev.SuccessRate
			report.SampleCount = ev.SampleCount
			report.Confidence = ev.Confidence
		}

		slog.InfoContext(ctx, "evolution run complete",
			"winner", best.ID,
			"fitness", best.Score,
			"promotion_state", state,
			"sample_count", ev.SampleCount,
		)

		if err := evolution.SaveReport(ctx, report, cfg.ReportPath); err != nil {
			return fmt.Errorf("save report: %w", err)
		}

		slog.InfoContext(ctx, "evolution report saved",
			"path", cfg.ReportPath,
		)

		return nil
	}
}
