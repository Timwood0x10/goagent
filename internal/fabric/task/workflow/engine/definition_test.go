// nolint: errcheck // Test code may ignore return values
package engine

import (
	"context"
	"strings"
	"testing"
)

// =====================================================
// DefinitionParser Coverage Tests
// =====================================================

//nolint:gocyclo // Test function with comprehensive test cases
func TestDefinitionParserCoverage(t *testing.T) {
	t.Run("create definition parser", func(t *testing.T) {
		parser := NewDefinitionParser()
		if parser == nil {
			t.Error("Parser should not be nil")
		}
	})

	t.Run("parse from bytes with minimal valid content", func(t *testing.T) {
		parser := NewDefinitionParser()

		content := []byte(`name: test-agent
type: leader
`)

		def, err := parser.ParseBytes(context.Background(), content)
		if err != nil {
			t.Fatalf("ParseBytes error: %v", err)
		}

		if def.Name != "test-agent" {
			t.Errorf("Expected name 'test-agent', got %s", def.Name)
		}

		if def.Type != "leader" {
			t.Errorf("Expected type 'leader', got %s", def.Type)
		}
	})

	t.Run("parse from bytes with full content", func(t *testing.T) {
		parser := NewDefinitionParser()

		content := []byte(`name: test-agent
type: leader
description: A test agent for unit testing
## Prompt: system
This is the system prompt
## Prompt: user
This is the user prompt
## Tools
- tool1
- tool2
- tool3
## Metadata
- key1: value1
- key2: value2
`)

		def, err := parser.ParseBytes(context.Background(), content)
		if err != nil {
			t.Fatalf("ParseBytes error: %v", err)
		}

		if def.Name != "test-agent" {
			t.Errorf("Expected name 'test-agent', got %s", def.Name)
		}

		if def.Type != "leader" {
			t.Errorf("Expected type 'leader', got %s", def.Type)
		}

		if def.Description != "A test agent for unit testing" {
			t.Errorf("Expected description, got %s", def.Description)
		}

		if len(def.Prompts) != 2 {
			t.Errorf("Expected 2 prompts, got %d", len(def.Prompts))
		}

		if len(def.Tools) != 3 {
			t.Errorf("Expected 3 tools, got %d", len(def.Tools))
		}

		if len(def.Metadata) != 2 {
			t.Errorf("Expected 2 metadata items, got %d", len(def.Metadata))
		}
	})

	t.Run("parse from reader", func(t *testing.T) {
		parser := NewDefinitionParser()

		content := strings.NewReader(`name: test-agent
type: leader
`)

		def, err := parser.Parse(context.Background(), content)
		if err != nil {
			t.Fatalf("Parse error: %v", err)
		}

		if def.Name != "test-agent" {
			t.Errorf("Expected name 'test-agent', got %s", def.Name)
		}
	})

	t.Run("parse with missing required field", func(t *testing.T) {
		parser := NewDefinitionParser()

		content := []byte(`name: test-agent
`)

		_, err := parser.ParseBytes(context.Background(), content)
		if err == nil {
			t.Error("Expected error when type field is missing")
		}
	})

	t.Run("parse with invalid field pattern", func(t *testing.T) {
		parser := NewDefinitionParser()

		content := []byte(`name test-agent
type leader
`)

		_, err := parser.ParseBytes(context.Background(), content)
		if err == nil {
			t.Error("Expected error with invalid field pattern")
		}
	})

	t.Run("extract field with different patterns", func(t *testing.T) {
		parser := NewDefinitionParser()

		// Test pattern 1: name :: value
		content1 := `name :: test-agent
type :: leader
`
		def1, err := parser.ParseBytes(context.Background(), []byte(content1))
		if err != nil {
			t.Fatalf("ParseBytes error: %v", err)
		}
		if def1.Name != "test-agent" {
			t.Errorf("Expected name 'test-agent', got %s", def1.Name)
		}

		// Test pattern 2: name : value
		content2 := `name : test-agent
type : leader
`
		def2, err := parser.ParseBytes(context.Background(), []byte(content2))
		if err != nil {
			t.Fatalf("ParseBytes error: %v", err)
		}
		if def2.Name != "test-agent" {
			t.Errorf("Expected name 'test-agent', got %s", def2.Name)
		}
	})

	t.Run("parse prompts section", func(t *testing.T) {
		parser := NewDefinitionParser()

		content := []byte(`name: test-agent
type: leader
## Prompt: system
System prompt content
## Prompt: user
User prompt content
`)

		def, err := parser.ParseBytes(context.Background(), content)
		if err != nil {
			t.Fatalf("ParseBytes error: %v", err)
		}

		if len(def.Prompts) != 2 {
			t.Errorf("Expected 2 prompts, got %d", len(def.Prompts))
		}

		if _, exists := def.Prompts["system"]; !exists {
			t.Error("System prompt should exist")
		}

		if _, exists := def.Prompts["user"]; !exists {
			t.Error("User prompt should exist")
		}
	})

	t.Run("parse tools section", func(t *testing.T) {
		parser := NewDefinitionParser()

		content := []byte(`name: test-agent
type: leader
## Tools
- search
- weather
- database
`)

		def, err := parser.ParseBytes(context.Background(), content)
		if err != nil {
			t.Fatalf("ParseBytes error: %v", err)
		}

		if len(def.Tools) != 3 {
			t.Errorf("Expected 3 tools, got %d", len(def.Tools))
		}

		expectedTools := []string{"search", "weather", "database"}
		for i, tool := range def.Tools {
			if tool != expectedTools[i] {
				t.Errorf("Expected tool %s, got %s", expectedTools[i], tool)
			}
		}
	})

	t.Run("parse metadata section", func(t *testing.T) {
		parser := NewDefinitionParser()

		content := []byte(`name: test-agent
type: leader
## Metadata
- version: 1.0
- author: test
`)

		def, err := parser.ParseBytes(context.Background(), content)
		if err != nil {
			t.Fatalf("ParseBytes error: %v", err)
		}

		if len(def.Metadata) != 2 {
			t.Errorf("Expected 2 metadata items, got %d", len(def.Metadata))
		}

		if def.Metadata["version"] != "1.0" {
			t.Errorf("Expected version 1.0, got %s", def.Metadata["version"])
		}

		if def.Metadata["author"] != "test" {
			t.Errorf("Expected author test, got %s", def.Metadata["author"])
		}
	})

	t.Run("parse with empty content", func(t *testing.T) {
		parser := NewDefinitionParser()

		_, err := parser.ParseBytes(context.Background(), []byte{})
		if err == nil {
			t.Error("Expected error with empty content")
		}
	})

	t.Run("parse with case insensitive fields", func(t *testing.T) {
		parser := NewDefinitionParser()

		content := []byte(`NAME: test-agent
TYPE: leader
`)

		def, err := parser.ParseBytes(context.Background(), content)
		if err != nil {
			t.Fatalf("ParseBytes error: %v", err)
		}

		if def.Name != "test-agent" {
			t.Errorf("Expected name 'test-agent', got %s", def.Name)
		}

		if def.Type != "leader" {
			t.Errorf("Expected type 'leader', got %s", def.Type)
		}
	})
}

