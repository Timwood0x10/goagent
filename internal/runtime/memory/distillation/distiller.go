// Package distillation provides memory distillation functionality for agent experience extraction.
package distillation

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	apiembed "github.com/Timwood0x10/ares/internal/embedding"
	areserrors "github.com/Timwood0x10/ares/internal/errors"
	memembed "github.com/Timwood0x10/ares/internal/runtime/memory/embedding"
	truncpkg "github.com/Timwood0x10/ares/internal/truncate"
)

// DistillationConfig holds configuration for the distillation process.
type DistillationConfig struct {
	// MinImportance is the minimum importance score for memories to be kept.
	MinImportance float64

	// ConflictThreshold is the similarity threshold for conflict detection.
	ConflictThreshold float64

	// MaxMemoriesPerDistillation is the maximum number of memories to keep per distillation.
	MaxMemoriesPerDistillation int

	// MaxSolutionsPerTenant is the global cap on solution memories per tenant.
	MaxSolutionsPerTenant int

	// EnableCodeFilter enables code block filtering.
	EnableCodeFilter bool

	// EnableStacktraceFilter enables stacktrace filtering.
	EnableStacktraceFilter bool

	// EnableLogFilter enables log filtering.
	EnableLogFilter bool

	// EnableMarkdownTableFilter enables markdown table filtering.
	EnableMarkdownTableFilter bool

	// EnableCrossTurnExtraction enables cross-turn conversation extraction.
	EnableCrossTurnExtraction bool

	// EnableLengthBonus enables length bonus in importance scoring.
	EnableLengthBonus bool

	// LengthThreshold is the threshold for length bonus.
	LengthThreshold int

	// LengthBonus is the bonus value for length threshold.
	LengthBonus float64

	// TopNBeforeConflict enables top-N filtering before conflict detection.
	TopNBeforeConflict bool

	// ConflictSearchLimit is the limit for vector search in conflict detection.
	ConflictSearchLimit int

	// PrecisionOverRecall prioritizes precision over recall.
	PrecisionOverRecall bool

	// DistillationThreshold is the number of conversation rounds that accumulate
	// before distillation fires in the event subscription path. A value of 0
	// disables round gating: every EventMessageAdded triggers distillation.
	// Mirrors v0.2.4 examples/knowledge-base config.yaml distillation_threshold.
	DistillationThreshold int
}

// DefaultDistillationConfig returns the default configuration for distillation.
func DefaultDistillationConfig() *DistillationConfig {
	return &DistillationConfig{
		MinImportance:              0.6,
		ConflictThreshold:          0.85,
		MaxMemoriesPerDistillation: 3,
		MaxSolutionsPerTenant:      5000,
		EnableCodeFilter:           true,
		EnableStacktraceFilter:     true,
		EnableLogFilter:            true,
		EnableMarkdownTableFilter:  true,
		EnableCrossTurnExtraction:  true,
		EnableLengthBonus:          true,
		LengthThreshold:            60,
		LengthBonus:                0.1,
		TopNBeforeConflict:         true,
		ConflictSearchLimit:        5,
		PrecisionOverRecall:        true,
		// DistillationThreshold 0 means event-driven ungated firing
		// (preserves existing behaviour when no threshold is configured).
		DistillationThreshold: 0,
	}
}

// DistillationMetrics holds metrics for the distillation process.
type DistillationMetrics struct {
	AttemptTotal     int64
	SuccessTotal     int64
	FilteredNoise    int64
	FilteredSecurity int64
	ConflictResolved int64
	MemoriesCreated  int64
}

// atomicMetrics holds atomic counters for metrics.
type atomicMetrics struct {
	AttemptTotal     atomic.Int64
	SuccessTotal     atomic.Int64
	FilteredNoise    atomic.Int64
	FilteredSecurity atomic.Int64
	ConflictResolved atomic.Int64
	MemoriesCreated  atomic.Int64
}

// String returns a string representation of the atomic metrics.
func (a *atomicMetrics) String() string {
	return fmt.Sprintf("attempts=%d,success=%d,filtered_noise=%d,filtered_security=%d,conflicts_resolved=%d,memories_created=%d",
		a.AttemptTotal.Load(), a.SuccessTotal.Load(), a.FilteredNoise.Load(), a.FilteredSecurity.Load(), a.ConflictResolved.Load(), a.MemoriesCreated.Load())
}

