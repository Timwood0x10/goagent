package embedding

import (
	"testing"
)

func TestBuildMemoryQuerySpec_Deterministic(t *testing.T) {
	s1 := BuildMemoryQuerySpec("create a REST API in Go", "test-model", 1, 128)
	s2 := BuildMemoryQuerySpec("create a REST API in Go", "test-model", 1, 128)

	if s1.Hash != s2.Hash {
		t.Errorf("same inputs should produce same hash: %s vs %s", s1.Hash, s2.Hash)
	}

	if s1.Text != "create a REST API in Go" {
		t.Errorf("unexpected text: %s", s1.Text)
	}

	if s1.Prefix != "query:" {
		t.Errorf("expected prefix query:, got %s", s1.Prefix)
	}
}

func TestBuildMemoryQuerySpec_DifferentQueryDiffers(t *testing.T) {
	s1 := BuildMemoryQuerySpec("query one", "test-model", 1, 128)
	s2 := BuildMemoryQuerySpec("query two", "test-model", 1, 128)

	if s1.Hash == s2.Hash {
		t.Error("different queries should produce different hashes")
	}
}

func TestBuildMemoryQuerySpec_DifferentModelDiffers(t *testing.T) {
	s1 := BuildMemoryQuerySpec("same query", "model-a", 1, 128)
	s2 := BuildMemoryQuerySpec("same query", "model-b", 1, 128)

	if s1.Hash == s2.Hash {
		t.Error("different models should produce different hashes")
	}

	if s1.Text != s2.Text {
		t.Errorf("text should be identical regardless of model")
	}
}

func TestBuildMemoryExperienceSpec_Deterministic(t *testing.T) {
	s1 := BuildMemoryExperienceSpec("knowledge", "How to create API", "Use Go with gin", "test-model", 1, 128)
	s2 := BuildMemoryExperienceSpec("knowledge", "How to create API", "Use Go with gin", "test-model", 1, 128)

	if s1.Hash != s2.Hash {
		t.Errorf("same inputs should produce same hash: %s vs %s", s1.Hash, s2.Hash)
	}

	expected := "MemoryType: knowledge\nProblem: How to create API\nSolution: Use Go with gin"
	if s1.Text != expected {
		t.Errorf("unexpected canonical text:\ngot:  %q\nwant: %q", s1.Text, expected)
	}

	if s1.Prefix != "memory:" {
		t.Errorf("expected prefix memory:, got %s", s1.Prefix)
	}
}

func TestBuildMemoryExperienceSpec_ReorderedFields(t *testing.T) {
	// The canonical text is always in a fixed order regardless of input order.
	s := BuildMemoryExperienceSpec("preference", "User prefers dark mode", "Set theme to dark", "test-model", 1, 128)

	if s.Kind != KindMemoryExperience {
		t.Errorf("expected kind memory_experience, got %s", s.Kind)
	}

	if s.Version != 1 {
		t.Errorf("expected version 1, got %d", s.Version)
	}

	if s.Hash == "" {
		t.Error("hash should not be empty")
	}
}

func TestBuildMemoryExperienceSpec_DifferentTypesDiffers(t *testing.T) {
	s1 := BuildMemoryExperienceSpec("knowledge", "same problem", "same solution", "test-model", 1, 128)
	s2 := BuildMemoryExperienceSpec("interaction", "same problem", "same solution", "test-model", 1, 128)

	if s1.Hash == s2.Hash {
		t.Error("different types should produce different hashes")
	}
}

func TestBuildMemoryExperienceSpec_DifferentVersionDiffers(t *testing.T) {
	s1 := BuildMemoryExperienceSpec("knowledge", "problem", "solution", "test-model", 1, 128)
	s2 := BuildMemoryExperienceSpec("knowledge", "problem", "solution", "test-model", 2, 128)

	if s1.Hash == s2.Hash {
		t.Error("different versions should produce different hashes")
	}
}

func TestComputeHash_Deterministic(t *testing.T) {
	h1 := computeHash(KindMemoryQuery, "query:", "hello", "model", 1, 128)
	h2 := computeHash(KindMemoryQuery, "query:", "hello", "model", 1, 128)

	if h1 != h2 {
		t.Errorf("same inputs should produce same hash: %s vs %s", h1, h2)
	}
}
