package engine

//nolint: errcheck // best-effort operations: ResponseWriter writes, cleanup Close/Wait, deferred shutdown
import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/sync/errgroup"

	"github.com/Timwood0x10/ares/internal/errors"
)

// ReloadCallback is called when workflows are reloaded.
type ReloadCallback func(workflows map[string]*Workflow)

// callbackWithID wraps a ReloadCallback with an ID.
type callbackWithID struct {
	id string
	fn ReloadCallback
}

// FileWatcher watches files for changes using fsnotify.
type FileWatcher struct {
	watcher      *fsnotify.Watcher
	workflows    map[string]*Workflow
	loader       WorkflowLoader
	callbacks    []callbackWithID
	callbackID   uint64
	mu           sync.RWMutex
	pollInterval time.Duration // Fallback polling interval (only used if fsnotify fails)
	wg           sync.WaitGroup
	stopCtx      context.Context
	stopCancel   context.CancelFunc
	g            *errgroup.Group
}

// NewFileWatcher creates a new FileWatcher.
func NewFileWatcher(loader WorkflowLoader, workflows map[string]*Workflow) (*FileWatcher, error) {
	if loader == nil {
		return nil, errors.New("loader cannot be nil")
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Warn("FileWatcher: fsnotify not available, falling back to polling", "error", err)
	} else {
		log.Info("FileWatcher: using fsnotify for real-time file monitoring")
	}

	stopCtx, stopCancel := context.WithCancel(context.Background())

	return &FileWatcher{
		watcher:      watcher,
		loader:       loader,
		workflows:    workflows,
		callbacks:    make([]callbackWithID, 0),
		pollInterval: 5 * time.Second,
		stopCtx:      stopCtx,
		stopCancel:   stopCancel,
	}, nil
}

// Watch starts watching workflow files for changes.
func (w *FileWatcher) Watch(ctx context.Context, dir string) error {
	if err := w.scanAndLoad(ctx, dir); err != nil {
		return err
	}

	// Create errgroup for goroutine management
	w.g, ctx = errgroup.WithContext(ctx)

	// If we have fsnotify watcher, use event-driven approach
	if w.watcher != nil {
		// Add directory to watch
		if err := w.watcher.Add(dir); err != nil {
			return errors.Wrap(err, "watch directory")
		}

		// Watch subdirectories for workflow files
		w.watchDirectory(ctx, dir)

		w.wg.Add(1)
		w.g.Go(func() error {
			w.fsnotifyLoop(dir)
			return nil
		})
	} else {
		// Fallback to polling
		w.wg.Add(1)
		w.g.Go(func() error {
			w.watchLoop(dir)
			return nil
		})
	}

	return nil
}

// watchDirectory recursively adds directories to fsnotify watch.
func (w *FileWatcher) watchDirectory(ctx context.Context, dir string) {
	if w.watcher == nil {
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			path := filepath.Join(dir, entry.Name())
			if err := w.watcher.Add(path); err != nil {
				continue
			}
			// Recursively watch subdirectories
			w.watchDirectory(ctx, path)
		}
	}
}

// fsnotifyLoop watches for file change events.
// The watcher is closed by Close() after this goroutine exits, so no
// deferred close is needed here (fixing a double-close bug).
func (w *FileWatcher) fsnotifyLoop(dir string) {
	defer w.wg.Done()

	for {
		select {
		case <-w.stopCtx.Done():
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			// Only handle write and create events
			if event.Op&fsnotify.Write == 0 && event.Op&fsnotify.Create == 0 {
				continue
			}
			// Check if it's a workflow file
			ext := filepath.Ext(event.Name)
			if ext != ".json" && ext != ".yaml" && ext != ".yml" {
				continue
			}
			// Reload on file change
			if err := w.scanAndLoad(w.stopCtx, dir); err != nil {
				continue
			}
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			log.Error("FileWatcher error", "error", err)
		}
	}
}

