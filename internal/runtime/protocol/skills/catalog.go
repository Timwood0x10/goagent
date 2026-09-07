package ares_skills

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/knowledge/skills"
)

// MCPConnector lazily connects an MCP server by name at skill activation
// (design principle 3: no MCP server is started until a skill that declares
// it is activated; ares_mcp.MCPManager satisfies this interface).
type MCPConnector interface {
	// ConnectServer connects the named server and registers its tools.
	ConnectServer(ctx context.Context, name string) error
}

// CatalogConfig configures the top-level SkillCatalog facade.
type CatalogConfig struct {
	// ProjectSkillsDir is the project-local skills root (".ares/skills").
	ProjectSkillsDir string
	// UserSkillsDir is the user-global skills root ("~/.ares/skills").
	UserSkillsDir string
	// RegisteredDirs are extra directory sources from config.toml.
	RegisteredDirs []string
	// AllowLocalExecutables permits executables declared by trusted sources.
	AllowLocalExecutables bool
	// Builtins are the known framework builtin tool names.
	Builtins []string
	// ExperiencePath, when non-empty, persists learned relevance priors as a
	// JSON file at this path (design §11).
	ExperiencePath string
}

// Catalog is the SkillCatalog facade: it composes SourceManager, Indexer,
// Discovery, Loader, ToolResolver and Experience behind one entry point. It
// can also seed the existing knowledge/skills.Registry so the memory
// manager's resident skill block stays in sync with the index.
type Catalog struct {
	mu        sync.RWMutex
	sm        *SourceManager
	indexer   *Indexer
	discovery *Discovery
	loader    *Loader
	resolver  *Resolver
	exp       *Experience
	mcp       MCPConnector
	httpSrcs  []HTTPSource
	// registry, when set via SeedRegistry, is re-seeded after every
	// Build/Refresh so the memory manager's resident block stays in sync.
	registry *skills.Registry
}

// NewCatalog creates a SkillCatalog over the declared sources.
//
// Args:
//   - cfg: the catalog configuration.
//
// Returns:
//   - *Catalog: ready to Build (index) and serve.
func NewCatalog(cfg CatalogConfig) *Catalog {
	c := &Catalog{
		sm:       NewSourceManager(cfg.ProjectSkillsDir, cfg.UserSkillsDir, cfg.RegisteredDirs),
		indexer:  NewIndexer(),
		resolver: NewResolver(cfg.AllowLocalExecutables, cfg.Builtins),
		exp:      NewExperience(),
	}
	if cfg.ExperiencePath != "" {
		c.exp = NewExperienceWithStore(context.Background(), NewJSONExperienceStore(cfg.ExperiencePath))
	}
	return c
}

// SetGitSources attaches declared git sources (config.toml type="git").
// Call SyncGitSources before Build to refresh local clones.
//
// Args:
//   - gits: the git sources to index via their local cache directories.
func (c *Catalog) SetGitSources(gits []GitSource) {
	c.sm.SetGitSources(gits)
}

// SyncGitSources clones or refreshes every declared git source into its local
// cache directory. A failure is returned (not fatal): the catalog can still
// Build from whatever local checkouts exist.
//
// Args:
//   - ctx: context for cancellation.
//
// Returns:
//   - error: joined sync errors, or nil.
func (c *Catalog) SyncGitSources(ctx context.Context) error {
	return c.sm.SyncGitSources(ctx)
}

// SetHTTPSources attaches declared http/oci manifest sources. FetchHTTPManifest
// is called during Build; a fetch failure skips that source without failing
// the whole index.
//
// Args:
//   - srcs: the http/oci manifest sources.
func (c *Catalog) SetHTTPSources(srcs []HTTPSource) {
	c.httpSrcs = append([]HTTPSource(nil), srcs...)
}

// Build indexes all declared sources (metadata only — zero disk scanning
// beyond the declared roots; git clones synced first; http/oci manifests
// fetched). Safe to call again after sources change. It is safe for concurrent
// use with Search/Load/Refresh (an internal RWMutex guards index swaps).
//
// Returns:
//   - error: wrapped index error, or nil.
func (c *Catalog) Build() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buildLocked()
}

// buildLocked performs the actual indexing; caller must hold the write lock.
//
// Returns:
//   - error: wrapped index error, or nil.
func (c *Catalog) buildLocked() error {
	sources := c.sm.Sources()
	entries, err := c.indexer.Index(sources, c.sm)
	if err != nil {
		return err
	}
	for _, src := range c.httpSrcs {
		remote, fetchErr := FetchHTTPManifest(context.Background(), src)
		if fetchErr != nil {
			continue // remote source unreachable: index local declarations only
		}
		entries = append(entries, remote...)
	}
	c.swapIndex(entries)
	return nil
}

