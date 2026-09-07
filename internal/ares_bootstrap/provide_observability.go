// Package ares_bootstrap — runtime observability providers (the standalone
// dashboard :8090 service was deleted; the providers migrated into the
// introspection control plane).
//
// Previously ProvideDashboard assembled a standalone :8090 gin server that
// historically was never started by serve (its endpoints fed a server no one
// could reach). Under the consolidation the three surfaces with real
// data — evolution trajectory, human feedback, cross-Fabric
// spans — are wired straight into introspect.ControlServer via
// introspect options, and the :8090 server itself is gone.
package ares_bootstrap

import (
	"github.com/Timwood0x10/ares/internal/aresrecovery"
	"github.com/Timwood0x10/ares/internal/introspect"
)

// ObservabilityProviders bundles the provider adapters handed to the
// introspection control plane. Fields are nil when the backing component is
// nil (endpoint disabled), matching the old dashboard behavior.
type ObservabilityProviders struct {
	// Trajectory backs /api/evolution/trajectory.
	Trajectory introspect.EvolutionTrajectoryProvider
	// Feedback backs POST /api/evolution/feedback.
	Feedback introspect.EvolutionFeedbackSink
	// Spans backs /api/observability/spans.
	Spans introspect.ObservabilitySpansProvider
}

// ProvideObservability wraps the SHARED aresrecovery components (created once
// in Bootstrap, not per-call) as introspection control-plane providers, so the
// endpoints read the same tracer / feedback store the runtime writes.
func ProvideObservability(
	evolutionTracer *aresrecovery.EvolutionTracer,
	feedbackStore *aresrecovery.FeedbackStore,
	globalTracer *aresrecovery.GlobalTracer,
) *ObservabilityProviders {
	return &ObservabilityProviders{
		Trajectory: NewEvolutionTrajectoryProvider(evolutionTracer, feedbackStore),
		Feedback:   NewEvolutionFeedbackSink(feedbackStore),
		Spans:      NewObservabilitySpansProvider(globalTracer),
	}
}

// IntrospectOptions converts the providers into introspect.ControlServerOption
// values. The returned slice is always length 2 (evolution + observability);
// nil providers are ignored inside the server (endpoint disabled).
func (p *ObservabilityProviders) IntrospectOptions() []introspect.ControlServerOption {
	return []introspect.ControlServerOption{
		introspect.WithEvolution(p.Trajectory, p.Feedback),
		introspect.WithObservability(p.Spans),
	}
}
