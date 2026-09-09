package sdk

import (
	"context"
	"errors"
	"strings"
	"testing"

	tools "github.com/Timwood0x10/ares/internal/apitools"
	mcp "github.com/Timwood0x10/ares/internal/knowledge/mcp"
)

// TestAkfToolAdapter exercises the akfToolAdapter that bridges the internal
// AKF mcp.Tool (string in / string out) to the public tools.Tool interface
// (map in / Result out). It verifies the metadata accessors, the JSON
// marshalling boundary, and the rule that tool-execute errors are wrapped in
// Result.Success=false (not propagated as Go errors) while marshal failures
// are propagated as Go errors.
func TestAkfToolAdapter(t *testing.T) {
	tests := []struct {
		name      string
		tool      mcp.Tool
		params    map[string]any
		wantName  string
		wantDesc  string
		wantErr   bool
		wantOK    bool
		wantData  string
		dataCheck string
	}{
		{
			name: "name_and_description",
			tool: mcp.Tool{
				Name:        "x",
				Description: "d",
				Execute: func(_ context.Context, _ string) (string, error) {
					return "", nil
				},
			},
			wantName: "x",
			wantDesc: "d",
		},
		{
			// Parameters must return nil: AKF tools accept a free-form JSON
			// string and do not declare a JSON Schema here.
			name: "parameters_nil",
			tool: mcp.Tool{
				Name:        "p",
				Description: "pdesc",
				Execute: func(_ context.Context, _ string) (string, error) {
					return "", nil
				},
			},
			wantName: "p",
		},
		{
			// Capabilities must return nil: AKF knowledge tools do not declare
			// planner capabilities.
			name: "capabilities_nil",
			tool: mcp.Tool{
				Name:        "c",
				Description: "cdesc",
				Execute: func(_ context.Context, _ string) (string, error) {
					return "", nil
				},
			},
			wantName: "c",
		},
		{
			name: "execute_success",
			tool: mcp.Tool{
				Name:        "ok",
				Description: "returns ok",
				Execute: func(_ context.Context, input string) (string, error) {
					return "out:" + input, nil
				},
			},
			params:   map[string]any{"goal": "test"},
			wantOK:   true,
			wantData: "out:",
		},
		{
			// Inner Execute errors must NOT propagate as a Go error; they are
			// reported via Result.Success=false with the error message in Data
			// so the agent loop can surface them to the LLM.
			name: "execute_inner_error",
			tool: mcp.Tool{
				Name:        "boom",
				Description: "fails",
				Execute: func(_ context.Context, _ string) (string, error) {
					return "", errors.New("boom")
				},
			},
			params:    map[string]any{"goal": "test"},
			wantOK:    false,
			wantErr:   false,
			dataCheck: "boom",
		},
		{
			// Params that cannot be JSON-marshalled (channels are unsupported
			// by encoding/json) must propagate as a Go error and return a
			// zero-valued Result (Success=false).
			name: "execute_marshal_error",
			tool: mcp.Tool{
				Name:        "marshal",
				Description: "marshal fail",
				Execute: func(_ context.Context, _ string) (string, error) {
					t.Error("Execute should not be called on marshal failure")
					return "unreached", nil
				},
			},
			params:  map[string]any{"bad": make(chan int)},
			wantErr: true,
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &akfToolAdapter{tool: tt.tool}
			if tt.wantName != "" && a.Name() != tt.wantName {
				t.Errorf("Name() = %q, want %q", a.Name(), tt.wantName)
			}
			if tt.wantDesc != "" && a.Description() != tt.wantDesc {
				t.Errorf("Description() = %q, want %q", a.Description(), tt.wantDesc)
			}
			// Always assert nil Parameters/Capabilities for AKF adapters.
			if a.Parameters() != nil {
				t.Errorf("Parameters() = %v, want nil", a.Parameters())
			}
			if a.Capabilities() != nil {
				t.Errorf("Capabilities() = %v, want nil", a.Capabilities())
			}

			// Subtests without params only exercise metadata accessors.
			if tt.params == nil && tt.wantData == "" && !tt.wantErr && !tt.wantOK {
				return
			}

			res, err := a.Execute(context.Background(), tt.params)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected marshal error, got nil")
				}
				if res.Success {
					t.Errorf("expected Result.Success=false on marshal error, got true")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if res.Success != tt.wantOK {
				t.Errorf("Result.Success = %v, want %v", res.Success, tt.wantOK)
			}
			if tt.dataCheck != "" {
				dataStr, _ := res.Data.(string)
				if !strings.Contains(dataStr, tt.dataCheck) {
					t.Errorf("Result.Data = %q, want substring %q", dataStr, tt.dataCheck)
				}
			}
			if tt.wantData != "" {
				dataStr, _ := res.Data.(string)
				if !strings.Contains(dataStr, tt.wantData) {
					t.Errorf("Result.Data = %q, want substring %q", dataStr, tt.wantData)
				}
			}
		})
	}
}

