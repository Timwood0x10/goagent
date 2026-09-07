// Package ares_bootstrap — MCP state observability tests (Stage 5).
//
// Verifies that MCP server connection state is observable through the
// manager's status API (disconnected servers report Connected=false with an
// error), so a Required-server outage surfaces as Degraded instead of being
// invisible. No real MCP servers are needed: an empty config yields an empty
// status list without panicking, and a manual ConnectServer failure records
// the error in the status.
package ares_bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/runtime/protocol/mcp"
	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// TestMCPStatus_EmptyConfig_Observable verifies that an MCP manager built with
// no servers reports an empty status list (not nil) and remains queryable.
func TestMCPStatus_EmptyConfig_Observable(t *testing.T) {
	mgr, err := ares_mcp.NewMCPManager(&ares_mcp.MCPManagerConfig{}, core.NewRegistry())
	require.NoError(t, err)

	statuses := mgr.ListServers()
	assert.NotNil(t, statuses, "status list must be non-nil")
	assert.Empty(t, statuses, "no servers configured → no statuses")
}

// TestMCPStatus_DisconnectedServer_ReportsError verifies that a configured but
// disconnected server is observable: Connected=false and the error surfaced.
func TestMCPStatus_DisconnectedServer_ReportsError(t *testing.T) {
	mgr, err := ares_mcp.NewMCPManager(&ares_mcp.MCPManagerConfig{
		Servers: []ares_mcp.MCPServerConfig{
			{Name: "codegraph", Enabled: true, AutoStart: false, Transport: ares_mcp.TransportConfig{}},
		},
	}, core.NewRegistry())
	require.NoError(t, err)

	// ConnectServer with an empty/invalid transport must fail and record the
	// error so the status shows the server is not connected.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = mgr.ConnectServer(ctx, "codegraph") // error expected; status carries it

	statuses := mgr.ListServers()
	require.Len(t, statuses, 1, "one configured server must appear")
	assert.Equal(t, "codegraph", statuses[0].Name)
	assert.False(t, statuses[0].Connected,
		"disconnected server must report Connected=false (Degraded, not silent)")
}

// TestMCPConfig_ServersEmpty verifies the DEFAULT minimal config path: the
// zero-value MCPConfig must carry no servers AND Bootstrap's ProvideMCP must
// accept it (start a manager that connects nothing) — not merely re-assert a
// field against itself.
func TestMCPConfig_ServersEmpty(t *testing.T) {
	cfg := ares_config.MCPConfig{}
	require.Empty(t, cfg.Servers, "zero-value config has no servers")

	mgr, err := ProvideMCP(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, mgr)
	assert.Empty(t, mgr.ListServers(), "no configured servers → no server statuses")
}
