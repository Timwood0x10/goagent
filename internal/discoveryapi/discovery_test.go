package discoveryapi_test

import (
	"context"
	"testing"

	discovery "github.com/Timwood0x10/ares/internal/discoveryapi"
)

func TestEngine_RegisterAndList(t *testing.T) {
	engine := discovery.NewEngine(discovery.EngineConfig{})
	ctx := context.Background()

	err := engine.Register(ctx, discovery.RegisterRequest{
		Name:     "my-tool",
		Endpoint: "/usr/bin/my-tool",
		Tags:     []string{"capability:search"},
		Metadata: map[string]string{"version": "1.0"},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	services, err := engine.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(services))
	}
	if services[0].Identity.Name != "my-tool" {
		t.Errorf("expected name 'my-tool', got %q", services[0].Identity.Name)
	}
	if services[0].BestSource != "register" {
		t.Errorf("expected source 'register', got %q", services[0].BestSource)
	}
}

func TestEngine_UpdateTags(t *testing.T) {
	engine := discovery.NewEngine(discovery.EngineConfig{})
	ctx := context.Background()

	_ = engine.Register(ctx, discovery.RegisterRequest{
		Name: "x", Endpoint: "/x", Tags: []string{"a", "b"},
	})

	err := engine.UpdateTags(ctx, "x", discovery.UpdateTagsRequest{
		Add:    []string{"c"},
		Remove: []string{"a"},
	})
	if err != nil {
		t.Fatalf("update tags: %v", err)
	}

	svc, _ := engine.Get(ctx, "x")
	tagSet := make(map[string]bool)
	for _, tag := range svc.Identity.Tags {
		tagSet[tag] = true
	}
	if !tagSet["b"] || !tagSet["c"] || tagSet["a"] {
		t.Errorf("expected tags {b, c}, got %v", svc.Identity.Tags)
	}
}

func TestEngine_Unregister(t *testing.T) {
	engine := discovery.NewEngine(discovery.EngineConfig{})
	ctx := context.Background()

	_ = engine.Register(ctx, discovery.RegisterRequest{Name: "x", Endpoint: "/x"})
	_ = engine.Unregister(ctx, "x")

	svc, _ := engine.Get(ctx, "x")
	if svc != nil {
		t.Error("expected nil after unregister")
	}
}

func TestEngine_OnEvent(t *testing.T) {
	engine := discovery.NewEngine(discovery.EngineConfig{})
	ctx := context.Background()

	var events []discovery.Event
	engine.OnEvent(func(evt discovery.Event) {
		events = append(events, evt)
	})

	_ = engine.Register(ctx, discovery.RegisterRequest{Name: "x", Endpoint: "/x"})

	if len(events) == 0 {
		t.Fatal("expected at least 1 event")
	}
	if events[0].Type != discovery.EventServiceAdded {
		t.Errorf("expected EventServiceAdded, got %s", events[0].Type)
	}
}

func TestEngine_RegisterValidation(t *testing.T) {
	engine := discovery.NewEngine(discovery.EngineConfig{})
	ctx := context.Background()

	err := engine.Register(ctx, discovery.RegisterRequest{})
	if err == nil {
		t.Error("expected error for empty name")
	}

	err = engine.Register(ctx, discovery.RegisterRequest{Name: "x"})
	if err == nil {
		t.Error("expected error for empty endpoint")
	}
}

func TestEngine_DiscoverNow(t *testing.T) {
	engine := discovery.NewEngine(discovery.EngineConfig{})
	ctx := context.Background()

	// Should not error even with default providers.
	err := engine.DiscoverNow(ctx)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	_, _ = engine.List(ctx)
}
