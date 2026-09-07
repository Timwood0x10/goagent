package agents

import (
	"strings"
	"testing"
)

// TestToolBudgetFromParams locks the "malformed budget must not restrict"
// contract: every unparsable shape degrades to 0 (unlimited), never to 1 or an
// error, because a bad mutation must only fail to restrict execution — it must
// not break it.
func TestToolBudgetFromParams(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		want   int
	}{
		{name: "nil_params_unlimited", params: nil, want: 0},
		{name: "missing_key_unlimited", params: map[string]any{"temperature": 0.5}, want: 0},
		{name: "int", params: map[string]any{ParamKeyBudget: 3}, want: 3},
		{name: "int64", params: map[string]any{ParamKeyBudget: int64(4)}, want: 4},
		// JSON round-trip through the plan payload yields float64.
		{name: "float64_from_json", params: map[string]any{ParamKeyBudget: float64(5)}, want: 5},
		{name: "float64_truncates", params: map[string]any{ParamKeyBudget: 2.9}, want: 2},
		{name: "float64_below_one_unlimited", params: map[string]any{ParamKeyBudget: 0.4}, want: 0},
		{name: "string", params: map[string]any{ParamKeyBudget: "7"}, want: 7},
		{name: "string_padded", params: map[string]any{ParamKeyBudget: "  8 "}, want: 8},
		{name: "string_garbage_unlimited", params: map[string]any{ParamKeyBudget: "many"}, want: 0},
		{name: "zero_unlimited", params: map[string]any{ParamKeyBudget: 0}, want: 0},
		{name: "negative_unlimited", params: map[string]any{ParamKeyBudget: -3}, want: 0},
		{name: "wrong_type_unlimited", params: map[string]any{ParamKeyBudget: true}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ToolBudgetFromParams(tt.params); got != tt.want {
				t.Fatalf("ToolBudgetFromParams() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestToolAllowedByBudget covers the gate's boundary: the Nth call is allowed,
// the (N+1)th is not, and budget<=0 never blocks.
func TestToolAllowedByBudget(t *testing.T) {
	tests := []struct {
		name   string
		uses   map[string]int
		budget int
		want   bool
	}{
		{name: "unlimited_budget_allows", uses: map[string]int{"search": 99}, budget: 0, want: true},
		{name: "negative_budget_allows", uses: map[string]int{"search": 99}, budget: -1, want: true},
		{name: "nil_uses_allows", uses: nil, budget: 1, want: true},
		{name: "untracked_tool_allows", uses: map[string]int{"other": 5}, budget: 1, want: true},
		{name: "below_budget_allows", uses: map[string]int{"search": 1}, budget: 2, want: true},
		{name: "at_budget_blocks", uses: map[string]int{"search": 2}, budget: 2, want: false},
		{name: "over_budget_blocks", uses: map[string]int{"search": 5}, budget: 2, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ToolAllowedByBudget("search", tt.uses, tt.budget); got != tt.want {
				t.Fatalf("ToolAllowedByBudget() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestToolAllowedByBudgetExactCallCount walks the counter the way the executors
// do (increment before the call) and asserts exactly `budget` calls get through.
// This is what makes the cap bind: counting after execution would let a failing
// tool retry forever.
func TestToolAllowedByBudgetExactCallCount(t *testing.T) {
	const budget = 3
	uses := map[string]int{}
	allowed := 0
	for range 10 {
		if !ToolAllowedByBudget("search", uses, budget) {
			break
		}
		uses["search"]++
		allowed++
	}
	if allowed != budget {
		t.Fatalf("allowed %d calls, want exactly %d", allowed, budget)
	}
}

func TestPriorHintFromParams(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		want   string
	}{
		{name: "nil_params_empty", params: nil, want: ""},
		{name: "missing_key_empty", params: map[string]any{"temperature": 0.5}, want: ""},
		{name: "string_hint", params: map[string]any{ParamKeyPrior: "prefer web_search"}, want: "prefer web_search"},
		{name: "trimmed", params: map[string]any{ParamKeyPrior: "  high  "}, want: "high"},
		{name: "whitespace_only_empty", params: map[string]any{ParamKeyPrior: "   "}, want: ""},
		{name: "non_string_empty", params: map[string]any{ParamKeyPrior: 0.9}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PriorHintFromParams(tt.params); got != tt.want {
				t.Fatalf("PriorHintFromParams() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestApplyPriorHint locks two properties: an empty hint leaves the prompt
// byte-identical (so the non-evolved path is untouched), and a present hint is
// prepended while the original prompt survives verbatim.
func TestApplyPriorHint(t *testing.T) {
	const prompt = "Plan a trip to Kyoto"

	t.Run("empty_hint_is_identity", func(t *testing.T) {
		if got := ApplyPriorHint(prompt, ""); got != prompt {
			t.Fatalf("ApplyPriorHint() = %q, want unchanged %q", got, prompt)
		}
	})

	t.Run("hint_prepended_prompt_preserved", func(t *testing.T) {
		got := ApplyPriorHint(prompt, "prefer web_search")
		if !strings.HasSuffix(got, prompt) {
			t.Fatalf("original prompt must survive verbatim at the tail, got %q", got)
		}
		if !strings.Contains(got, "prefer web_search") {
			t.Fatalf("hint missing from %q", got)
		}
		// The hint must state it is advisory: prior biases, never restricts.
		if !strings.Contains(got, "does not restrict") {
			t.Fatalf("hint must be marked advisory, got %q", got)
		}
	})
}

// TestPriorDoesNotAffectToolSet is the separation-of-concerns guard for the
// budget/prior pair: prior is prompt-only. A params map carrying ONLY a prior must leave both
// whitelist and budget at their permissive zero values, so a bad prior can
// never silently remove a tool from the advertised set.
func TestPriorDoesNotAffectToolSet(t *testing.T) {
	params := map[string]any{ParamKeyPrior: "never use tools"}
	if set := ToolWhitelistFromParams(params); set != nil {
		t.Fatalf("prior must not produce a whitelist, got %v", set)
	}
	if b := ToolBudgetFromParams(params); b != 0 {
		t.Fatalf("prior must not produce a budget, got %d", b)
	}
}
