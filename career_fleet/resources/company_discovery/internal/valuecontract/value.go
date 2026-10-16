// Package valuecontract defines the float64 value boundary shared by the
// factory and its standalone printed packages. It has no non-stdlib imports.
package valuecontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
)

const MaxMagnitude = 9007199254740991

// Number is the explicit float64 conversion for consumers that genuinely work
// in float64 (bounds, decision numerics, adapter arithmetic). It checks the
// decimal before conversion: ordinary binary rounding is permitted; overflow,
// underflow to zero and out-of-domain magnitudes are not. It is not the
// generic JSON data authority: Decode, Exact and Decode-preserving paths keep
// exact JSON number tokens and never route them through this function.
func Number(text string) (float64, error) {
	if len(text) > 4096 {
		return 0, fmt.Errorf("numeric representation exceeds bound")
	}
	if i := strings.IndexAny(text, "eE"); i >= 0 {
		exponent, err := strconv.ParseInt(text[i+1:], 10, 32)
		if err != nil || exponent < -4096 || exponent > 4096 {
			return 0, fmt.Errorf("numeric exponent exceeds bound")
		}
	}
	n, ok := new(big.Rat).SetString(text)
	if !ok {
		return 0, fmt.Errorf("invalid number")
	}
	if new(big.Rat).Abs(n).Cmp(new(big.Rat).SetInt64(MaxMagnitude)) > 0 {
		return 0, fmt.Errorf("number outside supported finite range")
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("number outside supported finite range")
	}
	if f == 0 {
		if n.Sign() != 0 {
			return 0, fmt.Errorf("number underflows supported range")
		}
		return 0, nil // canonical positive zero
	}
	return f, nil
}

// Decode is the generic JSON data authority. It rejects ambiguous JSON
// (duplicates, trailing data, invalid grammar) and preserves every number as
// its exact json.Number token with no float64 roundtrip and no numeric cap.
// Normalize remains the explicit adapter ABI float conversion.
func Decode(raw []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > 256 {
			return nil, fmt.Errorf("JSON nesting exceeds bound")
		}
		t, err := d.Token()
		if err != nil {
			return nil, err
		}
		if n, ok := t.(json.Number); ok {
			// Exact transport: the token is the value.
			return n, nil
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return t, nil
		}
		switch delim {
		case '{':
			out := map[string]any{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return nil, err
				}
				key, ok := k.(string)
				if !ok {
					return nil, fmt.Errorf("object key must be string")
				}
				if _, ok := out[key]; ok {
					return nil, fmt.Errorf("duplicate JSON object key")
				}
				v, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				out[key] = v
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return nil, fmt.Errorf("incomplete object")
			}
			return out, nil
		case '[':
			out := []any{}
			for d.More() {
				v, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return nil, fmt.Errorf("incomplete array")
			}
			return out, nil
		default:
			return nil, fmt.Errorf("invalid JSON delimiter")
		}
	}
	v, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, fmt.Errorf("JSON trailing data")
	}
	return v, nil
}

// Exact validates and detaches one value as generic JSON data without
// converting numbers: exact json.Number tokens are preserved and containers
// are copied. It is the generic data-path counterpart to Normalize's explicit
// float64 adapter conversion.
func Exact(value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return Decode(raw)
}

// Normalize is the explicit adapter ABI float64 conversion. Generic data
// transport uses Decode or Exact instead; this function exists for consumers
// that genuinely operate in float64.
func Normalize(value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	v, err := Decode(raw)
	if err != nil {
		return nil, err
	}
	return normalize(v)
}
func normalize(value any) (any, error) {
	switch v := value.(type) {
	case json.Number:
		return Number(string(v))
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			n, err := normalize(item)
			if err != nil {
				return nil, err
			}
			out[key] = n
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			n, err := normalize(item)
			if err != nil {
				return nil, err
			}
			out[i] = n
		}
		return out, nil
	default:
		return value, nil
	}
}

// CheckIntegers runs on the exact value, so a fractional decimal that would
// round to an integer can never satisfy an integer schema. It has no float64
// magnitude cap: arbitrary exact integers are valid JSON integers.
func CheckIntegers(schema map[string]any, value any) error {
	switch schema["type"] {
	case "integer":
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		n, ok := new(big.Rat).SetString(string(raw))
		if !ok || !n.IsInt() {
			return fmt.Errorf("integer required")
		}
		return nil
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		props, _ := schema["properties"].(map[string]any)
		keys := make([]string, 0, len(obj))
		for key := range obj {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			item := obj[key]
			if typ, ok := props[key].(map[string]any); ok {
				if err := CheckIntegers(typ, item); err != nil {
					return fmt.Errorf("%s: %w", key, err)
				}
			}
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return nil
		}
		typ, _ := schema["items"].(map[string]any)
		for i, item := range items {
			if err := CheckIntegers(typ, item); err != nil {
				return fmt.Errorf("item %d: %w", i, err)
			}
		}
	}
	return nil
}