// String returns a string representation of the metrics.
func (m *DistillationMetrics) String() string {
	return fmt.Sprintf("attempts=%d,success=%d,filtered_noise=%d,filtered_security=%d,conflicts_resolved=%d,memories_created=%d",
		m.AttemptTotal, m.SuccessTotal, m.FilteredNoise, m.FilteredSecurity, m.ConflictResolved, m.MemoriesCreated)
}

// Distiller is the unified distillation engine that orchestrates all components.
type Distiller struct {
	config      *DistillationConfig
	configMu    sync.RWMutex
	extractor   *ExperienceExtractor
	classifier  *MemoryClassifier
	scorer      *ImportanceScorer
	resolver    *ConflictResolver
	noiseFilter *NoiseFilter
	embedder    apiembed.EmbeddingService
	pipeline    memembed.EmbeddingPipeline
	repo        ExperienceRepository
	expStore    ExperienceStore // Optional: writes distilled memories to experience store
	metrics     atomicMetrics   // Thread-safe atomic counters
	distillEg   *errgroup.Group // Manages the event-subscription goroutine (lifecycle via ctx)

	// OnTaskCompleted is called when a task completion event is received.
	// If set, the distiller invokes it with the task ID from the event payload.
	// The handler should trigger the full distillation pipeline for the task.
	OnTaskCompleted func(ctx context.Context, taskID string)

	// OnMessageAdded is called when a message-added event passes the round gate
	// (i.e. after DistillationThreshold gating in SubscribeAndDistill, or
	// immediately when no threshold is configured). If set, the distiller
	// invokes it with the stream ID and role from the event payload. This is
	// the observable hook for the round-gate behaviour; without it, message
	// events only produce a debug log.
	OnMessageAdded func(ctx context.Context, streamID, role string)
}

// NewDistiller creates a new Distiller instance.
//
// Args:
//
//	config - distillation configuration.
//	embedder - embedding service for generating vectors.
//	repo - experience repository for storage and retrieval.
//
// Returns:
//
//	*Distiller - configured distiller instance.
func NewDistiller(config *DistillationConfig, embedder apiembed.EmbeddingService, repo ExperienceRepository) *Distiller {
	if config == nil {
		config = DefaultDistillationConfig()
	}

	// Create noise filter with configuration
	noiseFilterConfig := &NoiseFilterConfig{
		EnableCodeFilter:          config.EnableCodeFilter,
		EnableStacktraceFilter:    config.EnableStacktraceFilter,
		EnableLogFilter:           config.EnableLogFilter,
		EnableMarkdownTableFilter: config.EnableMarkdownTableFilter,
	}

	return &Distiller{
		config:      config,
		extractor:   NewExperienceExtractorWithConfig(config.EnableCrossTurnExtraction),
		classifier:  NewMemoryClassifier(),
		scorer:      NewImportanceScorerWithConfig(config.MinImportance, config.EnableLengthBonus),
		resolver:    NewConflictResolverWithConfig(repo, config.ConflictThreshold, config.ConflictSearchLimit),
		noiseFilter: NewNoiseFilterWithConfig(noiseFilterConfig),
		embedder:    embedder,
		repo:        repo,
		metrics:     atomicMetrics{},
		distillEg:   &errgroup.Group{},
	}
}

// SetEmbeddingPipeline configures the unified embedding pipeline for conflict detection.
// When set, DistillConversation uses the pipeline with canonical spec builders
// instead of calling the raw embedder directly.
func (d *Distiller) SetEmbeddingPipeline(pipeline memembed.EmbeddingPipeline) {
	d.pipeline = pipeline
}

// UpdateConfig atomically replaces the distiller's configuration.
// This allows runtime reconfiguration of thresholds (MinImportance, MaxMemories, etc.)
// without recreating the distiller.
func (d *Distiller) UpdateConfig(config *DistillationConfig) {
	if config == nil {
		return
	}
	d.configMu.Lock()
	defer d.configMu.Unlock()
	d.config = config
}

// getConfig returns the current configuration in a thread-safe manner.
func (d *Distiller) getConfig() *DistillationConfig {
	d.configMu.RLock()
	defer d.configMu.RUnlock()
	return d.config
}

