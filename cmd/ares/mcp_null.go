package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/Timwood0x10/ares/internal/runtime/protocol/mcp"
)

var mcpNullCmd = &cobra.Command{
	Use:   "mcp-null",
	Short: "Start minimal MCP null server (stdio)",
	Long: `Starts a minimal MCP server with an echo tool over stdio transport.
Useful for demos and testing the MCP protocol without external tools.`,
}

var mcpNullServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the MCP null server",
	RunE: func(cmd *cobra.Command, args []string) error {
		server := ares_mcp.NewMCPServer(
			ares_mcp.Implementation{Name: "ares_mcp-null", Version: "1.0.0"},
			ares_mcp.NewStdioServerTransport(),
		)

		echoSchema := json.RawMessage(`{
			"type": "object",
			"properties": {
				"message": {"type": "string"}
			},
			"required": ["message"]
		}`)

		err := server.RegisterTool("echo", "Echoes back the input (no-op for demos)", echoSchema,
			func(ctx context.Context, args map[string]any) (*ares_mcp.ToolCallResult, error) {
				msg, _ := args["message"].(string)
				return &ares_mcp.ToolCallResult{
					Content: []ares_mcp.ContentBlock{
						{Type: "text", Text: fmt.Sprintf("ares_mcp-null: %s", msg)},
					},
				}, nil
			})
		if err != nil {
			return fmt.Errorf("register echo tool: %w", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

		sigEg, sigCtx := errgroup.WithContext(ctx)
		sigEg.Go(func() error {
			select {
			case <-sigCh:
				cancel()
				return nil
			case <-sigCtx.Done():
				return sigCtx.Err()
			}
		})

		if err := server.Serve(ctx); err != nil {
			return fmt.Errorf("serve: %w", err)
		}
		// server.Serve returned nil (clean shutdown); cancel the signal
		// context so sigEg.Wait() does not block forever waiting for a
		// signal that will never arrive.
		cancel()
		if err := sigEg.Wait(); err != nil {
			return fmt.Errorf("signal handler: %w", err)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(mcpNullCmd)
	mcpNullCmd.AddCommand(mcpNullServeCmd)
}
