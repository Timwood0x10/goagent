package mutation

import (
	"strings"
	"testing"
	"time"
)

func TestMutationTypeString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		give MutationType
		want string
	}{
		{give: MutationParameter, want: "parameter"},
		{give: MutationPrompt, want: "prompt"},
		{give: MutationTool, want: "tool"},
		{give: MutationCrossover, want: "crossover"},
		{give: MutationRoot, want: "root"},
		{give: MutationType(0), want: "unknown"},
		{give: MutationType(99), want: "unknown"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()

			if got := tt.give.String(); got != tt.want {
				t.Errorf("MutationType(%d).String() = %q, want %q", tt.give, got, tt.want)
			}
		})
	}
}

func TestParseMutationType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		give    string
		want    MutationType
		wantLog bool // whether a warning log is expected (for garbage input)
	}{
		{name: "parameter", give: "parameter", want: MutationParameter, wantLog: false},
		{name: "prompt", give: "prompt", want: MutationPrompt, wantLog: false},
		{name: "tool", give: "tool", want: MutationTool, wantLog: false},
		{name: "crossover", give: "crossover", want: MutationCrossover, wantLog: false},
		{name: "root", give: "root", want: MutationRoot, wantLog: false},
		{name: "empty string treated as root", give: "", want: MutationRoot, wantLog: false},
		{name: "garbage falls back to root with warning", give: "garbage", want: MutationRoot, wantLog: true},
		{name: "unknown type falls back to root with warning", give: "random_type", want: MutationRoot, wantLog: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := ParseMutationType(tt.give)
			if got != tt.want {
				t.Errorf("ParseMutationType(%q) = %d, want %d", tt.give, got, tt.want)
			}
		})
	}
}

func TestMutationRootRoundTrip(t *testing.T) {
	t.Parallel()

	if got := ParseMutationType(MutationRoot.String()); got != MutationRoot {
		t.Errorf("ParseMutationType(MutationRoot.String()) = %d, want %d", got, MutationRoot)
	}
}

// TestComputeEvidenceKey_IncludesToolsField verifies Y.3-ACT: the tool
// whitelist in Params["tools"] is included in the evidence key so two
// strategies that differ only in tool selection land on different keys.
func TestComputeEvidenceKey_IncludesToolsField(t *testing.T) {
	t.Parallel()

	base := Strategy{
		ID:             "test-evidence-tools",
		Version:        1,
		PromptTemplate: "default prompt",
		Params:         map[string]any{"temperature": 0.5},
		CreatedAt:      time.Now(),
	}

	stratA := base.Clone()
	stratA.Params["tools"] = "web_search,calculator"

	stratB := base.Clone()
	stratB.Params["tools"] = "web_search,code_exec"

	keyA := stratA.ComputeEvidenceKey()
	keyB := stratB.ComputeEvidenceKey()

	if keyA == keyB {
		t.Errorf("strategies with different tool whitelists must have different evidence keys: both got %q", keyA)
	}
}

// TestComputeEvidenceKey_ToolOrderIndependent verifies that the tool field
// in the evidence key is order-independent: "b,a" and "a, b" produce the same
// key because the set of tools is what matters, not the order.
func TestComputeEvidenceKey_ToolOrderIndependent(t *testing.T) {
	t.Parallel()

	base := Strategy{
		ID:             "test-evidence-order",
		Version:        1,
		PromptTemplate: "default prompt",
		Params:         map[string]any{"temperature": 0.5},
		CreatedAt:      time.Now(),
	}

	stratA := base.Clone()
	stratA.Params["tools"] = "web_search,calculator"

	stratB := base.Clone()
	stratB.Params["tools"] = "calculator, web_search"

	keyA := stratA.ComputeEvidenceKey()
	keyB := stratB.ComputeEvidenceKey()

	if keyA != keyB {
		t.Errorf("evidence key must be order-independent for tools: got %q and %q", keyA, keyB)
	}
}

