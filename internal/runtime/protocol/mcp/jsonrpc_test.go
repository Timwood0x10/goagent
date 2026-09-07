package ares_mcp

import (
	"encoding/json"
	"testing"
)

func TestNewRequest(t *testing.T) {
	tests := []struct {
		name    string
		id      int64
		method  string
		params  interface{}
		wantErr bool
	}{
		{
			name:   "simple request",
			id:     1,
			method: "initialize",
			params: map[string]string{"key": "value"},
		},
		{
			name:   "nil params",
			id:     2,
			method: "tools/list",
			params: nil,
		},
		{
			name:    "unmarshalable params",
			id:      3,
			method:  "test",
			params:  make(chan int),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, err := NewRequest(tt.id, tt.method, tt.params)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if msg.JSONRPC != JSONRPCVersion {
				t.Errorf("JSONRPC = %s, want %s", msg.JSONRPC, JSONRPCVersion)
			}
			if msg.ID == nil || *msg.ID != tt.id {
				t.Errorf("ID = %v, want %d", msg.ID, tt.id)
			}
			if msg.Method != tt.method {
				t.Errorf("Method = %s, want %s", msg.Method, tt.method)
			}
		})
	}
}

func TestNewNotification(t *testing.T) {
	msg, err := NewNotification("notifications/initialized", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.ID != nil {
		t.Error("notification should not have ID")
	}
	if msg.Method != "notifications/initialized" {
		t.Errorf("Method = %s, want notifications/initialized", msg.Method)
	}
}

func TestNewResponse(t *testing.T) {
	result := map[string]string{"status": "ok"}
	msg, err := NewResponse(42, result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.ID == nil || *msg.ID != 42 {
		t.Errorf("ID = %v, want 42", msg.ID)
	}
	if len(msg.Result) == 0 {
		t.Error("Result should not be empty")
	}
}

func TestNewErrorResponse(t *testing.T) {
	msg, err := NewErrorResponse(1, InvalidParams, "bad params", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.Error == nil {
		t.Fatal("Error should not be nil")
	}
	if msg.Error.Code != InvalidParams {
		t.Errorf("Error.Code = %d, want %d", msg.Error.Code, InvalidParams)
	}
}

func TestEncodeDecode(t *testing.T) {
	original, _ := NewRequest(1, "test", map[string]int{"a": 1})

	data, err := Encode(original)
	if err != nil {
		t.Fatalf("Encode error: %v", err)
	}

	decoded, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}

	if decoded.Method != original.Method {
		t.Errorf("Method = %s, want %s", decoded.Method, original.Method)
	}
}

func TestEncodeNilMessage(t *testing.T) {
	_, err := Encode(nil)
	if err == nil {
		t.Error("expected error for nil message")
	}
}

func TestDecodeEmptyData(t *testing.T) {
	_, err := Decode([]byte{})
	if err == nil {
		t.Error("expected error for empty data")
	}
}

func TestDecodeInvalidJSON(t *testing.T) {
	_, err := Decode([]byte("{invalid"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestIsRequest(t *testing.T) {
	msg := &JSONRPCMessage{Method: "test", ID: int64Ptr(1)}
	if !IsRequest(msg) {
		t.Error("expected true for request")
	}

	notif := &JSONRPCMessage{Method: "test"}
	if IsRequest(notif) {
		t.Error("expected false for notification")
	}
}

func TestIsNotification(t *testing.T) {
	notif := &JSONRPCMessage{Method: "test"}
	if !IsNotification(notif) {
		t.Error("expected true for notification")
	}

	req := &JSONRPCMessage{Method: "test", ID: int64Ptr(1)}
	if IsNotification(req) {
		t.Error("expected false for request")
	}
}

func TestIsResponse(t *testing.T) {
	resp := &JSONRPCMessage{ID: int64Ptr(1)}
	if !IsResponse(resp) {
		t.Error("expected true for response")
	}
}

func TestIsError(t *testing.T) {
	err := &JSONRPCMessage{Error: &JSONRPCError{Code: -1, Message: "err"}}
	if !IsError(err) {
		t.Error("expected true for error")
	}

	ok := &JSONRPCMessage{Result: json.RawMessage(`{}`)}
	if IsError(ok) {
		t.Error("expected false for non-error")
	}
}

func TestDecodeResult(t *testing.T) {
	type testResult struct {
		Name string `json:"name"`
	}

	result := json.RawMessage(`{"name":"test"}`)
	msg := &JSONRPCMessage{Result: result}

	var tr testResult
	if err := DecodeResult(msg, &tr); err != nil {
		t.Fatalf("DecodeResult error: %v", err)
	}
	if tr.Name != "test" {
		t.Errorf("Name = %s, want test", tr.Name)
	}
}

func TestDecodeResultError(t *testing.T) {
	msg := &JSONRPCMessage{
		Error: &JSONRPCError{Code: -1, Message: "fail"},
	}

	var tr map[string]any
	err := DecodeResult(msg, &tr)
	if err == nil {
		t.Error("expected error for error message")
	}
}

func TestDecodeParams(t *testing.T) {
	type testParams struct {
		Key string `json:"key"`
	}

	msg := &JSONRPCMessage{
		Params: json.RawMessage(`{"key":"value"}`),
	}

	var tp testParams
	if err := DecodeParams(msg, &tp); err != nil {
		t.Fatalf("DecodeParams error: %v", err)
	}
	if tp.Key != "value" {
		t.Errorf("Key = %s, want value", tp.Key)
	}
}

func TestJSONRPCError_Error(t *testing.T) {
	e := &JSONRPCError{Code: -32600, Message: "invalid request"}
	expected := "jsonrpc error -32600: invalid request"
	if e.Error() != expected {
		t.Errorf("Error() = %s, want %s", e.Error(), expected)
	}
}

func TestIDGenerator(t *testing.T) {
	var gen IDGenerator
	ids := make(map[int64]bool)
	for i := 0; i < 100; i++ {
		id := gen.Next()
		if ids[id] {
			t.Errorf("duplicate ID: %d", id)
		}
		ids[id] = true
	}
}

func int64Ptr(v int64) *int64 {
	return &v
}
