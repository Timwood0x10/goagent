// Simple embedding bridge that forwards requests to Ollama's embedding API.
// Acts as a drop-in replacement for the Python embedding service.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

func main() {
	client := &http.Client{Timeout: 30 * time.Second}

	http.HandleFunc("/embed", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20)) // 10 MB cap
		if err != nil {
			http.Error(w, "read request body", http.StatusBadRequest)
			return
		}
		var req struct {
			Text   string `json:"text"`
			Prefix string `json:"prefix"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		payload := map[string]any{
			"model": "qwen3-embedding:0.6b",
			"input": req.Prefix + req.Text,
		}
		data, err := json.Marshal(payload)
		if err != nil {
			http.Error(w, "marshal upstream payload", http.StatusInternalServerError)
			return
		}
		reqCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		httpReq, err := http.NewRequestWithContext(reqCtx, "POST",
			"http://localhost:11434/api/embed", bytes.NewReader(data))
		if err != nil {
			http.Error(w, "build upstream request", http.StatusInternalServerError)
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(httpReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		if err != nil {
			http.Error(w, "read upstream response", http.StatusBadGateway)
			return
		}

		var ollamaResp struct {
			Embeddings [][]float64 `json:"embeddings"`
		}
		if err := json.Unmarshal(respBody, &ollamaResp); err != nil {
			http.Error(w, "decode upstream response", http.StatusBadGateway)
			return
		}
		if len(ollamaResp.Embeddings) == 0 {
			http.Error(w, "no embeddings", http.StatusInternalServerError)
			return
		}

		result := map[string]any{"embedding": ollamaResp.Embeddings[0]}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})

	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"status":"healthy","model":"qwen3-embedding:0.6b"}`)
	})

	srv := &http.Server{
		Addr:              ":8000",
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	fmt.Println("Embedding bridge on :8000 → Ollama :11434")
	_ = srv.ListenAndServe()
}