// TestComputeEvidenceKey_NoToolsField verifies that strategies without a
// tools field produce a key without the tools suffix.
func TestComputeEvidenceKey_NoToolsField(t *testing.T) {
	t.Parallel()

	s := Strategy{
		ID:             "test-evidence-no-tools",
		Version:        1,
		PromptTemplate: "default prompt",
		Params:         map[string]any{"temperature": 0.5},
		CreatedAt:      time.Now(),
	}

	key := s.ComputeEvidenceKey()

	// Key should not contain "|tools="
	if strings.Contains(key, "|tools=") {
		t.Errorf("evidence key should not contain tools suffix when no tools field: got %q", key)
	}
}

// TestClone_DoesNotInheritHashCache is the regression guard for the fitness
// bug: Clone() is the first step of every mutation path, so a clone that
// carried its parent's cached hash would be hash-identical to the parent even
// after its Params were mutated. StrategyHash's cache fast path would return
// the parent hash, the score cache would hit the parent's entry, and the child
// would be scored with its parent's fitness — selection pressure silently
// zeroed. The clone must therefore start with a cold hash.
func TestClone_DoesNotInheritHashCache(t *testing.T) {
	t.Parallel()

	parent := &Strategy{
		ID:             "parent",
		Version:        1,
		PromptTemplate: "prompt",
		Params:         map[string]any{"temperature": 0.5},
		CreatedAt:      time.Now(),
	}
	parent.SetHash(0xDEADBEEF)
	if !parent.HashCached() {
		t.Fatal("precondition: parent must have a cached hash")
	}

	child := parent.Clone()
	if child.HashCached() {
		t.Error("Clone() must not inherit the parent's cached hash: a mutated child would otherwise resolve to the parent's hash and inherit its cached score")
	}
	if child.HashValue() == parent.HashValue() && child.HashValue() != 0 {
		t.Errorf("clone hash value = %#x, want zero value", child.HashValue())
	}

	// The parent's own cache must survive Clone (it is still valid for the
	// parent, which was not mutated).
	if !parent.HashCached() || parent.HashValue() != 0xDEADBEEF {
		t.Error("Clone() must not disturb the receiver's own hash cache")
	}
}

// TestComputeEvidenceKey_IncludesIntegerParams verifies that integer-valued
// params reach the evidence key. DefaultParamRanges stores untyped int
// literals for top_k / max_steps / memory_limit, so a float64-only type
// assertion dropped those dimensions entirely and let two strategies differing
// only in top_k collapse onto one key — merging their evidence.
func TestComputeEvidenceKey_IncludesIntegerParams(t *testing.T) {
	t.Parallel()

	base := Strategy{
		ID:             "test-evidence-int",
		Version:        1,
		PromptTemplate: "default prompt",
		Params:         map[string]any{"temperature": 0.5},
		CreatedAt:      time.Now(),
	}

	stratA := base.Clone()
	stratA.Params["top_k"] = 20 // int, as DefaultParamRanges yields

	stratB := base.Clone()
	stratB.Params["top_k"] = 80

	keyA := stratA.ComputeEvidenceKey()
	keyB := stratB.ComputeEvidenceKey()

	if keyA == keyB {
		t.Errorf("strategies differing in an integer param must have different evidence keys: both got %q", keyA)
	}
	if !strings.Contains(keyA, "top_k=20.00") {
		t.Errorf("evidence key must include the integer param: got %q", keyA)
	}

	// An int and its float64 equivalent describe the same phenotype (JSON
	// round-trips turn ints into float64s), so they must agree on the key —
	// otherwise a strategy would lose its evidence merely by being reloaded
	// from the store.
	stratC := base.Clone()
	stratC.Params["top_k"] = float64(20)
	if got := stratC.ComputeEvidenceKey(); got != keyA {
		t.Errorf("int and float64 forms of the same value must agree: %q vs %q", got, keyA)
	}
}

// TestComputeEvidenceKey_SkipsNonNumericParams verifies that non-numeric
// params (other than the dedicated tools field) stay out of the numeric
// section of the key rather than being formatted as zero.
func TestComputeEvidenceKey_SkipsNonNumericParams(t *testing.T) {
	t.Parallel()

	s := Strategy{
		ID:             "test-evidence-nonnumeric",
		Version:        1,
		PromptTemplate: "p",
		Params:         map[string]any{"model": "gpt-4", "verbose": true, "temperature": 0.5},
		CreatedAt:      time.Now(),
	}

	key := s.ComputeEvidenceKey()
	if strings.Contains(key, "model=") || strings.Contains(key, "verbose=") {
		t.Errorf("non-numeric params must not appear in the numeric section: got %q", key)
	}
	if !strings.Contains(key, "temperature=0.50") {
		t.Errorf("numeric param missing from key: got %q", key)
	}
}

