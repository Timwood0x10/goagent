package ares_skills

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// TrustLevel is the permission tier for a resolved tool (design §9: the
// smallest possible trust gate — Discovered -> Declared -> Trusted? -> Allowed?).
type TrustLevel int

const (
	// TrustUntrusted means the tool must not be executed without explicit
	// user approval (e.g. external or learned sources).
	TrustUntrusted TrustLevel = iota
	// TrustAsk means the tool requires confirmation before execution.
	TrustAsk
	// TrustAllowed means the tool may execute freely.
	TrustAllowed
)

// trustForSource maps a skill source kind to its default trust tier
// (design §9 trust matrix).
//
// Args:
//   - kind: the declaring source.
//
// Returns:
//   - TrustLevel: the default trust for tools declared by that source.
func trustForSource(kind SourceKind) TrustLevel {
	switch kind {
	case SourceProject:
		return TrustAllowed // project-local: user opted into the repo
	case SourceUser:
		return TrustAllowed // user explicitly installed it
	case SourceRegistered:
		return TrustAsk // declared extra dirs still get a confirmation gate
	default:
		return TrustUntrusted
	}
}

// Resolver implements ToolResolver: it binds a skill's manifest tool
// declarations to runnable providers under the trust gate. It never invokes
// tools itself — it only produces ResolvedTool descriptors.
type Resolver struct {
	// allowLocalExecutables permits executable tools declared by trusted
	// sources (config `[tools].allow_local_executables`).
	allowLocalExecutables bool
	// builtins are the known framework builtin tool names.
	builtins map[string]bool
}

// NewResolver creates a ToolResolver.
//
// Args:
//   - allowLocalExecutables: whether trusted sources may declare executables.
//   - builtins: known builtin tool names.
//
// Returns:
//   - *Resolver: ready to resolve manifest declarations.
func NewResolver(allowLocalExecutables bool, builtins []string) *Resolver {
	set := make(map[string]bool, len(builtins))
	for _, b := range builtins {
		set[b] = true
	}
	return &Resolver{allowLocalExecutables: allowLocalExecutables, builtins: set}
}

// Resolve binds a manifest's tool declarations for a skill of the given
// source kind. Each declaration becomes a ResolvedTool only when it passes
// the trust and existence gates; invalid declarations produce an error so the
// caller can surface a broken skill instead of silently dropping tools.
//
// Args:
//   - decls: the manifest tool declarations.
//   - kind: the declaring source kind (drives the trust gate).
//
// Returns:
//   - []ResolvedTool: bound tools (may be empty).
//   - error: ErrToolUntrusted or wrapped error, or nil.
func (r *Resolver) Resolve(decls []ToolDecl, kind SourceKind) ([]ResolvedTool, error) {
	trust := trustForSource(kind)
	var out []ResolvedTool
	for _, d := range decls {
		tool, err := r.resolveOne(d, trust)
		if err != nil {
			return nil, err
		}
		out = append(out, tool)
	}
	return out, nil
}

// resolveOne binds a single declaration to a provider.
//
// Args:
//   - d: the tool declaration.
//   - trust: the source trust tier.
//
// Returns:
//   - ResolvedTool: the bound tool.
//   - error: ErrToolUntrusted / invalid declaration, or nil.
func (r *Resolver) resolveOne(d ToolDecl, trust TrustLevel) (ResolvedTool, error) {
	switch strings.ToLower(d.Type) {
	case string(ToolBuiltin):
		if !r.builtins[d.Name] {
			return ResolvedTool{}, fmt.Errorf("ares_skills: unknown builtin tool %q", d.Name)
		}
		return ResolvedTool{ID: d.ID, Kind: ToolBuiltin, Target: d.Name}, nil

	case string(ToolMCP):
		if d.Server == "" {
			return ResolvedTool{}, fmt.Errorf("ares_skills: mcp tool %q missing server", d.ID)
		}
		return ResolvedTool{ID: d.ID, Kind: ToolMCP, Target: d.Server}, nil

	case string(ToolExecutable):
		if trust == TrustUntrusted {
			return ResolvedTool{}, fmt.Errorf("%w: executable %q from untrusted source", ErrToolUntrusted, d.Command)
		}
		if !r.allowLocalExecutables {
			return ResolvedTool{}, fmt.Errorf("%w: local executables disabled", ErrToolUntrusted)
		}
		if d.Command == "" {
			return ResolvedTool{}, fmt.Errorf("ares_skills: executable tool %q missing command", d.ID)
		}
		// Declaration-only verification: the command must resolve in PATH or
		// be an existing project-relative path. No scanning beyond that.
		if !executableExists(d.Command) {
			return ResolvedTool{}, fmt.Errorf("ares_skills: declared executable %q not found", d.Command)
		}
		return ResolvedTool{ID: d.ID, Kind: ToolExecutable, Target: d.Command, Args: d.Args}, nil

	default:
		return ResolvedTool{}, fmt.Errorf("ares_skills: unsupported tool type %q", d.Type)
	}
}

// executableExists verifies a declared executable without scanning the disk:
// either it resolves in PATH (LookPath) or it is an existing relative path.
//
// Args:
//   - command: the declared command.
//
// Returns:
//   - bool: true when the executable exists.
func executableExists(command string) bool {
	if command == "" {
		return false
	}
	// Relative or absolute filesystem path (contains a separator — both \ and
	// / so forward-slash commands keep the direct-existence branch on
	// Windows): check existence directly. A bare name with no separator is a
	// PATH lookup only.
	if strings.ContainsAny(command, "\\/") {
		if _, err := exec.LookPath(command); err == nil {
			return true
		}
		info, err := filepathAbs(command)
		return err == nil && info
	}
	// Bare name: PATH lookup only.
	_, err := exec.LookPath(command)
	return err == nil
}

// filepathAbs reports whether a path exists as a regular file or symlink.
//
// Args:
//   - path: the path to check.
//
// Returns:
//   - bool: true when it exists.
//   - error: nil (absence is not an error; only the existence flag matters).
func filepathAbs(path string) (bool, error) {
	_, err := os.Stat(path)
	return err == nil, nil
}

// ErrToolUntrusted is returned when a tool declaration fails the trust gate.
var ErrToolUntrusted = errors.New("ares_skills: tool untrusted")
