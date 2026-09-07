package ares_bootstrap

import (
	"context"
	"errors"
	"testing"

	"github.com/Timwood0x10/ares/internal/aresrecovery"
	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
)

// TestNewIPCProtocolPolicySourceNilStore verifies a nil store yields nil (skip).
func TestNewIPCProtocolPolicySourceNilStore(t *testing.T) {
	if got := NewIPCProtocolPolicySource(nil); got != nil {
		t.Fatalf("nil store must yield nil source, got %v", got)
	}
}

// TestActiveIPCProtocolPolicyDefaults verifies no active strategy yields the
// plain-json default (backward compatible).
func TestActiveIPCProtocolPolicyDefaults(t *testing.T) {
	src := NewIPCProtocolPolicySource(&stubStrategyStore{})
	if src == nil {
		t.Fatal("non-nil store must yield a source")
	}
	policy, err := src.ActiveIPCProtocolPolicy(context.Background())
	if err != nil {
		t.Fatalf("ActiveIPCProtocolPolicy: %v", err)
	}
	if policy.Encoding != aresrecovery.WireJSON {
		t.Fatalf("default encoding must be json, got %q", policy.Encoding)
	}
}

// TestActiveIPCProtocolPolicyNoActiveStrategy verifies ErrNoActiveStrategy maps
// to the json default instead of surfacing as an error.
func TestActiveIPCProtocolPolicyNoActiveStrategy(t *testing.T) {
	src := NewIPCProtocolPolicySource(&stubStrategyStore{err: evolution.ErrNoActiveStrategy})
	policy, err := src.ActiveIPCProtocolPolicy(context.Background())
	if err != nil {
		t.Fatalf("ErrNoActiveStrategy must map to default, got %v", err)
	}
	if policy.Encoding != aresrecovery.WireJSON {
		t.Fatalf("default encoding must be json, got %q", policy.Encoding)
	}
}

// TestActiveIPCProtocolPolicyFromParams verifies the ipc.encoding and
// ipc.min_compress_size params are read from the active strategy.
func TestActiveIPCProtocolPolicyFromParams(t *testing.T) {
	src := NewIPCProtocolPolicySource(&stubStrategyStore{
		active: &evolution.Strategy{
			ID: "s1",
			Params: map[string]any{
				"ipc.encoding":          "json+gzip",
				"ipc.min_compress_size": float64(1024),
			},
		},
	})
	policy, err := src.ActiveIPCProtocolPolicy(context.Background())
	if err != nil {
		t.Fatalf("ActiveIPCProtocolPolicy: %v", err)
	}
	if policy.Encoding != aresrecovery.WireJSONGzip {
		t.Fatalf("want json+gzip, got %q", policy.Encoding)
	}
	if policy.MinCompressSize != 1024 {
		t.Fatalf("want min_compress_size=1024, got %d", policy.MinCompressSize)
	}
}

// TestActiveIPCProtocolPolicyErrors verifies malformed/unknown params surface
// as errors instead of being silently ignored.
func TestActiveIPCProtocolPolicyErrors(t *testing.T) {
	cases := []map[string]any{
		{"ipc.encoding": "protobuf"},
		{"ipc.encoding": 42},
		{"ipc.min_compress_size": "lots"},
	}
	for _, params := range cases {
		src := NewIPCProtocolPolicySource(&stubStrategyStore{
			active: &evolution.Strategy{ID: "s1", Params: params},
		})
		if _, err := src.ActiveIPCProtocolPolicy(context.Background()); err == nil {
			t.Fatalf("params %v must error, got nil", params)
		}
	}
}

// TestActiveIPCProtocolPolicyStoreErrorPropagates verifies a store failure
// surfaces instead of silently falling back to the default.
func TestActiveIPCProtocolPolicyStoreErrorPropagates(t *testing.T) {
	src := NewIPCProtocolPolicySource(&stubStrategyStore{err: errors.New("store down")})
	if _, err := src.ActiveIPCProtocolPolicy(context.Background()); err == nil {
		t.Fatal("store error must propagate")
	}
}

// TestIPCAdapterSatisfiesContract verifies the adapter implements the
// aresrecovery.IPCProtocolPolicySource contract.
func TestIPCAdapterSatisfiesContract(t *testing.T) {
	var _ aresrecovery.IPCProtocolPolicySource = (*evolutionIPCProtocolPolicySource)(nil)
}
