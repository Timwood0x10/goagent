// Package evolution is the ARES evolution engine.
//
// It holds the genetic machinery that acts on the L1 ToolClass graph:
// genome representation and workflow operators, patch generation and
// application, candidate pipelines with regression gates, staged deployment
// with monitoring/rollback, and the GA loop. Runtime wiring that deploys
// and judges strategies (gates, fitness aggregation, shadow sampling)
// lives next door in ares_evolution — engine vs. wiring, not old vs. new.
package evolution
