package main

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_bootstrap"
	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/ares_ratelimit"
	"github.com/Timwood0x10/ares/internal/aresrecovery"
	"github.com/Timwood0x10/ares/internal/fabric/agent"
	"github.com/Timwood0x10/ares/internal/fabric/task"
	"github.com/Timwood0x10/ares/internal/introspect"
)

// chaosStopControl is the process-level kill switch for the live chaos loop
// (REVIEW #12 Phase 2 emergency stop). The HTTP handler POST /api/chaos/stop
// calls RequestStop; the loop polls Stopped and exits permanently. Shadow
// mode is unaffected — it never touches production agents.
type chaosStopControl struct {
	mu      sync.Mutex
	stopped bool
}

// liveChaosCtl is the singleton control for this process.
var liveChaosCtl = &chaosStopControl{}

// RequestStop trips the kill switch.
func (c *chaosStopControl) RequestStop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = true
}

// Stopped reports whether the kill switch has been tripped.
func (c *chaosStopControl) Stopped() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped
}

// shadowSandboxLoop runs a periodic shadow Sandbox verification: it constructs
// an independent scratch fabric, replays a canonical failure scenario
// (agent kill → lease expire → recovery), and logs the result. Production
// agents are never touched — the sandbox uses its own scratch fabrics.
//
// This closes REVIEW #12 Phase 1: the chaos subsystem defaults to shadow
// mode, which verifies recovery capability without impacting live agents.
//
// The status reporter (Phase 3) records the latest verification outcome so the
// introspection panel can surface shadow-sandbox health.
func shadowSandboxLoop(ctx context.Context, interval time.Duration, status *introspect.ChaosReporter) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Printf("serve: shadow sandbox loop started (interval=%s, production agents untouched)",
		interval.String())

	for {
		select {
		case <-ctx.Done():
			log.Printf("serve: shadow sandbox loop stopping (context cancelled)")
			return
		case <-ticker.C:
			runShadowSandbox(ctx, status)
		}
	}
}

// runShadowSandbox constructs a scratch fabric, runs a canonical
// agent-kill→recovery scenario, and logs the outcome. All scratch fabrics
// are local to this call and discarded after — production is never touched.
// The outcome (recovered_ready / errored) is recorded to the panel status.
func runShadowSandbox(ctx context.Context, status *introspect.ChaosReporter) {
	// Build scratch fabrics — completely independent from production.
	scratchTasks := taskfabric.NewFabric()
	scratchAgents := agentfabric.NewFabric()

	// Build a scratch Recovery wired to the scratch fabrics.
	scratchRecovery := aresrecovery.New(scratchTasks, scratchAgents, aresrecovery.DefaultRestartPolicy())

	// Build the Sandbox on the scratch fabrics.
	sandbox := aresrecovery.NewSandbox(scratchTasks, scratchAgents, scratchRecovery)

	// Scripted scenario: spawn agent → create task → agent acquires task →
	// agent is killed → lease expires → recovery runs.
	events := []aresrecovery.SandboxEvent{
		{Type: aresrecovery.SandboxEventAgentSpawn, AgentID: "shadow-agent-1"},
		{Type: aresrecovery.SandboxEventTaskCreate, TaskID: "shadow-task-1"},
		{Type: aresrecovery.SandboxEventTaskAcquire, TaskID: "shadow-task-1", AgentID: "shadow-agent-1"},
		{Type: aresrecovery.SandboxEventAgentKill, AgentID: "shadow-agent-1"},
		{Type: aresrecovery.SandboxEventLeaseExpire, TaskID: "shadow-task-1"},
		{Type: aresrecovery.SandboxEventRecoverAll},
	}

	outcomes, err := sandbox.Replay(ctx, events)
	if err != nil {
		log.Printf("serve: shadow sandbox replay failed: %v (recovery verification inconclusive)", err)
		if status != nil {
			status.RecordShadow(introspect.ShadowResult{
				LastRun:   time.Now(),
				Events:    len(events),
				Recovered: false,
				Errored:   true,
			})
		}
		return
	}

	// Check the final outcome — the recovery chain must have fully recovered
	// the requeued task (RecoverFromAgentDeath re-acquires it for a
	// replacement agent, so the final state is LEASED, not READY). The
	// reliable signal is the recovered-task count carried on the
	// recover.all outcome's Detail. A missing/empty outcome list is treated
	// as inconclusive.
	if len(outcomes) == 0 {
		log.Printf("serve: shadow sandbox replay produced no outcomes (recovery verification inconclusive)")
		if status != nil {
			status.RecordShadow(introspect.ShadowResult{
				LastRun:   time.Now(),
				Events:    len(events),
				Recovered: false,
				Errored:   true,
			})
		}
		return
	}
	last := outcomes[len(outcomes)-1]
	recovered, _ := last.Detail["recovered"].(int)
	recoveredOK := recovered > 0
	log.Printf("serve: shadow sandbox completed (events=%d, final_task_state=%s, recovered=%d)",
		len(outcomes), last.TaskState, recovered)
	if !recoveredOK {
		log.Printf("serve: shadow sandbox WARNING — recovery chain did not recover the requeued task; chain may be degraded")
	}
	if status != nil {
		status.RecordShadow(introspect.ShadowResult{
			LastRun:   time.Now(),
			Events:    len(outcomes),
			Recovered: recoveredOK,
			Errored:   false,
		})
	}
}

