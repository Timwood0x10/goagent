package memory

import (
	"math"
	"testing"
)

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name      string
		v1        []float64
		v2        []float64
		expected  float64
		tolerance float64
	}{
		{
			name:      "identical vectors",
			v1:        []float64{1, 2, 3},
			v2:        []float64{1, 2, 3},
			expected:  1.0,
			tolerance: 1e-10,
		},
		{
			name:      "orthogonal vectors",
			v1:        []float64{1, 0},
			v2:        []float64{0, 1},
			expected:  0.0,
			tolerance: 1e-10,
		},
		{
			name:      "opposite vectors",
			v1:        []float64{1, 1},
			v2:        []float64{-1, -1},
			expected:  -1.0,
			tolerance: 1e-10,
		},
		{
			name:      "different length vectors",
			v1:        []float64{1, 2},
			v2:        []float64{1, 2, 3},
			expected:  0.0,
			tolerance: 1e-10,
		},
		{
			name:      "zero vectors",
			v1:        []float64{0, 0},
			v2:        []float64{0, 0},
			expected:  0.0,
			tolerance: 1e-10,
		},
		{
			name:      "normalized vector 3-4-5",
			v1:        []float64{3, 4},
			v2:        []float64{6, 8},
			expected:  1.0,
			tolerance: 1e-10,
		},
		{
			name:      "cosine 60 degrees",
			v1:        []float64{1, 0},
			v2:        []float64{0.5, 0.86602540378},
			expected:  0.5,
			tolerance: 1e-10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cosineSimilarity(tt.v1, tt.v2)
			if math.Abs(result-tt.expected) > tt.tolerance {
				t.Errorf("cosineSimilarity() = %v, want %v", result, tt.expected)
			}
		})
	}
}
