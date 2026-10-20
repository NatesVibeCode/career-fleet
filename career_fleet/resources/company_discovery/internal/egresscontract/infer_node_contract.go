package egresscontract

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"discovery.local/process/internal/contractschema"
)

const InferNodeContractSchema = "forge.infer_node_contract.v2"
const InferNodeContractSchemaV3 = "forge.infer_node_contract.v3"

// InferNodeContract belongs to the exact Node definition, not model selection.
// InputSchema declares the fixed named data slots sent beside the pinned prompt.
// OutputSchema declares the blanks the model may fill. No tool/menu/loop ABI is
// exposed. The Process may invoke this one-call Node again in a reviewed loop.
type InferNodeContract struct {
	Schema          string                          `json:"schema"`
	Outcome         string                          `json:"outcome"`
	PromptDigest    string                          `json:"prompt_digest"`
	InputSchema     map[string]any                  `json:"input_schema"`
	OutputSchema    map[string]any                  `json:"output_schema"`
	OutputAuthority map[string]InferOutputAuthority `json:"output_authority"`
	RequestProfile  string                          `json:"request_profile,omitempty"`
	CallsMax        int                             `json:"calls_max"`
}

func (c InferNodeContract) Validate() error {
	if (c.Schema != InferNodeContractSchema && c.Schema != InferNodeContractSchemaV3) || strings.TrimSpace(c.Outcome) == "" || len(c.Outcome) > 256 || c.CallsMax != 1 {
		return fmt.Errorf("Infer contract requires schema, one outcome and calls_max=1")
	}
	if c.Schema == InferNodeContractSchema && c.RequestProfile != "" {
		return fmt.Errorf("Infer v2 contract cannot select a request profile")
	}
	if c.Schema == InferNodeContractSchemaV3 {
		if err := c.validateRequestProfile(); err != nil {
			return err
		}
	}
	if len(c.PromptDigest) != 64 {
		return fmt.Errorf("Infer contract prompt digest absent")
	}
	for _, r := range c.PromptDigest {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return fmt.Errorf("Infer contract prompt digest invalid")
		}
	}
	for name, raw := range map[string]map[string]any{"input": c.InputSchema, "output": c.OutputSchema} {
		if raw["type"] != "object" {
			return fmt.Errorf("Infer contract %s must be a closed object", name)
		}
		props, ok := raw["properties"].(map[string]any)
		if !ok || len(props) == 0 {
			return fmt.Errorf("Infer contract %s slots absent", name)
		}
		requiredRaw, err := json.Marshal(raw["required"])
		if err != nil {
			return err
		}
		var required []string
		if json.Unmarshal(requiredRaw, &required) != nil || len(required) != len(props) {
			return fmt.Errorf("Infer contract %s requires every declared slot", name)
		}
		seen := map[string]bool{}
		for _, key := range required {
			if _, ok := props[key]; !ok || seen[key] {
				return fmt.Errorf("Infer contract %s required slots mismatch", name)
			}
			seen[key] = true
		}
		if _, err := schemasubset.ParseMap(raw, schemasubset.ModeStrict); err != nil {
			return fmt.Errorf("Infer contract %s: %w", name, err)
		}
	}
	return c.validateOutputAuthority()
}

func schemaProperties(value any) (map[string]any, bool) {
	obj, ok := value.(map[string]any)
	if !ok || obj["type"] != "object" || obj["additionalProperties"] != false {
		return nil, false
	}
	props, ok := obj["properties"].(map[string]any)
	return props, ok
}

func exactSchemaProperties(schema map[string]any, names ...string) (map[string]any, bool) {
	props, ok := schemaProperties(schema)
	if !ok || len(props) != len(names) {
		return nil, false
	}
	want := map[string]bool{}
	for _, name := range names {
		want[name] = true
		if _, exists := props[name]; !exists {
			return nil, false
		}
	}
	for name := range props {
		if !want[name] {
			return nil, false
		}
	}
	return props, true
}

func schemaIsType(value any, typ string) bool {
	obj, ok := value.(map[string]any)
	return ok && obj["type"] == typ
}

