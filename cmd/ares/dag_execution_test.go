package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/fabric/agent"
)

// stubBody is a distinct-identity Cognition so the test can tell which body
// the gate selected. It is never executed in the gate tests; the adapter
// tests drive it with a canned outcome.
type stubBody struct {
	outcome    *agentfabric.StepOutcome
	outcomeErr error
}

func (s *stubBody) ExecuteStep(_ context.Context, _ *models.Task) (*agentfabric.StepOutcome, error) {
	return s.outcome, s.outcomeErr
}

// TestResolveMaxPlanDepth pins the M4-A2 depth mapping: zero/absent means the
// planner default (agentfabric.DefaultMaxPlanDepth), a positive value passes
// through, and a negative — which validation rejects at load — can never
// widen or remove the guard even if it reaches the resolver.
func TestResolveMaxPlanDepth(t *testing.T) {
	tests := []struct {
		name       string
		configured int
		want       int
	}{
		{"absent means planner default", 0, agentfabric.DefaultMaxPlanDepth},
		{"custom depth passes through", 3, 3},
		{"negative falls back to default", -1, agentfabric.DefaultMaxPlanDepth},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ares_config.DAGExecutionConfig{MaxPlanDepth: tt.configured}
			if got := resolveMaxPlanDepth(cfg); got != tt.want {
				t.Errorf("resolveMaxPlanDepth(max_plan_depth=%d) = %d, want %d",
					tt.configured, got, tt.want)
			}
		})
	}
}

// TestResolveReaperGrace pins the P0-1 grace mapping: zero/absent passes
// through as 0 so the reaper's own 30s default stays the single source of
// truth; a positive config value wins; a negative (unreachable through
// Validate, defended anyway) degrades to the default rather than disabling
// the read-window protection.
func TestResolveReaperGrace(t *testing.T) {
	tests := []struct {
		name   string
		config ares_config.DAGExecutionConfig
		want   time.Duration
	}{
		{"absent section defaults to reaper", ares_config.DAGExecutionConfig{}, 0},
		{"zero passes through", ares_config.DAGExecutionConfig{ReaperGrace: 0}, 0},
		{"positive wins", ares_config.DAGExecutionConfig{ReaperGrace: 2 * time.Minute}, 2 * time.Minute},
		{"negative degrades to default", ares_config.DAGExecutionConfig{ReaperGrace: -time.Second}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveReaperGrace(tt.config); got != tt.want {
				t.Errorf("resolveReaperGrace(%+v) = %s, want %s", tt.config, got, tt.want)
			}
		})
	}
}

// TestPeerCapabilities_UnifiedL2Set pins the M4-D single path: every peer
// advertises exactly the L2 set (ares/root, ares/plan, ares/answer,
// tool/<name> per bound tool) and never a primary type — the canary
// partition is retired with the gate.
func TestPeerCapabilities_UnifiedL2Set(t *testing.T) {
	got := peerCapabilities([]string{"grep", "read"})
	want := map[string]bool{"ares/root": true, "ares/plan": true, "ares/answer": true, "tool/grep": true, "tool/read": true}
	if len(got) != len(want) {
		t.Fatalf("caps = %v, want exactly the L2 set", got)
	}
	for _, c := range got {
		if !want[c] {
			t.Errorf("caps contain %q, want only the L2 set", c)
		}
		if c == "researcher" {
			t.Error("caps must NOT contain a primary type — there is no legacy traffic anymore")
		}
	}

	empty := peerCapabilities(nil)
	if len(empty) != 3 {
		t.Errorf("caps with no tools = %v, want exactly the 3 ares/* capabilities", empty)
	}
	blank := peerCapabilities([]string{"", "grep"})
	for _, c := range blank {
		if c == "tool/" {
			t.Error("blank tool names must be skipped, not advertised as bare tool/")
		}
	}
}

// TestSelectRecoveryBody pins the M4-D dispatch: L2-capability tasks take
// the router; a nil router or a non-routable capability falls back (nil =
// caller builds a fresh cognition-backed executor, never ReAct).
func TestSelectRecoveryBody(t *testing.T) {
	router := &stubBody{}

	tests := []struct {
		name       string
		router     agentfabric.Cognition
		capability string
		wantRouter bool
	}{
		{"legacy primary caps fall back", router, "researcher", false},
		{"plan tasks take the router", router, "ares/plan", true},
		{"tool tasks take the router", router, "tool/grep", true},
		{"answer tasks take the router", router, "ares/answer", true},
		{"nil router never selected", nil, "ares/plan", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectRecoveryBody(tt.router, tt.capability)
			if tt.wantRouter && got == nil {
				t.Errorf("capability %q must take the router", tt.capability)
			}
			if !tt.wantRouter && got != nil {
				t.Errorf("capability %q must fall back, got router", tt.capability)
			}
		})
	}
}

// TestNewCognitionExecutor_TranslatesOutcome pins the adapter contract:
// Done/Checkpoint/Result ride field-for-field across the boundary, and body
// errors propagate without translation.
func TestNewCognitionExecutor_TranslatesOutcome(t *testing.T) {
	if _, err := newCognitionExecutor("a1", "ares/plan", nil); err == nil {
		t.Error("nil body must fail at construction, not at first quantum")
	}

	want := &agentfabric.StepOutcome{Done: true, Checkpoint: "ck"}
	exec, err := newCognitionExecutor("a1", models.AgentType("ares/plan"),
		&stubBody{outcome: want})
	if err != nil {
		t.Fatalf("construction error = %v", err)
	}
	if exec.ID() != "a1" || exec.Type() != models.AgentType("ares/plan") {
		t.Errorf("identity not preserved: %q %q", exec.ID(), exec.Type())
	}
	got, err := exec.ExecuteStep(context.Background(), &models.Task{})
	if err != nil {
		t.Fatalf("ExecuteStep error = %v", err)
	}
	if !got.Done || got.Checkpoint != "ck" {
		t.Errorf("outcome not translated field-for-field: %+v", got)
	}

	boom := errors.New("boom")
	execErr, err := newCognitionExecutor("a2", "x", &stubBody{outcomeErr: boom})
	if err != nil {
		t.Fatalf("construction error = %v", err)
	}
	if _, err := execErr.ExecuteStep(context.Background(), &models.Task{}); !errors.Is(err, boom) {
		t.Errorf("body error must propagate untranslated, got %v", err)
	}
}
