package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// stdioTransport implements JSON-RPC over stdin/stdout.
type stdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner
}

// ConnectStdio connects to an MCP server via stdio transport.
func ConnectStdio(ctx context.Context, name, command string, args []string) (*Client, error) {
	if !filepath.IsAbs(command) {
		return nil, fmt.Errorf("command must be an absolute path, got: %s", command)
	}

	cmd := exec.CommandContext(ctx, command, args...) //nolint:gosec // guarded by IsAbs check above
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	tr := &stdioTransport{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewScanner(stdout),
	}

	c := &Client{
		name:      name,
		transport: tr,
		idCounter: 1,
	}

	if err := c.initialize(ctx); err != nil {
		_ = cmd.Process.Kill() //nolint: errcheck
		return nil, fmt.Errorf("initialize: %w", err)
	}

	c.connected = true
	return c, nil
}

func (tr *stdioTransport) roundTrip(ctx context.Context, req jsonrpcRequest) (*jsonrpcResponse, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	if _, err := fmt.Fprintf(tr.stdin, "%s\n", data); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	type result struct {
		resp *jsonrpcResponse
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		if tr.stdout.Scan() {
			var resp jsonrpcResponse
			if err := json.Unmarshal(tr.stdout.Bytes(), &resp); err != nil {
				ch <- result{nil, err}
				return
			}
			ch <- result{&resp, nil}
		} else {
			ch <- result{nil, fmt.Errorf("connection closed")}
		}
	}()

	select {
	case r := <-ch:
		return r.resp, r.err
	case <-ctx.Done():
		// Context cancelled — close stdin to unblock the scan goroutine.
		_ = tr.stdin.Close()
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		// Timeout — close stdin to unblock the scan goroutine, preventing leak.
		_ = tr.stdin.Close()
		return nil, fmt.Errorf("timeout waiting for response")
	}
}

func (tr *stdioTransport) notify(ctx context.Context, notif jsonrpcNotification) error {
	data, err := json.Marshal(notif)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	// A wedged child process that stops draining stdin would block the pipe
	// write forever while the client mutex is held. Bound the write: on ctx
	// cancellation or timeout, close stdin to unblock the writer and return.
	writeCh := make(chan error, 1)
	go func() {
		_, err := fmt.Fprintf(tr.stdin, "%s\n", data)
		writeCh <- err
	}()
	select {
	case err := <-writeCh:
		if err != nil {
			return fmt.Errorf("write notification: %w", err)
		}
		return nil
	case <-ctx.Done():
		_ = tr.stdin.Close()
		return ctx.Err()
	case <-time.After(30 * time.Second):
		_ = tr.stdin.Close()
		return fmt.Errorf("timeout writing notification")
	}
}

func (tr *stdioTransport) close() error {
	if tr.cmd == nil || tr.cmd.Process == nil {
		return nil
	}
	// Close stdin to signal the child to exit, then kill to force-terminate.
	_ = tr.stdin.Close()
	if err := tr.cmd.Process.Kill(); err != nil {
		return fmt.Errorf("kill process: %w", err)
	}
	// Wait reaps the child process, preventing zombies.
	_ = tr.cmd.Wait()
	return nil
}
