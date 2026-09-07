package agentfabric

import (
	"errors"
	"sync"
	"time"
)

// AgentState is the lifecycle state of a managed Agent (design §3 of
// ares-runtime: disposable execution).
type AgentState string

const (
	// StateIdle: the agent is alive and available for assignment.
	StateIdle AgentState = "IDLE"
	// StateRunning: the agent is executing a task.
	StateRunning AgentState = "RUNNING"
	// StateSuspended: the agent is paused (Lifecycle, not Task); its state
	// is preserved and it can be resumed.
	StateSuspended AgentState = "SUSPENDED"
	// StateRetired: the agent is permanently decommissioned (retire); it
	// cannot be resumed. Its in-flight tasks are reclaimed by the Runtime.
	StateRetired AgentState = "RETIRED"
)

// Agent is a disposable, peer-equivalent cognitive process (design §3 +
// §13). Agents are NOT orchestrated — they are scheduled (by taskfabric) and
// managed (by this Fabric). An Agent independently holds its own Cognitive
// State; the Runtime never depends on hidden CoT, only on checkpointable
// state.
type Agent struct {
	// Identity is the stable agent identifier.
	Identity string
	// Capabilities are declared capabilities (used by the capability-aware
	// scheduler in taskfabric).
	Capabilities []string
	// State is the current lifecycle state.
	State AgentState
	// Load is the current utilization (0 = idle; scheduler hint).
	Load float64
	// Confidence is the experience-derived prior (ares_skills source).
	Confidence float64
	// Parent is the spawning agent's identity ("" for a root agent). This
	// is PROVENANCE ONLY — parent/child does NOT form a permission
	// hierarchy (§13 invariant #1: A ≡ B ≡ C).
	Parent string
	// Priority is the scheduling priority (>= 0; 0 = normal). It mirrors
	// OS-thread priority: the taskfabric scheduler boosts higher-priority
	// agents when choosing among capable candidates.
	Priority float64
	// SpawnedAt is when the agent was created via spawn.
	SpawnedAt time.Time

	// resources is the agent's P5 resource claim (name → amount), recorded at
	// spawn for quota accounting and released on kill/retire. Guarded by
	// Fabric.mu (the registry lock), not a.mu: it is only read/written under
	// the fabric lock during spawn/kill/retire.
	resources map[string]float64

	mu             sync.RWMutex // guards cognitive, privateContext, taskContext, state, load, cognition
	cognitive      CognitiveState
	privateContext map[string]any
	taskContext    map[string]any // shared task state (read-only view for the agent)
	// cognition is the agent's execution body (A1: 执行能力注入统一 Agent).
	// Nil means the agent has no execution capability yet — it can be managed
	// (spawn/kill/recover) but cannot run a quantum until injected via
	// SpawnSpec.CognitionFactory. Guarded by mu.
	cognition Cognition
	// governance is the P3 budget state (see governance.go). Nil when the
	// agent was spawned without budgets.
	governance *governanceState
}

// CognitiveStateSchemaVersion is the current CognitiveState schema version.
// Bump when CognitiveState fields change; DecodeCognitiveState handles
// migration from prior versions. (A2: 带版本的结构.)
const CognitiveStateSchemaVersion = 1

// CognitiveState is the agent's independent cognitive content (design §13:
// Context / Observation / Working Memory / Decision / Tool State / Checkpoint).
// It is independently checkpointable — the Runtime does NOT depend on hidden
// chain-of-thought, only on this durable state.
//
// SchemaVersion is the version of this struct. DecodeCognitiveState rejects a
// future version instead of silently misinterpreting it. Callers that
// construct a zero-value CognitiveState (e.g. in tests) produce SchemaVersion=0
// (legacy), which DecodeCognitiveState accepts as compatible.
type CognitiveState struct {
	// SchemaVersion is the state's schema version. Set by SetCognitiveState
	// and Recover; zero means legacy (pre-A2) and is accepted by
	// DecodeCognitiveState.
	SchemaVersion int
	// Context is the active reasoning context (task goal + constraints).
	Context any
	// Observation is the latest observation from the environment/tools.
	Observation any
	// WorkingMemory is the agent's scratchpad for intermediate reasoning.
	WorkingMemory any
	// Decision is the current decision/hypothesis.
	Decision any
	// ToolState is the state of active tools (open files, connections…).
	ToolState any
	// Checkpoint is the durable progress pointer (links to taskfabric
	// Checkpoint when the agent is executing a Task).
	Checkpoint any
}

// ErrAgentNotFound is returned when an agent id is unknown to the Fabric.
var ErrAgentNotFound = errors.New("agentfabric: agent not found")

// ErrAgentExists is returned when an agent with the given id already exists.
var ErrAgentExists = errors.New("agentfabric: agent already exists")

// ErrAgentNotIdle is returned when an operation requires an IDLE agent but
// the agent is in a different state.
var ErrAgentNotIdle = errors.New("agentfabric: agent not idle")

// ErrAgentRetired is returned when an operation targets a retired agent.
var ErrAgentRetired = errors.New("agentfabric: agent retired")

// ErrAgentNotSuspended is returned when an operation (e.g. resume) targets an
// agent that is not in the SUSPENDED state.
var ErrAgentNotSuspended = errors.New("agentfabric: agent not suspended")

// ErrAgentRunning is returned when an operation cannot proceed because the
// agent is RUNNING (e.g. retire a running agent without suspend first).
var ErrAgentRunning = errors.New("agentfabric: agent running")

// ErrAgentNotExecutable is returned when ExecuteStep is called on an agent
// that was spawned without a CognitionFactory (A1: 执行能力未注入).
var ErrAgentNotExecutable = errors.New("agentfabric: agent not executable")

// Executable reports whether the agent has an execution body injected (A1:
// 执行能力已注入). An agent spawned without a CognitionFactory is managed
// (spawn/kill/recover) but NOT schedulable — the scheduler must not offer it
// as a candidate. Thread-safe; reads the cognition under the agent lock.
func (a *Agent) Executable() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cognition != nil
}

// ErrInvalidSpawnSpec is returned when a SpawnSpec is invalid.
var ErrInvalidSpawnSpec = errors.New("agentfabric: invalid spawn spec")
