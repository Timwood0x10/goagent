package memory

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	patch "github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

// TestMemoryConfigPatch_RacesWithHotPaths pins the config race fix: the
// MemoryPatchExecutor mutates MaxHistory/CleanOptions under the write lock,
// so the hot read paths (BuildPromptMessages / BuildContext) MUST take the
// read lock. Run under -race, the old unlocked reads failed this test.
func TestMemoryConfigPatch_RacesWithHotPaths(t *testing.T) {
	ctx := context.Background()
	mgr, err := NewMemoryManager(DefaultMemoryConfig())
	require.NoError(t, err)
	defer func() { _ = mgr.Stop(ctx) }()

	sessionID, err := mgr.CreateSession(ctx, "race-user")
	require.NoError(t, err)
	require.NoError(t, mgr.AddMessage(ctx, sessionID, "user", "hello"))

	reg := patch.NewRegistry()
	require.NoError(t, reg.RegisterComponent(NewMemoryPatchExecutor(castConcrete(t, mgr))))

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)

	// Writer: apply config patches in a loop (evolution ticker shape).
	go func() {
		defer wg.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			h := 3 + i%50
			_ = reg.Apply(ctx, patch.RuntimePatch{
				Type:   patch.PatchChangePlanner,
				Target: "memory",
				Value:  map[string]any{"max_history": h},
			})
			i++
		}
	}()

	// Reader: hammer the hot prompt/context paths concurrently.
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = mgr.BuildPromptMessages(ctx, sessionID)
			_, _ = mgr.BuildContext(ctx, "query", sessionID)
		}
	}()

	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
}
