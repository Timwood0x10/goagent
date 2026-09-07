// Package ares_skills implements the ARES Capability Fabric (0.3.0): a small
// abstraction that treats a Skill as a Capability Package (SKILL.md +
// references + tool declarations) rather than a Tool. The implementation layer
// is deliberately limited to SkillCatalog / SkillLoader / ToolResolver — no
// SkillManager / Orchestrator / Marketplace.
//
// Design pillars (ares-capability-fabric-design.md):
//  1. Only declared sources are scanned — never a full-disk find.
//  2. A Skill is a capability package; a Tool is its execution carrier.
//  3. MCP servers are lazy-loaded at skill activation, not pre-connected.
//  4. Content is progressively disclosed: metadata → SKILL.md → resources.
//  5. Discovery, loading, execution and trust are four separate concerns.
package ares_skills

// SourceKind classifies a skill discovery source.
type SourceKind string

const (
	// SourceProject is the project-local ".ares/skills" directory.
	SourceProject SourceKind = "project"
	// SourceUser is the user-global "~/.ares/skills" directory.
	SourceUser SourceKind = "user"
	// SourceRegistered is an extra directory declared in config.toml.
	SourceRegistered SourceKind = "registered"
	// SourceExperience is a learned relevance prior (never auto-executed).
	SourceExperience SourceKind = "experience"
)

// SkillIndexEntry is the metadata-only index record (Level 0 of progressive
// disclosure). The SKILL.md body is deliberately NOT loaded here so that 100
// skills cost ~100 x 100 tokens instead of 100 full instruction bodies.
type SkillIndexEntry struct {
	// ID is the stable identifier (e.g. "rust-review").
	ID string `json:"id"`
	// Name is the human-readable name (e.g. "Rust Code Review").
	Name string `json:"name"`
	// Description is the always-resident one-liner.
	Description string `json:"description"`
	// Keywords are search terms (e.g. ["rust","ownership","unsafe"]).
	Keywords []string `json:"keywords"`
	// Source is where the skill was declared.
	Source SourceKind `json:"source"`
	// Path is the skill directory (e.g. ".ares/skills/rust-review").
	Path string `json:"path"`
	// Version is the manifest version (e.g. "1.0.0").
	Version string `json:"version"`
	// Capabilities are capability labels (e.g. ["code-review","security"]).
	Capabilities []string `json:"capabilities"`
	// ToolTypes are the tool kinds declared by the manifest (e.g. ["mcp","executable"]).
	ToolTypes []string `json:"tool_types"`
	// Hash is the content hash used for change detection.
	Hash string `json:"hash"`
}

// ExperienceRecord maps a task pattern to a useful skill with a success rate.
// It is a relevance prior only: a learned skill is indexable but NEVER
// auto-executed (Discovery != Permission).
type ExperienceRecord struct {
	// Skill is the skill ID (e.g. "pdf-generation").
	Skill string `json:"skill"`
	// TaskPattern is the task pattern (e.g. "document-to-pdf").
	TaskPattern string `json:"task_pattern"`
	// SuccessRate is the observed success rate (0-1).
	SuccessRate float64 `json:"success_rate"`
}

// ToolKind classifies a resolved tool provider.
type ToolKind string

const (
	// ToolMCP is an MCP server tool (lazy-connected at skill activation).
	ToolMCP ToolKind = "mcp"
	// ToolExecutable is a host executable declared by the skill manifest.
	ToolExecutable ToolKind = "executable"
	// ToolBuiltin is a framework builtin tool (trusted).
	ToolBuiltin ToolKind = "builtin"
)

// ResolvedTool is a skill-declared tool bound to a runnable provider.
type ResolvedTool struct {
	// ID is the tool id from the manifest (e.g. "semgrep").
	ID string
	// Kind is the provider class (mcp / executable / builtin).
	Kind ToolKind
	// Target is the MCP server name, executable command, or builtin registry name.
	Target string
	// Args are the static arguments from the manifest (executables only).
	Args []string
}

// ToolDecl is one tool declaration inside a skill manifest.
type ToolDecl struct {
	// ID is the tool identifier.
	ID string `yaml:"id"`
	// Type is the provider kind: "mcp" | "executable" | "builtin".
	Type string `yaml:"type"`
	// Command is the executable command (executable kind).
	Command string `yaml:"command,omitempty"`
	// Args are static executable arguments.
	Args []string `yaml:"args,omitempty"`
	// Server is the MCP server name (mcp kind).
	Server string `yaml:"server,omitempty"`
	// Name is the builtin registry name (builtin kind).
	Name string `yaml:"name,omitempty"`
}

// Manifest is the parsed skill.yaml tool declaration file. SKILL.md stays the
// instruction surface; the manifest declares only execution carriers.
type Manifest struct {
	ID          string     `yaml:"id"`
	Name        string     `yaml:"name"`
	Description string     `yaml:"description"`
	Keywords    []string   `yaml:"keywords"`
	Version     string     `yaml:"version"`
	Tools       []ToolDecl `yaml:"tools"`
}

// SourceDir is one declared skill source directory.
type SourceDir struct {
	// Kind is the source classification.
	Kind SourceKind
	// Path is the absolute directory containing skill subdirectories.
	Path string
}
