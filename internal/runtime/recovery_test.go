package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBasicRecoveryPlugin(t *testing.T) {
	p := NewBasicRecoveryPlugin("recovery")
	ctx := context.Background()

	// No allowlist yet.
	assert.False(t, p.ShouldRecover(ctx, StepFailure{StepID: "s1"}, ExecutionState{}))

	p.AllowStep("s1")
	assert.True(t, p.ShouldRecover(ctx, StepFailure{StepID: "s1"}, ExecutionState{}))

	// Other steps not allowed.
	assert.False(t, p.ShouldRecover(ctx, StepFailure{StepID: "s2"}, ExecutionState{}))

	p.RevokeStep("s1")
	assert.False(t, p.ShouldRecover(ctx, StepFailure{StepID: "s1"}, ExecutionState{}))
}
