// Package ares_bootstrap — evolution IPC protocol policy adapter.
package ares_bootstrap

import (
	"context"
	"errors"
	"fmt"

	"github.com/Timwood0x10/ares/internal/aresrecovery"
	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
)

// IPC policy param keys read from the active evolution strategy's Params
// map. The evolution system evolves these values; the Kernel enforces them
// through aresrecovery.EvolutionAwareIPC (v0.3.0 M2-3).
const (
	// ipcEncodingParam ("ipc.encoding") selects the wire encoding: "json"
	// (default) or "json+gzip".
	ipcEncodingParam = "ipc.encoding"
	// ipcMinCompressSizeParam ("ipc.min_compress_size") compresses payloads
	// whose serialized size exceeds this threshold when encoding is
	// "json+gzip" (<= 0 means always compress).
	ipcMinCompressSizeParam = "ipc.min_compress_size"
)

// evolutionIPCProtocolPolicySource adapts an evolution.StrategyStore to the
// aresrecovery.IPCProtocolPolicySource contract (same pattern as
// strategy_adapter.go / spawn_policy_source.go).
type evolutionIPCProtocolPolicySource struct {
	store evolution.StrategyStore
}

// NewIPCProtocolPolicySource wraps an evolution StrategyStore as an
// aresrecovery.IPCProtocolPolicySource. Returns nil when the store is nil so
// callers can skip injection safely.
func NewIPCProtocolPolicySource(store evolution.StrategyStore) aresrecovery.IPCProtocolPolicySource {
	if store == nil {
		return nil
	}
	return &evolutionIPCProtocolPolicySource{store: store}
}

var _ aresrecovery.IPCProtocolPolicySource = (*evolutionIPCProtocolPolicySource)(nil)

// ActiveIPCProtocolPolicy derives the current IPC wire policy from the active
// evolution strategy's params. With no active strategy (or no IPC params) the
// policy defaults to plain JSON, preserving backward compatibility.
func (s *evolutionIPCProtocolPolicySource) ActiveIPCProtocolPolicy(ctx context.Context) (aresrecovery.IPCProtocolPolicy, error) {
	policy := aresrecovery.IPCProtocolPolicy{Encoding: aresrecovery.WireJSON}

	st, err := s.store.GetActive(ctx)
	if err != nil {
		if errors.Is(err, evolution.ErrNoActiveStrategy) {
			return policy, nil // no deployed strategy: plain json
		}
		return aresrecovery.IPCProtocolPolicy{}, fmt.Errorf("bootstrap IPC policy: active strategy: %w", err)
	}
	if st == nil {
		return policy, nil
	}

	if v, ok := st.Params[ipcEncodingParam]; ok {
		enc, err := asString(v)
		if err != nil {
			return aresrecovery.IPCProtocolPolicy{}, fmt.Errorf("bootstrap IPC policy: %s: %w", ipcEncodingParam, err)
		}
		if enc != aresrecovery.WireJSON && enc != aresrecovery.WireJSONGzip {
			return aresrecovery.IPCProtocolPolicy{}, fmt.Errorf("bootstrap IPC policy: unknown encoding %q", enc)
		}
		policy.Encoding = enc
	}
	if v, ok := st.Params[ipcMinCompressSizeParam]; ok {
		size, err := asInt(v)
		if err != nil {
			return aresrecovery.IPCProtocolPolicy{}, fmt.Errorf("bootstrap IPC policy: %s: %w", ipcMinCompressSizeParam, err)
		}
		policy.MinCompressSize = size
	}
	return policy, nil
}

// asString converts a strategy param to string.
func asString(v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("expected string, got %T (%v)", v, v)
	}
	return s, nil
}