// WithExperienceStore configures the optional experience store for syncing memories.
// When set, DistillConversation will also write distilled memories as experiences.
//
// Args:
//
//	store - the experience store implementation. May be nil to disable syncing.
func (d *Distiller) WithExperienceStore(store ExperienceStore) {
	d.expStore = store
}

// memWithEmbedding holds a memory with its embedding validity status.
type memWithEmbedding struct {
	mem   Memory
	valid bool
}

// extractPhase extracts raw experiences from conversation messages.
//
// Args:
//
//	ctx - operation context.
//	conversationID - unique identifier for the conversation.
//	messages - conversation messages.
//
// Returns:
//
//	[]Experience - extracted experiences.
//	error - any error encountered.
func (d *Distiller) extractPhase(ctx context.Context, conversationID string, messages []Message) ([]Experience, error) {
	log.DebugContext(ctx, "[Memory Distillation] Extracting experiences from conversation",
		"conversation_id", conversationID)

	experiences := d.extractor.ExtractExperiences(messages)
	if len(experiences) == 0 {
		log.InfoContext(ctx, "[Memory Distillation] WARNING No experiences extracted from conversation",
			"conversation_id", conversationID,
			"reason", "filtered as noise")
		d.metrics.FilteredNoise.Add(1)
		return []Experience{}, nil
	}

	log.InfoContext(ctx, "[Memory Distillation] Experiences extracted",
		"conversation_id", conversationID,
		"experience_count", len(experiences))

	return experiences, nil
}

// classifyAndScorePhase classifies experiences, applies filters, scores importance,
// and creates memory candidates.
//
// Args:
//
//	ctx - operation context.
//	conversationID - unique identifier for the conversation.
//	messages - original conversation messages for evidence extraction.
//	experiences - raw experiences to process.
//	tenantID - tenant ID for multi-tenancy.
//	userID - user ID for the conversation.
//
// Returns:
//
//	[]Memory - classified and scored memory candidates.
func (d *Distiller) classifyAndScorePhase(ctx context.Context, conversationID string, messages []Message, experiences []Experience, tenantID, userID string) []Memory {
	log.DebugContext(ctx, "[Memory Distillation] Classifying and scoring experiences",
		"conversation_id", conversationID)

	var memories []Memory
	for idx, exp := range experiences {
		// Security filter (always apply).
		if !SecurityFilter(exp.Problem) || !SecurityFilter(exp.Solution) {
			log.DebugContext(ctx, "[Memory Distillation] Experience filtered by security filter",
				"conversation_id", conversationID,
				"experience_index", idx,
				"reason", "security violation")
			d.metrics.FilteredSecurity.Add(1)
			continue
		}

		// Classify memory type FIRST (before noise filtering).
		memoryType := d.classifier.ClassifyMemory(&exp)

		// Noise filter: skip for user profiles, apply for others.
		if memoryType != MemoryProfile && d.noiseFilter.IsNoise(exp.Solution) {
			log.DebugContext(ctx, "[Memory Distillation] Experience filtered as noise",
				"conversation_id", conversationID,
				"experience_index", idx,
				"memory_type", memoryType.String(),
				"reason", "content noise")
			d.metrics.FilteredNoise.Add(1)
			continue
		}

		// Score importance.
		problem := exp.Problem
		solution := exp.Solution
		score := d.scorer.ScoreMemory(memoryType, problem, solution)
		exp.Confidence = score

		// Skip low importance memories.
		if !d.scorer.ShouldKeep(score) {
			log.DebugContext(ctx, "[Memory Distillation] Experience filtered by importance score",
				"conversation_id", conversationID,
				"experience_index", idx,
				"memory_type", memoryType.String(),
				"score", score,
				"threshold", d.getConfig().MinImportance,
				"reason", "below importance threshold")
			d.metrics.FilteredNoise.Add(1)
			continue
		}

		// Create memory with UUID.
		evidence := extractEvidenceFromMessages(messages, extractTurnID(messages, problem))
		memory := Memory{
			ID:         uuid.New().String(),
			Type:       memoryType,
			Content:    FormatExperience(&exp),
			Importance: score,
			Source:     conversationID,
			CreatedAt:  time.Now(),
			Metadata: map[string]interface{}{
				"memory_type":       memoryType.String(),
				"conversation_id":   conversationID,
				"source":            "distillation",
				"confidence":        exp.Confidence,
				"extraction_method": string(exp.ExtractionMethod),
				"problem":           problem,
				"solution":          solution,
				"evidence":          evidence,
				"tenant_id":         tenantID,
				"user_id":           userID,
			},
		}

		log.DebugContext(ctx, "[Memory Distillation] Memory candidate created",
			"conversation_id", conversationID,
			"experience_index", idx,
			"memory_type", memoryType.String(),
			"importance_score", score,
			"content_preview", truncpkg.WithEllipsis(memory.Content, 50))

		memories = append(memories, memory)
	}

	return memories
}

