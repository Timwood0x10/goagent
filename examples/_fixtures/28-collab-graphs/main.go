// Collaboration graphs — submit an explicit DAG over HTTP and let the kernel
// execute it (fusion plan Phase C4: sdk.Graph shapes reachable from ops).
//
// The endpoint reuses the same kernel fabric + scheduler as every other
// submission path. Validation happens BEFORE execution: unknown capability →
// 400 with the available list; cycles → 400; edges capped at 4096.
//
// Prerequisites:
//
//  1. Start a peer runtime in another terminal:
//     ares serve                       (reads ./ares.yaml)
//  2. This example then POSTs two graphs:
//     - pipeline: research → write
//     - orchestrate: root fans out to two workers, join aggregates
//
// Core APIs used: none beyond net/http — this is the OPERATIONS surface.
//
// Run:
//
//	go run examples/28-collab-graphs/main.go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

const baseURL = "http://localhost:8080"

type node struct {
	ID         string `json:"id"`
	Capability string `json:"capability"`
	Input      any    `json:"input,omitempty"`
}

type edge struct{ From, To string }

type graphReq struct {
	SchemaVersion int    `json:"schema_version"`
	Nodes         []node `json:"nodes"`
	Edges         []edge `json:"edges,omitempty"`
}

func submit(apiKey string, req graphReq) map[string]any {
	b, _ := json.Marshal(req)
	httpReq, _ := http.NewRequest(http.MethodPost, baseURL+"/api/graphs", bytes.NewReader(b))
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ POST /api/graphs: %v\n   → is the peer runtime running? start it with: ares serve\n", err)
		os.Exit(1)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // best-effort body close
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	fmt.Printf("HTTP %d\n", resp.StatusCode)
	return out
}

func main() {
	apiKey := os.Getenv("ARES_API_KEY")

	fmt.Println("═══ 1. Pipeline graph (research → write) ═══")
	r1 := submit(apiKey, graphReq{
		SchemaVersion: 1,
		Nodes: []node{
			{ID: "s1", Capability: "research", Input: "topic X"},
			{ID: "s2", Capability: "writer", Input: "draft from s1"},
		},
		Edges: []edge{{From: "s1", To: "s2"}},
	})
	printResult(r1)

	fmt.Println("\n═══ 2. Orchestration graph (root → workers → join) ═══")
	r2 := submit(apiKey, graphReq{
		SchemaVersion: 1,
		Nodes: []node{
			{ID: "root", Capability: "research"},
			{ID: "w1", Capability: "research"},
			{ID: "w2", Capability: "writer"},
			{ID: "join", Capability: "review"},
		},
		Edges: []edge{
			{From: "root", To: "w1"}, {From: "root", To: "w2"},
			{From: "w1", To: "join"}, {From: "w2", To: "join"},
		},
	})
	printResult(r2)

	// Give the scheduler a moment if nodes are still draining, then show
	// final outputs again for the second graph.
	time.Sleep(5 * time.Second)
	fmt.Println("\n── final outputs (graph 2) ──")
	printResult(map[string]any{"outputs": r2["outputs"]})
}

func printResult(r map[string]any) {
	if err, ok := r["error"].(string); ok && err != "" {
		fmt.Println("  error:", err)
		if caps, ok := r["available_capabilities"].([]any); ok {
			fmt.Println("  available:", caps)
		}
		return
	}
	if ids, ok := r["task_ids"].(map[string]any); ok {
		fmt.Println("  task_ids:", ids)
	}
	if outs, ok := r["outputs"].(map[string]any); ok {
		for id, v := range outs {
			s := fmt.Sprint(v)
			if len(s) > 80 {
				s = s[:80] + "…"
			}
			fmt.Printf("  [%s] %s\n", id, s)
		}
	}
}
