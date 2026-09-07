// Package llm provides LLM client functionality for various providers.
package llm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/api/core"
	"github.com/Timwood0x10/ares/internal/ares_ratelimit"
	"github.com/Timwood0x10/ares/internal/runtime/observability"
)

// Default cooldown duration for rate-limited providers.
const defaultCooldownDuration = 60 * time.Second

// FailoverClient chains multiple LLM clients with automatic failover and
// rate-limit-aware cooldown. Clients are tried in order (primary first, then
// fallbacks). When a provider returns HTTP 429, it is marked as cooled down
// and skipped for a configurable duration.
//
// Usage:
//
//	client, _ := llm.NewFailoverClient(configs, 30*time.Second, 10, 20)
//	resp, err := client.Generate(ctx, prompt)
//
// Integrates with config.LLMConfig.Fallbacks for declarative setup.
type FailoverClient struct {
	clients          []*Client
	timeout          time.Duration
	cooldownDuration time.Duration
	mu               sync.RWMutex
	cooldowns        map[string]time.Time // provider+model → cooldown expiry
}

// FailoverOption configures a FailoverClient.
type FailoverOption func(*FailoverClient)

// WithCooldownDuration sets how long a rate-limited provider is skipped.
func WithCooldownDuration(d time.Duration) FailoverOption {
	return func(fc *FailoverClient) {
		fc.cooldownDuration = d
	}
}

// NewFailoverClient creates a FailoverClient from a list of LLM configs.
// The first config is the primary client (gets rate limiting); subsequent
// configs are fallbacks tried in order on failure.
//
// Args:
//
//	configs  - list of LLM configs: configs[0] = primary, configs[1:] = fallbacks.
//	timeout  - per-call timeout applied to each client.
//	rate     - token bucket rate (req/s) for the primary client; 0 = no limiting.
//	burst    - token bucket burst size for the primary client.
//	opts     - optional FailoverOption functions.
//
// Returns an error if no clients could be created.
func NewFailoverClient(configs []*Config, timeout time.Duration, rate float64, burst int, opts ...FailoverOption) (*FailoverClient, error) {
	if len(configs) == 0 {
		return nil, errors.New("at least one LLM config is required")
	}

	clients := make([]*Client, 0, len(configs))

	for i, cfg := range configs {
		var clientOpts []Option

		// The failover layer owns provider-level failover (cooldown + switch).
		// Disable per-call retry/circuit-breaking on the underlying clients so a
		// failing provider is failed over promptly instead of being retried
		// internally up to MaxAttempts.
		clientOpts = append(clientOpts,
			WithRetryPolicy(RetryPolicy{MaxAttempts: 1}),
			WithCircuitBreaker(nil),
		)

		// Rate limiting only on the primary client.
		if i == 0 && rate > 0 {
			limiter := ares_ratelimit.NewTokenBucketLimiter(&ares_ratelimit.LimiterConfig{
				Rate:  rate,
				Burst: burst,
			})
			clientOpts = append(clientOpts, WithRateLimiter(limiter))
		}

		client, err := NewClient(cfg, clientOpts...)
		if err != nil {
			if i == 0 {
				return nil, fmt.Errorf("create primary LLM client: %w", err)
			}
			log.Warn("FailoverClient: failed to create fallback client, skipping",
				"index", i,
				"model", cfg.Model,
				"provider", cfg.Provider,
				"error", err,
			)
			continue
		}
		clients = append(clients, client)
	}

	if len(clients) == 0 {
		return nil, errors.New("no LLM clients could be created")
	}

	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	fc := &FailoverClient{
		clients:          clients,
		timeout:          timeout,
		cooldownDuration: defaultCooldownDuration,
		cooldowns:        make(map[string]time.Time),
	}
	for _, opt := range opts {
		opt(fc)
	}

	log.Info("FailoverClient created",
		"total_clients", len(clients),
		"fallback_count", len(clients)-1,
		"primary_model", clients[0].GetModel(),
		"timeout", timeout,
		"cooldown", fc.cooldownDuration,
	)

	return fc, nil
}

