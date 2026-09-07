package experience

import (
	"testing"
)

// TestNormalizedExperience_Validation tests that normalized experience validation works correctly.
func TestNormalizedExperience_Validation(t *testing.T) {
	tests := []struct {
		name    string
		exp     NormalizedExperience
		wantErr bool
	}{
		{
			name: "valid experience",
			exp: NormalizedExperience{
				ID:         "exp-1",
				StrategyID: "strategy-1",
				TaskType:   "code_review",
			},
			wantErr: false,
		},
		{
			name: "missing ID",
			exp: NormalizedExperience{
				StrategyID: "strategy-1",
				TaskType:   "code_review",
			},
			wantErr: true,
		},
		{
			name: "missing strategy ID",
			exp: NormalizedExperience{
				ID:       "exp-1",
				TaskType: "code_review",
			},
			wantErr: true,
		},
		{
			name: "missing task type",
			exp: NormalizedExperience{
				ID:         "exp-1",
				StrategyID: "strategy-1",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateExperience(tt.exp)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateExperience() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestExperienceStoreConfig_Defaults tests that config defaults are sensible.
func TestExperienceStoreConfig_Defaults(t *testing.T) {
	cfg := ExperienceStoreConfig{}

	if cfg.MaxSize != 0 {
		t.Errorf("default MaxSize should be 0, got %d", cfg.MaxSize)
	}
	if cfg.EnableIndexing {
		t.Error("default EnableIndexing should be false")
	}
	if cfg.RetentionDays != 0 {
		t.Errorf("default RetentionDays should be 0, got %d", cfg.RetentionDays)
	}
}
