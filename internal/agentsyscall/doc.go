// Package agentsyscall provides the Kernel-validated spawn_agent and
// create_task syscalls that are exposed to real LLM agents as tools.
//
// Design: a real LLM executor (sub.Agent) decides
// whether to split a task. When it does, it calls the spawn_agent tool —
// the Kernel validates capability/quota/resource, creates the Agent in
// agentfabric, registers it as a scheduler executor, and optionally creates
// a Task Fabric task for the new agent. The LLM's decomposition decision
// is the cognition layer; this package is the enforcement layer.
//
// "Agent decides. Kernel enforces."
package agentsyscall