// wireChaos wires the chaos subsystem based on the kernel config. By default
// (chaos disabled or mode=shadow), only the shadow sandbox loop is started.
// When mode=live AND allow_live=true, a real Chaos harness is also constructed
// — but only for dedicated testing environments. Production deployments should
// never enable live mode.
//
// The shadow sandbox loop is attached to the provided context and runs as a
// managed background loop (K3: runBackground — panic-recovered, joined by the
// orchestrator/bootstrap on shutdown, never a bare `go`). It is best-effort:
// a panic in the sandbox is recovered and logged, never crashing the process.
//
// status (Phase 3) bridges the loops into the introspection panel; it may be
// nil when the panel is not wired — the loops then only log.
func wireChaos(ctx context.Context, comp *ares_bootstrap.Components, cfg *ares_config.Config, peerKernel *kernelHandle, gaActive func() bool, status *introspect.ChaosReporter) {
	if status != nil {
		if cfg.Kernel.Chaos.Enabled {
			status.SetConfig(true, effectiveChaosMode(cfg))
		} else {
			status.SetConfig(false, "off")
		}
	}
	if !cfg.Kernel.Chaos.Enabled {
		log.Printf("serve: chaos subsystem disabled (kernel.chaos.enabled=false)")
		return
	}

	mode := cfg.Kernel.Chaos.Mode
	if mode == "" {
		mode = "shadow"
	}

	startShadow := func() {
		interval := parseChaosInterval(cfg.Kernel.Chaos.Interval, 5*time.Minute)
		runBackground(ctx, comp, "chaos-shadow", func(loopCtx context.Context) error {
			shadowSandboxLoop(loopCtx, interval, status)
			return nil
		})
	}

	switch mode {
	case "shadow":
		startShadow()

	case "live":
		if !cfg.Kernel.Chaos.AllowLive {
			log.Printf("serve: chaos mode=live but allow_live=false — falling back to shadow mode")
			startShadow()
			return
		}
		// Live chaos is dangerous: it kills real production agents.
		// Only construct the Chaos harness when explicitly confirmed AND a
		// non-empty target whitelist is configured (#12 Phase 2): an empty
		// eligible_capabilities list must disable injection entirely rather
		// than default to "everything is a target".
		if len(cfg.Kernel.Chaos.EligibleCapabilities) == 0 {
			log.Printf("serve: live chaos requested but eligible_capabilities is empty — refusing to arm (falling back to shadow)")
			startShadow()
			return
		}
		if peerKernel != nil && peerKernel.agents != nil && peerKernel.recovery != nil {
			if cfg.Kernel.Chaos.StopToken == "" {
				log.Printf("serve: live chaos requested but stop_token is empty — refusing to arm without an emergency-stop credential")
				startShadow()
				return
			}
			chaos := aresrecovery.NewChaos(peerKernel.agents, peerKernel.recovery)
			interval := parseChaosInterval(cfg.Kernel.Chaos.Interval, 5*time.Minute)
			runBackground(ctx, comp, "chaos-live", func(loopCtx context.Context) error {
				liveChaosLoop(loopCtx, chaos, peerKernel.agents, interval, cfg.Kernel.Chaos, gaActive, status)
				return nil
			})
			log.Printf("serve: LIVE chaos mode enabled — agents WILL be killed (interval=%s, rate=%d/min enforced, whitelist=%v)",
				interval.String(), cfg.Kernel.Chaos.RatePerMin, cfg.Kernel.Chaos.EligibleCapabilities)
		} else {
			log.Printf("serve: live chaos requested but kernel handle incomplete — falling back to shadow")
			startShadow()
		}

	default:
		log.Printf("serve: unknown chaos mode %q — defaulting to shadow", mode)
		startShadow()
	}
}

