package schemasubset

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// DecodeJSON keeps numeric spelling and rejects ambiguous duplicate keys,
// trailing values, and excessive nesting before schema or value admission.
func DecodeJSON(raw []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > 256 {
			return nil, fmt.Errorf("JSON nesting exceeds bound")
		}
		token, err := d.Token()
		if err != nil {
			return nil, err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return token, nil
		}
		switch delim {
		case '{':
			value := map[string]any{}
			for d.More() {
				keyToken, err := d.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, fmt.Errorf("JSON object key invalid")
				}
				if _, exists := value[key]; exists {
					return nil, fmt.Errorf("duplicate JSON object key")
				}
				child, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				value[key] = child
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return nil, fmt.Errorf("JSON object incomplete")
			}
			return value, nil
		case '[':
			value := []any{}
			for d.More() {
				child, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				value = append(value, child)
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return nil, fmt.Errorf("JSON array incomplete")
			}
			return value, nil
		default:
			return nil, fmt.Errorf("JSON delimiter invalid")
		}
	}
	value, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, fmt.Errorf("JSON trailing data")
	}
	return value, nil
}
