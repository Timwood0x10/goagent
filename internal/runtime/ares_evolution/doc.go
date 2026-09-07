// Package evolution is the ARES evolution runtime wiring v1 (convergence
// Phase 3). The package keeps its historic name; the directory marks the
// layering.
//
// It holds everything that deploys and judges strategies at runtime:
// lifecycle gates, fitness aggregation over evidence windows, the shadow
// sampler (replay-only since M4-D), deployment adapters with
// monitor-and-rollback, guardrails, and the strategy/experience stores the
// planner reads. The genetic machinery itself (genome, patches, GA loop)
// lives next door in evolution/ — wiring vs. engine, not old vs. new.
package evolution
