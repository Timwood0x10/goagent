package ahp

import (
	"encoding/json"
	"sync"

	"github.com/Timwood0x10/ares/internal/errors"
)

// Codec handles serialization and deserialization of AHP messages.
type Codec interface {
	Encode(msg *AHPMessage) ([]byte, error)
	Decode(data []byte) (*AHPMessage, error)
	EncodeMultiple(msgs []*AHPMessage) ([]byte, error)
	DecodeMultiple(data []byte) ([]*AHPMessage, error)
}

// JSONCodec is a JSON-based codec for AHP messages.
type JSONCodec struct{}

// NewJSONCodec creates a new JSONCodec.
func NewJSONCodec() *JSONCodec {
	return &JSONCodec{}
}

// Encode encodes a message to JSON bytes.
func (c *JSONCodec) Encode(msg *AHPMessage) ([]byte, error) {
	if msg == nil {
		return nil, errors.New("nil message")
	}
	return json.Marshal(msg)
}

// Decode decodes JSON bytes to a message.
func (c *JSONCodec) Decode(data []byte) (*AHPMessage, error) {
	if len(data) == 0 {
		return nil, errors.New("empty data")
	}

	msg := &AHPMessage{}
	if err := json.Unmarshal(data, msg); err != nil {
		return nil, errors.Wrap(err, "decode failed")
	}
	return msg, nil
}

// EncodeMultiple encodes multiple messages to JSON bytes.
func (c *JSONCodec) EncodeMultiple(msgs []*AHPMessage) ([]byte, error) {
	if msgs == nil {
		return nil, errors.New("nil messages")
	}
	return json.Marshal(msgs)
}

// DecodeMultiple decodes JSON bytes to multiple messages.
func (c *JSONCodec) DecodeMultiple(data []byte) ([]*AHPMessage, error) {
	if len(data) == 0 {
		return nil, errors.New("empty data")
	}

	var msgs []*AHPMessage
	if err := json.Unmarshal(data, &msgs); err != nil {
		return nil, errors.Wrap(err, "decode failed")
	}
	return msgs, nil
}

// CodecRegistry manages available codecs.
type CodecRegistry struct {
	codecs map[string]Codec
	mu     sync.RWMutex
}

// NewCodecRegistry creates a new CodecRegistry.
func NewCodecRegistry() *CodecRegistry {
	return &CodecRegistry{
		codecs: make(map[string]Codec),
	}
}

// Register registers a codec with a name.
func (r *CodecRegistry) Register(name string, codec Codec) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.codecs[name] = codec
}

// Get returns a codec by name.
func (r *CodecRegistry) Get(name string) (Codec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	codec, ok := r.codecs[name]
	return codec, ok
}

// Default returns the default JSON codec.
func (r *CodecRegistry) Default() Codec {
	r.mu.RLock()
	c, ok := r.codecs["json"]
	r.mu.RUnlock()
	if ok {
		return c
	}
	return NewJSONCodec()
}

// InitDefaultCodecs registers the default codecs.
func (r *CodecRegistry) InitDefaultCodecs() {
	r.Register("json", NewJSONCodec())
}
