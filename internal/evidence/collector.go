// Package evidence provides the universal data primitive for ARES.
package evidence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// NewEvidence creates a new Evidence record with the current timestamp
// and a generated ID. This is the canonical way to produce evidence.
//
// Usage:
//
//	ev := evidence.NewEvidence("memory", evidence.KindKnowledge, payload,
//	    evidence.WithMetadata("task_id", taskID),
//	)
func NewEvidence(source string, kind EvidenceKind, payload any, opts ...EvidenceOption) Evidence {
	id := fmt.Sprintf("ev_%x", time.Now().UnixNano())
	var raw json.RawMessage
	if payload != nil {
		// Marshal failures are practically impossible for the map/struct
		// payloads producers pass, but the constructor's signature cannot
		// return an error (it is the canonical producer entry point): on
		// failure keep the zero RawMessage, which stores/queries already
		// treat as "no payload" — visible, not silently wrong.
		b, err := json.Marshal(payload)
		if err == nil {
			raw = b
		}
	}
	e := Evidence{
		ID:        id,
		Source:    source,
		Kind:      kind,
		Payload:   raw,
		Metadata:  make(map[string]string),
		Timestamp: time.Now(),
	}
	for _, opt := range opts {
		opt(&e)
	}
	return e
}

// EvidenceOption configures an Evidence record during construction.
type EvidenceOption func(*Evidence)

// WithMetadata adds a key-value pair to the evidence metadata.
func WithMetadata(key, value string) EvidenceOption {
	return func(e *Evidence) {
		if e.Metadata == nil {
			e.Metadata = make(map[string]string)
		}
		e.Metadata[key] = value
	}
}

// WithTTL sets the TTL for the evidence record.
func WithTTL(ttl time.Duration) EvidenceOption {
	return func(e *Evidence) {
		e.TTL = ttl
	}
}

// WithID overrides the auto-generated evidence ID.
func WithID(id string) EvidenceOption {
	return func(e *Evidence) {
		if id != "" {
			e.ID = id
		}
	}
}

// Collector is a convenience wrapper around Store that provides
// type-safe helpers for producing evidence from any subsystem.
//
// Usage:
//
//	collector := evidence.NewCollector(evStore, "memory")
//	collector.Emit(ctx, evidence.KindKnowledge, payload)
//	collector.EmitWithMeta(ctx, evidence.KindKnowledge, payload, "task_id", taskID)
type Collector struct {
	store  Store
	source string
}

// NewCollector creates a new evidence collector for a specific source.
// store is the evidence Store to write to; source identifies the producer
// (e.g. "memory", "flight", "chaos", "akf", "genome").
func NewCollector(store Store, source string) *Collector {
	return &Collector{store: store, source: source}
}

// Emit creates and stores an evidence record with the current timestamp.
func (c *Collector) Emit(ctx context.Context, kind EvidenceKind, payload any, opts ...EvidenceOption) error {
	if c.store == nil {
		return nil // silent no-op when no store is configured
	}
	opts = append(opts, WithID(""))
	e := NewEvidence(c.source, kind, payload, opts...)
	return c.store.Append(ctx, e)
}

// EmitWithMeta is a convenience method that creates evidence with metadata
// key-value pairs. Equivalent to calling Emit with WithMetadata options.
func (c *Collector) EmitWithMeta(ctx context.Context, kind EvidenceKind, payload any, keysAndValues ...string) error {
	if c.store == nil {
		return nil
	}
	opts := make([]EvidenceOption, 0, len(keysAndValues)/2)
	for i := 0; i+1 < len(keysAndValues); i += 2 {
		opts = append(opts, WithMetadata(keysAndValues[i], keysAndValues[i+1]))
	}
	return c.Emit(ctx, kind, payload, opts...)
}
