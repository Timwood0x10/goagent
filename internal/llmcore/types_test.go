package llmcore

import (
	"testing"
	"time"
)

// TestBaseConfig tests BaseConfig construction and validation.
func TestBaseConfig(t *testing.T) {
	cfg := BaseConfig{
		RequestTimeout: 30 * time.Second,
		MaxRetries:     3,
		RetryDelay:     1 * time.Second,
	}
	if cfg.RequestTimeout != 30*time.Second {
		t.Errorf("RequestTimeout = %v, want 30s", cfg.RequestTimeout)
	}
	if cfg.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", cfg.MaxRetries)
	}
	if cfg.RetryDelay != 1*time.Second {
		t.Errorf("RetryDelay = %v, want 1s", cfg.RetryDelay)
	}
}

// TestBaseConfigZeroValues tests zero-value defaults.
func TestBaseConfigZeroValues(t *testing.T) {
	var cfg BaseConfig
	if cfg.RequestTimeout != 0 {
		t.Errorf("expected zero RequestTimeout")
	}
	if cfg.MaxRetries != 0 {
		t.Errorf("expected zero MaxRetries")
	}
	if cfg.RetryDelay != 0 {
		t.Errorf("expected zero RetryDelay")
	}
}

// TestMetadata tests Metadata operations.
func TestMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata Metadata
		key      string
		value    interface{}
	}{
		{"nil metadata", nil, "", nil},
		{"empty metadata", make(Metadata), "", nil},
		{"metadata with string value", Metadata{"key": "value"}, "key", "value"},
		{"metadata with int value", Metadata{"count": 42}, "count", 42},
		{"metadata with bool value", Metadata{"enabled": true}, "enabled", true},
		{"metadata with multiple values", Metadata{"key1": "value1", "key2": 123, "key3": true}, "", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.metadata != nil && tt.key != "" {
				if val, exists := tt.metadata[tt.key]; exists {
					if val != tt.value {
						t.Errorf("Metadata[%s] = %v, want %v", tt.key, val, tt.value)
					}
				}
			}
		})
	}
}
