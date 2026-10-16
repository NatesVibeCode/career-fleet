package egresscontract

import (
	"fmt"
	"strconv"
	"strings"
)

// OutcomeBinding is an operation-authored identity correspondence. Result
// meaning remains in typed output requirements and independent Process acceptance.
type OutcomeBinding struct {
	InputField  string `json:"input_field"`
	InputPath   string `json:"input_path"`
	OutputField string `json:"output_field"`
	OutputPath  string `json:"output_path"`
}

func ValidOutcomePointer(pointer string) bool {
	if pointer == "" {
		return true
	}
	if !strings.HasPrefix(pointer, "/") {
		return false
	}
	for i := 0; i < len(pointer); i++ {
		if pointer[i] == '~' {
			i++
			if i == len(pointer) || (pointer[i] != '0' && pointer[i] != '1') {
				return false
			}
		}
	}
	return true
}
func OutcomePointer(value any, path string) (any, bool) {
	if !ValidOutcomePointer(path) {
		return nil, false
	}
	if path == "" {
		return value, true
	}
	for _, segment := range strings.Split(path[1:], "/") {
		key := strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
		switch object := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = object[key]
			if !ok {
				return nil, false
			}
		case []any:
			index, err := strconv.Atoi(key)
			if err != nil || index < 0 || index >= len(object) || strconv.Itoa(index) != key {
				return nil, false
			}
			value = object[index]
		default:
			return nil, false
		}
	}
	return value, true
}
func ValidateOutcomeBindings(bindings []OutcomeBinding, inputs, outputs map[string]any) error {
	for _, binding := range bindings {
		in, present := inputs[binding.InputField]
		if !present {
			return fmt.Errorf("outcome input identity absent: %s", binding.InputField)
		}
		out, present := outputs[binding.OutputField]
		if !present {
			return fmt.Errorf("outcome output identity absent: %s", binding.OutputField)
		}
		a, ok := OutcomePointer(in, binding.InputPath)
		if !ok {
			return fmt.Errorf("outcome input identity path absent")
		}
		b, ok := OutcomePointer(out, binding.OutputPath)
		if !ok {
			return fmt.Errorf("outcome output identity path absent")
		}
		x, err := ObservationDigest(a)
		if err != nil {
			return err
		}
		y, err := ObservationDigest(b)
		if err != nil {
			return err
		}
		if x != y {
			return fmt.Errorf("outcome original identity mismatch: %s -> %s", binding.InputField, binding.OutputField)
		}
	}
	return nil
}
