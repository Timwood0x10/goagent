package scoring

import (
	"sync"
	"testing"
)

func TestNewBudget(t *testing.T) {
	tests := []struct {
		name       string
		giveMax    int
		wantErr    error
		wantMaxLLM int
	}{
		{
			name:       "valid positive limit",
			giveMax:    10,
			wantErr:    nil,
			wantMaxLLM: 10,
		},
		{
			name:       "valid limit of 1",
			giveMax:    1,
			wantErr:    nil,
			wantMaxLLM: 1,
		},
		{
			name:       "zero limit rejected",
			giveMax:    0,
			wantErr:    ErrInvalidBudgetLimit,
			wantMaxLLM: 0,
		},
		{
			name:       "negative limit rejected",
			giveMax:    -5,
			wantErr:    ErrInvalidBudgetLimit,
			wantMaxLLM: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := NewBudget(tt.giveMax)
			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error %v, got nil", tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if int(b.MaxLLMCalls) != tt.wantMaxLLM {
				t.Errorf("MaxLLMCalls = %d, want %d", b.MaxLLMCalls, tt.wantMaxLLM)
			}
		})
	}
}

func TestCanCallLLM(t *testing.T) {
	t.Run("before exhausting budget", func(t *testing.T) {
		b, _ := NewBudget(3)
		for i := 0; i < 3; i++ {
			if !b.CanCallLLM() {
				t.Errorf("call %d: expected CanCallLLM() = true", i+1)
			}
			if !b.TryRecordLLMCall() {
				t.Errorf("call %d: expected TryRecordLLMCall() = true", i+1)
			}
		}
	})

	t.Run("after exhausting budget", func(t *testing.T) {
		b, _ := NewBudget(2)
		b.TryRecordLLMCall()
		b.TryRecordLLMCall()
		if b.CanCallLLM() {
			t.Error("expected CanCallLLM() = false after exhausting budget")
		}
		if b.TryRecordLLMCall() {
			t.Error("expected TryRecordLLMCall() = false after exhausting budget")
		}
	})

	t.Run("exactly at limit", func(t *testing.T) {
		b, _ := NewBudget(1)
		if !b.CanCallLLM() {
			t.Error("expected CanCallLLM() = true before any calls")
		}
		if !b.TryRecordLLMCall() {
			t.Error("expected TryRecordLLMCall() = true for first call")
		}
		if b.CanCallLLM() {
			t.Error("expected CanCallLLM() = false after 1 call with limit 1")
		}
	})
}

func TestTryRecordLLMCall(t *testing.T) {
	b, _ := NewBudget(5)

	for i := 1; i <= 5; i++ {
		if !b.TryRecordLLMCall() {
			t.Errorf("call %d: expected TryRecordLLMCall() = true", i)
		}
		used, max, _, _ := b.Usage()
		if used != i {
			t.Errorf("after %d calls: UsedLLMCalls = %d, want %d", i, used, i)
		}
		if max != 5 {
			t.Errorf("MaxLLMCalls = %d, want 5", max)
		}
	}
}

func TestRecordCacheHit(t *testing.T) {
	b, _ := NewBudget(10)

	b.RecordCacheHit()
	b.RecordCacheHit()
	b.RecordCacheHit()

	used, max, hits, _ := b.Usage()
	if used != 0 {
		t.Errorf("UsedLLMCalls = %d, want 0 (cache hits should not consume)", used)
	}
	if max != 10 {
		t.Errorf("MaxLLMCalls = %d, want 10", max)
	}
	if hits != 3 {
		t.Errorf("CacheHits = %d, want 3", hits)
	}
}

func TestRecordFallback(t *testing.T) {
	b, _ := NewBudget(10)

	b.RecordFallback()
	b.RecordFallback()

	_, _, _, fallbacks := b.Usage()
	if fallbacks != 2 {
		t.Errorf("FallbackCount = %d, want 2", fallbacks)
	}
}

func TestReset(t *testing.T) {
	b, _ := NewBudget(5)

	// Consume some budget.
	b.TryRecordLLMCall()
	b.TryRecordLLMCall()
	b.RecordCacheHit()
	b.RecordFallback()

	b.Reset()

	used, max, hits, fallbacks := b.Usage()
	if used != 0 {
		t.Errorf("after reset: UsedLLMCalls = %d, want 0", used)
	}
	if max != 5 {
		t.Errorf("after reset: MaxLLMCalls = %d, want 5 (limit preserved)", max)
	}
	if hits != 0 {
		t.Errorf("after reset: CacheHits = %d, want 0", hits)
	}
	if fallbacks != 0 {
		t.Errorf("after reset: FallbackCount = %d, want 0", fallbacks)
	}

	// Budget should be usable again after reset.
	if !b.CanCallLLM() {
		t.Error("expected CanCallLLM() = true after reset")
	}
}

func TestUsage(t *testing.T) {
	b, _ := NewBudget(20)

	b.TryRecordLLMCall()
	b.TryRecordLLMCall()
	b.TryRecordLLMCall()
	b.RecordCacheHit()
	b.RecordCacheHit()
	b.RecordFallback()

	used, max, hits, fallbacks := b.Usage()
	if used != 3 || max != 20 || hits != 2 || fallbacks != 1 {
		t.Errorf("Usage() = (%d, %d, %d, %d), want (3, 20, 2, 1)",
			used, max, hits, fallbacks)
	}
}

func TestBudgetConcurrency(t *testing.T) {
	b, _ := NewBudget(100)

	const goroutines = 50
	const opsPerGoroutine = 20

	var wg sync.WaitGroup
	wg.Add(goroutines * 3) // 3 kinds of operations per goroutine

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				b.TryRecordLLMCall()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				b.RecordCacheHit()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				b.RecordFallback()
				b.CanCallLLM()
				b.Usage()
			}
		}()
	}

	wg.Wait()

	used, max, hits, fallbacks := b.Usage()
	expectedHits := goroutines * opsPerGoroutine
	expectedFallbacks := goroutines * opsPerGoroutine

	if used != max {
		t.Errorf("UsedLLMCalls = %d, want %d (budget should be exactly exhausted)", used, max)
	}
	if max != 100 {
		t.Errorf("MaxLLMCalls = %d, want 100", max)
	}
	if hits != expectedHits {
		t.Errorf("CacheHits = %d, want %d", hits, expectedHits)
	}
	if fallbacks != expectedFallbacks {
		t.Errorf("FallbackCount = %d, want %d", fallbacks, expectedFallbacks)
	}
}
