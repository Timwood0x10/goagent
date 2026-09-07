// mcp-registry demonstrates MCP service discovery and lifecycle management.
//
// Purpose:
//
//	This example walks through the full lifecycle of MCP (Model Context
//	Protocol) services in the discovery engine: active discovery, passive
//	registration, listing, tag management, and unregistration — with an
//	event callback showing every lifecycle change as it happens.
//
// Learning objectives:
//   - How the discovery engine's phases map to API calls (DiscoverNow →
//     Register → List → UpdateTags → Unregister).
//   - How OnEvent delivers lifecycle events (added/removed/updated) for
//     observability or audit.
//   - How tags express service capabilities (capability:code, type:query...)
//     and are mutated with UpdateTags.
//
// Core APIs (with package paths):
//   - discovery.NewEngine / discovery.EngineConfig (api/discovery)
//   - (*Engine).OnEvent / DiscoverNow / Register / List / UpdateTags /
//     Unregister
//   - discovery.NewMemoryStore / discovery.RegisterRequest /
//     discovery.UpdateTagsRequest
//
// Run:
//
//	go run ./examples/mcp-registry
//
// Expected output:
//
//	=== Phase 1: Discovery ===
//	  Discovered N MCP server(s)
//	=== Phase 2: Registration ===
//	  ✓ Registered codegraph ... (plus event lines)
//	=== Phase 3/4/5 === list, tag update, unregister
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/Timwood0x10/ares/api/discovery"
)

func main() {
	ctx := context.Background()

	// ── Step 1: Create the engine over an in-memory store ──
	// NewMemoryStore is the default store; passing it explicitly mirrors the
	// production pattern where a durable store would be injected instead.
	store := discovery.NewMemoryStore()
	engine := discovery.NewEngine(discovery.EngineConfig{
		ProjectDir: ".",
		Store:      store,
	})

	// ── Step 2: Subscribe to lifecycle events ──
	// Every Register/Unregister/UpdateTags fires an event; printing them
	// shows the engine's lifecycle in action.
	engine.OnEvent(func(evt discovery.Event) {
		fmt.Printf("  [event] %-25s %s\n", evt.Type, evt.ServiceID)
	})

	// ── Step 3: Active discovery ──
	// DiscoverNow scans configured providers for MCP servers; List then
	// returns whatever was found.
	fmt.Println("=== Phase 1: Discovery ===")
	if err := engine.DiscoverNow(ctx); err != nil {
		log.Printf("discovery: %v", err)
	}
	services, err := engine.List(ctx)
	if err != nil {
		log.Fatalf("list: %v", err)
	}
	fmt.Printf("  Discovered %d MCP server(s)\n", len(services))
	for _, svc := range services {
		fmt.Printf("    %s — %s\n", svc.Identity.Name, svc.Identity.Endpoint)
	}

	// ── Step 4: Passive registration ──
	// Register manually adds known MCP servers (not auto-discovered). Tags
	// declare capabilities so a later query can match by capability.
	fmt.Println("\n=== Phase 2: Registration ===")
	mockServices := []discovery.RegisterRequest{
		{
			Name:     "codegraph",
			Endpoint: "codegraph serve --mcp",
			Tags:     []string{"capability:code", "type:query"},
		},
		{
			Name:     "postgres-mcp",
			Endpoint: "postgres-mcp --url postgres://localhost/mydb",
			Tags:     []string{"capability:database", "type:query"},
		},
		{
			Name:     "web-search-mcp",
			Endpoint: "web-search-mcp serve",
			Tags:     []string{"capability:search", "type:query"},
		},
		{
			Name:     "file-manager",
			Endpoint: "file-manager serve",
			Tags:     []string{"capability:filesystem", "type:action"},
		},
	}
	for _, reg := range mockServices {
		if err := engine.Register(ctx, reg); err != nil {
			fmt.Printf("  ✗ register %s: %v\n", reg.Name, err)
		} else {
			fmt.Printf("  ✓ Registered %s\n", reg.Name)
		}
	}

	// ── Step 5: List all services ──
	// After discovery + registration the registry holds both sources; list
	// shows endpoints and capability tags.
	fmt.Println("\n=== Phase 3: All Services ===")
	all, err := engine.List(ctx)
	if err != nil {
		log.Fatalf("list: %v", err)
	}
	for _, svc := range all {
		fmt.Printf("  %s:\n", svc.Identity.Name)
		fmt.Printf("    endpoint:  %s\n", svc.Identity.Endpoint)
		if len(svc.Identity.Tags) > 0 {
			fmt.Printf("    tags:      %v\n", svc.Identity.Tags)
		}
	}

	// ── Step 6: Tag management ──
	// UpdateTags adds/removes tags on an existing service — reclassifying a
	// capability without re-registering.
	fmt.Println("\n=== Phase 4: Tag Management ===")
	if err := engine.UpdateTags(ctx, "codegraph", discovery.UpdateTagsRequest{
		Add: []string{"domain:source-code"},
	}); err != nil {
		fmt.Printf("  ✗ update tags: %v\n", err)
	} else {
		fmt.Println("  ✓ Updated tags on codegraph")
	}

	// ── Step 7: Cleanup ──
	// Unregister removes a service (firing an event); the final List confirms
	// the registry shrank.
	fmt.Println("\n=== Phase 5: Cleanup ===")
	if err := engine.Unregister(ctx, "file-manager"); err != nil {
		fmt.Printf("  ✗ unregister: %v\n", err)
	} else {
		fmt.Println("  ✓ Unregistered file-manager")
	}

	services, err = engine.List(ctx)
	if err != nil {
		log.Fatalf("list: %v", err)
	}
	fmt.Printf("  Remaining: %d services\n", len(services))
}
