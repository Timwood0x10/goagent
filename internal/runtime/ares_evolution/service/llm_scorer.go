package evolution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
)

const (
	defaultLLMTemperature = 0.3
	defaultLLMMaxTokens   = 256
)

// Default deterministic scorer constants.
const (
	// deterministicBaseScore is the starting score before parameter adjustments.
	deterministicBaseScore = 50.0

	// deterministicMaxScore is the upper clamp limit for the deterministic scorer.
	deterministicMaxScore = 100.0

	// deterministicMinScore is the lower clamp limit for the deterministic scorer.
	deterministicMinScore = 5.0

	// deterministicPromptReward is the bonus for "precise" prompt template.
	deterministicPromptReward = 15.0

	// deterministicCarefulReward is the bonus for "careful" prompt template.
	deterministicCarefulReward = 8.0

	// deterministicCreativeReward is the bonus for "creative" prompt template.
	deterministicCreativeReward = 4.0
)

// getParamFloat extracts a float64 from a params map, handling multiple numeric types.
// Returns (value, true) if the key exists and can be converted, (0, false) otherwise.
// Supports float64, int, int64, int32, float32, and json.Number types.
//
// Args:
//
//	params - the strategy parameters map.
//	key - the parameter key to look up.
//
// Returns:
//
//	float64 - the parameter value as float64.
//	bool - true if the parameter was found and converted successfully.
func getParamFloat(params map[string]any, key string) (float64, bool) {
	if params == nil {
		return 0, false
	}
	val, ok := params[key]
	if !ok {
		return 0, false
	}

	switch v := val.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case int32:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

// DeterministicScore computes a parameter-aware fitness score for a strategy.
// This is a pure heuristic (no random noise / no LLM call) used as fallback
// scorer when no custom LLM scorer is configured. The scoring formula:
//
//   - Base score: 50.0
//   - temperature: lower is better → (1.0 - temp) * 25  (range [0, +25])
//   - top_k near 30 is optimal → penalty = dist²/10 where dist = top_k - 30
//   - prompt template: "precise" +15, "careful" +8, "creative" +4
//   - Final score clamped to [deterministicMinScore, deterministicMaxScore]
//
// Args:
//
//	s - the strategy to score (must not be nil).
//
// Returns:
//
//	float64 - fitness score in [deterministicMinScore, deterministicMaxScore].
func DeterministicScore(s *Strategy) float64 {
	if s == nil {
		return deterministicBaseScore
	}

	score := deterministicBaseScore

	// Temperature: lower is better (0.0 -> +25, 1.0 -> +0).
	// Handle multiple numeric types since mutation may store values as int/int64.
	temp, tempFound := getParamFloat(s.Params, "temperature")
	if tempFound {
		score += (1.0 - temp) * 25
	}

	// Top_k: optimal near 30. Penalty is quadratic distance from optimum.
	// Handle multiple numeric types since mutation may store values as int/int64.
	topK, topKFound := getParamFloat(s.Params, "top_k")
	if topKFound {
		dist := topK - 30.0
		score -= (dist * dist) / 10.0
	}

	// Prompt template bonus.
	promptVal := ""
	if pt, ok := s.Params["prompt_template"].(string); ok {
		promptVal = pt
	} else if pt, ok := s.Params["PromptTemplate"].(string); ok {
		promptVal = pt
	} else if s.PromptTemplate != "" {
		promptVal = s.PromptTemplate
	}
	switch promptVal {
	case "precise":
		score += deterministicPromptReward
	case "careful":
		score += deterministicCarefulReward
	case "creative":
		score += deterministicCreativeReward
	}

	// Clamp to valid range.
	if score > deterministicMaxScore {
		score = deterministicMaxScore
	}
	if score < deterministicMinScore {
		score = deterministicMinScore
	}
	return score
}

// LLMScorerConfig configures an LLMScorer instance.
type LLMScorerConfig struct {
	// Client is the LLM client used for scoring (must not be nil).
	Client LLMClient

	// EvalPrompt is the evaluation prompt template.
	// The strategy params and prompt_template are interpolated.
	// If empty, a default prompt is used.
	EvalPrompt string

	// Model is the LLM model name (for logging/metrics).
	Model string

	// Temperature for the LLM evaluation call.
	// When 0, defaults to 0.3. When Seed != 0, forced to 0 for deterministic output.
	Temperature float64

	// MaxTokens for the LLM response.
	MaxTokens int

	// Seed enables deterministic LLM scoring when > 0.
	// Forces Temperature to 0 and embeds the seed in the evaluation prompt
	// so identical strategies always receive the same score.
	Seed int64

	// NumSamples sets how many times the LLM is called per strategy.
	// When > 1, the best score across all samples is returned (max-of-N),
	// hedging against transient API errors. Default 1 (single call).
	NumSamples int

	// Fallback is called when the LLM API is unreachable (all samples fail).
	// When set, the evolution keeps running with deterministic scoring instead
	// of assigning zeros that would collapse the population.
	// Example: pass a parameter-aware ScorerFunc as the circuit breaker.
	Fallback ScorerFunc
}

// DefaultEvalPrompt is the default evaluation prompt template.
// {strategy_json} is replaced with the strategy's JSON representation.
const DefaultEvalPrompt = `You are evaluating an AI agent strategy. The strategy defines how an agent:
- Generates responses (temperature controls creativity vs determinism)
- Selects knowledge (top_k controls focus breadth)
- Structures its prompts (prompt_template sets behavior style)

Score this strategy on a scale of 0 to 100 based on:
- Reasoning quality: Does the temperature setting allow coherent reasoning?
- Focus accuracy: Does top_k balance breadth vs precision?
- Instruction following: Does the prompt template guide appropriate behavior?
- General capability: Would this strategy perform well on diverse tasks?

Strategy configuration:
{strategy_json}

Respond with ONLY a JSON object containing:
{"score": <0-100>, "reasoning": "<brief explanation>", "focus": "<brief explanation>", "instruction": "<brief explanation>"}`

// LLMScorer evaluates strategies by calling an LLM.
// It serializes the strategy config, sends it to the LLM,
// and extracts a score from the structured response.
type LLMScorer struct {
	client      LLMClient
	evalPrompt  string
	model       string
	temperature float64
	maxTokens   int
	seed        int64
	numSamples  int
	fallback    ScorerFunc
}

// NewLLMScorer creates an LLMScorer from config.
func NewLLMScorer(cfg LLMScorerConfig) (*LLMScorer, error) {
	if cfg.Client == nil {
		return nil, errors.New("LLM client must not be nil")
	}
	evalPrompt := cfg.EvalPrompt
	if evalPrompt == "" {
		evalPrompt = DefaultEvalPrompt
	}
	temp := cfg.Temperature
	forcedDeterministic := false
	if cfg.Seed != 0 {
		temp = 0 // seed requires deterministic output
		forcedDeterministic = true
	}
	if temp == 0 && !forcedDeterministic {
		temp = defaultLLMTemperature
	}
	maxTokens := cfg.MaxTokens
	if maxTokens == 0 {
		maxTokens = defaultLLMMaxTokens
	}
	numSamples := cfg.NumSamples
	if numSamples < 1 {
		numSamples = 1
	}
	return &LLMScorer{
		client:      cfg.Client,
		evalPrompt:  evalPrompt,
		model:       cfg.Model,
		temperature: temp,
		maxTokens:   maxTokens,
		seed:        cfg.Seed,
		numSamples:  numSamples,
		fallback:    cfg.Fallback,
	}, nil
}

// Score implements scorer for population evaluation.
// It builds a prompt from the strategy, calls the LLM, and parses the score.
// When numSamples > 1, the LLM is called multiple times and the best score
// is returned (max-of-N), providing robustness against transient API failures
// and non-deterministic outputs. Max is used instead of median because the
// primary noise source is API errors (score=0), not numerical variance —
// a single successful call gives the best available LLM judgment.
func (s *LLMScorer) Score(strategy *Strategy) float64 {
	return s.ScoreWithContext(context.Background(), strategy)
}

// ScoreWithContext evaluates a strategy using the LLM with a provided context.
// This is the context-aware variant of Score, useful when the caller has an
// active request context for cancellation or timeout control.
func (s *LLMScorer) ScoreWithContext(ctx context.Context, strategy *Strategy) float64 {
	if strategy == nil {
		return 0
	}

	if s.numSamples <= 1 {
		return s.sampleOnce(ctx, strategy)
	}

	// Use errgroup for structured concurrency so each sampling goroutine is
	// ctx-cancelable. A manual semaphore (instead of g.SetLimit) preserves
	// the original behavior of bailing out when ctx is cancelled while
	// waiting for a concurrency slot.
	g, gCtx := errgroup.WithContext(ctx)
	var (
		best float64
		mu   sync.Mutex
		sem  = make(chan struct{}, 3)
	)
loop:
	for range s.numSamples {
		select {
		case <-gCtx.Done():
			break loop
		case sem <- struct{}{}:
		}
		g.Go(func() error {
			defer func() { <-sem }()
			if gCtx.Err() != nil {
				return nil
			}
			sc := s.sampleOnce(gCtx, strategy)
			mu.Lock()
			if sc > best {
				best = sc
			}
			mu.Unlock()
			return nil
		})
	}
	// Wait for all in-flight samples to complete. Errors are not returned
	// because sampleOnce never fails (it falls back to the deterministic
	// scorer); sampling results are aggregated via the shared best value.
	_ = g.Wait()
	return best
}

// sampleOnce calls the LLM once for the given strategy and returns the parsed score.
// If the LLM call fails and a fallback scorer is configured, the fallback is used
// instead — this keeps the evolution running when the API is temporarily down.
func (s *LLMScorer) sampleOnce(ctx context.Context, strategy *Strategy) float64 {
	prompt := s.buildPrompt(strategy)
	resp, err := s.client.Generate(ctx, prompt)
	if err != nil {
		if s.fallback != nil {
			return s.fallback(strategy)
		}
		return 0
	}
	return s.parseScore(resp)
}

// AsScorerFunc returns a ScorerFunc that delegates to this LLMScorer.
// Note: the returned function uses context.Background() since ScorerFunc
// does not carry context. Callers that need context propagation should
// use ScoreWithContext directly.
func (s *LLMScorer) AsScorerFunc() ScorerFunc {
	return func(agent *Strategy) float64 {
		return s.ScoreWithContext(context.Background(), agent)
	}
}

// buildPrompt constructs the evaluation prompt from a strategy.

// BatchScore evaluates all strategies in a single LLM call.
// This reduces API calls from N to 1 per generation, significantly reducing
// rate-limit pressure and improving throughput.
//
// When the batch exceeds maxBatchSize (10), it is split into sub-batches
// to keep the prompt manageable and reduce timeout risk.
//
// Implements the BatchScorer interface. If the batch call fails, falls back
// to per-agent scoring with the deterministic fallback.
func (s *LLMScorer) BatchScore(strategies []*Strategy) []float64 {
	const maxBatchSize = 10

	scores := make([]float64, len(strategies))
	if len(strategies) == 0 {
		return scores
	}

	// If small enough, do a single call.
	if len(strategies) <= maxBatchSize {
		return s.batchScoreChunk(strategies, scores, 0)
	}

	// Split into sub-batches.
	for start := 0; start < len(strategies); start += maxBatchSize {
		end := start + maxBatchSize
		if end > len(strategies) {
			end = len(strategies)
		}
		s.batchScoreChunk(strategies[start:end], scores, start)
	}
	return scores
}

// batchScoreChunk scores a chunk of strategies via a single LLM call.
func (s *LLMScorer) batchScoreChunk(chunk []*Strategy, scores []float64, offset int) []float64 {
	ctx := context.Background()
	prompt := s.buildBatchPrompt(chunk)
	resp, err := s.client.Generate(ctx, prompt)
	if err != nil {
		// Fallback: use deterministic scorer for all.
		for i, item := range chunk {
			if s.fallback != nil {
				scores[offset+i] = s.fallback(item)
			}
		}
		return scores
	}

	// Parse batch response: expect {"scores": [70, 85, 60, ...]}
	parsed := s.parseBatchScores(resp, len(chunk))
	for i := range chunk {
		if i < len(parsed) && parsed[i] > 0 {
			scores[offset+i] = parsed[i]
		} else if s.fallback != nil {
			scores[offset+i] = s.fallback(chunk[i])
		}
	}
	return scores
}

// DefaultBatchEvalPrompt is the batch evaluation prompt template.
// {strategies_json} is replaced with the JSON array of all strategies.
const DefaultBatchEvalPrompt = `You are evaluating AI agent strategies. Each strategy defines how an agent:
- Generates responses (temperature controls creativity vs determinism)
- Selects knowledge (top_k controls focus breadth)
- Structures its prompts (prompt_template sets behavior style)

Score EACH strategy on a scale of 0 to 100. Consider:
- Reasoning quality: Does the temperature allow coherent reasoning?
- Focus accuracy: Does top_k balance breadth vs precision?
- Instruction following: Does the prompt template guide appropriate behavior?
- General capability: Would this strategy perform well on diverse tasks?

Strategies to evaluate:
{strategies_json}

Respond with ONLY a JSON object containing an array of scores in the SAME ORDER as the strategies:
{"scores": [<score_0>, <score_1>, <score_2>, ...]}`

// buildBatchPrompt constructs a single prompt for evaluating all strategies.
func (s *LLMScorer) buildBatchPrompt(strategies []*Strategy) string {
	items := make([]map[string]any, len(strategies))
	for i, item := range strategies {
		params := make(map[string]any)
		for k, v := range item.Params {
			params[k] = v
		}
		params["prompt_template"] = item.PromptTemplate
		params["id"] = item.ID
		items[i] = params
	}

	data, _ := json.MarshalIndent(items, "  ", "  ")
	prompt := strings.ReplaceAll(DefaultBatchEvalPrompt, "{strategies_json}", string(data))

	if s.seed != 0 {
		prompt += fmt.Sprintf("\n\n(Scoring seed: %d. Use temperature 0 for fully deterministic evaluation.)", s.seed)
	}
	return prompt
}

// parseBatchScores extracts scores from a batch LLM response.
func (s *LLMScorer) parseBatchScores(resp string, expected int) []float64 {
	resp = strings.TrimSpace(resp)

	var parsed struct {
		Scores []float64 `json:"scores"`
	}
	if err := json.Unmarshal([]byte(resp), &parsed); err == nil && len(parsed.Scores) > 0 {
		for i, sc := range parsed.Scores {
			if sc > 100 {
				parsed.Scores[i] = 100
			} else if sc < 0 {
				parsed.Scores[i] = 0
			}
		}
		return parsed.Scores
	}
	return nil
}

// When a seed is configured, it embeds a determinism instruction to reduce
// output variance across repeated evaluations of the same parameters.
func (s *LLMScorer) buildPrompt(strategy *Strategy) string {
	params := make(map[string]any)
	for k, v := range strategy.Params {
		params[k] = v
	}
	params["prompt_template"] = strategy.PromptTemplate
	params["name"] = strategy.Name

	data, _ := json.MarshalIndent(params, "  ", "  ")
	prompt := strings.ReplaceAll(s.evalPrompt, "{strategy_json}", string(data))

	if s.seed != 0 {
		prompt += fmt.Sprintf("\n\n(Scoring seed: %d. Use temperature 0 for fully deterministic evaluation.)", s.seed)
	}
	return prompt
}

// parseScore extracts a numeric score from the LLM response.
// Expects a JSON response with a "score" field. Falls back to:
//  1. Regex extraction of "score": N from mixed text/reasoning output
//  2. Keyword heuristic for free-text responses
func (s *LLMScorer) parseScore(resp string) float64 {
	resp = strings.TrimSpace(resp)

	// Try direct JSON parse.
	var parsed struct {
		Score float64 `json:"score"`
	}
	if err := json.Unmarshal([]byte(resp), &parsed); err == nil && parsed.Score > 0 {
		if parsed.Score > 100 {
			return 100
		}
		return parsed.Score
	}

	// Try extracting "score": N from mixed text (reasoning models may embed
	// JSON within their thinking output).
	if sc := extractScoreFromText(resp); sc > 0 {
		return sc
	}

	// Fallback: keyword heuristic.
	return s.fallbackScore(resp)
}

// extractScoreFromText tries to find a "score": N pattern in free text.
func extractScoreFromText(text string) float64 {
	// Look for patterns like "score": 75 or "score":75.0
	for i := 0; i < len(text)-8; i++ {
		if text[i] == '"' && i+7 < len(text) && text[i+1:i+7] == "score\"" {
			// Find the colon and number after it.
			rest := text[i+7:]
			colonIdx := strings.Index(rest, ":")
			if colonIdx < 0 {
				continue
			}
			numStr := strings.TrimSpace(rest[colonIdx+1:])
			// Extract digits.
			end := 0
			for end < len(numStr) && (numStr[end] >= '0' && numStr[end] <= '9' || numStr[end] == '.') {
				end++
			}
			if end > 0 {
				var sc float64
				if _, err := fmt.Sscanf(numStr[:end], "%f", &sc); err == nil && sc > 0 && sc <= 100 {
					return sc
				}
			}
		}
	}
	return 0
}

// fallbackScore estimates quality from the LLM's free-text response.
func (s *LLMScorer) fallbackScore(resp string) float64 {
	keywords := map[string]float64{
		"excellent": 90, "outstanding": 95, "very good": 80,
		"good": 70, "decent": 60, "average": 50,
		"poor": 30, "bad": 20, "terrible": 10,
	}
	lower := strings.ToLower(resp)
	best := 50.0
	for kw, score := range keywords {
		if strings.Contains(lower, kw) && score > best {
			best = score
		}
	}
	return best
}
