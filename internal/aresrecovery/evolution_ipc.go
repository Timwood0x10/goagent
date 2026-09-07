package aresrecovery

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/Timwood0x10/ares/internal/agentipc"
)

// Evolution-driven IPC protocol: the Evolution system chooses
// the message wire format and compression policy at runtime. The wrapper
// applies the policy when sending through the underlying IPC bus — "Evolution
// decides; Kernel enforces", same as the spawn and quota managers.
//
// The wrapper depends on an IPCProtocolPolicySource (defined here, at the
// consumer) so aresrecovery never imports the evolution package.

// IPCProtocolPolicy is the evolution-produced message encoding decision.
type IPCProtocolPolicy struct {
	// Encoding selects the wire encoding: "json" (default) or "json+gzip".
	Encoding string
	// MinCompressSize compresses payloads whose serialized size exceeds this
	// threshold when Encoding == "json+gzip". <= 0 means always compress.
	MinCompressSize int
}

// WireEncoding constants for IPCProtocolPolicy.Encoding.
const (
	// WireJSON is the plain JSON encoding (default, backward compatible).
	WireJSON = "json"
	// WireJSONGzip is JSON + gzip compression.
	WireJSONGzip = "json+gzip"
)

// IPCProtocolPolicySource supplies the current evolution IPC policy.
type IPCProtocolPolicySource interface {
	// ActiveIPCProtocolPolicy returns the currently deployed IPC policy.
	ActiveIPCProtocolPolicy(ctx context.Context) (IPCProtocolPolicy, error)
}

// WireMessage is what actually travels through the bus when the evolution
// policy applies an encoding. Plain payloads pass through unchanged (zero
// value of WireMessage is never produced for them).
type WireMessage struct {
	// Encoded is the serialized payload (JSON, or gzip-compressed JSON when
	// Compressed is true).
	Encoded []byte `json:"encoded"`
	// Compressed reports whether Encoded is gzip-compressed.
	Compressed bool `json:"compressed"`
}

// EvolutionAwareIPC wraps the IPC bus and applies the evolution message
// policy on Send. The receiver sees a *WireMessage for policy-
// encoded sends and the raw payload otherwise — Decode recovers the original.
type EvolutionAwareIPC struct {
	bus    *agentipc.Bus
	source IPCProtocolPolicySource
}

// NewEvolutionAwareIPC wires the IPC wrapper to the bus and the evolution
// policy source.
//
// Args:
//   - bus: the underlying IPC bus.
//   - source: the evolution IPC policy source (may be nil → plain send).
//
// Returns:
//   - *EvolutionAwareIPC: ready to Send.
func NewEvolutionAwareIPC(bus *agentipc.Bus, source IPCProtocolPolicySource) *EvolutionAwareIPC {
	return &EvolutionAwareIPC{bus: bus, source: source}
}

// Send encodes the payload per the evolution policy and delivers it through
// the bus. When the policy is WireJSONGzip and the serialized payload exceeds
// MinCompressSize, the payload is gzip-compressed and wrapped in a
// *WireMessage; otherwise the raw payload is sent (backward compatible).
//
// Args:
//   - ctx: for the policy lookup.
//   - from / to / topic: the bus delivery arguments.
//   - payload: the message body (any JSON-serializable value).
//
// Returns:
//   - error: the policy-source error, marshal error, or the bus error.
func (i *EvolutionAwareIPC) Send(ctx context.Context, from, to, topic string, payload any) error {
	if i.bus == nil {
		return errors.New("aresrecovery: evolution IPC has no bus")
	}
	policy := IPCProtocolPolicy{Encoding: WireJSON}
	if i.source != nil {
		p, err := i.source.ActiveIPCProtocolPolicy(ctx)
		if err != nil {
			return fmt.Errorf("evolution IPC policy: %w", err)
		}
		policy = p
	}
	if policy.Encoding != WireJSONGzip {
		// Plain send: raw payload, unchanged behavior.
		return i.bus.Send(ctx, from, to, topic, payload)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("evolution IPC marshal: %w", err)
	}
	if policy.MinCompressSize > 0 && len(encoded) <= policy.MinCompressSize {
		// Below the threshold: send as a WireMessage with no compression so
		// the receiver always decodes via the same path.
		return i.bus.Send(ctx, from, to, topic, &WireMessage{Encoded: encoded, Compressed: false})
	}
	compressed, err := gzipBytes(encoded)
	if err != nil {
		return fmt.Errorf("evolution IPC compress: %w", err)
	}
	return i.bus.Send(ctx, from, to, topic, &WireMessage{Encoded: compressed, Compressed: true})
}

// Decode restores the original payload from a WireMessage (or returns the
// input unchanged when it is not a WireMessage — i.e. a plain send).
//
// Args:
//   - msg: the received message payload.
//
// Returns:
//   - any: the original payload.
//   - error: when the wire message cannot be decoded.
func Decode(msg any) (any, error) {
	wire, ok := msg.(*WireMessage)
	if !ok {
		return msg, nil
	}
	raw := wire.Encoded
	if wire.Compressed {
		dec, err := gunzipBytes(raw)
		if err != nil {
			return nil, fmt.Errorf("evolution IPC gunzip: %w", err)
		}
		raw = dec
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("evolution IPC decode: %w", err)
	}
	return out, nil
}

// gzipBytes compresses data with gzip (best speed is a fine default for IPC).
func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// gunzipBytes decompresses data produced by gzipBytes.
func gunzipBytes(data []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()
	return io.ReadAll(zr)
}

// Bus exposes the underlying agentipc bus (used by ops tooling and tests to
// submit collaboration requests exactly as remote peers do).
func (e *EvolutionAwareIPC) Bus() *agentipc.Bus { return e.bus }
