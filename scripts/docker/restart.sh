#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"

echo "=== Stopping existing containers ==="
docker compose -f "$ROOT/docker-compose.yml" down -v 2>/dev/null || true

echo ""
echo "=== Starting PostgreSQL + pgvector ==="
docker compose -f "$ROOT/docker-compose.yml" up -d

echo ""
echo "=== Waiting for PostgreSQL to be ready ==="
until docker compose -f "$ROOT/docker-compose.yml" exec -T postgres pg_isready -U postgres >/dev/null 2>&1; do
  sleep 1
done
echo "PostgreSQL is ready."

echo ""
echo "=== Running test database migrations ==="
cd "$ROOT" && go run ./cmd/ares db setup-test

echo ""
echo "=== Running production database migrations ==="
export DB_NAME="ARES"
cd "$ROOT" && go run ./cmd/ares db migrate

echo ""
echo "✅ All services are up and databases are migrated."
echo ""
echo "   Test DB:      postgres://postgres:postgres@localhost:5433/ARES_test?sslmode=disable"
echo "   Production DB: postgres://postgres:postgres@localhost:5433/ARES?sslmode=disable"
echo ""
echo "   Run tests:    export TEST_POSTGRES_DSN=\"postgres://postgres:postgres@localhost:5433/ARES_test?sslmode=disable\""
echo "                 make demo-test"
echo ""
echo "   View logs:    docker compose -f $ROOT/docker-compose.yml logs -f"
echo "   Shutdown:     docker compose -f $ROOT/docker-compose.yml down -v"
