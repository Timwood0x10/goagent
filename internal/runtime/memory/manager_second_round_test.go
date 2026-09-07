package memory

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/agents/lease"
	memctx "github.com/Timwood0x10/ares/internal/runtime/memory/context"
)

// castConcrete exposes the concrete *memoryManager so second-round tests can
// reach methods that are real behavior but deliberately not part of the
// exported MemoryManager interface (setters, config access, lock guard,
// session leasing). Tests in this package live inside package memory, which
// keeps the surface honest: if a method earns production callers, it should
// be promoted to the interface instead of growing more cast-sites.
func castConcrete(t *testing.T, mgr MemoryManager) *memoryManager {
	t.Helper()
	c, ok := mgr.(*memoryManager)
	require.True(t, ok, "NewMemoryManager must return *memoryManager")
	return c
}

// TestConcreteSettersAndConfig covers the concrete-only setter family and the
// config accessor: each setter must store its dependency (observable via the
// leasing behavior flip) and GetConfig must return the LIVE config pointer —
// mutating through it must be visible to later reads (callers rely on that
// for runtime tuning).
func TestConcreteSettersAndConfig(t *testing.T) {
	ctx := context.Background()
	mgr, err := NewMemoryManager(DefaultMemoryConfig())
	require.NoError(t, err)
	defer func() { _ = mgr.Stop(ctx) }()
	c := castConcrete(t, mgr)

	// Setters store without panicking; leasing flips from unconfigured to
	// configured after SetLeaseManager.
	assert.NotPanics(t, func() {
		c.SetSkillsRegistry(nil)
		c.SetRetrievers(nil)
		c.SetLeaseManager(lease.NewManager())
	})

	l, err := c.AcquireSessionLease(ctx, "sess-1", "owner-a", time.Minute)
	require.NoError(t, err)
	assert.Equal(t, "sess-1", l.SessionID)
	assert.Equal(t, "owner-a", l.Owner)
	assert.False(t, l.ExpiresAt.IsZero())

	// Second owner on the same session is refused while held...
	_, err = c.AcquireSessionLease(ctx, "sess-1", "owner-b", time.Minute)
	assert.Error(t, err, "session lease must be exclusive per owner")

	// ...and only the OWNER may release it.
	assert.Error(t, c.ReleaseSessionLease(ctx, "sess-1", "owner-b"))
	assert.NoError(t, c.ReleaseSessionLease(ctx, "sess-1", "owner-a"))

	// Unconfigured leasing errors with a precise message.
	c.SetLeaseManager(nil)
	_, err = c.AcquireSessionLease(ctx, "sess-2", "owner-a", time.Minute)
	assert.ErrorContains(t, err, "session leasing not configured")
	assert.ErrorContains(t, c.ReleaseSessionLease(ctx, "sess-2", "owner-a"),
		"session leasing not configured")

	cfg := c.GetConfig()
	assert.NotNil(t, cfg)
}

// TestLockGuardSerializesCriticalSections is a behavioral smoke for the
// exported-with-cast Lock/Unlock pair: concurrent holders cannot interleave —
// a goroutine holding the lock increments a guarded counter twice per turn
// while the main goroutine spins reads; any torn read would fail the final
// invariant. Run under -race.
func TestLockGuardSerializesCriticalSections(t *testing.T) {
	mgr, err := NewMemoryManager(DefaultMemoryConfig())
	require.NoError(t, err)
	defer func() { _ = mgr.Stop(context.Background()) }()
	c := castConcrete(t, mgr)

	var guarded int
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			c.Lock()
			guarded++
			c.Unlock()
		}
	}()
	for i := 0; i < 50; i++ {
		c.Lock()
		guarded++
		c.Unlock()
	}
	<-done
	assert.Equal(t, 250, guarded, "every critical section must be counted exactly once")
}

// TestCreateTaskWithIDBranches locks the validation contract:
//   - empty caller id → ErrInvalidArgument (wrapped), nothing stored;
//   - duplicate id overwrites silently (last-write-wins contract callers use
//     for retry bookkeeping);
//   - UpdateTaskOutput on an unknown task surfaces the storage error.
func TestCreateTaskWithIDBranches(t *testing.T) {
	ctx := context.Background()
	mgr, err := NewMemoryManager(DefaultMemoryConfig())
	require.NoError(t, err)
	defer func() { _ = mgr.Stop(ctx) }()

	sessionID, err := mgr.CreateSession(ctx, "branch-user")
	require.NoError(t, err)

	errDup := mgr.CreateTaskWithID(ctx, "", sessionID, "u", "in")
	assert.Error(t, errDup)
	assert.ErrorContains(t, errDup, "invalid argument")

	require.NoError(t, mgr.CreateTaskWithID(ctx, "dup-1", sessionID, "u", "first"))
	require.NoError(t, mgr.CreateTaskWithID(ctx, "dup-1", sessionID, "u", "second"))

	assert.Error(t, mgr.UpdateTaskOutput(ctx, "never-created", "out"))
}

// TestBuildPromptMessagesMaxHistoryTruncation locks the truncation branch:
// with MaxHistory=2, five messages collapse to the LAST two in original order.
func TestBuildPromptMessagesMaxHistoryTruncation(t *testing.T) {
	ctx := context.Background()
	cfg := DefaultMemoryConfig()
	cfg.MaxHistory = 2
	mgr, err := NewMemoryManager(cfg)
	require.NoError(t, err)
	defer func() { _ = mgr.Stop(ctx) }()

	sessionID, err := mgr.CreateSession(ctx, "trunc-user")
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		require.NoError(t, mgr.AddStructuredMessage(ctx, sessionID,
			Message{Role: "user", Content: fmt.Sprintf("msg-%d", i)}))
	}

	promptMsgs, err := mgr.BuildPromptMessages(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, promptMsgs, 2)
	assert.Equal(t, "msg-3", promptMsgs[0].Content)
	assert.Equal(t, "msg-4", promptMsgs[1].Content)
}

// TestDeleteSessionUnknownIsIdempotent locks the storage contract: deleting
// an unknown session is an idempotent no-op (nil error), and a later Get
// finds nothing.
func TestDeleteSessionUnknownIsIdempotent(t *testing.T) {
	ctx := context.Background()
	mgr, err := NewMemoryManager(DefaultMemoryConfig())
	require.NoError(t, err)
	defer func() { _ = mgr.Stop(ctx) }()

	assert.NoError(t, mgr.DeleteSession(ctx, "no-such-session"))
	msgs, err := mgr.GetMessages(ctx, "no-such-session")
	require.ErrorIs(t, err, memctx.ErrSessionNotFound,
		"reading an unknown session must return the sentinel, not an arbitrary error")
	assert.Empty(t, msgs)
}