// D13: NewFailoverScorer removed (deprecated alias, 0 production calls).

// clientKey returns a unique key for cooldown tracking.
func (fc *FailoverClient) clientKey(c *Client) string {
	return c.GetProvider() + "/" + c.GetModel()
}

// isCooledDown returns true if the client is in a rate-limit cooldown.
// Expired entries are cleaned up eagerly to prevent unbounded map growth.
func (fc *FailoverClient) isCooledDown(key string) bool {
	fc.mu.RLock()
	expiry, ok := fc.cooldowns[key]
	fc.mu.RUnlock()
	if !ok {
		return false
	}
	if time.Now().Before(expiry) {
		return true
	}
	// Cooldown expired; clean up.
	fc.mu.Lock()
	delete(fc.cooldowns, key)
	fc.mu.Unlock()
	return false
}

// markCooldown records a cooldown of the given duration for the client key.
// The duration comes from cooldownForError so rate-limited providers get the
// full cooldown while transient failures get a shorter one.
func (fc *FailoverClient) markCooldown(key string, d time.Duration) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.cooldowns[key] = time.Now().Add(d)
}

// clearCooldown removes a cooldown on success.
func (fc *FailoverClient) clearCooldown(key string) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	delete(fc.cooldowns, key)
}

// cooldownForError returns the cooldown duration based on error type.
// Rate-limited providers get the full configured cooldown; other errors get
// a shorter cooldown (1/3 of configured, clamped to at least 100ms and at
// most the full cooldown) so they are retried sooner but not on every call.
func (fc *FailoverClient) cooldownForError(err error) time.Duration {
	if isRateLimitError(err) {
		return fc.cooldownDuration
	}
	short := fc.cooldownDuration / 3
	if short < 100*time.Millisecond {
		short = 100 * time.Millisecond
	}
	if short > fc.cooldownDuration {
		short = fc.cooldownDuration
	}
	return short
}

// Generate tries each LLM client in order and returns the first successful
// response. All errors trigger cooldown so the next call skips the provider
// instead of waiting for the same timeout/429 again.
func (fc *FailoverClient) Generate(ctx context.Context, prompt string) (string, error) {
	var lastErr error

	for _, client := range fc.clients {
		key := fc.clientKey(client)

		if fc.isCooledDown(key) {
			log.Debug("FailoverClient: skipping cooled-down provider",
				"provider", client.GetProvider(),
				"model", client.GetModel(),
			)
			continue
		}

		cctx, cancel := context.WithTimeout(ctx, fc.timeout)
		resp, err := client.Generate(cctx, prompt)
		cancel()

		if err == nil {
			fc.clearCooldown(key)
			return resp, nil
		}

		lastErr = err
		cd := fc.cooldownForError(err)
		fc.markCooldown(key, cd)

		if isRateLimitError(err) {
			log.Warn("FailoverClient: rate limited, cooling down",
				"provider", client.GetProvider(),
				"model", client.GetModel(),
				"cooldown", cd,
			)
		} else {
			log.Warn("FailoverClient: provider failed, cooling down",
				"provider", client.GetProvider(),
				"model", client.GetModel(),
				"cooldown", cd,
				"error", err,
			)
		}
	}

	if lastErr == nil {
		return "", fmt.Errorf("FailoverClient: no provider available (all %d cooled down)", len(fc.clients))
	}
	return "", fmt.Errorf("FailoverClient: all %d clients failed; last error: %w",
		len(fc.clients), lastErr)
}

