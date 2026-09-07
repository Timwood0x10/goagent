package distillation

import (
	"testing"
)

func TestExtractUserProfile_English(t *testing.T) {
	extractor := NewExperienceExtractor()

	messages := []Message{
		{Role: "user", Content: "hello I'm Ken font-end programmer , like JS,TS,VUE,nice to met you"},
		{Role: "assistant", Content: "Hello Ken! Nice to meet you."},
	}

	experiences := extractor.ExtractExperiences(messages)

	if len(experiences) == 0 {
		t.Fatal("Expected to extract user profile, got none")
	}

	profile := experiences[0]
	if profile.Problem != "User profile information" {
		t.Errorf("Expected problem 'User profile information', got '%s'", profile.Problem)
	}

	if profile.ExtractionMethod != ExtractionDirect {
		t.Errorf("Expected extraction method '%s', got '%s'", ExtractionDirect, profile.ExtractionMethod)
	}

	if profile.Confidence < 0.8 {
		t.Errorf("Expected high confidence (>0.8), got %.2f", profile.Confidence)
	}

	t.Logf("Extracted profile: %s → %s", profile.Problem, profile.Solution)
}

func TestExtractUserProfile_Chinese(t *testing.T) {
	extractor := NewExperienceExtractor()

	messages := []Message{
		{Role: "user", Content: "你好，我叫小刚，我是前端开发工程师技术栈是JS,TS,VUE，不喜欢后端开发"},
		{Role: "assistant", Content: "你好小刚！很高兴认识你。"},
	}

	experiences := extractor.ExtractExperiences(messages)

	if len(experiences) == 0 {
		t.Fatal("Expected to extract user profile, got none")
	}

	profile := experiences[0]
	if profile.Problem != "User profile information" {
		t.Errorf("Expected problem 'User profile information', got '%s'", profile.Problem)
	}

	t.Logf("Extracted profile: %s → %s", profile.Problem, profile.Solution)
}

func TestExtractUserProfile_WithQuestions(t *testing.T) {
	extractor := NewExperienceExtractor()

	messages := []Message{
		{Role: "user", Content: "hello I'm Ken font-end programmer , like JS,TS,VUE,nice to met you"},
		{Role: "assistant", Content: "Hello Ken! Nice to meet you."},
		{Role: "user", Content: "what's the go-agent"},
		{Role: "assistant", Content: "go-agent is a general AI agent framework."},
		{Role: "user", Content: "what's coding standards with golang"},
		{Role: "assistant", Content: "Go coding standards include gofmt, go vet, etc."},
		{Role: "user", Content: "cool thanks"},
		{Role: "assistant", Content: "You're welcome!"},
	}

	experiences := extractor.ExtractExperiences(messages)

	if len(experiences) == 0 {
		t.Fatal("Expected to extract experiences, got none")
	}

	// Should extract user profile
	profileFound := false
	for _, exp := range experiences {
		if exp.Problem == "User profile information" {
			profileFound = true
			t.Logf("Found user profile: %s → %s", exp.Problem, exp.Solution)
			break
		}
	}

	if !profileFound {
		t.Error("Expected to find user profile in experiences")
	}

	t.Logf("Total experiences extracted: %d", len(experiences))
	for i, exp := range experiences {
		t.Logf("Experience %d: %s → %s (method: %s, confidence: %.2f)",
			i+1, exp.Problem, exp.Solution, exp.ExtractionMethod, exp.Confidence)
	}
}

func TestClassifier_IsUserProfile(t *testing.T) {
	classifier := NewMemoryClassifier()

	tests := []struct {
		name     string
		problem  string
		solution string
		expected bool
	}{
		{
			name:     "user profile with name",
			problem:  "User profile information",
			solution: "Name: Ken | Profession: font-end programmer | Skills: JS,TS,VUE",
			expected: true,
		},
		{
			name:     "self-introduction",
			problem:  "User profile information",
			solution: "hello I'm Ken font-end programmer , like JS,TS,VUE,nice to met you",
			expected: true,
		},
		{
			name:     "not a profile",
			problem:  "what's the go-agent",
			solution: "go-agent is a general AI agent framework",
			expected: false,
		},
		{
			name:     "developer profile",
			problem:  "User profile information",
			solution: "Name: John | Profession: developer | Skills: Python, JavaScript",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := classifier.isUserProfile(tt.problem, tt.solution)
			if result != tt.expected {
				t.Errorf("isUserProfile() = %v, expected %v", result, tt.expected)
			}
		})
	}
}
