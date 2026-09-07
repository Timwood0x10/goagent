package runtime

//nolint: errcheck // best-effort operations: ResponseWriter writes, cleanup Close/Wait, deferred shutdown
import (
	"context"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/pipeline"
	"github.com/Timwood0x10/ares/internal/knowledge/planner"
	"github.com/Timwood0x10/ares/internal/knowledge/provider"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

// KnowledgeRuntime is the central execution engine of AKF.
// It orchestrates Plan → Load → Link → Reduce → Graph.
type KnowledgeRuntime struct {
	planner   planner.KnowledgePlanner
	discovery planner.SourceDiscovery
	registry  *provider.ProviderRegistry
	pipeline  *knowledge.KnowledgePipeline
	linkers   []Linker
	reducers  []Reducer

	patchRegMu sync.RWMutex
	patchReg   *patch.Registry

	// planMu guards the planner field. SetPlanConfig (writer) swaps the
	// planner at runtime via KnowledgePatchExecutor.Apply; PlanConfig and the
	// Execute hot path (readers) take the read lock so a config swap landing
	// mid-query cannot race the planner read.
	planMu sync.RWMutex

	evStore evidence.Store      // optional: unified Evidence Store
	evColl  *evidence.Collector // optional: evidence emitter (Source "akf")
	fitColl *evidence.Collector // optional: knowledge fitness emitter (Source "knowledge")
}

// New creates a KnowledgeRuntime with the given components.
// If pipe is nil, a default KnowledgePipeline with DefaultNormalizer,
// DefaultEntityMatcher, DefaultValidator, and DefaultSummarizer is created.
func New(
	p planner.KnowledgePlanner,
	d planner.SourceDiscovery,
	reg *provider.ProviderRegistry,
	pipe *knowledge.KnowledgePipeline,
	linkers []Linker,
	reducers []Reducer,
) *KnowledgeRuntime {
	if pipe == nil {
		pipe = knowledge.NewKnowledgePipeline(
			[]knowledge.Normalizer{&pipeline.DefaultNormalizer{MaxRawBytes: 10240}},
			[]knowledge.EntityMatcher{&pipeline.DefaultEntityMatcher{MatchThreshold: 0.6}},
			[]knowledge.Validator{&pipeline.DefaultValidator{}},
			[]knowledge.Summarizer{&pipeline.DefaultSummarizer{MaxSummaryLen: 200}},
		)
	}
	return &KnowledgeRuntime{
		planner:   p,
		discovery: d,
		registry:  reg,
		pipeline:  pipe,
		linkers:   linkers,
		reducers:  reducers,
	}
}

// WithPatchRegistry sets the runtime's patch registry for dynamic knowledge config changes.
func (r *KnowledgeRuntime) WithPatchRegistry(pr *patch.Registry) *KnowledgeRuntime {
	r.patchRegMu.Lock()
	defer r.patchRegMu.Unlock()
	r.patchReg = pr
	return r
}

// WithEvidenceStore sets the runtime's evidence store for emitting AKF insights
// and knowledge fitness evidence.
func (r *KnowledgeRuntime) WithEvidenceStore(store evidence.Store) *KnowledgeRuntime {
	r.evStore = store
	if store != nil {
		r.evColl = evidence.NewCollector(store, "akf")
		// Knowledge fitness evidence is emitted under Source "knowledge" so the
		// GA KnowledgeGenome (which filters on that source) consumes it.
		r.fitColl = evidence.NewCollector(store, "knowledge")
	}
	return r
}

// RegisterProvider adds a graph provider to the runtime's registry after
// construction. Providers whose backing components are created later in the
// bootstrap sequence (e.g. the evolution StrategyStore, which only exists
// once wireGAEvolution has run) attach here instead of forcing a reorder of
// bootstrap steps. Safe to call before Execute; not safe concurrently with
// Execute (same contract as ProviderRegistry.Register).
func (r *KnowledgeRuntime) RegisterProvider(p provider.GraphProvider) error {
	return r.registry.Register(p)
}

// ProviderNames lists the names of all registered graph providers. Read-only
// view for wiring assertions and observability dashboards.
func (r *KnowledgeRuntime) ProviderNames() []string {
	return r.registry.List()
}

// Config holds optional runtime configuration.
type Config struct {
	MaxConcurrentProviders int  // Max parallel provider loads (default 5)
	LazyLoading            bool // Clamp the graph budget when set; full lazy loading was removed with LazyGraph (tech-debt: see plan/0.3.1plan)
}

// maxLazyForGraph caps the graph budget in lazy mode before Reduce.
// DefaultReducer estimates ~50 tokens per node, so this limits the returned
// WorkingGraph to at most 40 nodes, approximating a lazy graph until a
// full *LazyGraph return type is implemented (see the clamp in Execute).
const maxLazyForGraph = 2000

// Execute runs the full AKF pipeline: Plan → Load → Link → Reduce → Graph.
func (r *KnowledgeRuntime) Execute(ctx context.Context, goal string, budget knowledge.TokenBudget, cfg *Config) (*knowledge.WorkingGraph, error) {
	if r == nil {
		return nil, errors.New("runtime: planner is not configured")
	}
	// Snapshot the planner under planMu: SetPlanConfig swaps the interface
	// field at runtime (KnowledgePatchExecutor.Apply), and a bare read here
	// raced with that write whenever an evolution patch landed mid-query.
	r.planMu.RLock()
	planner := r.planner
	r.planMu.RUnlock()
	if planner == nil {
		return nil, errors.New("runtime: planner is not configured")
	}
	if cfg == nil {
		cfg = &Config{MaxConcurrentProviders: 5}
	}
	if cfg.MaxConcurrentProviders <= 0 {
		cfg.MaxConcurrentProviders = 5
	}

	// 1. Plan: generate knowledge requirements.
	plan, err := planner.Plan(ctx, goal, budget)
	if err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}
	if len(plan.Requirements) == 0 {
		return nil, fmt.Errorf("plan: no requirements generated for goal %q", goal)
	}

	// 2. Discover: map requirements to providers.
	sources, err := r.discovery.Discover(ctx, plan.Requirements, budget)
	if err != nil {
		return nil, fmt.Errorf("discover: %w", err)
	}
	if len(sources) == 0 {
		return nil, errors.New("discover: no providers matched requirements")
	}

	// 3. Load & Pipeline: stream from providers, normalize, resolve, summarize.
	objects, err := r.loadAndProcess(ctx, sources, cfg)
	if err != nil {
		return nil, fmt.Errorf("load: %w", err)
	}

	// 4. Link: generate relations between objects.
	edges, err := r.link(ctx, objects)
	if err != nil {
		return nil, fmt.Errorf("link: %w", err)
	}

	// Lazy loading is approximated by clamping budget.ForGraph to
	// maxLazyForGraph before Reduce. The reducer then prunes the graph to a
	// smaller size, so the returned WorkingGraph is genuinely smaller.
	//
	// Known limitation: this is not a full LazyGraph. Execute still loads
	// every object from the providers and returns *knowledge.WorkingGraph;
	// only the final graph size is reduced. A complete implementation would
	// return *LazyGraph built from summaries with an expandFn that fetches
	// full objects on demand.
	//
	// Future direction: change Execute's return type to *LazyGraph (or add a
	// parallel ExecuteLazy) and defer object loading until Expand is called.
	// Until then the clamp below is the lazy-loading mechanism.
	if cfg.LazyLoading && budget.ForGraph > maxLazyForGraph {
		log.Info("lazy loading: clamping graph budget",
			"original", budget.ForGraph,
			"clamped", maxLazyForGraph)
		budget.ForGraph = maxLazyForGraph
	}

	// 5. Reduce: prune and compress to fit budget (uses clamped budget when lazy).
	graph := &knowledge.WorkingGraph{Nodes: objects, Edges: edges}
	graph, err = r.reduce(ctx, graph, budget)
	if err != nil {
		return nil, fmt.Errorf("reduce: %w", err)
	}

	// Emit insight evidence to the unified Evidence Store.
	if r.evColl != nil {
		_ = r.evColl.EmitWithMeta(ctx, evidence.KindInsight,
			map[string]any{
				"goal":        goal,
				"node_count":  len(graph.Nodes),
				"edge_count":  len(graph.Edges),
				"budget_used": budget.ForGraph,
			},
			"goal", goal,
		)
	}

	// Emit knowledge fitness evidence under Source "knowledge": a successful
	// AKG graph produced within budget is a win (1.0). The GA KnowledgeGenome
	// aggregates the mean value to score the planner/reducer strategies it
	// evolves.
	if r.fitColl != nil {
		_ = r.fitColl.Emit(ctx, evidence.KindFitness,
			map[string]any{"value": 1.0},
		)
	}

	return graph, nil
}