// =====================================================
// DirectoryParser Coverage Tests
// =====================================================

func TestDirectoryParserCoverage(t *testing.T) {
	t.Run("create directory parser", func(t *testing.T) {
		parser := NewDefinitionParser()
		_ = NewDirectoryParser(parser)

		// Directory parser created successfully
	})

	t.Run("parse directory with non-existent path", func(t *testing.T) {
		parser := NewDefinitionParser()
		dirParser := NewDirectoryParser(parser)

		_, err := dirParser.ParseAll(context.Background(), "/non/existent/path")
		if err == nil {
			t.Error("Expected error with non-existent directory")
		}
		_ = dirParser // Use the variable
	})

	t.Run("parse directory with invalid files", func(t *testing.T) {
		parser := NewDefinitionParser()
		dirParser := NewDirectoryParser(parser)

		// Try to parse a system directory
		_, err := dirParser.ParseAll(context.Background(), "/tmp")
		if err != nil {
			t.Logf("Expected error parsing /tmp: %v", err)
		}
		_ = dirParser // Use the variable
	})

	t.Run("handle duplicate agent definitions", func(t *testing.T) {
		parser := NewDefinitionParser()
		_ = NewDirectoryParser(parser)

		// This would require creating a directory with duplicate definitions
		// For now, we just verify the error is defined
		if ErrDuplicateAgentDefinition == nil {
			t.Error("ErrDuplicateAgentDefinition should not be nil")
		}
	})
}

// =====================================================
// Extract Functions Coverage Tests
// =====================================================

