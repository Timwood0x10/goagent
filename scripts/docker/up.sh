#!/usr/bin/env bash
# ── ARES Local Dev Environment Starter ────────────────────────────────────
# Usage: ./scripts/docker/up.sh
#
# Starts pgvector + Ollama locally, pulls a model, and prints connection info.
# Requires Docker. Run once at the start of a dev session.
set -euo pipefail

cd "$(dirname "$0")/../.."

echo "🚀 ARES Local Dev Environment"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# ── 1. PostgreSQL + pgvector ─────────────────────────────────────────────
if docker ps --filter name=ARES-pg --format "{{.Names}}" | grep -q ARES-pg; then
  echo "✅ pgvector already running (port 5433)"
else
  echo "📦 Starting pgvector..."
  docker run -d \
    --name ARES-pg \
    -e POSTGRES_USER=postgres \
    -e POSTGRES_PASSWORD=postgres \
    -e POSTGRES_DB=ARES_test \
    -p 5433:5432 \
    --health-cmd "pg_isready -U postgres" \
    --health-interval 5s \
    --health-timeout 5s \
    --health-retries 5 \
    pgvector/pgvector:pg15

  echo "⏳ Waiting for pgvector to be healthy..."
  until docker exec ARES-pg pg_isready -U postgres >/dev/null 2>&1; do sleep 1; done
  echo "✅ pgvector ready (port 5433)"
fi

# ── 2. Ollama (local) ────────────────────────────────────────────────────
echo "🔍 Checking local Ollama..."
if curl -s http://localhost:11434/api/tags >/dev/null 2>&1; then
  echo "✅ Ollama already running (localhost:11434)"
else
  echo "📦 Starting local Ollama..."
  if command -v ollama >/dev/null 2>&1; then
    ollama serve >/tmp/ollama.log 2>&1 &
    echo "⏳ Waiting for Ollama to start..."
    until curl -s http://localhost:11434/api/tags >/dev/null 2>&1; do sleep 2; done
    echo "✅ Ollama started (localhost:11434)"
  else
    echo "❌ ollama command not found. Install it from https://ollama.com"
    exit 1
  fi
fi

# ── 3. Pull model (skip if already present) ──────────────────────────────
MODEL="${1:-llama3.2}"
if curl -s http://localhost:11434/api/tags | grep -q "\"name\":\"$MODEL\""; then
  echo "✅ Model $MODEL already pulled"
else
  echo "📥 Pulling model: $MODEL (this may take a while on first run)..."
  ollama pull "$MODEL"
  echo "✅ Model $MODEL ready"
fi

# ── 4. Summary ───────────────────────────────────────────────────────────
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "✅ Environment Ready!"
echo ""
echo "   PostgreSQL:  postgres://postgres:postgres@localhost:5433/ARES_test?sslmode=disable"
echo "   Ollama:      http://localhost:11434"
echo "   Model:       $MODEL"
echo ""
echo "   Run the demo:"
echo "     go run examples/_fixtures/11-knowledge-import/main.go -mode auto"
echo "     go run examples/_fixtures/11-knowledge-import/main.go -mode explicit"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"