// topNBeforeConflictPhase applies Top-N filtering before conflict detection
// to improve performance when there are many candidates.
//
// Args:
//
//	ctx - operation context.
//	conversationID - unique identifier for the conversation.
//	memories - candidate memories to filter.
//
// Returns:
//
//	[]Memory - filtered memories.
func (d *Distiller) topNBeforeConflictPhase(ctx context.Context, conversationID string, memories []Memory) []Memory {
	config := d.getConfig()
	if !config.TopNBeforeConflict || len(memories) <= config.MaxMemoriesPerDistillation {
		return memories
	}

	// Filter by minimum importance and sort by importance (descending), then
	// take the top N. This replicates ImportanceScorer.TopNFilter behavior
	// directly on memories to avoid a stale-data bug: the previous code used
	// memories[:len(filtered)] which selected the first N memories instead of
	// the highest-scoring N.
	filtered := make([]Memory, 0, len(memories))
	for _, mem := range memories {
		if mem.Importance >= config.MinImportance {
			filtered = append(filtered, mem)
		}
	}

	if len(filtered) == 0 {
		return filtered
	}

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Importance > filtered[j].Importance
	})

	if len(filtered) > config.MaxMemoriesPerDistillation {
		filtered = filtered[:config.MaxMemoriesPerDistillation]
	}

	return filtered
}