// GenerateStream tries each LLM client in order and returns the first
// successful stream. Failed providers are cooled down with the same policy
// as Generate (rate-limit = full cooldown, other errors = shorter cooldown).
//
// The stream itself runs under the caller's context (a streaming-specific
// deadline), NOT the request-level fc.timeout: a fixed 30s request timeout
// would cut long outputs off mid-stream (H8). fc.timeout is only used to
// bound the wait for the FIRST chunk, which covers the connection/handshake
// phase so a silent provider still fails over.
//
// NOTE: Failover only covers stream creation (HTTP handshake). Once a stream
// is established and chunks are being delivered, mid-stream errors (e.g.,
// connection drops) are reported to the caller via StreamChunk.Err and are
// NOT handled by the failover layer. Callers must handle StreamChunk.Err
// themselves.
func (fc *FailoverClient) GenerateStream(ctx context.Context, prompt string) (<-chan StreamChunk, error) {
	var lastErr error

	for _, client := range fc.clients {
		key := fc.clientKey(client)

		if fc.isCooledDown(key) {
			log.Debug("FailoverClient: skipping cooled-down provider (stream)",
				"provider", client.GetProvider(),
				"model", client.GetModel(),
			)
			continue
		}

		// N6: per-attempt context so a silent provider can be cancelled and
		// failed over without tearing down the caller's overall context.
		attemptCtx, attemptCancel := context.WithCancel(ctx)
		ch, err := client.GenerateStream(attemptCtx, prompt)
		if err != nil {
			attemptCancel()
			lastErr = err
			cd := fc.cooldownForError(err)
			fc.markCooldown(key, cd)

			if isRateLimitError(err) {
				log.Warn("FailoverClient: rate limited on stream, cooling down",
					"provider", client.GetProvider(),
					"model", client.GetModel(),
					"cooldown", cd,
				)
			} else {
				log.Warn("FailoverClient: provider failed on stream, cooling down",
					"provider", client.GetProvider(),
					"model", client.GetModel(),
					"cooldown", cd,
					"error", err,
				)
			}
			continue
		}

		// The first chunk must arrive within fc.timeout (handshake bound).
		// A timeout here is a FAILED attempt, not a success: the wrapped
		// stream is cancelled and the next provider is tried, so a silent
		// provider cannot surface as an empty successful stream (N6: stream
		// timeout false-success).
		// Use a stoppable timer, not time.After: the time.After timer goroutine
		// would otherwise linger until fc.timeout after a successful handshake,
		// leaking a timer per stream attempt (review finding).
		timer := time.NewTimer(fc.timeout)
		var first StreamChunk
		select {
		case chunk, ok := <-ch:
			// Stop and drain the timer so it is released on the success path.
			if !timer.Stop() {
				<-timer.C
			}
			if !ok {
				attemptCancel()
				lastErr = fmt.Errorf("stream from %s closed before first chunk", client.GetProvider())
				fc.markCooldown(key, fc.cooldownForError(lastErr))
				log.Warn("FailoverClient: provider closed stream before first chunk, cooling down",
					"provider", client.GetProvider(),
					"model", client.GetModel(),
				)
				continue
			}
			first = chunk
		case <-timer.C:
			attemptCancel()
			lastErr = fmt.Errorf("stream from %s: no first chunk within %s", client.GetProvider(), fc.timeout)
			fc.markCooldown(key, fc.cooldownForError(lastErr))
			log.Warn("FailoverClient: provider silent on stream (handshake timeout), cooling down and failing over",
				"provider", client.GetProvider(),
				"model", client.GetModel(),
				"timeout", fc.timeout,
			)
			continue
		case <-ctx.Done():
			attemptCancel()
			timer.Stop()
			return nil, ctx.Err()
		}

		// Success: wrap the channel. The first chunk is forwarded, then the
		// stream runs until the caller's context is done or the provider
		// finishes, so long outputs are not cut off by a fixed timeout.
		fc.clearCooldown(key)
		wrappedCh := make(chan StreamChunk, defaultStreamBuffer)
		go func() {
			defer close(wrappedCh)
			defer attemptCancel()
			select {
			case wrappedCh <- first:
			case <-ctx.Done():
				return
			}
			for chunk := range ch {
				select {
				case wrappedCh <- chunk:
				case <-ctx.Done():
					return
				}
			}
		}()
		return wrappedCh, nil
	}

	if lastErr == nil {
		return nil, fmt.Errorf("FailoverClient: no provider available (all %d cooled down)", len(fc.clients))
	}
	return nil, fmt.Errorf("FailoverClient: all %d stream clients failed; last error: %w",
		len(fc.clients), lastErr)
}

