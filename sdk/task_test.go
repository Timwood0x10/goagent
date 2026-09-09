package sdk

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	llmcore "github.com/Timwood0x10/ares/internal/llmcore"
)

// contains reports whether s contains substr.
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// TestSubmit_RegisteredAgent verifies the closed loop
// (NewRuntime → RegisterAgent → Submit → 结果): a task
// submitted with a registered capability is executed by the agent registered
// for it, and the result flows back unchanged.
func TestSubmit_RegisteredAgent(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()
	rt.llmSvc = &mockLLMSvc{responses: []*llmcore.GenerateResponse{
		{Content: "handled by coder", Usage: llmcore.TokenUsage{PromptTokens: 2, CompletionTokens: 4}},
	}}

	rt.RegisterAgent("coder", WithInstruction("you handle code tasks"))
	res, err := rt.Submit(context.Background(), Task{Capability: "coder", Input: "refactor main.go"})
	if err != nil {
		t.Fatalf("Submit error: %v", err)
	}
	if res.Output != "handled by coder" {
		t.Fatalf("Output = %q, want %q", res.Output, "handled by coder")
	}
}

// TestSubmit_UnregisteredCapabilityAutoCreates verifies that a runtime never
// refuses a well-formed task just because its capability was not pre-
// registered: Submit auto-creates a capability-named agent and runs it.
func TestSubmit_UnregisteredCapabilityAutoCreates(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()
	rt.llmSvc = &mockLLMSvc{responses: []*llmcore.GenerateResponse{
		{Content: "auto-created agent ran it"},
	}}

	res, err := rt.Submit(context.Background(), Task{Capability: "auditor", Input: "audit config"})
	if err != nil {
		t.Fatalf("Submit error: %v", err)
	}
	if res.Output != "auto-created agent ran it" {
		t.Fatalf("Output = %q, want %q", res.Output, "auto-created agent ran it")
	}
}

// TestSubmit_EmptyCapabilityUsesAnyRegistered verifies that a task without a
// capability is dispatched to any registered agent (the flat peer pool has no
// required capability).
func TestSubmit_EmptyCapabilityUsesAnyRegistered(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()
	rt.llmSvc = &mockLLMSvc{responses: []*llmcore.GenerateResponse{
		{Content: "any registered agent"},
	}}

	rt.RegisterAgent("coder")
	res, err := rt.Submit(context.Background(), Task{Input: "do the thing"})
	if err != nil {
		t.Fatalf("Submit error: %v", err)
	}
	if res.Output != "any registered agent" {
		t.Fatalf("Output = %q, want %q", res.Output, "any registered agent")
	}
}

// blockingLLM blocks until the context is done, then returns the context
// error — it makes Timeout propagation observable through Submit → Run →
// agentloop.Engine.
type blockingLLM struct{}

func (b *blockingLLM) Generate(ctx context.Context, _ *llmcore.GenerateRequest) (*llmcore.GenerateResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (b *blockingLLM) GetProvider() llmcore.LLMProvider { return llmcore.LLMProviderOllama }
func (b *blockingLLM) GetModel() string                 { return "mock-blocking" }
func (b *blockingLLM) Close()                           {}

// TestSubmit_TimeoutPropagates verifies that Task.Timeout bounds the
// execution: a blocked LLM surfaces a deadline-exceeded cause from Submit
// (context cancellation propagates, never swallowed).
func TestSubmit_TimeoutPropagates(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()
	rt.llmSvc = &blockingLLM{}

	_, err := rt.Submit(context.Background(), Task{
		Capability: "slow",
		Input:      "long task",
		Timeout:    50 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("Submit must surface the timeout error")
	}
	// The agentloop engine wraps the LLM error with FriendlyErr (a string
	// label, not an unwrappable %w chain), so assert on the surfaced message
	// containing the deadline cause rather than errors.Is.
	if !errors.Is(err, context.DeadlineExceeded) &&
		!contains(err.Error(), "deadline exceeded") &&
		!contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("Submit error = %v, want a deadline-exceeded cause", err)
	}
}

// TestRegisterAgent_FirstCapabilityWins verifies the registration semantics:
// the first agent registered for a capability wins, and a later registration
// for the same capability does not silently replace it.
func TestRegisterAgent_FirstCapabilityWins(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()

	first := rt.RegisterAgent("coder")
	second := rt.RegisterAgent("writer")
	if got := rt.lookupAgent("coder"); got != first {
		t.Fatal("first registration for a capability must win")
	}
	if got := rt.lookupAgent("writer"); got != second {
		t.Fatal("second capability must map to its agent")
	}
	// Re-registering the same capability must NOT replace the first agent.
	_ = rt.RegisterAgent("coder")
	if got := rt.lookupAgent("coder"); got != first {
		t.Fatal("re-registration must not replace the winning agent")
	}
}
