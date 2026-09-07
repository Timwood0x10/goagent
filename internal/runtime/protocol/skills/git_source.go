package ares_skills

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GitSource is a declared remote skill source cloned into a local cache
// directory (config.toml `[[skill_sources]] type = "git"`). The local
// checkout is indexed like a registered directory; the URL is the only thing
// discovered — never a scan.
type GitSource struct {
	// URL is the git repository URL.
	URL string `toml:"url"`
	// LocalDir is the cache directory the repository is cloned into.
	LocalDir string `toml:"local_dir"`
}

// SyncGitSource ensures the git source is cloned (or refreshed) into its
// local cache directory. A shallow clone is used when the checkout is absent;
// an existing checkout is refreshed with a fast-forward pull. Any failure is
// returned so the caller can decide whether to skip the source.
//
// Args:
//   - ctx: context for cancellation.
//   - src: the declared git source.
//
// Returns:
//   - error: wrapped clone/pull error, or nil.
func SyncGitSource(ctx context.Context, src GitSource) error {
	if src.URL == "" || src.LocalDir == "" {
		return errors.New("ares_skills: git source needs url and local_dir")
	}
	if _, err := os.Stat(filepath.Join(src.LocalDir, ".git")); err != nil {
		// Absent or broken checkout: (re)clone shallowly.
		if mkErr := os.MkdirAll(filepath.Dir(src.LocalDir), 0o750); mkErr != nil {
			return fmt.Errorf("ares_skills: mkdir git cache: %w", mkErr)
		}
		// URL and LocalDir come from the declared config (config.toml), not
		// from untrusted user input; the command is fixed with only these two
		// declared arguments.
		cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", src.URL, src.LocalDir) //nolint:gosec // declared config source
		if out, runErr := cmd.CombinedOutput(); runErr != nil {
			return fmt.Errorf("ares_skills: git clone %s: %v: %s", src.URL, runErr, strings.TrimSpace(string(out)))
		}
		return nil
	}
	// Existing checkout: fast-forward refresh.
	cmd := exec.CommandContext(ctx, "git", "-C", src.LocalDir, "pull", "--ff-only") //nolint:gosec // declared config source
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		return fmt.Errorf("ares_skills: git pull %s: %v: %s", src.LocalDir, runErr, strings.TrimSpace(string(out)))
	}
	return nil
}
