// Package planner - capability definition model.
//
// This file defines the canonical capability model used by the planner.
// Every built-in tool must expose at least one capability from this set.
package planner

// CapabilityDef defines a single capability with its metadata.
type CapabilityDef struct {
	// Name is the canonical capability name (e.g., "Arithmetic", "PDFParsing").
	Name string

	// Aliases are alternative names for natural-language matching.
	Aliases []string

	// Description explains what this capability does.
	Description string

	// InputType is the expected input type.
	InputType string

	// OutputType is the produced output type.
	OutputType string

	// Version tracks capability definition changes.
	Version int

	// Deterministic indicates same input always produces same output.
	Deterministic bool
}

// BuiltinCapabilities returns the canonical set of built-in capabilities.
// Each capability maps to one or more tools in the registry.
func BuiltinCapabilities() []CapabilityDef {
	return []CapabilityDef{
		{
			Name: "Arithmetic", Version: 1,
			Aliases:     []string{"calculate", "compute", "math", "加", "减", "乘", "除", "计算", "运算"},
			Description: "Basic arithmetic operations: +, -, *, /, %, **",
			InputType:   "Expression", OutputType: "Number",
			Deterministic: true,
		},
		{
			Name: "Summation", Version: 1,
			Aliases:     []string{"sum", "add", "累加", "求和", "total", "plus"},
			Description: "Fast summation using Gaussian formula: n*(n+1)/2",
			InputType:   "Expression", OutputType: "Number",
			Deterministic: true,
		},
		{
			Name: "DiscreteMath", Version: 1,
			Aliases: []string{"combinatorics", "permutation", "combination", "factorial",
				"组合", "排列", "阶乘", "离散"},
			Description: "Discrete math: factorial, nPr, nCr, combinatorics",
			InputType:   "Expression", OutputType: "Number",
			Deterministic: true,
		},
		{
			Name: "Probability", Version: 1,
			Aliases: []string{"probability", "binomial", "normal", "poisson",
				"概率", "正态分布", "泊松"},
			Description: "Probability: binomial, normal PDF, Poisson distributions",
			InputType:   "Expression", OutputType: "Number",
			Deterministic: true,
		},
		{
			Name: "Statistics", Version: 1,
			Aliases: []string{"mean", "median", "stddev", "variance",
				"平均", "标准差", "方差", "统计"},
			Description: "Statistics: mean, median, stddev, variance",
			InputType:   "Values", OutputType: "Number",
			Deterministic: true,
		},
		{
			Name: "NumberTheory", Version: 1,
			Aliases: []string{"gcd", "lcm", "prime", "modulo",
				"最大公约数", "最小公倍数", "素数", "质数", "数论"},
			Description: "Number theory: gcd, lcm, isPrime",
			InputType:   "Expression", OutputType: "Number",
			Deterministic: true,
		},
		{
			Name: "StringManipulation", Version: 1,
			Aliases: []string{"upper", "lower", "trim", "split", "join", "reverse", "replace",
				"大写", "小写", "截取", "替换"},
			Description: "String operations: case conversion, trim, split, join, reverse, substring, replace",
			InputType:   "Text", OutputType: "Text",
			Deterministic: true,
		},
		{
			Name: "Regex", Version: 1,
			Aliases:     []string{"regular expression", "match", "正则", "匹配"},
			Description: "Regular expression match, extract, and replace",
			InputType:   "Text", OutputType: "Text",
			Deterministic: true,
		},
		{
			Name: "Hashing", Version: 1,
			Aliases:     []string{"md5", "sha1", "sha256", "sha512", "hash", "哈希"},
			Description: "Cryptographic hashing: MD5, SHA1, SHA256, SHA512",
			InputType:   "Text", OutputType: "Text",
			Deterministic: true,
		},
		{
			Name: "Base64", Version: 1,
			Aliases:     []string{"encode", "decode", "base64", "编码", "解码"},
			Description: "Base64 encoding and decoding",
			InputType:   "Text", OutputType: "Text",
			Deterministic: true,
		},
		{
			Name: "JSONProcessing", Version: 1,
			Aliases:     []string{"json", "parse", "format", "pretty", "marshal", "unmarshal"},
			Description: "JSON parse, extract, merge, and pretty-print",
			InputType:   "JSON", OutputType: "JSON",
			Deterministic: true,
		},
		{
			Name: "PDFParsing", Version: 1,
			Aliases:     []string{"pdf", "document", "extract", "parse pdf", "读取pdf", "解析pdf"},
			Description: "Extract text content from PDF files",
			InputType:   "File", OutputType: "Text",
			Deterministic: true,
		},
		{
			Name: "WebSearch", Version: 1,
			Aliases:     []string{"search", "find", "lookup", "查询", "搜索", "查找"},
			Description: "Web search using SearXNG meta search engine",
			InputType:   "Query", OutputType: "JSON",
			Deterministic: false,
		},
		{
			Name: "HTTPRequest", Version: 1,
			Aliases:     []string{"http", "request", "fetch", "get", "post", "api"},
			Description: "Make HTTP requests to external APIs",
			InputType:   "URL", OutputType: "Text",
			Deterministic: false,
		},
		{
			Name: "IDGeneration", Version: 1,
			Aliases:     []string{"uuid", "generate", "id", "生成"},
			Description: "Generate UUIDs and short unique identifiers",
			InputType:   "None", OutputType: "Identifier",
			Deterministic: false,
		},
		{
			Name: "CodeExecution", Version: 1,
			Aliases:     []string{"run", "execute", "code", "脚本", "执行"},
			Description: "Execute code snippets in sandboxed environments",
			InputType:   "Code", OutputType: "Text",
			Deterministic: true,
		},
		{
			Name: "Embedding", Version: 1,
			Aliases:     []string{"vector", "embed", "embedding"},
			Description: "Generate vector embeddings for text",
			InputType:   "Text", OutputType: "Vector",
			Deterministic: true,
		},
		{
			Name: "DateTime", Version: 1,
			Aliases:     []string{"time", "date", "now", "format", "时间", "日期"},
			Description: "Date and time operations: now, format, parse, add, diff",
			InputType:   "Query", OutputType: "Text",
			Deterministic: false,
		},
		{
			Name: CapabilityDataTransform, Version: 1,
			Aliases:     []string{"csv", "transform", "convert", "转换"},
			Description: "Transform data between CSV, JSON, and flattened formats",
			InputType:   "Text", OutputType: "Text",
			Deterministic: true,
		},
		{
			Name: CapabilityLogAnalysis, Version: 1,
			Aliases:     []string{"log", "analyze", "日志", "分析"},
			Description: "Parse and analyze structured log files",
			InputType:   "Text", OutputType: "JSON",
			Deterministic: true,
		},
		{
			Name: "TaskPlanning", Version: 1,
			Aliases:     []string{"plan", "schedule", "task", "规划", "任务"},
			Description: "Break down complex goals into executable task plans",
			InputType:   "Goal", OutputType: "Plan",
			Deterministic: false,
		},
	}
}

