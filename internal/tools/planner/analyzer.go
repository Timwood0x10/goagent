package planner

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ruleBasedAnalyzer implements SemanticAnalyzer using keyword-based rules.
// It does not use an LLM; all matching is deterministic.
type ruleBasedAnalyzer struct {
	rules []intentRule
}

// intentRule maps a keyword pattern to an Intent.
type intentRule struct {
	keywords     []string
	goal         string
	operation    string
	complexity   string
	capabilities []string
}

// NewRuleBasedAnalyzer creates a SemanticAnalyzer with built-in rules
// for common request patterns.
func NewRuleBasedAnalyzer() SemanticAnalyzer {
	return &ruleBasedAnalyzer{
		rules: defaultRules(),
	}
}

// Analyze matches the request against known patterns and returns a structured intent.
// Returns an error if no rule matches.
func (a *ruleBasedAnalyzer) Analyze(_ context.Context, request string) (*Intent, error) {
	if request == "" {
		return nil, errors.New("planner: empty request")
	}

	lower := strings.ToLower(request)

	for _, rule := range a.rules {
		if matchAnyKeyword(lower, rule.keywords) {
			return &Intent{
				Goal:                 rule.goal,
				Operation:            rule.operation,
				Complexity:           rule.complexity,
				RequiredCapabilities: rule.capabilities,
				Constraints:          extractConstraints(lower),
			}, nil
		}
	}

	return nil, fmt.Errorf("planner: no matching rule for request: %s", request)
}

