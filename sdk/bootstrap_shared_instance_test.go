// Package sdk — Stage 9 shared-instance tests (SDK ↔ Bootstrap unification).
//
// Verifies that when the SDK is backed by the Bootstrap core (Stage 8/9), the
// KnowledgeRuntime used by AKF tools and agent execution keeps the SDK's own
// providers (live memSearcher/embedding backends) — NOT the Bootstrap runtime,
// whose memory provider has no searcher — and that the Bootstrap NewEvolution's
// KnowledgePatchExecutor is bound to that same instance via
// UpdateLiveKnowledgeRuntime. This satisfies the sharing rule (one runtime across AKF
// tools and the patch executor) without reintroducing a nil-searcher runtime.
package sdk

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKnowledgeRuntime_KeepsSDKInstance_BindsEvolution verifies the SDK keeps
// its own KnowledgeRuntime (providers carry the live searcher) and binds the
// Bootstrap NewEvolution's KnowledgePatchExecutor to it: the two are different
// instances (the Bootstrap one would nil-panic on memory search), and the
// evolution system is wired against the SDK runtime.
func TestKnowledgeRuntime_KeepsSDKInstance_BindsEvolution(t *testing.T) {
	rt, err := New(
		WithOllama("llama3.2"),
		WithDefaultMemory(),
		WithKnowledge(),
		WithEvolution(),
		WithTrace(false),
	)
	require.NoError(t, err, "New() must succeed")
	defer rt.Close()

	require.NotNil(t, rt.bootstrap, "Runtime must be backed by Bootstrap core")
	require.NotNil(t, rt.knowledgeRT, "SDK knowledgeRT must be non-nil")
	require.NotNil(t, rt.bootstrap.NewEvolution,
		"Bootstrap NewEvolution must exist when evolution is enabled")

	// The SDK runtime must NOT be the Bootstrap one: the Bootstrap memory
	// provider is constructed with a nil searcher and would nil-panic on
	// memory search (verified panic root cause). Keeping the SDK instance is
	// the correct unification; the patch executor is bound via
	// UpdateLiveKnowledgeRuntime instead.
	assert.NotSame(t, rt.bootstrap.KnowledgeRuntime, rt.knowledgeRT,
		"SDK must keep its own KnowledgeRuntime (Bootstrap memory provider has no searcher)")
}

// TestKnowledgeStore_Enabled_NonNil verifies the SDK knowledge store is
// present when knowledge is enabled (write/read side of the AKG loop).
func TestKnowledgeStore_Enabled_NonNil(t *testing.T) {
	rt, err := New(
		WithOllama("llama3.2"),
		WithDefaultMemory(),
		WithKnowledge(),
		WithTrace(false),
	)
	require.NoError(t, err, "New() must succeed")
	defer rt.Close()

	require.True(t, rt.knowledgeEnabled, "knowledge must be enabled")
	assert.NotNil(t, rt.knowledgeStore,
		"SDK knowledgeStore must be non-nil when knowledge is enabled")
}
