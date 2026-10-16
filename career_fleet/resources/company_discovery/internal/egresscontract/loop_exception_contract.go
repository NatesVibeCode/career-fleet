package egresscontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"discovery.local/process/internal/contractschema"
	"strings"
)

const LoopExceptionSchema = "forge.loop_exception_contract.v1"

type LoopNodeDefinition struct{ Kind, Digest string }

type LoopDeclaration struct {
	Key    string `json:"key"`
	Driver struct {
		Kind    string `json:"kind"`
		NodeKey string `json:"node_key"`
	} `json:"driver"`
	AttemptsMax int                   `json:"attempts_max"`
	ToolsMax    int                   `json:"tools_max"`
	Tools       []string              `json:"tools"`
	Escalate    bool                  `json:"escalate"`
	EscalateOn  []string              `json:"escalate_on"`
	Exception   LoopExceptionContract `json:"exception"`
}

// ParseLoopDeclarations is the single contract parser used before printing and
// again by the standalone worker before any driver/tool executes.
func ParseLoopDeclarations(raw []byte, definitions map[string]LoopNodeDefinition) ([]LoopDeclaration, error) {
	value, err := schemasubset.DecodeJSON(raw)
	if err != nil {
		return nil, err
	}
	rows, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("loops must be an array")
	}
	keys := map[string]bool{}
	loops := []LoopDeclaration{}
	for index, row := range rows {
		entry, ok := row.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("loop %d must be an object", index)
		}
		required := []string{"key", "driver", "attempts_max", "tools_max", "tools", "escalate", "escalate_on", "exception"}
		if len(entry) != len(required) {
			return nil, fmt.Errorf("loop %d declaration fields incomplete or unknown", index)
		}
		for _, name := range required {
			if entry[name] == nil {
				return nil, fmt.Errorf("loop %d requires %s", index, name)
			}
		}
		driver, ok := entry["driver"].(map[string]any)
		if !ok || len(driver) != 2 || driver["kind"] == nil || driver["node_key"] == nil {
			return nil, fmt.Errorf("loop %d driver requires kind and node_key", index)
		}
		encoded, err := json.Marshal(entry)
		if err != nil {
			return nil, err
		}
		var loop LoopDeclaration
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&loop); err != nil {
			return nil, fmt.Errorf("loop %d: %w", index, err)
		}
		if strings.TrimSpace(loop.Key) == "" || len(loop.Key) > 256 || keys[loop.Key] {
			return nil, fmt.Errorf("loop key blank or duplicate")
		}
		keys[loop.Key] = true
		d, exists := definitions[loop.Driver.NodeKey]
		if !exists || (loop.Driver.Kind != "infer" && loop.Driver.Kind != "harness") || d.Kind != loop.Driver.Kind {
			return nil, fmt.Errorf("loop %s driver definition mismatch", loop.Key)
		}
		if loop.AttemptsMax < 1 || loop.AttemptsMax > 10000 {
			return nil, fmt.Errorf("loop %s attempts bound", loop.Key)
		}
		if loop.ToolsMax < 0 || loop.ToolsMax > 1 || len(loop.Tools) > loop.ToolsMax {
			return nil, fmt.Errorf("loop %s permits at most one declared tool", loop.Key)
		}
		pins := map[string]string{loop.Driver.NodeKey: d.Digest}
		for _, key := range loop.Tools {
			tool, ok := definitions[key]
			if !ok || tool.Kind != "integration" || pins[key] != "" {
				return nil, fmt.Errorf("loop %s invalid or duplicate tool", loop.Key)
			}
			pins[key] = tool.Digest
		}
		if !loop.Escalate || len(loop.EscalateOn) == 0 {
			return nil, fmt.Errorf("loop %s escalation absent", loop.Key)
		}
		seen := map[string]bool{}
		for _, signal := range loop.EscalateOn {
			if (signal != "output_invalid" && signal != "context_full" && signal != "agent_judgment") || seen[signal] {
				return nil, fmt.Errorf("loop %s invalid or duplicate signal", loop.Key)
			}
			seen[signal] = true
		}
		if err := loop.Exception.Validate(pins); err != nil {
			return nil, fmt.Errorf("loop %s: %w", loop.Key, err)
		}
		loops = append(loops, loop)
	}
	return loops, nil
}

// LoopExceptionContract is Process-owned. DefinitionPins bind the review to
// every exact driver/tool definition. A decomposition record is review evidence,
// not a machine proof that a better deterministic decomposition cannot exist.
type LoopExceptionContract struct {
	Schema  string `json:"schema"`
	Outcome struct {
		ID    string         `json:"id"`
		Field string         `json:"field"`
		Type  map[string]any `json:"type"`
	} `json:"outcome"`
	DefinitionPins map[string]string `json:"definition_pins"`
	Decomposition  []struct {
		Steps   []string `json:"steps"`
		Blocker string   `json:"blocker"`
	} `json:"decomposition"`
}

func (c LoopExceptionContract) Validate(pins map[string]string) error {
	text := func(s string) bool { return strings.TrimSpace(s) != "" && len(s) <= 8192 }
	if c.Schema != LoopExceptionSchema || !text(c.Outcome.ID) || !text(c.Outcome.Field) || len(c.Outcome.ID) > 256 || len(c.Outcome.Field) > 256 {
		return fmt.Errorf("loop exception requires one typed outcome")
	}
	if _, err := schemasubset.ParseMap(c.Outcome.Type, schemasubset.ModeStrict); err != nil {
		return fmt.Errorf("loop outcome type: %w", err)
	}
	if len(pins) == 0 || len(c.DefinitionPins) != len(pins) {
		return fmt.Errorf("loop exception definition pins incomplete")
	}
	for key, digest := range pins {
		if len(digest) != 64 || c.DefinitionPins[key] != digest {
			return fmt.Errorf("loop exception stale definition pin %s", key)
		}
		for _, r := range digest {
			if !strings.ContainsRune("0123456789abcdef", r) {
				return fmt.Errorf("loop exception invalid definition digest")
			}
		}
	}
	if len(c.Decomposition) == 0 || len(c.Decomposition) > 16 {
		return fmt.Errorf("loop exception decomposition review absent or too large")
	}
	for _, plan := range c.Decomposition {
		if len(plan.Steps) < 2 || len(plan.Steps) > 64 || !text(plan.Blocker) {
			return fmt.Errorf("loop exception requires attempted steps and unresolved blocker")
		}
		seen := map[string]bool{}
		for _, step := range plan.Steps {
			if !text(step) || seen[step] {
				return fmt.Errorf("loop exception invalid or repeated step")
			}
			seen[step] = true
		}
	}
	return nil
}

func (c LoopExceptionContract) Accept(out map[string]any) error {
	value, present := out[c.Outcome.Field]
	if !present {
		return fmt.Errorf("loop outcome %s missing field %s", c.Outcome.ID, c.Outcome.Field)
	}
	schema, err := schemasubset.ParseMap(c.Outcome.Type, schemasubset.ModeStrict)
	if err != nil {
		return err
	}
	return schema.FirstError(value, "$loop_outcome")
}
