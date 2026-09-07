package engine

// Architecture gate 6.2: every exported type
// declared in types.go must have at least one PRODUCTION reference outside its
// declaring file (a non-test .go file). This is the dead-declaration defense:
// types like LoopConfig / ConditionFunc survived multiple releases declaring
// capabilities the engine never executed.
//
// Detection: exported TypeSpecs parsed from types.go. A type is "referenced"
// if some non-test .go file OTHER than types.go contains the type name as a
// whole word. Referencing only within types.go does NOT count — a field
// declaration inside another dead type is exactly the "declared but never
// executed" trap this gate exists for.
//
// Allowlist policy ("白名单起步逐个消化"): known-dead types are allowlisted
// with an explicit reason. Two failure modes:
//   - a type with ZERO outside references that is NOT allowlisted → new dead
//     declaration: wire it or delete it, then update this file;
//   - an allowlisted type that GAINED an outside reference → the allowlist
//     entry is stale: remove it (the gate must shrink, never grow silently).
//
// Known limitations (accepted, documented): same-named types in other packages
// produce false "alive" verdicts; scope is types.go only (the declaration hub
// of this package), not every file.

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

// deadTypeAllowlist maps exported types with zero production references
// outside types.go to the reason they are still allowed to exist. Every entry
// is a debt marker: the goal state is an EMPTY allowlist.
var deadTypeAllowlist = map[string]string{
	"InterruptConfig":   "HITL interrupt config; engine has no interrupt executor — NewInterruptPlugin is the future home (ares_runtime)",
	"WorkflowExecution": "legacy whole-run state container; execution lives in StepResult + kernel scheduling",
	"StepState":         "superseded by StepResult fields (status/started/finished); ares_runtime has its own StepStateSnapshot",
	"WorkflowStatus":    "only referenced by dead types (WorkflowExecution/WorkflowResult fields); orphaned once those are digested",
	"WorkflowResult":    "engine never produces a whole-run result type; dataflow ends at StepResult",
}

func TestExportedTypesMustHaveProductionReferences(t *testing.T) {
	typesGo := filepath.Join(findModuleRoot(t), "internal", "fabric", "task", "workflow", "engine", "types.go")
	if _, err := os.Stat(typesGo); err != nil {
		t.Fatalf("types.go not found: %v", err)
	}

	exported := discoverExportedTypes(t, typesGo)
	if len(exported) == 0 {
		t.Fatal("discovered zero exported types — the discovery logic is broken or types.go was emptied; do not let this gate rot")
	}

	refs := countReferencesOutside(t, typesGo, exported)

	var dead, stale []string
	for _, name := range exported {
		reason, allowlisted := deadTypeAllowlist[name]
		switch {
		case refs[name] == 0 && !allowlisted:
			dead = append(dead, name)
		case refs[name] > 0 && allowlisted:
			stale = append(stale, name+" ("+reason+")")
		}
	}

	for _, name := range dead {
		t.Errorf("exported type %s has ZERO production references outside types.go — wire it or delete it, or add it to deadTypeAllowlist with a reason", name)
	}
	for _, entry := range stale {
		t.Errorf("allowlist entry is STALE, remove it: %s now has production references and must be digested off deadTypeAllowlist", entry)
	}
}

// discoverExportedTypes AST-parses declaring file and returns every exported
// TypeSpec name.
func discoverExportedTypes(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var names []string
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if ok && ts.Name.IsExported() {
				names = append(names, ts.Name.Name)
			}
		}
	}
	return names
}

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

// countReferencesOutside scans every non-test .go file under the module root
// except the declaring file, and counts whole-word occurrences of each
// identifier. Line comments are stripped first (a mention in a comment is not
// a reference); block comments are NOT handled (accepted noise, it errs
// toward "alive" so the gate cannot false-positive).
func countReferencesOutside(t *testing.T, declaringFile string, idents []string) map[string]int {
	t.Helper()
	root := findModuleRoot(t)
	res := make(map[string]int, len(idents))
	patterns := make(map[string]*regexp.Regexp, len(idents))
	for _, id := range idents {
		patterns[id] = regexp.MustCompile(`\b` + id + `\b`)
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
		if path == declaringFile {
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
				if patterns[id].MatchString(line) {
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
