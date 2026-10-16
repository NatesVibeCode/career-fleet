// Package schemasubset implements the fully typed JSON-Schema subset for typed
// output contracts.
//
// It is intentionally a stdlib-only leaf package so Work can import it without
// pulling the full Process runtime.
package schemasubset

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// TypedOutputContractSchemaVersion is the wire version for governed report contracts.
const TypedOutputContractSchemaVersion = "praxis.typed_output_contract.v1"

// TypedInputContractSchemaVersion is the wire version for receipt-edge consumer
// contracts. Input and output contracts deliberately share the same closed schema
// subset, but keep distinct identities so a producer declaration cannot be
// silently reused as consumer authority.
const TypedInputContractSchemaVersion = "praxis.typed_input_contract.v1"

// Mode selects which extension keywords and object-closing rules apply.
type Mode string

const (
	// ModeStrict is the typed-output contract: only core keywords,
	// objects declare additionalProperties:false or a typed value schema.
	ModeStrict Mode = "strict"
	// ModePack is the authoring pack/params schema: admits description and
	// model_visible on property schemas; every object value remains typed.
	ModePack Mode = "pack"
)

// Core keywords admitted in every mode.
var coreKeywords = map[string]bool{
	"type":                 true,
	"properties":           true,
	"required":             true,
	"additionalProperties": true,
	"enum":                 true,
	"items":                true,
	"minItems":             true,
	"maxItems":             true,
}

// Pack-only extension keywords (ModePack).
var packExtensionKeywords = map[string]bool{
	"description":   true,
	"model_visible": true,
}

