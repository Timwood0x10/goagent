package ares_skills

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// Capability Fabric tool names exposed to the agent. They close the design §10
// main loop (Discover Skill -> Load on Demand -> Execute): the LLM sees the
// resident Level-0 metadata block, calls skill_search to rank candidates,
// skill_load to fetch a SKILL.md body, and skill_activate to lazily connect the
// declared MCP servers / resolve executable and builtin carriers.
const (
	// ToolSkillSearch searches the Level-0 metadata index (Discover Skill).
	ToolSkillSearch = "skill_search"
	// ToolSkillLoad loads a skill's SKILL.md body (Load on Demand, Level 1).
	ToolSkillLoad = "skill_load"
	// ToolSkillActivate resolves a skill's tools and lazily connects the
	// declared MCP servers (Execute, Level 2). No MCP server is ever connected
	// before this is called (design principle 3 / acceptance #3).
	ToolSkillActivate = "skill_activate"
	// ToolSkillList returns every indexed skill's metadata (unfiltered).
	ToolSkillList = "skill_list"
	// ToolSkillExperience queries the learned-source relevance prior: the best
	// historically successful skill for a task pattern (design §11). Read-only.
	ToolSkillExperience = "skill_experience"

	// schemaTypeObject is the JSON schema type for the tool parameter objects.
	schemaTypeObject = "object"
	// schemaTypeString is the JSON schema type for string parameters.
	schemaTypeString = "string"
	// paramQuery is the skill_search query parameter name.
	paramQuery = "query"
	// paramLimit is the skill_search limit parameter name.
	paramLimit = "limit"
	// paramID is the skill_load / skill_activate id parameter name.
	paramID = "id"
	// paramTaskPattern is the skill_experience task_pattern parameter name.
	paramTaskPattern = "task_pattern"
)