// embedPhase generates embeddings for all memories concurrently using errgroup.
//
// Args:
//
//	ctx - operation context.
//	conversationID - unique identifier for the conversation.
//	memories - memories to embed.
//
// Returns:
//
//	[]memWithEmbedding - embedded memories with validity flags.
func (d *Distiller) embedPhase(ctx context.Context, conversationID string, memories []Memory) []memWithEmbedding {
	embedded := make([]memWithEmbedding, len(memories))
	g, embedCtx := errgroup.WithContext(ctx)
	g.SetLimit(5)

	for idx := range memories {
		idx := idx
		g.Go(func() error {
			memory := memories[idx]
			var embedding []float64
			var err error
			var spec memembed.EmbeddingSpec
			var embeddingText string
			if d.pipeline != nil {
				problem, _ := memory.Metadata["problem"].(string)
				solution, _ := memory.Metadata["solution"].(string)
				// Assign to the outer spec so the retry path below can reuse it.
				// Using := here would shadow spec and leave the retry with a zero value.
				var specErr error
				spec, specErr = d.pipeline.BuildSpec(memembed.KindMemoryExperience, memembed.MemoryExperienceInput{
					MemoryType: memory.Type.String(),
					Problem:    problem,
					Solution:   solution,
				})
				if specErr != nil {
					log.WarnContext(embedCtx, "[Memory Distillation] Failed to build embedding spec",
						"conversation_id", conversationID, "memory_index", idx, "error", specErr)
					embedded[idx] = memWithEmbedding{valid: false}
					return nil
				}
				embedding, err = d.pipeline.Embed(embedCtx, spec)
			} else {
				// Assign to the outer embeddingText so the retry path below can reuse it.
				// Using := here would shadow embeddingText and leave the retry with empty text.
				embeddingText = fmt.Sprintf("%s → %s", memory.Metadata["problem"], memory.Metadata["solution"])
				embedding, err = d.embedder.EmbedWithPrefix(embedCtx, embeddingText, "memory:")
			}
			if err != nil {
				// Retry once after a short delay to handle transient embedder failures.
				log.WarnContext(embedCtx, "[Memory Distillation] Initial embedding failed, retrying once",
					"conversation_id", conversationID, "memory_index", idx, "error", err)
				select {
				case <-time.After(100 * time.Millisecond):
				case <-embedCtx.Done():
					embedded[idx] = memWithEmbedding{valid: false}
					return nil
				}
				if d.pipeline != nil {
					embedding, err = d.pipeline.Embed(embedCtx, spec)
				} else {
					embedding, err = d.embedder.EmbedWithPrefix(embedCtx, embeddingText, "memory:")
				}
				if err != nil {
					log.WarnContext(embedCtx, "[Memory Distillation] Retry embedding also failed, discarding memory",
						"conversation_id", conversationID, "memory_index", idx, "error", err)
					embedded[idx] = memWithEmbedding{valid: false}
					return nil
				}
			}
			memory.Vector = embedding
			embedded[idx] = memWithEmbedding{mem: memory, valid: true}
			log.InfoContext(embedCtx, "[Memory Distillation] Embedding generated",
				"conversation_id", conversationID, "memory_index", idx,
				"memory_type", memory.Type.String(), "dimensions", len(embedding))
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		log.ErrorContext(ctx, "[Memory Distillation] Embedding phase failed", "error", err)
	}

	return embedded
}

// embedOneMemory generates an embedding for a single Memory.
// Returns the embedding vector or an error. The caller should fall back to
// an existing vector if this fails (embedding is a best-effort augmentation).
func (d *Distiller) embedOneMemory(ctx context.Context, memory *Memory) ([]float64, error) {
	if d.pipeline != nil {
		problem, _ := memory.Metadata["problem"].(string)
		solution, _ := memory.Metadata["solution"].(string)
		spec, err := d.pipeline.BuildSpec(memembed.KindMemoryExperience, memembed.MemoryExperienceInput{
			MemoryType: memory.Type.String(),
			Problem:    problem,
			Solution:   solution,
		})
		if err != nil {
			return nil, fmt.Errorf("build spec: %w", err)
		}
		return d.pipeline.Embed(ctx, spec)
	}
	embeddingText := fmt.Sprintf("%s → %s", memory.Metadata["problem"], memory.Metadata["solution"])
	return d.embedder.EmbedWithPrefix(ctx, embeddingText, "memory:")
}

// resolveConflictsPhase detects and resolves conflicts among embedded memories.
//
// Args:
//
//	ctx - operation context.
//	conversationID - unique identifier for the conversation.
//	tenantID - tenant ID for multi-tenancy.
//	embedded - embedded memories with validity flags.
//
// Returns:
//
//	[]Memory - resolved memories after conflict handling.
func (d *Distiller) resolveConflictsPhase(ctx context.Context, conversationID, tenantID string, embedded []memWithEmbedding) []Memory {
	var finalMemories []Memory

	for idx, ew := range embedded {
		if !ew.valid {
			continue
		}
		memory := ew.mem

		problem, problemOk := memory.Metadata["problem"].(string)
		if !problemOk {
			problem = ""
		}
		solution, solutionOk := memory.Metadata["solution"].(string)
		if !solutionOk {
			solution = ""
		}

		exp := &Experience{
			Type:       memory.Type,
			Problem:    problem,
			Solution:   solution,
			Confidence: memory.Importance,
		}

		conflict, err := d.resolver.DetectConflict(ctx, memory.Vector, tenantID)
		if err != nil && !errors.Is(err, ErrNoConflict) {
			log.WarnContext(ctx, "[Memory Distillation] Failed to detect conflicts",
				"conversation_id", conversationID, "memory_index", idx, "error", err)
		}
		if conflict != nil {
			strategy := d.resolver.ResolveConflict(exp, conflict)
			log.InfoContext(ctx, "[Memory Distillation] Conflict resolved",
				"conversation_id", conversationID, "memory_index", idx,
				"strategy", string(strategy))
			d.metrics.ConflictResolved.Add(1)

			switch strategy {
			case ReplaceOld:
				finalMemories = append(finalMemories, memory)
			case KeepOld:
				// Drop the incoming near-duplicate: the stored memory has
				// higher confidence and remains in the repository untouched.
				log.InfoContext(ctx, "[Memory Distillation] Discarding lower-confidence duplicate",
					"conversation_id", conversationID, "memory_index", idx,
					"new_confidence", memory.Importance, "existing_confidence", conflict.Confidence)
			case KeepBoth:
				// Preserve the full structured metadata the downstream
				// StoreDistilledTask expects (problem/solution/confidence). The
				// previous map only carried "solution", so the persisted experience
				// for the kept-old memory lost its problem statement and confidence
				// (both stored as empty/zero), and its re-embedding text was
				// garbage.
				oldMemory := Memory{
					ID:      uuid.New().String(),
					Content: conflict.Problem,
					Metadata: map[string]interface{}{
						"problem":           conflict.Problem,
						"solution":          conflict.Solution,
						"confidence":        conflict.Confidence,
						"extraction_method": string(conflict.ExtractionMethod),
					},
					Type:       memory.Type,
					Importance: conflict.Confidence,
					Vector:     conflict.Vector, // fallback if re-embed fails
					CreatedAt:  time.Now(),
				}
				// Re-embed oldMemory with a fresh vector so it can participate
				// in subsequent conflict detection properly, rather than relying
				// on a potentially stale vector from a prior conflicting memory.
				if vec, err := d.embedOneMemory(ctx, &oldMemory); err == nil {
					oldMemory.Vector = vec
				} else {
					log.WarnContext(ctx, "[Memory Distillation] KeepBoth re-embed failed, using existing vector",
						"conversation_id", conversationID, "memory_index", idx, "error", err)
				}
				finalMemories = append(finalMemories, oldMemory)
				finalMemories = append(finalMemories, memory)
			default:
				finalMemories = append(finalMemories, memory)
				log.WarnContext(ctx, "[Memory Distillation] WARNING Unknown resolution strategy, defaulting to keep new memory",
					"conversation_id", conversationID,
					"memory_index", idx,
					"strategy", string(strategy))
			}
		} else {
			finalMemories = append(finalMemories, memory)
		}
	}

	return finalMemories
}

// finalTopNPhase applies final Top-N filtering after conflict resolution.
//
// Args:
//
//	memories - resolved memories to filter.
//
// Returns:
//
//	[]Memory - filtered memories.
func (d *Distiller) finalTopNPhase(memories []Memory) []Memory {
	maxMemories := d.getConfig().MaxMemoriesPerDistillation
	if len(memories) <= maxMemories {
		return memories
	}

	sort.Slice(memories, func(i, j int) bool {
		return memories[i].Importance > memories[j].Importance
	})
	return memories[:maxMemories]
}

// DistillConversation distills memories from a conversation.
// This is the main entry point for the distillation process.
// It orchestrates the full pipeline: extraction, classification, scoring,
// embedding, conflict resolution, and optional syncing to experience store.
//
// Args:
//
//	ctx - operation context.
//	conversationID - unique identifier for the conversation.
//	messages - conversation messages.
//	tenantID - tenant ID for multi-tenancy.
//	userID - user ID for the conversation.
//
// Returns:
//
//	[]Memory - distilled memories.
//	error - any error encountered.
func (d *Distiller) DistillConversation(ctx context.Context, conversationID string, messages []Message, tenantID, userID string) ([]Memory, error) {
	startTime := time.Now()
	log.InfoContext(ctx, "[Memory Distillation] Starting distillation process",
		"conversation_id", conversationID,
		"tenant_id", tenantID,
		"user_id", userID,
		"message_count", len(messages),
		"timestamp", startTime.Format(time.RFC3339))

	d.metrics.AttemptTotal.Add(1)

	if ctx.Err() != nil {
		log.ErrorContext(ctx, "[Memory Distillation] ERROR Context cancelled",
			"conversation_id", conversationID,
			"error", ctx.Err())
		return nil, fmt.Errorf("context cancelled: %w", ctx.Err())
	}

	// Phase 1: Extract experiences.
	experiences, err := d.extractPhase(ctx, conversationID, messages)
	if err != nil {
		return nil, err
	}

	// Phase 2: Classify and score experiences into memory candidates.
	memories := d.classifyAndScorePhase(ctx, conversationID, messages, experiences, tenantID, userID)
	if len(memories) == 0 {
		log.InfoContext(ctx, "[Memory Distillation] WARNING No memories passed all filters",
			"conversation_id", conversationID,
			"initial_experiences", len(experiences))
		return []Memory{}, nil
	}

	log.InfoContext(ctx, "[Memory Distillation] Memory candidates created",
		"conversation_id", conversationID,
		"candidate_count", len(memories),
		"filtered_count", len(experiences)-len(memories))

	// Phase 3: Top-N filtering before conflict detection.
	memories = d.topNBeforeConflictPhase(ctx, conversationID, memories)

	// Phase 4: Generate embeddings concurrently.
	log.InfoContext(ctx, "[Memory Distillation] Generating embeddings and detecting conflicts",
		"conversation_id", conversationID,
		"memory_count", len(memories))

	embedded := d.embedPhase(ctx, conversationID, memories)

	// Phase 5: Conflict detection and resolution.
	finalMemories := d.resolveConflictsPhase(ctx, conversationID, tenantID, embedded)

	// Phase 6: Final Top-N filtering.
	finalMemories = d.finalTopNPhase(finalMemories)

	// Phase 7: Enforce solution cap.
	log.DebugContext(ctx, "[Memory Distillation] Enforcing solution cap",
		"conversation_id", conversationID,
		"tenant_id", tenantID,
		"current_memories", len(finalMemories))

	if err := d.enforceSolutionCap(ctx, tenantID); err != nil {
		log.WarnContext(ctx, "[Memory Distillation] WARNING Failed to enforce solution cap",
			"tenant_id", tenantID,
			"error", err.Error())
	}

	d.metrics.SuccessTotal.Add(1)
	d.metrics.MemoriesCreated.Add(int64(len(finalMemories)))

	// Phase 8: Sync to experience store if configured.
	if d.expStore != nil {
		if err := d.syncToExperienceStore(ctx, finalMemories, tenantID); err != nil {
			log.WarnContext(ctx, "[Memory Distillation] Failed to sync to experience store",
				"conversation_id", conversationID,
				"error", err)
		}
	}

	log.InfoContext(ctx, "[Memory Distillation] Distillation completed successfully",
		"conversation_id", conversationID,
		"tenant_id", tenantID,
		"user_id", userID,
		"final_memory_count", len(finalMemories),
		"importance_scores", formatImportanceScores(finalMemories),
		"memory_types", formatMemoryTypes(finalMemories),
		"metrics", d.metrics.String(),
		"duration_ms", time.Since(startTime).Milliseconds())

	return finalMemories, nil
}

// enforceSolutionCap enforces the global cap on knowledge (solution) memories per tenant.
// Solution memories are stored with type MemoryKnowledge. If the count exceeds
// MaxSolutionsPerTenant, the lowest-importance memories are pruned.
//
// Args:
//
//	ctx - operation context.
//	tenantID - tenant ID for multi-tenancy.
//
// Returns:
//
//	error - any error encountered.
func (d *Distiller) enforceSolutionCap(ctx context.Context, tenantID string) error {
	if d.repo == nil {
		return nil
	}

	config := d.getConfig()

	// Count first to avoid loading all solutions when under cap.
	count, err := d.repo.CountByMemoryType(ctx, tenantID, MemoryKnowledge)
	if err != nil {
		return areserrors.Wrap(err, "failed to get solution count")
	}

	if count <= config.MaxSolutionsPerTenant {
		return nil
	}

	// Over cap: load only the excess lowest-confidence solutions.
	solutions, err := d.repo.GetByMemoryType(ctx, tenantID, MemoryKnowledge)
	if err != nil {
		return areserrors.Wrap(err, "failed to get solutions for pruning")
	}

	// Sort by confidence ascending and delete the lowest ones.
	sort.Slice(solutions, func(i, j int) bool {
		return solutions[i].Confidence < solutions[j].Confidence
	})

	deleteCount := count - config.MaxSolutionsPerTenant
	if deleteCount > len(solutions) {
		deleteCount = len(solutions)
	}

	ids := make([]string, deleteCount)
	for i := 0; i < deleteCount; i++ {
		ids[i] = solutions[i].ID
	}

	log.WarnContext(ctx, "solution count exceeds cap, pruning lowest importance memories",
		"tenant_id", tenantID,
		"current_count", count,
		"max_count", config.MaxSolutionsPerTenant,
		"delete_count", deleteCount,
	)

	if err := d.repo.DeleteBatch(ctx, ids); err != nil {
		// Fall back to individual deletes on batch failure.
		for i, id := range ids {
			if err := d.repo.Delete(ctx, id); err != nil {
				log.WarnContext(ctx, "failed to delete solution during pruning",
					"problem", solutions[i].Problem,
					"error", err)
			}
		}
	}

	return nil
}

// GetMetrics returns the current distillation metrics.
//
// Thread-safety: Uses atomic operations to safely read metrics.
//
// Returns:
//
//	*DistillationMetrics - the metrics.
