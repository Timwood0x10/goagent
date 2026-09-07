// Package runtime is the ARES AgentOS service layer.
//
// It owns everything above the kernel/fabric substrate that serves agents:
// evolution, memory, evaluation, protocol adapters (MCP/skills/AHP),
// observability, the chaos arena, flight recording, and the round archive.
// Lifecycle orchestration (spawn/start/stop/restore) belongs here, not in
// the kernel — the kernel only schedules quanta.
//
// Layout (one directory per service; each keeps its own package identity):
//
//	archive        – round archive reader (recall commands)
//	arena          – chaos engineering scenarios
//	ares_evolution – evolution runtime wiring: deployment, gates, fitness
//	eval           – evaluation suites and judges (single package eval)
//	evolution      – evolution engine: genome, patches, GA (package evolution)
//	memory         – session memory, distillation pipeline, experience store
//	observability  – metrics, tracers, flight recorder
//	protocol       – external protocol adapters: mcp, skills, ahp
package runtime
