package runtime

// Architecture gate: every RuntimePlugin
// constructor in this package must have at least one PRODUCTION reference (a
// non-test .go file). This is the root-cause defense for the original bug
// this gate was written for: LoopPlugin was fully implemented, never wired,
// and nobody noticed for multiple releases because nothing failed.
//
// Detection: constructor = exported FuncDecl named "New*Plugin" in this
// package. A constructor is "wired" if some non-test .go file in the module
// mentions `Name(` on a line that is not its own `func Name(` declaration.
//
// Allowlist policy ("白名单起步逐个消化"): known-dead constructors are
// allowlisted with an explicit reason and a tracking pointer. Two failure
// modes:
//   - a constructor with ZERO production references that is NOT allowlisted
//     → new dead plugin: wire it or delete it, then update this file;
//   - an allowlisted constructor that GAINED a production reference
//     → the allowlist entry is stale: remove it (the gate must shrink, never
//     grow silently).
//
// Known limitations (accepted, documented): plugins instantiated via a bare
// `&SomePlugin{}` composite literal instead of a constructor are invisible;
// constructor names not matching New*Plugin are invisible.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// testOnlyPluginAllowlist maps constructor names with zero production
// references to the reason they are still allowed to exist. Every entry is a
// debt marker: the goal state is an EMPTY allowlist.
var testOnlyPluginAllowlist = map[string]string{
	"NewArenaPlugin":         "chaos/arena demo plugin; wiring tracked under chaos REVIEW #12 follow-up",
	"NewCheckpointPlugin":    "downstream registration item of W-L1 (loop clock flush) — needs a real CheckpointStore in cmd/ares wiring",
	"NewEvolutionPlugin":     "evolution record plugin; needs cmd/ares wiring alongside the population adapter",
	"NewInterruptPlugin":     "HITL interrupt plugin; no human-approval transport exists yet",
	"NewObserverPlugin":      "event-mirror plugin; needs a real EventStore in cmd/ares wiring",
	"NewBasicRecoveryPlugin": "recovery patch plugin; kernel recovery loop uses aresrecovery directly today",
	"NewToolPlugin":          "tool execution plugin; needs cmd/ares wiring to the tool registry",
}

func TestPluginsMustBeWiredInProduction(t *testing.T) {
	pkgDir := mustModuleRelativeDir(t, "internal", "runtime")
	root := findModuleRoot(t)

	constructors := discoverPluginConstructors(t, pkgDir)
	if len(constructors) == 0 {
		t.Fatal("discovered zero New*Plugin constructors — the discovery logic is broken or the package was emptied; do not let this gate rot")
	}

	refs := countReferencesInModule(t, root, constructors)

	var dead, stale []string
	for _, name := range constructors {
		reason, allowlisted := testOnlyPluginAllowlist[name]
		switch {
		case refs[name] == 0 && !allowlisted:
			dead = append(dead, name)
		case refs[name] > 0 && allowlisted:
			stale = append(stale, name+" ("+reason+")")
		}
	}

	for _, name := range dead {
		t.Errorf("plugin constructor %s has ZERO production references (test-only) — wire it via cmd/ares/startPluginBus or delete it, or add it to testOnlyPluginAllowlist with a reason", name)
	}
	for _, entry := range stale {
		t.Errorf("allowlist entry is STALE, remove it: %s now has production references and must be digested off testOnlyPluginAllowlist", entry)
	}
}

// discoverPluginConstructors AST-parses the non-test files of pkgDir and
// returns every exported FuncDecl named "New*Plugin".
func discoverPluginConstructors(t *testing.T, pkgDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	var names []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(pkgDir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || fd.Name == nil {
				continue
			}
			n := fd.Name.Name
			if fd.Name.IsExported() && strings.HasPrefix(n, "New") && strings.HasSuffix(n, "Plugin") && len(n) > len("NewPlugin") {
				names = append(names, n)
			}
		}
	}
	return names
}

// findModuleRoot walks up from the current directory until it finds go.mod.
func findModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above test directory")
		}
		dir = parent
	}
}

// mustModuleRelativeDir joins findModuleRoot with the path segments and fails
// the test if the resulting directory does not exist (guards against the
// gate outliving the package it watches).
func mustModuleRelativeDir(t *testing.T, segs ...string) string {
	t.Helper()
	p := filepath.Join(append([]string{findModuleRoot(t)}, segs...)...)
	if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
		t.Fatalf("watched package dir missing: %s", p)
	}
	return p
}

// countReferencesInModule scans every non-test .go file under root and counts,
// per identifier, occurrences of `ident(` that are not the identifier's own
// `func ident(` declaration. Line comments are stripped first (a mention in a
// comment is not a reference); block comments are NOT handled (accepted noise,
// it errs toward "alive" so the gate cannot false-positive).
func countReferencesInModule(t *testing.T, root string, idents []string) map[string]int {
	t.Helper()
	res := make(map[string]int, len(idents))
	patterns := make(map[string]*regexp.Regexp, len(idents))
	for _, id := range idents {
		patterns[id] = regexp.MustCompile(`\b` + id + `\(`)
	}

	scanned := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		for _, line := range strings.Split(string(raw), "\n") {
			if i := strings.Index(line, "//"); i >= 0 {
				line = line[:i]
			}
			for _, id := range idents {
				if patterns[id].MatchString(line) && !strings.Contains(line, "func "+id+"(") {
					res[id]++
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	if scanned < 100 {
		t.Fatalf("only %d non-test .go files scanned — module walk is broken; gate results are meaningless", scanned)
	}
	return res
}
