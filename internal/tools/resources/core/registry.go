package core

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"

	llmcore "github.com/Timwood0x10/ares/api/core"
	gerr "github.com/Timwood0x10/ares/internal/errors"
)

// Registry manages tool registration and lookup.
type Registry struct {
	tools       map[string]Tool
	mu          sync.RWMutex
	schemaCache []ToolSchema
	schemaDirty bool
	onChange    []func() // callbacks invoked after Register/Unregister

	// activeTools restricts which tools GetSchemas/GetLLMTools expose to the
	// LLM. nil means "all tools active" (zero-value backwards compatible);
	// a non-nil set means only the listed names are advertised, mirroring
	// prime-agent's active-tools subset (progressive disclosure). Guarded by
	// mu. Execute/Get/List still see the full registry — this only controls
	// what is offered to the model.
	activeTools map[string]bool
}

// NewRegistry creates a new Registry.
func NewRegistry() *Registry {
	return &Registry{
		tools:       make(map[string]Tool),
		schemaDirty: true,
	}
}

// OnChange registers a callback that is invoked after every Register/Unregister.
// The callback is called while the write lock is held — keep it fast.
func (r *Registry) OnChange(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onChange = append(r.onChange, fn)
}

// SetActiveTools restricts which tools GetSchemas/GetLLMTools expose to the
// LLM. Passing an empty slice (or nil) restores the zero-value behavior where
// all registered tools are advertised. Tools not in the active set remain
// registered and executable via Execute/Get — only the LLM-facing tool list is
// narrowed, mirroring prime-agent's active-tools subset.
func (r *Registry) SetActiveTools(names []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(names) == 0 {
		r.activeTools = nil
	} else {
		set := make(map[string]bool, len(names))
		for _, name := range names {
			set[name] = true
		}
		r.activeTools = set
	}
	// Invalidate the schema cache in BOTH branches: restoring the full set
	// must also drop the previously narrowed cache, otherwise GetSchemas
	// returns the stale subset even though activeTools is nil again.
	r.schemaCache = nil
	r.schemaDirty = true
}

// ActiveTools returns the currently active tool names. An empty slice means
// all registered tools are active (zero-value behavior).
func (r *Registry) ActiveTools() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.activeTools == nil {
		return nil
	}
	names := make([]string, 0, len(r.activeTools))
	for name := range r.activeTools {
		names = append(names, name)
	}
	return names
}

// ClearActiveTools removes the active-tool restriction, restoring the
// zero-value behavior where all registered tools are advertised to the LLM.
func (r *Registry) ClearActiveTools() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.activeTools = nil
	r.schemaCache = nil
	r.schemaDirty = true
}

// Register registers a tool.
func (r *Registry) Register(tool Tool) error {
	r.mu.Lock()

	if tool == nil {
		r.mu.Unlock()
		return ErrNilTool
	}

	name := tool.Name()
	if _, exists := r.tools[name]; exists {
		r.mu.Unlock()
		return gerr.Wrap(ErrToolAlreadyRegistered, name)
	}

	r.tools[name] = tool
	r.schemaDirty = true
	// Collect onChange callbacks before releasing the lock to avoid
	// deadlock when a callback itself needs to read from the registry (T-04).
	callbacks := make([]func(), len(r.onChange))
	copy(callbacks, r.onChange)
	r.mu.Unlock()

	for _, fn := range callbacks {
		fn()
	}
	return nil
}

// Unregister removes a tool from the registry.
func (r *Registry) Unregister(name string) error {
	r.mu.Lock()

	if _, exists := r.tools[name]; !exists {
		r.mu.Unlock()
		return gerr.Wrap(ErrToolNotFound, name)
	}

	delete(r.tools, name)
	r.schemaDirty = true
	callbacks := make([]func(), len(r.onChange))
	copy(callbacks, r.onChange)
	r.mu.Unlock()

	for _, fn := range callbacks {
		fn()
	}
	return nil
}

// Get retrieves a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tool, exists := r.tools[name]
	return tool, exists
}

// List returns all registered tool names.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}

	return names
}

// Count returns the number of registered tools.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.tools)
}

// Execute executes a tool by name after validating parameters.
func (r *Registry) Execute(ctx context.Context, name string, params map[string]interface{}) (Result, error) {
	tool, exists := r.Get(name)
	if !exists {
		return Result{}, gerr.Wrap(ErrToolNotFound, name)
	}

	if err := ValidateParams(tool.Parameters(), params); err != nil {
		return NewErrorResult(err.Error()), nil
	}

	return tool.Execute(ctx, params)
}

// Clear removes all tools.
func (r *Registry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.tools = make(map[string]Tool)
	r.schemaCache = nil
	r.schemaDirty = true
}

