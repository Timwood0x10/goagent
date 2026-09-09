// Package openaiapi is the official OpenAI-API-compatible protocol adapter for ARES.
//
// It exposes ARES capabilities over the full OpenAI API wire format so any
// OpenAI-API-compatible client (LangChain, LLM frameworks, custom apps)
// plugs into the ARES runtime. The adapter binds api/llmcore.LLMService to the
// compat/protocol.ProtocolAdapter interface.
//
// Supported endpoints (within the raw body):
//   - POST chat/completions     — OpenAI v1/v2 Chat Completions (full request/response + SSE)
//   - POST completions          — Legacy v1 Completions (text-davinci style)
//   - POST embeddings           — OpenAI Embeddings
//   - GET  models               — List available models
//   - GET  models/{id}          — Retrieve a specific model
//   - POST images/generations   — Image generation (stub)
//   - POST audio/transcriptions — Audio transcription (stub)
//   - POST moderations          — Content moderation (stub)
//
// All stubs return a proper OpenAI-format error response.
package openaiapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Timwood0x10/ares/compat/protocol"
	llmcore "github.com/Timwood0x10/ares/internal/llmcore"
)

const (
	defaultEndpoint    = "chat/completions"
	responsesEndpoint  = "responses"
	embeddingsEndpoint = "embeddings"
	streamObject       = "chat.completion.chunk"
	finishReasonStop   = "stop"
	roleAssistant      = "assistant"
)

// ── Adapter ────────────────────────────────────────────────────────────────

// Adapter satisfies compat/protocol.ProtocolAdapter for the OpenAI API format.
type Adapter struct {
	svc    llmcore.LLMService
	models []ModelInfo
}

// New constructs an Adapter from a raw config map.
//
// Recognized keys:
//
//	service llmcore.LLMService — REQUIRED. The LLM service to proxy requests to.
//	models  []ModelInfo     — optional. Pre-configured model list for GET /models.
func New(config map[string]any) (*Adapter, error) {
	svc, _ := config["service"].(llmcore.LLMService)
	if svc == nil {
		return nil, fmt.Errorf("compat/protocol/openai_api: service is required")
	}
	var modelList []ModelInfo
	if ml, ok := config["models"].([]ModelInfo); ok {
		modelList = ml
	}
	return &Adapter{svc: svc, models: modelList}, nil
}

// ── ProtocolAdapter ────────────────────────────────────────────────────────

// Serve dispatches a raw request to the appropriate handler based on the
// body structure. The endpoint is inferred from the request fields.
func (a *Adapter) Serve(ctx context.Context, raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("compat/protocol/openai_api: empty request body")
	}
	ep := detectEndpoint(raw)
	switch ep {
	case "models":
		return a.handleModels(ctx, raw)
	case embeddingsEndpoint:
		return a.handleEmbeddings(ctx, raw)
	case "completions":
		return a.handleLegacyCompletions(ctx, raw)
	case responsesEndpoint:
		return a.handleResponses(ctx, raw)
	case "images/generations":
		return a.handleImageGeneration(ctx, raw)
	case "audio/transcriptions":
		return a.handleAudioTranscription(ctx, raw)
	case "moderations":
		return a.handleModeration(ctx, raw)
	default:
		return a.handleChatCompletions(ctx, raw)
	}
}

// Name returns the canonical protocol name.
func (*Adapter) Name() string { return "openai_api" }

// ContentType returns the MIME type this adapter produces.
func (*Adapter) ContentType() string { return "application/json" }

// Compile-time interface assertion.
var _ protocol.ProtocolAdapter = (*Adapter)(nil)

// ── Endpoint detection ─────────────────────────────────────────────────────

