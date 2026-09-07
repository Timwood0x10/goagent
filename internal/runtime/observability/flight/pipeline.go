package flight

import (
	"sync"
	"time"
)

// PipelineStage represents one stage in the memory evolution pipeline.
type PipelineStage struct {
	Name        string        `json:"name"`
	InputCount  int           `json:"input_count"`
	OutputCount int           `json:"output_count"`
	Duration    time.Duration `json:"duration"`
	Timestamp   time.Time     `json:"timestamp"`
}

// PipelineSummary aggregates the full memory pipeline.
type PipelineSummary struct {
	Stages           []PipelineStage `json:"stages"`
	TotalInput       int             `json:"total_input"`
	TotalOutput      int             `json:"total_output"`
	CompressionRatio float64         `json:"compression_ratio"`
	TotalDuration    time.Duration   `json:"total_duration"`
}

// MemoryPipeline tracks how memory evolves from raw messages to distilled knowledge.
type MemoryPipeline struct {
	sessionID string
	stages    []PipelineStage
	mu        sync.RWMutex
}

// NewMemoryPipeline creates a pipeline for a session.
func NewMemoryPipeline(sessionID string) *MemoryPipeline {
	return &MemoryPipeline{
		sessionID: sessionID,
		stages:    make([]PipelineStage, 0, 8),
	}
}

// SessionID returns the session this pipeline belongs to.
func (p *MemoryPipeline) SessionID() string {
	return p.sessionID
}

// AddStage records a pipeline stage.
func (p *MemoryPipeline) AddStage(stage PipelineStage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stages = append(p.stages, stage)
	// P1-2: ring cap — keep the most recent stages.
	if len(p.stages) > maxPipelineStages {
		p.stages = p.stages[len(p.stages)-maxPipelineStages:]
	}
}

// maxPipelineStages is the ring cap for stages within one pipeline.
const maxPipelineStages = 50

// lastActivity returns the timestamp of the most recent stage, or
// the zero Time when the pipeline has no stages. Used by the Collector's
// LRU eviction (P1-2).
func (p *MemoryPipeline) lastActivity() time.Time {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.stages) == 0 {
		return time.Time{}
	}
	return p.stages[len(p.stages)-1].Timestamp
}

// Stages returns a copy of all stages.
func (p *MemoryPipeline) Stages() []PipelineStage {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]PipelineStage, len(p.stages))
	copy(result, p.stages)
	return result
}

// Summary computes the full pipeline summary.
func (p *MemoryPipeline) Summary() PipelineSummary {
	p.mu.RLock()
	defer p.mu.RUnlock()

	summary := PipelineSummary{
		Stages: make([]PipelineStage, len(p.stages)),
	}
	copy(summary.Stages, p.stages)

	if len(p.stages) > 0 {
		summary.TotalInput = p.stages[0].InputCount
		summary.TotalOutput = p.stages[len(p.stages)-1].OutputCount

		for _, s := range p.stages {
			summary.TotalDuration += s.Duration
		}

		if summary.TotalInput > 0 {
			summary.CompressionRatio = float64(summary.TotalOutput) / float64(summary.TotalInput)
		}
	}

	return summary
}
