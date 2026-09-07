package scoreutil

import (
	"math"
	"testing"
)

// TestClampUnit validates that ClampUnit bounds values to [0, 1] including the
// NaN and boundary edge cases (defensive programming).
func TestClampUnit(t *testing.T) {
	tests := []struct {
		name string
		in   float64
		want float64
	}{
		{name: "zero", in: 0, want: 0},
		{name: "one", in: 1, want: 1},
		{name: "mid range", in: 0.42, want: 0.42},
		{name: "negative clamped to zero", in: -0.5, want: 0},
		{name: "above one clamped to one", in: 1.5, want: 1},
		{name: "large positive clamped to one", in: 1000, want: 1},
		{name: "large negative clamped to zero", in: -1000, want: 0},
		{name: "nan treated as zero", in: math.NaN(), want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClampUnit(tt.in); got != tt.want {
				t.Errorf("ClampUnit(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