// CatalogTools builds the agent-facing tools over a built catalog. Registering
// them into the internal tool registry (and re-bridging the tool binder) makes
// them visible in the LLM tool schemas so the agent can actually call the
// catalog at runtime.
//
// Args:
//   - catalog: the built SkillCatalog (may be nil; Execute then errors).
//
// Returns:
//   - []core.Tool: the four capability-fabric tools.
func CatalogTools(catalog *Catalog) []core.Tool {
	return []core.Tool{
		&catalogTool{
			catalog: catalog,
			name:    ToolSkillSearch,
			desc: "Search the available skill catalog by keywords and return " +
				"ranked metadata (id, name, description, source, version) — Level-0 " +
				"only, bodies are NOT included. Call this to discover which skill " +
				"fits the current task, then skill_load the best id.",
			params: &core.ParameterSchema{
				Type: schemaTypeObject,
				Properties: map[string]*core.Parameter{
					paramQuery: {Type: schemaTypeString, Description: "Free-text search query (matches id, name, description, keywords)."},
					paramLimit: {Type: "number", Description: "Maximum results to return (default 5)."},
				},
				Required: []string{paramQuery},
			},
			execute: func(ctx context.Context, params map[string]interface{}) (core.Result, error) {
				query, ok := paramString(params, paramQuery)
				if !ok || strings.TrimSpace(query) == "" {
					return core.NewErrorResult("query is required"), nil
				}
				limit, _ := paramInt(params, paramLimit)
				if limit <= 0 {
					limit = 5
				}
				entries := catalog.Search(query, limit)
				return resultJSON(toSkillViews(entries))
			},
			idem: true,
		},
		&catalogTool{
			catalog: catalog,
			name:    ToolSkillLoad,
			desc: "Load the full SKILL.md body of one skill by id — Level-1 " +
				"progressive disclosure. Returns the complete instructions and " +
				"when-to-use guidance, fetched on demand only.",
			params: &core.ParameterSchema{
				Type: schemaTypeObject,
				Properties: map[string]*core.Parameter{
					paramID: {Type: schemaTypeString, Description: "The skill id returned by skill_search."},
				},
				Required: []string{paramID},
			},
			execute: func(ctx context.Context, params map[string]interface{}) (core.Result, error) {
				id, ok := paramString(params, paramID)
				if !ok {
					return core.NewErrorResult("id is required"), nil
				}
				body, err := catalog.Load(id)
				if err != nil {
					return core.NewErrorResult(err.Error()), nil
				}
				return core.NewResult(true, body), nil
			},
			idem: true,
		},
		&catalogTool{
			catalog: catalog,
			name:    ToolSkillActivate,
			desc: "Activate a skill by id: resolves its declared tools and " +
				"lazily connects the MCP servers it declares (Level-2). Returns the " +
				"resolved tool list {id, kind, target}. MCP servers are connected " +
				"only at this point, never earlier.",
			params: &core.ParameterSchema{
				Type: schemaTypeObject,
				Properties: map[string]*core.Parameter{
					paramID: {Type: schemaTypeString, Description: "The skill id returned by skill_search."},
				},
				Required: []string{paramID},
			},
			execute: func(ctx context.Context, params map[string]interface{}) (core.Result, error) {
				id, ok := paramString(params, paramID)
				if !ok {
					return core.NewErrorResult("id is required"), nil
				}
				tools, err := catalog.Activate(ctx, id)
				if err != nil {
					return core.NewErrorResult(err.Error()), nil
				}
				// Level-2 resource disclosure: expose the skill's reference
				// files alongside the resolved tools. A references listing
				// failure never blocks activation — it is disclosure-only.
				refs, refsErr := catalog.ListReferences(id)
				if refsErr != nil {
					refs = nil
				}
				views := make([]resolvedToolView, 0, len(tools))
				for _, t := range tools {
					views = append(views, resolvedToolView{
						ID:         t.ID,
						Kind:       t.Kind,
						Target:     t.Target,
						Args:       t.Args,
						References: refs,
					})
				}
				return resultJSON(views)
			},
			idem: false,
		},
		&catalogTool{
			catalog: catalog,
			name:    ToolSkillList,
			desc: "List every indexed skill's metadata (id, name, description, " +
				"source, version) without ranking — useful for an overview of all " +
				"available capabilities.",
			params: &core.ParameterSchema{
				Type:       schemaTypeObject,
				Properties: map[string]*core.Parameter{},
			},
			execute: func(ctx context.Context, params map[string]interface{}) (core.Result, error) {
				return resultJSON(toSkillViews(catalog.All()))
			},
			idem: true,
		},
		&catalogTool{
			catalog: catalog,
			name:    ToolSkillExperience,
			desc: "Query the learned-source experience store for the best " +
				"historically successful skill of a task pattern (design §11). " +
				"Returns {skill, task_pattern, success_rate} or a no-experience " +
				"notice. Purely a relevance prior — it never executes anything.",
			params: &core.ParameterSchema{
				Type: schemaTypeObject,
				Properties: map[string]*core.Parameter{
					paramTaskPattern: {Type: schemaTypeString, Description: "The task pattern to look up, e.g. \"document-to-pdf\"."},
				},
				Required: []string{paramTaskPattern},
			},
			execute: func(ctx context.Context, params map[string]interface{}) (core.Result, error) {
				pattern, ok := paramString(params, paramTaskPattern)
				if !ok {
					return core.NewErrorResult("task_pattern is required"), nil
				}
				rec, found := catalog.Experience().BestMatch(pattern)
				if !found {
					return core.NewResult(true, "no recorded experience for task pattern "+strconv.Quote(pattern)), nil
				}
				return resultJSON(rec)
			},
			idem: true,
		},
	}
}

// catalogTool implements core.Tool over a shared scaffold so the four catalog
// tools differ only in name, description, params and behavior.
type catalogTool struct {
	catalog *Catalog
	name    string
	desc    string
	params  *core.ParameterSchema
	execute func(ctx context.Context, params map[string]interface{}) (core.Result, error)
	idem    bool
}

