package ares_mcp

import (
	"context"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

func TestNewMCPConfigWatcher_NilManager(t *testing.T) {
	w, err := NewMCPConfigWatcher(nil, "test.yaml")
	require.Error(t, err)
	assert.Nil(t, w)
}

func TestNewMCPConfigWatcher_EmptyPath(t *testing.T) {
	mgr, _ := NewMCPManager(nil, core.NewRegistry())
	w, err := NewMCPConfigWatcher(mgr, "")
	require.Error(t, err)
	assert.Nil(t, w)
}

func TestNewMCPConfigWatcher_FileNotFound(t *testing.T) {
	mgr, _ := NewMCPManager(nil, core.NewRegistry())
	w, err := NewMCPConfigWatcher(mgr, "/tmp/nonexistent-mcp-config.yaml")
	require.Error(t, err)
	assert.Nil(t, w)
}

func TestApplyConfig_NoChanges(t *testing.T) {
	reg := core.NewRegistry()
	mgr, err := NewMCPManager(nil, reg)
	require.NoError(t, err)

	changes := mgr.ApplyConfig(context.Background(), nil)
	assert.Empty(t, changes)
}

func TestApplyConfig_WithServers(t *testing.T) {
	reg := core.NewRegistry()
	mgr, err := NewMCPManager(nil, reg)
	require.NoError(t, err)

	cfg := &MCPManagerConfig{
		Servers: []MCPServerConfig{
			{
				Name:      "test-server",
				Enabled:   false, // not enabled, so no connection attempt
				AutoStart: false,
				Transport: TransportConfig{Type: "stdio"},
			},
		},
	}

	changes := mgr.ApplyConfig(context.Background(), cfg)
	// Server is not enabled, so no connect action
	assert.Empty(t, changes)
}

func TestApplyConfig_RemoveServer(t *testing.T) {
	reg := core.NewRegistry()
	mgr, err := NewMCPManager(nil, reg)
	require.NoError(t, err)

	// Set up initial config with a server.
	oldCfg := &MCPManagerConfig{
		Servers: []MCPServerConfig{
			{Name: "removed-server", Enabled: true},
		},
	}
	mgr.config = oldCfg

	// Apply empty config — should not panic (no clients to disconnect).
	changes := mgr.ApplyConfig(context.Background(), nil)
	assert.Len(t, changes, 0, "no disconnect happens because no client is connected")
}

func TestHasConfigChanged_SameConfig(t *testing.T) {
	a := &MCPServerConfig{
		Name: "srv",
		Transport: TransportConfig{
			Type: "stdio",
			Stdio: &StdioConfig{
				Command: "echo",
				Args:    []string{"hello"},
			},
		},
	}
	b := &MCPServerConfig{
		Name: "srv",
		Transport: TransportConfig{
			Type: "stdio",
			Stdio: &StdioConfig{
				Command: "echo",
				Args:    []string{"hello"},
			},
		},
	}
	assert.False(t, hasConfigChanged(a, b))
}

func TestHasConfigChanged_DifferentCommand(t *testing.T) {
	a := &MCPServerConfig{
		Transport: TransportConfig{
			Type:  "stdio",
			Stdio: &StdioConfig{Command: "echo", Args: []string{"hello"}},
		},
	}
	b := &MCPServerConfig{
		Transport: TransportConfig{
			Type:  "stdio",
			Stdio: &StdioConfig{Command: "echo", Args: []string{"world"}},
		},
	}
	assert.True(t, hasConfigChanged(a, b))
}

func TestHasConfigChanged_DifferentType(t *testing.T) {
	a := &MCPServerConfig{
		Transport: TransportConfig{Type: "stdio"},
	}
	b := &MCPServerConfig{
		Transport: TransportConfig{Type: "sse"},
	}
	assert.True(t, hasConfigChanged(a, b))
}

func TestHasConfigChanged_DifferentSSEURL(t *testing.T) {
	a := &MCPServerConfig{
		Transport: TransportConfig{
			Type: "sse",
			SSE:  &SSEConfig{URL: "http://localhost:8000/sse"},
		},
	}
	b := &MCPServerConfig{
		Transport: TransportConfig{
			Type: "sse",
			SSE:  &SSEConfig{URL: "http://other:8000/sse"},
		},
	}
	assert.True(t, hasConfigChanged(a, b))
}

func TestConvertConfigFile_NilInput(t *testing.T) {
	cfg := convertConfigFile(nil)
	assert.Nil(t, cfg)
}

func TestConvertConfigFile_EmptyServers(t *testing.T) {
	cfg := convertConfigFile(&MCPConfigFile{})
	assert.Nil(t, cfg)
}

func TestConvertConfigFile_StdioServer(t *testing.T) {
	cfgFile := &MCPConfigFile{}
	cfgFile.MCP.Servers = []struct {
		Name      string `yaml:"name"`
		Enabled   bool   `yaml:"enabled"`
		AutoStart bool   `yaml:"auto_start"`
		Timeout   int    `yaml:"timeout"`
		Transport struct {
			Type  string `yaml:"type"`
			Stdio *struct {
				Command string            `yaml:"command"`
				Args    []string          `yaml:"args"`
				Env     map[string]string `yaml:"env,omitempty"`
				WorkDir string            `yaml:"work_dir,omitempty"`
			} `yaml:"stdio,omitempty"`
			SSE *struct {
				URL     string            `yaml:"url"`
				Headers map[string]string `yaml:"headers,omitempty"`
				Timeout int               `yaml:"timeout,omitempty"`
			} `yaml:"sse,omitempty"`
		} `yaml:"transport"`
	}{
		{
			Name:      "codegraph",
			Enabled:   true,
			AutoStart: true,
			Timeout:   30,
			Transport: struct {
				Type  string `yaml:"type"`
				Stdio *struct {
					Command string            `yaml:"command"`
					Args    []string          `yaml:"args"`
					Env     map[string]string `yaml:"env,omitempty"`
					WorkDir string            `yaml:"work_dir,omitempty"`
				} `yaml:"stdio,omitempty"`
				SSE *struct {
					URL     string            `yaml:"url"`
					Headers map[string]string `yaml:"headers,omitempty"`
					Timeout int               `yaml:"timeout,omitempty"`
				} `yaml:"sse,omitempty"`
			}{
				Type: "stdio",
				Stdio: &struct {
					Command string            `yaml:"command"`
					Args    []string          `yaml:"args"`
					Env     map[string]string `yaml:"env,omitempty"`
					WorkDir string            `yaml:"work_dir,omitempty"`
				}{
					Command: "my-mcp-server",
					Args:    []string{"--port", "8080"},
				},
			},
		},
	}

	mgrCfg := convertConfigFile(cfgFile)
	require.NotNil(t, mgrCfg)
	require.Len(t, mgrCfg.Servers, 1)
	assert.Equal(t, "codegraph", mgrCfg.Servers[0].Name)
	assert.True(t, mgrCfg.Servers[0].Enabled)
	assert.Equal(t, 30*time.Second, mgrCfg.Servers[0].Timeout)
	assert.Equal(t, "stdio", mgrCfg.Servers[0].Transport.Type)
	require.NotNil(t, mgrCfg.Servers[0].Transport.Stdio)
	assert.Equal(t, "my-mcp-server", mgrCfg.Servers[0].Transport.Stdio.Command)
	assert.Equal(t, []string{"--port", "8080"}, mgrCfg.Servers[0].Transport.Stdio.Args)
}

func TestIsRelevantEvent(t *testing.T) {
	// Write event on the exact config file should be relevant.
	rel := isRelevantEvent(fsnotify.Event{
		Name: "/tmp/ares_mcp.yaml",
		Op:   fsnotify.Write,
	}, "/tmp/ares_mcp.yaml")
	assert.True(t, rel)

	// Chmod event should NOT be relevant.
	rel = isRelevantEvent(fsnotify.Event{
		Name: "/tmp/ares_mcp.yaml",
		Op:   fsnotify.Chmod,
	}, "/tmp/ares_mcp.yaml")
	assert.False(t, rel)
}