func schemaArrayItems(value any) (map[string]any, bool) {
	obj, ok := value.(map[string]any)
	if !ok || obj["type"] != "array" {
		return nil, false
	}
	items, ok := obj["items"].(map[string]any)
	return items, ok
}

func schemaRequiredExactly(schema map[string]any, names ...string) bool {
	raw, err := json.Marshal(schema["required"])
	if err != nil {
		return false
	}
	var required []string
	if json.Unmarshal(raw, &required) != nil || len(required) != len(names) {
		return false
	}
	want := map[string]bool{}
	for _, name := range names {
		want[name] = true
	}
	for _, name := range required {
		if !want[name] {
			return false
		}
		delete(want, name)
	}
	return len(want) == 0
}

func (c InferNodeContract) validateRequestProfile() error {
	switch c.RequestProfile {
	case RequestProfileJSONSchemaCompletion:
		if c.OutputSchema == nil {
			return fmt.Errorf("JSON Schema completion profile requires an explicit output schema")
		}
	case RequestProfileScaleDownSummarize:
		inputs, ok := exactSchemaProperties(c.InputSchema, "text", "focus", "format", "language", "length")
		if !ok || !schemaIsType(inputs["text"], "string") || !schemaIsType(inputs["focus"], "string") || !schemaIsType(inputs["format"], "string") || !schemaIsType(inputs["language"], "string") || !schemaIsType(inputs["length"], "string") {
			return fmt.Errorf("ScaleDown Summarize profile requires typed text and summary controls")
		}
		outputs, ok := exactSchemaProperties(c.OutputSchema, "summary")
		if !ok || !schemaIsType(outputs["summary"], "string") {
			return fmt.Errorf("ScaleDown Summarize profile requires one string summary output")
		}
	case RequestProfileScaleDownExtract:
		inputs, ok := exactSchemaProperties(c.InputSchema, "source", "entities")
		if !ok {
			return fmt.Errorf("ScaleDown Extract profile requires source and entity descriptions")
		}
		source, ok := inputs["source"].(map[string]any)
		if !ok {
			return fmt.Errorf("ScaleDown Extract source must be a closed object")
		}
		sourceProps, ok := exactSchemaProperties(source, "kind", "text", "filename", "mime_type", "data_base64")
		if !ok || !schemaRequiredExactly(source, "kind", "text", "filename", "mime_type", "data_base64") || !schemaIsType(sourceProps["kind"], "string") || !schemaIsType(sourceProps["text"], "string") || !schemaIsType(sourceProps["filename"], "string") || !schemaIsType(sourceProps["mime_type"], "string") || !schemaIsType(sourceProps["data_base64"], "string") {
			return fmt.Errorf("ScaleDown Extract source fields are not typed")
		}
		kindSchema, _ := sourceProps["kind"].(map[string]any)
		kindEnumRaw, _ := json.Marshal(kindSchema["enum"])
		if string(kindEnumRaw) != `["text","document"]` {
			return fmt.Errorf("ScaleDown Extract source kind must distinguish text and document")
		}
		items, ok := schemaArrayItems(inputs["entities"])
		if !ok {
			return fmt.Errorf("ScaleDown Extract entity descriptions must be an array")
		}
		entityProps, ok := exactSchemaProperties(items, "name", "description")
		if !ok || !schemaRequiredExactly(items, "name", "description") || !schemaIsType(entityProps["name"], "string") || !schemaIsType(entityProps["description"], "string") {
			return fmt.Errorf("ScaleDown Extract entity description fields are not typed")
		}
		outputs, ok := exactSchemaProperties(c.OutputSchema, "entities")
		if !ok {
			return fmt.Errorf("ScaleDown Extract profile requires one entities output")
		}
		entityItems, ok := schemaArrayItems(outputs["entities"])
		if !ok {
			return fmt.Errorf("ScaleDown Extract output entities must be an array")
		}
		entityOut, ok := exactSchemaProperties(entityItems, "type", "text", "confidence", "start", "end", "context")
		if !ok || !schemaRequiredExactly(entityItems, "type", "text", "confidence", "start", "end", "context") || !schemaIsType(entityOut["type"], "string") || !schemaIsType(entityOut["text"], "string") || !schemaIsType(entityOut["confidence"], "number") || !schemaIsType(entityOut["start"], "integer") || !schemaIsType(entityOut["end"], "integer") || !schemaIsType(entityOut["context"], "string") {
			return fmt.Errorf("ScaleDown Extract output entity fields are not typed")
		}
	default:
		return fmt.Errorf("unsupported Infer request profile")
	}
	return nil
}

