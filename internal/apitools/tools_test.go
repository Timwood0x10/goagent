package apitools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRegistry(t *testing.T) {
	r := NewRegistry()
	if r == nil {
		t.Fatal("NewRegistry returned nil")
	}
	// Registry should have built-in tools pre-populated.
	if len(r.List()) == 0 {
		t.Fatal("New registry should have built-in tools pre-populated")
	}
}

func TestRegisterNilTool(t *testing.T) {
	r := NewEmptyRegistry()
	err := r.Register(nil)
	if err == nil {
		t.Fatal("expected error for nil tool")
	}
}

func TestRegistryRegisterGetListUnregister(t *testing.T) {
	r := NewEmptyRegistry()
	ctx := context.Background()

	t1 := ToolFunc{ToolName: "tool1", ToolDesc: "first", Fn: func(ctx context.Context, params map[string]any) (any, error) {
		return "one", nil
	}}
	t2 := ToolFunc{ToolName: "tool2", ToolDesc: "second", Fn: func(ctx context.Context, params map[string]any) (any, error) {
		return "two", nil
	}}

	if err := r.Register(t1); err != nil {
		t.Fatalf("register t1: %v", err)
	}
	if err := r.Register(t2); err != nil {
		t.Fatalf("register t2: %v", err)
	}

	got, ok := r.Get("tool1")
	if !ok {
		t.Fatal("tool1 not found")
	}
	if got.Name() != "tool1" {
		t.Fatalf("got name %q, want tool1", got.Name())
	}

	names := r.List()
	sort.Strings(names)
	if len(names) != 2 || names[0] != "tool1" || names[1] != "tool2" {
		t.Fatalf("unexpected list: %v", names)
	}

	_ = r.Unregister("tool1")
	if _, ok := r.Get("tool1"); ok {
		t.Fatal("tool1 should be unregistered")
	}
	if len(r.List()) != 1 {
		t.Fatal("expected 1 tool after unregister")
	}

	result, err := r.Execute(ctx, "tool2", nil)
	if err != nil {
		t.Fatalf("execute tool2: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
}

func TestExecuteUnknownTool(t *testing.T) {
	r := NewRegistry()
	_, err := r.Execute(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
}

func TestListTools(t *testing.T) {
	r := NewEmptyRegistry()
	_ = r.Register(ToolFunc{ToolName: "alpha", ToolDesc: "a desc"})
	_ = r.Register(ToolFunc{ToolName: "beta", ToolDesc: "b desc"})

	infos := r.ListTools()
	if len(infos) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(infos))
	}
	m := map[string]string{}
	for _, info := range infos {
		m[info.Name] = info.Description
	}
	if m["alpha"] != "a desc" {
		t.Fatalf("alpha desc: got %q", m["alpha"])
	}
	if m["beta"] != "b desc" {
		t.Fatalf("beta desc: got %q", m["beta"])
	}
}

func TestToolFuncNameDescription(t *testing.T) {
	tf := ToolFunc{ToolName: "mytool", ToolDesc: "my description"}
	if tf.Name() != "mytool" {
		t.Fatalf("Name: got %q", tf.Name())
	}
	if tf.Description() != "my description" {
		t.Fatalf("Description: got %q", tf.Description())
	}
}

func TestToolFuncExecuteSuccess(t *testing.T) {
	tf := ToolFunc{
		ToolName: "test",
		Fn: func(_ context.Context, params map[string]any) (any, error) {
			return params["key"], nil
		},
	}
	result, err := tf.Execute(context.Background(), map[string]any{"key": "val"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	if result.Data != "val" {
		t.Fatalf("got data %v, want val", result.Data)
	}
}

func TestToolFuncExecuteFailure(t *testing.T) {
	tf := ToolFunc{
		ToolName: "fail",
		Fn: func(_ context.Context, params map[string]any) (any, error) {
			return nil, fmt.Errorf("oops")
		},
	}
	result, err := tf.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute should not return error for function errors: %v", err)
	}
	if result.Success {
		t.Fatal("expected success=false")
	}
	if result.Data != "oops" {
		t.Fatalf("expected data 'oops', got %v", result.Data)
	}
}

// ── calculatorTool ──────────────────────────────────────────

func TestCalculatorToolNameDescription(t *testing.T) {
	ct := &calculatorTool{}
	if ct.Name() != "calculator" {
		t.Fatalf("Name: %q", ct.Name())
	}
	if ct.Description() == "" {
		t.Fatal("Description should not be empty")
	}
}

func TestCalculatorToolMissingParams(t *testing.T) {
	ct := &calculatorTool{}
	result, err := ct.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected success=false")
	}
	if result.Data != "expression is required" {
		t.Fatalf("got %v", result.Data)
	}
}

func TestCalculatorToolSimple(t *testing.T) {
	ct := &calculatorTool{}
	tests := []struct {
		expr   string
		expect float64
	}{
		{"2+2", 4},
		{"3*4", 12},
		{"10/2", 5},
		{"2-7", -5},
	}
	for _, tt := range tests {
		result, err := ct.Execute(context.Background(), map[string]any{"expression": tt.expr})
		if err != nil {
			t.Fatalf("%s: %v", tt.expr, err)
		}
		if !result.Success {
			t.Fatalf("%s: unexpected failure: %v", tt.expr, result.Data)
		}
		data := result.Data.(map[string]any)
		got := data["result"].(float64)
		if math.Abs(got-tt.expect) > 1e-9 {
			t.Fatalf("%s: got %f, want %f", tt.expr, got, tt.expect)
		}
	}
}

func TestCalculatorToolComplex(t *testing.T) {
	ct := &calculatorTool{}
	tests := []struct {
		expr   string
		expect float64
	}{
		{"(1+2)*3", 9},
		{"2*3+4*5", 26},
		{"2*(3+4)", 14},
	}
	for _, tt := range tests {
		result, err := ct.Execute(context.Background(), map[string]any{"expression": tt.expr})
		if err != nil {
			t.Fatalf("%s: %v", tt.expr, err)
		}
		if !result.Success {
			t.Fatalf("%s: unexpected failure: %v", tt.expr, result.Data)
		}
		data := result.Data.(map[string]any)
		got := data["result"].(float64)
		if math.Abs(got-tt.expect) > 1e-9 {
			t.Fatalf("%s: got %f, want %f", tt.expr, got, tt.expect)
		}
	}
}

func TestCalculatorToolDivisionByZero(t *testing.T) {
	ct := &calculatorTool{}
	result, err := ct.Execute(context.Background(), map[string]any{"expression": "1/0"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected success=false")
	}
	if result.Data != "division by zero" {
		t.Fatalf("got %v", result.Data)
	}
}

func TestCalculatorToolInvalidExpression(t *testing.T) {
	ct := &calculatorTool{}
	result, err := ct.Execute(context.Background(), map[string]any{"expression": "foo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected success=false")
	}
}

func TestCalculatorToolConstants(t *testing.T) {
	ct := &calculatorTool{}

	result, err := ct.Execute(context.Background(), map[string]any{"expression": "pi"})
	if err != nil {
		t.Fatalf("pi: %v", err)
	}
	if !result.Success {
		t.Fatalf("pi: %v", result.Data)
	}
	data := result.Data.(map[string]any)
	got := data["result"].(float64)
	if math.Abs(got-math.Pi) > 1e-9 {
		t.Fatalf("pi: got %f, want %f", got, math.Pi)
	}

	result, err = ct.Execute(context.Background(), map[string]any{"expression": "e"})
	if err != nil {
		t.Fatalf("e: %v", err)
	}
	if !result.Success {
		t.Fatalf("e: %v", result.Data)
	}
	data = result.Data.(map[string]any)
	got = data["result"].(float64)
	if math.Abs(got-math.E) > 1e-9 {
		t.Fatalf("e: got %f, want %f", got, math.E)
	}
}

func TestCalculatorToolUnaryMinus(t *testing.T) {
	ct := &calculatorTool{}

	result, err := ct.Execute(context.Background(), map[string]any{"expression": "-5"})
	if err != nil {
		t.Fatalf("-5: %v", err)
	}
	if !result.Success {
		t.Fatalf("-5: %v", result.Data)
	}
	data := result.Data.(map[string]any)
	got := data["result"].(float64)
	if math.Abs(got+5) > 1e-9 {
		t.Fatalf("-5: got %f", got)
	}

	result, err = ct.Execute(context.Background(), map[string]any{"expression": "-(3+2)"})
	if err != nil {
		t.Fatalf("-(3+2): %v", err)
	}
	if !result.Success {
		t.Fatalf("-(3+2): %v", result.Data)
	}
	data = result.Data.(map[string]any)
	got = data["result"].(float64)
	if math.Abs(got+5) > 1e-9 {
		t.Fatalf("-(3+2): got %f", got)
	}
}

func TestCalculatorToolWhitespace(t *testing.T) {
	ct := &calculatorTool{}
	result, err := ct.Execute(context.Background(), map[string]any{"expression": "  2  +  2  "})
	if err != nil {
		t.Fatalf("whitespace: %v", err)
	}
	if !result.Success {
		t.Fatalf("whitespace: %v", result.Data)
	}
	data := result.Data.(map[string]any)
	got := data["result"].(float64)
	if math.Abs(got-4) > 1e-9 {
		t.Fatalf("whitespace: got %f, want 4", got)
	}
}

// ── regexTool ───────────────────────────────────────────────

func TestRegexToolNameDescription(t *testing.T) {
	rt := &regexTool{}
	if rt.Name() != "regex" {
		t.Fatalf("Name: %q", rt.Name())
	}
	if rt.Description() == "" {
		t.Fatal("Description should not be empty")
	}
}

func TestRegexToolMissingParams(t *testing.T) {
	rt := &regexTool{}

	result, err := rt.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected success=false")
	}
	if result.Data != "pattern is required" {
		t.Fatalf("got %v", result.Data)
	}

	result, err = rt.Execute(context.Background(), map[string]any{"pattern": "foo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected success=false")
	}
	if result.Data != "text is required" {
		t.Fatalf("got %v", result.Data)
	}
}

func TestRegexToolMatch(t *testing.T) {
	rt := &regexTool{}

	result, err := rt.Execute(context.Background(), map[string]any{
		"pattern":   `\d+`,
		"text":      "abc 123 def 456",
		"operation": "match",
	})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if !result.Success {
		t.Fatalf("match: %v", result.Data)
	}
	data := result.Data.(map[string]any)
	if data["matched"].(bool) != true {
		t.Fatal("expected matched=true")
	}
	if data["match_count"].(int) != 2 {
		t.Fatalf("expected 2 matches, got %d", data["match_count"])
	}

	result, err = rt.Execute(context.Background(), map[string]any{
		"pattern":   `\d+`,
		"text":      "abc def",
		"operation": "match",
	})
	if err != nil {
		t.Fatalf("no match: %v", err)
	}
	if !result.Success {
		t.Fatalf("no match: %v", result.Data)
	}
	data = result.Data.(map[string]any)
	if data["matched"].(bool) != false {
		t.Fatal("expected matched=false")
	}
	if data["match_count"].(int) != 0 {
		t.Fatalf("expected 0 matches")
	}
}

func TestRegexToolExtract(t *testing.T) {
	rt := &regexTool{}
	result, err := rt.Execute(context.Background(), map[string]any{
		"pattern":   `(\w+)@(\w+)\.(\w+)`,
		"text":      "user@example.com",
		"operation": "extract",
	})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !result.Success {
		t.Fatalf("extract: %v", result.Data)
	}
	data := result.Data.(map[string]any)
	if data["matched"].(bool) != true {
		t.Fatal("expected matched=true")
	}
	if data["match_count"].(int) != 1 {
		t.Fatalf("expected 1 match, got %d", data["match_count"])
	}
}

func TestRegexToolReplace(t *testing.T) {
	rt := &regexTool{}
	result, err := rt.Execute(context.Background(), map[string]any{
		"pattern":     `world`,
		"text":        "hello world",
		"replacement": "there",
		"operation":   "replace",
	})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if !result.Success {
		t.Fatalf("replace: %v", result.Data)
	}
	data := result.Data.(map[string]any)
	if data["result"].(string) != "hello there" {
		t.Fatalf("got %q, want 'hello there'", data["result"])
	}
}

func TestRegexToolInvalidRegex(t *testing.T) {
	rt := &regexTool{}
	result, err := rt.Execute(context.Background(), map[string]any{
		"pattern":   `[invalid`,
		"text":      "test",
		"operation": "match",
	})
	if err != nil {
		t.Fatalf("invalid regex: %v", err)
	}
	if result.Success {
		t.Fatal("expected success=false")
	}
}

func TestRegexToolUnknownOperation(t *testing.T) {
	rt := &regexTool{}
	result, err := rt.Execute(context.Background(), map[string]any{
		"pattern":   `\d+`,
		"text":      "123",
		"operation": "unknown_op",
	})
	if err != nil {
		t.Fatalf("unknown op: %v", err)
	}
	if result.Success {
		t.Fatal("expected success=false")
	}
	if result.Data != "unknown operation: unknown_op" {
		t.Fatalf("got %v", result.Data)
	}
}

// ── jsonTool ─────────────────────────────────────────────────

func TestJsonToolNameDescription(t *testing.T) {
	jt := &jsonTool{}
	if jt.Name() != "json_tools" {
		t.Fatalf("Name: %q", jt.Name())
	}
	if jt.Description() == "" {
		t.Fatal("Description should not be empty")
	}
}

func TestJsonToolParse(t *testing.T) {
	jt := &jsonTool{}

	result, err := jt.Execute(context.Background(), map[string]any{
		"input":     `{"a":1,"b":[2,3]}`,
		"operation": "parse",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !result.Success {
		t.Fatalf("parse: %v", result.Data)
	}
	data := result.Data.(map[string]any)
	if data["operation"] != "parse" {
		t.Fatalf("expected parse op, got %v", data["operation"])
	}

	result, err = jt.Execute(context.Background(), map[string]any{
		"input":     `invalid json`,
		"operation": "parse",
	})
	if err != nil {
		t.Fatalf("parse invalid: %v", err)
	}
	if result.Success {
		t.Fatal("expected success=false")
	}
}

func TestJsonToolValidate(t *testing.T) {
	jt := &jsonTool{}

	result, err := jt.Execute(context.Background(), map[string]any{
		"input":     `{"valid": true}`,
		"operation": "validate",
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !result.Success {
		t.Fatalf("validate: %v", result.Data)
	}
	data := result.Data.(map[string]any)
	if data["valid"].(bool) != true {
		t.Fatal("expected valid=true")
	}

	result, err = jt.Execute(context.Background(), map[string]any{
		"input":     `{invalid}`,
		"operation": "validate",
	})
	if err != nil {
		t.Fatalf("validate invalid: %v", err)
	}
	if !result.Success {
		t.Fatalf("validate invalid: %v", result.Data)
	}
	data = result.Data.(map[string]any)
	if data["valid"].(bool) != false {
		t.Fatal("expected valid=false")
	}
	if data["error"] == "" {
		t.Fatal("expected error message")
	}
}

func TestJsonToolPrettify(t *testing.T) {
	jt := &jsonTool{}
	result, err := jt.Execute(context.Background(), map[string]any{
		"input":     `{"b":2,"a":1}`,
		"operation": "prettify",
	})
	if err != nil {
		t.Fatalf("prettify: %v", err)
	}
	if !result.Success {
		t.Fatalf("prettify: %v", result.Data)
	}
	data := result.Data.(map[string]any)
	var parsed any
	if err := json.Unmarshal([]byte(data["result"].(string)), &parsed); err != nil {
		t.Fatalf("prettify result not valid JSON: %v", err)
	}

	result, err = jt.Execute(context.Background(), map[string]any{
		"input":     `invalid`,
		"operation": "prettify",
	})
	if err != nil {
		t.Fatalf("prettify invalid: %v", err)
	}
	if result.Success {
		t.Fatal("expected success=false")
	}
}

func TestJsonToolUnknownOperation(t *testing.T) {
	jt := &jsonTool{}
	result, err := jt.Execute(context.Background(), map[string]any{
		"input":     `{}`,
		"operation": "bogus",
	})
	if err != nil {
		t.Fatalf("unknown op: %v", err)
	}
	if result.Success {
		t.Fatal("expected success=false")
	}
}

func TestJsonToolMissingInput(t *testing.T) {
	jt := &jsonTool{}
	result, err := jt.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("missing input: %v", err)
	}
	if result.Success {
		t.Fatal("expected success=false")
	}
	if result.Data != "input is required" {
		t.Fatalf("got %v", result.Data)
	}
}

// ── webSearchTool ────────────────────────────────────────────

func TestWebSearchToolNameDescription(t *testing.T) {
	wst := &webSearchTool{}
	if wst.Name() != "web_search" {
		t.Fatalf("Name: %q", wst.Name())
	}
	if wst.Description() == "" {
		t.Fatal("Description should not be empty")
	}
}

func TestWebSearchToolMissingQuery(t *testing.T) {
	wst := &webSearchTool{}
	result, err := wst.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected success=false")
	}
	if result.Data != "query is required" {
		t.Fatalf("got %v", result.Data)
	}
}

func TestWebSearchToolSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "hello" {
			t.Fatalf("unexpected query: %v", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"query": "hello",
			"results": [
				{"title": "Hello Page", "url": "https://example.com", "content": "Hello desc", "engine": "google"},
				{"title": "Hi Page", "url": "https://example.org", "content": "Hi desc", "engine": "bing"}
			]
		}`))
	}))
	defer srv.Close()

	wst := &webSearchTool{trustedClient: srv.Client(), ssrfClient: srv.Client()}
	result, err := wst.Execute(context.Background(), map[string]any{
		"query":            "hello",
		"searxng_base_url": srv.URL,
	})
	if err != nil {
		t.Fatalf("success: %v", err)
	}
	if !result.Success {
		t.Fatalf("success: %v", result.Data)
	}
	data := result.Data.(map[string]any)
	if data["query"] != "hello" {
		t.Fatalf("query: got %v", data["query"])
	}
	count := data["count"].(int)
	if count != 2 {
		t.Fatalf("expected 2 results, got %d", count)
	}
}

// TestWebSearchToolDefaultLoopbackReachable pins the default-endpoint
// contract: the operator-configured default (http://localhost:5605) is dialed
// by the TRUSTED client, so a SearXNG instance on loopback is reachable.
// Regression: the tool previously routed every request through the SSRF-safe
// dialer, which refuses loopback — the flagship tool failed on every default
// call while tests injected their own client and never noticed.
func TestWebSearchToolDefaultLoopbackReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"query": "q", "results": [{"title": "t", "url": "https://example.com", "content": "c", "engine": "google"}]}`))
	}))
	defer srv.Close()

	wst := newWebSearchTool()
	// Point the OPERATOR base URL at the test server's loopback address — the
	// same shape as the production default localhost:5605.
	wst.baseURL = srv.URL
	result, err := wst.Execute(context.Background(), map[string]any{"query": "q"})
	if err != nil {
		t.Fatalf("default loopback must be reachable: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %v", result.Data)
	}
}