func detectEndpoint(raw []byte) string {
	var env struct {
		Model        string          `json:"model"`
		Messages     json.RawMessage `json:"messages"`
		Prompt       json.RawMessage `json:"prompt"`
		Input        json.RawMessage `json:"input"`
		Instructions string          `json:"instructions"`
		Image        string          `json:"image"`
		File         json.RawMessage `json:"file"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return defaultEndpoint
	}
	if len(env.Messages) > 0 {
		return defaultEndpoint
	}
	if env.Image != "" {
		return "images/generations"
	}
	if len(env.File) > 0 {
		return "audio/transcriptions"
	}
	if len(env.Prompt) > 0 {
		return "completions"
	}
	if len(env.Input) > 0 {
		// Responses API requests are identified by Responses-specific fields
		// such as instructions.
		if env.Instructions != "" {
			return responsesEndpoint
		}
		// Disambiguate by input shape: a bare string `input` is legal for both
		// the Responses API (chat) and the Embeddings API (single document);
		// route string inputs by model prefix so embedding models reach the
		// embeddings handler. An array `input` is legal for BOTH Embeddings
		// (batch) and Responses (multi-turn), so route by model prefix the
		// same way — only embedding-prefixed models go to embeddings.
		var inputStr string
		if json.Unmarshal(env.Input, &inputStr) == nil {
			if strings.HasPrefix(env.Model, "text-embedding-") {
				return "embeddings"
			}
			return responsesEndpoint
		}
		// Array input: same model-prefix disambiguation.
		if strings.HasPrefix(env.Model, "text-embedding-") {
			return embeddingsEndpoint
		}
		return responsesEndpoint
	}
	return defaultEndpoint
}

// ── Error types (OpenAI standard v1) ───────────────────────────────────────

// ErrorResponse matches the standard OpenAI API error envelope.
type ErrorResponse struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param,omitempty"`
	Code    string `json:"code,omitempty"`
}

func newError(message, errType, code string) []byte {
	out, _ := json.Marshal(ErrorResponse{
		Error: errorBody{Message: message, Type: errType, Code: code},
	})
	return out
}

// ── Chat Completions (v1/v2) — Request types ───────────────────────────────

// chatRequest covers both v1 and v2/v3 Chat Completions parameters.
type chatRequest struct {
	Model               string             `json:"model"`
	Messages            []chatMessage      `json:"messages"`
	Temperature         *float64           `json:"temperature,omitempty"`
	TopP                *float64           `json:"top_p,omitempty"`
	N                   *int               `json:"n,omitempty"`
	Stream              bool               `json:"stream,omitempty"`
	Stop                []string           `json:"stop,omitempty"`
	MaxTokens           *int               `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int               `json:"max_completion_tokens,omitempty"`
	PresencePenalty     *float64           `json:"presence_penalty,omitempty"`
	FrequencyPenalty    *float64           `json:"frequency_penalty,omitempty"`
	LogitBias           map[string]int     `json:"logit_bias,omitempty"`
	Logprobs            *bool              `json:"logprobs,omitempty"`
	TopLogprobs         *int               `json:"top_logprobs,omitempty"`
	Seed                *int64             `json:"seed,omitempty"`
	User                string             `json:"user,omitempty"`
	Tools               []toolDef          `json:"tools,omitempty"`
	ToolChoice          json.RawMessage    `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool              `json:"parallel_tool_calls,omitempty"`
	ResponseFormat      *responseFormatObj `json:"response_format,omitempty"`
	ServiceTier         string             `json:"service_tier,omitempty"`
	Store               *bool              `json:"store,omitempty"`
	Metadata            map[string]string  `json:"metadata,omitempty"`
	ReasoningEffort     string             `json:"reasoning_effort,omitempty"`
}

type responseFormatObj struct {
	Type       string           `json:"type"`
	JSONSchema *json.RawMessage `json:"json_schema,omitempty"`
}

type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"` // string or array of contentPart
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
	Name       string          `json:"name,omitempty"`
	Refusal    string          `json:"refusal,omitempty"`
}

type contentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *imageURLPart `json:"image_url,omitempty"`
	Refusal  string        `json:"refusal,omitempty"`
}

type imageURLPart struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type toolDef struct {
	Type     string      `json:"type"`
	Function functionDef `json:"function"`
}

type functionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// ── Chat Completions — Response types (v1-compatible) ──────────────────────

type chatResponse struct {
	ID                string   `json:"id"`
	Object            string   `json:"object"`
	Created           int64    `json:"created"`
	Model             string   `json:"model"`
	Choices           []choice `json:"choices"`
	Usage             *usageV2 `json:"usage,omitempty"`
	SystemFingerprint string   `json:"system_fingerprint,omitempty"`
	ServiceTier       string   `json:"service_tier,omitempty"`
}

