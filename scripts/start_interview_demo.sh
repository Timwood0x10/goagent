#!/usr/bin/env bash
#
# start_interview_demo.sh - One-click startup script
#
# 1. Starts SearXNG (Docker Compose)
# 2. Waits for SearXNG health check
# 3. Starts the Interview Agent CLI
#
# Usage:
#   ./scripts/start_interview_demo.sh
#
# Environment Variables:
#   CONFIG_PATH  - Path to server.yaml (default: examples/_fixtures/interview-demo/config/server.yaml)
#   SEARXNG_URL  - SearXNG base URL (default: http://localhost:5605)
#

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

# ── Colors ──────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

log_info()  { printf "${GREEN}[INFO]${NC}  %s\n" "$*"; }
log_warn()  { printf "${YELLOW}[WARN]${NC}  %s\n" "$*"; }
log_error() { printf "${RED}[ERROR]${NC} %s\n" "$*"; }
log_step()  { printf "\n${CYAN}══════ %s ══════${NC}\n" "$*"; }

# ── Cleanup on exit ─────────────────────────────
cleanup() {
  log_step "Shutting down"
  log_info "Stopping SearXNG..."
  docker compose stop searxng 2>/dev/null || true
  log_info "Done."
}
trap cleanup EXIT

# ── 1. Check Docker ────────────────────────────
log_step "Checking Docker"
if ! command -v docker &>/dev/null; then
  log_error "Docker is not installed. Install Docker Desktop: https://docs.docker.com/get-docker/"
  exit 1
fi

if ! docker info &>/dev/null; then
  log_error "Docker daemon is not running. Start Docker Desktop first."
  exit 1
fi
log_info "Docker is running."

# ── 2. Start SearXNG ───────────────────────────
log_step "Starting SearXNG"

# Check if already running
if docker ps --format '{{.Names}}' | grep -q 'ARES-searxng'; then
  log_info "SearXNG container already running."
else
  # Remove exited container if present
  docker rm -f ARES-searxng 2>/dev/null || true

  log_info "Bringing up searxng service..."
  if ! docker compose up -d searxng 2>&1; then
    log_error "Failed to start SearXNG container."
    log_error "Check: docker compose logs searxng"
    exit 1
  fi
fi

# Wait for container to be running
log_info "Waiting for SearXNG container..."
MAX_WAIT=30
WAIT=0
while [ $WAIT -lt $MAX_WAIT ]; do
  STATUS=$(docker inspect -f '{{.State.Status}}' ARES-searxng 2>/dev/null || echo "missing")
  if [ "$STATUS" = "running" ]; then
    break
  elif [ "$STATUS" = "exited" ] || [ "$STATUS" = "dead" ]; then
    log_error "SearXNG container exited unexpectedly."
    log_error "Logs:"
    docker logs --tail 10 ARES-searxng 2>&1 | sed 's/^/  /'
    exit 1
  fi
  WAIT=$((WAIT + 1))
  sleep 1
done

if [ $WAIT -ge $MAX_WAIT ]; then
  log_error "SearXNG container did not start within ${MAX_WAIT}s."
  exit 1
fi

# Wait for HTTP endpoint
log_info "Waiting for SearXNG HTTP endpoint..."
MAX_RETRIES=30
RETRY=0
SEARXNG_URL="${SEARXNG_URL:-http://localhost:5605}"
while [ $RETRY -lt $MAX_RETRIES ]; do
  if curl -sf "${SEARXNG_URL}/search?q=health&format=json" >/dev/null 2>&1; then
    log_info "SearXNG is ready on ${SEARXNG_URL}"
    break
  fi
  RETRY=$((RETRY + 1))
  if [ $RETRY -eq $MAX_RETRIES ]; then
    log_error "SearXNG HTTP not ready within ${MAX_RETRIES}s."
    log_error "Check: docker compose logs searxng"
    exit 1
  fi
  sleep 1
done

# Verify JSON API works
RESULT=$(curl -sf "${SEARXNG_URL}/search?q=test&format=json" 2>/dev/null | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d.get('results',[])))" 2>/dev/null || echo "0")
if [ "$RESULT" -gt 0 ]; then
  log_info "SearXNG JSON API working (${RESULT} results for 'test')"
else
  log_warn "SearXNG responding but JSON API may have issues"
fi

# ── 3. Build and start the Interview Agent ──────
log_step "Starting Interview Agent"

CONFIG_PATH="${CONFIG_PATH:-examples/_fixtures/interview-demo/config/server.yaml}"
if [ ! -f "$CONFIG_PATH" ]; then
  log_error "Config not found: $CONFIG_PATH"
  exit 1
fi
export CONFIG_PATH

log_info "Building interview-demo..."
if ! go build -o /tmp/interview-demo ./examples/_fixtures/interview-demo/; then
  log_error "Build failed."
  exit 1
fi
log_info "Build succeeded."

echo ""
echo "============================================================"
echo "  Interview Agent is ready!"
echo "  Type a technical interview question to search and analyze."
echo "  Type 'caps' to see capabilities, 'exit' to quit."
echo "============================================================"
echo ""

# Run the binary (subshell so trap handles docker stop)
/tmp/interview-demo