// Name returns the tool name.
func (t *catalogTool) Name() string { return t.name }

// Description returns the LLM-visible description.
func (t *catalogTool) Description() string { return t.desc }

// Category returns CategoryKnowledge (the capability fabric is a knowledge
// discovery surface, not a system-level primitive).
func (t *catalogTool) Category() core.ToolCategory { return core.CategoryKnowledge }

// Capabilities returns the knowledge capability label.
func (t *catalogTool) Capabilities() []core.Capability {
	return []core.Capability{core.CapabilityKnowledge}
}

// Parameters returns the declared parameter schema.
func (t *catalogTool) Parameters() *core.ParameterSchema { return t.params }

// Execute runs the tool against the bound catalog.
func (t *catalogTool) Execute(ctx context.Context, params map[string]interface{}) (core.Result, error) {
	if t.catalog == nil {
		return core.NewErrorResult("skill catalog unavailable"), nil
	}
	return t.execute(ctx, params)
}

// IsIdempotent reports whether the tool is safe to retry: search/load/list are
// read-only; activate connects MCP servers and must not be retried blindly.
func (t *catalogTool) IsIdempotent() bool { return t.idem }

// skillView is the compact Level-0 metadata returned to the LLM. The local
// filesystem Path and the change-detection Hash are deliberately omitted.
type skillView struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Keywords    []string   `json:"keywords,omitempty"`
	Source      SourceKind `json:"source"`
	Version     string     `json:"version,omitempty"`
}

// resolvedToolView is the compact resolved-tool view returned by skill_activate.
// References holds the skill-level reference resource files (Level-2
// progressive disclosure), shared by every resolved tool of the skill.
type resolvedToolView struct {
	ID         string   `json:"id"`
	Kind       ToolKind `json:"kind"`
	Target     string   `json:"target"`
	Args       []string `json:"args,omitempty"`
	References []string `json:"references,omitempty"`
}

// toSkillViews maps index entries onto the compact LLM-visible view.
//
// Args:
//   - entries: the catalog's index entries.
//
// Returns:
//   - []skillView: the compact views (same order).
func toSkillViews(entries []SkillIndexEntry) []skillView {
	views := make([]skillView, 0, len(entries))
	for _, e := range entries {
		views = append(views, skillView{
			ID:          e.ID,
			Name:        e.Name,
			Description: e.Description,
			Keywords:    e.Keywords,
			Source:      e.Source,
			Version:     e.Version,
		})
	}
	return views
}

// resultJSON wraps a value as a JSON string in a successful Result so the LLM
// sees valid JSON in the tool message (mirrors the discover_tools convention).
//
// Args:
//   - v: the value to marshal.
//
// Returns:
//   - core.Result: success with the JSON string, or an error result.
//   - error: nil (errors are encoded in the Result, not returned).
func resultJSON(v interface{}) (core.Result, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return core.NewErrorResult("marshal skill result: " + err.Error()), nil
	}
	return core.NewResult(true, string(b)), nil
}

// paramString extracts a non-empty string parameter.
//
// Args:
//   - params: the tool-call arguments.
//   - key: the parameter name.
//
// Returns:
//   - string: the string value.
//   - bool: false when absent or empty.
func paramString(params map[string]interface{}, key string) (string, bool) {
	v, ok := params[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok {
		s = fmt.Sprintf("%v", v)
	}
	return s, strings.TrimSpace(s) != ""
}

// paramInt extracts an integer parameter tolerating float64 (JSON numbers).
//
// Args:
//   - params: the tool-call arguments.
//   - key: the parameter name.
//
// Returns:
//   - int: the parsed value.
//   - bool: false when absent or unparseable.
func paramInt(params map[string]interface{}, key string) (int, bool) {
	v, ok := params[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	default:
		i, err := strconv.Atoi(fmt.Sprintf("%v", v))
		if err != nil {
			return 0, false
		}
		return i, true
	}
}
