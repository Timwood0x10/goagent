// Package ares_bootstrap — MCP provider.
package ares_bootstrap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/runtime/protocol/mcp"
	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// mapMCPServerConfig converts ares_config MCPServerEntry to ares_mcp MCPServerConfig.
func mapMCPServerConfig(cfg ares_config.MCPConfig) *ares_mcp.MCPManagerConfig {
	if len(cfg.Servers) == 0 {
		return nil
	}
	mgrCfg := &ares_mcp.MCPManagerConfig{
		Servers: make([]ares_mcp.MCPServerConfig, 0, len(cfg.Servers)),
	}
	for _, s := range cfg.Servers {
		sc := ares_mcp.MCPServerConfig{
			Name:      s.Name,
			Enabled:   s.Enabled,
			AutoStart: s.AutoStart,
			Timeout:   time.Duration(s.Timeout) * time.Second,
			Transport: ares_mcp.TransportConfig{
				Type: s.Transport.Type,
			},
		}
		switch s.Transport.Type {
		case "stdio":
			if s.Transport.Stdio != nil {
				sc.Transport.Stdio = &ares_mcp.StdioConfig{
					Command: s.Transport.Stdio.Command,
					Args:    s.Transport.Stdio.Args,
					Env:     s.Transport.Stdio.Env,
					WorkDir: s.Transport.Stdio.WorkDir,
				}
			}
		case "sse":
			if s.Transport.SSE != nil {
				sc.Transport.SSE = &ares_mcp.SSEConfig{
					URL:     s.Transport.SSE.URL,
					Headers: s.Transport.SSE.Headers,
					Timeout: time.Duration(s.Transport.SSE.Timeout) * time.Second,
				}
			}
		}
		mgrCfg.Servers = append(mgrCfg.Servers, sc)
	}
	return mgrCfg
}

func ProvideMCP(ctx context.Context, cfg ares_config.MCPConfig) (*ares_mcp.MCPManager, error) {
	if len(cfg.Servers) == 0 {
		// No MCP servers configured is valid for minimal configs
		registry := core.NewRegistry()
		return ares_mcp.NewMCPManager(&ares_mcp.MCPManagerConfig{}, registry)
	}
	registry := core.NewRegistry()
	mgrCfg := mapMCPServerConfig(cfg)
	mcpMgr, err := ares_mcp.NewMCPManager(mgrCfg, registry)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: MCP manager: %w", err)
	}
	if err := mcpMgr.Start(ctx); err != nil {
		return nil, fmt.Errorf("bootstrap: MCP manager start: %w", err)
	}
	return mcpMgr, nil
}

// SetupMCP is a backward-compatible alias kept for existing callers.
func SetupMCP(ctx context.Context, cfg *ares_config.MCPConfig, registry *core.Registry) (*ares_mcp.MCPManager, error) {
	if cfg == nil || len(cfg.Servers) == 0 {
		return nil, errors.New("bootstrap: MCP not configured")
	}
	mgrCfg := mapMCPServerConfig(*cfg)
	mcpMgr, err := ares_mcp.NewMCPManager(mgrCfg, registry)
	if err != nil {
		return nil, fmt.Errorf("create ares_mcp manager: %w", err)
	}
	if err := mcpMgr.Start(ctx); err != nil {
		return nil, fmt.Errorf("start ares_mcp manager: %w", err)
	}
	return mcpMgr, nil
}