func validateProfileInput(profile string, input any) error {
	obj, ok := input.(map[string]any)
	if !ok {
		return fmt.Errorf("Infer profile input must be an object")
	}
	if profile == RequestProfileScaleDownSummarize {
		for _, name := range []string{"text", "focus", "format", "language", "length"} {
			value, ok := obj[name].(string)
			if !ok || strings.TrimSpace(value) == "" {
				return fmt.Errorf("ScaleDown Summarize requires nonempty %s", name)
			}
		}
		return nil
	}
	if profile != RequestProfileScaleDownExtract {
		return nil
	}
	source, ok := obj["source"].(map[string]any)
	if !ok {
		return fmt.Errorf("ScaleDown Extract source absent")
	}
	kind, _ := source["kind"].(string)
	text, _ := source["text"].(string)
	filename, _ := source["filename"].(string)
	mimeType, _ := source["mime_type"].(string)
	encoded, _ := source["data_base64"].(string)
	switch kind {
	case "text":
		if strings.TrimSpace(text) == "" || filename != "" || mimeType != "" || encoded != "" {
			return fmt.Errorf("ScaleDown Extract text source must contain only nonempty text")
		}
	case "document":
		if text != "" || strings.TrimSpace(filename) == "" || encoded == "" {
			return fmt.Errorf("ScaleDown Extract document source requires filename and base64 data only")
		}
		if mimeType != "application/pdf" && mimeType != "image/jpeg" && mimeType != "image/png" {
			return fmt.Errorf("ScaleDown Extract document MIME type is unsupported")
		}
		if _, err := base64.StdEncoding.DecodeString(encoded); err != nil {
			return fmt.Errorf("ScaleDown Extract document base64 is invalid")
		}
	default:
		return fmt.Errorf("ScaleDown Extract source kind is unsupported")
	}
	entities, ok := obj["entities"].([]any)
	if !ok || len(entities) == 0 {
		return fmt.Errorf("ScaleDown Extract requires at least one entity description")
	}
	seen := map[string]bool{}
	for _, raw := range entities {
		entity, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("ScaleDown Extract entity description is invalid")
		}
		name, nameOK := entity["name"].(string)
		description, descriptionOK := entity["description"].(string)
		if !nameOK || !descriptionOK || strings.TrimSpace(name) == "" || strings.TrimSpace(description) == "" || seen[name] {
			return fmt.Errorf("ScaleDown Extract entity names must be nonempty and unique, with a description")
		}
		seen[name] = true
	}
	return nil
}

// InferContractFromNodeSpec is also shipped in the sovereign worker. Decoding
// the contract is strict even when this view reads only part of the Node spec.
func InferContractFromNodeSpec(raw []byte) (InferNodeContract, error) {
	var view struct {
		Identity struct {
			Kind string `json:"kind"`
		} `json:"identity"`
		Composition struct {
			Outcomes []string `json:"outcomes"`
		} `json:"composition"`
		Mechanism struct {
			Prompt struct {
				Ref struct {
					Digest string `json:"digest"`
				} `json:"ref"`
				Contract json.RawMessage `json:"contract"`
			} `json:"prompt"`
		} `json:"mechanism"`
	}
	if _, err := schemasubset.DecodeJSON(raw); err != nil {
		return InferNodeContract{}, err
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		return InferNodeContract{}, err
	}
	var c InferNodeContract
	dec := json.NewDecoder(bytes.NewReader(view.Mechanism.Prompt.Contract))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("Infer contract: %w", err)
	}
	if err := c.Validate(); err != nil {
		return c, err
	}
	if view.Identity.Kind != "infer" || len(view.Composition.Outcomes) != 1 || view.Composition.Outcomes[0] != c.Outcome || view.Mechanism.Prompt.Ref.Digest != c.PromptDigest {
		return c, fmt.Errorf("Infer contract does not bind the Node kind, prompt and single outcome")
	}
	return c, nil
}

