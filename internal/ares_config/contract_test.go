package ares_config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// G2 config contract gate: every leaf field of
// MemoryConfig/ServerConfig/... must be consumed outside the config package
// itself (validate / defaults / redacted don't count — self-referential).
// Fields with no consumer make the YAML a lie; wire them, remove them, or
// add them to knownDead with a reason.
//
// Precision: config fields are accessed as cfg.<SubStruct>.<Field>. We parse
// the declaring sub-struct for each field from config.go and count only
// qualified references outside ares_config — a bare ".Field" grep would match
// every same-named field in the repo and be worthless as a signal.
//
// A reference counts as a consumer only when the access path ends at the
// match (next char is not an identifier byte and, for wholesale sub-struct
// passes, not a '.'): ".Memory" must NOT match ".MemoryStore", and
// "cfg.Memory.SessionMemory.X" must NOT count as a wholesale pass of
// ".Memory" — navigation into a field is not consumption of the parent.

// knownDead lists config subtrees (dot paths from Config, e.g.
// "Memory.SessionMemory" exempts every leaf under it) or single leaves that
// are display-only, consumed only through receiver methods defined in
// this package, or pending removal. Keys are ACCESS paths — Config
// field names, what callers write (cfg.Memory.X) — NOT type names. Adding an
// entry requires a reason comment.
var knownDead = map[string]string{
	"Tools.Defaults":            "C4 backlog",
	"Tools.Agents":              "C4 backlog",
	"Memory.SessionMemory":      "C4 backlog (subtree)",
	"Memory.UserProfile":        "C4 backlog (subtree)",
	"Memory.TaskDistillation":   "C4 backlog (subtree)",
	"Memory.EnableDistillation": "read only via MemoryConfig.DistillationEnabled() (tri-state accessor)",
	"Workflow.AutoReload":       "C4 backlog",
	"Workflow.DefinitionPath":   "C4 backlog",
	"Workflow.ReloadInterval":   "C4 backlog",
	"Validation":                "C4 backlog (subtree): validated + defaulted, no runtime consumer",
	"Validation.CustomSchema":   "C4 backlog",
	"Output":                    "C4 backlog (subtree): CLI formatting never wired to a renderer",
	"Prompts.ProfileExtraction": "C4 backlog",
	"Prompts.StyleAnalysis":     "C4 backlog",
	"Prompts.Recommendation":    "consumer retired with the ReAct executor (M4-D); L2 cognition prompts come from the plan graph",
	"Storage.PGVector":          "C4 backlog (subtree)",
	"Embedding.RedisAddr":       "C4 backlog",
}