// TestComputeEvidenceKey_EmptyToolsField verifies that an empty tools string
// does not add the tools suffix to the evidence key.
func TestComputeEvidenceKey_EmptyToolsField(t *testing.T) {
	t.Parallel()

	s := Strategy{
		ID:             "test-evidence-empty-tools",
		Version:        1,
		PromptTemplate: "default prompt",
		Params:         map[string]any{"temperature": 0.5, "tools": ""},
		CreatedAt:      time.Now(),
	}

	key := s.ComputeEvidenceKey()

	if strings.Contains(key, "|tools=") {
		t.Errorf("evidence key should not contain tools suffix for empty tools: got %q", key)
	}
}

// TestComputeEvidenceKey_IncludesBudgetPrior verifies the Y1 C5 attribution
// half of the budget/prior wiring: both change execution behavior, so both
// must change the key. Without these suffixes two strategies differing only
// in budget or prior would share one evidence stream and evolution could not
// select between them (the P1 gap: behavior moves, attribution does not).
func TestComputeEvidenceKey_IncludesBudgetPrior(t *testing.T) {
	t.Parallel()

	newBase := func() Strategy {
		return Strategy{
			ID:             "test-evidence-budget-prior",
			Version:        1,
			PromptTemplate: "default prompt",
			Params:         map[string]any{"temperature": 0.5},
			CreatedAt:      time.Now(),
		}
	}

	t.Run("prior_only_difference_separates", func(t *testing.T) {
		t.Parallel()
		a := newBase()
		a.Params["prior"] = "prefer web_search"
		b := newBase()
		b.Params["prior"] = "prefer calculator"
		if a.ComputeEvidenceKey() == b.ComputeEvidenceKey() {
			t.Errorf("strategies differing only in prior must have different evidence keys: both got %q", a.ComputeEvidenceKey())
		}
	})

	t.Run("budget_only_difference_separates", func(t *testing.T) {
		t.Parallel()
		a := newBase()
		a.Params["budget"] = 1
		b := newBase()
		b.Params["budget"] = 5
		keyA, keyB := a.ComputeEvidenceKey(), b.ComputeEvidenceKey()
		if keyA == keyB {
			t.Errorf("strategies differing only in budget must have different evidence keys: both got %q", keyA)
		}
		if !strings.Contains(keyA, "|budget=1") {
			t.Errorf("evidence key must carry the canonical budget suffix: got %q", keyA)
		}
	})

	t.Run("budget_spellings_agree", func(t *testing.T) {
		t.Parallel()
		// int (strategy store), float64 (JSON round-trip), and string (node
		// Metadata) are all spellings the executor accepts for the same cap —
		// they must agree on one key, or one phenotype splits by spelling.
		spellings := []any{3, int64(3), float64(3), "3", "  3 "}
		var want string
		for i, spelling := range spellings {
			s := newBase()
			s.Params["budget"] = spelling
			got := s.ComputeEvidenceKey()
			if i == 0 {
				want = got
				continue
			}
			if got != want {
				t.Errorf("budget spelling %v (%T) key %q disagrees with %q", spelling, spelling, got, want)
			}
		}
	})

	t.Run("absent_budget_prior_add_no_suffix", func(t *testing.T) {
		t.Parallel()
		s := newBase()
		key := s.ComputeEvidenceKey()
		if strings.Contains(key, "|budget=") || strings.Contains(key, "|prior=") {
			t.Errorf("strategies without budget/prior must keep the historical key: got %q", key)
		}
		empty := newBase()
		empty.Params["budget"] = 0
		empty.Params["prior"] = "   "
		if got := empty.ComputeEvidenceKey(); got != key {
			t.Errorf("zero budget / blank prior must be unlimited-identity: %q vs %q", got, key)
		}
	})
}
