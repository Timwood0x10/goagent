package ares_mcp

//nolint: errcheck // best-effort operations: ResponseWriter writes, cleanup Close/Wait, deferred shutdown
import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// MCPServerConfig holds configuration for a single MCP server.
type MCPServerConfig struct {
	Name      string          `yaml:"name" json:"name"`
	Transport TransportConfig `yaml:"transport" json:"transport"`
	Timeout   time.Duration   `yaml:"timeout" json:"timeout"`
	Enabled   bool            `yaml:"enabled" json:"enabled"`
	AutoStart bool            `yaml:"auto_start" json:"auto_start"`
}

// MCPManagerConfig holds configuration for the MCP manager.
type MCPManagerConfig struct {
	Servers []MCPServerConfig `yaml:"servers" json:"servers"`
}

// MCPServerStatus represents the current status of an MCP server.
type MCPServerStatus struct {
	Name      string    `json:"name"`
	Connected bool      `json:"connected"`
	ToolCount int       `json:"tool_count"`
	Version   string    `json:"version"`
	Error     string    `json:"error,omitempty"`
	ConnAt    time.Time `json:"connected_at,omitempty"`
}

// MCPManager manages multiple MCPClient instances and their tool registrations.
type MCPManager struct {
	clients  map[string]*managedClient
	registry *core.Registry
	mu       sync.RWMutex
	config   *MCPManagerConfig
	// toolChangeHandler, when set, is invoked after every tools/listChanged
	// refresh so consumers (e.g. the skill catalog) can re-index on demand.
	toolChangeHandler func()
}

// SetToolChangeHandler registers a callback invoked after every
// tools/listChanged refresh. nil detaches it. Consumers (e.g. the skill
// catalog) use it to trigger incremental re-indexing on demand.
//
// Args:
//   - handler: the callback, or nil.
func (m *MCPManager) SetToolChangeHandler(handler func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.toolChangeHandler = handler
}

// notifyToolChange invokes the registered listChanged handler, if any.
// Caller must NOT hold the lock (the handler may call back into the manager).
func (m *MCPManager) notifyToolChange() {
	m.mu.RLock()
	handler := m.toolChangeHandler
	m.mu.RUnlock()
	if handler != nil {
		handler()
	}
}

// managedClient holds an MCPClient and its metadata.
type managedClient struct {
	client  *MCPClient
	config  MCPServerConfig
	connAt  time.Time
	lastErr error
	tools   []string // registered tool names
}

// NewMCPManager creates a new MCPManager.
// Args:
// config - manager configuration, may be nil for lazy initialization.
// registry - tool registry for registering MCP server tools, must not be nil.
// Returns:
// manager - created MCPManager instance.
// err - error if registry is nil.
func NewMCPManager(config *MCPManagerConfig, registry *core.Registry) (*MCPManager, error) {
	if registry == nil {
		return nil, errors.New("mcp: registry is required")
	}
	return &MCPManager{
		clients:  make(map[string]*managedClient),
		registry: registry,
		config:   config,
	}, nil
}

// Start connects to all enabled auto_start servers.
func (m *MCPManager) Start(ctx context.Context) error {
	if m.config == nil {
		return nil
	}

	for _, sc := range m.config.Servers {
		if !sc.Enabled || !sc.AutoStart {
			continue
		}

		if err := m.ConnectServer(ctx, sc.Name); err != nil {
			log.Error("mcp: failed to connect to server", "server", sc.Name, "error", err)
		}
	}

	return nil
}

// Stop disconnects all servers and unregisters their tools.
//
// Locking: clients are CLOSED outside m.mu. MCPClient.Close waits for the
// receive loop's notification goroutines, whose onChange callback re-enters
// RefreshTools → m.mu.Lock(); closing under the lock deadlocks shutdown
// whenever a tools/listChanged notification is in flight.
func (m *MCPManager) Stop(_ context.Context) error {
	m.mu.Lock()
	closing := make([]*managedClient, 0, len(m.clients))
	for name, mc := range m.clients {
		m.unregisterTools(mc)
		closing = append(closing, mc)
		delete(m.clients, name)
	}
	m.mu.Unlock()

	for _, mc := range closing {
		if err := mc.client.Close(); err != nil {
			log.Warn("mcp: failed to close client", "server", mc.config.Name, "error", err)
		}
	}

	return nil
}

