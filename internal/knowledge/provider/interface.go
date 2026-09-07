package provider

import (
	"context"

	"github.com/Timwood0x10/ares/internal/knowledge"
)

// GraphProvider converts external data sources into KnowledgeObject streams.
//
// Stream mode: Instead of loading all objects into memory (which would OOM
// for 10M-order tables), providers emit objects one at a time through a
// channel. The caller (KnowledgeRuntime) can process objects incrementally
// and cancel via context at any point.
type GraphProvider interface {
	// Name returns a unique identifier for this provider instance.
	Name() string

	// IntentMatch returns a match score [0, 1] indicating how relevant this
	// provider is for the given intent. Used by SourceDiscovery to select
	// the best providers for a task. Providers that don't match at all
	// should return 0 to avoid being selected.
	IntentMatch(intent knowledge.Intent) float64

	// Stream delivers KnowledgeObjects one at a time through the channel.
	// The provider must close the channel when done. If ctx is cancelled,
	// the provider should stop producing and return immediately.
	// Errors during streaming are sent through the error channel.
	//
	// Consumption contract: callers MUST drain both channels until they
	// are closed, or cancel ctx when abandoning early. Producers select on
	// ctx.Done while sending, so cancellation always releases them; a caller
	// that stops consuming WITHOUT cancelling would block the producer
	// goroutine on a full channel buffer forever. The runtime's only
	// production consumer drains unconditionally (runtime.go) and providers
	// close both channels via defer on exit.
	Stream(ctx context.Context, intent knowledge.Intent) (<-chan *knowledge.KnowledgeObject, <-chan error)
}

// ProviderType classifies a GraphProvider by its backing data source kind.
// SourceDiscovery uses it to route query planning (e.g. SQL for relational
// providers, vector for embedding stores, memory for task history).
type ProviderType string

const (
	// ProviderMemory wraps a task-similarity searcher (past executions).
	ProviderMemory ProviderType = "memory"
	// ProviderEvolution wraps an evolution strategy store.
	ProviderEvolution ProviderType = "evolution"
	// ProviderPostgres reads rows from a PostgreSQL table.
	ProviderPostgres ProviderType = "postgres"
	// ProviderVector queries a vector store for semantic similarity.
	ProviderVector ProviderType = "vector"
	// ProviderStore recalls AKG-distilled objects from a KnowledgeStore.
	ProviderStore ProviderType = "store"
	// ProviderCode indexes a source tree (AST-level).
	ProviderCode ProviderType = "code"
)

// TypedProvider is optionally implemented by GraphProviders to expose their
// backing data source type. SourceDiscovery detects it via type assertion so
// the planner stays decoupled from concrete provider packages; providers that
// do not implement it are treated as unknown ("") by detectProviderType.
type TypedProvider interface {
	ProviderType() ProviderType
}

// ProviderConfig is a generic configuration for database-backed providers.
// Specific providers (PG, MySQL, etc.) extend this with their own connection params.
type ProviderConfig struct {
	Name       string        `yaml:"name"`
	Namespace  string        `yaml:"namespace"`
	IntentTags []string      `yaml:"intent_tags"`
	Mapping    ColumnMapping `yaml:"mapping"`
	// Table is the source table (or view) name used by SQL-based providers
	// when building SELECT queries. Required for providers that issue SQL
	// against an external database.
	Table string `yaml:"table"`
}

// ColumnMapping defines how a database table maps to KnowledgeObject fields.
// For SQL-based providers, this drives the SELECT query construction.
type ColumnMapping struct {
	IDColumn      string `yaml:"id_column"`
	SummaryColumn string `yaml:"summary_column"`
	ContentColumn string `yaml:"content_column,omitempty"`
	TagColumn     string `yaml:"tag_column,omitempty"`
	TimeColumn    string `yaml:"time_column,omitempty"`
}