// effectiveChaosMode resolves the mode that will actually run given the
// arming guards (allow_live, whitelist, stop token, kernel handle). It mirrors
// the branching inside wireChaos so the panel reports the true effective mode
// rather than the raw configured string.
func effectiveChaosMode(cfg *ares_config.Config) string {
	mode := cfg.Kernel.Chaos.Mode
	if mode == "" {
		mode = "shadow"
	}
	if mode != "live" || !cfg.Kernel.Chaos.AllowLive {
		return "shadow"
	}
	if len(cfg.Kernel.Chaos.EligibleCapabilities) == 0 || cfg.Kernel.Chaos.StopToken == "" {
		return "shadow"
	}
	return "live"
}

// liveChaosGuard holds the enforced safety state for a live chaos loop:
// the rate limiter, per-agent cooldowns, the round-robin cursor, and the
// fail-safe stop latch (REVIEW #12 Phase 2).
type liveChaosGuard struct {
	limiter     *ares_ratelimit.TokenBucketLimiter
	cooldownFor time.Duration
	nextIndex   int

	mu       sync.Mutex
	cooldown map[string]time.Time // agentID -> earliest next injection time
	stopped  bool                 // set when recovery verification fails; stops all future injections
}

func newLiveChaosGuard(ratePerMin int, cooldown time.Duration) *liveChaosGuard {
	if ratePerMin <= 0 {
		ratePerMin = 2
	}
	if cooldown <= 0 {
		cooldown = 10 * time.Minute
	}
	return &liveChaosGuard{
		// Token bucket: ratePerMin injections per minute → per-second rate,
		// burst 1 so injections can never stack.
		limiter: ares_ratelimit.NewTokenBucketLimiter(&ares_ratelimit.LimiterConfig{
			Rate:  float64(ratePerMin) / 60.0,
			Burst: 1,
		}),
		cooldownFor: cooldown,
		cooldown:    make(map[string]time.Time),
	}
}

// allowTarget reports whether agentID is outside its cooldown window. An
// expired cooldown entry is dropped on first touch so the map stays bounded to
// in-cooldown agents instead of accumulating every injected id forever.
func (g *liveChaosGuard) allowTarget(agentID string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	until, ok := g.cooldown[agentID]
	if !ok {
		return true
	}
	if now.After(until) {
		delete(g.cooldown, agentID)
		return true
	}
	return false
}

// markInjected records that agentID was just injected and advances the
// round-robin cursor past it.
func (g *liveChaosGuard) markInjected(agentID string, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cooldown[agentID] = now.Add(g.cooldownFor)
}

