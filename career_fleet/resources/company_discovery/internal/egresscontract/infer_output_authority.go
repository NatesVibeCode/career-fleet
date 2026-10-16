package egresscontract

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// InferOutputAuthority is authored for every model-output leaf. It is not
// inferred from a field name or populated by the provider. Wildcards denote
// array items, not permission to add properties. Runtime metadata belongs in
// the deterministic Process/Node result, outside the provider output schema.
type InferOutputAuthority struct {
	Kind      string `json:"kind"`
	Rationale string `json:"rationale"`
	InputPath string `json:"input_path,omitempty"`
}

func schemaLeaves(schema map[string]any, path string, leaves map[string]map[string]any) error {
	switch schema["type"] {
	case "object":
		props, ok := schema["properties"].(map[string]any)
		if !ok || len(props) == 0 || schema["additionalProperties"] != false {
			return fmt.Errorf("Infer output authority requires closed typed objects at %s", path)
		}
		keys := make([]string, 0, len(props))
		for key := range props {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			// These path characters are reserved by this closed contract's
			// pointer dialect; a literal wildcard cannot impersonate an item.
			if key == "" || strings.ContainsAny(key, "/~*") {
				return fmt.Errorf("Infer authority field path invalid: %s", key)
			}
			child, ok := props[key].(map[string]any)
			if !ok {
				return fmt.Errorf("Infer authority field untyped: %s/%s", path, key)
			}
			if err := schemaLeaves(child, path+"/"+key, leaves); err != nil {
				return err
			}
		}
	case "array":
		child, ok := schema["items"].(map[string]any)
		if !ok {
			return fmt.Errorf("Infer authority array untyped: %s", path)
		}
		return schemaLeaves(child, path+"/*", leaves)
	case "string", "integer", "number", "boolean", "null":
		leaves[path] = schema
	default:
		return fmt.Errorf("Infer authority leaf requires an explicit type: %s", path)
	}
	return nil
}

// InferSchemaLeaves is shared with Forge's source-bound result-binding review.
// The provider contract still requires coverage through Validate, not this helper.
func InferSchemaLeaves(schema map[string]any, path string, leaves map[string]map[string]any) error {
	return schemaLeaves(schema, path, leaves)
}

func InferInfrastructureOutputPath(path string) bool { return infrastructureOutputPath(path) }

// Execution identity cannot be chosen through a semantic evidence selector.
func InferExecutionIdentityPath(path string) bool {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		name := strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				return unicode.ToLower(r)
			}
			return -1
		}, part)
		switch name {
		case "workid", "taskid", "nodeid", "invocationid", "mutationid", "receiptid", "receipt", "receipts", "idempotencykey":
			return true
		}
		if i+1 < len(parts) && (name == "task" || name == "work" || name == "invocation" || name == "mutation" || name == "node") && (parts[i+1] == "id" || parts[i+1] == "key") {
			return true
		}
	}
	return false
}

// This is a defensive check for known infrastructure fields, not a semantic
// classifier. Source-bound review must still reject disguised bookkeeping.
func infrastructureOutputPath(path string) bool {
	if InferExecutionIdentityPath(path) {
		return true
	}
	for _, part := range strings.Split(path, "/") {
		name := strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				return unicode.ToLower(r)
			}
			return -1
		}, part)
		if strings.HasSuffix(name, "digest") || strings.HasSuffix(name, "hash") || strings.HasSuffix(name, "revision") {
			return true
		}
		switch name {
		case "workid", "taskid", "nodeid", "windowid", "sourceid", "documentid", "evidenceid", "invocationid", "mutationid", "receiptid", "receipt", "receipts", "idempotencykey", "expectedrevision", "sourcedigests", "definitionpins", "promptdigest", "outputschemadigest":
			return true
		}
	}
	return false
}