// defaultRules returns the built-in keyword rules.
func defaultRules() []intentRule {
	return []intentRule{
		// Most specific rules first
		{
			keywords:     []string{"累加", "求和"},
			goal:         "mathematical computation",
			operation:    "summation",
			complexity:   "simple",
			capabilities: []string{"Summation", "Arithmetic"},
		},
		{
			keywords:     []string{"sum", "add", "plus", "total"},
			goal:         "mathematical computation",
			operation:    "summation",
			complexity:   "simple",
			capabilities: []string{"Summation", "Arithmetic"},
		},
		{
			// NOTE: matchAnyKeyword uses strings.Contains, which treats
			// keywords literally — a regex like "到.*和" never matches. Keep
			// only literal keywords ("到的和" covers "1到100的和" and friends).
			keywords:     []string{"到的和", "的和"},
			goal:         "mathematical computation",
			operation:    "summation",
			complexity:   "simple",
			capabilities: []string{"Summation", "Arithmetic"},
		},
		// Advanced math
		{
			keywords: []string{"factorial", "permutation", "combination", "combinatorics",
				"nPr", "nCr", "阶乘", "排列", "组合", "离散"},
			goal:         "mathematical computation",
			operation:    "discrete_math",
			complexity:   "moderate",
			capabilities: []string{"DiscreteMath", "Arithmetic"},
		},
		{
			keywords: []string{"probability", "binomial", "normal distribution", "poisson",
				"概率", "正态分布", "泊松", "贝叶斯"},
			goal:         "mathematical computation",
			operation:    "probability",
			complexity:   "moderate",
			capabilities: []string{"Probability", "Arithmetic"},
		},
		{
			keywords: []string{"mean", "median", "stddev", "variance", "average",
				"平均", "标准差", "方差", "统计"},
			goal:         "mathematical computation",
			operation:    "statistics",
			complexity:   "moderate",
			capabilities: []string{"Statistics", "Arithmetic"},
		},
		{
			keywords:     []string{"gcd", "lcm", "prime", "modulo", "数论", "公约数", "公倍数", "素数", "质数"},
			goal:         "mathematical computation",
			operation:    "number_theory",
			complexity:   "moderate",
			capabilities: []string{"NumberTheory", "Arithmetic"},
		},
		{
			keywords:     []string{"hash", "md5", "sha1", "sha256", "sha512", "哈希"},
			goal:         "cryptographic operation",
			operation:    "hashing",
			complexity:   "simple",
			capabilities: []string{"Hashing"},
		},
		{
			keywords:     []string{"json", "parse", "format", "pretty", "marshal", "unmarshal"},
			goal:         "data transformation",
			operation:    "json_processing",
			complexity:   "simple",
			capabilities: []string{"JSONProcessing"},
		},
		{
			keywords:     []string{"base64", "encode", "decode", "编码", "解码"},
			goal:         "encoding operation",
			operation:    "base64",
			complexity:   "simple",
			capabilities: []string{"Base64"},
		},
		{
			keywords:     []string{"pdf", "document"},
			goal:         "document processing",
			operation:    "pdf_parsing",
			complexity:   "moderate",
			capabilities: []string{"PDFParsing", "TextExtraction"},
		},
		{
			keywords:     []string{"extract", "read", "提取", "解析", "读取"},
			goal:         "document processing",
			operation:    "extraction",
			complexity:   "moderate",
			capabilities: []string{"PDFParsing", "TextExtraction"},
		},
		{
			keywords:     []string{"calculate", "compute", "evaluate", "计算", "运算", "math", "等于多少"},
			goal:         "mathematical computation",
			operation:    "arithmetic",
			complexity:   "simple",
			capabilities: []string{"Arithmetic"},
		},
		// General rules below
		{
			keywords: []string{"upper", "lower", "trim", "split", "join", "reverse",
				"大写", "小写", "截取", "替换", "去掉", "空格", "两端"},
			goal:         "text processing",
			operation:    "string_manipulation",
			complexity:   "simple",
			capabilities: []string{"StringManipulation"},
		},
		{
			keywords:     []string{"search", "find", "lookup", "查询", "搜索", "查找"},
			goal:         "information retrieval",
			operation:    "search",
			complexity:   "moderate",
			capabilities: []string{"WebSearch"},
		},
		{
			keywords:     []string{"download", "fetch", "get", "下载", "获取"},
			goal:         "data retrieval",
			operation:    "fetch",
			complexity:   "moderate",
			capabilities: []string{"HTTPRequest", "WebFetch"},
		},
		{
			keywords:     []string{"embed", "vector", "embedding", "向量化"},
			goal:         "vector embedding",
			operation:    "embedding",
			complexity:   "moderate",
			capabilities: []string{"Embedding"},
		},
		{
			keywords:     []string{"generate", "create", "id", "uuid", "生成"},
			goal:         "identifier generation",
			operation:    "id_generation",
			complexity:   "simple",
			capabilities: []string{"IDGeneration"},
		},
		{
			keywords:     []string{"regex", "regular expression", "正则"},
			goal:         "text processing",
			operation:    "regex",
			complexity:   "simple",
			capabilities: []string{"Regex"},
		},
		{
			keywords:     []string{"code", "run", "execute", "脚本", "运行", "执行"},
			goal:         "code execution",
			operation:    "code_execution",
			complexity:   "complex",
			capabilities: []string{"CodeExecution"},
		},
		{
			keywords:     []string{"plan", "schedule", "task", "规划", "任务"},
			goal:         "task management",
			operation:    "planning",
			complexity:   "complex",
			capabilities: []string{"TaskPlanning"},
		},
	}
}

// matchAnyKeyword checks if at least one keyword is present in the request.
func matchAnyKeyword(request string, keywords []string) bool {
	for _, kw := range keywords {
		if strings.Contains(request, kw) {
			return true
		}
	}
	return false
}

// limitRe matches explicit numeric limits in a lowercased request, e.g.
// "limit 5", "top 10", or "top_k 20".
var limitRe = regexp.MustCompile(`(?:limit|top(?:_k)?)\s+(\d+)`)

// extractConstraints extracts constraint-like patterns from the request,
// which is expected to be lowercased. It recognizes explicit numeric limits
// ("limit N", "top N", "top_k N") and qualitative speed/accuracy preferences.
// Unknown patterns are ignored.
func extractConstraints(request string) map[string]string {
	constraints := make(map[string]string)

	if m := limitRe.FindStringSubmatch(request); len(m) > 1 {
		if strings.HasPrefix(m[0], "top_k") {
			constraints["top_k"] = m[1]
		} else {
			constraints["limit"] = m[1]
		}
	}
	if strings.Contains(request, "fast") || strings.Contains(request, "quick") {
		constraints["speed"] = "fast"
	}
	if strings.Contains(request, "accurate") || strings.Contains(request, "precise") {
		constraints["accuracy"] = "high"
	}

	return constraints
}