// stop trips the fail-safe latch; after this no further injections run.
func (g *liveChaosGuard) stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stopped = true
}

func (g *liveChaosGuard) isStopped() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stopped
}

// liveChaosLoop runs periodic live chaos injections. This is the dangerous
// path: real production agents are killed/suspended. Every injection cycle is
// gated by six enforced guardrails (REVIEW #12 Phase 2):
//
//  1. Emergency stop — POST /api/chaos/stop (X-Chaos-Token) exits the loop
//     permanently.
//  2. Fail-safe latch — if recovery verification ever fails, ALL further
//     injections stop until process restart.
//  3. GA quiet window — when cfg.PauseDuringGA is set, injections are deferred
//     while gaActive() reports a generation mid-flight.
//  4. Rate limit — token bucket capped at cfg.RatePerMin injections/minute.
//  5. Cooldown — an injected agent is not targeted again for cfg.Cooldown.
//  6. Target whitelist — only agents declaring a capability from
//     cfg.EligibleCapabilities qualify (arming itself refuses an empty list).
//
// status (Phase 3) surfaces the loop's operational state to the panel
// (active / injections / fail-safe / GA pause); it may be nil.
func liveChaosLoop(ctx context.Context, chaos *aresrecovery.Chaos, fabric *agentfabric.Fabric, interval time.Duration, cfg ares_config.ChaosConfig, gaActive func() bool, status *introspect.ChaosReporter) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}

	ratePerMin := cfg.RatePerMin
	if ratePerMin <= 0 {
		ratePerMin = 2
	}
	cooldown := parseChaosInterval(cfg.Cooldown, 10*time.Minute)
	guard := newLiveChaosGuard(ratePerMin, cooldown)
	pausedForGA := false

	// Report the armed live state to the panel on loop start.
	if status != nil {
		status.SetLive(introspect.LiveChaosState{Active: true})
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Printf("serve: live chaos loop started (interval=%s, rate_limit=%d/min ENFORCED, cooldown=%s ENFORCED, fail_safe=latch, whitelist=%v, ga_pause=%t)",
		interval.String(), ratePerMin, cooldown.String(), cfg.EligibleCapabilities, cfg.PauseDuringGA)

	for {
		select {
		case <-ctx.Done():
			log.Printf("serve: live chaos loop stopping (context cancelled)")
			if status != nil {
				status.SetLive(introspect.LiveChaosState{Active: false})
			}
			return
		case <-ticker.C:
			// Emergency stop (REVIEW #12 Phase 2): POST /api/chaos/stop trips
			// this permanently — the loop exits rather than idles.
			if liveChaosCtl.Stopped() {
				log.Printf("serve: live chaos loop stopped by emergency stop endpoint")
				if status != nil {
					status.SetLive(introspect.LiveChaosState{
						Active:           false,
						StoppedByControl: true,
					})
				}
				return
			}
			if guard.isStopped() {
				log.Printf("serve: live chaos loop stopped by fail-safe latch (earlier recovery verification failed)")
				if status != nil {
					status.SetLive(introspect.LiveChaosState{
						Active:          false,
						FailSafeTripped: true,
					})
				}
				return
			}
			// GA quiet window (#12 Phase 2): defer injections while a
			// generation is mid-flight. State transitions are logged once so
			// operators can see the pause engaging and releasing.
			if cfg.PauseDuringGA && gaActive != nil && gaActive() {
				if !pausedForGA {
					pausedForGA = true
					log.Printf("serve: live chaos paused — GA generation in flight (quiet window)")
					if status != nil {
						status.SetLive(introspect.LiveChaosState{Active: true, PausedForGA: true})
					}
				}
				continue
			}
			if pausedForGA {
				pausedForGA = false
				log.Printf("serve: live chaos resumed — GA generation finished")
				if status != nil {
					status.SetLive(introspect.LiveChaosState{Active: true, PausedForGA: false})
				}
			}
			runLiveChaosInjection(ctx, chaos, fabric, guard, cfg.EligibleCapabilities, status)
		}
	}
}

