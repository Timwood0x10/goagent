package main

import (
	"context"
	"fmt"
	"time"

	"github.com/Timwood0x10/ares/internal/agentfabric"
	"github.com/Timwood0x10/ares/internal/agents/base"
	"github.com/Timwood0x10/ares/internal/agents/sub"
	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/core/models"
	llm "github.com/Timwood0x10/ares/internal/llm"
)

// cognitionTaskExecutor adapts an agentfabric Cognition to sub.TaskExecutor
// (M4-D: the only TaskExecutor implementation left; the ReAct tool loop is
// deleted). ExecuteStep delegates one quantum field-for-field — subAgent
// picks it up structurally via its stepExecutor check. Execute runs a single
// quantum and translates the outcome; completion is driven by the scheduler
// draining quanta, never by looping here. RegisterFallback is a no-op (no
// fallback loop exists anymore). A nil body fails loud: identity-only agents
// (peer registry shells) must never be driven.
type cognitionTaskExecutor struct {
	id  string
	cog agentfabric.Cognition
}

// Execute runs a single quantum through the wrapped cognition.
func (e *cognitionTaskExecutor) Execute(ctx context.Context, task *models.Task) (*models.TaskResult, error) {
	if e.cog == nil {
		return nil, fmt.Errorf("peer mode: executor %q has no execution body (identity-only agent must not be driven)", e.id)
	}
	out, err := e.cog.ExecuteStep(ctx, task)
	if err != nil {
		return nil, err
	}
	if out != nil && out.Done && out.Result != nil {
		return out.Result, nil
	}
	// Single-quantum pass-through: not done means the scheduler resumes it.
	res := models.NewTaskResult(task.TaskID, task.AgentType)
	res.Success = false
	res.Reason = "quantum yielded; resume via scheduler"
	return res, nil
}

// RegisterFallback implements sub.TaskExecutor. No-op: no fallback loop.
func (e *cognitionTaskExecutor) RegisterFallback(models.AgentType, sub.FallbackHandler) {
}

// ExecuteStep implements the quantum path for subAgent's structural
// stepExecutor check.
func (e *cognitionTaskExecutor) ExecuteStep(ctx context.Context, task *models.Task) (*sub.StepOutcome, error) {
	if e.cog == nil {
		return nil, fmt.Errorf("peer mode: executor %q has no execution body (identity-only agent must not be driven)", e.id)
	}
	out, err := e.cog.ExecuteStep(ctx, task)
	if err != nil {
		return nil, err
	}
	return &sub.StepOutcome{Done: out.Done, Checkpoint: out.Checkpoint, Result: out.Result}, nil
}

// createPeerSubAgents builds the sub.Agent identities for the C1 flat peer
// population (cfg.Agents.Peers). M4-D: these are identity shells for the
// peer registry/IPC — execution flows through fabric-spawned router
// cognitions, so each shell carries a body-less adapter that fails loud if
// ever driven (it never is: the static scheduler pool is gone and one-shot
// Execute has no production callers).
//
// C1 convergence (review P1): no heartbeat monitor, no message queue —
// the fabric owns scheduling and lifecycle.
func createPeerSubAgents(
	peers []ares_config.PeerAgentConfig,
	store ares_events.EventStore,
) []sub.Agent {
	agents := make([]sub.Agent, 0, len(peers))
	for _, p := range peers {
		typ := ""
		if len(p.Capabilities) > 0 {
			typ = p.Capabilities[0]
		}
		handler := sub.NewMessageHandler(p.ID)
		agent := sub.New(
			p.ID,
			models.AgentType(typ),
			&cognitionTaskExecutor{id: p.ID},
			handler,
			nil, // message queue: the fabric owns scheduling; no AHP queue loop
			nil, // heartbeat monitor: no Process/Launch lifecycle in peer mode
			&sub.SubAgentConfig{
				Config: base.Config{
					ID:   p.ID,
					Type: models.AgentType(typ),
				},
				EnableTools: true,
			},
			sub.WithEventStore(store),
		)
		agents = append(agents, agent)
	}
	return agents
}

// createChatClient creates a FailoverClient from the LLM config for Chat API support.
func createChatClient(cfg *ares_config.Config) (sub.ChatClient, error) {
	configs := make([]*llm.Config, 0, 1+len(cfg.LLM.Fallbacks))
	configs = append(configs, &llm.Config{
		Provider:  cfg.LLM.Provider,
		APIKey:    cfg.LLM.APIKey,
		BaseURL:   cfg.LLM.BaseURL,
		Model:     cfg.LLM.Model,
		Timeout:   cfg.LLM.Timeout,
		MaxTokens: cfg.LLM.MaxTokens,
	})
	for _, fb := range cfg.LLM.Fallbacks {
		provider := fb.Provider
		if provider == "" {
			provider = "openai"
		}
		configs = append(configs, &llm.Config{
			Provider:  provider,
			APIKey:    fb.APIKey,
			BaseURL:   fb.BaseURL,
			Model:     fb.Model,
			Timeout:   fb.Timeout,
			MaxTokens: fb.MaxTokens,
		})
	}

	timeout := time.Duration(cfg.LLM.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	rate := cfg.LLM.ScorerAPIRate
	burst := cfg.LLM.ScorerAPIBurst
	return llm.NewFailoverClient(configs, timeout, rate, burst)
}
