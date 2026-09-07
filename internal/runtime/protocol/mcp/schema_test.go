package ares_mcp

import (
	"encoding/json"
	"testing"
)

func TestConvertJSONSchema(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantType string
		wantErr  bool
		checkFn  func(t *testing.T, schema interface{})
	}{
		{
			name:     "empty schema",
			input:    "",
			wantType: "object",
		},
		{
			name:     "null schema",
			input:    "null",
			wantType: "object",
		},
		{
			name: "simple object",
			input: `{
				"type": "object",
				"properties": {
					"name": {"type": "string", "description": "User name"},
					"age": {"type": "integer", "description": "User age", "minimum": 0, "maximum": 150}
				},
				"required": ["name"]
			}`,
			wantType: "object",
			checkFn: func(t *testing.T, v interface{}) {
				schema, ok := v.(interface {
					GetProperties() map[string]interface{ GetType() string }
				})
				_ = schema
				_ = ok
			},
		},
		{
			name: "with enum",
			input: `{
				"type": "object",
				"properties": {
					"color": {"type": "string", "enum": ["red", "green", "blue"]}
				}
			}`,
			wantType: "object",
		},
		{
			name:    "invalid json",
			input:   "{invalid",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw json.RawMessage
			if tt.input != "" && tt.input != "null" {
				raw = json.RawMessage(tt.input)
			}

			result, err := ConvertJSONSchema(raw)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Type != tt.wantType {
				t.Errorf("Type = %s, want %s", result.Type, tt.wantType)
			}
			if tt.checkFn != nil {
				tt.checkFn(t, result)
			}
		})
	}
}

func TestConvertJSONSchemaProperties(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "The name"},
			"count": {"type": "integer", "minimum": 0, "maximum": 100},
			"tag": {"type": "string", "enum": ["a", "b", "c"], "default": "a"}
		},
		"required": ["name"]
	}`

	schema, err := ConvertJSONSchema(json.RawMessage(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(schema.Required) != 1 || schema.Required[0] != "name" {
		t.Errorf("Required = %v, want [name]", schema.Required)
	}

	if len(schema.Properties) != 3 {
		t.Fatalf("Properties count = %d, want 3", len(schema.Properties))
	}

	// Check name property.
	nameProp, ok := schema.Properties["name"]
	if !ok {
		t.Fatal("missing 'name' property")
	}
	if nameProp.Type != "string" {
		t.Errorf("name.Type = %s, want string", nameProp.Type)
	}
	if nameProp.Description != "The name" {
		t.Errorf("name.Description = %s, want 'The name'", nameProp.Description)
	}

	// Check count property with min/max.
	countProp, ok := schema.Properties["count"]
	if !ok {
		t.Fatal("missing 'count' property")
	}
	if countProp.Min == nil || *countProp.Min != 0 {
		t.Errorf("count.Min = %v, want 0", countProp.Min)
	}
	if countProp.Max == nil || *countProp.Max != 100 {
		t.Errorf("count.Max = %v, want 100", countProp.Max)
	}

	// Check tag property with enum and default.
	tagProp, ok := schema.Properties["tag"]
	if !ok {
		t.Fatal("missing 'tag' property")
	}
	if len(tagProp.Enum) != 3 {
		t.Errorf("tag.Enum count = %d, want 3", len(tagProp.Enum))
	}
	if tagProp.Default != "a" {
		t.Errorf("tag.Default = %v, want a", tagProp.Default)
	}
}

func TestConvertJSONSchemaDefaultType(t *testing.T) {
	// Property without type should default to "string".
	input := `{
		"type": "object",
		"properties": {
			"unknown": {"description": "no type specified"}
		}
	}`

	schema, err := ConvertJSONSchema(json.RawMessage(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	prop, ok := schema.Properties["unknown"]
	if !ok {
		t.Fatal("missing 'unknown' property")
	}
	if prop.Type != "string" {
		t.Errorf("default type = %s, want string", prop.Type)
	}
}

func TestConvertJSONSchemaEmptyObject(t *testing.T) {
	input := `{"type": "object"}`
	schema, err := ConvertJSONSchema(json.RawMessage(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if schema.Properties == nil {
		t.Error("Properties should not be nil for empty object")
	}
}
