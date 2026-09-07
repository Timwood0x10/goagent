// nolint: errcheck // Test code may ignore return values
// nolint: errcheck // Test code may ignore return values
package context

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSessionMemory(t *testing.T) {
	t.Run("create session memory", func(t *testing.T) {
		memory := NewSessionMemory(100, time.Minute)

		if memory == nil {
			t.Errorf("memory should not be nil")
		}
	})

	t.Run("set and get session", func(t *testing.T) {
		memory := NewSessionMemory(100, time.Minute)
		messages := []Message{{Role: "user", Content: "hello"}}

		// Test code: memory.Set is used to set test data
		// nolint: errcheck // This is intentional in test code
		err := memory.Set(context.Background(), "sess1", "user1", messages)
		if err != nil {
			t.Errorf("set error: %v", err)
		}

		data, exists := memory.Get(context.Background(), "sess1")
		if !exists {
			t.Errorf("session should exist")
		}
		if data.UserID != "user1" {
			t.Errorf("expected user1, got %s", data.UserID)
		}
	})

	t.Run("add message", func(t *testing.T) {
		memory := NewSessionMemory(100, time.Minute)
		// Create session first
		_ = memory.Set(context.Background(), "sess1", "user1", nil) // Test setup, error ignored

		err := memory.AddMessage(context.Background(), "sess1", Message{Role: "user", Content: "test"})
		if err != nil {
			t.Errorf("add message error: %v", err)
		}
	})

	t.Run("delete session", func(t *testing.T) {
		memory := NewSessionMemory(100, time.Minute)
		// Create session first
		_ = memory.Set(context.Background(), "sess1", "user1", nil) // Test setup, error ignored

		err := memory.Delete(context.Background(), "sess1")
		if err != nil {
			t.Errorf("delete error: %v", err)
		}

		_, exists := memory.Get(context.Background(), "sess1")
		if exists {
			t.Errorf("session should not exist after delete")
		}
	})

	t.Run("size", func(t *testing.T) {
		memory := NewSessionMemory(100, time.Minute)
		// Create session first
		_ = memory.Set(context.Background(), "sess1", "user1", nil) // Test setup, error ignored

		if memory.Size() != 1 {
			t.Errorf("expected size 1, got %d", memory.Size())
		}
	})
}

func TestTaskMemoryTTL(t *testing.T) {
	t.Run("TTL cleanup removes expired tasks", func(t *testing.T) {
		memory := NewTaskMemory(100, 100*time.Millisecond)
		ctx := context.Background()

		err := memory.Set(ctx, "task1", "sess1", "user1", "input1")
		if err != nil {
			t.Fatalf("set error: %v", err)
		}

		_, exists := memory.Get(ctx, "task1")
		if !exists {
			t.Errorf("task1 should exist immediately after set")
		}

		memory.Start(ctx)
		defer memory.Stop()

		time.Sleep(200 * time.Millisecond)

		_, exists = memory.Get(ctx, "task1")
		if exists {
			t.Errorf("task1 should have been cleaned up after TTL expired")
		}
	})

	t.Run("Start is idempotent", func(t *testing.T) {
		memory := NewTaskMemory(100, time.Minute)
		ctx := context.Background()

		memory.Start(ctx)
		memory.Start(ctx)
		memory.Start(ctx)

		memory.Stop()
	})

	t.Run("Stop is idempotent", func(t *testing.T) {
		memory := NewTaskMemory(100, time.Minute)
		ctx := context.Background()

		memory.Start(ctx)
		memory.Stop()
		memory.Stop()
		memory.Stop()
	})
}

func TestCache(t *testing.T) {
	t.Run("create cache", func(t *testing.T) {
		cache := NewCache(100, time.Minute)

		if cache == nil {
			t.Errorf("cache should not be nil")
		}
	})

	t.Run("set and get", func(t *testing.T) {
		cache := NewCache(100, time.Minute)

		_ = cache.Set(context.Background(), "key1", "value1") // Test setup, error ignored

		val, exists := cache.Get(context.Background(), "key1")
		if !exists {
			t.Errorf("key should exist")
		}
		if val != "value1" {
			t.Errorf("expected value1, got %v", val)
		}
	})

	t.Run("delete", func(t *testing.T) {
		cache := NewCache(100, time.Minute)
		_ = cache.Set(context.Background(), "key1", "value1") // Test setup, error ignored

		_ = cache.Delete(context.Background(), "key1") // Test operation, error ignored

		_, exists := cache.Get(context.Background(), "key1")
		if exists {
			t.Errorf("key should not exist after delete")
		}
	})
}

// TestSessionMemory_ConcurrentGet tests that concurrent Get calls don't cause data races.
func TestSessionMemory_ConcurrentGet(t *testing.T) {
	sm := NewSessionMemory(100, 10*time.Second)
	sm.StartCleanup()
	defer sm.Close(context.Background())

	// Pre-populate a session.
	err := sm.Set(context.Background(), "session-1", "user-1", []Message{
		{Role: "user", Content: "hello", Time: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to set session: %v", err)
	}

	// Concurrent reads should not race.
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				data, ok := sm.Get(context.Background(), "session-1")
				if !ok {
					t.Error("expected session to exist")
					return
				}
				if data.SessionID != "session-1" {
					t.Errorf("expected session ID session-1, got %s", data.SessionID)
				}
			}
		}()
	}
	wg.Wait()
}

// TestSessionMemory_GetUpdatesAccessedAt tests that Get updates the access time.
func TestSessionMemory_GetUpdatesAccessedAt(t *testing.T) {
	sm := NewSessionMemory(100, 10*time.Second)
	defer sm.Close(context.Background())

	err := sm.Set(context.Background(), "session-1", "user-1", []Message{
		{Role: "user", Content: "hello", Time: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to set session: %v", err)
	}

	// Get the session.
	data, ok := sm.Get(context.Background(), "session-1")
	if !ok {
		t.Fatal("expected session to exist")
	}

	originalAccessedAt := data.AccessedAt

	// Wait a bit and get again.
	time.Sleep(10 * time.Millisecond)

	data, ok = sm.Get(context.Background(), "session-1")
	if !ok {
		t.Fatal("expected session to exist after second get")
	}

	if !data.AccessedAt.After(originalAccessedAt) {
		t.Error("AccessedAt should be updated on each Get call")
	}
}

// TestSessionMemory_GetExpiredSession tests that expired sessions are cleaned up on Get.
func TestSessionMemory_GetExpiredSession(t *testing.T) {
	sm := NewSessionMemory(100, 50*time.Millisecond)
	defer sm.Close(context.Background())

	err := sm.Set(context.Background(), "session-1", "user-1", []Message{
		{Role: "user", Content: "hello", Time: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to set session: %v", err)
	}

	// Wait for TTL to expire.
	time.Sleep(60 * time.Millisecond)

	_, ok := sm.Get(context.Background(), "session-1")
	if ok {
		t.Error("expired session should not be found")
	}
}

func contains(text, substr string) bool {
	return strings.Contains(text, substr)
}