// swapIndex atomically installs a new index generation: it closes the previous
// FTS5 backing store (if any), builds a fresh FTS5 index, replaces the
// discovery and loader views, and re-seeds the registered memory registry.
// Caller must hold the write lock.
//
// Args:
//   - entries: the new index entries.
func (c *Catalog) swapIndex(entries []SkillIndexEntry) {
	old := c.discovery
	if old != nil {
		_ = old.closeFTS5() // release the previous SQLite handle
	}
	next := NewDiscovery(entries)
	if fts, ftsErr := NewFTS5Index(entries); ftsErr == nil {
		next.SetFTS5(fts)
	} // FTS5 build failure degrades to keyword matching silently
	c.discovery = next
	c.loader = NewLoader(entries)
	if c.registry != nil {
		_ = c.seedRegistryLocked(c.registry)
	}
}

// Close releases the FTS5 index backing store, if any.
//
// Returns:
//   - error: wrapped close error, or nil.
func (c *Catalog) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.discovery == nil {
		return nil
	}
	return c.discovery.closeFTS5()
}

// Search returns top-K metadata matches for a query (Level-0 only). Safe for
// concurrent use with Build/Refresh.
//
// Args:
//   - query: free-text query.
//   - limit: maximum results (<= 0 means all).
//
// Returns:
//   - []SkillIndexEntry: ranked metadata matches.
func (c *Catalog) Search(query string, limit int) []SkillIndexEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.discovery == nil {
		return nil
	}
	return c.discovery.Search(query, limit)
}

// All returns every indexed skill entry (unfiltered). Safe for concurrent use.
//
// Returns:
//   - []SkillIndexEntry: the full index snapshot.
func (c *Catalog) All() []SkillIndexEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.discovery == nil {
		return nil
	}
	return c.discovery.All()
}

// Count returns the number of indexed skills. Safe for concurrent use.
//
// Returns:
//   - int: index size.
func (c *Catalog) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.discovery == nil {
		return 0
	}
	return c.discovery.Count()
}

// Load returns the SKILL.md body for a skill ID (Level-1, on demand). Safe for
// concurrent use with Build/Refresh. For git sources the body is read from the
// live LocalDir checkout, which Refresh may rewrite mid-read — callers should
// treat a transient read/parse error as retryable.
//
// Args:
//   - id: the skill ID.
//
// Returns:
//   - string: the skill body.
//   - error: ErrSkillNotFound or wrapped read error.
func (c *Catalog) Load(id string) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.loader == nil {
		return "", ErrSkillNotFound
	}
	return c.loader.Load(id)
}

// ListReferences lists the reference resource files (references/ dir) of a
// skill — Level-2 progressive disclosure: the LLM sees which resource files
// exist and can request their content via the loader. The catalog must be
// built first. Safe for concurrent use with Build/Refresh.
//
// Args:
//   - id: the skill ID.
//
// Returns:
//   - []string: reference file names (nil when the skill has none).
//   - error: ErrSkillNotFound or wrapped read error.
func (c *Catalog) ListReferences(id string) ([]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.loader == nil {
		return nil, ErrSkillNotFound
	}
	return c.loader.ListReferences(id)
}

// ResolveTools binds a skill's manifest tool declarations to runnable
// providers under the trust gate (Level-2, on demand). The catalog must be
// built first. Safe for concurrent use with Build/Refresh. For git sources the
// manifest is read from the live LocalDir checkout, which Refresh may rewrite
// mid-read — a transient read/parse error is retryable.
//
// Args:
//   - id: the skill ID.
//
// Returns:
//   - []ResolvedTool: bound tools (may be empty).
//   - error: ErrSkillNotFound / ErrToolUntrusted / wrapped error.
func (c *Catalog) ResolveTools(id string) ([]ResolvedTool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.discovery == nil || c.loader == nil {
		return nil, ErrSkillNotFound
	}
	entry, ok := c.entryByIDLocked(id)
	if !ok {
		return nil, ErrSkillNotFound
	}
	// P1-8: Remote skills (from HTTP manifest) don't have a local skill.yaml.
	if strings.HasPrefix(entry.Path, "http://") || strings.HasPrefix(entry.Path, "https://") {
		return nil, nil
	}
	manifest, err := loadManifest(filepath.Join(entry.Path, "skill.yaml"))
	if err != nil {
		return nil, err
	}
	if manifest == nil {
		return nil, nil // no manifest -> no declared tools
	}
	return c.resolver.Resolve(manifest.Tools, entry.Source)
}