// FindCapability looks up a capability by name or alias.
// Returns nil if not found.
func FindCapability(name string) *CapabilityDef {
	for _, c := range BuiltinCapabilities() {
		if c.Name == name {
			return &c
		}
		for _, alias := range c.Aliases {
			if alias == name {
				return &c
			}
		}
	}
	return nil
}

// ToolCapabilityMap returns the mapping of tool names to their capabilities.
// This is the single source of truth for tool capabilities.
func ToolCapabilityMap() map[string][]string {
	return map[string][]string{
		"calculator":       {"Arithmetic", "Summation", "ExpressionEvaluation"},
		"hash_tool":        {"Hashing", "Base64"},
		"string_utils":     {"StringManipulation"},
		"regex_tool":       {"Regex"},
		"json_tools":       {"JSONProcessing"},
		"pdf_tool":         {"PDFParsing", "TextExtraction"},
		"web_search":       {"WebSearch"},
		"http_request":     {"HTTPRequest"},
		"web_scraper":      {"WebFetch", "WebSearch"},
		"id_generator":     {"IDGeneration"},
		"code_runner":      {"CodeExecution"},
		"embedding":        {"Embedding"},
		"datetime":         {"DateTime"},
		"data_transform":   {CapabilityDataTransform},
		"data_validation":  {CapabilityDataValidation},
		"log_analyzer":     {CapabilityLogAnalysis},
		"text_processor":   {"TextProcessing"},
		"task_planner":     {"TaskPlanning"},
		"memory_search":    {"MemorySearch"},
		"knowledge_search": {"KnowledgeSearch"},
	}
}