// runLiveChaosInjection performs a single chaos injection cycle against the
// next round-robin target that is outside its cooldown window. It injects a
// kill, then verifies recovery; a failed verification trips the fail-safe
// latch so no further injections occur. The cycle is wrapped in panic
// recovery so a chaos failure never crashes the process.
//
// status (Phase 3) records the injection count and fail-safe state; it may be
// nil.
func runLiveChaosInjection(ctx context.Context, chaos *aresrecovery.Chaos, fabric *agentfabric.Fabric, guard *liveChaosGuard, eligible []string, status *introspect.ChaosReporter) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("serve: live chaos injection panicked (recovered): %v", r)
		}
	}()

	agents := fabric.Agents()
	if len(agents) == 0 {
		log.Printf("serve: live chaos — no agents available for injection")
		return
	}

	now := time.Now()

	// Round-robin target selection, skipping agents inside their cooldown
	// window AND agents whose declared capabilities are not whitelisted
	// (#12 Phase 2). If no agent qualifies, skip this cycle entirely.
	var target string
	for i := 0; i < len(agents); i++ {
		candidate := agents[guard.nextIndex%len(agents)]
		guard.nextIndex++
		if !guard.allowTarget(candidate, now) {
			continue
		}
		if !agentEligibleForChaos(fabric, candidate, eligible) {
			continue
		}
		target = candidate
		break
	}
	if target == "" {
		log.Printf("serve: live chaos — no eligible target (cooldown or whitelist), skipping cycle")
		return
	}

	// Enforced rate limit: the token bucket admits at most RatePerMin
	// injections per minute regardless of ticker cadence.
	if allowed, err := guard.limiter.Allow(ctx); err != nil || !allowed {
		log.Printf("serve: live chaos — rate limited (%v), skipping injection on %s", err, target)
		return
	}

	if err := chaos.InjectFailure(ctx, target, aresrecovery.FailureKill); err != nil {
		log.Printf("serve: live chaos inject kill %s failed: %v", target, err)
		return
	}
	guard.markInjected(target, now)

	// Report injection to the panel (Phase 3).
	if status != nil {
		status.AddInjection(now)
	}

	// Verify recovery. VerifyRecovery returns the count of recovered agents;
	// zero means the recovery chain did not restore anything — trip the
	// fail-safe latch so no further injections run.
	recovered := chaos.VerifyRecovery(ctx)
	if recovered == 0 {
		guard.stop()
		log.Printf("serve: live chaos — recovery verification FAILED for %s (0 agents recovered); FURTHER INJECTIONS STOPPED by fail-safe latch", target)
		if status != nil {
			status.SetLive(introspect.LiveChaosState{
				Active:          true,
				FailSafeTripped: true,
			})
		}
		return
	}

	log.Printf("serve: live chaos — agent %s killed and recovered (%d agents recovered)", target, recovered)
}

// parseChaosInterval parses the chaos interval string, returning the default
// on empty or invalid input.
func parseChaosInterval(s string, defaultInterval time.Duration) time.Duration {
	if s == "" {
		return defaultInterval
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return defaultInterval
	}
	return d
}

// agentEligibleForChaos reports whether the named agent declares at least one
// capability present in the whitelist (#12 Phase 2). The whitelist is matched
// against the agent's own Capabilities list; an unknown agent is never
// eligible.
func agentEligibleForChaos(fabric *agentfabric.Fabric, agentID string, whitelist []string) bool {
	if len(whitelist) == 0 {
		return false
	}
	a, err := fabric.Get(agentID)
	if err != nil || a == nil {
		return false
	}
	for _, capName := range a.Capabilities {
		for _, w := range whitelist {
			if capName == w {
				return true
			}
		}
	}
	return false
}
