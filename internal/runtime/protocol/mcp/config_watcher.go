package ares_mcp

//nolint: errcheck // best-effort operations: ResponseWriter writes, cleanup Close/Wait, deferred shutdown
import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"
)

// configDebounceWindow is how long the watcher waits for events to settle
// before triggering a single reload (#29).
const configDebounceWindow = 200 * time.Millisecond

// MCPConfigWatcher watches an MCP config file for changes and hot-reloads
// the MCPManager when the file is modified.
type MCPConfigWatcher struct {
	manager *MCPManager
	path    string
	watcher *fsnotify.Watcher
	done    chan struct{}
}

// MCPConfigFile holds the YAML structure for the MCP config file.
type MCPConfigFile struct {
	MCP struct {
		Servers []struct {
			Name      string `yaml:"name"`
			Enabled   bool   `yaml:"enabled"`
			AutoStart bool   `yaml:"auto_start"`
			Timeout   int    `yaml:"timeout"`
			Transport struct {
				Type  string `yaml:"type"`
				Stdio *struct {
					Command string            `yaml:"command"`
					Args    []string          `yaml:"args"`
					Env     map[string]string `yaml:"env,omitempty"`
					WorkDir string            `yaml:"work_dir,omitempty"`
				} `yaml:"stdio,omitempty"`
				SSE *struct {
					URL     string            `yaml:"url"`
					Headers map[string]string `yaml:"headers,omitempty"`
					Timeout int               `yaml:"timeout,omitempty"`
				} `yaml:"sse,omitempty"`
			} `yaml:"transport"`
		} `yaml:"servers"`
	} `yaml:"mcp"`
}

// NewMCPConfigWatcher creates a watcher that monitors the given YAML config file
// and applies changes to the MCPManager on every modification.
//
// The watcher uses fsnotify for file events with a short debounce to avoid
// reacting to partial writes from editors.
func NewMCPConfigWatcher(manager *MCPManager, configPath string) (*MCPConfigWatcher, error) {
	if manager == nil {
		return nil, errors.New("mcp manager is required")
	}
	if configPath == "" {
		return nil, errors.New("config path is required")
	}

	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}

	// Verify the file exists.
	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("config file not found: %s", absPath)
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create fsnotify watcher: %w", err)
	}

	// Watch both the file and its directory to catch editor save patterns
	// (some editors write a temp file then rename).
	if err := w.Add(absPath); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("watch config file: %w", err)
	}
	dir := filepath.Dir(absPath)
	if err := w.Add(dir); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("watch config dir: %w", err)
	}

	return &MCPConfigWatcher{
		manager: manager,
		path:    absPath,
		watcher: w,
		done:    make(chan struct{}),
	}, nil
}

// Start begins watching the config file for changes. It blocks until the context
// is cancelled or the watcher encounters a fatal error. Run it in a goroutine.
func (cw *MCPConfigWatcher) Start(ctx context.Context) error {
	// Debounce timer: coalesce rapid file events into a single reload.
	var debounce *time.Timer

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case event, ok := <-cw.watcher.Events:
			if !ok {
				return nil
			}
			// Only react to write/create/rename events for our config file.
			if !isRelevantEvent(event, cw.path) {
				continue
			}

			// Debounce (#29): keep consuming events and resetting the timer
			// until a full debounce window elapses with no relevant event.
			// The previous form waited only on timer/ctx, so every burst of
			// N events produced N reloads spaced 200ms apart — no coalescing.
			if debounce == nil {
				debounce = time.NewTimer(configDebounceWindow)
			} else {
				if !debounce.Stop() {
					select {
					case <-debounce.C:
					default:
					}
				}
				debounce.Reset(configDebounceWindow)
			}
			for {
				select {
				case ev, ok := <-cw.watcher.Events:
					if !ok {
						return nil
					}
					if isRelevantEvent(ev, cw.path) {
						if !debounce.Stop() {
							select {
							case <-debounce.C:
							default:
							}
						}
						debounce.Reset(configDebounceWindow)
					}
				case <-debounce.C:
					cw.reload(ctx)
					// Consume any event already drained into the channel
					// buffer so the outer select doesn't immediately re-fire.
					debounce = nil
				case <-ctx.Done():
					return ctx.Err()
				}
				if debounce == nil {
					break
				}
			}

		case err, ok := <-cw.watcher.Errors:
			if !ok {
				return nil
			}
			log.Warn("mcp: config watcher error", "error", err)
		}
	}
}

// Stop shuts down the file watcher.
func (cw *MCPConfigWatcher) Stop() error {
	return cw.watcher.Close()
}

// reload reads the config file and applies changes to the manager.
func (cw *MCPConfigWatcher) reload(ctx context.Context) {
	data, err := os.ReadFile(cw.path)
	if err != nil {
		log.Warn("mcp: hot-reload read config", "path", cw.path, "error", err)
		return
	}

	var cfgFile MCPConfigFile
	if err := yaml.Unmarshal(data, &cfgFile); err != nil {
		log.Warn("mcp: hot-reload parse config", "path", cw.path, "error", err)
		return
	}

	mgrCfg := convertConfigFile(&cfgFile)
	changes := cw.manager.ApplyConfig(ctx, mgrCfg)
	if len(changes) > 0 {
		log.Info("mcp: hot-reload applied", "changes", changes)
	}
}

// isRelevantEvent checks if a file event should trigger a config reload.
func isRelevantEvent(event fsnotify.Event, configPath string) bool {
	// Normalize paths for comparison.
	eventName, _ := filepath.Abs(event.Name)
	cfgAbs, _ := filepath.Abs(configPath)

	// The event's file is our config file.
	if eventName == cfgAbs {
		return event.Has(fsnotify.Write) || event.Has(fsnotify.Create)
	}

	// If the event is on the directory, check if it's a rename to our file
	// (common atomic save pattern: write temp → rename over target).
	if event.Has(fsnotify.Rename) || event.Has(fsnotify.Create) {
		if _, err := os.Stat(cfgAbs); err == nil {
			return true
		}
	}

	return false
}

// convertConfigFile converts the YAML config structure to MCPManagerConfig.
func convertConfigFile(cfgFile *MCPConfigFile) *MCPManagerConfig {
	if cfgFile == nil || len(cfgFile.MCP.Servers) == 0 {
		return nil
	}
	mgrCfg := &MCPManagerConfig{
		Servers: make([]MCPServerConfig, 0, len(cfgFile.MCP.Servers)),
	}
	for _, s := range cfgFile.MCP.Servers {
		sc := MCPServerConfig{
			Name:      s.Name,
			Enabled:   s.Enabled,
			AutoStart: s.AutoStart,
			Timeout:   time.Duration(s.Timeout) * time.Second,
			Transport: TransportConfig{
				Type: s.Transport.Type,
			},
		}
		switch s.Transport.Type {
		case TransportTypeStdio:
			if s.Transport.Stdio != nil {
				sc.Transport.Stdio = &StdioConfig{
					Command: s.Transport.Stdio.Command,
					Args:    s.Transport.Stdio.Args,
					Env:     s.Transport.Stdio.Env,
					WorkDir: s.Transport.Stdio.WorkDir,
				}
			}
		case TransportTypeSSE:
			if s.Transport.SSE != nil {
				sc.Transport.SSE = &SSEConfig{
					URL:     s.Transport.SSE.URL,
					Headers: s.Transport.SSE.Headers,
					Timeout: time.Duration(s.Transport.SSE.Timeout) * time.Second,
				}
			}
		}
		mgrCfg.Servers = append(mgrCfg.Servers, sc)
	}
	return mgrCfg
}
