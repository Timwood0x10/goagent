package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Timwood0x10/ares/internal/tools/resources/base"
	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// HTTPRequest performs HTTP requests to external APIs.
type HTTPRequest struct {
	*base.BaseTool
	client *http.Client
}

// NewHTTPRequest creates a new HTTPRequest tool.
//
// The client is configured with SSRF defenses: private/loopback IPs are
// blocked, redirects are capped at MaxHTTPRedirects with each destination
// re-validated, and response bodies are capped at MaxHTTPResponseBytes.
func NewHTTPRequest() *HTTPRequest {
	params := &core.ParameterSchema{
		Type: "object",
		Properties: map[string]*core.Parameter{
			"url": {
				Type:        "string",
				Description: "Target URL",
			},
			"method": {
				Type:        "string",
				Description: "HTTP method (GET, POST, PUT, DELETE, PATCH)",
				Default:     "GET",
				Enum:        []interface{}{"GET", "POST", "PUT", "DELETE", "PATCH"},
			},
			"headers": {
				Type:        "object",
				Description: "Request headers as key-value pairs",
			},
			"body": {
				Type:        "string",
				Description: "Request body (for POST, PUT, PATCH)",
			},
			"timeout": {
				Type:        "integer",
				Description: "Request timeout in seconds",
				Default:     30,
			},
		},
		Required: []string{"url"},
	}

	return &HTTPRequest{
		BaseTool: base.NewBaseToolWithCapabilities("http_request", "Perform HTTP requests to external APIs", core.CategoryCore, []core.Capability{core.CapabilityNetwork}, params),
		client: &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: SSRFCheckRedirect,
			Transport:     SSRFTransport(),
		},
	}
}

// Execute performs the HTTP request.
func (t *HTTPRequest) Execute(ctx context.Context, params map[string]interface{}) (core.Result, error) {
	url, ok := params["url"].(string)
	if !ok || url == "" {
		return core.NewErrorResult("url is required"), nil
	}

	// SSRF defense: validate scheme and block private/loopback/link-local IPs.
	if err := ValidateURL(ctx, url); err != nil {
		return core.NewErrorResult(fmt.Sprintf("url rejected by SSRF filter: %v", err)), nil
	}

	method := getString(params, "method")
	if method == "" {
		method = "GET"
	}

	headers := make(map[string]string)
	if headersParam, ok := params["headers"].(map[string]interface{}); ok {
		for k, v := range headersParam {
			if val, ok := v.(string); ok {
				headers[k] = val
			}
		}
	}

	timeout := getInt(params, "timeout", 30)
	var reqCtx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		reqCtx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	} else {
		reqCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	var bodyReader io.Reader
	if body, ok := params["body"].(string); ok && body != "" {
		// Only validate JSON when Content-Type is explicitly set to application/json
		if contentType, exists := headers["Content-Type"]; exists && contentType == "application/json" {
			if !json.Valid([]byte(body)) {
				return core.NewErrorResult("invalid JSON body"), nil
			}
		}
		bodyReader = bytes.NewBufferString(body)
	}

	req, err := http.NewRequestWithContext(reqCtx, method, url, bodyReader)
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("failed to create request: %v", err)), nil
	}

	// Set default Content-Type for POST/PUT/PATCH
	if method != "GET" && method != "DELETE" && bodyReader != nil {
		if headers["Content-Type"] == "" {
			req.Header.Set("Content-Type", "application/json")
		}
	}

	// Set headers
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// Execute request
	startTime := time.Now()
	resp, err := t.client.Do(req)
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("request failed: %v", err)), nil
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Error("failed to close response body", "error", err)
		}
	}()

	duration := time.Since(startTime)

	// Cap response body reads to prevent memory exhaustion from oversized responses.
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, MaxHTTPResponseBytes))
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("failed to read response: %v", err)), nil
	}

	// Try to parse as JSON
	var jsonBody interface{}
	if err := json.Unmarshal(respBody, &jsonBody); err != nil {
		// If not JSON, return as string
		jsonBody = string(respBody)
	}

	// Collect response headers
	respHeaders := make(map[string]string)
	for k, v := range resp.Header {
		if len(v) > 0 {
			respHeaders[k] = v[0]
		}
	}

	return core.NewResult(true, map[string]interface{}{
		"status_code": resp.StatusCode,
		"status":      resp.Status,
		"headers":     respHeaders,
		"body":        jsonBody,
		"size_bytes":  len(respBody),
		"duration_ms": duration.Milliseconds(),
	}), nil
}

// SetClient sets a custom HTTP client.
func (t *HTTPRequest) SetClient(client *http.Client) {
	t.client = client
}

// getString safely gets a string parameter.
func getString(params map[string]interface{}, key string) string {
	if v, ok := params[key].(string); ok {
		return v
	}
	return ""
}

// getInt safely gets an int parameter.
func getInt(params map[string]interface{}, key string, defaultVal int) int {
	switch v := params[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return defaultVal
}
