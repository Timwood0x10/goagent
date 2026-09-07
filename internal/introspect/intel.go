// Package introspect — intelligence engine (migrated from internal/dashboard).
//
// The engine observes agent behavior, computes health scores, detects
// anomalies, and generates actionable insights (migrated from the old
// monitoring engine, not rewritten — the algorithm is unchanged). It is fed from the
// shared event stream via FeedIntel and queried by the serve control plane
// (/api/health, /api/anomalies, /api/insights).
package introspect

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// HealthLevel rates the overall health of a component.
type HealthLevel string

const (
	HealthHealthy   HealthLevel = "healthy"
	HealthDegraded  HealthLevel = "degraded"
	HealthUnhealthy HealthLevel = "unhealthy"
	HealthUnknown   HealthLevel = "unknown"
)

// HealthScore is a point-in-time assessment of a single agent or system.
type HealthScore struct {
	AgentID   string      `json:"agent_id"`
	Level     HealthLevel `json:"level"`
	Score     float64     `json:"score"`       // 0.0 (dead) — 1.0 (perfect)
	Latency   float64     `json:"latency_p99"` // p99 latency in ms
	ErrorRate float64     `json:"error_rate"`  // errors per minute
	Uptime    float64     `json:"uptime_pct"`  // % uptime in window
	UpdatedAt time.Time   `json:"updated_at"`
}

// Severity rates how critical an anomaly is.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Anomaly describes an unusual or problematic event pattern.
type Anomaly struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id,omitempty"`
	Severity  Severity  `json:"severity"`
	Category  string    `json:"category"` // "high_restarts", "slow_llm", "tool_failure", "memory_leak"
	Message   string    `json:"message"`
	Count     int       `json:"count"` // how many times observed
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Resolved  bool      `json:"resolved"`
}