// ContractInferClient owns an immutable copy and a one-call budget. It is the
// only client passed to Infer task code by both the printer and proof runner.
type ContractInferClient struct {
	mu            sync.Mutex
	client        InferClient
	contract      InferNodeContract
	input, output *schemasubset.Schema
	used          bool
	failure       error
}

func NewContractInferClient(client InferClient, c InferNodeContract) (*ContractInferClient, error) {
	if client == nil {
		return nil, fmt.Errorf("Infer client absent")
	}
	// Detach authored maps: a Node must not mutate its authority through aliases.
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	var frozen InferNodeContract
	if err = json.Unmarshal(raw, &frozen); err != nil {
		return nil, err
	}
	c = frozen
	if err = c.Validate(); err != nil {
		return nil, err
	}
	in, _ := schemasubset.ParseMap(c.InputSchema, schemasubset.ModeStrict)
	out, _ := schemasubset.ParseMap(c.OutputSchema, schemasubset.ModeStrict)
	return &ContractInferClient{client: client, contract: c, input: in, output: out}, nil
}

func (c *ContractInferClient) Err() error { c.mu.Lock(); defer c.mu.Unlock(); return c.failure }

func (c *ContractInferClient) Infer(ctx context.Context, req InferRequest) (InferResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fail := func(err error) (InferResponse, error) {
		if c.failure == nil {
			c.failure = err
		}
		return InferResponse{}, err
	}
	if c.failure != nil {
		return InferResponse{}, c.failure
	}
	if c.used {
		return fail(fmt.Errorf("Infer contract: calls_max exceeded"))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if inferenceDigest("praxis.prompt.v1", []byte(req.Prompt)) != c.contract.PromptDigest {
		return fail(fmt.Errorf("Infer contract: fixed prompt changed"))
	}
	if req.RequestProfile != "" {
		return fail(fmt.Errorf("Infer contract: task code cannot select a request profile"))
	}
	value, err := schemasubset.DecodeJSON(req.Input)
	if err != nil {
		return fail(fmt.Errorf("Infer contract input: %w", err))
	}
	if err = c.input.FirstError(value, "$input"); err != nil {
		return fail(err)
	}
	profile := c.contract.RequestProfile
	if profile == "" {
		profile = RequestProfileJSONCompletion
	}
	if err = validateProfileInput(profile, value); err != nil {
		return fail(err)
	}
	want, err := json.Marshal(c.contract.OutputSchema)
	if err != nil {
		return fail(err)
	}
	got, err := json.Marshal(req.OutputSchema)
	if err != nil {
		return fail(err)
	}
	if !bytes.Equal(want, got) {
		return fail(fmt.Errorf("Infer contract: output schema changed"))
	}
	// Freeze request values before crossing the provider boundary.
	req.Input = append(json.RawMessage(nil), req.Input...)
	req.OutputSchema = nil
	if err = json.Unmarshal(want, &req.OutputSchema); err != nil {
		return fail(err)
	}
	req.RequestProfile = profile
	c.used = true
	response, err := c.client.Infer(ctx, req)
	if err != nil {
		return fail(err)
	}
	// Normalize before checking selectors: typed slices/maps from an adapter
	// must not evade the same checks applied to decoded provider JSON.
	rawOutput, err := json.Marshal(response.Output)
	if err != nil {
		return fail(err)
	}
	response.Output, err = schemasubset.DecodeJSON(rawOutput)
	if err != nil {
		return fail(err)
	}
	if err = c.output.FirstError(response.Output, "$output"); err != nil {
		return fail(err)
	}
	if err = c.contract.validateSourceSelections(value, response.Output); err != nil {
		return fail(err)
	}
	return response, nil
}