// TestG2ConfigContract is the G2 gate: every non-whitelisted config leaf has
// at least one qualified consumer outside ares_config — either the FIELD-NAME
// access path itself (e.g. ".Kernel.PollInterval") or a wholesale pass of one
// of its parent sub-structs (e.g. cfg.Memory.Archive handed to a constructor
// covers every leaf under it).
func TestG2ConfigContract(t *testing.T) {
	// structTypes: type name → ordered fields (name, fieldType) parsed from
	// config.go.
	structTypes := parseConfigStructs(t)

	// Walk the tree from Config: for each struct-typed field, extend the
	// access path; for each leaf (non-config type), register the needle.
	needles := map[string]string{} // needle (".A.B.C") → "Type.Field" provenance
	var walk func(typeName, prefix string)
	walk = func(typeName, prefix string) {
		for _, fld := range structTypes[typeName] {
			accessPath := prefix + "." + fld.name
			if _, isStruct := structTypes[fld.fieldType]; isStruct {
				walk(fld.fieldType, accessPath)
				continue
			}
			needles[accessPath] = typeName + "." + fld.name
		}
	}
	walk("Config", "")

	// Precompute every parent path of each needle (".A.B", ".A.B.C") so a
	// sub-struct passed WHOLESALE to a helper covers all its leaves. The
	// wholesale match requires the path to END at the match (allowDot=false),
	// so "cfg.Memory.Archive.MaxRounds" does not cover ".Memory.Archive.Dir".
	//
	// Depth-1 roots (".Evolution") ARE included — this codebase really does
	// hand whole top-level sections to providers (ProvideEvolution(&cfg.
	// Evolution, ...)) — but only when the receiver is a config value, see
	// configReceiver. Without that guard a bare ".Output" would match
	// exp.Output / result.Output and neuter the gate.
	prefixes := map[string][]string{} // needle → ordered parent paths
	for needle := range needles {
		parts := strings.Split(strings.TrimPrefix(needle, "."), ".")
		var chains []string
		for i := 1; i < len(parts); i++ {
			chains = append(chains, "."+strings.Join(parts[:i], "."))
		}
		prefixes[needle] = chains
	}

	// Scan all non-test Go files outside ares_config.
	consumers := map[string]int{}     // needle → direct field accesses
	wholesale := map[string]int{}     // parent path → whole-value uses
	root := filepath.Join("..", "..") // repo root (test lives in internal/ares_config)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.Contains(filepath.ToSlash(path), "ares_config/") {
			return nil
		}
		f, ferr := os.Open(path)
		if ferr != nil {
			return nil
		}
		defer func() { _ = f.Close() }()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Text()
			for needle := range needles {
				if containsAccess(line, needle, true) {
					consumers[needle]++
				}
			}
			for _, chains := range prefixes {
				for _, p := range chains {
					if containsWholesale(line, p) {
						wholesale[p]++
					}
				}
			}
		}
		if err := sc.Err(); err != nil {
			// A file we could not fully scan would produce phantom "dead"
			// findings, so fail loudly instead of under-counting consumers.
			return fmt.Errorf("scan %s: %w", path, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	var dead []string
	for needle, prov := range needles {
		if consumers[needle] > 0 {
			continue
		}
		// Parent consumed wholesale (sub-struct handed to a helper as a
		// value): its leaves are covered.
		covered := false
		for _, p := range prefixes[needle] {
			if wholesale[p] > 0 {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		if whitelisted(strings.TrimPrefix(needle, ".")) {
			continue
		}
		dead = append(dead, needle+" ["+prov+"]")
	}
	sort.Strings(dead)
	for _, d := range dead {
		t.Errorf("config contract violated: %s has no consumer outside ares_config (wire, remove, or whitelist with reason)", d)
	}
}

// whitelisted reports whether the access path (no leading dot) is exempt:
// either directly in knownDead or because a whitelisted ANCESTOR subtree
// covers it ("Memory.SessionMemory" exempts "Memory.SessionMemory.MaxHistory").
func whitelisted(path string) bool {
	for p := path; p != ""; {
		if _, ok := knownDead[p]; ok {
			return true
		}
		i := strings.LastIndex(p, ".")
		if i < 0 {
			return false
		}
		p = p[:i]
	}
	return false
}

// containsAccess reports whether line references path such that the path
// ENDS at the match. The byte after a match must not be an identifier byte
// (so ".Memory" does not match ".MemoryStore"); when allowDot is true a
// following '.' is also accepted — that is a method call or index on the
// leaf value itself. When allowDot is false (wholesale sub-struct pass) a
// following '.' means navigation into a child field, which does NOT consume
// the parent as a value.
func containsAccess(line, path string, allowDot bool) bool {
	_, ok := findAccess(line, path, allowDot, 0)
	return ok
}

// containsWholesale reports whether line passes the sub-struct at path as a
// WHOLE value off a config receiver, e.g. `ProvideEvolution(&cfg.Evolution)`.
// The receiver check is what makes depth-1 roots usable: a bare ".Output"
// match would otherwise be satisfied by `exp.Output` or `result.Output`,
// which are unrelated struct fields, and would silently exempt every leaf
// under OutputConfig.
func containsWholesale(line, path string) bool {
	for from := 0; ; {
		at, ok := findAccess(line, path, false, from)
		if !ok {
			return false
		}
		if isConfigReceiver(line[:at]) {
			return true
		}
		from = at + 1
	}
}

// configReceivers are the identifiers this codebase uses for an ares_config
// value. Anything else owning a same-named field (comp.Evolution, exp.Output)
// is not a config access.
var configReceivers = map[string]bool{
	"cfg": true, "c": true, "acfg": true, "config": true, "conf": true,
}

// isConfigReceiver reports whether the text immediately preceding a match
// ends in a config receiver identifier (an optional '&' is ignored, so both
// `cfg.Evolution` and `&cfg.Evolution` qualify).
func isConfigReceiver(before string) bool {
	end := len(before)
	i := end
	for i > 0 && isIdentByte(before[i-1]) {
		i--
	}
	if i == end {
		return false // no identifier directly before the dot
	}
	return configReceivers[before[i:end]]
}

// findAccess locates the first match of path at or after from where the path
// ENDS at the match, returning its start offset. See containsAccess for the
// allowDot semantics.
func findAccess(line, path string, allowDot bool, from int) (int, bool) {
	for from <= len(line) {
		i := strings.Index(line[from:], path)
		if i < 0 {
			return 0, false
		}
		at := from + i
		next := at + len(path)
		if next >= len(line) {
			return at, true
		}
		switch c := line[next]; {
		case isIdentByte(c):
			// longer identifier continues the token — not this path
		case c == '.' && allowDot:
			return at, true
		case c == '.':
			// deeper chain: navigation, not a whole-value use
		default:
			return at, true
		}
		from = at + 1
	}
	return 0, false
}

// isIdentByte reports whether c can appear inside a Go identifier.
func isIdentByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

// cfgField is one member of a config struct.
type cfgField struct {
	name      string
	fieldType string
}

// fieldDeclRe matches a struct member line: exported name, type, yaml tag.
var fieldDeclRe = regexp.MustCompile(`^\s+([A-Z][A-Za-z0-9_]+)\s+([A-Za-z0-9_\.\[\]\*]+)\s+.*yaml:"`)

// structDeclRe matches a type declaration opening a struct block.
var structDeclRe = regexp.MustCompile(`^type ([A-Za-z0-9_]+) struct`)

// parseConfigStructs scans config.go for `type X struct` blocks and returns
// type name → ordered fields.
func parseConfigStructs(t *testing.T) map[string][]cfgField {
	t.Helper()
	out := map[string][]cfgField{}
	f, err := os.Open("config.go")
	if err != nil {
		t.Fatalf("open config.go: %v", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	current := ""
	depth := 0
	for sc.Scan() {
		line := sc.Text()
		if m := structDeclRe.FindStringSubmatch(line); m != nil && depth == 0 {
			current = m[1]
			depth = 1
			continue
		}
		if current == "" {
			continue
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if depth <= 0 {
			current = ""
			depth = 0
			continue
		}
		if m := fieldDeclRe.FindStringSubmatch(line); m != nil {
			out[current] = append(out[current], cfgField{name: m[1], fieldType: m[2]})
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan config.go: %v", err)
	}
	return out
}