// Refresh re-indexes all declared sources — re-syncing git sources, re-fetching
// http/oci manifests and rebuilding the FTS5 index, exactly like Build — and
// returns the diff against the previous index generation (content-hash based
// change detection, design §5). On success the in-memory index, loader, FTS5
// and memory registry views are replaced atomically; on error the previous
// index is kept intact.
//
// Git sources are synced first WITHOUT holding the index write lock: a git
// pull can block for a long time on an unreachable host, and holding the lock
// would stall every concurrent Search/Load/All. Each git failure degrades to
// local-checkout-only indexing (same policy as Build).
//
// Returns:
//   - IndexChange: added / modified / removed skills since the last index.
//   - error: wrapped index error, or nil.
func (c *Catalog) Refresh() (IndexChange, error) {
	// Bound the git sync like the startup path in cmd/ares/serve.go: an
	// unreachable/stalled host must degrade to local-checkout indexing within
	// a bounded time instead of blocking a listChanged-triggered refresh.
	syncCtx, syncCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer syncCancel()
	if err := c.SyncGitSources(syncCtx); err != nil {
		// Degrade: index the local checkouts as they are; a git failure is
		// never fatal to a refresh triggered by an MCP listChanged.
		log.Warn("skill catalog: refresh git sync failed (indexing local checkouts)", "error", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	var prev []SkillIndexEntry
	if c.discovery != nil {
		prev = c.discovery.All()
	}
	sources := c.sm.Sources()
	next, err := c.indexer.Index(sources, c.sm)
	if err != nil {
		return IndexChange{}, err
	}
	for _, src := range c.httpSrcs {
		remote, fetchErr := FetchHTTPManifest(context.Background(), src)
		if fetchErr != nil {
			continue // remote source unreachable: index local declarations only
		}
		next = append(next, remote...)
	}
	change := DetectIndexChanges(prev, next)
	c.swapIndex(next)
	return change, nil
}

// SeedRegistry populates an existing knowledge/skills.Registry with the
// indexed skill descriptions (name + one-liner). This wires the catalog into
// the memory manager's resident skill block without changing its interface.
// The registry is remembered and re-seeded automatically after every
// Build/Refresh so catalog and memory never diverge. A detail loader is also
// attached so Registry.LoadDetail reads a skill's SKILL.md body on demand
// (Level-1 progressive disclosure) instead of returning an empty body.
//
// Args:
//   - reg: the target registry (may be nil, then this is a no-op).
//
// Returns:
//   - error: wrapped registration error, or nil.
func (c *Catalog) SeedRegistry(reg *skills.Registry) error {
	if reg == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.registry = reg
	// Level-1 on-demand body loading: LoadDetail falls back to the catalog's
	// Loader when the registry's in-memory Detail is empty. The closure calls
	// c.Load (read-locked) only when invoked, never while this write lock is
	// held.
	reg.SetDetailLoader(func(name string) (string, bool) {
		body, err := c.Load(name)
		if err != nil {
			return "", false
		}
		return body, true
	})
	if c.discovery == nil {
		return nil
	}
	return c.seedRegistryLocked(reg)
}

// seedRegistryLocked registers the current index entries into the target
// registry. Caller must hold the write lock.
//
// Args:
//   - reg: the target registry.
//
// Returns:
//   - error: wrapped registration error, or nil.
func (c *Catalog) seedRegistryLocked(reg *skills.Registry) error {
	for _, e := range c.discovery.All() {
		if err := reg.Register(skills.Skill{
			Name:        e.ID,
			Description: e.Description,
		}); err != nil {
			return fmt.Errorf("ares_skills: seed registry %s: %w", e.ID, err)
		}
	}
	return nil
}

// SetMCPConnector attaches a lazy MCP connector. When set, Activate connects
// every MCP server declared by the skill at activation time (and only then);
// without it, MCP tools resolve to descriptors but are never connected.
//
// Args:
//   - conn: the MCP connector (may be nil to detach).
func (c *Catalog) SetMCPConnector(conn MCPConnector) {
	c.mcp = conn
}

// Activate loads a skill's body, resolves its tools and — for MCP tools —
// lazily connects the declared servers (design §3 / acceptance #3). The skill
// must be indexed (Build) first.
//
// Args:
//   - ctx: context for the MCP connections.
//   - id: the skill ID.
//
// Returns:
//   - []ResolvedTool: the resolved tools (MCP tools are connected).
//   - error: ErrSkillNotFound / ErrToolUntrusted / wrapped error.
func (c *Catalog) Activate(ctx context.Context, id string) ([]ResolvedTool, error) {
	tools, err := c.ResolveTools(id)
	if err != nil {
		return nil, err
	}
	if c.mcp == nil {
		return tools, nil
	}
	for _, t := range tools {
		if t.Kind != ToolMCP {
			continue
		}
		if err := c.mcp.ConnectServer(ctx, t.Target); err != nil {
			return nil, fmt.Errorf("ares_skills: connect mcp %q for skill %q: %w", t.Target, id, err)
		}
	}
	return tools, nil
}

// Experience exposes the learned-source store for relevance priors.
//
// Returns:
//   - *Experience: the shared experience store.
func (c *Catalog) Experience() *Experience {
	return c.exp
}

// entryByIDLocked looks up an index entry by skill ID. Caller must hold at
// least the read lock (the discovery view may be swapped by Refresh).
//
// Args:
//   - id: the skill ID.
//
// Returns:
//   - SkillIndexEntry: the matching entry.
//   - bool: true when found.
func (c *Catalog) entryByIDLocked(id string) (SkillIndexEntry, bool) {
	for _, e := range c.discovery.All() {
		if e.ID == id {
			return e, true
		}
	}
	return SkillIndexEntry{}, false
}
