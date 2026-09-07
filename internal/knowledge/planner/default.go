package planner

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/provider"
)

// providerSelectThreshold is the minimum IntentMatch score a provider must
// reach to be selected for a requirement. It sits above MemoryProvider's
// weak scores (0.3 for code/architecture, where memory is least useful) so
// that memory is not injected into code queries, while still including its
// strong scores (0.6–0.8 for memory/decision/issue).
const providerSelectThreshold = 0.35

// NewKnowledgePlanner creates a default planner that generates requirements
// based on keyword matching against the task goal.
func NewKnowledgePlanner() KnowledgePlanner {
	return &defaultPlanner{}
}

type defaultPlanner struct{}

func (p *defaultPlanner) Plan(_ context.Context, goal string, budget knowledge.TokenBudget) (*KnowledgePlan, error) {
	if goal == "" {
		return nil, errors.New("goal cannot be empty")
	}

	reqs := generateRequirements(goal, budget)
	plan := &KnowledgePlan{
		Requirements: reqs,
		TokenBudget:  budget,
	}
	return plan, nil
}

// generateRequirements creates KnowledgeRequirements based on task goal keywords.
// MaxResults for each requirement are computed dynamically from the token budget
// so that the total loaded KnowledgeObjects fit within ForGraph.
func generateRequirements(goal string, budget knowledge.TokenBudget) []KnowledgeRequirement {
	goalLower := strings.ToLower(goal)

	// Compute per-requirement limits proportional to budget.
	maxTotal := budget.ForGraph / 50 // est 50 tokens/node
	if maxTotal <= 0 {
		maxTotal = 10
	}
	if maxTotal > 200 {
		maxTotal = 200 // safety cap
	}

	reqs := make([]KnowledgeRequirement, 0, 5)

	// Always include decision relevance.
	needs := []struct {
		need     NeedType
		weight   int
		keywords []string
	}{
		{NeedDecision, 3, nil}, // always included
		{NeedArchitecture, 2, []string{"architect", "design", "stack", "infrastructure", "deploy"}},
		{NeedCode, 2, []string{"code", "implement", "function", "api", "class", "method"}},
		{NeedIssue, 2, []string{"bug", "fix", "issue", "problem", "error"}},
		{NeedPerformance, 1, []string{"performance", "slow", "latency", "benchmark", "optimize"}},
	}

	var totalWeight int
	var matched []struct {
		need   NeedType
		weight int
	}
	for _, n := range needs {
		if n.keywords == nil || containsAny(goalLower, n.keywords) {
			matched = append(matched, struct {
				need   NeedType
				weight int
			}{n.need, n.weight})
			totalWeight += n.weight
		}
	}

	// Always include history as fallback.
	matched = append(matched, struct {
		need   NeedType
		weight int
	}{NeedHistory, 1})
	totalWeight++

	for _, m := range matched {
		// Distribute maxTotal proportionally to weight.
		limit := maxTotal * m.weight / totalWeight
		if limit < 3 {
			limit = 3
		}
		reqs = append(reqs, KnowledgeRequirement{
			Need:        m.need,
			Description: fmt.Sprintf("%s related to: %s", m.need, goal),
			Priority:    1,
			MaxResults:  limit,
		})
	}

	return reqs
}

// containsAny reports whether s contains any of the substrings.
func containsAny(s string, substrs []string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// needToObjectTypes maps a knowledge Need to the ObjectTypes it is most
// relevant to, so that type-aware providers (e.g. MemoryProvider) can score
// the requirement correctly. Without this mapping the intent carries no
// types and every provider collapses to its generic "no type" score, which
// previously caused MemoryProvider to be selected for all queries.
func needToObjectTypes(need NeedType) []knowledge.ObjectType {
	switch need {
	case NeedDecision:
		return []knowledge.ObjectType{knowledge.ObjectDecision}
	case NeedArchitecture:
		return []knowledge.ObjectType{knowledge.ObjectArchitecture}
	case NeedCode:
		return []knowledge.ObjectType{knowledge.ObjectCode}
	case NeedIssue:
		return []knowledge.ObjectType{knowledge.ObjectIssue}
	case NeedPerformance:
		return []knowledge.ObjectType{knowledge.ObjectCommit}
	case NeedHistory:
		return []knowledge.ObjectType{knowledge.ObjectMemory}
	default:
		return nil
	}
}

// defaultSourceDiscovery maps KnowledgeRequirements to providers by
// scoring each provider's IntentMatch against generated intents.
type defaultSourceDiscovery struct {
	registry *provider.ProviderRegistry
	planner  QueryPlanner
}

// NewSourceDiscovery creates a SourceDiscovery with the given registry and query planner.
func NewSourceDiscovery(registry *provider.ProviderRegistry, planner QueryPlanner) SourceDiscovery {
	return &defaultSourceDiscovery{
		registry: registry,
		planner:  planner,
	}
}

func (d *defaultSourceDiscovery) Discover(ctx context.Context, reqs []KnowledgeRequirement, budget knowledge.TokenBudget) ([]PlannedSource, error) {
	if len(reqs) == 0 {
		return nil, nil
	}

	var sources []PlannedSource

	for _, req := range reqs {
		// Build an intent for each requirement.
		intent := knowledge.Intent{
			Goal: req.Description,
			Scope: knowledge.Scope{
				Types:      needToObjectTypes(req.Need),
				MaxObjects: req.MaxResults,
			},
			Budget: budget,
		}

		// Find matching providers.
		providers := d.registry.Select(intent, providerSelectThreshold)
		if len(providers) == 0 {
			continue
		}

		// For each matching provider, generate a query plan.
		for _, prov := range providers {
			providerType := detectProviderType(prov)
			qp, err := d.planner.PlanQuery(ctx, req, prov.Name(), providerType)
			if err != nil {
				continue
			}

			sources = append(sources, PlannedSource{
				ProviderName: prov.Name(),
				Requirement:  req,
				Query:        qp,
				Priority:     req.Priority,
				MaxResults:   req.MaxResults,
			})
		}
	}

	// Sort by priority.
	sort.Slice(sources, func(i, j int) bool {
		return sources[i].Priority < sources[j].Priority
	})

	return sources, nil
}

// detectProviderType returns the backing data source type of a GraphProvider
// via the optional TypedProvider interface. Providers that explicitly expose
// their type (memory, evolution, postgres, mysql, vector, store, code) return
// their registered type; providers that do not implement TypedProvider return
// "" (unknown), which SourceDiscovery treats as a generic/keyword query.
func detectProviderType(p provider.GraphProvider) string {
	if tp, ok := p.(provider.TypedProvider); ok {
		return string(tp.ProviderType())
	}
	return ""
}

// defaultQueryPlanner is a simple QueryPlanner that generates keyword-based
// query descriptions for each requirement-provider pair.
type defaultQueryPlanner struct{}

// NewQueryPlanner creates a default query planner.
func NewQueryPlanner() QueryPlanner {
	return &defaultQueryPlanner{}
}

func (q *defaultQueryPlanner) PlanQuery(_ context.Context, req KnowledgeRequirement, providerName, providerType string) (*QueryPlan, error) {
	if req.Description == "" {
		return nil, errors.New("requirement description cannot be empty")
	}
	return &QueryPlan{
		Query:      req.Description,
		QueryType:  QueryKeyword,
		MaxResults: req.MaxResults,
		Parameters: map[string]any{
			"need":          string(req.Need),
			"provider_name": providerName,
			"provider_type": providerType,
		},
	}, nil
}