type choice struct {
	Index        int           `json:"index"`
	Message      responseMsg   `json:"message"`
	FinishReason string        `json:"finish_reason"`
	Logprobs     *logprobsData `json:"logprobs,omitempty"`
}

type responseMsg struct {
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	ToolCalls []toolCallResp `json:"tool_calls,omitempty"`
	Refusal   string         `json:"refusal,omitempty"`
}

type toolCallResp struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function functionCallResp `json:"function"`
}

type functionCallResp struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type logprobsData struct {
	Content []logprobToken `json:"content"`
}

type logprobToken struct {
	Token       string            `json:"token"`
	Logprob     float64           `json:"logprob"`
	Bytes       []int             `json:"bytes,omitempty"`
	TopLogprobs []topLogprobEntry `json:"top_logprobs,omitempty"`
}

type topLogprobEntry struct {
	Token   string  `json:"token"`
	Logprob float64 `json:"logprob"`
	Bytes   []int   `json:"bytes,omitempty"`
}

// usageV2 — v2 usage with detailed breakdowns; v1 clients ignore extra fields.
type usageV2 struct {
	PromptTokens            int                      `json:"prompt_tokens"`
	CompletionTokens        int                      `json:"completion_tokens"`
	TotalTokens             int                      `json:"total_tokens"`
	CompletionTokensDetails *completionTokensDetails `json:"completion_tokens_details,omitempty"`
	PromptTokensDetails     *promptTokensDetails     `json:"prompt_tokens_details,omitempty"`
}

type completionTokensDetails struct {
	ReasoningTokens          int `json:"reasoning_tokens,omitempty"`
	AudioTokens              int `json:"audio_tokens,omitempty"`
	AcceptedPredictionTokens int `json:"accepted_prediction_tokens,omitempty"`
	RejectedPredictionTokens int `json:"rejected_prediction_tokens,omitempty"`
}

type promptTokensDetails struct {
	AudioTokens  int `json:"audio_tokens,omitempty"`
	CachedTokens int `json:"cached_tokens,omitempty"`
}

// ── SSE streaming types ────────────────────────────────────────────────────

type streamChunk struct {
	ID                string         `json:"id"`
	Object            string         `json:"object"`
	Created           int64          `json:"created"`
	Model             string         `json:"model"`
	Choices           []streamChoice `json:"choices"`
	SystemFingerprint string         `json:"system_fingerprint,omitempty"`
	Usage             *usageV2       `json:"usage,omitempty"`
}

type streamChoice struct {
	Index        int           `json:"index"`
	Delta        streamDelta   `json:"delta"`
	FinishReason string        `json:"finish_reason,omitempty"`
	Logprobs     *logprobsData `json:"logprobs,omitempty"`
}

type streamDelta struct {
	Role      string          `json:"role,omitempty"`
	Content   string          `json:"content,omitempty"`
	ToolCalls json.RawMessage `json:"tool_calls,omitempty"`
	Refusal   string          `json:"refusal,omitempty"`
}

// ── Legacy Completions (v1 /completions) — Request/Response types ──────────