// Insight is an actionable observation derived from correlated events.
type Insight struct {
	ID              string    `json:"id"`
	Type            string    `json:"type"`
	Severity        Severity  `json:"severity"`
	Title           string    `json:"title"`
	Description     string    `json:"description"`
	AgentIDs        []string  `json:"agent_ids,omitempty"`
	SuggestedAction string    `json:"suggested_action,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	Acknowledged    bool      `json:"acknowledged"`
}

// Engine monitors agent behavior, computes health, and surfaces insights.
type Engine struct {
	mu        sync.RWMutex
	agents    map[string]*agentState
	anomalies []*Anomaly
	insights  []*Insight

	// Configurable thresholds.
	HealthWindow      time.Duration // sliding window for health computation
	MaxRestartsPerMin int           // above this → anomaly
	MaxErrorRate      float64       // errors/min above this → degraded
	LatencyThreshold  float64       // p99 ms above this → degraded
	AnomalyCooldown   time.Duration // min time between same-type anomalies
	InsightCooldown   time.Duration // min time between same-type insights

	onInsight func(*Insight) // optional callback (e.g., WebSocket broadcast)
}

// agentState tracks per-agent metrics for health computation.
type agentState struct {
	restarts   []time.Time
	errors     []time.Time
	latencies  []float64
	lastEvent  time.Time
	totalOps   int
	successOps int
}

// maxLatencySamples caps the number of latency samples retained per agent to
// bound memory usage. Older samples are discarded first.
const maxLatencySamples = 1000

// Retention bounds carried over from the migration (the old dashboard engine
// relied on an external pruner that no longer exists — these caps replace it
// so a long-running process cannot grow unboundedly).
const (
	// maxEventSamplesPerAgent hard-caps restarts/errors per agent on top of
	// the time-window trim, so a high-frequency burst within the window
	// cannot balloon the slice to rate×window.
	maxEventSamplesPerAgent = 1000
	// maxAnomalies caps the retained anomaly ring; oldest are evicted first.
	// Anomalies() still filters resolved entries from the response.
	maxAnomalies = 500
	// maxTrackedAgents caps the per-stream state map. When exceeded, the
	// least-recently-active entries are pruned (bounded by stream cardinality
	// on a busy bus: tasks, sessions and agents each get a StreamID).
	maxTrackedAgents = 2000
)

// DefaultEngineConfig returns sensible defaults.
func DefaultEngineConfig() *EngineConfig {
	return &EngineConfig{
		HealthWindow:      5 * time.Minute,
		MaxRestartsPerMin: 3,
		MaxErrorRate:      5.0,
		LatencyThreshold:  5000, // 5s
		AnomalyCooldown:   30 * time.Second,
		InsightCooldown:   1 * time.Minute,
	}
}

// EngineConfig holds thresholds for the intelligence engine.
type EngineConfig struct {
	HealthWindow      time.Duration
	MaxRestartsPerMin int
	MaxErrorRate      float64
	LatencyThreshold  float64
	AnomalyCooldown   time.Duration
	InsightCooldown   time.Duration
}

// NewEngine creates an intelligence engine with the given config.
func NewEngine(cfg *EngineConfig) *Engine {
	if cfg == nil {
		cfg = DefaultEngineConfig()
	}
	return &Engine{
		agents:            make(map[string]*agentState),
		anomalies:         make([]*Anomaly, 0),
		insights:          make([]*Insight, 0),
		HealthWindow:      cfg.HealthWindow,
		MaxRestartsPerMin: cfg.MaxRestartsPerMin,
		MaxErrorRate:      cfg.MaxErrorRate,
		LatencyThreshold:  cfg.LatencyThreshold,
		AnomalyCooldown:   cfg.AnomalyCooldown,
		InsightCooldown:   cfg.InsightCooldown,
	}
}

// OnInsight registers a callback fired when a new insight is generated.
func (e *Engine) OnInsight(fn func(*Insight)) {
	e.onInsight = fn
}

// ObserveAgentEvent feeds a raw agent observation into the engine.
func (e *Engine) ObserveAgentEvent(agentID, eventType string, latencyMs float64, hasError bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	state := e.ensureState(agentID)
	now := time.Now()
	state.lastEvent = now
	state.totalOps++
	if !hasError {
		state.successOps++
	}

	// Prune stale entries to bound memory and keep per-observation cost low.
	e.trimState(state)

	switch {
	case eventType == "restart" || eventType == "resurrection":
		state.restarts = capAppend(state.restarts, now)
		e.detectRestartAnomaly(agentID, state, now)

	case hasError:
		state.errors = capAppend(state.errors, now)
		e.detectErrorAnomaly(agentID, state, now)

	case latencyMs > 0:
		state.latencies = append(state.latencies, latencyMs)
		if len(state.latencies) > maxLatencySamples {
			// Drop oldest sample to keep the slice bounded.
			state.latencies = state.latencies[len(state.latencies)-maxLatencySamples:]
		}
		e.detectLatencyAnomaly(agentID, state, now)
	}
}

// capAppend appends t and hard-caps the slice at maxEventSamplesPerAgent,
// dropping oldest entries — a ceiling on top of the time-window trim so a
// burst within the window cannot grow the slice without bound (#intel).
func capAppend(times []time.Time, t time.Time) []time.Time {
	times = append(times, t)
	if len(times) > maxEventSamplesPerAgent {
		times = times[len(times)-maxEventSamplesPerAgent:]
	}
	return times
}

// trimState prunes entries older than the health window from the restarts and
// errors slices. Latencies are bounded by maxLatencySamples instead.
func (e *Engine) trimState(s *agentState) {
	cutoff := time.Now().Add(-e.HealthWindow)
	s.restarts = trimTimes(s.restarts, cutoff)
	s.errors = trimTimes(s.errors, cutoff)
}

// trimTimes drops all entries at or before cutoff. The slice is assumed to be
// in ascending chronological order (append-ordered).
func trimTimes(times []time.Time, cutoff time.Time) []time.Time {
	idx := sort.Search(len(times), func(i int) bool {
		return times[i].After(cutoff)
	})
	if idx == 0 {
		return times
	}
	// Shift to the front to allow the old backing array to be GC'd.
	return append(times[:0], times[idx:]...)
}

// Health returns the current health score for an agent.
func (e *Engine) Health(agentID string) HealthScore {
	// Single read-lock acquisition: the agentState pointer is stable (never
	// replaced after creation), so the previous double-lock (RLock, release,
	// RLock) was unnecessary and fragile.
	e.mu.RLock()
	defer e.mu.RUnlock()

	state, ok := e.agents[agentID]
	if !ok {
		return HealthScore{AgentID: agentID, Level: HealthUnknown, Score: 0}
	}

	score, level := e.computeHealth(state)
	return HealthScore{
		AgentID:   agentID,
		Level:     level,
		Score:     score,
		Latency:   percentile(state.latencies, 0.99),
		ErrorRate: ratePerMin(state.errors, e.HealthWindow),
		Uptime:    uptimePct(state.successOps, state.totalOps),
		UpdatedAt: state.lastEvent,
	}
}

// AllHealth returns health scores for all known agents.
func (e *Engine) AllHealth() []HealthScore {
	e.mu.RLock()
	ids := make([]string, 0, len(e.agents))
	for id := range e.agents {
		ids = append(ids, id)
	}
	e.mu.RUnlock()

	out := make([]HealthScore, len(ids))
	for i, id := range ids {
		out[i] = e.Health(id)
	}
	return out
}

// SystemHealth returns aggregate health across all agents.
func (e *Engine) SystemHealth() HealthScore {
	scores := e.AllHealth()
	if len(scores) == 0 {
		return HealthScore{Level: HealthUnknown, Score: 0}
	}

	var avgScore float64
	var maxLatency float64
	var totalErrorRate float64
	var worstLevel = HealthHealthy

	for _, s := range scores {
		avgScore += s.Score
		if s.Latency > maxLatency {
			maxLatency = s.Latency
		}
		totalErrorRate += s.ErrorRate
		if severity(s.Level) > severity(worstLevel) {
			worstLevel = s.Level
		}
	}

	return HealthScore{
		AgentID:   "__system__",
		Level:     worstLevel,
		Score:     avgScore / float64(len(scores)),
		Latency:   maxLatency,
		ErrorRate: totalErrorRate,
		UpdatedAt: time.Now(),
	}
}

// Anomalies returns all active (unresolved) anomalies.
func (e *Engine) Anomalies() []*Anomaly {
	e.mu.RLock()
	defer e.mu.RUnlock()

	out := make([]*Anomaly, 0, len(e.anomalies))
	for _, a := range e.anomalies {
		if !a.Resolved {
			out = append(out, a)
		}
	}
	return out
}

// ResolveAnomaly marks an anomaly as resolved.
func (e *Engine) ResolveAnomaly(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, a := range e.anomalies {
		if a.ID == id {
			a.Resolved = true
			return
		}
	}
}

// Insights returns all unacknowledged insights.
func (e *Engine) Insights() []*Insight {
	e.mu.RLock()
	defer e.mu.RUnlock()

	out := make([]*Insight, 0, len(e.insights))
	for _, in := range e.insights {
		if !in.Acknowledged {
			out = append(out, in)
		}
	}
	return out
}

// AcknowledgeInsight marks an insight as acknowledged.
func (e *Engine) AcknowledgeInsight(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, in := range e.insights {
		if in.ID == id {
			in.Acknowledged = true
			return
		}
	}
}

// ensureState returns the per-agent state, creating it on first sight.
// When the map exceeds maxTrackedAgents, the least-recently-active entries
// are pruned first — bounding the map by stream cardinality on a busy bus
// (#intel: every distinct StreamID — tasks, sessions, agents — would
// otherwise become a permanent entry).
func (e *Engine) ensureState(agentID string) *agentState {
	s, ok := e.agents[agentID]
	if !ok {
		if len(e.agents) >= maxTrackedAgents {
			e.pruneStalestAgent()
		}
		s = &agentState{}
		e.agents[agentID] = s
	}
	return s
}

// pruneStalestAgent removes the single least-recently-active agent state.
// Caller holds e.mu. O(n) but only runs at the cap boundary.
func (e *Engine) pruneStalestAgent() {
	var oldestID string
	var oldest time.Time
	first := true
	for id, s := range e.agents {
		if first || s.lastEvent.Before(oldest) {
			oldestID, oldest, first = id, s.lastEvent, false
		}
	}
	if oldestID != "" {
		delete(e.agents, oldestID)
	}
}

func (e *Engine) computeHealth(s *agentState) (float64, HealthLevel) {
	window := e.HealthWindow
	errRate := ratePerMin(s.errors, window)
	restartRate := ratePerMin(s.restarts, window)
	p99 := percentile(s.latencies, 0.99)

	// Start at 1.0, deduct for each problem.
	score := 1.0

	// Error rate penalty: -0.2 per error/min above threshold.
	if errRate > e.MaxErrorRate {
		score -= 0.2 * (errRate - e.MaxErrorRate)
	}

	// Restart penalty: -0.3 per restart/min.
	if restartRate > 0 {
		score -= 0.3 * restartRate
	}

	// Latency penalty: -0.1 per second above threshold (in ms).
	if p99 > e.LatencyThreshold {
		excess := (p99 - e.LatencyThreshold) / 1000
		if excess > 1 {
			excess = 1
		}
		score -= 0.1 * excess
	}

	// Uptime penalty.
	up := uptimePct(s.successOps, s.totalOps)
	score *= up

	if score < 0 {
		score = 0
	}

	var level HealthLevel
	switch {
	case score >= 0.85:
		level = HealthHealthy
	case score >= 0.5:
		level = HealthDegraded
	default:
		level = HealthUnhealthy
	}

	return score, level
}

func (e *Engine) detectRestartAnomaly(agentID string, s *agentState, now time.Time) {
	rate := ratePerMin(s.restarts, e.HealthWindow)
	if rate >= float64(e.MaxRestartsPerMin) && !e.recentAnomaly(agentID, "high_restarts") {
		e.addAnomaly(&Anomaly{
			ID:        fmt.Sprintf("anomaly-%d", now.UnixNano()),
			AgentID:   agentID,
			Severity:  SeverityWarning,
			Category:  "high_restarts",
			Message:   fmt.Sprintf("Agent %s restarted %.1f times/min", agentID, rate),
			Count:     len(s.restarts),
			FirstSeen: s.restarts[0],
			LastSeen:  now,
		})
	}
}

func (e *Engine) detectErrorAnomaly(agentID string, s *agentState, now time.Time) {
	rate := ratePerMin(s.errors, e.HealthWindow)
	if rate >= e.MaxErrorRate && !e.recentAnomaly(agentID, "high_errors") {
		e.addAnomaly(&Anomaly{
			ID:        fmt.Sprintf("anomaly-%d", now.UnixNano()),
			AgentID:   agentID,
			Severity:  SeverityCritical,
			Category:  "high_errors",
			Message:   fmt.Sprintf("Agent %s error rate %.1f/min", agentID, rate),
			Count:     len(s.errors),
			FirstSeen: s.errors[0],
			LastSeen:  now,
		})
	}
}

func (e *Engine) detectLatencyAnomaly(agentID string, s *agentState, now time.Time) {
	p99 := percentile(s.latencies, 0.99)
	if p99 > e.LatencyThreshold && !e.recentAnomaly(agentID, "high_latency") {
		e.addAnomaly(&Anomaly{
			ID:       fmt.Sprintf("anomaly-%d", now.UnixNano()),
			AgentID:  agentID,
			Severity: SeverityWarning,
			Category: "high_latency",
			Message:  fmt.Sprintf("Agent %s p99 latency %.0fms", agentID, p99),
			Count:    len(s.latencies),
			LastSeen: now,
		})
	}
}

func (e *Engine) recentAnomaly(agentID, category string) bool {
	for _, a := range e.anomalies {
		if a.AgentID == agentID && a.Category == category && !a.Resolved {
			if time.Since(a.LastSeen) < e.AnomalyCooldown {
				a.Count++
				a.LastSeen = time.Now()
				return true
			}
		}
	}
	return false
}

func (e *Engine) addAnomaly(a *Anomaly) {
	e.anomalies = append(e.anomalies, a)
	// Bound the ring (#intel migration regression: the old engine relied on
	// an external pruner). Evict oldest first; Anomalies() still filters
	// resolved entries from the response.
	if len(e.anomalies) > maxAnomalies {
		e.anomalies = e.anomalies[len(e.anomalies)-maxAnomalies:]
	}
}

func ratePerMin(events []time.Time, window time.Duration) float64 {
	if len(events) == 0 {
		return 0
	}
	cutoff := time.Now().Add(-window)
	var count int
	for _, t := range events {
		if t.After(cutoff) {
			count++
		}
	}
	return float64(count) / window.Minutes()
}

// percentile returns the p-th percentile (0.0 ≤ p ≤ 1.0) of values using
// the nearest-rank method. An empty input returns 0. p outside [0,1] is
// clamped. The input slice is not mutated; a copy is sorted internally.
func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if p < 0 {
		p = 0
	} else if p > 1 {
		p = 1
	}
	cp := make([]float64, len(values))
	copy(cp, values)
	sort.Float64s(cp)
	// Nearest-rank: rank = ceil(p * n), clamped to [1, n].
	rank := int(p*float64(len(cp)) + 0.9999999)
	if rank < 1 {
		rank = 1
	}
	if rank > len(cp) {
		rank = len(cp)
	}
	return cp[rank-1]
}

func uptimePct(success, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(success) / float64(total)
}

func severity(l HealthLevel) int {
	switch l {
	case HealthHealthy:
		return 0
	case HealthDegraded:
		return 1
	case HealthUnhealthy:
		return 2
	default:
		return -1
	}
}

// evErrKind is the event-type label mapped to an error observation.
const evErrKind = "error"

// FeedIntel feeds one bus event into the intelligence engine using the same
// mapping as the migrated dashboard.EventBridge:
// restart/error/latency/tick observations drive health scoring.
func FeedIntel(intel *Engine, evt *ares_events.Event) {
	if intel == nil || evt == nil {
		return
	}
	agentID := evt.StreamID
	latency := extractLatency(evt)
	hasError := evt.Type == evErrKind || evt.Type == "task.failed" || evt.Type == "tool.error" || evt.Type == "llm.error"
	isRestart := evt.Type == "agent.restarted" || evt.Type == "failover.completed"
	switch {
	case isRestart:
		intel.ObserveAgentEvent(agentID, "restart", 0, false)
	case hasError:
		intel.ObserveAgentEvent(agentID, evErrKind, 0, true)
	case latency > 0:
		intel.ObserveAgentEvent(agentID, "latency", latency, false)
	default:
		intel.ObserveAgentEvent(agentID, "tick", 0, false)
	}
}

// extractLatency attempts to extract a latency value in milliseconds from an
// event payload. The "duration_ms" key is already in milliseconds. The
// "duration" key stores nanoseconds (as float64, int64, or time.Duration) and
// is converted to milliseconds to avoid unit confusion that would cause
// 5ms to appear as 5,000,000ms.
func extractLatency(evt *ares_events.Event) float64 {
	if evt.Payload == nil {
		return 0
	}
	// duration_ms is already in milliseconds.
	if d, ok := evt.Payload["duration_ms"].(float64); ok {
		return d
	}
	// duration is stored as nanoseconds; convert to ms.
	if d, ok := evt.Payload["duration"].(float64); ok {
		return d / 1e6
	}
	if d, ok := evt.Payload["duration"].(time.Duration); ok {
		return float64(d.Milliseconds())
	}
	if d, ok := evt.Payload["duration"].(int64); ok {
		return float64(d) / 1e6
	}
	return 0
}
