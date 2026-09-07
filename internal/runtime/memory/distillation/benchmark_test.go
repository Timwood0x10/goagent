package distillation

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

// BenchmarkScoreMemory benchmarks the importance scoring
func BenchmarkScoreMemory(b *testing.B) {
	scorer := NewImportanceScorer()

	problems := []string{
		"How to fix the database connection error?",
		"The API is returning 500 errors",
		"Need to optimize the query performance",
		"User authentication is failing",
	}

	solutions := []string{
		"Check the connection string and restart the service",
		"Review the error logs and fix the null pointer exception",
		"Add indexes to the frequently queried columns",
		"Verify the JWT token configuration and update the secret key",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := range problems {
			scorer.ScoreMemory(MemoryKnowledge, problems[j], solutions[j])
		}
	}
}

// BenchmarkConflictDetection benchmarks conflict detection
func BenchmarkConflictDetection(b *testing.B) {
	resolver := NewConflictResolver(nil)

	// Generate test vectors
	vector1 := generateRandomVector(1024)
	vector2 := generateRandomVector(1024)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = resolver.cosineSimilarity(vector1, vector2)
	}
}

// BenchmarkNoiseFilter benchmarks noise filtering
func BenchmarkNoiseFilter(b *testing.B) {
	filter := NewNoiseFilter()

	testTexts := []string{
		"Here is the solution to fix the error: restart the service",
		"ok",
		"thanks",
		"The problem is caused by incorrect configuration",
		"```python\nprint('hello')\n```",
		"Exception: NullPointerException at line 42",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, text := range testTexts {
			filter.IsNoise(text)
		}
	}
}

// BenchmarkMemoryClassification benchmarks memory classification
func BenchmarkMemoryClassification(b *testing.B) {
	classifier := NewMemoryClassifier()

	experiences := []*Experience{
		{Problem: "How to fix the error?", Solution: "Restart the service"},
		{Problem: "I prefer dark mode", Solution: "Set theme to dark"},
		{Problem: "User profile information", Solution: "Name: John, Role: Developer"},
		{Problem: "System configuration", Solution: "Set timeout to 30 seconds"},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, exp := range experiences {
			classifier.ClassifyMemory(exp)
		}
	}
}

// BenchmarkExperienceExtraction benchmarks experience extraction
func BenchmarkExperienceExtraction(b *testing.B) {
	extractor := NewExperienceExtractor()

	messages := generateTestMessages(50)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = extractor.ExtractExperiences(messages)
	}
}

// BenchmarkTopNFilter benchmarks Top-N filtering
func BenchmarkTopNFilter(b *testing.B) {
	scorer := NewImportanceScorer()

	// Generate test experiences
	experiences := make([]Experience, 100)
	for i := range experiences {
		experiences[i] = Experience{
			Problem:    fmt.Sprintf("Problem %d", i),
			Solution:   fmt.Sprintf("Solution %d with detailed content", i),
			Confidence: rand.Float64(),
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = scorer.TopNFilter(experiences, 10)
	}
}

// Helper functions

func generateTestMessages(count int) []Message {
	messages := make([]Message, count)

	problems := []string{
		"How do I fix the database connection error?",
		"The API is returning 500 internal server errors",
		"Need to optimize the slow query performance",
		"User authentication is failing with invalid token",
		"The application is crashing on startup",
	}

	solutions := []string{
		"Check the connection string configuration and restart the database service",
		"Review the error logs and fix the null pointer exception in the handler",
		"Add indexes to the frequently queried columns and optimize the JOIN operations",
		"Verify the JWT token configuration and update the secret key in the config file",
		"Check the dependency injection setup and ensure all required services are registered",
	}

	for i := range messages {
		if i%2 == 0 {
			messages[i] = Message{
				Role:    "user",
				Content: problems[i%len(problems)],
			}
		} else {
			messages[i] = Message{
				Role:    "assistant",
				Content: solutions[i%len(solutions)],
			}
		}
	}

	return messages
}

func generateRandomVector(dim int) []float64 {
	vector := make([]float64, dim)
	for i := range vector {
		vector[i] = rand.Float64()
	}
	return vector
}

// BenchmarkMemoryOperations benchmarks memory operations
func BenchmarkMemoryOperations(b *testing.B) {
	b.Run("Create", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = Memory{
				ID:         fmt.Sprintf("mem-%d", i),
				Type:       MemoryKnowledge,
				Content:    "Test content",
				Importance: 0.8,
				Source:     "test",
				CreatedAt:  time.Now(),
				Metadata:   map[string]interface{}{"key": "value"},
			}
		}
	})

	b.Run("Classification", func(b *testing.B) {
		classifier := NewMemoryClassifier()
		exp := &Experience{
			Problem:  "Test problem",
			Solution: "Test solution",
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = classifier.ClassifyMemory(exp)
		}
	})
}

// BenchmarkStringOperations benchmarks string operations in distillation
func BenchmarkStringOperations(b *testing.B) {
	b.Run("Format", func(b *testing.B) {
		exp := &Experience{
			Problem:  "Test problem",
			Solution: "Test solution",
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = FormatExperience(exp)
		}
	})
}
