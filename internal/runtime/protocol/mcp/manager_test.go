package ares_mcp

import (
	"context"
	"testing"

	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

func TestMCPManagerStartStop(t *testing.T) {
	registry := core.NewRegistry()
	config := &MCPManagerConfig{
		Servers: []MCPServerConfig{
			{
				Name:      "disabled-server",
				Enabled:   false,
				AutoStart: true,
				Transport: TransportConfig{Type: "stdio", Stdio: &StdioConfig{Command: "echo"}},
			},
		},
	}

	manager := newTestManager(t, config, registry)
	ctx := context.Background()

	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start error: %v", err)
	}

	servers := manager.ListServers()
	if len(servers) != 0 {
		t.Errorf("ListServers count = %d, want 0 (disabled server)", len(servers))
	}

	if err := manager.Stop(ctx); err != nil {
		t.Fatalf("Stop error: %v", err)
	}
}

func TestMCPManagerStartEmptyConfig(t *testing.T) {
	registry := core.NewRegistry()
	manager := newTestManager(t, nil, registry)

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start error: %v", err)
	}
}

func TestMCPManagerConnectDisconnect(t *testing.T) {
	registry := core.NewRegistry()
	config := &MCPManagerConfig{
		Servers: []MCPServerConfig{
			{
				Name:      "test",
				Enabled:   true,
				AutoStart: false,
				Transport: TransportConfig{Type: "stdio", Stdio: &StdioConfig{Command: "echo"}},
			},
		},
	}

	manager := newTestManager(t, config, registry)

	// ConnectServer will fail because "echo" isn't a real MCP server,
	// but we can test the config lookup.
	err := manager.ConnectServer(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent server")
	}
}

func TestMCPManagerGetClient(t *testing.T) {
	registry := core.NewRegistry()
	manager := newTestManager(t, &MCPManagerConfig{}, registry)

	_, ok := manager.GetClient("nonexistent")
	if ok {
		t.Error("expected false for nonexistent client")
	}
}

func TestMCPManagerListServers(t *testing.T) {
	registry := core.NewRegistry()
	manager := newTestManager(t, &MCPManagerConfig{}, registry)

	servers := manager.ListServers()
	if servers == nil {
		servers = []MCPServerStatus{}
	}
	if len(servers) != 0 {
		t.Errorf("expected empty server list, got %d", len(servers))
	}
}

func TestMCPToolFactory(t *testing.T) {
	registry := core.NewRegistry()
	manager := newTestManager(t, &MCPManagerConfig{}, registry)
	factory := NewMCPToolFactory(manager)

	if factory.Name() != "mcp" {
		t.Errorf("Name = %s, want mcp", factory.Name())
	}

	if factory.Description() == "" {
		t.Error("Description should not be empty")
	}
}

func TestMCPToolFactoryValidateConfig(t *testing.T) {
	registry := core.NewRegistry()
	manager := newTestManager(t, &MCPManagerConfig{}, registry)
	factory := NewMCPToolFactory(manager)

	tests := []struct {
		name    string
		config  map[string]interface{}
		wantErr bool
	}{
		{
			name: "valid stdio",
			config: map[string]interface{}{
				"name":    "test",
				"command": "codegraph",
			},
		},
		{
			name: "valid sse",
			config: map[string]interface{}{
				"name":           "test",
				"transport_type": "sse",
				"url":            "http://localhost:8080",
			},
		},
		{
			name:    "missing name",
			config:  map[string]interface{}{"command": "test"},
			wantErr: true,
		},
		{
			name:    "stdio missing command",
			config:  map[string]interface{}{"name": "test"},
			wantErr: true,
		},
		{
			name: "sse missing url",
			config: map[string]interface{}{
				"name":           "test",
				"transport_type": "sse",
			},
			wantErr: true,
		},
		{
			name: "unsupported transport",
			config: map[string]interface{}{
				"name":           "test",
				"transport_type": "grpc",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := factory.ValidateConfig(tt.config)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func newTestManager(t *testing.T, config *MCPManagerConfig, registry *core.Registry) *MCPManager {
	t.Helper()
	m, err := NewMCPManager(config, registry)
	if err != nil {
		t.Fatalf("NewMCPManager failed: %v", err)
	}
	return m
}

func TestToStringSlice(t *testing.T) {
	tests := []struct {
		name  string
		input interface{}
		want  []string
	}{
		{"nil", nil, nil},
		{"string slice", []string{"a", "b"}, []string{"a", "b"}},
		{"interface slice", []interface{}{"a", "b"}, []string{"a", "b"}},
		{"mixed interface", []interface{}{"a", 1, "b"}, []string{"a", "b"}},
		{"invalid type", 42, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toStringSlice(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("len = %d, want %d", len(got), len(tt.want))
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %s, want %s", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestToStringMap(t *testing.T) {
	tests := []struct {
		name  string
		input interface{}
		want  map[string]string
	}{
		{"nil", nil, nil},
		{"string map", map[string]string{"a": "b"}, map[string]string{"a": "b"}},
		{"interface map", map[string]interface{}{"a": "b"}, map[string]string{"a": "b"}},
		{"mixed interface", map[string]interface{}{"a": "b", "c": 1}, map[string]string{"a": "b"}},
		{"invalid type", 42, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toStringMap(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("len = %d, want %d", len(got), len(tt.want))
				return
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("[%s] = %s, want %s", k, got[k], v)
				}
			}
		})
	}
}