// loadAndProcess streams objects from all selected providers concurrently,
// runs the KnowledgePipeline on each object, and collects results.
// Uses errgroup for goroutine lifecycle management (§4.5: no bare goroutines).
func (r *KnowledgeRuntime) loadAndProcess(ctx context.Context, sources []planner.PlannedSource, cfg *Config) (map[string]*knowledge.KnowledgeObject, error) {
	objects := make(map[string]*knowledge.KnowledgeObject)
	var mu sync.Mutex

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(cfg.MaxConcurrentProviders)

	for _, src := range sources {
		if ctx.Err() != nil {
			break
		}

		prov := r.registry.Get(src.ProviderName)
		if prov == nil {
			log.Warn("provider not found (skipping)", "provider", src.ProviderName)
			continue
		}

		src, prov := src, prov // capture loop vars
		g.Go(func() error {
			intent := knowledge.Intent{
				Goal: src.Requirement.Description,
				Scope: knowledge.Scope{
					MaxObjects: src.MaxResults,
				},
			}
			if src.Query != nil && src.Query.Query != "" {
				intent.Goal = src.Query.Query
			}

			objCh, streamErrCh := prov.Stream(ctx, intent)
		loop:
			for {
				select {
				case obj, ok := <-objCh:
					if !ok {
						break loop
					}
					// Run through pipeline.
					if r.pipeline != nil {
						var pErr error
						obj, pErr = r.pipeline.Process(ctx, obj)
						if pErr != nil {
							continue
						}
					}
					mu.Lock()
					objects[obj.ID] = obj
					mu.Unlock()
				case <-ctx.Done():
					// Context cancelled; drain remaining objects so the
					// producer goroutine can exit instead of blocking on
					// the send forever (goroutine leak fix).
					for range objCh {
					}
					break loop
				}
			}

			// Check stream error.
			select {
			case sErr := <-streamErrCh:
				if sErr != nil {
					log.Warn("provider stream error (partial data may remain)", "provider", src.ProviderName, "error", sErr)
				}
			default:
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("load: %w", err)
	}

	if len(objects) == 0 {
		return nil, errors.New("load: no objects loaded from any provider")
	}

	return objects, nil
}

// link runs all linkers to generate relations between objects.
func (r *KnowledgeRuntime) link(ctx context.Context, objects map[string]*knowledge.KnowledgeObject) ([]knowledge.Relation, error) {
	if len(r.linkers) == 0 {
		return nil, nil
	}

	objList := make([]*knowledge.KnowledgeObject, 0, len(objects))
	for _, obj := range objects {
		objList = append(objList, obj)
	}

	var allEdges []knowledge.Relation
	for _, l := range r.linkers {
		edges, err := l.Link(ctx, objList)
		if err != nil {
			log.Warn("linker failed (skipping)", "linker", l.Name(), "error", err)
			continue
		}
		allEdges = append(allEdges, edges...)
	}

	// Defensive dedup on the (From, To, Name) triple (#43): individual
	// linkers cannot produce duplicates today, but nothing enforces that
	// invariant across current or future linkers, and downstream aggregation
	// has no dedup either. First occurrence wins; Properties/Score of the
	// duplicate are dropped.
	seen := make(map[knowledge.RelationKey]struct{}, len(allEdges))
	deduped := make([]knowledge.Relation, 0, len(allEdges))
	for _, e := range allEdges {
		k := knowledge.RelationKey{From: e.From, To: e.To, Name: e.Name}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		deduped = append(deduped, e)
	}
	return deduped, nil
}

// reduce runs reducers in sequence to prune and compress the graph.
func (r *KnowledgeRuntime) reduce(ctx context.Context, graph *knowledge.WorkingGraph, budget knowledge.TokenBudget) (*knowledge.WorkingGraph, error) {
	current := graph
	for _, red := range r.reducers {
		var err error
		current, err = red.Reduce(ctx, current, budget)
		if err != nil {
			log.Warn("reducer failed (skipping)", "reducer", red.Name(), "error", err)
			continue
		}
	}
	return current, nil
}