// watchLoop periodically checks for file changes (fallback when fsnotify unavailable).
func (w *FileWatcher) watchLoop(dir string) {
	defer w.wg.Done()
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopCtx.Done():
			return
		case <-ticker.C:
			if err := w.scanAndLoad(w.stopCtx, dir); err != nil {
				continue
			}
		}
	}
}

// Close closes the file watcher and releases resources.
func (w *FileWatcher) Close() {
	if w.stopCancel != nil {
		w.stopCancel()
	}
	w.wg.Wait()
	// Wait for errgroup to complete (ignoring errors as we're shutting down)
	if w.g != nil {
		_ = w.g.Wait()
	}
	if w.watcher != nil {
		if err := w.watcher.Close(); err != nil {
			log.Warn("reloader: close watcher failed", "error", err)
		}
		w.watcher = nil
	}
}

// scanAndLoad scans and loads workflows from directory.
// M6 fix: hold Lock across the entire compare-and-swap cycle to prevent
// TOCTOU race where concurrent scanAndLoad calls interleave read and write.
func (w *FileWatcher) scanAndLoad(ctx context.Context, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return errors.Wrap(err, "read directory")
	}

	// Build loaded workflows outside the lock (I/O is slow).
	type loadedEntry struct {
		workflow *Workflow
		modTime  time.Time
	}
	loaded := make(map[string]loadedEntry)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		ext := filepath.Ext(entry.Name())
		if ext != ".json" && ext != ".yaml" && ext != ".yml" {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		stat, err := os.Stat(path)
		if err != nil {
			continue
		}

		workflow, err := w.loader.Load(ctx, path)
		if err != nil {
			continue
		}

		loaded[workflow.ID] = loadedEntry{workflow: workflow, modTime: stat.ModTime()}
	}

	// Hold Lock for the entire compare-and-swap to prevent interleaving.
	w.mu.Lock()
	modified := false
	for id, le := range loaded {
		oldWF, exists := w.workflows[id]
		if !exists || le.modTime.After(oldWF.UpdatedAt) {
			modified = true
			break
		}
	}
	if modified {
		newWorkflows := make(map[string]*Workflow, len(loaded))
		for id, le := range loaded {
			newWorkflows[id] = le.workflow
		}
		w.workflows = newWorkflows
	}
	w.mu.Unlock()

	if modified {
		w.notifyCallbacks()
	}

	return nil
}

// notifyCallbacks notifies all registered callbacks.
// M7 fix: deep-copy the workflows map before passing to callbacks so that
// callbacks cannot mutate the shared map or see inconsistent state.
func (w *FileWatcher) notifyCallbacks() {
	w.mu.RLock()
	workflowsCopy := make(map[string]*Workflow, len(w.workflows))
	for k, v := range w.workflows {
		workflowsCopy[k] = v
	}
	callbacks := w.callbacks
	w.mu.RUnlock()

	for _, cb := range callbacks {
		cb.fn(workflowsCopy)
	}
}

// RegisterCallback registers a callback for reload events and returns the callback ID.
func (w *FileWatcher) RegisterCallback(callback ReloadCallback) string {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.callbackID++
	id := fmt.Sprintf("callback-%d", w.callbackID)
	w.callbacks = append(w.callbacks, callbackWithID{
		id: id,
		fn: callback,
	})
	return id
}

// UnregisterCallback removes a callback by ID.
func (w *FileWatcher) UnregisterCallback(callbackID string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	for i, cb := range w.callbacks {
		if cb.id == callbackID {
			w.callbacks = append(w.callbacks[:i], w.callbacks[i+1:]...)
			return
		}
	}
}

// WorkflowReloader manages workflow hot reloading.
type WorkflowReloader struct {
	loader     WorkflowLoader
	workflows  map[string]*Workflow
	callbackID uint64
	callbacks  map[string]ReloadCallback // Use map for O(1) lookup
	mu         sync.RWMutex
	watcher    *FileWatcher
	cancel     context.CancelFunc
	cancelCtx  context.Context
}