func (c InferNodeContract) validateOutputAuthority() error {
	out, in := map[string]map[string]any{}, map[string]map[string]any{}
	if err := schemaLeaves(c.OutputSchema, "", out); err != nil {
		return err
	}
	if err := schemaLeaves(c.InputSchema, "", in); err != nil {
		return err
	}
	if len(c.OutputAuthority) != len(out) {
		return fmt.Errorf("Infer output authority must cover every model-output leaf exactly")
	}
	paths := make([]string, 0, len(out))
	for path := range out {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		role, ok := c.OutputAuthority[path]
		if !ok || strings.TrimSpace(role.Rationale) == "" {
			return fmt.Errorf("Infer output authority review absent: %s", path)
		}
		if infrastructureOutputPath(path) {
			return fmt.Errorf("Infer runtime-owned metadata cannot be model output: %s", path)
		}
		if out[path]["type"] == "null" {
			return fmt.Errorf("Infer fixed output must be supplied deterministically: %s", path)
		}
		if raw, present := out[path]["enum"]; present {
			encoded, err := json.Marshal(raw)
			if err != nil {
				return err
			}
			var options []any
			if err := json.Unmarshal(encoded, &options); err != nil {
				return err
			}
			if len(options) == 1 {
				return fmt.Errorf("Infer singleton output must be supplied deterministically: %s", path)
			}
		}
		switch role.Kind {
		case "semantic":
			if role.InputPath != "" {
				return fmt.Errorf("Infer semantic output cannot declare a copy binding: %s", path)
			}
		case "source_selection":
			input, exists := schemaCollection(c.InputSchema, role.InputPath)
			if !exists || infrastructureOutputPath(role.InputPath) || input["type"] != "array" {
				return fmt.Errorf("Infer source selector must reference one supplied typed collection: %s", path)
			}
			if out[path]["type"] != "integer" {
				return fmt.Errorf("Infer source selection requires an integer position, never copied identity: %s", path)
			}
		default:
			return fmt.Errorf("Infer output must be semantic or source_selection; deterministic/runtime output refused: %s", path)
		}
	}
	return nil
}

func pointerValues(value any, parts []string) []any {
	if len(parts) == 0 {
		return []any{value}
	}
	if parts[0] == "*" {
		items, ok := value.([]any)
		if !ok {
			return nil
		}
		var values []any
		for _, item := range items {
			values = append(values, pointerValues(item, parts[1:])...)
		}
		return values
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	child, ok := object[parts[0]]
	if !ok {
		return nil
	}
	return pointerValues(child, parts[1:])
}

// A selector names one statically declared collection. Nested/wildcard input
// joins are prepared deterministically before inference, not guessed by it.
func schemaCollection(schema map[string]any, path string) (map[string]any, bool) {
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "~*") {
		return nil, false
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if schema["type"] != "object" {
			return nil, false
		}
		props, ok := schema["properties"].(map[string]any)
		if !ok {
			return nil, false
		}
		schema, ok = props[part].(map[string]any)
		if !ok {
			return nil, false
		}
	}
	return schema, true
}

func (c InferNodeContract) validateSourceSelections(input, output any) error {
	for path, role := range c.OutputAuthority {
		if role.Kind != "source_selection" {
			continue
		}
		collections := pointerValues(input, strings.Split(strings.TrimPrefix(role.InputPath, "/"), "/"))
		if len(collections) != 1 {
			return fmt.Errorf("Infer source collection absent: %s", path)
		}
		records, ok := collections[0].([]any)
		if !ok {
			return fmt.Errorf("Infer source collection untyped: %s", path)
		}
		for _, value := range pointerValues(output, strings.Split(strings.TrimPrefix(path, "/"), "/")) {
			number, ok := value.(json.Number)
			if !ok {
				return fmt.Errorf("Infer source selection must be an integer position: %s", path)
			}
			position, err := number.Int64()
			if err != nil || position < 0 || position >= int64(len(records)) {
				return fmt.Errorf("Infer source selection outside supplied collection: %s", path)
			}
		}
	}
	return nil
}
