package sdk

import (
	"context"
	"errors"
	"testing"

	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/linker"
	"github.com/Timwood0x10/ares/internal/knowledge/pipeline"
	"github.com/Timwood0x10/ares/internal/knowledge/planner"
	"github.com/Timwood0x10/ares/internal/knowledge/provider"
	khruntime "github.com/Timwood0x10/ares/internal/knowledge/runtime"
	memory "github.com/Timwood0x10/ares/internal/runtime/memory"
)

// newTestConfig returns a fresh default config so each subtest starts from a
// known baseline. Tests mutate the returned config before passing it to the
// wiring helper under test.
func newTestConfig() *config {
	return defaultConfig()
}

// newTestKnowledgeRuntime constructs a minimal but real KnowledgeRuntime so
// wireSDKRetrievers can build a KnowledgeRetriever against it. Mirrors the
// construction shape used by wireKnowledge (sdk.go) and the runtime package's
// own buildTestRuntime helper, without taking a dependency on a live store.
func newTestKnowledgeRuntime() *khruntime.KnowledgeRuntime {
	pipe := knowledge.NewKnowledgePipeline(
		[]knowledge.Normalizer{&pipeline.DefaultNormalizer{MaxRawBytes: 10240}},
		[]knowledge.EntityMatcher{&pipeline.DefaultEntityMatcher{MatchThreshold: 0.6}},
		[]knowledge.Validator{&pipeline.DefaultValidator{}},
		[]knowledge.Summarizer{&pipeline.DefaultSummarizer{MaxSummaryLen: 200}},
	)
	reg := provider.NewProviderRegistry()
	discovery := planner.NewSourceDiscovery(reg, planner.NewQueryPlanner())
	return khruntime.New(
		planner.NewKnowledgePlanner(),
		discovery,
		reg,
		pipe,
		[]khruntime.Linker{
			&khruntime.DefaultLinker{},
			&linker.DecisionLinker{},
			&linker.ArchitectureLinker{},
			&linker.TimelineLinker{},
			&linker.SimilarityLinker{},
		},
		[]khruntime.Reducer{&khruntime.DefaultReducer{}},
	)
}

// TestWireMemory_Basic exercises the top-level wireMemory dispatcher across
// the basic configurations that do NOT require live embedding/postgres deps.
// The distill_with_deps path is skipped because we cannot stand up real
// services in unit tests.
func TestWireMemory_Basic(t *testing.T) {
	tests := []struct {
		name       string
		memEnabled bool
		distill    bool
		embURL     string
		dbHost     string
		ragTopK    int
		ragMin     float64
		wantMgr    bool
		wantErr    bool
		skipLive   bool
	}{
		{
			name:       "basic_enabled",
			memEnabled: true,
			distill:    false,
			wantMgr:    true,
			wantErr:    false,
		},
		{
			// wireMemory is normally only called when memCfg.Enabled is true
			// (New gates it), but the helper itself does not re-check the
			// flag — buildMemoryConfig forces mc.Enabled=true. Calling it
			// directly with memCfg.Enabled=false still yields a working
			// compression-only manager. This subtest documents that behavior
			// so future refactors do not silently regress the contract.
			name:       "disabled_caller_skipped_but_helper_still_works",
			memEnabled: false,
			distill:    false,
			wantMgr:    true,
			wantErr:    false,
		},
		{
			name:       "distill_no_deps_graceful_fallback",
			memEnabled: true,
			distill:    true,
			embURL:     "",
			dbHost:     "",
			wantMgr:    true,
			wantErr:    false,
		},
		{
			name:       "distill_with_deps_requires_live_services",
			memEnabled: true,
			distill:    true,
			embURL:     "http://localhost:8000",
			dbHost:     "localhost",
			wantMgr:    true,
			wantErr:    false,
			skipLive:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.skipLive {
				t.Skip("requires live embedding service + postgres; cannot run in unit tests")
			}
			cfg := newTestConfig()
			cfg.memCfg.Enabled = tt.memEnabled
			cfg.distillCfg.Enabled = tt.distill
			cfg.embedCfg.ServiceURL = tt.embURL
			cfg.dbCfg.Host = tt.dbHost
			if tt.ragTopK > 0 {
				cfg.memCfg.EnableRAG = true
				cfg.memCfg.RAGTopK = tt.ragTopK
				cfg.memCfg.RAGMinScore = tt.ragMin
			}
			w, err := wireMemory(context.Background(), cfg)
			verifyWiringResult(t, tt.wantErr, tt.wantMgr, w, err)
		})
	}
}

