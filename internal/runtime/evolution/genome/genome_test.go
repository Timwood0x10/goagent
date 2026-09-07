package genome

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── mockGenome ───────────────────────────────

type mockGenome struct {
	name     string
	mutateFn func(ctx context.Context, n int) ([]Genome, error)
	fitness  float64
	snapshot any
}

func (m *mockGenome) Name() string                                              { return m.name }
func (m *mockGenome) Mutate(ctx context.Context, n int) ([]Genome, error)       { return m.mutateFn(ctx, n) }
func (m *mockGenome) Crossover(_ context.Context, other Genome) (Genome, error) { return m, nil }
func (m *mockGenome) Fitness(_ context.Context) (float64, error)                { return m.fitness, nil }
func (m *mockGenome) Snapshot(_ context.Context) (any, error)                   { return m.snapshot, nil }

// ── NewRegistry ─────────────────────────────

func TestNewRegistry(t *testing.T) {
	r := NewRegistry()
	require.NotNil(t, r)
	assert.Len(t, r.genomes, 0)
}

// ── Register ────────────────────────────────

func TestRegistry_Register(t *testing.T) {
	r := NewRegistry()
	g := &mockGenome{name: "workflow"}
	err := r.Register(g)
	assert.NoError(t, err)
	assert.Len(t, r.genomes, 1)
}

func TestRegistry_Register_Duplicate(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(&mockGenome{name: "workflow"}))
	err := r.Register(&mockGenome{name: "workflow"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")
}

func TestRegistry_Register_Nil(t *testing.T) {
	r := NewRegistry()
	err := r.Register(nil)
	assert.Error(t, err)
}

func TestRegistry_Register_EmptyName(t *testing.T) {
	r := NewRegistry()
	err := r.Register(&mockGenome{name: ""})
	assert.Error(t, err)
}

func TestRegistry_Register_Multiple(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(&mockGenome{name: "workflow"}))
	require.NoError(t, r.Register(&mockGenome{name: "knowledge"}))
	require.NoError(t, r.Register(&mockGenome{name: "scheduler"}))
	assert.Len(t, r.genomes, 3)
}

// ── Get ──────────────────────────────────────

func TestRegistry_Get(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(&mockGenome{name: "workflow"}))

	g, err := r.Get("workflow")
	assert.NoError(t, err)
	assert.Equal(t, "workflow", g.Name())
}

func TestRegistry_Get_NotFound(t *testing.T) {
	r := NewRegistry()
	_, err := r.Get("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// ── List ─────────────────────────────────────

func TestRegistry_List(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(&mockGenome{name: "workflow"}))
	require.NoError(t, r.Register(&mockGenome{name: "knowledge"}))

	names := r.List()
	assert.ElementsMatch(t, []string{"workflow", "knowledge"}, names)
}

func TestRegistry_List_Empty(t *testing.T) {
	r := NewRegistry()
	assert.Len(t, r.List(), 0)
}

// ── Unregister ───────────────────────────────

func TestRegistry_Unregister(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(&mockGenome{name: "workflow"}))

	err := r.Unregister("workflow")
	assert.NoError(t, err)
	assert.Len(t, r.List(), 0)
}

func TestRegistry_Unregister_NotFound(t *testing.T) {
	r := NewRegistry()
	err := r.Unregister("nonexistent")
	assert.Error(t, err)
}

// ── Mutate ───────────────────────────────────

func TestGenome_Mutate(t *testing.T) {
	g := &mockGenome{
		name: "test",
		mutateFn: func(_ context.Context, n int) ([]Genome, error) {
			children := make([]Genome, n)
			for i := 0; i < n; i++ {
				children[i] = &mockGenome{name: fmt.Sprintf("child-%d", i), fitness: float64(i) * 0.1}
			}
			return children, nil
		},
	}

	children, err := g.Mutate(context.Background(), 3)
	assert.NoError(t, err)
	assert.Len(t, children, 3)
	for i, child := range children {
		assert.Equal(t, fmt.Sprintf("child-%d", i), child.Name())
	}
}

func TestGenome_Mutate_Zero(t *testing.T) {
	g := &mockGenome{
		name: "test",
		mutateFn: func(_ context.Context, n int) ([]Genome, error) {
			return []Genome{}, nil
		},
	}

	children, err := g.Mutate(context.Background(), 0)
	assert.NoError(t, err)
	assert.Len(t, children, 0)
}

// ── Fitness ──────────────────────────────────

func TestGenome_Fitness(t *testing.T) {
	g := &mockGenome{name: "test", fitness: 0.85}
	score, err := g.Fitness(context.Background())
	assert.NoError(t, err)
	assert.InDelta(t, 0.85, score, 0.001)
}

func TestGenome_Fitness_Zero(t *testing.T) {
	g := &mockGenome{name: "test", fitness: 0.0}
	score, err := g.Fitness(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, 0.0, score)
}

// ── Snapshot ─────────────────────────────────

func TestGenome_Snapshot(t *testing.T) {
	g := &mockGenome{name: "test", snapshot: "v1.0"}
	snap, err := g.Snapshot(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, "v1.0", snap)
}

// ── Crossover ────────────────────────────────

func TestGenome_Crossover(t *testing.T) {
	parentA := &mockGenome{name: "parent-a", fitness: 0.9}
	parentB := &mockGenome{name: "parent-b", fitness: 0.7}

	child, err := parentA.Crossover(context.Background(), parentB)
	assert.NoError(t, err)
	assert.NotNil(t, child)
}

// ── Concurrent ───────────────────────────────

func TestRegistry_ConcurrentRegister(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	const n = 50
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("genome-%d", i)
			_ = r.Register(&mockGenome{name: name})
		}()
	}
	wg.Wait()
	assert.Len(t, r.List(), n)
}

func TestRegistry_ConcurrentGet(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(&mockGenome{name: "target"}))

	var wg sync.WaitGroup
	const n = 100
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			g, err := r.Get("target")
			assert.NoError(t, err)
			assert.Equal(t, "target", g.Name())
		}()
	}
	wg.Wait()
}