// TestWebSearchToolOverrideBlocksPrivateTarget executes the REAL ssrf guard:
// a caller-supplied searxng_base_url pointing at a private address is refused
// by the dialer. The old tests replaced the transport wholesale, so this —
// the guard's only interesting behavior — never ran.
func TestWebSearchToolOverrideBlocksPrivateTarget(t *testing.T) {
	wst := newWebSearchTool()
	result, err := wst.Execute(context.Background(), map[string]any{
		"query":            "q",
		"searxng_base_url": "http://127.0.0.1:9200",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("SSRF override to loopback must fail, got success: %v", result.Data)
	}
	if !strings.Contains(fmt.Sprint(result.Data), "ssrf") {
		t.Fatalf("want ssrf refusal in data, got: %v", result.Data)
	}
}

func TestWebSearchToolSearXNGFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	wst := &webSearchTool{trustedClient: srv.Client(), ssrfClient: srv.Client()}
	result, err := wst.Execute(context.Background(), map[string]any{
		"query":            "test",
		"searxng_base_url": srv.URL,
	})
	if err != nil {
		t.Fatalf("searxng error: %v", err)
	}
	if result.Success {
		t.Fatal("expected success=false")
	}
}

func TestWebSearchToolMaxResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"query": "test",
			"results": [
				{"title": "R1", "url": "https://1.com", "content": "d1", "engine": "g"},
				{"title": "R2", "url": "https://2.com", "content": "d2", "engine": "g"},
				{"title": "R3", "url": "https://3.com", "content": "d3", "engine": "g"},
				{"title": "R4", "url": "https://4.com", "content": "d4", "engine": "g"},
				{"title": "R5", "url": "https://5.com", "content": "d5", "engine": "g"}
			]
		}`))
	}))
	defer srv.Close()

	wst := &webSearchTool{trustedClient: srv.Client(), ssrfClient: srv.Client()}
	result, err := wst.Execute(context.Background(), map[string]any{
		"query":            "test",
		"max_results":      float64(3),
		"searxng_base_url": srv.URL,
	})
	if err != nil {
		t.Fatalf("max_results: %v", err)
	}
	if !result.Success {
		t.Fatalf("max_results: %v", result.Data)
	}
	data := result.Data.(map[string]any)
	count := data["count"].(int)
	if count != 3 {
		t.Fatalf("expected 3 results, got %d", count)
	}
}

func TestWebSearchToolCustomBaseURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"query":"x","results":[]}`))
	}))
	defer srv.Close()

	wst := &webSearchTool{trustedClient: srv.Client(), ssrfClient: srv.Client(), baseURL: srv.URL}
	result, err := wst.Execute(context.Background(), map[string]any{
		"query": "x",
	})
	if err != nil {
		t.Fatalf("custom base: %v", err)
	}
	if !result.Success {
		t.Fatalf("custom base: %v", result.Data)
	}
}

