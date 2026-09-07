package ares_mcp

import (
	"encoding/json"
	"fmt"

	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// JSON Schema type constants
const (
	jsonSchemaTypeObject = "object"
	jsonSchemaTypeString = "string"
)

// jsonSchema represents a parsed JSON Schema object.
type jsonSchema struct {
	Type        string                `json:"type"`
	Properties  map[string]jsonSchema `json:"properties"`
	Required    []string              `json:"required"`
	Description string                `json:"description"`
	Enum        []interface{}         `json:"enum"`
	Minimum     *float64              `json:"minimum,omitempty"`
	Maximum     *float64              `json:"maximum,omitempty"`
	Default     interface{}           `json:"default,omitempty"`
	Items       *jsonSchema           `json:"items,omitempty"`
}

// ConvertJSONSchema converts a raw JSON Schema (from MCP inputSchema) to a core.ParameterSchema.
func ConvertJSONSchema(raw json.RawMessage) (*core.ParameterSchema, error) {
	if len(raw) == 0 {
		return &core.ParameterSchema{
			Type:       jsonSchemaTypeObject,
			Properties: make(map[string]*core.Parameter),
		}, nil
	}

	var schema jsonSchema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("unmarshal json schema: %w", err)
	}

	if schema.Type == "" {
		schema.Type = jsonSchemaTypeObject
	}

	result := &core.ParameterSchema{
		Type:       schema.Type,
		Properties: make(map[string]*core.Parameter),
		Required:   schema.Required,
	}

	for name, prop := range schema.Properties {
		param := convertProperty(prop)
		result.Properties[name] = param
	}

	return result, nil
}

// convertProperty converts a single JSON Schema property to a core.Parameter.
func convertProperty(schema jsonSchema) *core.Parameter {
	param := &core.Parameter{
		Type:        schema.Type,
		Description: schema.Description,
		Default:     schema.Default,
	}

	if len(schema.Enum) > 0 {
		param.Enum = make([]interface{}, len(schema.Enum))
		copy(param.Enum, schema.Enum)
	}

	if schema.Minimum != nil {
		param.Min = schema.Minimum
	}

	if schema.Maximum != nil {
		param.Max = schema.Maximum
	}

	if param.Type == "" {
		param.Type = jsonSchemaTypeString
	}

	return param
}