// verifyWiringResult asserts the post-conditions shared by every
// TestWireMemory_Basic subtest: error expectations are honored, the manager
// is non-nil when expected, BuildContext runs cleanly, and cleanup (when
// present) does not panic. Extracted to keep TestWireMemory_Basic under the
// 100-line limit.
func verifyWiringResult(t *testing.T, wantErr, wantMgr bool, w *memoryWiring, err error) {
	t.Helper()
	if wantErr {
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		return
	}
	if err != nil {
		t.Fatalf("wireMemory error: %v", err)
	}
	if w == nil {
		t.Fatal("expected non-nil wiring")
	}
	if wantMgr && w.mgr == nil {
		t.Fatal("expected non-nil MemoryManager")
	}
	if w.mgr == nil {
		return
	}
	// Manager must be usable: BuildContext must not error and must return the
	// input string at minimum (no session yet). Cleanup is nil for the
	// compression-only path; safe to call only when non-nil.
	ctx := context.Background()
	out, berr := w.mgr.BuildContext(ctx, "hello", "session-missing")
	if berr != nil {
		t.Fatalf("BuildContext error: %v", berr)
	}
	if out == "" {
		t.Error("BuildContext returned empty string for non-empty input")
	}
	if w.cleanup != nil {
		w.cleanup()
	}
}

// TestWireDistillationDeps_MissingDeps verifies that wireDistillationDeps
// returns ErrDistillDepsMissing (wrapped) whenever embedding URL or database
// host is missing. This is the sentinel wireMemory relies on to fall back to
// a compression-only manager instead of failing the whole Runtime.
func TestWireDistillationDeps_MissingDeps(t *testing.T) {
	tests := []struct {
		name    string
		embURL  string
		dbHost  string
		wantErr bool
	}{
		{
			name:   "no_embedding",
			embURL: "",
			dbHost: "localhost",
		},
		{
			name:   "no_database",
			embURL: "http://x",
			dbHost: "",
		},
		{
			name:   "both_missing",
			embURL: "",
			dbHost: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newTestConfig()
			cfg.embedCfg.ServiceURL = tt.embURL
			cfg.dbCfg.Host = tt.dbHost

			embClient, expRepo, cleanup, err := wireDistillationDeps(
				context.Background(), cfg,
			)
			if err == nil {
				t.Fatal("expected error wrapping ErrDistillDepsMissing, got nil")
			}
			if !errors.Is(err, ErrDistillDepsMissing) {
				t.Fatalf("expected ErrDistillDepsMissing wrap, got: %v", err)
			}
			if embClient != nil {
				t.Error("expected nil embedding client on error")
			}
			if expRepo != nil {
				t.Error("expected nil experience repo on error")
			}
			if cleanup != nil {
				t.Error("expected nil cleanup on error")
			}
		})
	}
}

// TestWireSDKRetrievers verifies wireSDKRetrievers is best-effort and
// non-fatal: missing deps are skipped, a real KnowledgeRuntime is wired when
// supplied, and a nil manager does not panic.
func TestWireSDKRetrievers(t *testing.T) {
	t.Run("nil_deps_no_panic", func(t *testing.T) {
		cfg := newTestConfig()
		memMgr := mustBuildManager(t, cfg)

		// All deps nil; helper must log warnings and return without panicking.
		wireSDKRetrievers(context.Background(), cfg, memMgr, nil, nil, nil, nil, "")
	})

	t.Run("with_knowledge_runtime_no_panic", func(t *testing.T) {
		cfg := newTestConfig()
		memMgr := mustBuildManager(t, cfg)
		knowRt := newTestKnowledgeRuntime()

		// Real KnowledgeRuntime but no embedding/expRepo: knowledge retriever
		// may wire, memory retriever skipped. Must not panic or error.
		wireSDKRetrievers(context.Background(), cfg, memMgr, nil, nil, knowRt, nil, "")
	})

	t.Run("nil_manager_no_panic", func(t *testing.T) {
		cfg := newTestConfig()
		knowRt := newTestKnowledgeRuntime()

		// nil memMgr: type assertion yields (nil, false), helper logs and
		// returns. Critical: must not panic on the nil interface assertion.
		wireSDKRetrievers(context.Background(), cfg, nil, nil, nil, knowRt, nil, "")
	})
}

