// Example 09 — Full App: a complete ARES application with a web UI.
//
// Purpose:
//
//	Show how to assemble every ARES subsystem (LLM, memory, distillation,
//	AKG knowledge) into one runnable HTTP server with a minimal HTML
//	dashboard and custom tools.
//
// Learning objectives:
//   - Load a YAML config and turn it into SDK options.
//   - Register custom tools on the runtime tool registry.
//   - Create an Agent and call Agent.Run from an HTTP handler.
//   - Track token usage, tool-call count, and latency per query.
//
// Core APIs used:
//   - github.com/Timwood0x10/ares/sdk.LoadConfigFile
//   - github.com/Timwood0x10/ares/sdk.Config.ToOptions
//   - github.com/Timwood0x10/ares/sdk.NewRuntime
//   - github.com/Timwood0x10/ares/sdk.Runtime.NewAgent
//   - github.com/Timwood0x10/ares/sdk.Agent.Run
//   - github.com/Timwood0x10/ares/api/tools.ToolFunc
//
// Run:
//
//	go run examples/09-full-app/main.go
//
// Then open http://localhost:8080 in a browser.
//
// The server binds 127.0.0.1 by default — this demo dashboard has no auth, so
// it stays off the network unless you opt in. Set SERVER_HOST=0.0.0.0 to bind
// every interface (needed inside a container to reach a published port).
//
// Expected output:
//
//	🌐 Open http://localhost:8080 (listening on 127.0.0.1:8080)
//	(followed by HTTP request logs as you chat in the browser)
//
// Try modifying:
//   - SERVER_HOST / the port in the addr construction to listen elsewhere.
//   - The appTools slice to add or replace custom tools.
//   - The instruction passed to WithInstruction to change agent persona.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/api/tools"
	"github.com/Timwood0x10/ares/sdk"
)

func main() {
	// ── Step 1: Load ares.yaml and build the runtime ──
	// LoadConfigFile reads the YAML; ToOptions converts each populated
	// field into a functional SDK option. NewRuntime wires the LLM,
	// memory, distillation, and AKG subsystems from those options.
	cfg, err := sdk.LoadConfigFile("ares.yaml")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	opts, err := cfg.ToOptions()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	rt := sdk.NewRuntime(opts...) // runtime owns all subsystem lifecycles
	defer rt.Close()

	// ── Step 2: Register custom tools ──
	// Each ToolFunc is registered on the runtime's tool registry so the
	// agent can discover and call them during a Run cycle.
	for _, t := range appTools {
		if err := rt.ToolRegistry().Register(t); err != nil {
			log.Printf("register: %v", err)
			return
		}
	}

	// ── Step 3: Create the agent ──
	// NewAgent returns a configured Agent. WithInstruction sets the
	// system prompt that guides tool selection and response style.
	agent := rt.NewAgent("assistant",
		sdk.WithInstruction("You are a helpful assistant with tools. Use calculator for math, weather for forecasts."),
	)

	// ── Step 4: Wire HTTP routes and start the server ──
	// appState holds the agent and a mutex-guarded chat history so
	// concurrent HTTP requests can safely append entries.
	app := &appState{
		agent:   agent,
		history: make([]chatEntry, 0),
	}

	http.HandleFunc("/", app.handleIndex)        // serves the HTML dashboard
	http.HandleFunc("/api/chat", app.handleChat) // POST a user message
	http.HandleFunc("/api/stats", app.handleStats)

	// Bind loopback by default (0.3.1 hardening): this dashboard has no
	// authentication at all, so binding every interface would expose the chat
	// endpoint — and through it the configured LLM credentials' spend — to the
	// whole network. SERVER_HOST opts into a wider bind for containers, where
	// reaching the process through a published port requires 0.0.0.0.
	host := os.Getenv("SERVER_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	addr := net.JoinHostPort(host, "8080")
	fmt.Printf("🌐 Open http://localhost:8080 (listening on %s)\n", addr)
	if host == "0.0.0.0" {
		log.Printf("WARNING: SERVER_HOST=0.0.0.0 exposes this UNAUTHENTICATED demo dashboard on every interface")
	}
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Printf("server: %v", err)
	}
}

// ---- app state ----

// chatEntry records a single user/assistant exchange for the dashboard.
type chatEntry struct {
	Time    time.Time `json:"time"`
	Input   string    `json:"input"`
	Output  string    `json:"output"`
	Tools   int       `json:"tools"`
	Tokens  int       `json:"tokens"`
	Latency string    `json:"latency"`
}

// appState holds the agent and chat history shared across HTTP handlers.
type appState struct {
	mu      sync.Mutex // guards history
	agent   *sdk.Agent
	history []chatEntry
}

// ---- HTTP handlers ----

// handleIndex serves the embedded HTML dashboard.
func (app *appState) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprint(w, indexHTML)
}