// Filter returns tools that match the given filter criteria.
func (r *Registry) Filter(filter *ToolFilter) *Registry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// If filter is nil, return all tools (deep copy to avoid data race)
	if filter == nil {
		toolsCopy := make(map[string]Tool, len(r.tools))
		for k, v := range r.tools {
			toolsCopy[k] = v
		}
		return &Registry{
			tools:       toolsCopy,
			schemaDirty: true,
		}
	}

	filtered := NewRegistry()

	for name, tool := range r.tools {
		// Check if tool is in enabled list
		if len(filter.Enabled) > 0 && !containsString(filter.Enabled, name) {
			continue
		}

		// Check if tool is in disabled list - if so, exclude it
		if len(filter.Disabled) > 0 && containsString(filter.Disabled, name) {
			continue
		}

		// Check category filter
		if len(filter.Categories) > 0 && !containsCategory(filter.Categories, tool.Category()) {
			continue
		}

		// Register tool in filtered registry
		filtered.tools[name] = tool
	}

	return filtered
}

// FilterByCategory returns tools of a specific category.
func (r *Registry) FilterByCategory(category ToolCategory) *Registry {
	return r.Filter(&ToolFilter{
		Categories: []ToolCategory{category},
	})
}

// GetSchemas returns schema information for all tools in the registry.
func (r *Registry) GetSchemas() []ToolSchema {
	r.mu.RLock()
	if !r.schemaDirty && r.schemaCache != nil {
		defer r.mu.RUnlock()
		return r.schemaCache
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.schemaDirty || r.schemaCache == nil {
		schemas := make([]ToolSchema, 0, len(r.tools))
		for _, tool := range r.tools {
			// Respect the active-tools subset: tools excluded from the active
			// set are not advertised to the LLM (progressive disclosure).
			// Execute/Get still resolve them; only the LLM-facing list is cut.
			if r.activeTools != nil && !r.activeTools[tool.Name()] {
				continue
			}
			schema := ToolSchema{
				Name:        tool.Name(),
				Description: tool.Description(),
				Category:    tool.Category(),
				Parameters:  tool.Parameters(),
			}
			if tt, ok := tool.(TaggableTool); ok {
				schema.Tags = tt.Tags()
			}
			schemas = append(schemas, schema)
		}

		r.schemaCache = schemas
		r.schemaDirty = false
	}

	return r.schemaCache
}

// GetLLMTools converts all registered tools to api/core.Tool format
// for passing to the LLM Chat API. Uses ToolSchemaToLLMTool internally.
func (r *Registry) GetLLMTools() []llmcore.Tool {
	schemas := r.GetSchemas()
	tools := make([]llmcore.Tool, 0, len(schemas))
	for _, schema := range schemas {
		tools = append(tools, ToolSchemaToLLMTool(schema))
	}
	return tools
}

// FindByTags returns tools whose tags match ALL specified key-value pairs.
// Use "*" as value to match any tool that has the key regardless of value.
//
// Example:
//
//	r.FindByTags(map[string]string{"domain": "math", "side_effects": "false"})
//	r.FindByTags(map[string]string{"domain": "*"}) // all tools with domain tag
func (r *Registry) FindByTags(tags map[string]string) []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Tool
	for _, tool := range r.tools {
		tt, ok := tool.(TaggableTool)
		if !ok {
			continue
		}
		toolTags := tt.Tags()
		match := true
		for key, val := range tags {
			tagVal, exists := toolTags[key]
			if !exists {
				match = false
				break
			}
			if val != "*" && tagVal != val {
				match = false
				break
			}
		}
		if match {
			result = append(result, tool)
		}
	}
	return result
}

// ToolFilter defines filter criteria for tools.
type ToolFilter struct {
	Enabled    []string       // List of enabled tool names (if not empty, only these tools are included)
	Disabled   []string       // List of disabled tool names (these tools are excluded)
	Categories []ToolCategory // List of allowed categories (if not empty, only these categories are included)
}