// mustBuildManager constructs a compression-only MemoryManager via wireMemory
// so retriever tests have a real retrieverSetter to inject into. Fails the
// test if construction errors. Memory is force-enabled to make the helper's
// intent explicit even though wireMemory does not re-check the flag.
func mustBuildManager(t *testing.T, cfg *config) memory.MemoryManager {
	t.Helper()
	cfg.memCfg.Enabled = true
	w, err := wireMemory(context.Background(), cfg)
	if err != nil {
		t.Fatalf("wireMemory: %v", err)
	}
	if w == nil || w.mgr == nil {
		t.Fatal("wireMemory returned nil manager")
	}
	return w.mgr
}

// TestNewMemoryManager_RAGConfig verifies that applying WithRAG to a config
// yields a manager whose BuildContext runs cleanly. Since MemoryConfig is not
// exposed on the MemoryManager interface, we assert behaviorally: a manager
// built from a RAG-enabled config must still serve BuildContext without error
// even when no retrievers are wired (retrieval is a no-op then).
func TestNewMemoryManager_RAGConfig(t *testing.T) {
	cfg := newTestConfig()
	if err := WithRAG(5, 0.4)(cfg); err != nil {
		t.Fatalf("WithRAG: %v", err)
	}
	if !cfg.memCfg.EnableRAG {
		t.Fatal("expected memCfg.EnableRAG=true after WithRAG")
	}

	w, err := wireMemory(context.Background(), cfg)
	if err != nil {
		t.Fatalf("wireMemory: %v", err)
	}
	if w == nil || w.mgr == nil {
		t.Fatal("expected non-nil manager")
	}

	// Behavioral check: BuildContext must succeed and echo the input when no
	// session exists. RAG retrieval is a no-op without wired retrievers, so
	// this also verifies the RAG-enabled path does not panic when retrievers
	// are absent.
	ctx := context.Background()
	out, err := w.mgr.BuildContext(ctx, "rag-test-input", "no-such-session")
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}
	if out == "" {
		t.Error("BuildContext returned empty string")
	}
	if w.cleanup != nil {
		w.cleanup()
	}
}

// TestWireMemory_GracefulFallbackDoesNotPanic specifically asserts the
// distillation-fallback path: with distillation enabled but no embedding URL
// and no database host, wireMemory must NOT panic, NOT error, and return a
// working compression-only manager. This is the contract New() relies on to
// keep the Runtime bootstrapping even when optional deps are absent.
func TestWireMemory_GracefulFallbackDoesNotPanic(t *testing.T) {
	cfg := newTestConfig()
	cfg.memCfg.Enabled = true
	cfg.distillCfg.Enabled = true
	// Intentionally leave embedCfg.ServiceURL and dbCfg.Host empty.
	cfg.embedCfg.ServiceURL = ""
	cfg.dbCfg.Host = ""

	w, err := wireMemory(context.Background(), cfg)
	if err != nil {
		t.Fatalf("expected nil error on graceful fallback, got: %v", err)
	}
	if w == nil {
		t.Fatal("expected non-nil wiring")
	}
	if w.mgr == nil {
		t.Fatal("expected non-nil manager after fallback")
	}

	// The fallback path must NOT construct a distiller, so embClient/expRepo
	// must remain nil and cleanup must be safe (nil or no-op).
	if w.embClient != nil {
		t.Error("expected nil embClient on fallback path")
	}
	if w.expRepo != nil {
		t.Error("expected nil expRepo on fallback path")
	}
	if w.cleanup != nil {
		// Safe to call: cleanup from the fallback path is never set, but
		// invoke it defensively to prove it does not panic if ever wired.
		w.cleanup()
	}

	// Final guard: BuildContext must still work on the fallback manager.
	ctx := context.Background()
	if _, err := w.mgr.BuildContext(ctx, "fallback-check", "missing-session"); err != nil {
		t.Fatalf("BuildContext on fallback manager: %v", err)
	}
}
