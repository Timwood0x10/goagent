# Contributing to ares

Thank you for considering contributing to ares! This document provides guidelines and instructions for contributing.

## Code of Conduct

By participating in this project, you agree to maintain a respectful and inclusive environment for everyone.

## How to Contribute

### Reporting Bugs

Before creating a bug report:
- Check the [issue tracker](https://github.com/Timwood0x10/ares/issues) to avoid duplicates
- Verify the bug exists in the latest version
- Use the [bug report template](.github/ISSUE_TEMPLATE/bug_report.md) when creating issues

### Suggesting Features

We welcome feature suggestions! Please:
- Use the [feature request template](.github/ISSUE_TEMPLATE/feature_request.md)
- Describe the feature in detail
- Explain the motivation and use cases

### Pull Requests

1. **Fork the repository** and create your branch from `master`

2. **Follow coding standards** as defined in `plan/code_rules.md`

3. **Write meaningful commit messages**:
   ```
   feat(memory): add cosine similarity optimization
   fix(circuit-breaker): resolve race condition in half-open state
   docs(readme): update installation instructions
   ```

4. **Ensure all tests pass**:
   ```bash
   # Run unit tests
   go test -race -short ./...

   # Run linter
   golangci-lint run ./...

   # Build
   go build ./...
   ```

5. **Update documentation** if needed

6. **Submit a pull request** using the [PR template](.github/PULL_REQUEST_TEMPLATE/pull_request_template.md)

## Development Setup

### Prerequisites

- Go 1.21+
- PostgreSQL 15+ with pgvector extension
- Docker (for local development)
- Git

### Local Development

1. **Clone the repository**:
   ```bash
   git clone https://github.com/Timwood0x10/ares.git
   cd ares
   ```

2. **Start PostgreSQL with pgvector**:
   ```bash
   docker run -d --name ares-db \
     -e POSTGRES_PASSWORD=postgres \
     -e POSTGRES_DB=ARES \
     -p 5433:5432 \
     pgvector/pgvector:pg16

   # Enable pgvector extension
   docker exec -it ares-db psql -U postgres -d ARES -c "CREATE EXTENSION IF NOT EXISTS vector;"
   ```

3. **Run database migrations**:
   ```bash
   go run cmd/migrate_ares/main.go
   ```

4. **Run tests**:
   ```bash
   go test -race -short ./...
   ```

5. **Run examples**: the demos live under `examples/_fixtures/` — pick one and run it with:
   ```bash
   go run ./examples/_fixtures/<demo-name>
   ```

## Coding Standards

### Go Conventions

- Follow [Effective Go](https://go.dev/doc/effective_go)
- Run `go fmt` before committing
- Run `golangci-lint` for code quality
- Write idiomatic Go code

### Concurrency

- Always use mutexes or atomic operations for shared state
- Use `errgroup` for managing goroutine groups
- Avoid race conditions - run tests with `-race` flag

### Error Handling

- Return errors instead of logging and continuing
- Use the `errors` package for wrapping errors
- Provide context in error messages

### Testing

- Write unit tests for all new functionality
- Aim for meaningful test coverage
- Use table-driven tests where appropriate
- Mark integration tests with build tags

## Project Structure

```
ares/
├── api/                    # API layer
├── cmd/                    # Command-line tools
├── configs/                # Configuration files
├── docs/                   # Documentation
├── examples/               # Example applications
├── internal/               # Core implementation
│   ├── agents/            # Agent system
│   ├── memory/            # Memory management
│   ├── storage/           # Database layer
│   └── workflow/          # Workflow engine
├── services/               # External services
└── .github/               # GitHub configuration
```

## License

By contributing, you agree that your contributions will be licensed under the same license as the project.
