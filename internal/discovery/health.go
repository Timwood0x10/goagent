package discovery

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	api_mcp "github.com/Timwood0x10/ares/internal/mcpclient"
)

// allowedMCPBinaryDirs lists directories whose binaries are trusted to be
// probed as MCP stdio servers. Binaries discovered outside these roots are
// refused to prevent arbitrary command execution from untrusted sources.
var allowedMCPBinaryDirs = []string{
	"/usr/local/bin",
	"/usr/bin",
	"/opt/homebrew/bin",
}

// MCPHealthChecker checks MCP servers by connecting and listing tools.
type MCPHealthChecker struct {
	timeout time.Duration
}

// NewMCPHealthChecker creates a health checker for MCP services.
func NewMCPHealthChecker(timeout time.Duration) *MCPHealthChecker {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &MCPHealthChecker{timeout: timeout}
}

func (c *MCPHealthChecker) CheckHealth(ctx context.Context, svc *DiscoveredService) (*HealthStatus, error) {
	if svc == nil {
		return nil, errors.New("service is nil")
	}

	// Pick best endpoint.
	endpoint, args := bestEndpoint(svc)
	if endpoint == "" {
		return &HealthStatus{
			Healthy:   false,
			Message:   "no endpoint",
			CheckedAt: time.Now(),
		}, nil
	}

	start := time.Now()
	checkCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	status := probeMCP(checkCtx, svc.Identity.Name, endpoint, args)
	status.Latency = time.Since(start)
	status.CheckedAt = time.Now()

	return status, nil
}

// probeMCP connects to an MCP server, does initialize → list_tools → close.
// Only http/https endpoints and binaries located under allowedMCPBinaryDirs
// are accepted; anything else is refused to prevent SSRF and arbitrary
// command execution from untrusted discovery records.
func probeMCP(ctx context.Context, name, endpoint string, args []string) *HealthStatus {
	var client *api_mcp.Client
	var err error

	if isURL(endpoint) {
		if !isAllowedMCPURL(endpoint) {
			return &HealthStatus{
				Healthy: false,
				Message: fmt.Sprintf("refused endpoint %q: only http/https URLs are allowed", endpoint),
			}
		}
		client, err = api_mcp.ConnectSSE(ctx, name, endpoint)
	} else {
		if !isAllowedMCPBinary(endpoint) {
			return &HealthStatus{
				Healthy: false,
				Message: fmt.Sprintf("refused binary %q: not in allowlist", endpoint),
			}
		}
		client, err = api_mcp.ConnectStdio(ctx, name, endpoint, args)
	}
	if err != nil {
		return &HealthStatus{
			Healthy: false,
			Message: fmt.Sprintf("connect failed: %v", err),
		}
	}
	defer func() {
		if err := client.Close(); err != nil {
			log.Warn("health: close mcp client failed", "name", name, "error", err)
		}
	}()

	// list_tools verifies the server is fully functional.
	tools, err := client.ListTools(ctx)
	if err != nil {
		return &HealthStatus{
			Healthy: false,
			Message: fmt.Sprintf("list_tools failed: %v", err),
		}
	}

	return &HealthStatus{
		Healthy: true,
		Message: fmt.Sprintf("ok, %d tools", len(tools)),
	}
}

// isAllowedMCPURL reports whether endpoint is an http or https URL.
// Other schemes (file://, gopher://, etc.) are refused to prevent SSRF.
func isAllowedMCPURL(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u == nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	return scheme == "http" || scheme == "https"
}

// isAllowedMCPBinary reports whether endpoint is a binary path that lives
// under one of the trusted binary directories. Symlinks are resolved before
// comparison to prevent traversal tricks.
func isAllowedMCPBinary(endpoint string) bool {
	if endpoint == "" {
		return false
	}
	resolved, err := filepath.EvalSymlinks(endpoint)
	if err != nil {
		// Fall back to the raw path; the stat check below will still apply.
		resolved = endpoint
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return false
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		return false
	}
	for _, dir := range allowedMCPBinaryDirs {
		root, err := filepath.EvalSymlinks(dir)
		if err != nil {
			root = dir
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

// bestEndpoint extracts the highest-confidence endpoint from a service.
func bestEndpoint(svc *DiscoveredService) (string, []string) {
	if len(svc.Records) == 0 {
		return "", nil
	}

	best := svc.Records[0]
	for _, r := range svc.Records[1:] {
		if r.Confidence > best.Confidence {
			best = r
		}
	}
	return best.Endpoint, best.Args
}

// isURL checks if an endpoint looks like a URL (http/https).
func isURL(endpoint string) bool {
	return strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://")
}
