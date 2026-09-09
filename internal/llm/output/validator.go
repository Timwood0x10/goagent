package output

import (
	"errors"
	"fmt"
	"math"
	"reflect" // used for comparing arbitrary values via reflect.DeepEqual in validation
	"regexp"
	"sync"
	"unicode/utf8"

	"github.com/Timwood0x10/ares/internal/core/models"
)

// Validator validates data against schemas.
type Validator struct {
	mu               sync.RWMutex
	customValidators map[string]ValidatorFunc
	schemaType       string   // Schema type for validation (e.g., "default", "travel", "custom")
	regexCache       sync.Map // Cache compiled regex patterns
}

// ValidatorFunc is a custom validation function.
type ValidatorFunc func(interface{}) error

// ValidatorOption is a functional option for Validator.
type ValidatorOption func(*Validator)

// WithSchemaType sets the schema type for validation.
func WithSchemaType(schemaType string) ValidatorOption {
	return func(v *Validator) {
		v.schemaType = schemaType
	}
}

// NewValidator creates a new Validator.
func NewValidator(opts ...ValidatorOption) *Validator {
	v := &Validator{
		customValidators: make(map[string]ValidatorFunc),
		schemaType:       "default", // default schema type
	}

	// Apply options
	for _, opt := range opts {
		opt(v)
	}

	v.registerDefaults()

	return v
}

// registerDefaults registers built-in validators.
func (v *Validator) registerDefaults() {
	v.RegisterValidator("string", v.validateString)
	v.RegisterValidator("number", v.validateNumber)
	v.RegisterValidator("integer", v.validateInteger)
	v.RegisterValidator("boolean", v.validateBoolean)
	v.RegisterValidator("array", v.validateArray)
	v.RegisterValidator("object", v.validateObject)
}

// RegisterValidator registers a custom validator.
func (v *Validator) RegisterValidator(name string, fn ValidatorFunc) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.customValidators[name] = fn
}

// Validate validates data against a schema.
func (v *Validator) Validate(data interface{}, schema *Schema) error {
	if schema == nil {
		return nil
	}

	return v.validateValue(data, schema, "root")
}

