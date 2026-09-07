package ares_skills

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Timwood0x10/ares/internal/knowledge/skills"
)

// TestCatalogRefreshConcurrent runs Refresh from many goroutines while
// readers Search concurrently. It must be run with -race: overlapping
// Refresh calls used to race on discovery/loader swaps before the catalog
// mutex was introduced.
func TestCatalogRefreshConcurrent(t *testing.T) {
	root := t.TempDir()
	makeSkillDir(t, root, "alpha", "Alpha", "alpha skill", "")
	makeSkillDir(t, root, "beta", "Beta", "beta skill", "")
	cat := NewCatalog(CatalogConfig{ProjectSkillsDir: root})
	if err := cat.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}

	const workers = 8
	const rounds = 20
	var wg sync.WaitGroup
	errCh := make(chan error, workers*rounds)

	// Writers: concurrent Refresh calls.
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				if _, err := cat.Refresh(); err != nil {
					errCh <- err
					return
				}
			}
		}()
	}
	// Readers: concurrent Search/Load/All during the swaps.
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				_ = cat.Search("alpha", 5)
				_, _ = cat.Load("alpha")
				_ = cat.All()
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent refresh error: %v", err)
	}
	if got := cat.Count(); got != 2 {
		t.Fatalf("want 2 skills after concurrent refreshes, got %d", got)
	}
}

// TestCatalogRefreshPreservesHTTPSourcesAndFTS5 verifies Build->Refresh
// parity: HTTP/OCI manifest entries survive a Refresh and the FTS5 index is
// re-attached (a previous divergence dropped both on the first Refresh).
func TestCatalogRefreshPreservesHTTPSourcesAndFTS5(t *testing.T) {
	root := t.TempDir()
	makeSkillDir(t, root, "local", "Local", "local skill", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"skills":[{"id":"remote","name":"Remote","description":"remote skill","version":"0.1.0"}]}`))
	}))
	defer srv.Close()

	cat := NewCatalog(CatalogConfig{ProjectSkillsDir: root})
	cat.SetHTTPSources([]HTTPSource{{URL: srv.URL}})
	if err := cat.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if cat.Count() != 2 {
		t.Fatalf("after Build want 2 entries, got %d", cat.Count())
	}

	// Refresh must preserve the http source and re-attach FTS5.
	if _, err := cat.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if cat.Count() != 2 {
		t.Fatalf("after Refresh want 2 entries (http source preserved), got %d", cat.Count())
	}
	matches := cat.Search("remote", 5)
	if len(matches) != 1 || matches[0].ID != "remote" {
		t.Fatalf("remote skill must survive Refresh, got %+v", matches)
	}
	// FTS5-backed search must still work after Refresh (index re-attached).
	ftsMatches := cat.Search("local skill", 5)
	if len(ftsMatches) == 0 {
		t.Fatal("FTS5-backed search must work after Refresh")
	}
}

// TestCatalogRefreshReseedsRegistry verifies the memory registry is re-seeded
// after Refresh: skills added between Build and Refresh appear in the
// registered registry without an explicit SeedRegistry call.
func TestCatalogRefreshReseedsRegistry(t *testing.T) {
	root := t.TempDir()
	makeSkillDir(t, root, "alpha", "Alpha", "alpha skill", "")
	cat := NewCatalog(CatalogConfig{ProjectSkillsDir: root})
	if err := cat.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	reg := skills.NewRegistry()
	if err := cat.SeedRegistry(reg); err != nil {
		t.Fatalf("SeedRegistry: %v", err)
	}

	// Add a skill after Build, then Refresh: it must appear in the registry.
	makeSkillDir(t, root, "beta", "Beta", "beta skill", "")
	if _, err := cat.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !reg.Has("beta") {
		t.Fatal("registry must be re-seeded after Refresh with the new skill")
	}
}