// Violation is one path-addressed validation failure.
type Violation struct {
	Path    string `json:"path"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

func (v Violation) String() string {
	if v.Path == "" {
		return v.Message
	}
	if strings.HasPrefix(v.Message, v.Path) {
		return v.Message
	}
	return v.Path + ": " + v.Message
}

// Schema is a parsed, admitted closed-subset schema.
type Schema struct {
	raw  map[string]any
	mode Mode
}

// Parse admits a JSON schema document under the given mode.
// Unknown keywords fail closed with code schemasubset.unknown_keyword.
func Parse(body []byte, mode Mode) (*Schema, error) {
	if mode != ModeStrict && mode != ModePack {
		return nil, fmt.Errorf("schemasubset: unknown mode %q", mode)
	}
	value, err := DecodeJSON(body)
	if err != nil {
		return nil, fmt.Errorf("schemasubset: invalid JSON: %w", err)
	}
	raw, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schemasubset: schema must be an object")
	}
	return ParseMap(raw, mode)
}

// ParseMap admits an already-decoded schema object under the given mode.
// Top-level schema_version is stripped before admission (typed-output wire).
func ParseMap(schema map[string]any, mode Mode) (*Schema, error) {
	if mode != ModeStrict && mode != ModePack {
		return nil, fmt.Errorf("schemasubset: unknown mode %q", mode)
	}
	if schema == nil {
		return nil, fmt.Errorf("schemasubset: schema is required")
	}
	copy := copyMap(schema)
	delete(copy, "schema_version")
	if err := admitSchema(copy, "$", mode); err != nil {
		return nil, err
	}
	return &Schema{raw: copy, mode: mode}, nil
}

// Validate returns all path-addressed violations for value (empty = ok).
func (s *Schema) Validate(value any) []Violation {
	if s == nil {
		return []Violation{{Path: "$", Code: "schemasubset.nil", Message: "schema is nil"}}
	}
	return collectValue(s.raw, value, "$")
}

// ValidateAt is Validate with a custom root path (e.g. "output", "report_contract").
func (s *Schema) ValidateAt(value any, rootPath string) []Violation {
	if s == nil {
		return []Violation{{Path: rootPath, Code: "schemasubset.nil", Message: "schema is nil"}}
	}
	if rootPath == "" {
		rootPath = "$"
	}
	return collectValue(s.raw, value, rootPath)
}

// FirstError returns the first violation as an error, preserving message style
// used by typed-output paths (message already includes path).
func (s *Schema) FirstError(value any, rootPath string) error {
	issues := s.ValidateAt(value, rootPath)
	if len(issues) == 0 {
		return nil
	}
	return fmt.Errorf("%s", issues[0].Message)
}

// Digest returns "sha256:<hex>" over canonical JSON of body.
func Digest(body []byte) (string, error) {
	canonical, err := CanonicalJSON(body)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// CanonicalJSON normalizes JSON with sorted object keys (Go json.Marshal map order)
// and json.Number preservation via re-decode.
func CanonicalJSON(raw []byte) ([]byte, error) {
	value, err := DecodeJSON(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// ValidateTypedOutputContractSchema admits a governed report_contract schema.
// Requires schema_version == TypedOutputContractSchemaVersion.
// Error paths use "report_contract" prefix for legacy parity.
func ValidateTypedOutputContractSchema(schema map[string]any) error {
	if stringField(schema, "schema_version") != TypedOutputContractSchemaVersion {
		return fmt.Errorf("report_contract.schema_version must be %s", TypedOutputContractSchemaVersion)
	}
	copy := copyMap(schema)
	delete(copy, "schema_version")
	return admitSchema(copy, "report_contract", ModeStrict)
}

// ValidateTypedInputContractSchema admits a governed receipt-edge consumer schema.
func ValidateTypedInputContractSchema(schema map[string]any) error {
	if stringField(schema, "schema_version") != TypedInputContractSchemaVersion {
		return fmt.Errorf("consumer_input_contract.schema_version must be %s", TypedInputContractSchemaVersion)
	}
	copy := copyMap(schema)
	delete(copy, "schema_version")
	return admitSchema(copy, "consumer_input_contract", ModeStrict)
}

// ValidateProducerAssignableToConsumer proves that every value admitted by the
// producer schema is also admitted by the consumer schema. Both contracts must
// be closed and typed. This catches schema drift before a run starts; runtime
// validation still checks the actual selected value before copying it.
func ValidateProducerAssignableToConsumer(producer, consumer map[string]any) error {
	if err := ValidateTypedOutputContractSchema(producer); err != nil {
		return fmt.Errorf("producer: %w", err)
	}
	if err := ValidateTypedInputContractSchema(consumer); err != nil {
		return fmt.Errorf("consumer: %w", err)
	}
	producerCopy := copyMap(producer)
	consumerCopy := copyMap(consumer)
	delete(producerCopy, "schema_version")
	delete(consumerCopy, "schema_version")
	return validateAssignable(producerCopy, consumerCopy, "$", "$consumer")
}

func validateAssignable(producer, consumer map[string]any, producerPath, consumerPath string) error {
	producerType := stringField(producer, "type")
	consumerType := stringField(consumer, "type")
	if producerType != consumerType {
		return fmt.Errorf("%s type %q is not assignable to %s type %q", producerPath, producerType, consumerPath, consumerType)
	}
	if err := validateEnumAssignable(producer, consumer, producerPath, consumerPath); err != nil {
		return err
	}
	switch producerType {
	case "array":
		if err := validateArrayCardinalityAssignable(producer, consumer, producerPath, consumerPath); err != nil {
			return err
		}
		producerItems, _ := producer["items"].(map[string]any)
		consumerItems, _ := consumer["items"].(map[string]any)
		return validateAssignable(producerItems, consumerItems, producerPath+"[]", consumerPath+"[]")
	case "object":
		producerProperties, _ := producer["properties"].(map[string]any)
		consumerProperties, _ := consumer["properties"].(map[string]any)
		producerRequired, _ := stringList(producer["required"])
		consumerRequired, _ := stringList(consumer["required"])
		producerRequiredSet := make(map[string]bool, len(producerRequired))
		for _, field := range producerRequired {
			producerRequiredSet[field] = true
		}
		for _, field := range consumerRequired {
			if !producerRequiredSet[field] {
				return fmt.Errorf("%s requires field %q that %s does not guarantee", consumerPath, field, producerPath)
			}
		}
		producerFields := make([]string, 0, len(producerProperties))
		for field := range producerProperties {
			producerFields = append(producerFields, field)
		}
		sort.Strings(producerFields)
		for _, field := range producerFields {
			consumerRaw, exists := consumerProperties[field]
			if !exists {
				consumerRaw, exists = consumer["additionalProperties"].(map[string]any)
			}
			if !exists {
				return fmt.Errorf("%s may emit field %q rejected by closed %s", producerPath, field, consumerPath)
			}
			producerChild, _ := producerProperties[field].(map[string]any)
			consumerChild, _ := consumerRaw.(map[string]any)
			if err := validateAssignable(producerChild, consumerChild, producerPath+"."+field, consumerPath+"."+field); err != nil {
				return err
			}
		}
		if producerAdditional, ok := producer["additionalProperties"].(map[string]any); ok {
			consumerAdditional, ok := consumer["additionalProperties"].(map[string]any)
			if !ok {
				return fmt.Errorf("%s may emit arbitrary fields rejected by closed %s", producerPath, consumerPath)
			}
			if err := validateAssignable(producerAdditional, consumerAdditional, producerPath+".*", consumerPath+".*"); err != nil {
				return err
			}
			// A producer's dynamic key can also name a fixed consumer property.
			// That value must satisfy the fixed property's more specific schema.
			consumerFields := make([]string, 0, len(consumerProperties))
			for field := range consumerProperties {
				consumerFields = append(consumerFields, field)
			}
			sort.Strings(consumerFields)
			for _, field := range consumerFields {
				if _, fixed := producerProperties[field]; fixed {
					continue
				}
				child := consumerProperties[field].(map[string]any)
				if err := validateAssignable(producerAdditional, child, producerPath+".*", consumerPath+"."+field); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// validateArrayCardinalityAssignable proves the bounds emitted by producer are
// at least as narrow as the bounds required by consumer. An omitted producer
// lower bound means zero; an omitted upper bound means unbounded.
func validateArrayCardinalityAssignable(producer, consumer map[string]any, producerPath, consumerPath string) error {
	producerMin, producerHasMin, _ := arrayCardinalityBound(producer, "minItems")
	consumerMin, consumerHasMin, _ := arrayCardinalityBound(consumer, "minItems")
	if consumerHasMin && consumerMin > 0 && (!producerHasMin || producerMin < consumerMin) {
		producerDescription := "an omitted minItems"
		if producerHasMin {
			producerDescription = fmt.Sprintf("minItems %d", producerMin)
		}
		return fmt.Errorf("%s %s does not guarantee %s minItems %d", producerPath, producerDescription, consumerPath, consumerMin)
	}

	producerMax, producerHasMax, _ := arrayCardinalityBound(producer, "maxItems")
	consumerMax, consumerHasMax, _ := arrayCardinalityBound(consumer, "maxItems")
	if consumerHasMax && (!producerHasMax || producerMax > consumerMax) {
		producerDescription := "an omitted maxItems"
		if producerHasMax {
			producerDescription = fmt.Sprintf("maxItems %d", producerMax)
		}
		return fmt.Errorf("%s %s does not guarantee %s maxItems %d", producerPath, producerDescription, consumerPath, consumerMax)
	}
	return nil
}

func validateEnumAssignable(producer, consumer map[string]any, producerPath, consumerPath string) error {
	consumerEnum, consumerConstrained := consumer["enum"].([]any)
	if !consumerConstrained {
		return nil
	}
	producerEnum, producerConstrained := producer["enum"].([]any)
	if !producerConstrained {
		return fmt.Errorf("%s is unconstrained but %s declares an enum", producerPath, consumerPath)
	}
	for _, producerValue := range producerEnum {
		accepted := false
		for _, consumerValue := range consumerEnum {
			if enumValuesEqual(producerValue, consumerValue) {
				accepted = true
				break
			}
		}
		if !accepted {
			return fmt.Errorf("%s enum value %v is rejected by %s", producerPath, producerValue, consumerPath)
		}
	}
	return nil
}

// enumValuesEqual compares JSON values, not their decoder's Go number types.
// Collection order and scalar types remain significant. It deliberately does
// not coerce strings or round arbitrary-precision JSON numbers through float64.
func enumValuesEqual(a, b any) bool {
	switch left := a.(type) {
	case map[string]any:
		right, ok := b.(map[string]any)
		if !ok || (left == nil) != (right == nil) || len(left) != len(right) {
			return false
		}
		for key, value := range left {
			other, exists := right[key]
			if !exists || !enumValuesEqual(value, other) {
				return false
			}
		}
		return true
	case []any:
		right, ok := b.([]any)
		if !ok || (left == nil) != (right == nil) || len(left) != len(right) {
			return false
		}
		for i, value := range left {
			if !enumValuesEqual(value, right[i]) {
				return false
			}
		}
		return true
	case json.Number, int, int32, int64, float32, float64:
		coefficientA, exponentA, okA := enumNumber(a)
		coefficientB, exponentB, okB := enumNumber(b)
		return okA && okB && coefficientA == coefficientB && exponentA == exponentB
	default:
		return reflect.DeepEqual(a, b)
	}
}

// enumNumber represents a valid JSON number as coefficient * 10^exponent.
// Only the exponent itself is a big integer: expanding 1e999999999 would
// otherwise allocate memory proportional to its value rather than input size.
func enumNumber(value any) (coefficient, exponent string, ok bool) {
	switch value.(type) {
	case json.Number, int, int32, int64, float32, float64:
	default:
		return "", "", false
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", "", false
	}
	mantissa := string(raw)
	power := new(big.Int)
	if i := strings.IndexAny(mantissa, "eE"); i >= 0 {
		if _, valid := power.SetString(mantissa[i+1:], 10); !valid {
			return "", "", false
		}
		mantissa = mantissa[:i]
	}
	negative := strings.HasPrefix(mantissa, "-")
	mantissa = strings.TrimPrefix(mantissa, "-")
	if i := strings.IndexByte(mantissa, '.'); i >= 0 {
		power.Sub(power, big.NewInt(int64(len(mantissa)-i-1)))
		mantissa = mantissa[:i] + mantissa[i+1:]
	}
	mantissa = strings.TrimLeft(mantissa, "0")
	if mantissa == "" {
		return "0", "0", true
	}
	trimmed := strings.TrimRight(mantissa, "0")
	power.Add(power, big.NewInt(int64(len(mantissa)-len(trimmed))))
	if negative {
		trimmed = "-" + trimmed
	}
	return trimmed, power.String(), true
}

func admitSchema(schema map[string]any, path string, mode Mode) error {
	allowed := map[string]bool{}
	for k, v := range coreKeywords {
		allowed[k] = v
	}
	if mode == ModePack {
		for k, v := range packExtensionKeywords {
			allowed[k] = v
		}
	}
	for key := range schema {
		if !allowed[key] {
			return fmt.Errorf("%s contains unsupported schema keyword %q", path, key)
		}
	}
	// model_visible / description only meaningful on property schemas; if present
	// at non-leaf they are still allowed in pack mode but ignored by FieldFlag walk.
	if mode == ModePack {
		if v, ok := schema["model_visible"]; ok {
			if _, isBool := v.(bool); !isBool {
				return fmt.Errorf("%s.model_visible must be a boolean", path)
			}
		}
		if v, ok := schema["description"]; ok {
			if _, isString := v.(string); !isString {
				return fmt.Errorf("%s.description must be a string", path)
			}
		}
	}

	typeName := stringField(schema, "type")
	switch typeName {
	case "object", "array", "string", "boolean", "integer", "number", "null", "scalar":
	default:
		return fmt.Errorf("%s.type must be object, array, string, boolean, integer, number, null, or scalar", path)
	}
	if typeName != "array" {
		for _, forbidden := range []string{"minItems", "maxItems"} {
			if _, exists := schema[forbidden]; exists {
				return fmt.Errorf("%s.%s is not valid for type %s", path, forbidden, typeName)
			}
		}
	}
	if enum, exists := schema["enum"]; exists {
		values, ok := enum.([]any)
		if !ok || len(values) == 0 {
			return fmt.Errorf("%s.enum must be a non-empty array", path)
		}
		for i, value := range values {
			if !valueMatches(typeName, value) {
				return fmt.Errorf("%s.enum[%d] does not match type %s", path, i, typeName)
			}
		}
	}
	switch typeName {
	case "object":
		switch additional := schema["additionalProperties"].(type) {
		case bool:
			if additional {
				return fmt.Errorf("%s.additionalProperties must be false or a typed object schema", path)
			}
		case map[string]any:
			if additional == nil {
				return fmt.Errorf("%s.additionalProperties must be false or a typed object schema", path)
			}
			if err := admitSchema(additional, path+".additionalProperties", mode); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s.additionalProperties must be false or a typed object schema", path)
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			_, declared := schema["properties"]
			_, dictionary := schema["additionalProperties"].(map[string]any)
			if declared || !dictionary {
				return fmt.Errorf("%s.properties must be an object", path)
			}
		}
		required, ok := stringList(schema["required"])
		if !ok {
			return fmt.Errorf("%s.required must be a string array", path)
		}
		for _, field := range required {
			if _, exists := properties[field]; !exists {
				return fmt.Errorf("%s.required field %q is absent from properties", path, field)
			}
		}
		for field, raw := range properties {
			child, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.properties.%s must be an object schema", path, field)
			}
			if err := admitSchema(child, path+".properties."+field, mode); err != nil {
				return err
			}
		}
	case "array":
		minItems, hasMinItems, err := arrayCardinalityBound(schema, "minItems")
		if err != nil {
			return fmt.Errorf("%s.minItems must be a non-negative integer", path)
		}
		maxItems, hasMaxItems, err := arrayCardinalityBound(schema, "maxItems")
		if err != nil {
			return fmt.Errorf("%s.maxItems must be a non-negative integer", path)
		}
		if hasMinItems && hasMaxItems && minItems > maxItems {
			return fmt.Errorf("%s.minItems must not exceed %s.maxItems", path, path)
		}
		items, ok := schema["items"].(map[string]any)
		if !ok {
			return fmt.Errorf("%s.items must be an object schema", path)
		}
		if err := admitSchema(items, path+".items", mode); err != nil {
			return err
		}
	default:
		for _, forbidden := range []string{"properties", "required", "additionalProperties", "items"} {
			if _, exists := schema[forbidden]; exists {
				return fmt.Errorf("%s.%s is not valid for type %s", path, forbidden, typeName)
			}
		}
	}
	// Collection enum values must themselves satisfy the complete closed
	// schema. Keeping the enum preserves exact configured field-reference
	// arrays across release-to-Forge translation, including order/cardinality.
	if values, ok := schema["enum"].([]any); ok && (typeName == "array" || typeName == "object") {
		shape := copyMap(schema)
		delete(shape, "enum")
		for i, value := range values {
			if issues := collectValue(shape, value, path); len(issues) != 0 {
				return fmt.Errorf("%s.enum[%d] violates its schema: %s", path, i, issues[0].Message)
			}
		}
	}
	return nil
}

func collectValue(schema map[string]any, value any, path string) []Violation {
	typeName := stringField(schema, "type")
	if !valueMatches(typeName, value) {
		return []Violation{{
			Path: path, Code: "schemasubset.type_mismatch",
			Message: fmt.Sprintf("%s must be %s", path, typeName),
		}}
	}
	var issues []Violation
	if values, ok := schema["enum"].([]any); ok {
		matched := false
		for _, candidate := range values {
			if enumValuesEqual(candidate, value) {
				matched = true
				break
			}
		}
		if !matched {
			issues = append(issues, Violation{
				Path: path, Code: "schemasubset.enum",
				Message: fmt.Sprintf("%s must be one of enum values", path),
			})
			return issues
		}
	}
	switch typeName {
	case "object":
		object := value.(map[string]any)
		properties, _ := schema["properties"].(map[string]any)
		if properties == nil {
			properties = map[string]any{}
		}
		required, _ := stringList(schema["required"])
		for _, field := range required {
			if _, exists := object[field]; !exists {
				issues = append(issues, Violation{
					Path: path + "." + field, Code: "schemasubset.required",
					Message: fmt.Sprintf("%s missing required field %q", path, field),
				})
			}
		}
		unknown := make([]string, 0)
		for field := range object {
			if _, exists := properties[field]; !exists {
				unknown = append(unknown, field)
			}
		}
		additional, dynamic := schema["additionalProperties"].(map[string]any)
		if len(unknown) > 0 && !dynamic {
			sort.Strings(unknown)
			// Report the first unknown field in stable order.
			issues = append(issues, Violation{
				Path: path + "." + unknown[0], Code: "schemasubset.unknown_field",
				Message: fmt.Sprintf("%s contains unknown field %q", path, unknown[0]),
			})
			return issues
		}
		// Stable field order for multi-issue collection.
		fields := make([]string, 0, len(object))
		for field := range object {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		for _, field := range fields {
			child, ok := properties[field].(map[string]any)
			if !ok {
				issues = append(issues, collectValue(additional, object[field], path+"["+strconv.Quote(field)+"]")...)
				continue
			}
			issues = append(issues, collectValue(child, object[field], path+"."+field)...)
		}
	case "array":
		values := value.([]any)
		if minItems, hasMinItems, _ := arrayCardinalityBound(schema, "minItems"); hasMinItems && len(values) < minItems {
			issues = append(issues, Violation{
				Path: path, Code: "schemasubset.min_items",
				Message: fmt.Sprintf("%s must contain at least %d items", path, minItems),
			})
		}
		if maxItems, hasMaxItems, _ := arrayCardinalityBound(schema, "maxItems"); hasMaxItems && len(values) > maxItems {
			issues = append(issues, Violation{
				Path: path, Code: "schemasubset.max_items",
				Message: fmt.Sprintf("%s must contain at most %d items", path, maxItems),
			})
		}
		items, _ := schema["items"].(map[string]any)
		for i, child := range values {
			issues = append(issues, collectValue(items, child, fmt.Sprintf("%s[%d]", path, i))...)
		}
	}
	return issues
}

// arrayCardinalityBound returns an admitted array cardinality keyword. It is
// intentionally tolerant of the Go numeric forms used by programmatic schema
// construction while refusing fractional, negative, and overflowing values.
func arrayCardinalityBound(schema map[string]any, key string) (int, bool, error) {
	raw, exists := schema[key]
	if !exists {
		return 0, false, nil
	}
	value, ok := nonNegativeInt(raw)
	if !ok {
		return 0, true, fmt.Errorf("invalid non-negative integer")
	}
	return value, true, nil
}

func nonNegativeInt(raw any) (int, bool) {
	const maxInt = int64(^uint(0) >> 1)
	var value int64
	switch n := raw.(type) {
	case int:
		value = int64(n)
	case int32:
		value = int64(n)
	case int64:
		value = n
	case json.Number:
		parsed, err := strconv.ParseInt(n.String(), 10, 64)
		if err != nil {
			return 0, false
		}
		value = parsed
	case float32:
		parsed := float64(n)
		if math.Trunc(parsed) != parsed || parsed > float64(maxInt) || parsed < 0 {
			return 0, false
		}
		value = int64(parsed)
	case float64:
		if math.Trunc(n) != n || n > float64(maxInt) || n < 0 {
			return 0, false
		}
		value = int64(n)
	default:
		return 0, false
	}
	if value < 0 || value > maxInt {
		return 0, false
	}
	return int(value), true
}

func valueMatches(typeName string, value any) bool {
	switch typeName {
	case "null":
		return value == nil
	case "scalar":
		// Scalar is the non-structural JSON scalar union (null, string,
		// bool, number). Containers never enter through this type, even when
		// their leaves happen to be scalar.
		switch value.(type) {
		case nil, string, bool:
			return true
		}
		if valueMatches("number", value) {
			_, err := json.Marshal(value)
			return err == nil
		}
		return false
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "integer":
		switch number := value.(type) {
		case int, int32, int64:
			return true
		case float64:
			return number == float64(int64(number))
		case json.Number:
			integer, valid := jsonNumberToken(string(number))
			return valid && integer
		}
	case "number":
		switch n := value.(type) {
		case int, int32, int64, float32, float64:
			return true
		case json.Number:
			_, valid := jsonNumberToken(string(n))
			return valid
		}
	}
	return false
}

// jsonNumberToken validates the JSON number grammar and reports semantic
// integrality from the token shape alone: no exponent expansion, no bigint or
// rational construction and no float64 conversion, so arbitrarily large or
// small exact tokens are decided in time proportional to their own digits.
// A saturated positive exponent keeps integrality without expanding it.
func jsonNumberToken(text string) (integer, valid bool) {
	i := 0
	if i < len(text) && text[i] == '-' {
		i++
	}
	start := i
	for i < len(text) && text[i] >= '0' && text[i] <= '9' {
		i++
	}
	intDigits := text[start:i]
	if intDigits == "" || (len(intDigits) > 1 && intDigits[0] == '0') {
		return false, false
	}
	fracDigits := ""
	if i < len(text) && text[i] == '.' {
		i++
		fs := i
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			i++
		}
		fracDigits = text[fs:i]
		if fracDigits == "" {
			return false, false
		}
	}
	exponent := 0
	saturated := false
	if i < len(text) {
		if text[i] != 'e' && text[i] != 'E' {
			return false, false
		}
		i++
		exponentNegative := false
		if i < len(text) && (text[i] == '+' || text[i] == '-') {
			exponentNegative = text[i] == '-'
			i++
		}
		es := i
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			i++
		}
		if es == i || i != len(text) {
			return false, false
		}
		expDigits := strings.TrimLeft(text[es:i], "0")
		switch {
		case expDigits == "":
			exponent = 0
		case len(expDigits) >= 19:
			saturated = true
		default:
			value, err := strconv.Atoi(expDigits)
			if err != nil {
				return false, false
			}
			exponent = value
		}
		if exponentNegative {
			if saturated {
				return false, true // negative exponent beyond any fraction length
			}
			exponent = -exponent
		}
	}
	if saturated {
		return true, true // positive saturated exponent exceeds any fraction length
	}
	fraction := strings.TrimRight(fracDigits, "0")
	if fraction == "" {
		return exponent >= 0, true
	}
	return exponent >= len(fraction), true
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}

func stringList(raw any) ([]string, bool) {
	switch list := raw.(type) {
	case nil:
		return nil, true
	case []string:
		return list, true
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	default:
		return nil, false
	}
}

func copyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
