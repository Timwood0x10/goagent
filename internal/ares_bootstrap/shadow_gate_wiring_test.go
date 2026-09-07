package ares_bootstrap

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- shadowGateMode three-branch invariant ---

// TestShadowGateMode_ThreeBranches is the table-driven unit test for the
// G2 registration decision. Every verification posture must map to exactly one
// (register, reason) pair, and the reason strings are fixed-vocabulary so the
// startup log, the gate-skipped metric and the lifecycle snapshot all agree.
func TestShadowGateMode_ThreeBranches(t *testing.T) {
	cases := []struct {
		name          string
		hasScorer     bool
		rollbackArmed bool
		wantRegister  bool
		wantReason    string
		wantErr       bool // errShadowGateNotConfigured (intentionally absent)
	}{
		{
			name:          "independent scorer wired -> gate registered (REAL verification)",
			hasScorer:     true,
			rollbackArmed: true,
			wantRegister:  true,
			wantReason:    "independent scorer wired",
		},
		{
			name:          "independent scorer wired, even with rollback off -> gate registered",
			hasScorer:     true,
			rollbackArmed: false,
			wantRegister:  true,
			wantReason:    "independent scorer wired",
		},
		{
			name:          "no scorer + rollback armed -> gate intentionally absent (canary+rollback carry risk)",
			hasScorer:     false,
			rollbackArmed: true,
			wantRegister:  false,
			wantReason:    "no scorer; canary+rollback armed",
			wantErr:       true,
		},
		{
			name:          "no scorer + no rollback -> fail-closed (the safety floor)",
			hasScorer:     false,
			rollbackArmed: false,
			wantRegister:  true,
			wantReason:    "no scorer and rollback disarmed — fail-closed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			register, reason, err := shadowGateMode(tc.hasScorer, tc.rollbackArmed)
			assert.Equal(t, tc.wantRegister, register, "register")
			assert.Equal(t, tc.wantReason, reason, "reason")
			if tc.wantErr {
				assert.Error(t, err)
				assert.True(t, errors.Is(err, errShadowGateNotConfigured),
					"intentional absence must be distinguishable via errors.Is")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestShadowGateMode_SafetyFloorWhenBothVerificationMissing is the INDEPENDENT
// case for the safety invariant: when neither preregistration verification
// (scorer) nor post-deployment verification (rollback) is armed, the G2 gate
// must STILL be registered and fail-closed. This is the guard that makes
// "no gate" a state the system can never silently fall into.
func TestShadowGateMode_SafetyFloorWhenBothVerificationMissing(t *testing.T) {
	register, reason, err := shadowGateMode(false, false)

	assert.True(t, register,
		"no scorer + no rollback: the gate must stay registered (fail-closed) — refusing promotion is the only correct answer")
	assert.Equal(t, "no scorer and rollback disarmed — fail-closed", reason)
	assert.NoError(t, err, "a fail-closed gate is a REGISTERED gate; it is not the intentional-absence error")
	assert.False(t, errors.Is(err, errShadowGateNotConfigured),
		"fail-closed must NOT be conflated with intentional absence")
}
