package evolution

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

const (
	maxPromptTemplateLen = 10000
	maxMutationDescLen   = 500
	maxParamsCount       = 20
	maxParamKeyLen       = 100
	maxParamValueSize    = 10000
)

// ValidateStrategySize checks a mutation.Strategy against size limits.
// Returns an error if any field exceeds its maximum allowed size.
//
// Args:
//   - s: the strategy to validate (must not be nil).
//
// Returns:
//   - error: non-nil if the strategy exceeds size limits.
func ValidateStrategySize(s *mutation.Strategy) error {
	if s == nil {
		return errors.New("strategy must not be nil")
	}

	if len(s.PromptTemplate) > maxPromptTemplateLen {
		return fmt.Errorf("prompt template too long: %d bytes (max %d)",
			len(s.PromptTemplate), maxPromptTemplateLen)
	}

	if len(s.MutationDesc) > maxMutationDescLen {
		return fmt.Errorf("mutation description too long: %d bytes (max %d)",
			len(s.MutationDesc), maxMutationDescLen)
	}

	if len(s.Params) > maxParamsCount {
		return fmt.Errorf("too many params: %d (max %d)",
			len(s.Params), maxParamsCount)
	}

	if len(s.Name) > 500 {
		return fmt.Errorf("strategy name too long: %d bytes (max 500)",
			len(s.Name))
	}

	if len(s.ID) > 255 {
		return fmt.Errorf("strategy ID too long: %d bytes (max 255)",
			len(s.ID))
	}

	for k, v := range s.Params {
		if len(k) > maxParamKeyLen {
			return fmt.Errorf("param key too long: %q (%d bytes, max %d)",
				k, len(k), maxParamKeyLen)
		}
		if vs, ok := v.(string); ok && len(vs) > maxParamValueSize {
			return fmt.Errorf("param %q value too long: %d bytes (max %d)",
				k, len(vs), maxParamValueSize)
		}
		// Check serialized size for complex types.
		jsonBytes, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("param %q cannot be serialized: %w", k, err)
		}
		if len(jsonBytes) > maxParamValueSize {
			return fmt.Errorf("param %q serialized too long: %d bytes (max %d)",
				k, len(jsonBytes), maxParamValueSize)
		}
	}

	if strings.Contains(s.ID, "..") || strings.Contains(s.ID, "/") {
		return fmt.Errorf("strategy ID contains invalid characters: %q", s.ID)
	}

	return nil
}