// Chat tries each LLM client in order and returns the first successful chat
// response with tool support. Failed providers are cooled down with the same
// policy as Generate. Per-call parameter overrides are forwarded
// to each underlying client unchanged.
// Args:
//
//	ctx - operation context.
//	messages - conversation messages.
//	tools - available tools for function calling.
//	params - optional per-call parameter overrides.
//
// Returns:
//
//	*core.GenerateResponse - the chat response including optional tool_calls.
//	error - all clients failed or no provider available.
func (fc *FailoverClient) Chat(ctx context.Context, messages []*core.LLMMessage, tools []core.Tool, params map[string]any) (*core.GenerateResponse, error) {
	var lastErr error

	for _, client := range fc.clients {
		key := fc.clientKey(client)

		if fc.isCooledDown(key) {
			log.Debug("FailoverClient: skipping cooled-down provider (chat)",
				"provider", client.GetProvider(),
				"model", client.GetModel(),
			)
			continue
		}

		cctx, cancel := context.WithTimeout(ctx, fc.timeout)
		resp, err := client.Chat(cctx, messages, tools, params)
		cancel()

		if err == nil {
			fc.clearCooldown(key)
			return resp, nil
		}

		lastErr = err
		cd := fc.cooldownForError(err)
		fc.markCooldown(key, cd)

		log.Warn("FailoverClient: provider failed on chat, cooling down",
			"provider", client.GetProvider(),
			"model", client.GetModel(),
			"cooldown", cd,
			"error", err,
		)
	}

	if lastErr == nil {
		return nil, errors.New("FailoverClient: no provider available for chat")
	}
	return nil, fmt.Errorf("FailoverClient: all chat clients failed; last error: %w",
		lastErr)
}

// IsEnabled returns true if the primary client is enabled.
func (fc *FailoverClient) IsEnabled() bool {
	if len(fc.clients) == 0 {
		return false
	}
	return fc.clients[0].IsEnabled()
}

// GetProvider returns the primary client's provider.
func (fc *FailoverClient) GetProvider() string {
	if len(fc.clients) == 0 {
		return ""
	}
	return fc.clients[0].GetProvider()
}

// GetModel returns the primary client's model.
func (fc *FailoverClient) GetModel() string {
	if len(fc.clients) == 0 {
		return ""
	}
	return fc.clients[0].GetModel()
}

// SetTracer sets the tracer on all underlying clients.
func (fc *FailoverClient) SetTracer(t observability.Tracer) {
	for _, c := range fc.clients {
		c.SetTracer(t)
	}
}

// Close closes all underlying clients.
func (fc *FailoverClient) Close() {
	for _, c := range fc.clients {
		c.Close()
	}
}

// Clients returns the underlying LLM clients (primary first, then fallbacks).
func (fc *FailoverClient) Clients() []*Client {
	result := make([]*Client, len(fc.clients))
	copy(result, fc.clients)
	return result
}

// Timeout returns the per-call timeout.
func (fc *FailoverClient) Timeout() time.Duration {
	return fc.timeout
}

// ActiveProviders returns the names of providers not currently cooled down.
func (fc *FailoverClient) ActiveProviders() []string {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	var active []string
	now := time.Now()
	for _, c := range fc.clients {
		key := fc.clientKey(c)
		if expiry, ok := fc.cooldowns[key]; !ok || now.After(expiry) {
			active = append(active, c.GetProvider()+":"+c.GetModel())
		}
	}
	return active
}

// D13: FailoverScorer deprecated alias removed (use FailoverClient directly).

// Ensure FailoverClient satisfies the common Generate and Chat interfaces.
var _ interface {
	Generate(ctx context.Context, prompt string) (string, error)
	GenerateStream(ctx context.Context, prompt string) (<-chan StreamChunk, error)
	Chat(ctx context.Context, messages []*core.LLMMessage, tools []core.Tool, params map[string]any) (*core.GenerateResponse, error)
	IsEnabled() bool
	GetProvider() string
	GetModel() string
	Close()
} = (*FailoverClient)(nil)