// handleChat accepts a POSTed user message, runs the agent, and returns
// the response plus usage metadata as JSON.
func (app *appState) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	input := r.FormValue("input")
	if input == "" {
		http.Error(w, "input required", 400)
		return
	}

	// Agent.Run executes the full agent loop (LLM call, tool dispatch,
	// memory write-back) and returns output plus usage stats.
	result, err := app.agent.Run(r.Context(), input)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	// Build a chat entry from the RunResult fields.
	entry := chatEntry{
		Time:    time.Now(),
		Input:   input,
		Output:  result.Output,
		Tools:   result.ToolCalls,
		Tokens:  result.TokenUsage.Total,
		Latency: result.Duration.Round(time.Millisecond).String(),
	}

	app.mu.Lock()
	app.history = append(app.history, entry)
	app.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(entry)
}

// handleStats returns aggregate usage counters as JSON.
func (app *appState) handleStats(w http.ResponseWriter, r *http.Request) {
	app.mu.Lock()
	defer app.mu.Unlock()

	totalTokens := 0
	totalTools := 0
	for _, e := range app.history {
		totalTokens += e.Tokens
		totalTools += e.Tools
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"total_queries":    len(app.history),
		"total_tokens":     totalTokens,
		"total_tool_calls": totalTools,
	})
}

// ---- tools ----

// appTools is the set of custom tools registered with the runtime.
var appTools = []tools.Tool{
	tools.ToolFunc{
		ToolName: "calculator",
		ToolDesc: "Evaluate a mathematical expression",
		Fn: func(_ context.Context, params map[string]any) (any, error) {
			expr, _ := params["expression"].(string)
			return fmt.Sprintf("result: %s = (demo) 42", expr), nil // demo returns a fixed value
		},
	},
	tools.ToolFunc{
		ToolName: "get_weather",
		ToolDesc: "Get current weather for a city",
		Fn: func(_ context.Context, params map[string]any) (any, error) {
			city, _ := params["city"].(string)
			return fmt.Sprintf(`{"city":%q,"temp":22,"condition":"sunny"}`, city), nil // demo weather JSON
		},
	},
}

// ---- HTML dashboard ----

// indexHTML is the single-page dashboard served at "/".
const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>ARES Full App</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font: 14px/1.6 -apple-system, sans-serif; max-width: 800px; margin: 0 auto; padding: 20px; }
  h1 { margin-bottom: 8px; }
  .stats { display: flex; gap: 16px; margin: 16px 0; }
  .stat { background: #f0f4f8; border-radius: 8px; padding: 12px 20px; flex: 1; }
  .stat .val { font-size: 24px; font-weight: 700; }
  .stat .lbl { font-size: 12px; color: #666; }
  form { display: flex; gap: 8px; margin: 16px 0; }
  input[type=text] { flex: 1; padding: 10px; border: 1px solid #ccc; border-radius: 6px; font-size: 14px; }
  button { padding: 10px 20px; background: #0066ff; color: #fff; border: none; border-radius: 6px; cursor: pointer; }
  button:hover { background: #0052cc; }
  .entry { border: 1px solid #e0e0e0; border-radius: 8px; padding: 12px; margin: 8px 0; }
  .entry .q { font-weight: 600; margin-bottom: 4px; }
  .entry .a { color: #333; }
  .entry .meta { font-size: 12px; color: #888; margin-top: 4px; }
  #loading { display: none; color: #666; margin: 8px 0; }
</style>
</head>
<body>
<h1>🤖 ARES Demo</h1>
<p>Full-stack agent with tools, memory, and a web UI.</p>

<div class="stats" id="stats">
  <div class="stat"><div class="val" id="q">0</div><div class="lbl">Queries</div></div>
  <div class="stat"><div class="val" id="t">0</div><div class="lbl">Tokens</div></div>
  <div class="stat"><div class="val" id="tc">0</div><div class="lbl">Tool Calls</div></div>
</div>

<form id="form" onsubmit="return send()">
  <input type="text" id="input" placeholder="Ask something..." autofocus>
  <button type="submit">Send</button>
</form>
<div id="loading">Thinking...</div>
<div id="history"></div>

<script>
async function send() {
  const input = document.getElementById('input');
  const loading = document.getElementById('loading');
  const history = document.getElementById('history');

  loading.style.display = 'block';
  const resp = await fetch('/api/chat', {
    method: 'POST',
    headers: {'Content-Type': 'application/x-www-form-urlencoded'},
    body: 'input='+encodeURIComponent(input.value),
  });
  loading.style.display = 'none';
  if (!resp.ok) { alert(await resp.text()); return false; }
  const data = await resp.json();

  const div = document.createElement('div');
  div.className = 'entry';
  div.innerHTML = '<div class="q">🧑 '+esc(data.input)+'</div>'
    + '<div class="a">🤖 '+esc(data.output)+'</div>'
    + '<div class="meta">tools: '+data.tools+' | tokens: '+data.tokens+' | '+data.latency+'</div>';
  history.prepend(div);
  input.value = '';
  refreshStats();
  return false;
}
async function refreshStats() {
  const r = await fetch('/api/stats');
  const s = await r.json();
  document.getElementById('q').textContent = s.total_queries;
  document.getElementById('t').textContent = s.total_tokens;
  document.getElementById('tc').textContent = s.total_tool_calls;
}
function esc(s) { return s.replace(/&/g,'&amp;').replace(/</g,'&lt;'); }
refreshStats();
</script>
</body>
</html>`
