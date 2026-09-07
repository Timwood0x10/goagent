// Package distillation provides memory distillation functionality for agent experience extraction.
package distillation

// TestSet defines test cases for memory distillation validation.
// This test set validates the distillation engine's ability to correctly extract
// or reject memories based on conversation content.
type TestSet struct {
	Name          string
	Description   string
	Messages      []Message
	ShouldExtract bool
	ExpectedCount int
	ExpectedTypes []MemoryType
	Reason        string
}

// testSetData contains the comprehensive test cases for memory distillation validation.
var testSetData = []TestSet{
	// ==================== SHOULD EXTRACT ====================
	{
		Name:        "Docker Container Error",
		Description: "Direct problem-solution pair about Docker container",
		Messages: []Message{
			{Role: "user", Content: "docker container won't start"},
			{Role: "assistant", Content: "restart docker daemon"},
		},
		ShouldExtract: true,
		ExpectedCount: 1,
		ExpectedTypes: []MemoryType{MemoryInteraction},
		Reason:        "Contains error keyword and solution",
	},
	{
		Name:        "Cross-Turn Solution",
		Description: "Solution appears after clarification questions",
		Messages: []Message{
			{Role: "user", Content: "docker container won't start"},
			{Role: "assistant", Content: "can you share the logs?"},
			{Role: "user", Content: "error: connection refused"},
			{Role: "assistant", Content: "restart docker daemon"},
		},
		ShouldExtract: true,
		ExpectedCount: 2,
		ExpectedTypes: []MemoryType{MemoryInteraction, MemoryInteraction},
		Reason:        "Cross-turn extraction should capture both pairs",
	},
	{
		Name:        "User Preference",
		Description: "User expresses preference for Go language",
		Messages: []Message{
			{Role: "user", Content: "I prefer Go over Python"},
			{Role: "assistant", Content: "Noted, I'll use Go examples"},
		},
		ShouldExtract: false,
		ExpectedCount: 0,
		ExpectedTypes: []MemoryType{},
		Reason:        "Preference statement is not a problem, current system only extracts problem-solution pairs",
	},
	{
		Name:        "Platform Fact",
		Description: "User's platform information",
		Messages: []Message{
			{Role: "user", Content: "What OS am I using?"},
			{Role: "assistant", Content: "You are using macOS"},
		},
		ShouldExtract: false,
		ExpectedCount: 0,
		ExpectedTypes: []MemoryType{},
		Reason:        "Simple Q&A has low importance score below threshold",
	},
	{
		Name:        "Multiple Problems",
		Description: "Multiple distinct problems in conversation",
		Messages: []Message{
			{Role: "user", Content: "I have an error in my code"},
			{Role: "assistant", Content: "Check the syntax on line 10"},
			{Role: "user", Content: "how do I fix the database connection?"},
			{Role: "assistant", Content: "Update the connection string"},
		},
		ShouldExtract: true,
		ExpectedCount: 2,
		ExpectedTypes: []MemoryType{MemoryInteraction, MemoryInteraction},
		Reason:        "Multiple problems with solutions",
	},
	{
		Name:        "Complex Solution",
		Description: "Detailed solution with action verbs",
		Messages: []Message{
			{Role: "user", Content: "I have a memory leak in my application"},
			{Role: "assistant", Content: "Use pprof to identify the leak, then fix the goroutine not being closed, and finally restart the service"},
		},
		ShouldExtract: true,
		ExpectedCount: 1,
		ExpectedTypes: []MemoryType{MemoryInteraction},
		Reason:        "Contains error and actionable solution",
	},
	{
		Name:        "Rule Extraction",
		Description: "Coding standard or rule",
		Messages: []Message{
			{Role: "user", Content: "What are the project coding standards?"},
			{Role: "assistant", Content: "Follow the Google Go style guide and use golangci-lint"},
		},
		ShouldExtract: true,
		ExpectedCount: 1,
		ExpectedTypes: []MemoryType{MemoryProfile},
		Reason:        "Contains configuration and rules",
	},

	// ==================== SHOULD NOT EXTRACT ====================
	{
		Name:        "Casual Acknowledgment",
		Description: "Simple acknowledgment without problem",
		Messages: []Message{
			{Role: "user", Content: "ok"},
			{Role: "assistant", Content: "You're welcome!"},
		},
		ShouldExtract: false,
		ExpectedCount: 0,
		ExpectedTypes: []MemoryType{},
		Reason:        "No problem or question detected",
	},
	{
		Name:        "Code Block",
		Description: "Conversation contains code block",
		Messages: []Message{
			{Role: "user", Content: "show me an example"},
			{Role: "assistant", Content: "```go func main() { println(\"hello\") } ```"},
		},
		ShouldExtract: false,
		ExpectedCount: 0,
		ExpectedTypes: []MemoryType{},
		Reason:        "Code block filtered out as noise",
	},
	{
		Name:        "Stacktrace",
		Description: "Conversation contains stack trace",
		Messages: []Message{
			{Role: "user", Content: "what is this error?"},
			{Role: "assistant", Content: "panic: runtime error: index out of range at main.go:123"},
		},
		ShouldExtract: false,
		ExpectedCount: 0,
		ExpectedTypes: []MemoryType{},
		Reason:        "Stacktrace filtered out as noise",
	},
	{
		Name:        "Log Message",
		Description: "Conversation contains log message",
		Messages: []Message{
			{Role: "user", Content: "what's happening?"},
			{Role: "assistant", Content: "[INFO] Application started successfully"},
		},
		ShouldExtract: false,
		ExpectedCount: 0,
		ExpectedTypes: []MemoryType{},
		Reason:        "Log message filtered out as noise",
	},
	{
		Name:        "Markdown Table",
		Description: "Conversation contains markdown table",
		Messages: []Message{
			{Role: "user", Content: "show me the config"},
			{Role: "assistant", Content: "| Key | Value |\n|-----|-------|\n| host | localhost |"},
		},
		ShouldExtract: false,
		ExpectedCount: 0,
		ExpectedTypes: []MemoryType{},
		Reason:        "Markdown table filtered out as noise",
	},
	{
		Name:        "Too Short",
		Description: "Message below minimum length threshold",
		Messages: []Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
		},
		ShouldExtract: false,
		ExpectedCount: 0,
		ExpectedTypes: []MemoryType{},
		Reason:        "Message too short, filtered as noise",
	},
	{
		Name:        "Sensitive Information",
		Description: "Conversation contains password",
		Messages: []Message{
			{Role: "user", Content: "how do I set the password?"},
			{Role: "assistant", Content: "Use mypassword123 for the database"},
		},
		ShouldExtract: false,
		ExpectedCount: 0,
		ExpectedTypes: []MemoryType{},
		Reason:        "Sensitive information filtered out by security filter",
	},
	{
		Name:        "Low Importance",
		Description: "Generic statement without problem",
		Messages: []Message{
			{Role: "user", Content: "tell me something interesting"},
			{Role: "assistant", Content: "Go is a statically typed language"},
		},
		ShouldExtract: false,
		ExpectedCount: 0,
		ExpectedTypes: []MemoryType{},
		Reason:        "Low importance score, filtered out",
	},
	{
		Name:        "Thanks Response",
		Description: "User thanking without follow-up problem",
		Messages: []Message{
			{Role: "user", Content: "thanks for the help"},
			{Role: "assistant", Content: "You're welcome!"},
		},
		ShouldExtract: false,
		ExpectedCount: 0,
		ExpectedTypes: []MemoryType{},
		Reason:        "Casual acknowledgment filtered as noise",
	},
	{
		Name:        "Got It Response",
		Description: "User acknowledging without problem",
		Messages: []Message{
			{Role: "user", Content: "got it, thanks"},
			{Role: "assistant", Content: "Glad I could help"},
		},
		ShouldExtract: false,
		ExpectedCount: 0,
		ExpectedTypes: []MemoryType{},
		Reason:        "Casual acknowledgment filtered as noise",
	},
}

