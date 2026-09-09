package builtin

import (
	stderrors "errors"
	"fmt"
	"os"
	"time"

	"github.com/Timwood0x10/ares/internal/errors"
	"github.com/Timwood0x10/ares/internal/llm"
	memory "github.com/Timwood0x10/ares/internal/runtime/memory"
	"github.com/Timwood0x10/ares/internal/storage/postgres/repositories"
	"github.com/Timwood0x10/ares/internal/tools/resources/base"
	builtin_embedding "github.com/Timwood0x10/ares/internal/tools/resources/builtin/embedding"
	builtin_execution "github.com/Timwood0x10/ares/internal/tools/resources/builtin/execution"
	builtin_file "github.com/Timwood0x10/ares/internal/tools/resources/builtin/file"
	builtin_hash "github.com/Timwood0x10/ares/internal/tools/resources/builtin/hash"
	builtin_knowledge "github.com/Timwood0x10/ares/internal/tools/resources/builtin/knowledge"
	builtin_math "github.com/Timwood0x10/ares/internal/tools/resources/builtin/math"
	builtin_memory "github.com/Timwood0x10/ares/internal/tools/resources/builtin/memory"
	builtin_network "github.com/Timwood0x10/ares/internal/tools/resources/builtin/network"
	builtin_pdf "github.com/Timwood0x10/ares/internal/tools/resources/builtin/pdf"
	builtin_planning "github.com/Timwood0x10/ares/internal/tools/resources/builtin/planning"
	builtin_stringutils "github.com/Timwood0x10/ares/internal/tools/resources/builtin/stringutils"
	builtin_system "github.com/Timwood0x10/ares/internal/tools/resources/builtin/system"
	builtin_text "github.com/Timwood0x10/ares/internal/tools/resources/builtin/text"
	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// fileToolsAllowedDirEnv is the environment variable used to configure the
// FileTools allowed directory at registration time. Operators MUST set this to
// a directory the agent is permitted to read and write.
const fileToolsAllowedDirEnv = "ARES_FILE_TOOLS_ALLOWED_DIR"

// resolveFileToolsAllowedDir returns the directory that FileTools may operate
// within. It reads from the ARES_FILE_TOOLS_ALLOWED_DIR environment variable,
// falling back to the current working directory if unset.
func resolveFileToolsAllowedDir() string {
	if dir := os.Getenv(fileToolsAllowedDirEnv); dir != "" {
		return dir
	}
	dir, err := os.Getwd()
	if err != nil {
		return "/tmp"
	}
	return dir
}

// GeneralToolsDeps carries the optional runtime dependencies for the
// knowledge / memory / planning tools registered by RegisterGeneralTools.
// A nil field keeps the tool registered with a nil guard: Execute returns a
// clear error instead of panicking (see knowledge_base.go, distilled_memory_tools.go).
// Production callers wire real dependencies from the bootstrap Components so
// the tools are actually usable, not just crash-safe.
type GeneralToolsDeps struct {
	// KnowledgeSearcher backs knowledge_search.
	KnowledgeSearcher builtin_knowledge.KnowledgeSearcher
	// KnowledgeService backs knowledge_add / knowledge_update / knowledge_delete.
	KnowledgeService builtin_knowledge.KnowledgeService
	// KnowledgeRepo backs correct_knowledge.
	KnowledgeRepo repositories.KnowledgeRepositoryInterface
	// MemoryMgr backs memory_search and user_profile.
	MemoryMgr memory.MemoryManager
	// LLMClient backs task_planner.
	LLMClient *llm.Client
}

// RegisterGeneralTools registers all general-purpose tools into the provided
// registry. The caller owns the registry instance (typically the internal
// core.Registry bridged to agent ToolBinders), which keeps tool wiring
// explicit and avoids hidden global state.
//
// Optional deps provide real backends for the knowledge/memory/planning
// tools; when omitted the tools register with nil guards (Execute returns an
// error rather than panicking). Production callers should wire the deps from
// their bootstrap Components.
//
// SECURITY: FileTools is registered with WithAllowedDir so that path traversal
// is blocked by default. CodeRunner is registered with Python DISABLED by
// default — operators must opt in via EnablePython(true). HTTPRequest and
// WebScraper enforce SSRF filtering at the HTTP client layer.
func RegisterGeneralTools(reg *core.Registry, deps ...GeneralToolsDeps) error {
	if reg == nil {
		return errors.New("register general tools: registry cannot be nil")
	}
	var d GeneralToolsDeps
	if len(deps) > 0 {
		d = deps[0]
	}
	tools := []core.Tool{
		// Math capability
		base.WithToolTags(builtin_math.NewCalculator(), map[string]string{
			"domain": "math", "input_type": "text", "output_type": "number",
			"side_effects": "false",
		}),
		base.WithToolTags(builtin_math.NewDateTime(), map[string]string{
			"domain": "math", "input_type": "text", "output_type": "text",
			"side_effects": "false",
		}),
		base.WithToolTags(builtin_math.NewTextProcessor(), map[string]string{
			"domain": "text", "input_type": "text", "output_type": "text",
			"side_effects": "false",
		}),

		// Network capability
		base.WithToolTags(builtin_network.NewHTTPRequest(), map[string]string{
			"domain": "network", "input_type": "json", "output_type": "text",
			"side_effects": "true", "requires_network": "true",
		}),
		base.WithToolTags(
			builtin_network.NewWebScraper(builtin_network.NewWebFetcher(builtin_network.NewDefaultHTTPClient(30*time.Second))),
			map[string]string{"domain": "network", "input_type": "url", "output_type": "text",
				"side_effects": "false", "requires_network": "true"},
		),
		base.WithToolTags(builtin_network.NewWebSearch(), map[string]string{
			"domain": "network", "input_type": "text", "output_type": "text",
			"side_effects": "false", "requires_network": "true",
		}),

		// File capability — restricted to the configured allowed directory.
		base.WithToolTags(builtin_file.NewFileTools(builtin_file.WithAllowedDir(resolveFileToolsAllowedDir())), map[string]string{
			"domain": "file", "input_type": "text", "output_type": "text",
			"side_effects": "true", "mutates_state": "true",
		}),

		// Text capability
		base.WithToolTags(builtin_text.NewJSONTools(), map[string]string{
			"domain": "data", "input_type": "json", "output_type": "json",
			"side_effects": "false",
		}),
		base.WithToolTags(builtin_text.NewDataValidation(), map[string]string{
			"domain": "data", "input_type": "text", "output_type": "boolean",
			"side_effects": "false",
		}),
		base.WithToolTags(builtin_text.NewDataTransform(), map[string]string{
			"domain": "data", "input_type": "text", "output_type": "text",
			"side_effects": "false",
		}),
		base.WithToolTags(builtin_text.NewRegexTool(), map[string]string{
			"domain": "text", "input_type": "text", "output_type": "text",
			"side_effects": "false",
		}),
		base.WithToolTags(builtin_text.NewLogAnalyzer(), map[string]string{
			"domain": "text", "input_type": "text", "output_type": "text",
			"side_effects": "false",
		}),

		// System capability
		base.WithToolTags(builtin_system.NewIDGenerator(), map[string]string{
			"domain": "system", "input_type": "text", "output_type": "text",
			"side_effects": "false",
		}),

		// Execution capability
		base.WithToolTags(builtin_execution.NewCodeRunner(), map[string]string{
			"domain": "execution", "input_type": "text", "output_type": "text",
			"side_effects": "true",
		}),

		// Embedding capability
		base.WithToolTags(builtin_embedding.NewEmbeddingTool(""), map[string]string{
			"domain": "embedding", "input_type": "text", "output_type": "json",
			"side_effects": "false", "requires_network": "true",
		}),

		// Hash capability
		base.WithToolTags(builtin_hash.NewHashTool(), map[string]string{
			"domain": "crypto", "input_type": "text", "output_type": "text",
			"side_effects": "false",
		}),

		// String utils capability
		base.WithToolTags(builtin_stringutils.NewStringUtils(), map[string]string{
			"domain": "text", "input_type": "text", "output_type": "text",
			"side_effects": "false",
		}),

		// PDF capability — sandboxed to the same allowed directory as
		// FileTools so it cannot read arbitrary files (REVIEW #30).
		base.WithToolTags(builtin_pdf.NewPDFTool(builtin_pdf.WithAllowedDir(resolveFileToolsAllowedDir())), map[string]string{
			"domain": "pdf", "input_type": "file", "output_type": "text",
			"side_effects": "false",
		}),
	}

	// Dependency-backed tools (Stage 5): registered only when their backend
	// dependency is actually wired, so no tool is registered that is known to
	// fail at call time (no "registered but always fails" tools). Knowledge
	// tools need the AKG store adapter; memory tools need the live MemoryManager
	// and/or the distilled-memory repo; the planner needs an LLM client.
	if d.KnowledgeSearcher != nil {
		tools = append(tools, base.WithToolTags(builtin_knowledge.NewKnowledgeSearch(d.KnowledgeSearcher), map[string]string{
			"domain": "knowledge", "input_type": "text", "output_type": "json",
			"side_effects": "false",
		}))
	}
	if d.KnowledgeService != nil {
		tools = append(tools,
			base.WithToolTags(builtin_knowledge.NewKnowledgeAdd(d.KnowledgeService), map[string]string{
				"domain": "knowledge", "input_type": "json", "output_type": "boolean",
				"side_effects": "true", "mutates_state": "true",
			}),
			base.WithToolTags(builtin_knowledge.NewKnowledgeUpdate(d.KnowledgeService), map[string]string{
				"domain": "knowledge", "input_type": "json", "output_type": "boolean",
				"side_effects": "true", "mutates_state": "true",
			}),
			base.WithToolTags(builtin_knowledge.NewKnowledgeDelete(d.KnowledgeService), map[string]string{
				"domain": "knowledge", "input_type": "text", "output_type": "boolean",
				"side_effects": "true", "mutates_state": "true",
			}),
		)
	}
	if d.KnowledgeRepo != nil {
		tools = append(tools, base.WithToolTags(builtin_knowledge.NewCorrectKnowledge(d.KnowledgeRepo), map[string]string{
			"domain": "knowledge", "input_type": "json", "output_type": "boolean",
			"side_effects": "true", "mutates_state": "true",
		}))
	}
	if d.MemoryMgr != nil {
		tools = append(tools, base.WithToolTags(builtin_memory.NewMemorySearch(d.MemoryMgr), map[string]string{
			"domain": "memory", "input_type": "text", "output_type": "json",
			"side_effects": "false",
		}))
	}
	if d.MemoryMgr != nil {
		tools = append(tools, base.WithToolTags(builtin_memory.NewUserProfile(d.MemoryMgr), map[string]string{
			"domain": "memory", "input_type": "text", "output_type": "json",
			"side_effects": "false",
		}))
	}
	if d.LLMClient != nil {
		tools = append(tools, base.WithToolTags(builtin_planning.NewTaskPlanner(d.LLMClient), map[string]string{
			"domain": "planning", "input_type": "text", "output_type": "json",
			"side_effects": "false",
		}))
	}

	var (
		errs       []error
		registered int
	)
	for _, tool := range tools {
		if err := reg.Register(tool); err != nil {
			// On conflict (e.g. duplicate name from a prior registration), log a
			// warning and continue with the remaining tools instead of aborting
			// the whole registration. The pre-existing tool wins.
			log.Warn("builtin: failed to register tool", "tool", tool.Name(), "error", err)
			errs = append(errs, fmt.Errorf("%s: %w", tool.Name(), err))
			continue
		}
		registered++
	}

	// Succeed when at least one tool registered. Only fail when every
	// registration failed, which signals a catastrophic problem (e.g. the
	// registry is unusable) rather than a benign duplicate-name conflict.
	if registered == 0 && len(errs) > 0 {
		return errors.Wrap(stderrors.Join(errs...), "failed to register any tool")
	}

	return nil
}