// ConnectServer connects to a named MCP server.
// Args:
// ctx - context for cancellation and timeout.
// name - server name as defined in configuration, must not be empty.
// Returns:
// error - connection, transport, or tool registration error.
func (m *MCPManager) ConnectServer(ctx context.Context, name string) error {
	if name == "" {
		return errors.New("server name cannot be empty")
	}

	m.mu.RLock()
	sc := m.findServerConfig(name)
	m.mu.RUnlock()

	if sc == nil {
		return fmt.Errorf("server %q not found in config", name)
	}

	transport, err := NewTransportFromConfig(sc.Transport)
	if err != nil {
		return fmt.Errorf("create transport: %w", err)
	}

	return m.connectWithTransport(ctx, name, sc, transport)
}

// connectWithTransport finishes a server connect with an ALREADY-CREATED
// transport. It is the injection seam for tests (mock transports) and keeps
// the config→transport construction separate from registration/lifecycle.
//
// Args:
//   - ctx: connection + tool-listing context.
//   - name: server name as defined in configuration.
//   - sc: the resolved server config (must be non-nil).
//   - transport: the transport to bind the new client to.
func (m *MCPManager) connectWithTransport(ctx context.Context, name string, sc *MCPServerConfig, transport Transport) error {
	onChange := func() {
		// Use a fresh background-derived context instead of the caller's ctx,
		// which may be a short-lived request context that gets cancelled before
		// the refresh completes. Derive the timeout from the server config.
		refreshTimeout := sc.Timeout
		if refreshTimeout == 0 {
			refreshTimeout = 30 * time.Second
		}
		refreshCtx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
		defer cancel()
		if err := m.RefreshTools(refreshCtx, name); err != nil {
			log.Warn("mcp: failed to refresh tools", "server", name, "error", err)
		}
		m.notifyToolChange()
	}

	client := NewMCPClient(MCPClientConfig{
		ServerName: sc.Name,
		Transport:  sc.Transport,
		Timeout:    sc.Timeout,
		OnChange:   onChange,
	})

	if err := client.Connect(ctx, transport); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	mc := &managedClient{
		client: client,
		config: *sc,
		connAt: time.Now(),
	}

	// Register tools from this server.
	toolNames, err := m.registerTools(mc)
	if err != nil {
		_ = client.Close()
		return fmt.Errorf("register tools: %w", err)
	}
	mc.tools = toolNames

	m.mu.Lock()
	stale := m.clients[name]
	m.clients[name] = mc
	m.mu.Unlock()

	// Close the PREVIOUS connection (if any) outside the manager lock. Without
	// this, an overlapping Start/ApplyConfig re-connect orphans the old
	// managedClient: its stdio subprocess is never killed and its receive
	// loop leaks. Closing after the swap keeps concurrent readers on a live
	// client for the whole window.
	if stale != nil {
		if err := stale.client.Close(); err != nil {
			log.Warn("mcp: failed to close replaced client", "server", name, "error", err)
		}
	}

	return nil
}

// DisconnectServer disconnects from a named server.
//
// Locking: the client is CLOSED outside m.mu for the same reason as Stop —
// Close waits on notification goroutines that may be blocked acquiring m.mu
// inside RefreshTools.
func (m *MCPManager) DisconnectServer(_ context.Context, name string) error {
	m.mu.Lock()
	mc, ok := m.clients[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("server %q not connected", name)
	}
	m.unregisterTools(mc)
	delete(m.clients, name)
	m.mu.Unlock()

	if err := mc.client.Close(); err != nil {
		log.Warn("mcp: failed to close client", "server", name, "error", err)
	}

	return nil
}

