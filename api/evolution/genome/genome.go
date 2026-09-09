// Package genome is the DEPRECATED public alias of internal/evoapi/genome
// (M5). New code MUST import internal/evoapi/genome; this package exists
// only for external consumers and is scheduled for removal.
package genome

import (
	"github.com/Timwood0x10/ares/internal/evoapi/genome"
)

// CrossoverType defines the crossover strategy used during reproduction.
type CrossoverType = genome.CrossoverType

const (
	// CrossoverUniform mixes parameter values uniformly.
	CrossoverUniform = genome.CrossoverUniform
	// CrossoverSinglePoint splits parameters at one point.
	CrossoverSinglePoint = genome.CrossoverSinglePoint
	// CrossoverTwoPoint splits parameters at two points.
	CrossoverTwoPoint = genome.CrossoverTwoPoint
	// CrossoverScattered randomly picks each parameter from either parent.
	CrossoverScattered = genome.CrossoverScattered
)

// PromptCrossoverMode controls how prompt templates are inherited during crossover.
type PromptCrossoverMode = genome.PromptCrossoverMode

const (
	// PromptInherit inherits the prompt from the higher-scoring parent.
	PromptInherit = genome.PromptInherit
	// PromptHalfSplit performs half-sentence crossover on prompts.
	PromptHalfSplit = genome.PromptHalfSplit
	// PromptUniform randomly picks either parent's prompt.
	PromptUniform = genome.PromptUniform
)

// Crosser wraps the internal genome crossover engine for public use.
type Crosser = genome.Crosser

// CrosserConfig holds configuration for creating a Crosser.
type CrosserConfig = genome.CrosserConfig

// NewCrosser creates a new public Crosser wrapping the internal crossover engine.
func NewCrosser(cfg CrosserConfig) (*Crosser, error) { return genome.NewCrosser(cfg) }
