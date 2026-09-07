// Package memory is the ARES memory service.
//
// It owns two complementary stores under one module: per-session working
// memory (sessions, retrieval pipelines, RAG, embedding workers) and the
// distilled experience store (experience/ — task outcomes distilled into
// reusable priors for spawn-time injection and skill selection). Session
// memory answers "what happened"; experience answers "what worked".
// Backed by PostgreSQL (distilled_memories); the experience repository is
// the seam the memory manager distills through.
package memory
