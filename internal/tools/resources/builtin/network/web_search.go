package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Timwood0x10/ares/internal/tools/resources/base"
	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// SearXNGResult represents a single search result from SearXNG.
type SearXNGResult struct {
	Title         string `json:"title"`
	URL           string `json:"url"`
	Content       string `json:"content"`
	Engine        string `json:"engine"`
	PublishedDate any    `json:"publishedDate"`
	Category      string `json:"category"`
}

// SearXNGResponse represents the JSON response from SearXNG.
type SearXNGResponse struct {
	Query               string          `json:"query"`
	Results             []SearXNGResult `json:"results"`
	Answers             []any           `json:"answers"`
	Corrections         []any           `json:"corrections"`
	Infoboxes           []any           `json:"infoboxes"`
	Suggestions         []string        `json:"suggestions"`
	UnresponsiveEngines any             `json:"unresponsive_engines"`
}

// WebSearch performs searches using the SearXNG meta search engine.
//
// SECURITY: The SearXNG base URL is validated against an allowlist to prevent
// SSRF. The default allowlist contains only the local SearXNG instance. Use
// SetAllowedBaseURLs to permit additional trusted instances.
type WebSearch struct {
	*base.BaseTool
	client          *http.Client
	allowedBaseURLs map[string]bool
}

// defaultSearXNGBaseURL is the default allowed SearXNG instance.
const defaultSearXNGBaseURL = "http://localhost:5605"

// NewWebSearch creates a new WebSearch tool.
func NewWebSearch() *WebSearch {
	params := &core.ParameterSchema{
		Type: "object",
		Properties: map[string]*core.Parameter{
			"query": {
				Type:        "string",
				Description: "Search query",
			},
			"max_results": {
				Type:        "integer",
				Description: "Maximum number of results to return (1-50)",
				Default:     10,
			},
			"language": {
				Type:        "string",
				Description: "Language filter (e.g., 'en', 'zh', 'de')",
			},
			"categories": {
				Type:        "string",
				Description: "Search categories: general, news, images, videos, files, it, science, map",
			},
			"engines": {
				Type:        "string",
				Description: "Comma-separated engine names (e.g., 'google,bing,wikipedia')",
			},
			"pageno": {
				Type:        "integer",
				Description: "Page number for pagination",
				Default:     1,
			},
			"time_range": {
				Type:        "string",
				Description: "Time range filter: day, week, month, year",
			},
			"searxng_base_url": {
				Type:        "string",
				Description: "SearXNG instance base URL (must be in the configured allowlist)",
				Default:     defaultSearXNGBaseURL,
			},
		},
		Required: []string{"query"},
	}

	// The SSRF defense here is the operator-controlled allowlist (isBaseURLAllowed),
	// not a blanket loopback/private-IP dialer. The default SearXNG instance is
	// http://localhost:5605 (loopback), so the old SSRFTransport() self-blocked
	// the tool's own default endpoint: every default request failed at dial time.
	// We keep defense-in-depth by re-validating each redirect hop against the
	// allowlist instead, so a compromised SearXNG cannot redirect the client to
	// an arbitrary internal service.
	w := &WebSearch{
		BaseTool: base.NewBaseToolWithCapabilities(
			"web_search",
			"Search the web using SearXNG meta search engine. Returns structured results with titles, URLs, and content snippets.",
			core.CategoryCore,
			[]core.Capability{core.CapabilityNetwork, core.CapabilityKnowledge},
			params,
		),
		allowedBaseURLs: map[string]bool{defaultSearXNGBaseURL: true},
	}
	w.client = &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: w.checkRedirect,
	}
	return w
}

// checkRedirect caps the number of redirects and requires every hop to remain
// within the operator-approved base URLs. It is used as the http.Client
// CheckRedirect hook so a hijacked SearXNG response cannot redirect the tool
// to an arbitrary internal address (SSRF via redirect).
func (t *WebSearch) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= MaxHTTPRedirects {
		return fmt.Errorf("stopped after %d redirects", MaxHTTPRedirects)
	}
	base := req.URL.Scheme + "://" + req.URL.Host
	if !t.isBaseURLAllowed(base) {
		return fmt.Errorf("redirect to %q is not in the SearXNG allowlist", req.URL.String())
	}
	return nil
}

