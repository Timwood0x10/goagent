package ares_bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// stubLLMScorerClient is a minimal LLM client for wireLLMScorer tests.
// It returns a fixed JSON score response that LLMScorer.parseScore can parse.
// It satisfies the evoService.LLMClient interface (Generate only).
type stubLLMScorerClient struct {
	response string
	err      error
}

// Generate returns the pre-configured response or error.
func (c *stubLLMScorerClient) Generate(_ context.Context, _ string) (string, error) {
	if c.err != nil {
		return "", c.err
	}
	return c.response, nil
}

// TestWireLLMScorer verifies the opt-in LLM scorer wiring logic using
// table-driven tests. Each case exercises a different combination of config
// and component state, asserting whether scorers are produced or nil.
func TestWireLLMScorer(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *ares_config.Config
		comp       *Components
		wantScorer bool
		wantMax    int
		wantScore  float64 // expected score when wantScorer is true (LLM path)
	}{
		{
			name: "disabled returns nil scorers",
			cfg: &ares_config.Config{
				Evolution: ares_config.EvolutionConfig{
					LLMScoring: ares_config.LLMScoringConfig{Enabled: false},
				},
			},
			comp: &Components{LLM: &LLMComponents{
				Client: &stubLLMScorerClient{response: `{"score": 75}`},
			}},
			wantScorer: false,
			wantMax:    0,
		},
		{
			name: "enabled with valid LLM client returns scorers",
			cfg: &ares_config.Config{
				Evolution: ares_config.EvolutionConfig{
					LLMScoring: ares_config.LLMScoringConfig{
						Enabled:               true,
						Seed:                  42,
						MaxCallsPerGeneration: 50,
					},
				},
			},
			comp: &Components{LLM: &LLMComponents{
				Client: &stubLLMScorerClient{response: `{"score": 75}`},
			}},
			wantScorer: true,
			wantMax:    50,
			wantScore:  75,
		},
		{
			name: "enabled with nil LLM components returns nil scorers",
			cfg: &ares_config.Config{
				Evolution: ares_config.EvolutionConfig{
					LLMScoring: ares_config.LLMScoringConfig{Enabled: true},
				},
			},
			comp:       &Components{LLM: nil},
			wantScorer: false,
			wantMax:    0,
		},
		{
			name: "enabled with nil LLM client returns nil scorers",
			cfg: &ares_config.Config{
				Evolution: ares_config.EvolutionConfig{
					LLMScoring: ares_config.LLMScoringConfig{Enabled: true},
				},
			},
			comp:       &Components{LLM: &LLMComponents{Client: nil}},
			wantScorer: false,
			wantMax:    0,
		},
		{
			name: "enabled with non-LLM client type returns nil scorers",
			cfg: &ares_config.Config{
				Evolution: ares_config.EvolutionConfig{
					LLMScoring: ares_config.LLMScoringConfig{Enabled: true},
				},
			},
			comp:       &Components{LLM: &LLMComponents{Client: "not-a-client"}},
			wantScorer: false,
			wantMax:    0,
		},
		{
			name:       "nil config returns nil scorers",
			cfg:        nil,
			comp:       &Components{LLM: &LLMComponents{Client: &stubLLMScorerClient{}}},
			wantScorer: false,
			wantMax:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scorer, heuristic, maxCalls := wireLLMScorer(tt.cfg, tt.comp)

			if tt.wantScorer {
				require.NotNil(t, scorer, "scorer should be non-nil when LLM scoring is enabled")
				require.NotNil(t, heuristic, "heuristic should be non-nil when LLM scoring is enabled")
				assert.Equal(t, tt.wantMax, maxCalls, "maxCalls should match config value")

				// Verify the LLM scorer returns the expected score from the
				// mock LLM client. This exercises the full wiring path:
				// mutation.Strategy -> ToAPIStrategy -> LLMScorer -> mock LLM.
				agent := &mutation.Strategy{
					ID:     "test-strategy",
					Name:   "test",
					Params: map[string]any{"temperature": 0.7, "top_k": 40},
				}
				score := scorer(agent)
				assert.Equal(t, tt.wantScore, score,
					"LLM scorer should return the mock's parsed score")

				// Verify the heuristic returns a valid deterministic score
				// (independent of the LLM call).
				hScore := heuristic(agent)
				assert.True(t, hScore >= 0 && hScore <= 100,
					"heuristic should return a score in [0, 100], got %f", hScore)
			} else {
				assert.Nil(t, scorer, "scorer should be nil when disabled or client unavailable")
				assert.Nil(t, heuristic, "heuristic should be nil when disabled or client unavailable")
				assert.Equal(t, 0, maxCalls, "maxCalls should be 0 when disabled or client unavailable")
			}
		})
	}
}

// TestWireLLMScorer_DefaultMaxCalls verifies that when MaxCallsPerGeneration
// is zero in config, the returned value is zero (the caller leaves
// gaCfg.MaxLLMCallsPerGeneration at its DefaultSystemConfig value, and
// setDefaults fills in 100 before Validate runs). This test confirms the
// wireLLMScorer function itself does not apply the default — that is the
// config layer's responsibility.
func TestWireLLMScorer_DefaultMaxCalls(t *testing.T) {
	cfg := &ares_config.Config{
		Evolution: ares_config.EvolutionConfig{
			LLMScoring: ares_config.LLMScoringConfig{
				Enabled:               true,
				MaxCallsPerGeneration: 0, // zero — not defaulted by wireLLMScorer
			},
		},
	}
	comp := &Components{LLM: &LLMComponents{
		Client: &stubLLMScorerClient{response: `{"score": 80}`},
	}}

	scorer, heuristic, maxCalls := wireLLMScorer(cfg, comp)
	require.NotNil(t, scorer)
	require.NotNil(t, heuristic)
	assert.Equal(t, 0, maxCalls, "wireLLMScorer should return 0 when config value is 0")
}
