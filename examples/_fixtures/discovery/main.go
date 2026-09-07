// discovery demonstrates the Service Discovery Engine (api/discovery).
//
// Purpose:
//
//	This example shows the full lifecycle of service discovery: active
//	discovery of MCP servers, passive registration of known services, tag
//	management, health checking, listing with confidence/source details, and
//	unregistration — all through the public api/discovery engine.
//
// Learning objectives:
//   - How discovery.NewEngine orchestrates providers, stores, and events.
//   - The two registration paths: active (DiscoverNow) and passive (Register).
//   - How tag updates, health checks, and unregistration mutate service state.
//   - How per-service confidence and source records are aggregated for display.
//
// Core APIs (with package paths):
//   - discovery.NewEngine / discovery.EngineConfig (api/discovery)
//   - (*Engine).DiscoverNow / Register / UpdateTags / CheckHealth / List /
//     Unregister / OnEvent
//   - discovery.RegisterRequest / discovery.UpdateTagsRequest
//
// Run:
//
//	go run ./examples/discovery
//
// Expected output:
//
//	=== 1. Active Discovery ===
//	  Found N services
//	=== 2. Passive Registration ===
//	  ✓ Registered my-custom-mcp
//	... (tags, health, list with confidence, unregister)
package main

import (
	"context"
	"fmt"

	"github.com/Timwood0x10/ares/api/discovery"
)

func main() {
	ctx := context.Background()

	// ── Step 1: Create the engine with a pluggable store ──
	// The engine owns discovery providers, the service store, and health
	// checks. Store nil falls back to an in-memory store; pass your own
	// ServiceStore (SQLite, Postgres, JSON file — see custom-store example)
	// for durability.
	engine := discovery.NewEngine(discovery.EngineConfig{
		ProjectDir: "",
		Store:      nil, // Uses MemoryStore by default.
	})

	// ── Step 2: Subscribe to lifecycle events ──
	// OnEvent registers a callback fired on service add/remove/update and
	// health changes; production systems persist these to an audit store.
	engine.OnEvent(func(evt discovery.Event) {
		fmt.Printf("  [event] %-25s %s\n", evt.Type, evt.ServiceID)
	})

	// ── Step 3: Active discovery ──
	// DiscoverNow runs the configured providers (e.g. scanning for MCP
	// servers) and records what it finds; then List returns the accumulated
	// services.
	fmt.Println("=== 1. Active Discovery ===")
	_ = engine.DiscoverNow(ctx)

	services, _ := engine.List(ctx)
	fmt.Printf("  Found %d services\n", len(services))

	// ── Step 4: Passive registration ──
	// Register adds a service the engine did not discover itself — e.g. a
	// manually configured MCP endpoint. Tags and metadata make the service
	// findable by capability and team.
	fmt.Println("\n=== 2. Passive Registration ===")
	err := engine.Register(ctx, discovery.RegisterRequest{
		Name:     "my-custom-mcp",
		Endpoint: "/usr/local/bin/my-custom-mcp",
		Tags:     []string{"capability:analytics", "domain:business"},
		Metadata: map[string]string{"team": "platform", "env": "prod"},
	})
	if err != nil {
		fmt.Printf("  register error: %v\n", err)
	} else {
		fmt.Println("  ✓ Registered my-custom-mcp")
	}

	// ── Step 5: Tag management ──
	// UpdateTags atomically adds and removes tags on a service — useful for
	// reclassifying capabilities without re-registering.
	fmt.Println("\n=== 3. Tag Management ===")
	err = engine.UpdateTags(ctx, "my-custom-mcp", discovery.UpdateTagsRequest{
		Add:    []string{"capability:export", "priority:high"},
		Remove: []string{"domain:business"},
	})
	if err != nil {
		fmt.Printf("  update tags error: %v\n", err)
	} else {
		fmt.Println("  ✓ Updated tags on my-custom-mcp")
	}

	// ── Step 6: Health check ──
	// CheckHealth probes every registered service and records Healthy /
	// HealthMsg / CheckedAt on each.
	fmt.Println("\n=== 4. Health Check ===")
	_ = engine.CheckHealth(ctx)

	// ── Step 7: List all services with aggregated details ──
	// Each service carries Records (per-source confidence) and health state;
	// bestConfidence and sourceList aggregate them for display.
	fmt.Println("\n=== 5. All Services ===")
	services, _ = engine.List(ctx)
	for _, svc := range services {
		conf := bestConfidence(svc)
		healthIcon := "✗"
		healthMsg := "unchecked"
		if svc.CheckedAt != nil {
			if svc.Healthy {
				healthIcon = "✓"
			}
			healthMsg = svc.HealthMsg
		}

		fmt.Printf("\n  %s %s\n", healthIcon, svc.Identity.Name)
		fmt.Printf("    endpoint:    %s\n", svc.Identity.Endpoint)
		fmt.Printf("    confidence:  %d%%\n", conf)
		fmt.Printf("    sources:     %s\n", sourceList(svc))
		fmt.Printf("    health:      %s\n", healthMsg)
		if len(svc.Identity.Tags) > 0 {
			fmt.Printf("    tags:        %v\n", svc.Identity.Tags)
		}
		if len(svc.Identity.Metadata) > 0 {
			fmt.Printf("    metadata:    %v\n", svc.Identity.Metadata)
		}
	}

	// ── Step 8: Unregister ──
	// Unregister removes the service and emits an EventServiceRemoved.
	fmt.Println("\n=== 6. Unregister ===")
	_ = engine.Unregister(ctx, "my-custom-mcp")
	fmt.Println("  ✓ Unregistered my-custom-mcp")

	services, _ = engine.List(ctx)
	fmt.Printf("  Remaining: %d services\n", len(services))
}

// bestConfidence returns the highest confidence across all discovery records
// of a service, used to show how certain the engine is about it.
func bestConfidence(svc *discovery.DiscoveredService) discovery.Confidence {
	var best discovery.Confidence
	for _, r := range svc.Records {
		if r.Confidence > best {
			best = r.Confidence
		}
	}
	return best
}

// sourceList renders the distinct discovery sources with their confidence,
// e.g. "mcp-scan(95%), config(80%)", deduplicating repeated sources.
func sourceList(svc *discovery.DiscoveredService) string {
	sources := make([]string, 0, len(svc.Records))
	seen := make(map[string]bool)
	for _, r := range svc.Records {
		if !seen[r.Source] {
			seen[r.Source] = true
			sources = append(sources, fmt.Sprintf("%s(%d%%)", r.Source, r.Confidence))
		}
	}
	result := ""
	for i, s := range sources {
		if i > 0 {
			result += ", "
		}
		result += s
	}
	return result
}
