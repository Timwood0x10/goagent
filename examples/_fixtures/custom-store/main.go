// custom-store demonstrates implementing a custom ServiceStore for persistent
// service discovery (SQLite, Postgres, JSON file, etc.).
//
// Purpose:
//
//	The discovery engine persists discovered services through a ServiceStore
//	interface. The default is an in-memory store; this example implements a
//	JSON-file-backed store and shows that data survives an engine "restart",
//	which is the key property of any durable store.
//
// Learning objectives:
//   - How to implement the api/discovery.ServiceStore interface (Save / Get /
//     List / Delete).
//   - How to inject a custom store into discovery.NewEngine via EngineConfig.
//   - How to verify persistence across engine restarts.
//
// Core APIs (with package paths):
//   - discovery.NewEngine / discovery.EngineConfig (api/discovery)
//   - discovery.RegisterRequest (api/discovery)
//   - (*Engine).Register / (*Engine).List
//
// Run:
//
//	go run ./examples/custom-store
//
// Expected output:
//
//	=== Persisted to file ===
//	[...JSON of the registered service...]
//	=== After 'restart': 1 services ===
//	  my-mcp tags=[capability:search]
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Timwood0x10/ares/api/discovery"
)

func main() {
	ctx := context.Background()

	// ── Step 1: Create a temp working directory ──
	// The JSON file store needs a real file path; a temp dir keeps the demo
	// self-contained and is cleaned up on exit.
	dir, err := os.MkdirTemp("", "discovery-test")
	if err != nil {
		panic(err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup temp dir: %v\n", err)
		}
	}()

	// ── Step 2: Build a file-backed store and inject it into the engine ──
	// NewJSONFileStore is our custom ServiceStore implementation defined
	// below; passing it in EngineConfig.Store overrides the default in-memory
	// store, so every Save/Delete is persisted to services.json.
	store := NewJSONFileStore(filepath.Join(dir, "services.json"))
	engine := discovery.NewEngine(discovery.EngineConfig{
		Store: store,
	})

	// ── Step 3: Register a service ──
	// RegisterRequest carries the identity (name/endpoint/tags/metadata) of a
	// discovered MCP server. The engine assigns an ID and calls store.Save,
	// which in our custom store writes the JSON file.
	_ = engine.Register(ctx, discovery.RegisterRequest{
		Name:     "my-mcp",
		Endpoint: "/usr/bin/my-mcp",
		Tags:     []string{"capability:search"},
		Metadata: map[string]string{"version": "1.0"},
	})

	// ── Step 4: Verify the service was persisted to the file ──
	// Reading the JSON file directly proves the store's Save wrote durable
	// data rather than keeping it only in memory.
	data, _ := os.ReadFile(filepath.Join(dir, "services.json"))
	fmt.Println("=== Persisted to file ===")
	fmt.Println(string(data))

	// ── Step 5: Create a NEW engine over the same store ("restart") ──
	// A new engine with the same file-backed store must see the previously
	// registered service: this is the durability guarantee a persistent
	// ServiceStore provides over the in-memory default.
	engine2 := discovery.NewEngine(discovery.EngineConfig{
		Store: store,
	})
	services, _ := engine2.List(ctx)
	fmt.Printf("\n=== After 'restart': %d services ===\n", len(services))
	for _, svc := range services {
		fmt.Printf("  %s tags=%v\n", svc.Identity.Name, svc.Identity.Tags)
	}
}

// ── Custom ServiceStore implementation ──

// JSONFileStore is a file-backed ServiceStore for demonstration: the whole
// service list is read/written as one JSON document at `path`.
type JSONFileStore struct {
	path string
}

// ErrServiceNotFound is returned by Get/Delete when the ID is unknown.
var ErrServiceNotFound = errors.New("service not found")

// NewJSONFileStore creates a store that persists services to `path`.
func NewJSONFileStore(path string) *JSONFileStore {
	return &JSONFileStore{path: path}
}

// Save implements ServiceStore: it updates the service with the same ID or
// appends it, then writes the whole list back to the file.
func (s *JSONFileStore) Save(_ context.Context, svc *discovery.DiscoveredService) error {
	services := s.load()
	// Update or insert.
	found := false
	for i, existing := range services {
		if existing.Identity.ID == svc.Identity.ID {
			services[i] = svc
			found = true
			break
		}
	}
	if !found {
		services = append(services, svc)
	}
	return s.save(services)
}

// Get implements ServiceStore: it returns the service with the given ID or
// ErrServiceNotFound.
func (s *JSONFileStore) Get(_ context.Context, id string) (*discovery.DiscoveredService, error) {
	for _, svc := range s.load() {
		if svc.Identity.ID == id {
			return svc, nil
		}
	}
	return nil, ErrServiceNotFound
}

// List implements ServiceStore: it returns all persisted services.
func (s *JSONFileStore) List(_ context.Context) ([]*discovery.DiscoveredService, error) {
	return s.load(), nil
}

// Delete implements ServiceStore: it removes the service with the given ID and
// rewrites the file. Deleting an unknown ID is a no-op (no error).
func (s *JSONFileStore) Delete(_ context.Context, id string) error {
	services := s.load()
	filtered := make([]*discovery.DiscoveredService, 0, len(services))
	for _, svc := range services {
		if svc.Identity.ID != id {
			filtered = append(filtered, svc)
		}
	}
	return s.save(filtered)
}

// load reads and decodes the JSON file; a missing/unreadable file yields an
// empty list (a fresh store).
func (s *JSONFileStore) load() []*discovery.DiscoveredService {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	var services []*discovery.DiscoveredService
	_ = json.Unmarshal(data, &services)
	return services
}

// save encodes the service list and writes it to the JSON file.
func (s *JSONFileStore) save(services []*discovery.DiscoveredService) error {
	data, err := json.MarshalIndent(services, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}