// RefreshTools re-discovers and re-registers tools for a server.
//
// Failure atomicity: if re-discovery fails after the old tools were
// unregistered, the previous tool set is re-registered (best effort) so a
// transient ListTools blip during a hot-reload does not leave the server with
// zero tools in the LLM-facing registry.
func (m *MCPManager) RefreshTools(ctx context.Context, serverName string) error {
	m.mu.Lock()
	mc, ok := m.clients[serverName]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("server %q not connected", serverName)
	}

	// Unregister old tools.
	m.unregisterTools(mc)
	m.mu.Unlock()

	// Re-discover tools.
	if _, err := mc.client.ListTools(ctx); err != nil {
		// Restore the previous registration from the client's (still valid)
		// cached tool definitions so the registry keeps serving the old set.
		m.mu.Lock()
		if _, rerr := m.registerTools(mc); rerr != nil {
			log.Warn("mcp: failed to restore previous tools after failed refresh",
				"server", serverName, "refresh_error", err, "restore_error", rerr)
		}
		m.mu.Unlock()
		return fmt.Errorf("list tools: %w", err)
	}

	// Re-register.
	m.mu.Lock()
	defer m.mu.Unlock()

	toolNames, err := m.registerTools(mc)
	if err != nil {
		return fmt.Errorf("register tools: %w", err)
	}
	mc.tools = toolNames

	return nil
}

// ListServers returns the status of all configured servers.
func (m *MCPManager) ListServers() []MCPServerStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Report every configured, enabled server — including those not yet
	// connected — so a Required-server outage surfaces as Connected=false
	// (Degraded) instead of being invisible (Stage 5 observability).
	seen := make(map[string]bool, len(m.clients))
	statuses := make([]MCPServerStatus, 0, len(m.clients))
	for _, mc := range m.clients {
		status := MCPServerStatus{
			Name:      mc.config.Name,
			Connected: mc.client.IsConnected(),
			ToolCount: mc.client.ToolCount(),
			ConnAt:    mc.connAt,
		}
		if mc.lastErr != nil {
			status.Error = mc.lastErr.Error()
		}
		// The client does not expose a server version string; leaving Version
		// empty avoids filling the status field with a misleading state value.
		seen[mc.config.Name] = true
		statuses = append(statuses, status)
	}

	if m.config != nil {
		for _, sc := range m.config.Servers {
			if !sc.Enabled || seen[sc.Name] {
				continue
			}
			// Configured but never connected: report the disconnected state.
			statuses = append(statuses, MCPServerStatus{
				Name:      sc.Name,
				Connected: false,
				Error:     "not connected",
			})
		}
	}

	return statuses
}

// GetClient returns the MCPClient for a named server.
func (m *MCPManager) GetClient(serverName string) (*MCPClient, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	mc, ok := m.clients[serverName]
	if !ok {
		return nil, false
	}
	return mc.client, true
}

// RegisteredTools returns the tools currently registered by connected MCP
// servers. It is the read-side counterpart of the internal registry, letting
// callers bridge MCP tools into another registry without holding a second
// manager instance (and thus without duplicating server connections).
func (m *MCPManager) RegisteredTools() []core.Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := m.registry.List()
	tools := make([]core.Tool, 0, len(names))
	for _, name := range names {
		if t, ok := m.registry.Get(name); ok {
			tools = append(tools, t)
		}
	}
	return tools
}

// registerTools creates MCPTool instances and registers them in the registry.
func (m *MCPManager) registerTools(mc *managedClient) ([]string, error) {
	if mc.client == nil {
		return nil, fmt.Errorf("mcp: client is nil for server %s", mc.config.Name)
	}

	tools := mc.client.ToolCount()
	if tools == 0 {
		return []string{}, nil
	}

	// Get all tool definitions.
	mc.client.mu.RLock()
	defs := make([]*MCPToolDef, 0, len(mc.client.tools))
	for _, def := range mc.client.tools {
		defs = append(defs, def)
	}
	mc.client.mu.RUnlock()

	names := make([]string, 0, len(defs))
	for _, def := range defs {
		mcpTool, err := NewMCPTool(mc.client, def)
		if err != nil {
			return nil, fmt.Errorf("create mcp tool %s: %w", def.Name, err)
		}

		fullName := mcpTool.Name()
		if err := m.registry.Register(mcpTool); err != nil {
			// Conflict detected — builtin tool wins, skip the MCP tool. Log
			// the tool name and the winning source so the conflict is
			// observable. The conflict resolution policy (builtin wins) is
			// unchanged.
			log.Warn("mcp: tool name conflict, keeping builtin tool and skipping MCP tool",
				"tool", fullName,
				"server", mc.config.Name,
				"winner", "builtin",
				"error", err,
			)
			continue
		}

		names = append(names, fullName)
	}

	return names, nil
}