func TestExtractFunctionsCoverage(t *testing.T) {
	t.Run("extract field not found", func(t *testing.T) {
		parser := NewDefinitionParser()

		content := `name: test-agent
type: leader
`

		_, err := parser.extractField(content, "nonexistent")
		if err != ErrFieldNotFound {
			t.Errorf("Expected ErrFieldNotFound, got %v", err)
		}
	})

	t.Run("extract field with multiple patterns", func(t *testing.T) {
		parser := NewDefinitionParser()

		content := `name::test-agent
description::test description
`

		name, err := parser.extractField(content, "name")
		if err != nil {
			t.Fatalf("Extract field error: %v", err)
		}

		if name != "test-agent" {
			t.Errorf("Expected 'test-agent', got %s", name)
		}

		description, err := parser.extractField(content, "description")
		if err != nil {
			t.Fatalf("Extract field error: %v", err)
		}

		if description != "test description" {
			t.Errorf("Expected 'test description', got %s", description)
		}
	})

	t.Run("extract prompts with complex content", func(t *testing.T) {
		parser := NewDefinitionParser()

		content := `## Prompt: system
This is a multi-line system prompt.
It has multiple lines.
## Prompt: user
User prompt here.
`

		prompts := parser.extractPrompts(content)

		if len(prompts) != 2 {
			t.Errorf("Expected 2 prompts, got %d", len(prompts))
		}

		if !strings.Contains(prompts["system"], "multi-line") {
			t.Error("System prompt should contain 'multi-line'")
		}
	})

	t.Run("extract tools with spacing", func(t *testing.T) {
		parser := NewDefinitionParser()

		content := `## Tools
- tool1
  - tool2
-   tool3
`

		tools := parser.extractTools(content)

		if len(tools) != 3 {
			t.Errorf("Expected 3 tools, got %d", len(tools))
		}
	})

	t.Run("extract metadata with various formats", func(t *testing.T) {
		parser := NewDefinitionParser()

		content := `## Metadata
- key1: value1
- key2: value2 with spaces
- key3: value3
`

		metadata := parser.extractMetadata(content)

		if len(metadata) != 3 {
			t.Errorf("Expected 3 metadata items, got %d", len(metadata))
		}

		if metadata["key2"] != "value2 with spaces" {
			t.Errorf("Expected 'value2 with spaces', got %s", metadata["key2"])
		}
	})

	t.Run("extract with no matches", func(t *testing.T) {
		parser := NewDefinitionParser()

		content := `name: test-agent
type: leader
`

		prompts := parser.extractPrompts(content)
		if len(prompts) != 0 {
			t.Errorf("Expected 0 prompts, got %d", len(prompts))
		}

		tools := parser.extractTools(content)
		if len(tools) != 0 {
			t.Errorf("Expected 0 tools, got %d", len(tools))
		}

		metadata := parser.extractMetadata(content)
		if len(metadata) != 0 {
			t.Errorf("Expected 0 metadata items, got %d", len(metadata))
		}
	})
}

// =====================================================
// AgentDefinition Coverage Tests
// =====================================================

func TestAgentDefinitionCoverage(t *testing.T) {
	t.Run("create agent definition", func(t *testing.T) {
		def := &AgentDefinition{
			Name:        "test-agent",
			Type:        "leader",
			Description: "Test agent",
			Prompts:     map[string]string{"system": "test"},
			Tools:       []string{"tool1"},
			Metadata:    map[string]string{"key": "value"},
		}

		if def.Name != "test-agent" {
			t.Errorf("Expected name 'test-agent', got %s", def.Name)
		}

		if len(def.Prompts) != 1 {
			t.Errorf("Expected 1 prompt, got %d", len(def.Prompts))
		}

		if len(def.Tools) != 1 {
			t.Errorf("Expected 1 tool, got %d", len(def.Tools))
		}
	})

	t.Run("create agent definition with empty fields", func(t *testing.T) {
		def := &AgentDefinition{
			Name:     "test-agent",
			Type:     "leader",
			Prompts:  make(map[string]string),
			Tools:    make([]string, 0),
			Metadata: make(map[string]string),
		}

		if len(def.Prompts) != 0 {
			t.Errorf("Expected 0 prompts, got %d", len(def.Prompts))
		}

		if len(def.Tools) != 0 {
			t.Errorf("Expected 0 tools, got %d", len(def.Tools))
		}

		if len(def.Metadata) != 0 {
			t.Errorf("Expected 0 metadata items, got %d", len(def.Metadata))
		}
	})
}

// nolint: errcheck // Test code may ignore return values
