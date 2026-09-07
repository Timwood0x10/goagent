// package mcp - provides MCP (Model Context Protocol) client integration for ares.
package ares_mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
)

// JSONRPCVersion is the JSON-RPC protocol version string.
const JSONRPCVersion = "2.0"

// JSONRPCMessage represents a JSON-RPC 2.0 message.
type JSONRPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError represents a JSON-RPC 2.0 error object.
type JSONRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error implements the error interface.
func (e *JSONRPCError) Error() string {
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// Standard JSON-RPC error codes.
const (
	ParseError     = -32700
	InvalidRequest = -32600
	MethodNotFound = -32601
	InvalidParams  = -32602
	InternalError  = -32603
)

// IDGenerator generates unique JSON-RPC request IDs.
type IDGenerator struct {
	counter atomic.Int64
}

// Next returns the next unique ID.
func (g *IDGenerator) Next() int64 {
	return g.counter.Add(1)
}

// NewRequest creates a JSON-RPC request message.
func NewRequest(id int64, method string, params interface{}) (*JSONRPCMessage, error) {
	var rawParams json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal params: %w", err)
		}
		rawParams = data
	}

	return &JSONRPCMessage{
		JSONRPC: JSONRPCVersion,
		ID:      &id,
		Method:  method,
		Params:  rawParams,
	}, nil
}

// NewNotification creates a JSON-RPC notification (no ID, no response expected).
func NewNotification(method string, params interface{}) (*JSONRPCMessage, error) {
	var rawParams json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal params: %w", err)
		}
		rawParams = data
	}

	return &JSONRPCMessage{
		JSONRPC: JSONRPCVersion,
		Method:  method,
		Params:  rawParams,
	}, nil
}

// NewResponse creates a JSON-RPC success response.
func NewResponse(id int64, result interface{}) (*JSONRPCMessage, error) {
	rawResult, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal result: %w", err)
	}

	return &JSONRPCMessage{
		JSONRPC: JSONRPCVersion,
		ID:      &id,
		Result:  rawResult,
	}, nil
}

// NewErrorResponse creates a JSON-RPC error response.
func NewErrorResponse(id int64, code int, message string, data interface{}) (*JSONRPCMessage, error) {
	var rawData json.RawMessage
	if data != nil {
		d, err := json.Marshal(data)
		if err != nil {
			return nil, fmt.Errorf("marshal error data: %w", err)
		}
		rawData = d
	}

	return &JSONRPCMessage{
		JSONRPC: JSONRPCVersion,
		ID:      &id,
		Error: &JSONRPCError{
			Code:    code,
			Message: message,
			Data:    rawData,
		},
	}, nil
}

// Encode serializes a JSON-RPC message to bytes.
func Encode(msg *JSONRPCMessage) ([]byte, error) {
	if msg == nil {
		return nil, errors.New("nil message")
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("encode message: %w", err)
	}
	return data, nil
}

// Decode deserializes a JSON-RPC message from bytes.
func Decode(data []byte) (*JSONRPCMessage, error) {
	if len(data) == 0 {
		return nil, errors.New("empty data")
	}
	var msg JSONRPCMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("decode message: %w", err)
	}
	return &msg, nil
}

// IsRequest returns true if the message is a request (has method and ID).
func IsRequest(msg *JSONRPCMessage) bool {
	return msg != nil && msg.Method != "" && msg.ID != nil
}

// IsNotification returns true if the message is a notification (has method, no ID).
func IsNotification(msg *JSONRPCMessage) bool {
	return msg != nil && msg.Method != "" && msg.ID == nil
}

// IsResponse returns true if the message is a response (has ID, no method).
func IsResponse(msg *JSONRPCMessage) bool {
	return msg != nil && msg.ID != nil && msg.Method == ""
}

// IsError returns true if the message is an error response.
func IsError(msg *JSONRPCMessage) bool {
	return msg != nil && msg.Error != nil
}

// DecodeResult decodes the Result field into the given target.
func DecodeResult(msg *JSONRPCMessage, target interface{}) error {
	if msg == nil {
		return errors.New("nil message")
	}
	if msg.Error != nil {
		return msg.Error
	}
	if len(msg.Result) == 0 {
		return errors.New("empty result")
	}
	if err := json.Unmarshal(msg.Result, target); err != nil {
		return fmt.Errorf("decode result: %w", err)
	}
	return nil
}

// DecodeParams decodes the Params field into the given target.
func DecodeParams(msg *JSONRPCMessage, target interface{}) error {
	if msg == nil {
		return errors.New("nil message")
	}
	if len(msg.Params) == 0 {
		return errors.New("empty params")
	}
	if err := json.Unmarshal(msg.Params, target); err != nil {
		return fmt.Errorf("decode params: %w", err)
	}
	return nil
}