// unregisterTools removes all tools for a managed client from the registry.
func (m *MCPManager) unregisterTools(mc *managedClient) {
	for _, name := range mc.tools {
		if err := m.registry.Unregister(name); err != nil {
			log.Warn("mcp: failed to unregister tool", "tool", name, "error", err)
		}
	}
	mc.tools = nil
}

// findServerConfig returns the config for a named server.
func (m *MCPManager) findServerConfig(name string) *MCPServerConfig {
	if m.config == nil {
		return nil
	}
	for i := range m.config.Servers {
		if m.config.Servers[i].Name == name {
			return &m.config.Servers[i]
		}
	}
	return nil
}

// ApplyConfig diffs the new config against the current one and applies changes:
//   - Connects newly added servers
//   - Disconnects removed servers
//   - Reconnects servers whose config changed
//
// It returns the list of changes applied.
func (m *MCPManager) ApplyConfig(ctx context.Context, newCfg *MCPManagerConfig) []string {
	var changes []string

	m.mu.Lock()
	m.config = newCfg
	oldClients := make(map[string]*managedClient)
	for k, v := range m.clients {
		oldClients[k] = v
	}
	m.mu.Unlock()

	// Build index of new servers.
	newServers := make(map[string]MCPServerConfig)
	if newCfg != nil {
		for _, s := range newCfg.Servers {
			newServers[s.Name] = s
		}
	}

	// 1. Disconnect servers that were removed or disabled.
	for name, mc := range oldClients {
		newSrv, exists := newServers[name]
		if !exists || (!newSrv.Enabled && mc.config.Enabled) {
			if err := m.DisconnectServer(ctx, name); err != nil {
				log.Warn("mcp: hot-reload disconnect", "server", name, "error", err)
			}
			changes = append(changes, fmt.Sprintf("disconnected: %s", name))
			continue
		}
		// 2. Reconnect if server config changed (e.g., transport change).
		if hasConfigChanged(&mc.config, &newSrv) {
			if err := m.DisconnectServer(ctx, name); err != nil {
				log.Warn("mcp: hot-reload reconnect disconnect", "server", name, "error", err)
			}
			if err := m.ConnectServer(ctx, name); err != nil {
				log.Warn("mcp: hot-reload reconnect connect", "server", name, "error", err)
			}
			changes = append(changes, fmt.Sprintf("reconnected: %s", name))
		}
	}

	// 3. Connect newly added servers.
	if newCfg != nil {
		for _, s := range newCfg.Servers {
			if _, exists := oldClients[s.Name]; !exists && s.Enabled {
				if err := m.ConnectServer(ctx, s.Name); err != nil {
					log.Warn("mcp: hot-reload connect", "server", s.Name, "error", err)
				} else {
					changes = append(changes, fmt.Sprintf("connected: %s", s.Name))
				}
			}
		}
	}

	return changes
}

// hasConfigChanged checks whether two server configs differ in a meaningful way.
func hasConfigChanged(a, b *MCPServerConfig) bool {
	if a.Transport.Type != b.Transport.Type {
		return true
	}
	switch a.Transport.Type {
	case TransportTypeStdio:
		if a.Transport.Stdio != nil && b.Transport.Stdio != nil {
			return a.Transport.Stdio.Command != b.Transport.Stdio.Command ||
				!stringSliceEqual(a.Transport.Stdio.Args, b.Transport.Stdio.Args) ||
				!stringMapEqual(a.Transport.Stdio.Env, b.Transport.Stdio.Env) ||
				a.Transport.Stdio.WorkDir != b.Transport.Stdio.WorkDir
		}
		return (a.Transport.Stdio == nil) != (b.Transport.Stdio == nil)
	case TransportTypeSSE:
		if a.Transport.SSE != nil && b.Transport.SSE != nil {
			return a.Transport.SSE.URL != b.Transport.SSE.URL
		}
		return (a.Transport.SSE == nil) != (b.Transport.SSE == nil)
	}
	return false
}

// stringSliceEqual compares two string slices.
func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// stringMapEqual compares two string maps for equality (nil == empty).
func stringMapEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		if bv, ok := b[k]; !ok || av != bv {
			return false
		}
	}
	return true
}
