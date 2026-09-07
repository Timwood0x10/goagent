package evolution

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/agents"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// TestBuildMutator_WiresToolPool locks the C6 single-source wiring: when the
// SystemConfig carries a ToolPool, buildMutator must thread it into the mutator
// so the elite/random mutation path can actually emit Params["tools"]. Before
// this, the option was never supplied here — the pool was dead configuration and
// only guided mutation produced tool choices.
func TestBuildMutator_WiresToolPool(t *testing.T) {
	cfg := DefaultSystemConfig()
	cfg.ToolPool = []string{"web_search,calculator", "web_search"}

	mr, err := buildMutator(cfg)
	require.NoError(t, err)
	require.NotNil(t, mr, "buildMutator must return a non-nil result")
	require.NotNil(t, mr.rawMutator, "buildMutator must produce a raw mutator")

	parent := &mutation.Strategy{
		ID: "parent", Version: 1,
		Params: map[string]any{"tools": "web_search"},
	}
	children, mutateErr := mr.rawMutator.Mutate(context.Background(), parent, 200)
	require.NoError(t, mutateErr)
	require.NotEmpty(t, children)

	// With a 2-element pool and a 15% tool-mutation distribution over 200 draws,
	// the pool must surface in at least one child's tools param. Prove the pool
	// reached the mutator (not just guided aliases): the value must equal a pool
	// entry verbatim.
	sawPoolTool := false
	for _, c := range children {
		tools, ok := c.Params[agents.ParamKeyTools].(string)
		if !ok {
			continue
		}
		for _, poolEntry := range cfg.ToolPool {
			if tools == poolEntry {
				sawPoolTool = true
				break
			}
		}
	}
	require.True(t, sawPoolTool,
		"buildMutator with a ToolPool must produce a child whose tools param comes from the pool")
}