// GetTestSet returns the comprehensive test set for memory distillation.
func GetTestSet() []TestSet {
	return testSetData
}

// RunTestSet executes the test set and returns validation results.
func RunTestSet(extractor *ExperienceExtractor, classifier *MemoryClassifier, scorer *ImportanceScorer) []TestResult {
	testSet := GetTestSet()
	results := make([]TestResult, 0, len(testSet))

	noiseFilter := NewNoiseFilter()

	for _, test := range testSet {
		result := TestResult{
			Name:       test.Name,
			ShouldPass: test.ShouldExtract,
			ActualPass: false,
			Expected:   test.ExpectedCount,
			Actual:     0,
			Match:      false,
		}

		// Extract experiences
		experiences := extractor.ExtractExperiences(test.Messages)

		// Filter by noise and importance
		var filteredExperiences []Experience
		for _, exp := range experiences {
			// Security filter
			if !SecurityFilter(exp.Problem) || !SecurityFilter(exp.Solution) {
				continue
			}

			// Noise filter for solution
			if noiseFilter.IsNoise(exp.Solution) {
				continue
			}

			// Classify and score
			memoryType := classifier.ClassifyMemory(&exp)
			score := scorer.ScoreMemory(memoryType, exp.Problem, exp.Solution)

			// Check if score meets minimum threshold
			if scorer.ShouldKeep(score) {
				exp.Confidence = score
				filteredExperiences = append(filteredExperiences, exp)
			}
		}

		result.Actual = len(filteredExperiences)

		// Check if should extract
		if test.ShouldExtract {
			result.ActualPass = len(filteredExperiences) > 0
		} else {
			result.ActualPass = len(filteredExperiences) == 0
		}

		result.Match = result.ActualPass

		results = append(results, result)
	}

	return results
}

// TestResult represents the result of a single test case.
type TestResult struct {
	Name       string
	ShouldPass bool
	ActualPass bool
	Expected   int
	Actual     int
	Match      bool
}