// TestRegisterAKFTools covers the registerAKFTools wiring helper: nil-runtime
// rejection, the four-tool registration surface, and idempotency semantics.
//
// Note: the public tools.Registry.Register silently overwrites duplicates
// (it does not return an error on re-registration of an existing name), so
// the duplicate case asserts idempotent behaviour rather than an error.
func TestRegisterAKFTools(t *testing.T) {
	t.Run("nil_runtime_returns_error", func(t *testing.T) {
		reg := tools.NewEmptyRegistry()
		err := registerAKFTools(reg, nil)
		if err == nil {
			t.Fatal("expected error for nil runtime, got nil")
		}
		if !strings.Contains(err.Error(), "knowledge runtime is nil") {
			t.Errorf("error = %v, want substring %q", err, "knowledge runtime is nil")
		}
	})

	t.Run("registers_four_tools", func(t *testing.T) {
		reg := tools.NewEmptyRegistry()
		rt := newTestKnowledgeRuntime()
		if err := registerAKFTools(reg, rt); err != nil {
			t.Fatalf("registerAKFTools error: %v", err)
		}
		want := []string{"build_graph", "compile_context", "query_knowledge", "distill_memory"}
		names := reg.List()
		for _, w := range want {
			got, ok := reg.Get(w)
			if !ok {
				t.Errorf("Get(%q) not found; registry has %v", w, names)
				continue
			}
			if got.Name() != w {
				t.Errorf("Get(%q).Name() = %q", w, got.Name())
			}
		}
		// AKF tools must be registered alongside any pre-existing tools, not
		// replace them. With an empty registry the count is exactly four.
		if len(names) != len(want) {
			t.Errorf("registry size = %d, want %d (registry=%v)", len(names), len(want), names)
		}
	})

	t.Run("duplicate_registration_is_idempotent", func(t *testing.T) {
		// The public tools.Registry.Register overwrites duplicates silently
		// rather than rejecting them. Asserting this behaviour documents the
		// contract so a future change to the registry surfaces here.
		reg := tools.NewEmptyRegistry()
		rt := newTestKnowledgeRuntime()
		if err := registerAKFTools(reg, rt); err != nil {
			t.Fatalf("first registerAKFTools error: %v", err)
		}
		if err := registerAKFTools(reg, rt); err != nil {
			t.Fatalf("second registerAKFTools error: %v", err)
		}
		// After two registrations the registry still has exactly the four AKF
		// tools (overwrites do not grow the map).
		want := []string{"build_graph", "compile_context", "query_knowledge", "distill_memory"}
		if len(reg.List()) != len(want) {
			t.Errorf("registry size after duplicate = %d, want %d", len(reg.List()), len(want))
		}
		for _, w := range want {
			if _, ok := reg.Get(w); !ok {
				t.Errorf("Get(%q) not found after duplicate registration", w)
			}
		}
	})
}
