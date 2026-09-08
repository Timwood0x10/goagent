package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Timwood0x10/ares/internal/tools/resources/base"
	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// EmbeddingTool provides text embedding via the external embedding service.
type EmbeddingTool struct {
	*base.BaseTool
	client  *http.Client
	baseURL string
}

// EmbeddingRequest matches the Python embedding service POST /embed body.
type EmbeddingRequest struct {
	Text   string `json:"text"`
	Prefix string `json:"prefix,omitempty"`
}

// EmbeddingResponse matches the Python embedding service response.
type EmbeddingResponse struct {
	Embedding []float64 `json:"embedding"`
	Dimension int       `json:"dimension"`
	Cached    bool      `json:"cached"`
}

// BatchEmbeddingRequest matches the Python embedding service POST /embed_batch body.
type BatchEmbeddingRequest struct {
	Texts  []string `json:"texts"`
	Prefix string   `json:"prefix,omitempty"`
}

// BatchEmbeddingResponse matches the Python embedding service response.
type BatchEmbeddingResponse struct {
	Embeddings  [][]float64 `json:"embeddings"`
	Dimension   int         `json:"dimension"`
	CachedCount int         `json:"cached_count"`
}

const defaultEmbeddingBaseURL = "http://localhost:8000"

// NewEmbeddingTool creates a new EmbeddingTool that calls the configured embedding service.
// If baseURL is empty, defaults to http://localhost:8000.
func NewEmbeddingTool(baseURL string) *EmbeddingTool {
	if baseURL == "" {
		baseURL = defaultEmbeddingBaseURL
	}
	params := &core.ParameterSchema{
		Type: "object",
		Properties: map[string]*core.Parameter{
			"action": {
				Type:        "string",
				Description: "Action to perform: 'embed' for single text, 'embed_batch' for multiple texts, 'health' for service health check",
			},
			"text": {
				Type:        "string",
				Description: "Text to embed (required for 'embed' action)",
			},
			"texts": {
				Type:        "array",
				Description: "Array of texts to embed (required for 'embed_batch' action)",
			},
			"prefix": {
				Type:        "string",
				Description: "Optional prefix, e.g. 'query:' for search queries, 'passage:' for document storage (default: 'query:')",
			},
		},
		Required: []string{"action"},
	}

	return &EmbeddingTool{
		BaseTool: base.NewBaseToolWithCapabilities("embedding",
			"Generate vector embeddings for text using the embedding service. "+
				"Supports single text embedding and batch embedding of multiple texts. "+
				"Uses e5-large model (1024 dimensions) with Redis caching for performance.",
			core.CategoryExternal, []core.Capability{core.CapabilityExternal}, params),
		client:  &http.Client{Timeout: 30 * time.Second},
		baseURL: baseURL,
	}
}

// Execute performs the embedding operation.
func (t *EmbeddingTool) Execute(ctx context.Context, params map[string]interface{}) (core.Result, error) {
	action, _ := params["action"].(string)
	switch action {
	case "health":
		return t.checkHealth(ctx)
	case "embed":
		return t.embedText(ctx, params)
	case "embed_batch":
		return t.embedBatch(ctx, params)
	default:
		return core.NewErrorResult(fmt.Sprintf("unknown action: %s", action)), nil
	}
}

func (t *EmbeddingTool) checkHealth(ctx context.Context) (core.Result, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", t.baseURL+"/health", nil)
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("create request: %v", err)), nil
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("embedding service unreachable: %v", err)), nil
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Error("embedding: close health response body", "error", err)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("read response: %v", err)), nil
	}

	var health map[string]interface{}
	if err := json.Unmarshal(body, &health); err != nil {
		return core.NewErrorResult(fmt.Sprintf("parse health response: %v", err)), nil
	}
	return core.NewResult(true, health), nil
}

func (t *EmbeddingTool) embedText(ctx context.Context, params map[string]interface{}) (core.Result, error) {
	text, _ := params["text"].(string)
	if text == "" {
		return core.NewErrorResult("'text' parameter is required for 'embed' action"), nil
	}
	prefix, _ := params["prefix"].(string)
	if prefix == "" {
		prefix = "query:"
	}

	reqBody, err := json.Marshal(EmbeddingRequest{Text: text, Prefix: prefix})
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("marshal request: %v", err)), nil
	}

	req, err := http.NewRequestWithContext(ctx, "POST", t.baseURL+"/embed", bytes.NewReader(reqBody))
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("create request: %v", err)), nil
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("embedding service call failed: %v", err)), nil
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Error("embedding: close health response body", "error", err)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("read response: %v", err)), nil
	}

	var embedResp EmbeddingResponse
	if err := json.Unmarshal(body, &embedResp); err != nil {
		return core.NewErrorResult(fmt.Sprintf("parse embedding response: %v", err)), nil
	}

	return core.NewResult(true, map[string]interface{}{
		"text":      text,
		"dimension": embedResp.Dimension,
		"cached":    embedResp.Cached,
		"vector":    embedResp.Embedding,
	}), nil
}

func (t *EmbeddingTool) embedBatch(ctx context.Context, params map[string]interface{}) (core.Result, error) {
	textsRaw, ok := params["texts"].([]interface{})
	if !ok || len(textsRaw) == 0 {
		return core.NewErrorResult("'texts' parameter (array) is required for 'embed_batch' action"), nil
	}
	texts := make([]string, len(textsRaw))
	for i, v := range textsRaw {
		s, ok := v.(string)
		if !ok {
			// A non-string element would be silently zero-folded into "",
			// sending empty texts to the embedding service (wasted calls,
			// meaningless vectors). Reject the whole batch naming the index
			// so the caller can fix its arguments.
			return core.NewErrorResult(fmt.Sprintf("'texts'[%d] must be a string, got %T", i, v)), nil
		}
		texts[i] = s
	}
	prefix, _ := params["prefix"].(string)
	if prefix == "" {
		prefix = "passage:"
	}

	reqBody, err := json.Marshal(BatchEmbeddingRequest{Texts: texts, Prefix: prefix})
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("marshal batch request: %v", err)), nil
	}

	req, err := http.NewRequestWithContext(ctx, "POST", t.baseURL+"/embed_batch", bytes.NewReader(reqBody))
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("create request: %v", err)), nil
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("batch embedding service call failed: %v", err)), nil
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Error("embedding: close health response body", "error", err)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("read response: %v", err)), nil
	}

	var batchResp BatchEmbeddingResponse
	if err := json.Unmarshal(body, &batchResp); err != nil {
		return core.NewErrorResult(fmt.Sprintf("parse batch response: %v", err)), nil
	}

	return core.NewResult(true, map[string]interface{}{
		"count":        len(batchResp.Embeddings),
		"dimension":    batchResp.Dimension,
		"cached_count": batchResp.CachedCount,
	}), nil
}
