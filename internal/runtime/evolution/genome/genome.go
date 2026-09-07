// Package genome provides the plugin interface for evolvable runtime components.
//
// Each subsystem (workflow, scheduler, planner, knowledge, recovery) implements
// the Genome interface to participate in runtime evolution.
//
// IMPORTANT: Genome does NOT know about RuntimePatch.
// The boundary is:
//
//	Genome → Generate Candidate → Diff Engine → RuntimePatch
//
// Genome's only responsibilities are Mutation, Snapshot, and optional Fitness.
// Deployment is handled by Diff Engine + Coordinator.
//
// NOTE: Crossover and Fitness were removed from the core interface in 2026-07
// because they had zero production callers. They are available as optional
// interfaces: CrossoverGenome, FitnessGenome. Use type assertions to check:
//
//	if f, ok := child.(FitnessGenome); ok { score, err := f.Fitness(ctx) }
package genome

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Genome is the plugin interface for evolvable runtime components.
// The evolution pipeline operates on Genome instances without knowing what
// subsystem they represent — this enables pluggable evolution.
//
// Required methods: Name, Mutate, Snapshot.
// Optional capabilities: CrossoverGenome, FitnessGenome.
type Genome interface {
	// Name returns the genome identifier, used for registry lookup.
	Name() string

	// Mutate generates n candidate genomes from this parent.
	// Each candidate represents a possible runtime configuration.
	Mutate(ctx context.Context, n int) ([]Genome, error)

	// Snapshot returns a serializable representation of this genome's
	// current state. Used by Diff Engine to compute changes.
	Snapshot(ctx context.Context) (any, error)
}

// FitnessGenome is an optional extension for genomes that support fitness evaluation.
// Genomes that implement this interface can be scored by the coordinator.
type FitnessGenome interface {
	// Fitness evaluates this genome's quality in the current runtime context.
	// Higher scores indicate better configurations. Range is [0, 1].
	Fitness(ctx context.Context) (float64, error)
}

// CrossoverGenome is an optional extension for genomes that support recombination.
// Genomes that implement this interface can be crossed with another genome
// of the same type to produce a child genome.
type CrossoverGenome interface {
	// Crossover recombines this genome with another to produce a child.
	// Returns an error if the other genome is incompatible.
	Crossover(ctx context.Context, other Genome) (Genome, error)
}

// Registry manages pluggable genome implementations.
type Registry struct {
	mu      sync.RWMutex
	genomes map[string]Genome
}

// NewRegistry creates a new genome registry.
func NewRegistry() *Registry {
	return &Registry{
		genomes: make(map[string]Genome),
	}
}

// Register adds a genome to the registry. Returns an error if a genome
// with the same name is already registered.
func (r *Registry) Register(g Genome) error {
	if g == nil {
		return errors.New("genome: cannot register nil")
	}
	name := g.Name()
	if name == "" {
		return errors.New("genome: name must not be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.genomes[name]; exists {
		return fmt.Errorf("genome: %q already registered", name)
	}
	r.genomes[name] = g
	return nil
}

// Get returns the genome with the given name.
// Returns an error if not found.
func (r *Registry) Get(name string) (Genome, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	g, ok := r.genomes[name]
	if !ok {
		return nil, fmt.Errorf("genome: %q not found", name)
	}
	return g, nil
}

// List returns the names of all registered genomes.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.genomes))
	for name := range r.genomes {
		names = append(names, name)
	}
	return names
}

// Unregister removes a genome from the registry. Returns an error if not found.
func (r *Registry) Unregister(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.genomes[name]; !exists {
		return fmt.Errorf("genome: %q not found", name)
	}
	delete(r.genomes, name)
	return nil
}
