package engine

import (
	"bufio"
	"context"
	stderrors "errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/Timwood0x10/ares/internal/errors"
)

// Definition errors.
var (
	ErrFieldNotFound            = stderrors.New("field not found")
	ErrDuplicateAgentDefinition = stderrors.New("duplicate agent definition")
)

// Precompiled regex patterns for definition parsing.
var (
	rePromptSection = regexp.MustCompile(`(?i)^##\s*Prompt\s*:\s*(\w+)\s*$`)
	reToolsSection  = regexp.MustCompile(`(?i)^##\s*Tools\s*$`)
	reToolItem      = regexp.MustCompile(`^\s*-\s*(.+?)$`)
)

// AgentDefinition represents an agent definition from markdown.
type AgentDefinition struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Description string            `json:"description"`
	Prompts     map[string]string `json:"prompts"`
	Tools       []string          `json:"tools"`
	Metadata    map[string]string `json:"metadata"`
}

// DefinitionParser parses agent definitions from markdown files.
type DefinitionParser struct {
}

// NewDefinitionParser creates a new DefinitionParser.
func NewDefinitionParser() *DefinitionParser {
	return &DefinitionParser{}
}

// Parse parses an agent definition from a reader.
func (p *DefinitionParser) Parse(ctx context.Context, r io.Reader) (*AgentDefinition, error) {
	content, err := io.ReadAll(r)
	if err != nil {
		return nil, errors.Wrap(err, "read content")
	}

	return p.ParseBytes(ctx, content)
}

// ParseBytes parses an agent definition from bytes.
func (p *DefinitionParser) ParseBytes(ctx context.Context, content []byte) (*AgentDefinition, error) {
	if len(content) == 0 {
		return nil, errors.New("definition content is empty")
	}

	text := string(content)

	def := &AgentDefinition{
		Prompts:  make(map[string]string),
		Tools:    make([]string, 0),
		Metadata: make(map[string]string),
	}

	name, err := p.extractField(text, "name")
	if err != nil {
		return nil, errors.Wrap(err, "extract name")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name field is required but empty or whitespace-only")
	}
	def.Name = name

	agentType, err := p.extractField(text, "type")
	if err != nil {
		return nil, errors.Wrap(err, "extract type")
	}
	agentType = strings.TrimSpace(agentType)
	if agentType == "" {
		return nil, errors.New("type field is required but empty or whitespace-only")
	}
	def.Type = agentType

	description, err := p.extractField(text, "description")
	if err == nil {
		def.Description = description
	}

	def.Prompts = p.extractPrompts(text)

	def.Tools = p.extractTools(text)

	def.Metadata = p.extractMetadata(text)

	return def, nil
}

// ParseFile parses an agent definition from a file.
func (p *DefinitionParser) ParseFile(ctx context.Context, path string) (*AgentDefinition, error) {
	file, err := os.Open(path) // #nosec G304
	if err != nil {
		return nil, errors.Wrapf(err, "open file %s", path)
	}
	defer func() {
		if err := file.Close(); err != nil {
			log.Warn("definition: close file failed", "error", err)
		}
	}()

	return p.Parse(ctx, bufio.NewReader(file))
}

// extractField extracts a field value from markdown.
func (p *DefinitionParser) extractField(content, field string) (string, error) {
	patterns := []string{
		fmt.Sprintf(`(?i)%s\s*::\s*(.+?)(?:\n|$)`, field),
		fmt.Sprintf(`(?i)%s\s*:\s*(.+?)(?:\n|$)`, field),
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindStringSubmatch(content)
		if len(matches) > 1 {
			return strings.TrimSpace(matches[1]), nil
		}
	}

	return "", ErrFieldNotFound
}

// extractPrompts extracts prompts from markdown.
func (p *DefinitionParser) extractPrompts(content string) map[string]string {
	prompts := make(map[string]string)

	// Use a simpler regex pattern that doesn't require lookahead
	lines := strings.Split(content, "\n")
	var currentPrompt string
	var promptContent strings.Builder
	inPromptSection := false

	for _, line := range lines {
		if strings.HasPrefix(line, "##") {
			// New section
			if inPromptSection && currentPrompt != "" {
				prompts[currentPrompt] = strings.TrimSpace(promptContent.String())
			}
			currentPrompt = ""
			promptContent.Reset()
			inPromptSection = false

			// Check if this is a Prompt section
			promptMatch := rePromptSection.FindStringSubmatch(line)
			if len(promptMatch) > 1 {
				currentPrompt = strings.TrimSpace(promptMatch[1])
				inPromptSection = true
			}
		} else if inPromptSection {
			promptContent.WriteString(line + "\n")
		}
	}

	// Don't forget the last section
	if inPromptSection && currentPrompt != "" {
		prompts[currentPrompt] = strings.TrimSpace(promptContent.String())
	}

	return prompts
}

// extractTools extracts tool names from markdown.
func (p *DefinitionParser) extractTools(content string) []string {
	tools := make([]string, 0)

	lines := strings.Split(content, "\n")
	inToolsSection := false

	for _, line := range lines {
		if strings.HasPrefix(line, "##") {
			// New section
			inToolsSection = false

			// Check if this is a Tools section
			if reToolsSection.MatchString(strings.TrimSpace(line)) {
				inToolsSection = true
			}
		} else if inToolsSection {
			// Look for tool list items
			toolMatch := reToolItem.FindStringSubmatch(line)
			if len(toolMatch) > 1 {
				toolName := strings.TrimSpace(toolMatch[1])
				if toolName != "" {
					tools = append(tools, toolName)
				}
			}
		}
	}

	return tools
}

// extractMetadata extracts metadata from markdown.
func (p *DefinitionParser) extractMetadata(content string) map[string]string {
	metadata := make(map[string]string)

	lines := strings.Split(content, "\n")
	inMetadataSection := false

	for _, line := range lines {
		if strings.HasPrefix(line, "##") {
			// New section
			inMetadataSection = false

			// Check if this is a Metadata section
			if regexp.MustCompile(`(?i)^##\s*Metadata\s*$`).MatchString(strings.TrimSpace(line)) {
				inMetadataSection = true
			}
		} else if inMetadataSection {
			// Look for metadata key-value pairs
			metaMatch := regexp.MustCompile(`^\s*-\s*(\w+)\s*:\s*(.+?)$`).FindStringSubmatch(line)
			if len(metaMatch) > 2 {
				key := strings.TrimSpace(metaMatch[1])
				value := strings.TrimSpace(metaMatch[2])
				metadata[key] = value
			}
		}
	}

	return metadata
}

// DirectoryParser parses agent definitions from a directory.
type DirectoryParser struct {
	parser *DefinitionParser
}

// NewDirectoryParser creates a new DirectoryParser.
func NewDirectoryParser(parser *DefinitionParser) *DirectoryParser {
	return &DirectoryParser{
		parser: parser,
	}
}

// ParseAll parses all agent definitions from a directory.
func (p *DirectoryParser) ParseAll(ctx context.Context, dir string) (map[string]*AgentDefinition, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, errors.Wrapf(err, "read directory %s", dir)
	}

	definitions := make(map[string]*AgentDefinition)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		path := dir + "/" + entry.Name()
		def, err := p.parser.ParseFile(ctx, path)
		if err != nil {
			return nil, errors.Wrapf(err, "parse file %s", path)
		}

		if _, exists := definitions[def.Name]; exists {
			return nil, ErrDuplicateAgentDefinition
		}

		definitions[def.Name] = def
	}

	return definitions, nil
}