// ── fileTool ─────────────────────────────────────────────────

func TestFileToolNameDescription(t *testing.T) {
	ft := &fileTool{}
	if ft.Name() != "file_tools" {
		t.Fatalf("Name: %q", ft.Name())
	}
	if ft.Description() == "" {
		t.Fatal("Description should not be empty")
	}
}

func TestFileToolMissingPath(t *testing.T) {
	ft := &fileTool{}
	result, err := ft.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected success=false")
	}
	if result.Data != "path is required" {
		t.Fatalf("got %v", result.Data)
	}
}

func TestFileToolRead(t *testing.T) {
	dir := t.TempDir()
	ft := newFileTool(WithAllowedDir(dir))
	path := filepath.Join(dir, "test_read.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	result, err := ft.Execute(context.Background(), map[string]any{
		"operation": "read",
		"path":      path,
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !result.Success {
		t.Fatalf("read: %v", result.Data)
	}
	data := result.Data.(map[string]any)
	if data["content"].(string) != "hello world" {
		t.Fatalf("got %q, want 'hello world'", data["content"])
	}
	if data["size"].(int) != 11 {
		t.Fatalf("got size %d, want 11", data["size"])
	}
}

func TestFileToolWrite(t *testing.T) {
	dir := t.TempDir()
	ft := newFileTool(WithAllowedDir(dir))
	path := filepath.Join(dir, "test_write.txt")

	result, err := ft.Execute(context.Background(), map[string]any{
		"operation": "write",
		"path":      path,
		"content":   "some content",
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !result.Success {
		t.Fatalf("write: %v", result.Data)
	}

	b, _ := os.ReadFile(path)
	if string(b) != "some content" {
		t.Fatalf("file content: got %q", string(b))
	}
}

func TestFileToolList(t *testing.T) {
	dir := t.TempDir()
	ft := newFileTool(WithAllowedDir(dir))

	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644)

	result, err := ft.Execute(context.Background(), map[string]any{
		"operation": "list",
		"path":      dir,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !result.Success {
		t.Fatalf("list: %v", result.Data)
	}
	data := result.Data.(map[string]any)
	if data["count"].(int) != 2 {
		t.Fatalf("expected 2 files, got %d", data["count"])
	}
}

func TestFileToolExists(t *testing.T) {
	dir := t.TempDir()
	ft := newFileTool(WithAllowedDir(dir))
	path := filepath.Join(dir, "test_exists.txt")
	if err := os.WriteFile(path, []byte("exists"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	result, err := ft.Execute(context.Background(), map[string]any{
		"operation": "exists",
		"path":      path,
	})
	if err != nil {
		t.Fatalf("exists: %v", err)
	}
	if !result.Success {
		t.Fatalf("exists: %v", result.Data)
	}
	data := result.Data.(map[string]any)
	if data["exists"].(bool) != true {
		t.Fatal("expected exists=true")
	}

	result, err = ft.Execute(context.Background(), map[string]any{
		"operation": "exists",
		"path":      filepath.Join(dir, "nonexistent_xyz"),
	})
	if err != nil {
		t.Fatalf("exists false: %v", err)
	}
	if !result.Success {
		t.Fatalf("exists false: %v", result.Data)
	}
	data = result.Data.(map[string]any)
	if data["exists"].(bool) != false {
		t.Fatal("expected exists=false")
	}
}

func TestFileToolDelete(t *testing.T) {
	dir := t.TempDir()
	ft := newFileTool(WithAllowedDir(dir))
	path := filepath.Join(dir, "test_delete.txt")
	if err := os.WriteFile(path, []byte("delete me"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	result, err := ft.Execute(context.Background(), map[string]any{
		"operation": "delete",
		"path":      path,
	})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !result.Success {
		t.Fatalf("delete: %v", result.Data)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("file should not exist after delete")
	}
}

func TestFileToolMkdir(t *testing.T) {
	dir := t.TempDir()
	ft := newFileTool(WithAllowedDir(dir))

	newDir := filepath.Join(dir, "nested", "subdir")

	result, err := ft.Execute(context.Background(), map[string]any{
		"operation": "mkdir",
		"path":      newDir,
	})
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if !result.Success {
		t.Fatalf("mkdir: %v", result.Data)
	}

	if _, err := os.Stat(newDir); os.IsNotExist(err) {
		t.Fatal("directory should exist after mkdir")
	}
}

func TestFileToolUnknownOperation(t *testing.T) {
	ft := &fileTool{}
	result, err := ft.Execute(context.Background(), map[string]any{
		"operation": "unknown",
		"path":      "/tmp",
	})
	if err != nil {
		t.Fatalf("unknown op: %v", err)
	}
	if result.Success {
		t.Fatal("expected success=false")
	}
}

// ── RegisterBuiltinTools ─────────────────────────────────────

func TestRegisterBuiltinTools(t *testing.T) {
	r := NewEmptyRegistry()
	if err := RegisterBuiltinTools(r); err != nil {
		t.Fatalf("RegisterBuiltinTools: %v", err)
	}

	expected := []string{"calculator", "hash_tool", "string_utils", "pdf_tool", "id_generator",
		"regex", "json_tools", "web_search", "file_tools"}
	names := r.List()
	sort.Strings(names)

	if len(names) != len(expected) {
		t.Fatalf("got %d tools, expected %d: %v", len(names), len(expected), names)
	}

	for _, exp := range expected {
		tool, ok := r.Get(exp)
		if !ok {
			t.Fatalf("tool %q not found", exp)
		}
		if tool.Name() == "" {
			t.Fatalf("tool %q has empty name", exp)
		}
		if tool.Description() == "" {
			t.Fatalf("tool %q has empty description", exp)
		}
	}
}

// ── Planner Integration (public API) ─────────────────────────────────

func TestNewPlannerFromRegistry(t *testing.T) {
	r := NewRegistry()
	p, err := NewPlanner(r)
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	if p == nil {
		t.Fatal("NewPlanner returned nil")
	}

	// Verify the planner can produce a plan.
	plan, err := p.Plan(context.Background(), "计算1+1")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Steps) == 0 {
		t.Fatal("expected at least one step")
	}
	if plan.Steps[0].ToolName == "" {
		t.Fatal("expected tool name in step")
	}
}

func TestNewPlanner_UnknownRequest(t *testing.T) {
	r := NewRegistry()
	p, err := NewPlanner(r)
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}

	_, err = p.Plan(context.Background(), "xyznonexistent999")
	if err == nil {
		t.Fatal("expected error for unknown request")
	}
}

func TestNewBridgeFromRegistry(t *testing.T) {
	r := NewRegistry()
	p, err := NewPlanner(r)
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}

	bridge, err := NewBridge(r, p)
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	if bridge == nil {
		t.Fatal("NewBridge returned nil")
	}

	// Test planner fallback path.
	result, err := bridge.Execute(context.Background(), "", nil, "计算1+1")
	if err != nil {
		t.Fatalf("Bridge.Execute (fallback): %v", err)
	}
	if !result.Success {
		t.Fatal("expected successful execution")
	}
}

func TestNewBridge_DirectToolCall(t *testing.T) {
	r := NewRegistry()
	p, err := NewPlanner(r)
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}

	bridge, err := NewBridge(r, p)
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}

	result, err := bridge.Execute(context.Background(), "calculator",
		map[string]interface{}{"expression": "2+2"}, "")
	if err != nil {
		t.Fatalf("Bridge.Execute (direct): %v", err)
	}
	if !result.Success {
		t.Fatal("expected successful direct execution")
	}
}

func TestPlannerProvider(t *testing.T) {
	r := NewRegistry()
	provider := r.PlannerProvider()
	if provider == nil {
		t.Fatal("PlannerProvider returned nil")
	}

	tools := provider.ListTools()
	if len(tools) == 0 {
		t.Fatal("expected tools from provider")
	}

	caps, err := provider.GetToolCapabilities("calculator")
	if err != nil {
		t.Fatalf("GetToolCapabilities: %v", err)
	}
	// calculator declares math capability, which expands to granular capabilities.
	t.Logf("calculator capabilities: %v", caps)
}

func TestPlannerProvider_NotFound(t *testing.T) {
	r := NewRegistry()
	provider := r.PlannerProvider()

	_, err := provider.GetToolCapabilities("nonexistent_tool")
	if err == nil {
		t.Fatal("expected error for nonexistent tool")
	}
}

func TestFileTool_ValidatePathBlocksSymlinkTraversal(t *testing.T) {
	// Create a temp dir structure: allowedDir/safe.txt, and a symlink -> /etc
	tmpDir := t.TempDir()
	allowedDir := filepath.Join(tmpDir, "allowed")
	require.NoError(t, os.MkdirAll(allowedDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(allowedDir, "safe.txt"), []byte("safe"), 0644))

	// Create a symlink inside allowedDir pointing outside.
	symlinkPath := filepath.Join(allowedDir, "escape")
	require.NoError(t, os.Symlink("/etc", symlinkPath))

	ft := newFileTool(WithAllowedDir(allowedDir))

	// Accessing the symlink should be rejected.
	result, err := ft.Execute(context.Background(), map[string]any{
		"operation": "read",
		"path":      symlinkPath,
	})
	require.NoError(t, err, "Execute itself should not fail")
	assert.False(t, result.Success, "symlink escape should be blocked")
	assert.Contains(t, result.Data.(string), "access denied", "error should mention access denied")

	// Accessing a normal file inside allowedDir should succeed.
	result, err = ft.Execute(context.Background(), map[string]any{
		"operation": "read",
		"path":      filepath.Join(allowedDir, "safe.txt"),
	})
	require.NoError(t, err, "Execute itself should not fail")
	assert.True(t, result.Success, "normal file inside allowed dir should be accessible")
}
