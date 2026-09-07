#!/bin/bash

# Run Example Script for Style Agent
# This script starts required services and runs the example

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

echo "=========================================="
echo "Style Agent - Starting Services"
echo "=========================================="

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Function to print status
print_status() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Check if Docker is running
check_docker() {
    if ! docker info > /dev/null 2>&1; then
        print_error "Docker is not running. Please start Docker first."
        exit 1
    fi
    print_status "Docker is running"
}

# Start PostgreSQL with pgvector
start_pgvector() {
    print_status "Starting pgvector container on port 5433..."

    # Check if container already exists and is running
    if docker ps --format '{{.Names}}' | grep -q "^pgvector$"; then
        print_warning "pgvector container is already running"
        return
    fi

    # Check if container exists but is stopped
    if docker ps -a --format '{{.Names}}' | grep -q "^pgvector$"; then
        print_status "Starting existing pgvector container..."
        docker start pgvector
    else
        print_status "Creating and starting new pgvector container on port 5433..."
        docker run -d \
            --name pgvector \
            -e POSTGRES_PASSWORD=postgres \
            -e POSTGRES_USER=postgres \
            -e POSTGRES_DB=ARES \
            -p 5433:5432 \
            pgvector/pgvector:pg16
        print_status "pgvector started on port 5433"
    fi

    print_warning "Note: PostgreSQL is now on port 5433 (not 5432)"
}

# Start Ollama
start_ollama() {
    print_status "Checking Ollama..."

    # Check if Ollama is installed
    if ! command -v ollama &> /dev/null; then
        print_error "Ollama is not installed. Please install it first:"
        echo "  brew install ollama"
        exit 1
    fi

    # Check if Ollama service is running
    if pgrep -x "ollama" > /dev/null; then
        print_warning "Ollama is already running"
    else
        print_status "Starting Ollama service..."
        # Start Ollama in background
        ollama serve &
        OLLAMA_PID=$!
        print_status "Ollama started (PID: $OLLAMA_PID)"
        # Wait for Ollama to be ready
        sleep 3
    fi
}

# Pull and run llama3.2 model
setup_llama_model() {
    print_status "Checking for llama3.2 model..."

    # Check if model exists
    if ollama list | grep -q "llama3.2"; then
        print_warning "llama3.2 model already exists"
    else
        print_status "Pulling llama3.2 model (this may take a few minutes)..."
        ollama pull llama3.2
        print_status "llama3.2 model pulled successfully"
    fi
}

# Build the project
build_project() {
    print_status "Building project..."

    cd "$PROJECT_ROOT"

    # Run tests first
    print_status "Running tests..."
    make test

    # Build the example
    print_status "Building example..."
    go build -o bin/example ./examples/_fixtures/simple

    print_status "Build completed"
}

# Run the example
run_example() {
    print_status "Starting example..."

    cd "$PROJECT_ROOT"

    # Set environment variables
    export CONFIG_PATH="./examples/_fixtures/simple/config/server.yaml"

    # Run the example
    ./bin/example
}

# Cleanup function
cleanup() {
    print_status "Cleaning up..."
    # Note: We don't stop services as they may be needed for other things
    print_status "Done"
}

# Main execution
main() {
    print_status "Starting Style Agent Example"

    # Check Docker
    check_docker

    # Start services
    start_pgvector
    start_ollama
    setup_llama_model

    # Build and run
    build_project
    run_example

    print_status "Example completed successfully!"
}

# Handle script arguments
case "${1:-}" in
    --help|-h)
        echo "Usage: $0 [OPTIONS]"
        echo ""
        echo "Options:"
        echo "  --help, -h     Show this help message"
        echo "  --docker-only   Only start Docker services"
        echo "  --ollama-only   Only start Ollama"
        echo "  --build-only   Only build the project"
        echo "  --run-only     Only run the example"
        exit 0
        ;;
    --docker-only)
        check_docker
        start_pgvector
        print_status "Docker services started"
        exit 0
        ;;
    --ollama-only)
        start_ollama
        setup_llama_model
        print_status "Ollama services started"
        exit 0
        ;;
    --build-only)
        build_project
        print_status "Build completed"
        exit 0
        ;;
    --run-only)
        run_example
        exit 0
        ;;
    *)
        main
        ;;
esac