// NewWorkflowReloader creates a new WorkflowReloader.
func NewWorkflowReloader(loader WorkflowLoader) *WorkflowReloader {
	ctx, cancel := context.WithCancel(context.Background())
	return &WorkflowReloader{
		loader:    loader,
		workflows: make(map[string]*Workflow),
		callbacks: make(map[string]ReloadCallback),
		cancelCtx: ctx,
		cancel:    cancel,
	}
}

// Load workflows from a directory.
func (r *WorkflowReloader) Load(ctx context.Context, dir string) error {
	loader, ok := r.loader.(*FileLoader)
	if !ok {
		return ErrInvalidLoader
	}

	dirLoader := NewDirectoryLoader(loader)
	workflows, err := dirLoader.LoadAll(ctx, dir)
	if err != nil {
		return errors.Wrap(err, "load workflows")
	}

	r.mu.Lock()
	r.workflows = workflows
	r.mu.Unlock()

	return nil
}

// StartWatching starts watching for file changes.
func (r *WorkflowReloader) StartWatching(ctx context.Context, dir string) error {
	loader, ok := r.loader.(*FileLoader)
	if !ok {
		return ErrInvalidLoader
	}

	dirLoader := NewDirectoryLoader(loader)
	workflows, err := dirLoader.LoadAll(ctx, dir)
	if err != nil {
		return errors.Wrap(err, "load workflows")
	}

	r.mu.Lock()
	r.workflows = workflows
	r.mu.Unlock()

	watcher, err := NewFileWatcher(r.loader, r.workflows)
	if err != nil {
		return errors.Wrap(err, "create file watcher")
	}
	watcher.RegisterCallback(r.onReload)

	// Use reloader's cancel context for watching
	if err := watcher.Watch(r.cancelCtx, dir); err != nil {
		return errors.Wrap(err, "start watcher")
	}

	r.watcher = watcher

	return nil
}

// onReload handles workflow reload events.
func (r *WorkflowReloader) onReload(workflows map[string]*Workflow) {
	r.mu.Lock()
	r.workflows = workflows
	r.mu.Unlock()

	r.notifyCallbacks()
}

// notifyCallbacks notifies all registered callbacks.
// M7 fix: deep-copy the workflows map before passing to callbacks so that
// callbacks cannot mutate the shared map or see inconsistent state.
func (r *WorkflowReloader) notifyCallbacks() {
	r.mu.RLock()
	workflowsCopy := make(map[string]*Workflow, len(r.workflows))
	for k, v := range r.workflows {
		workflowsCopy[k] = v
	}
	callbacksCopy := make(map[string]ReloadCallback, len(r.callbacks))
	for k, v := range r.callbacks {
		callbacksCopy[k] = v
	}
	r.mu.RUnlock()

	for _, callback := range callbacksCopy {
		callback(workflowsCopy)
	}
}

// RegisterCallback registers a callback for reload events and returns the callback ID.
func (r *WorkflowReloader) RegisterCallback(callback ReloadCallback) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.callbackID++
	id := fmt.Sprintf("callback-%d", r.callbackID)
	r.callbacks[id] = callback
	return id
}

// UnregisterCallback removes a callback by ID.
func (r *WorkflowReloader) UnregisterCallback(callbackID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.callbacks, callbackID)
}

// GetWorkflow returns a workflow by ID.
func (r *WorkflowReloader) GetWorkflow(id string) (*Workflow, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	workflow, exists := r.workflows[id]
	return workflow, exists
}

// ListWorkflows returns all loaded workflows.
func (r *WorkflowReloader) ListWorkflows() []*Workflow {
	r.mu.RLock()
	defer r.mu.RUnlock()

	workflows := make([]*Workflow, 0, len(r.workflows))
	for _, wf := range r.workflows {
		workflows = append(workflows, wf)
	}

	return workflows
}

// StopWatching stops watching for file changes.
func (r *WorkflowReloader) StopWatching() {
	if r.cancel != nil {
		r.cancel()
	}
	if r.watcher != nil {
		r.watcher.Close()
		r.watcher = nil
	}
}
