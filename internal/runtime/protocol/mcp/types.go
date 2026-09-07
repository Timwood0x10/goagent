package ares_mcp

import "encoding/json"

// ProtocolVersion is the MCP protocol version supported.
const ProtocolVersion = "2024-11-05"

// ClientName identifies this MCP client implementation.
const ClientName = "ares"

// ClientVersion is the version reported during MCP initialization.
const ClientVersion = "1.0.0"

// --- Initialize handshake types ---

// Implementation identifies an MCP client or server.
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ClientCapabilities declares what the client supports.
type ClientCapabilities struct {
	Tools *ToolClientCapabilities `json:"tools,omitempty"`
}

// ToolClientCapabilities declares tool-related client capabilities.
type ToolClientCapabilities struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// InitializeParams holds parameters for the initialize request.
type InitializeParams struct {
	ProtocolVersion string             `json:"protocolVersion"`
	ClientInfo      Implementation     `json:"clientInfo"`
	Capabilities    ClientCapabilities `json:"capabilities"`
}

// ServerCapabilities declares what the server supports.
type ServerCapabilities struct {
	Tools     *ToolServerCapabilities     `json:"tools,omitempty"`
	Resources *ResourceServerCapabilities `json:"resources,omitempty"`
	Prompts   *PromptServerCapabilities   `json:"prompts,omitempty"`
}

// ToolServerCapabilities declares tool-related server capabilities.
type ToolServerCapabilities struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ResourceServerCapabilities declares resource-related server capabilities.
type ResourceServerCapabilities struct {
	Subscribe   bool `json:"subscribe,omitempty"`
	ListChanged bool `json:"listChanged,omitempty"`
}

// PromptServerCapabilities declares prompt-related server capabilities.
type PromptServerCapabilities struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// InitializeResult holds the server's response to initialize.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	ServerInfo      Implementation     `json:"serverInfo"`
	Capabilities    ServerCapabilities `json:"capabilities"`
}

// --- Tool types ---

// MCPToolDef represents a tool definition returned by the MCP server.
type MCPToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ToolsListResult holds the result of tools/list.
type ToolsListResult struct {
	Tools []MCPToolDef `json:"tools"`
}

// ToolCallParams holds parameters for tools/call.
type ToolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// ToolCallResult holds the result of tools/call.
type ToolCallResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ContentBlock represents a piece of content in a tool call result.
type ContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"`
	URI      string `json:"uri,omitempty"`
}

// --- Notification types ---

// NotificationMethod constants for MCP notifications.
const (
	NotificationInitialized      = "notifications/initialized"
	NotificationToolsListChanged = "notifications/tools/list_changed"
)

// --- Method constants ---

// Method constants for MCP JSON-RPC methods.
const (
	MethodInitialize    = "initialize"
	MethodToolsList     = "tools/list"
	MethodToolsCall     = "tools/call"
	MethodPing          = "ping"
	MethodResourcesList = "resources/list"
	MethodResourcesRead = "resources/read"
	MethodPromptsList   = "prompts/list"
	MethodPromptsGet    = "prompts/get"
)

// PingResult is an empty response to a ping.
type PingResult struct{}

// --- Resource types ---

// ResourceContent represents a piece of resource content.
type ResourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
}

// ReadResourceResult holds the result of resources/read.
type ReadResourceResult struct {
	Contents []ResourceContent `json:"contents"`
}

// ResourceItem describes a single resource.
type ResourceItem struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// ListResourcesResult holds the result of resources/list.
type ListResourcesResult struct {
	Resources []ResourceItem `json:"resources"`
}

// ResourceTemplate describes a resource URI template.
type ResourceTemplate struct {
	URITemplate string `json:"uriTemplate"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// ListResourceTemplatesResult holds the result of resources/templates/list.
type ListResourceTemplatesResult struct {
	ResourceTemplates []ResourceTemplate `json:"resourceTemplates"`
}

// --- Prompt types ---

// PromptArgument defines an argument for a prompt.
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required"`
}

// PromptMessage represents a message in a prompt result.
type PromptMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// GetPromptResult holds the result of prompts/get.
type GetPromptResult struct {
	Description string          `json:"description,omitempty"`
	Messages    []PromptMessage `json:"messages"`
}

// PromptItem describes a single prompt.
type PromptItem struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ListPromptsResult holds the result of prompts/list.
type ListPromptsResult struct {
	Prompts []PromptItem `json:"prompts"`
}
