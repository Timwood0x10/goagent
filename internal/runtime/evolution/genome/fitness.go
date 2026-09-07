// Package genome provides score evaluation helpers for evolution.
package genome

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Timwood0x10/ares/internal/evidence"
)

// errNoEvidence is returned when a fitness query matches no evidence records.
// Genomes map this to a neutral 0.5 fitness so the GA keeps exploring instead
// of collapsing to an extreme value while evidence is still being collected.
var errNoEvidence = errors.New("genome: no evidence available for fitness")

// fitnessEvidence is the shared payload contract that evidence producers emit
// for the GA to consume. Each genome defines its own Source filter; the
// optional Value field carries a real measured metric (e.g. recovery success
// rate, memory hit rate, scheduler latency ratio). Genomes that need richer
// signals may decode a custom payload type instead.
type fitnessEvidence struct {
	// Value is a normalized metric in [0, 1], higher is better. Zero means the
	// evidence carries no numeric signal (genomes then fall back to 0.5).
	Value float64 `json:"value,omitempty"`
}

// queryEvidenceSince returns the evidence records matching the filter, limited
// to the trailing window. A zero window disables the time cut.
func queryEvidenceSince(ctx context.Context, store evidence.Store, source evidence.EvidenceKind, window time.Duration, limit int) ([]evidence.Evidence, error) {
	if store == nil {
		return nil, errNoEvidence
	}
	filter := evidence.Filter{
		Source: string(source),
		Limit:  limit,
	}
	if window > 0 {
		filter.Since = time.Now().Add(-window)
	}
	evs, err := store.Query(ctx, filter)
	if err != nil {
		return nil, err
	}
	return evs, nil
}

// avgFitnessValue extracts the normalized Value field from each evidence record
// and returns its mean in [0, 1]. Records whose payload has no numeric Value
// are skipped. Returns errNoEvidence when nothing is usable.
func avgFitnessValue(ctx context.Context, store evidence.Store, source string, window time.Duration, limit int) (float64, error) {
	evs, err := queryEvidenceSince(ctx, store, evidence.EvidenceKind(source), window, limit)
	if err != nil {
		return 0, err
	}
	var sum float64
	var count int
	for _, ev := range evs {
		if len(ev.Payload) == 0 {
			continue
		}
		var fe fitnessEvidence
		if err := json.Unmarshal(ev.Payload, &fe); err != nil {
			continue
		}
		if fe.Value < 0 || fe.Value > 1 {
			continue
		}
		sum += fe.Value
		count++
	}
	if count == 0 {
		return 0, errNoEvidence
	}
	return sum / float64(count), nil
}