//nolint:gocyclo // Recursive schema validation with multiple type checks
func (v *Validator) validateValue(data interface{}, schema *Schema, path string) error {
	// Handle null
	if data == nil {
		if schema.Nullable {
			return nil
		}
		return fmt.Errorf("%s: value is null", path)
	}

	// Type validation
	if schema.Type != "" {
		if err := v.validateType(data, schema.Type, path); err != nil {
			return err
		}
	}

	// Enum validation
	if len(schema.Enum) > 0 {
		if err := v.validateEnum(data, schema, path); err != nil {
			return err
		}
	}

	// String-specific validations. asString (not a bare assertion) so that
	// named string types such as models.Occasion get their MinLength /
	// MaxLength / Pattern constraints checked too.
	if str, ok := asString(data); ok {
		// Count runes, not bytes: CJK characters are 3-4 bytes in UTF-8
		// and a byte-based length check would reject valid short strings.
		runeLen := utf8.RuneCountInString(str)
		if schema.MinLength != nil && runeLen < *schema.MinLength {
			return fmt.Errorf("%s: length %d is less than minimum %d", path, runeLen, *schema.MinLength)
		}
		if schema.MaxLength != nil && runeLen > *schema.MaxLength {
			return fmt.Errorf("%s: length %d exceeds maximum %d", path, runeLen, *schema.MaxLength)
		}
		if schema.Pattern != "" {
			// Use cached regex pattern for better performance.
			// regexp.Compile is used instead of MustCompile to avoid panic on invalid patterns.
			if re, ok := v.regexCache.Load(schema.Pattern); ok {
				regex := re.(*regexp.Regexp)
				if !regex.MatchString(str) {
					return fmt.Errorf("%s: does not match pattern %s", path, schema.Pattern)
				}
			} else {
				compiled, err := regexp.Compile(schema.Pattern)
				if err != nil {
					return fmt.Errorf("%s: invalid pattern %q: %w", path, schema.Pattern, err)
				}
				actual, _ := v.regexCache.LoadOrStore(schema.Pattern, compiled)
				regex := actual.(*regexp.Regexp)
				if !regex.MatchString(str) {
					return fmt.Errorf("%s: does not match pattern %s", path, schema.Pattern)
				}
			}
		}
	}

	// Number-specific validations
	if num, ok := toFloat64(data); ok {
		if schema.Minimum != nil && num < *schema.Minimum {
			return fmt.Errorf("%s: value %f is less than minimum %f", path, num, *schema.Minimum)
		}
		if schema.Maximum != nil && num > *schema.Maximum {
			return fmt.Errorf("%s: value %f exceeds maximum %f", path, num, *schema.Maximum)
		}
	}

	// Array validation
	if arr, ok := data.([]interface{}); ok {
		if schema.MinItems != nil && len(arr) < *schema.MinItems {
			return fmt.Errorf("%s: item count %d is less than minimum %d", path, len(arr), *schema.MinItems)
		}
		if schema.MaxItems != nil && len(arr) > *schema.MaxItems {
			return fmt.Errorf("%s: item count %d exceeds maximum %d", path, len(arr), *schema.MaxItems)
		}
		if schema.Items != nil {
			for i, item := range arr {
				if err := v.validateValue(item, schema.Items, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	}

	// Object validation
	if obj, ok := data.(map[string]interface{}); ok {
		// Required fields
		for _, required := range schema.Required {
			if _, exists := obj[required]; !exists {
				return fmt.Errorf("%s: missing required field %s", path, required)
			}
		}
		// Properties validation
		if schema.Properties != nil {
			for propName, propSchema := range schema.Properties {
				if val, exists := obj[propName]; exists {
					if err := v.validateValue(val, propSchema, fmt.Sprintf("%s.%s", path, propName)); err != nil {
						return err
					}
				}
			}
		}
	}

	// Custom validator
	if schema.Type != "" {
		v.mu.RLock()
		fn, exists := v.customValidators[schema.Type]
		v.mu.RUnlock()
		if exists {
			if err := fn(data); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
		}
	}

	return nil
}

func (v *Validator) validateType(data interface{}, expectedType string, path string) error {
	switch expectedType {
	case schemaTypeString:
		if _, ok := asString(data); !ok {
			return fmt.Errorf("%s: expected string, got %T", path, data)
		}
	case schemaTypeNumber:
		if _, ok := toFloat64(data); !ok {
			return fmt.Errorf("%s: expected number, got %T", path, data)
		}
	case schemaTypeInteger:
		if _, ok := toInt64(data); !ok {
			return fmt.Errorf("%s: expected integer, got %T", path, data)
		}
	case "boolean":
		if _, ok := asBool(data); !ok {
			return fmt.Errorf("%s: expected boolean, got %T", path, data)
		}
	case schemaTypeArray:
		_, ok := data.([]interface{})
		if !ok {
			return fmt.Errorf("%s: expected array, got %T", path, data)
		}
	case schemaTypeObject:
		_, ok := data.(map[string]interface{})
		if !ok {
			return fmt.Errorf("%s: expected object, got %T", path, data)
		}
	}
	return nil
}

// canonicalValue reduces a value to a plain primitive of the same kind so that
// a named type compares equal to its underlying type.
//
// Needed because reflect.DeepEqual compares DYNAMIC TYPES: models.Occasion("casual")
// is not DeepEqual to the enum entry "casual" (a plain string), so every
// named-type value was rejected by validateEnum even once the scalar type
// checks accepted it.
func canonicalValue(v interface{}) interface{} {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String:
		return rv.String()
	case reflect.Bool:
		return rv.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Uint()
	case reflect.Float32, reflect.Float64:
		return rv.Float()
	}
	return v
}

func (v *Validator) validateEnum(value interface{}, schema *Schema, path string) error {
	// Empty means "not set" only where the schema says so (Schema.AllowEmpty).
	// It used to be an unconditional bypass, which also let a REQUIRED enum field
	// validate with "" because Required never inspects the value.
	if schema.AllowEmpty {
		if s, ok := asString(value); ok && s == "" {
			return nil
		}
	}

	want := canonicalValue(value)
	for _, e := range schema.Enum {
		if reflect.DeepEqual(want, canonicalValue(e)) {
			return nil
		}
	}
	return fmt.Errorf("%s: value %v is not in enum %v", path, value, schema.Enum)
}

func (v *Validator) validateString(value interface{}) error {
	if _, ok := asString(value); !ok {
		return errors.New("expected string")
	}
	return nil
}

func (v *Validator) validateNumber(value interface{}) error {
	_, ok := toFloat64(value)
	if !ok {
		return errors.New("expected number")
	}
	return nil
}

func (v *Validator) validateInteger(value interface{}) error {
	_, ok := toInt64(value)
	if !ok {
		return errors.New("expected integer")
	}
	return nil
}

func (v *Validator) validateBoolean(value interface{}) error {
	// asBool, not a bare value.(bool): custom validators run after validateType,
	// so a bare assertion would accept a named bool type at the type check and
	// then reject it here.
	if _, ok := asBool(value); !ok {
		return errors.New("expected boolean")
	}
	return nil
}

// validateArray deliberately keeps the exact []interface{} assertion rather than
// accepting any slice kind: JSON decoding and ValidateRecommendResult only ever
// produce that type, and validateValue's array branch asserts it exactly too.
func (v *Validator) validateArray(value interface{}) error {
	_, ok := value.([]interface{})
	if !ok {
		return errors.New("expected array")
	}
	return nil
}

// validateObject keeps the exact map[string]interface{} assertion for the same
// reason as validateArray.
func (v *Validator) validateObject(value interface{}) error {
	_, ok := value.(map[string]interface{})
	if !ok {
		return errors.New("expected object")
	}
	return nil
}

// ValidateRecommendResult validates RecommendResult against schema.
func (v *Validator) ValidateRecommendResult(result *models.RecommendResult) error {
	if result == nil {
		return errors.New("result is nil")
	}

	// Convert RecommendResult items to []interface{} for validation
	itemsInterface := make([]interface{}, len(result.Items))
	for i, item := range result.Items {
		if item == nil {
			return fmt.Errorf("items[%d] is nil", i)
		}
		// Convert AgentPreferences []string to []interface{}
		agentPreferencesInterface := make([]interface{}, len(item.AgentPreferences))
		for j, s := range item.AgentPreferences {
			agentPreferencesInterface[j] = string(s)
		}
		// Convert Colors []string to []interface{}
		colorsInterface := make([]interface{}, len(item.Colors))
		for j, c := range item.Colors {
			colorsInterface[j] = c
		}

		itemsInterface[i] = map[string]interface{}{
			keyItemID:           item.ItemID,
			keyName:             item.Name,
			keyCategory:         item.Category,
			keyDescription:      item.Description,
			keyPrice:            item.Price,
			"url":               item.URL,
			keyImageURL:         item.ImageURL,
			"agent_preferences": agentPreferencesInterface,
			keyColors:           colorsInterface,
			keyMatchReason:      item.MatchReason,
			keyBrand:            item.Brand,
			keyMetadata:         item.Metadata,
		}
	}

	// Convert RecommendResult to map[string]interface{} for validation
	resultMap := map[string]interface{}{
		keySessionID:  result.SessionID,
		keyUserID:     result.UserID,
		keyItems:      itemsInterface,
		keyReason:     result.Reason,
		"total_price": result.TotalPrice,
		keyMatchScore: result.MatchScore,
		"occasion":    result.Occasion,
		"season":      result.Season,
		keyMetadata:   result.Metadata,
	}

	schema := v.getSchema()
	return v.Validate(resultMap, schema)
}

// getSchema returns the appropriate schema based on schemaType.
func (v *Validator) getSchema() *Schema {
	switch v.schemaType {
	case "travel":
		return GetTravelResultSchema()
	case "default":
		return GetRecommendResultSchema()
	default:
		return GetRecommendResultSchema()
	}
}

// GetTravelResultSchema returns the schema for travel recommendation results.
func GetTravelResultSchema() *Schema {
	return &Schema{
		Type: "object",
		Properties: map[string]*Schema{
			"session_id": {
				Type: "string",
			},
			"user_id": {
				Type: "string",
			},
			"items": {
				Type:     "array",
				MinItems: pointerToInt(1),
				Items:    GetTravelItemSchema(),
			},
			"reason": {
				Type: "string",
			},
			"total_price": {
				Type:    "number",
				Minimum: pointerToFloat64(0),
			},
			"match_score": {
				Type:    "number",
				Minimum: pointerToFloat64(0),
				Maximum: pointerToFloat64(1),
			},
			"metadata": {
				Type: "object",
			},
		},
		Required: []string{"items"},
	}
}

// GetTravelItemSchema returns the schema for travel recommendation items.
func GetTravelItemSchema() *Schema {
	return &Schema{
		Type: "object",
		Properties: map[string]*Schema{
			"item_id": {
				Type:      "string",
				MinLength: pointerToInt(1),
			},
			"category": {
				Type: "string",
				Enum: []interface{}{
					"destination", "food", "hotel", "itinerary", "transport", "activity",
				},
			},
			"name": {
				Type:      "string",
				MinLength: pointerToInt(1),
			},
			"brand": {
				Type: "string",
			},
			"description": {
				Type: "string",
			},
			"price": {
				Type:    "number",
				Minimum: pointerToFloat64(0),
			},
			"url": {
				Type:   "string",
				Format: "uri",
			},
			"image_url": {
				Type:   "string",
				Format: "uri",
			},
			"style": {
				Type:  "array",
				Items: &Schema{Type: "string"},
			},
			"colors": {
				Type:  "array",
				Items: &Schema{Type: "string"},
			},
			"match_reason": {
				Type: "string",
			},
			"metadata": {
				Type: "object",
			},
		},
		Required: []string{"item_id", "name", "category"},
	}
}

// Helper functions.
//
// Named-type handling (why these use reflect.Kind instead of type switches):
// a plain `v.(string)` / `v.(float64)` assertion matches only the EXACT dynamic
// type, so a named domain type with the right underlying kind — models.Occasion
// ("type Occasion string"), models.StyleTag, or a hypothetical
// "type Score float64" — was rejected as the wrong type. That made the
// validator fail on valid domain objects, and the symptom was papered over for
// a long time by a t.Skip in output_test.go. Comparing reflect.Kind keeps the
// check structural, which is what a JSON-schema "string"/"number" means.
//
// Strings are still rejected by the numeric converters: reflect.String is not a
// numeric kind, so the "reject ambiguous string→number conversion" rule holds.

// asString returns v's string value when v is a string or a named type whose
// underlying kind is string.
func asString(v interface{}) (string, bool) {
	if v == nil {
		return "", false
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.String {
		return "", false
	}
	return rv.String(), true
}

// asBool returns v's bool value when v is a bool or a named type whose
// underlying kind is bool.
func asBool(v interface{}) (bool, bool) {
	if v == nil {
		return false, false
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Bool {
		return false, false
	}
	return rv.Bool(), true
}

func toFloat64(v interface{}) (float64, bool) {
	if v == nil {
		return 0, false
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Float32, reflect.Float64:
		return rv.Float(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(rv.Uint()), true
	}
	// Every other kind — notably reflect.String — is rejected, preserving the
	// "no ambiguous string→number conversion" rule of the original switch.
	return 0, false
}

// int64OverflowBound is the first float64 that is out of int64 range: 2^63.
// int64's true maximum (2^63-1) is not representable as a float64, so any
// expression aiming at it rounds up to exactly 2^63 — which is why the
// comparison below must be strict.
const int64OverflowBound = float64(1 << 63)

// minInt64AsFloat is the true int64 lower bound (-2^63) and IS exactly
// representable, so it stays an inclusive bound. The original code used
// `^int64(0)`, which evaluates to -1 rather than MinInt64, so huge negative
// floats slipped through and were silently truncated.
const minInt64AsFloat = float64(-1 << 63)

func toInt64(v interface{}) (int64, bool) {
	if v == nil {
		return 0, false
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		u := rv.Uint()
		if u > ^uint64(0)>>1 {
			return 0, false // exceeds int64 range
		}
		return int64(u), true
	case reflect.Float32, reflect.Float64:
		f := rv.Float()
		// Range-check strictly before narrowing: int64(f) is undefined per the Go
		// spec when f is out of range, and 2^63 itself is already out of range.
		// NaN and ±Inf fail both comparisons, so they are rejected here.
		if f >= int64OverflowBound || f < minInt64AsFloat {
			return 0, false
		}
		// JSON Schema "integer" accepts 1.0 but not 1.5. Truncating silently would
		// make dirty data look valid, so require no fractional part.
		if f != math.Trunc(f) {
			return 0, false
		}
		return int64(f), true
	}
	return 0, false
}

// Validator errors.
var (
	ErrValidationFailed = errors.New("validation failed")
)
