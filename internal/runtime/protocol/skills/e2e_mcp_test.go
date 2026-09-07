package ares_skills_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/runtime/protocol/mcp"
	"github.com/Timwood0x10/ares/internal/runtime/protocol/skills"
	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// TestE2EMCPHelperProcess is the re-exec'd real MCP server subprocess: it
// serves a real ares_mcp MCPServer over stdio with one registered tool. The
// parent test spawns this binary so the Skill activation path crosses a real
// process boundary (true JSON-RPC over stdin/stdout), unlike the in-process
// fakeMCPConnector used elsewhere.
func TestE2EMCPHelperProcess(t *testing.T) {
	if os.Getenv("ARES_E2E_MCP_HELPER") != "1" {
		return
	}
	transport := ares_mcp.NewStdioServerTransport()
	server := ares_mcp.NewMCPServer(ares_mcp.Implementation{Name: "e2e-server", Version: "1.0.0"}, transport)
	_ = server.RegisterTool("greet", "Greet someone",
		json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`),
		func(_ context.Context, args map[string]any) (*ares_mcp.ToolCallResult, error) {
			return &ares_mcp.ToolCallResult{
				Content: []ares_mcp.ContentBlock{{Type: "text", Text: "hello " + fmt.Sprint(args["name"])}},
			}, nil
		})
	// Serve blocks until the transport (stdin) closes; the parent kills the
	// subprocess when the test ends.
	_ = server.Serve(context.Background())
	os.Exit(0)
}

// TestE2ESkillActivateConnectsRealMCPServer walks the full Capability Fabric
// path against a REAL MCP server subprocess: a skill declares an mcp tool →
// the catalog indexes it → Activate lazily connects via
// MCPManager.ConnectServer (stdio) → the server's tool is registered → calling
// it executes remotely across the process boundary.
func TestE2ESkillActivateConnectsRealMCPServer(t *testing.T) {
	if testing.Short() {
		t.Skip("E2E spawns a real MCP server subprocess")
	}

	// 1. Skill directory declaring an mcp tool.
	root := t.TempDir()
	skillDir := filepath.Join(root, "e2e-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("# e2e-skill\n\ndescription: e2e skill\n\n## When to use\n\nfor e2e verification\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "skill.yaml"),
		[]byte("id: e2e-skill\nname: E2E Skill\ndescription: e2e skill\ntools:\n  - id: greet\n    type: mcp\n    server: e2e\n"), 0o644))

	// 2. Build the catalog over the skill.
	cat := ares_skills.NewCatalog(ares_skills.CatalogConfig{ProjectSkillsDir: root})
	require.NoError(t, cat.Build())

	// 3. Real MCPManager with a stdio transport to a re-exec'd subprocess.
	exe, err := os.Executable()
	require.NoError(t, err)
	reg := core.NewRegistry()
	mcpMgr, err := ares_mcp.NewMCPManager(&ares_mcp.MCPManagerConfig{
		Servers: []ares_mcp.MCPServerConfig{{
			Name: "e2e",
			Transport: ares_mcp.TransportConfig{
				Type: ares_mcp.TransportTypeStdio,
				Stdio: &ares_mcp.StdioConfig{
					Command: exe,
					Args:    []string{"-test.run", "TestE2EMCPHelperProcess"},
					Env:     map[string]string{"ARES_E2E_MCP_HELPER": "1"},
				},
			},
			Timeout: 15 * time.Second,
		}},
	}, reg)
	require.NoError(t, err)

	// 4. Activate the skill → lazy ConnectServer over real stdio.
	cat.SetMCPConnector(mcpMgr)
	tools, err := cat.Activate(context.Background(), "e2e-skill")
	require.NoError(t, err)
	require.Len(t, tools, 1)

	// 5. The remote tool must be registered under the mcp namespace (the real
	// server's ListTools result, not a stub).
	tool, ok := reg.Get("mcp.e2e.greet")
	require.True(t, ok, "real MCP server tool must be registered after activation")

	// 6. Execute end-to-end across the process boundary.
	res, err := tool.Execute(context.Background(), map[string]any{"name": "world"})
	require.NoError(t, err)
	require.True(t, res.Success)
	require.Contains(t, fmt.Sprint(res.Data), "hello world")
}
