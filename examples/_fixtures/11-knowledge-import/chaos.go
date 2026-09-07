// This file provides Arena-style chaos injection and Resurrection-style agent
// recovery for the knowledge base example. It wraps the SDK Runtime with
// fault-injection and health-monitoring capabilities.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/sdk"
)

// ChaosConfig controls fault injection behaviour.
type ChaosConfig struct {
	// Enabled enables fault injection.
	Enabled bool
	// InjectFailRate is the probability [0,1] that a tool call will fail.
	InjectFailRate float64
	// InjectLatency simulates slow tool calls (e.g. embedding timeout).
	InjectLatency time.Duration
	// KillAgentAfter kills the agent after N tool calls (0 = disabled).
	KillAgentAfter int
}

// DefaultChaosConfig returns a disabled chaos config.
func DefaultChaosConfig() ChaosConfig {
	return ChaosConfig{Enabled: false}
}

// ChaosEnabled checks whether any fault injection is active.
func (c ChaosConfig) ChaosEnabled() bool {
	return c.Enabled && (c.InjectFailRate > 0 || c.InjectLatency > 0 || c.KillAgentAfter > 0)
}

// ── Tool-level fault injector ─────────────────────────────────────────────

// ToolWrapper wraps the SDK tool execution with fault injection.
// It implements a subset of what ares_arena.Injector does at the tool layer.
type ToolWrapper struct {
	cfg       ChaosConfig
	callCount map[string]int // per-agent tool call counter
	mu        sync.Mutex
}

// NewToolWrapper creates a tool wrapper with the given chaos config.
func NewToolWrapper(cfg ChaosConfig) *ToolWrapper {
	return &ToolWrapper{
		cfg:       cfg,
		callCount: make(map[string]int),
	}
}

// Wrap wraps a tool execution function with fault injection.
// agentID identifies which agent is making the call (e.g. "importer-3").
func (w *ToolWrapper) Wrap(agentID string, toolName string, fn func() (any, error)) (any, error) {
	if !w.cfg.ChaosEnabled() {
		return fn()
	}

	w.mu.Lock()
	w.callCount[agentID]++
	count := w.callCount[agentID]
	w.mu.Unlock()

	// Simulate latency.
	if w.cfg.InjectLatency > 0 {
		time.Sleep(w.cfg.InjectLatency)
	}

	// Kill agent after N calls.
	if w.cfg.KillAgentAfter > 0 && count >= w.cfg.KillAgentAfter {
		slog.Warn("🧨 arena: tool kill", "agent", agentID, "tool", toolName, "call", count)
		return nil, fmt.Errorf("arena: agent %s killed after %d tool calls", agentID, count)
	}

	// Random failure.
	if w.cfg.InjectFailRate > 0 && rand.Float64() < w.cfg.InjectFailRate {
		slog.Warn("🧨 arena: inject fault", "agent", agentID, "tool", toolName)
		return nil, fmt.Errorf("arena: injected fault in %s calling %s", agentID, toolName)
	}

	return fn()
}

// Reset clears the call counter for all agents.
func (w *ToolWrapper) Reset() {
	w.mu.Lock()
	w.callCount = make(map[string]int)
	w.mu.Unlock()
}

// ── Agent Supervisor (Resurrection-style) ──────────────────────────────────

// AgentSupervisor monitors sub-agents during Team execution and can restart
// failed agents. It implements a simplified version of the resurrection plugin.
type AgentSupervisor struct {
	cfg       ChaosConfig
	agents    map[string]*sdk.Agent
	healthy   map[string]bool
	failCount map[string]int
	mu        sync.Mutex
}

// NewAgentSupervisor creates a supervisor.
func NewAgentSupervisor(cfg ChaosConfig) *AgentSupervisor {
	return &AgentSupervisor{
		cfg:       cfg,
		agents:    make(map[string]*sdk.Agent),
		healthy:   make(map[string]bool),
		failCount: make(map[string]int),
	}
}

// RegisterAgent adds an agent to the supervision pool.
func (s *AgentSupervisor) RegisterAgent(id string, agent *sdk.Agent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agents[id] = agent
	s.healthy[id] = true
	s.failCount[id] = 0
	slog.Info("🔄 supervisor: registered", "agent", id)
}

// RecordFailure marks an agent as failed. Returns true if the agent should
// be considered dead (exceeded max retries).
func (s *AgentSupervisor) RecordFailure(agentID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failCount[agentID]++
	s.healthy[agentID] = false
	slog.Warn("🔄 supervisor: agent failed", "agent", agentID, "attempts", s.failCount[agentID])
	return s.failCount[agentID] >= 3
}

// RecordSuccess marks an agent as healthy.
func (s *AgentSupervisor) RecordSuccess(agentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.healthy[agentID] = true
}

// FailedAgents returns the list of agents that have failed.
func (s *AgentSupervisor) FailedAgents() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var failed []string
	for id, healthy := range s.healthy {
		if !healthy {
			failed = append(failed, id)
		}
	}
	return failed
}

// ── Arena Health Check (actual ares_arena.HTTP integration) ────────────────

// ArenaHTTPServer starts an HTTP server that exposes the arena health endpoint.
// This allows external chaos agents (e.g. Kubernetes Chaos Mesh) to inject
// faults via HTTP.
func ArenaHTTPServer(ctx context.Context, addr string) error {
	mux := &http.ServeMux{}
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"status":"ok","service":"knowledge-base"}`)
	})
	server := &http.Server{Addr: addr, Handler: mux}
	slog.Info("arena health endpoint", "addr", addr)
	go func() {
		<-ctx.Done()
		_ = server.Shutdown(context.Background())
	}()
	return server.ListenAndServe()
}