type legacyCompletionRequest struct {
	Model            string          `json:"model"`
	Prompt           json.RawMessage `json:"prompt"`
	Suffix           string          `json:"suffix,omitempty"`
	MaxTokens        *int            `json:"max_tokens,omitempty"`
	Temperature      *float64        `json:"temperature,omitempty"`
	TopP             *float64        `json:"top_p,omitempty"`
	N                *int            `json:"n,omitempty"`
	Stream           bool            `json:"stream,omitempty"`
	Logprobs         *int            `json:"logprobs,omitempty"`
	Echo             *bool           `json:"echo,omitempty"`
	Stop             []string        `json:"stop,omitempty"`
	PresencePenalty  *float64        `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64        `json:"frequency_penalty,omitempty"`
	BestOf           *int            `json:"best_of,omitempty"`
	LogitBias        map[string]int  `json:"logit_bias,omitempty"`
	User             string          `json:"user,omitempty"`
	Seed             *int64          `json:"seed,omitempty"`
}

type legacyCompletionResponse struct {
	ID                string                   `json:"id"`
	Object            string                   `json:"object"`
	Created           int64                    `json:"created"`
	Model             string                   `json:"model"`
	Choices           []legacyCompletionChoice `json:"choices"`
	Usage             *usageV2                 `json:"usage,omitempty"`
	SystemFingerprint string                   `json:"system_fingerprint,omitempty"`
}

type legacyCompletionChoice struct {
	Text         string          `json:"text"`
	Index        int             `json:"index"`
	Logprobs     *legacyLogprobs `json:"logprobs,omitempty"`
	FinishReason string          `json:"finish_reason"`
}

type legacyLogprobs struct {
	Tokens        []string             `json:"tokens"`
	TokenLogprobs []float64            `json:"token_logprobs"`
	TopLogprobs   []map[string]float64 `json:"top_logprobs"`
	TextOffset    []int                `json:"text_offset"`
}

// ── Embeddings types ───────────────────────────────────────────────────────

type embeddingRequest struct {
	Input          json.RawMessage `json:"input"`
	Model          string          `json:"model"`
	EncodingFormat string          `json:"encoding_format,omitempty"`
	Dimensions     *int            `json:"dimensions,omitempty"`
	User           string          `json:"user,omitempty"`
}

type embeddingResponse struct {
	Object string          `json:"object"`
	Data   []embeddingData `json:"data"`
	Model  string          `json:"model"`
	Usage  embeddingUsage  `json:"usage"`
}

type embeddingData struct {
	Object    string    `json:"object"`
	Index     int       `json:"index"`
	Embedding []float64 `json:"embedding"`
}

type embeddingUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// ── Models types ───────────────────────────────────────────────────────────

type ModelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type modelListResponse struct {
	Object string      `json:"object"`
	Data   []ModelInfo `json:"data"`
}

type modelRetrieveResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// ── Image Generation types (v1/images/generations) ─────────────────────────

type imageGenRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n,omitempty"`
	Quality        string `json:"quality,omitempty"`
	Size           string `json:"size,omitempty"`
	Style          string `json:"style,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
	User           string `json:"user,omitempty"`
}

// ── Audio Transcription types (v1/audio/transcriptions) ────────────────────

type audioTranscriptionRequest struct {
	File                   json.RawMessage `json:"file"`
	Model                  string          `json:"model"`
	Language               string          `json:"language,omitempty"`
	Prompt                 string          `json:"prompt,omitempty"`
	ResponseFormat         string          `json:"response_format,omitempty"`
	Temperature            *float64        `json:"temperature,omitempty"`
	TimestampGranularities []string        `json:"timestamp_granularities,omitempty"`
}

// ── Moderation types (v1/moderations) ──────────────────────────────────────

type moderationRequest struct {
	Input string `json:"input"`
	Model string `json:"model,omitempty"`
}

// ── Chat Completions handler ────────────────────────────────────────────────

func (a *Adapter) handleChatCompletions(ctx context.Context, raw []byte) ([]byte, error) {
	var req chatRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return newError(fmt.Sprintf("invalid request: %v", err), "invalid_request_error", "invalid_request"), nil
	}
	if len(req.Messages) == 0 {
		return newError("messages must not be empty", "invalid_request_error", "missing_messages"), nil
	}
	if req.Model == "" {
		req.Model = a.svc.GetModel()
	}

	messages, err := mapMessages(req.Messages)
	if err != nil {
		return newError(fmt.Sprintf("invalid messages: %v", err), "invalid_request_error", "invalid_messages"), nil
	}

	genReq := &llmcore.GenerateRequest{
		Messages:       messages,
		Model:          req.Model,
		Temperature:    req.Temperature,
		MaxTokens:      req.MaxTokens,
		Stream:         req.Stream,
		Tools:          mapToolDefs(req.Tools),
		TopP:           req.TopP,
		Stop:           req.Stop,
		Seed:           req.Seed,
		User:           req.User,
		Metadata:       req.Metadata,
		ToolChoice:     req.ToolChoice,
		ResponseFormat: mapResponseFormat(req.ResponseFormat),
	}

	if req.Stream {
		return a.handleStreamingChat(ctx, genReq, req)
	}

	genResp, err := a.svc.Generate(ctx, genReq)
	if err != nil {
		return newError(fmt.Sprintf("generate: %v", err), "server_error", "internal_error"), nil
	}

	resp := buildChatResponse(genResp, req.Model, a.svc.GetConfig())
	out, err := json.Marshal(resp)
	if err != nil {
		return newError("internal server error", "server_error", "internal_error"), nil
	}
	return out, nil
}

func mapMessages(msgs []chatMessage) ([]*llmcore.LLMMessage, error) {
	messages := make([]*llmcore.LLMMessage, 0, len(msgs))
	for _, m := range msgs {
		content := extractContent(m.Content)
		llmMsg := &llmcore.LLMMessage{
			Role:       m.Role,
			Content:    content,
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
		}
		if len(m.ToolCalls) > 0 {
			var toolCalls []llmcore.ToolCall
			if err := json.Unmarshal(m.ToolCalls, &toolCalls); err == nil {
				llmMsg.ToolCalls = toolCalls
			}
		}
		messages = append(messages, llmMsg)
	}
	return messages, nil
}

func extractContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Text != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(p.Text)
			}
			if p.Refusal != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString("[refusal: " + p.Refusal + "]")
			}
		}
		return b.String()
	}
	return string(raw)
}

func mapToolDefs(tools []toolDef) []llmcore.Tool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]llmcore.Tool, 0, len(tools))
	for _, t := range tools {
		td := llmcore.Tool{
			Type: t.Type,
			Function: llmcore.FunctionDefinition{
				Name:        t.Function.Name,
				Description: t.Function.Description,
			},
		}
		if t.Function.Parameters != nil {
			var params map[string]interface{}
			if err := json.Unmarshal(t.Function.Parameters, &params); err == nil {
				td.Function.Parameters = params
			}
		}
		out = append(out, td)
	}
	return out
}

func mapResponseFormat(rf *responseFormatObj) json.RawMessage {
	if rf == nil {
		return nil
	}
	data, _ := json.Marshal(rf)
	return data
}

func buildChatResponse(genResp *llmcore.GenerateResponse, model string, cfg *llmcore.LLMConfig) chatResponse {
	now := time.Now().Unix()
	respMsg := responseMsg{
		Role:    roleAssistant,
		Content: genResp.Content,
	}
	if len(genResp.ToolCalls) > 0 {
		respMsg.ToolCalls = make([]toolCallResp, 0, len(genResp.ToolCalls))
		for _, tc := range genResp.ToolCalls {
			respMsg.ToolCalls = append(respMsg.ToolCalls, toolCallResp{
				ID:   tc.ID,
				Type: tc.Type,
				Function: functionCallResp{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			})
		}
	}

	finishReason := genResp.FinishReason
	if finishReason == "" {
		finishReason = finishReasonStop
	}

	resp := chatResponse{
		// Nano-precision timestamp avoids same-second ID collisions when
		// multiple completions are generated within one second.
		ID:      fmt.Sprintf("chatcmpl-%x", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: now,
		Model:   model,
		Choices: []choice{
			{
				Index:        0,
				Message:      respMsg,
				FinishReason: finishReason,
			},
		},
		SystemFingerprint: fmt.Sprintf("fp_%x", now),
	}
	if cfg != nil && cfg.Provider != "" {
		resp.ServiceTier = string(cfg.Provider)
	}

	if genResp.Usage.TotalTokens > 0 {
		u := &usageV2{
			PromptTokens:     genResp.Usage.PromptTokens,
			CompletionTokens: genResp.Usage.CompletionTokens,
			TotalTokens:      genResp.Usage.TotalTokens,
		}
		resp.Usage = u
	}

	return resp
}

// ── Streaming handler ──────────────────────────────────────────────────────

func (a *Adapter) handleStreamingChat(ctx context.Context, genReq *llmcore.GenerateRequest, req chatRequest) ([]byte, error) {
	resp, err := a.svc.Generate(ctx, genReq)
	if err != nil {
		return newError(fmt.Sprintf("generate: %v", err), "server_error", "internal_error"), nil
	}

	now := time.Now().Unix()
	model := req.Model
	if model == "" {
		model = a.svc.GetModel()
	}

	var buf bytes.Buffer

	roleChunk := streamChunk{
		ID:      fmt.Sprintf("chatcmpl-%x", now),
		Object:  streamObject,
		Created: now,
		Model:   model,
		Choices: []streamChoice{
			{Index: 0, Delta: streamDelta{Role: roleAssistant}},
		},
		SystemFingerprint: fmt.Sprintf("fp_%x", now),
	}
	roleData, _ := json.Marshal(roleChunk)
	buf.WriteString("data: ")
	buf.Write(roleData)
	buf.WriteString("\n\n")

	contentChunk := streamChunk{
		ID:      roleChunk.ID,
		Object:  streamObject,
		Created: now,
		Model:   model,
		Choices: []streamChoice{
			{Index: 0, Delta: streamDelta{Content: resp.Content}},
		},
	}
	contentData, _ := json.Marshal(contentChunk)
	buf.WriteString("data: ")
	buf.Write(contentData)
	buf.WriteString("\n\n")

	if len(resp.ToolCalls) > 0 {
		tcData, _ := json.Marshal(resp.ToolCalls)
		toolChunk := streamChunk{
			ID:      roleChunk.ID,
			Object:  streamObject,
			Created: now,
			Model:   model,
			Choices: []streamChoice{
				{Index: 0, Delta: streamDelta{ToolCalls: tcData}},
			},
		}
		toolData, _ := json.Marshal(toolChunk)
		buf.WriteString("data: ")
		buf.Write(toolData)
		buf.WriteString("\n\n")
	}

	finishReason := resp.FinishReason
	if finishReason == "" {
		finishReason = finishReasonStop
	}
	finalChunk := streamChunk{
		ID:      roleChunk.ID,
		Object:  streamObject,
		Created: now,
		Model:   model,
		Choices: []streamChoice{
			{Index: 0, Delta: streamDelta{}, FinishReason: finishReason},
		},
	}
	if resp.Usage.TotalTokens > 0 {
		finalChunk.Usage = &usageV2{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		}
	}
	finalData, _ := json.Marshal(finalChunk)
	buf.WriteString("data: ")
	buf.Write(finalData)
	buf.WriteString("\n\n")
	buf.WriteString("data: [DONE]\n\n")

	return buf.Bytes(), nil
}

// ── Legacy Completions handler (v1 /completions) ───────────────────────────

func (a *Adapter) handleLegacyCompletions(ctx context.Context, raw []byte) ([]byte, error) {
	var req legacyCompletionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return newError(fmt.Sprintf("invalid request: %v", err), "invalid_request_error", "invalid_request"), nil
	}
	if len(req.Prompt) == 0 {
		return newError("prompt must not be empty", "invalid_request_error", "missing_prompt"), nil
	}

	// Extract prompt text.
	promptText := extractPrompt(req.Prompt)

	genResp, err := a.svc.GenerateSimple(ctx, promptText)
	if err != nil {
		return newError(fmt.Sprintf("generate: %v", err), "server_error", "internal_error"), nil
	}

	model := req.Model
	if model == "" {
		model = a.svc.GetModel()
	}
	now := time.Now().Unix()

	resp := legacyCompletionResponse{
		ID:      fmt.Sprintf("cmpl-%x", now),
		Object:  "text_completion",
		Created: now,
		Model:   model,
		Choices: []legacyCompletionChoice{
			{
				Text:         genResp,
				Index:        0,
				FinishReason: finishReasonStop,
			},
		},
		SystemFingerprint: fmt.Sprintf("fp_%x", now),
	}

	out, err := json.Marshal(resp)
	if err != nil {
		return newError("internal server error", "server_error", "internal_error"), nil
	}
	return out, nil
}

func extractPrompt(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
		return arr[0]
	}
	return string(raw)
}

// ── Responses API handler (v1/responses) ───────────────────────────────────

// responsesRequest matches the OpenAI Responses API (POST /v1/responses).
// See https://platform.openai.com/docs/api-reference/responses
type responsesRequest struct {
	Model           string            `json:"model"`
	Input           json.RawMessage   `json:"input"`
	Instructions    string            `json:"instructions,omitempty"`
	MaxOutputTokens *int              `json:"max_output_tokens,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	TopP            *float64          `json:"top_p,omitempty"`
	Tools           []toolDef         `json:"tools,omitempty"`
	ToolChoice      json.RawMessage   `json:"tool_choice,omitempty"`
	Store           *bool             `json:"store,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	User            string            `json:"user,omitempty"`
}

// responsesResponse matches the OpenAI Responses API response shape.
type responsesResponse struct {
	ID      string                `json:"id"`
	Object  string                `json:"object"`
	Created int64                 `json:"created"`
	Model   string                `json:"model"`
	Output  []responsesOutputItem `json:"output"`
	Usage   *usageV2              `json:"usage,omitempty"`
}

type responsesOutputItem struct {
	Type    string                  `json:"type"`
	ID      string                  `json:"id"`
	Status  string                  `json:"status,omitempty"`
	Role    string                  `json:"role,omitempty"`
	Content []responsesContentBlock `json:"content,omitempty"`
}

type responsesContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// Future: Refusal, ToolCall, etc.
}

func (a *Adapter) handleResponses(ctx context.Context, raw []byte) ([]byte, error) {
	var req responsesRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return newError(fmt.Sprintf("invalid request: %v", err), "invalid_request_error", "invalid_request"), nil
	}
	if len(req.Input) == 0 {
		return newError("input must not be empty", "invalid_request_error", "missing_input"), nil
	}

	// Extract the input text (string or array of message items).
	prompt := extractInputText(req.Input)
	if prompt == "" {
		return newError("input must contain text", "invalid_request_error", "invalid_input"), nil
	}

	// Build the full prompt with instructions if provided.
	fullPrompt := prompt
	if req.Instructions != "" {
		fullPrompt = req.Instructions + "\n\n" + prompt
	}

	model := req.Model
	if model == "" {
		model = a.svc.GetModel()
	}

	genReq := &llmcore.GenerateRequest{
		Model:       model,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxOutputTokens,
		TopP:        req.TopP,
		Tools:       mapToolDefs(req.Tools),
		ToolChoice:  req.ToolChoice,
		Metadata:    req.Metadata,
		User:        req.User,
		Messages: []*llmcore.LLMMessage{
			{Role: "user", Content: fullPrompt},
		},
	}

	genResp, err := a.svc.Generate(ctx, genReq)
	if err != nil {
		return newError(fmt.Sprintf("generate: %v", err), "server_error", "internal_error"), nil
	}

	now := time.Now().Unix()
	resp := responsesResponse{
		ID:      fmt.Sprintf("resp_%x", now),
		Object:  "response",
		Created: now,
		Model:   model,
		Output: []responsesOutputItem{
			{
				Type:   "message",
				ID:     fmt.Sprintf("msg_%x", now+1),
				Status: "completed",
				Role:   roleAssistant,
				Content: []responsesContentBlock{
					{Type: "output_text", Text: genResp.Content},
				},
			},
		},
	}
	if genResp.Usage.TotalTokens > 0 {
		resp.Usage = &usageV2{
			PromptTokens:     genResp.Usage.PromptTokens,
			CompletionTokens: genResp.Usage.CompletionTokens,
			TotalTokens:      genResp.Usage.TotalTokens,
		}
	}

	out, err := json.Marshal(resp)
	if err != nil {
		return newError("internal server error", "server_error", "internal_error"), nil
	}
	return out, nil
}

func extractInputText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Try array of input items (each with role+content).
	var items []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &items); err == nil && len(items) > 0 {
		var b strings.Builder
		for _, it := range items {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(it.Content)
		}
		return b.String()
	}
	return string(raw)
}

// ── Embeddings handler ─────────────────────────────────────────────────────

func (a *Adapter) handleEmbeddings(ctx context.Context, raw []byte) ([]byte, error) {
	var req embeddingRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return newError(fmt.Sprintf("invalid request: %v", err), "invalid_request_error", "invalid_request"), nil
	}
	if len(req.Input) == 0 {
		return newError("input must not be empty", "invalid_request_error", "missing_input"), nil
	}

	var text string
	if err := json.Unmarshal(req.Input, &text); err != nil {
		var texts []string
		if err := json.Unmarshal(req.Input, &texts); err == nil && len(texts) > 0 {
			text = texts[0]
		}
	}
	if text == "" {
		text = string(req.Input)
	}

	model := req.Model
	if model == "" {
		model = a.svc.GetModel()
	}

	embReq := &llmcore.EmbeddingRequest{Input: text, Model: model}
	embResp, err := a.svc.GenerateEmbedding(ctx, embReq)
	if err != nil {
		return newError(fmt.Sprintf("embedding: %v", err), "server_error", "internal_error"), nil
	}

	vec := make([]float64, len(embResp.Embedding))
	for i, v := range embResp.Embedding {
		vec[i] = float64(v)
	}

	resp := embeddingResponse{
		Object: "list",
		Data: []embeddingData{
			{Object: "embedding", Index: 0, Embedding: vec},
		},
		Model: model,
		Usage: embeddingUsage{
			PromptTokens: embResp.Usage.PromptTokens,
			TotalTokens:  embResp.Usage.TotalTokens,
		},
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return newError("internal server error", "server_error", "internal_error"), nil
	}
	return out, nil
}

// ── Models handler ─────────────────────────────────────────────────────────

func (a *Adapter) handleModels(ctx context.Context, raw []byte) ([]byte, error) {
	var modelID string
	var idReq struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(raw, &idReq) == nil && idReq.Model != "" {
		modelID = idReq.Model
	}
	if modelID != "" {
		return a.handleModelRetrieve(ctx, modelID)
	}
	return a.handleModelList(ctx)
}

func (a *Adapter) handleModelList(ctx context.Context) ([]byte, error) {
	data := a.models
	if data == nil {
		cfg := a.svc.GetConfig()
		if cfg != nil && cfg.Model != "" {
			data = []ModelInfo{
				{
					ID:      cfg.Model,
					Object:  "model",
					Created: time.Now().Unix(),
					OwnedBy: string(cfg.Provider),
				},
			}
		}
	}
	if data == nil {
		data = []ModelInfo{}
	}
	resp := modelListResponse{Object: "list", Data: data}
	out, err := json.Marshal(resp)
	if err != nil {
		return newError("internal server error", "server_error", "internal_error"), nil
	}
	return out, nil
}

func (a *Adapter) handleModelRetrieve(ctx context.Context, modelID string) ([]byte, error) {
	for _, m := range a.models {
		if m.ID == modelID {
			out, err := json.Marshal(m)
			if err != nil {
				return newError("internal server error", "server_error", "internal_error"), nil
			}
			return out, nil
		}
	}
	cfg := a.svc.GetConfig()
	if cfg != nil && cfg.Model == modelID {
		resp := modelRetrieveResponse{
			ID:      modelID,
			Object:  "model",
			Created: time.Now().Unix(),
			OwnedBy: string(cfg.Provider),
		}
		out, err := json.Marshal(resp)
		if err != nil {
			return newError("internal server error", "server_error", "internal_error"), nil
		}
		return out, nil
	}
	return newError(fmt.Sprintf("model %q not found", modelID), "invalid_request_error", "model_not_found"), nil
}

// ── Image Generation handler (stub) ────────────────────────────────────────

func (a *Adapter) handleImageGeneration(ctx context.Context, raw []byte) ([]byte, error) {
	var req imageGenRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return newError(fmt.Sprintf("invalid request: %v", err), "invalid_request_error", "invalid_request"), nil
	}
	if req.Prompt == "" {
		return newError("prompt must not be empty", "invalid_request_error", "missing_prompt"), nil
	}
	return newError("image generation is not available in this runtime", "invalid_request_error", "not_implemented"), nil
}

// ── Audio Transcription handler (stub) ─────────────────────────────────────

func (a *Adapter) handleAudioTranscription(ctx context.Context, raw []byte) ([]byte, error) {
	var req audioTranscriptionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return newError(fmt.Sprintf("invalid request: %v", err), "invalid_request_error", "invalid_request"), nil
	}
	if len(req.File) == 0 {
		return newError("file must not be empty", "invalid_request_error", "missing_file"), nil
	}
	return newError("audio transcription is not available in this runtime", "invalid_request_error", "not_implemented"), nil
}

// ── Moderation handler (stub) ──────────────────────────────────────────────

func (a *Adapter) handleModeration(ctx context.Context, raw []byte) ([]byte, error) {
	var req moderationRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return newError(fmt.Sprintf("invalid request: %v", err), "invalid_request_error", "invalid_request"), nil
	}
	if req.Input == "" {
		return newError("input must not be empty", "invalid_request_error", "missing_input"), nil
	}
	return newError("moderation is not available in this runtime", "invalid_request_error", "not_implemented"), nil
}
