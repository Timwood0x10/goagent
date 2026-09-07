# Examples

ARES examples, organized as a LEARNING PATH across four layers. Every example
runs on the single kernel execution path (taskfabric + kernelscheduler) —
there is no second engine.

> **Phase 4 (2026-09-07):** All example code has been moved to
> `examples/_fixtures/`. The top-level `examples/` directory now only
> contains `_fixtures/`, `arena/` (YAML scenarios), and this README.

| Layer | What you learn |
|---|---|
| **Basics** | SDK four verbs: NewRuntime → NewAgent/RegisterAgent → Run/Submit |
| **Orchestration** | `sdk.Graph`: conditions, router loops, fan-out+join, subgraphs; HTTP graph submission |
| **Kernel internals** | Watch the scheduler work; LLM-decided spawn via kernel syscalls; deterministic AgentOS baseline |
| **Evolution** | GA strategy evolution and genome patching |

Quick start (no API key needed):

```bash
make quickstart        # = go run examples/_fixtures/01-quickstart/main.go with Ollama
```

Legend: ★ flagship · LLM = needs a configured provider · dry = runs without an LLM

## Basics

| Example | Concept | Needs LLM |
|---|---|---|
| [01-quickstart](_fixtures/01-quickstart/) | Runtime → Agent → Run, minimal surface | yes |
| [02-tool-calling](_fixtures/02-tool-calling/) | Tool registry + ReAct loop | yes |
| [04-multi-agent](_fixtures/04-multi-agent/) | RegisterAgent by capability + Submit dispatch | yes |
| [07-human-in-loop](_fixtures/07-human-in-loop/) | Human approval gates inside agent loops | yes |
| [12-yaml-driven-flags](_fixtures/12-yaml-driven-flags/) | Config-driven setup (`ares.yaml`) | no |

## Orchestration

| Example | Concept | Needs LLM |
|---|---|---|
| [03-dag-workflow](_fixtures/03-dag-workflow/) | sdk.Graph core shapes + the three collaboration modes (delegate / pipeline / orchestrate) | dry |
| [28-collab-graphs](_fixtures/28-collab-graphs/) | Submit explicit DAGs over HTTP (`POST /api/graphs`); ops surface of C4 | yes (serve) |
| [29-akf-graph-node](_fixtures/29-akf-graph-node/) | AKF knowledge-fabric step as a `sdk.Graph` node (BETA adapter) | no |
| [09-full-app](_fixtures/09-full-app/) | Composing tools + memory + agents into a small app | yes |
| [21-ai-assistant-integration](_fixtures/21-ai-assistant-integration/) | Embedding ARES into an existing assistant stack | yes |

## Kernel internals

| Example | Concept | Needs LLM |
|---|---|---|
| [26-runtime-scheduling-demo](_fixtures/26-runtime-scheduling-demo/) ★ | Watch the kernelscheduler drive a capability agent | yes |
| [27-peer-spawn-demo](_fixtures/27-peer-spawn-demo/) ★★ | REAL LLM autonomously decomposes: spawn_agent ×N + create_task ×N through kernel syscalls; captured evidence in `evidence/` | yes |
| [aresos-demo](_fixtures/aresos-demo/) | Deterministic 7-step AgentOS baseline (spawn → parallel → death → IPC → revival → synthesis), zero deps | **no** |
| [06-chaos-resilience](_fixtures/06-chaos-resilience/) | Failure injection & recovery semantics | partial |

## Evolution

| Example | Concept | Needs LLM |
|---|---|---|
| [05-evolution-demo](_fixtures/05-evolution-demo/) | Strategy evolution intro (`rt.Evolve`) | yes |
| [10-ga-full-evolution](_fixtures/10-ga-full-evolution/) | Full GA pipeline on public api/evolution blocks | no |
| [19-ga-candidate-e2e](_fixtures/19-ga-candidate-e2e/) | Multi-generation GA → champion → CandidateVerifier gates | no |
| [22-evolution-blocks](_fixtures/22-evolution-blocks/) | Zero-internal composition path for external embedders | no |
| [runtime_evolution/](_fixtures/runtime_evolution/) | Genome patching over engine DAGs (workflow/knowledge/recovery) | no |

> The scheduler genome dimension was RETIRED (fusion plan §B1): sdk.Graph runs
> fully-parallel ready batches. A future concurrency dimension may evolve
> `sdk.Graph.MaxRoundConcurrency`.

## Advanced / integrations

Unnumbered utility examples, each demonstrating one integration surface:

| Directory | Surface |
|---|---|
| [08-mcp-integration](_fixtures/08-mcp-integration/) · [mcp-registry](_fixtures/mcp-registry/) | MCP tool discovery & servers |
| [11-knowledge-import](_fixtures/11-knowledge-import/) · [knowledge-fabric](_fixtures/knowledge-fabric/) | AKF/AKG knowledge pipeline & tools |
| [13-archive-akg-chain](_fixtures/13-archive-akg-chain/) | Archive → AKG distillation chain |
| [14-tool-discovery](_fixtures/14-tool-discovery/) · [external-tools](_fixtures/external-tools/) | Tool discovery sources |
| [15-llm-evolution-suite](_fixtures/15-llm-evolution-suite/) · [25-dual-endpoint-fallback](_fixtures/25-dual-endpoint-fallback/) | LLM-driven evolution suite · endpoint failover |
| [arena](arena/) · [eval](_fixtures/eval/) | Chaos arena CLI · evaluation harness |
| [custom-store](_fixtures/custom-store/) | Pluggable knowledge store backend |
| [discovery](_fixtures/discovery/) | Legacy service discovery (deprecated) |