// SetAllowedBaseURLs configures the set of SearXNG base URLs that users are
// permitted to query. This is the SSRF allowlist for the web_search tool.
func (t *WebSearch) SetAllowedBaseURLs(urls []string) {
	allowed := make(map[string]bool, len(urls))
	for _, u := range urls {
		allowed[u] = true
	}
	t.allowedBaseURLs = allowed
}

// isBaseURLAllowed reports whether the given SearXNG base URL is in the allowlist.
func (t *WebSearch) isBaseURLAllowed(baseURL string) bool {
	return t.allowedBaseURLs[baseURL]
}

// Execute performs a web search.
func (t *WebSearch) Execute(ctx context.Context, params map[string]interface{}) (core.Result, error) {
	query, ok := params["query"].(string)
	if !ok || query == "" {
		return core.NewErrorResult("query is required"), nil
	}

	baseURL := getString(params, "searxng_base_url")
	if baseURL == "" {
		baseURL = defaultSearXNGBaseURL
	}

	// SSRF defense: only allow pre-approved SearXNG base URLs.
	if !t.isBaseURLAllowed(baseURL) {
		return core.NewErrorResult(fmt.Sprintf("searxng_base_url %q is not in the allowlist; contact the operator to add it", baseURL)), nil
	}

	// Build query parameters
	searchURL, err := url.Parse(baseURL + "/search")
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("invalid base URL: %v", err)), nil
	}

	q := searchURL.Query()
	q.Set("q", query)
	q.Set("format", "json")

	if maxResults := getInt(params, "max_results", 10); maxResults > 0 {
		if maxResults > 50 {
			maxResults = 50
		}
		q.Set("max_results", strconv.Itoa(maxResults))
	}

	if lang := getString(params, "language"); lang != "" {
		q.Set("language", lang)
	}

	if categories := getString(params, "categories"); categories != "" {
		q.Set("categories", categories)
	}

	if engines := getString(params, "engines"); engines != "" {
		q.Set("engines", engines)
	}

	if pageno := getInt(params, "pageno", 1); pageno > 1 {
		q.Set("pageno", strconv.Itoa(pageno))
	}

	if timeRange := getString(params, "time_range"); timeRange != "" {
		q.Set("time_range", timeRange)
	}

	searchURL.RawQuery = q.Encode()

	// Create request
	req, err := http.NewRequestWithContext(ctx, "GET", searchURL.String(), nil)
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("failed to create request: %v", err)), nil
	}

	req.Header.Set("User-Agent", "ARES/1.0 (Interview Demo; +https://github.com/Timwood0x10/ares)")
	req.Header.Set("Accept", "application/json")

	// Execute request
	resp, err := t.client.Do(req)
	if err != nil {
		msg := fmt.Sprintf("SearXNG request failed: %v", err)
		msg += "\nEnsure SearXNG is running: docker compose up -d searxng"
		return core.NewErrorResult(msg), nil
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Error("failed to close response body", "error", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, MaxHTTPResponseBytes))
		return core.NewErrorResult(fmt.Sprintf("SearXNG returned status %d: %s", resp.StatusCode, string(body))), nil
	}

	// Parse response with size cap to prevent memory exhaustion.
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxHTTPResponseBytes))
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("failed to read response: %v", err)), nil
	}

	var searxngResp SearXNGResponse
	if err := json.Unmarshal(body, &searxngResp); err != nil {
		return core.NewErrorResult(fmt.Sprintf("failed to parse SearXNG response: %v", err)), nil
	}

	// Build structured results
	results := make([]map[string]interface{}, 0, len(searxngResp.Results))
	for _, r := range searxngResp.Results {
		results = append(results, map[string]interface{}{
			"title":   r.Title,
			"url":     r.URL,
			"snippet": r.Content,
			"engine":  r.Engine,
		})
	}

	// Build search metadata
	searchMeta := map[string]interface{}{
		"query":         query,
		"total_results": len(results),
		"results":       results,
	}

	// Add suggestions if available
	if len(searxngResp.Suggestions) > 0 {
		searchMeta["suggestions"] = searxngResp.Suggestions
	}

	// Add corrections if available
	if len(searxngResp.Corrections) > 0 {
		searchMeta["corrections"] = searxngResp.Corrections
	}

	return core.NewResult(true, searchMeta), nil
}
