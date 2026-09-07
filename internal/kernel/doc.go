// Package kernel is the ARES scheduling kernel (the
// former kernelscheduler, kernelctx, and system_runtime, unified).
//
// It is the ONLY place scheduling decisions are made: quantum-based task
// draining (Schedule→Acquire→RunQuantum), executor registry and scoring,
// load tracking, growth-depth quanta, decision recording, and shadow hooks.
// It owns no agents and stores no domain state — lifecycle lives in
// agentfabric, tasks in taskfabric, services in runtime/. Everything the
// kernel needs arrives per quantum; everything it decides goes back as a
// routing verdict.
package kernel
