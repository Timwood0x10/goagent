package evolution

// failClosedCode is the guardrail event code for the fail-closed state (C3.1).
const failClosedCode GuardrailErrorCode = "G1_CONSTRUCTION_FAILED"

// NewFailClosedGuardrails creates a G1 guardrail that always blocks. Used
// when the real guardrail construction fails (C3.1) so the system fails
// closed instead of silently allowing all candidates.
//
// Returns a guardrail configured with MaxStagnantGenerations=1 so it
// blocks after the first generation (stagnation triggers on gen 1 with
// no improvement). This is a reasonable fail-closed behavior:
// construction failed → the guardrail is configured to block quickly.
func NewFailClosedGuardrails() *EvolutionGuardrails {
	g := &EvolutionGuardrails{
		BaselineScore:          0,
		MaxStagnantGenerations: 1, // blocks after gen 1 with no improvement
		MaxLineageShare:        0.8,
		MaxEvents:              1000,
		bestBySource:           make(map[string]float64),
	}
	return g
}

// ErrCodeG1ConstructionFailed is the guardrail event code emitted when G1
// construction fails and the system enters fail-closed mode (C3.1).
//
// This is exported so the bootstrap layer and metrics can reference it
// consistently.
func ErrCodeG1ConstructionFailed() GuardrailErrorCode {
	return failClosedCode
}
