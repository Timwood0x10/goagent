package taskfabric

import (
	"context"
	"testing"
	"time"
)

// TestFailRetryBudgetContract locks the RetryPolicy.MaxRetries semantics
// end-to-end through Fabric.Fail: MaxRetries is the TOTAL attempt budget
// (first try included), so a budget of 2 allows exactly one requeue. 0 and
// negative budgets mean no retries — the first Fail is terminal. The old
// CanRetry treated MaxRetries<=0 as unlimited, so a zero-value task could
// never reach FAILED.
func TestFailRetryBudgetContract(t *testing.T) {
	cases := []struct {
		name       string
		maxRetries int
		// wantStates[i] is the expected state after the (i+1)-th Fail.
		wantStates []TaskState
	}{
		{
			name:       "zero_budget_first_fail_is_terminal",
			maxRetries: 0,
			wantStates: []TaskState{StateFailed},
		},
		{
			name:       "negative_budget_no_retries",
			maxRetries: -3,
			wantStates: []TaskState{StateFailed},
		},
		{
			name:       "budget_one_single_attempt",
			maxRetries: 1,
			wantStates: []TaskState{StateFailed},
		},
		{
			name:       "budget_two_requeues_once_then_fails_out",
			maxRetries: 2,
			wantStates: []TaskState{StateReady, StateFailed},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := NewFabric()
			tk := newTask("t1")
			tk.RetryPolicy = RetryPolicy{MaxRetries: tc.maxRetries}
			if err := f.Create(tk); err != nil {
				t.Fatalf("Create: %v", err)
			}
			for i, want := range tc.wantStates {
				epoch, err := f.Acquire("t1", "agent-a", time.Minute)
				if err != nil {
					t.Fatalf("acquire attempt %d: %v", i+1, err)
				}
				if err := f.Start("t1", "agent-a", epoch); err != nil {
					t.Fatalf("start attempt %d: %v", i+1, err)
				}
				if err := f.Fail("t1", "agent-a", epoch); err != nil {
					t.Fatalf("fail attempt %d: %v", i+1, err)
				}
				got, err := f.Task("t1")
				if err != nil {
					t.Fatalf("task lookup after fail %d: %v", i+1, err)
				}
				if got.State != want {
					t.Fatalf("state after fail %d = %s, want %s", i+1, got.State, want)
				}
				// A requeued task must come back unowned so the next
				// Acquire can win it (Agent 死亡 ≠ Task 死亡).
				if want == StateReady && got.Owner != "" {
					t.Fatalf("requeued task must be unowned, got %q", got.Owner)
				}
			}
		})
	}
}

// TestCompilePlanDefaultRetryBudget locks the plan-layer default: a PlanStep
// with MaxRetries <= 0 means "unset" and compiles to the kernel submission
// default (2 total attempts), never to the fabric's 0 = no retries. Without
// this resolution every unset plan step would have been unretryable after
// the CanRetry fix.
func TestCompilePlanDefaultRetryBudget(t *testing.T) {
	cases := []struct {
		name       string
		maxRetries int
		want       int
	}{
		{name: "unset_zero_gets_kernel_default", maxRetries: 0, want: planDefaultMaxRetries},
		{name: "negative_gets_kernel_default", maxRetries: -1, want: planDefaultMaxRetries},
		{name: "positive_honored_verbatim", maxRetries: 5, want: 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := NewFabric()
			ids, err := f.CompilePlan(context.Background(), []PlanStep{
				{ID: "s1", Capability: "coder", MaxRetries: tc.maxRetries},
			})
			if err != nil {
				t.Fatalf("CompilePlan: %v", err)
			}
			tk, err := f.Task(ids[0])
			if err != nil {
				t.Fatalf("Task: %v", err)
			}
			if tk.RetryPolicy.MaxRetries != tc.want {
				t.Fatalf("compiled MaxRetries = %d, want %d", tk.RetryPolicy.MaxRetries, tc.want)
			}
		})
	}
}