// containsString checks if a string is in a slice.
func containsString(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// containsCategory checks if a category is in a slice.
func containsCategory(slice []ToolCategory, item ToolCategory) bool {
	for _, c := range slice {
		if c == item {
			return true
		}
	}
	return false
}

// Registry errors.
var (
	ErrNilTool               = errors.New("tool is nil")
	ErrToolNotFound          = errors.New("tool not found")
	ErrToolAlreadyRegistered = errors.New("tool already registered")
)

// GlobalRegistry is the default tool registry.
//
// Deprecated: GlobalRegistry is no longer populated by production code
// (no production caller invokes Register). Use a *Registry
// instance created via NewRegistry and passed through dependency injection
// instead.
var GlobalRegistry = NewRegistry()

// Register registers a tool in the global registry.
//
// Deprecated: Register operates on the empty GlobalRegistry. Use a *Registry
// instance created via NewRegistry and passed through dependency injection
// instead.
func Register(tool Tool) error {
	return GlobalRegistry.Register(tool)
}

// Get retrieves a tool from the global registry.
//
// Deprecated: Get operates on the empty GlobalRegistry. Use a *Registry
// instance created via NewRegistry and passed through dependency injection
// instead.
func Get(name string) (Tool, bool) {
	return GlobalRegistry.Get(name)
}

// List returns all tools from the global registry.
//
// Deprecated: List operates on the empty GlobalRegistry. Use a *Registry
// instance created via NewRegistry and passed through dependency injection
// instead.
func List() []string {
	return GlobalRegistry.List()
}

// Execute executes a tool from the global registry.
//
// Deprecated: Execute operates on the empty GlobalRegistry. Use a *Registry
// instance created via NewRegistry and passed through dependency injection
// instead.
func Execute(ctx context.Context, name string, params map[string]interface{}) (Result, error) {
	return GlobalRegistry.Execute(ctx, name, params)
}

// ToolGroup groups related tools.
type ToolGroup struct {
	name        string
	description string
	registry    *Registry
}

// NewToolGroup creates a new ToolGroup.
func NewToolGroup(name, description string) *ToolGroup {
	return &ToolGroup{
		name:        name,
		description: description,
		registry:    NewRegistry(),
	}
}

// Register registers a tool in the group.
func (g *ToolGroup) Register(tool Tool) error {
	return g.registry.Register(tool)
}

// Get retrieves a tool from the group.
func (g *ToolGroup) Get(name string) (Tool, bool) {
	return g.registry.Get(name)
}

// List returns all tool names in the group.
func (g *ToolGroup) List() []string {
	return g.registry.List()
}

// Name returns the group name.
func (g *ToolGroup) Name() string {
	return g.name
}

// Description returns the group description.
func (g *ToolGroup) Description() string {
	return g.description
}

// ValidateParams checks params against a ParameterSchema before tool execution.
// Returns nil if valid, or an error describing the first violation found.
//
// Checks:
//   - Required params exist
//   - Each param's Go type matches the schema type
//   - If enum is defined, the value is one of the allowed values
//
// This prevents LLM-generated type mismatches (e.g., string vs number)
// from causing panics inside tool Execute methods.
func ValidateParams(schema *ParameterSchema, params map[string]interface{}) error {
	if schema == nil {
		return nil // no schema = no validation
	}

	// Check required params.
	for _, required := range schema.Required {
		if _, exists := params[required]; !exists {
			return fmt.Errorf("validation: missing required parameter %q", required)
		}
		if params[required] == nil {
			return fmt.Errorf("validation: required parameter %q is nil", required)
		}
	}

	// Type-check and enum-check each param against the schema.
	for key, value := range params {
		paramDef, defined := schema.Properties[key]
		if !defined {
			continue // unknown param, skip (tool can ignore)
		}

		// Type check.
		if err := checkType(paramDef.Type, value); err != nil {
			return fmt.Errorf("validation: parameter %q: %w", key, err)
		}

		// Enum check.
		if len(paramDef.Enum) > 0 {
			if err := checkEnum(paramDef.Enum, value); err != nil {
				return fmt.Errorf("validation: parameter %q: %w", key, err)
			}
		}
	}

	return nil
}

// checkType verifies a value matches the expected schema type.
func checkType(expected string, value interface{}) error {
	if value == nil {
		return nil // nil is allowed for non-required params
	}
	switch expected {
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("expected string, got %T", value)
		}
	case "integer":
		switch v := value.(type) {
		case int, int64:
			return nil
		case float64:
			// JSON unmarshal decodes numbers as float64. Accept only whole numbers.
			if v != math.Trunc(v) {
				return fmt.Errorf("expected integer, got float %v", v)
			}
			return nil
		default:
			return fmt.Errorf("expected integer, got %T", value)
		}
	case "number":
		switch value.(type) {
		case float64, int, int64:
			return nil
		default:
			return fmt.Errorf("expected number, got %T", value)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("expected boolean, got %T", value)
		}
	case "array":
		if _, ok := value.([]interface{}); !ok {
			return fmt.Errorf("expected array, got %T", value)
		}
	}
	return nil
}

// checkEnum verifies a value is one of the allowed enum values.
func checkEnum(allowed []interface{}, value interface{}) error {
	for _, a := range allowed {
		if a == value {
			return nil
		}
		// String comparison for JSON-derived values.
		vs, okS := value.(string)
		as, okA := a.(string)
		if okS && okA && vs == as {
			return nil
		}
	}
	return fmt.Errorf("value %v is not in allowed enum %v", value, allowed)
}
