package egresscontract

import (
	"encoding/json"
	"fmt"
	"discovery.local/process/internal/contractschema"
	"strings"
)

// The native Google response remains provider evidence, not a fetched web
// document. Generated text, grounding references and query counts stay separate.
func decodeGoogleSearch(b ModelBinding, req InferRequest, raw []byte) (InferResponse, error) {
	out := InferResponse{RequestedModel: b.Model}
	if _, e := schemasubset.DecodeJSON(raw); e != nil {
		return out, e
	}
	var wire struct {
		Model      string          `json:"modelVersion"`
		Usage      json.RawMessage `json:"usageMetadata"`
		Candidates []struct {
			Finish  string `json:"finishReason"`
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
			Grounding json.RawMessage `json:"groundingMetadata"`
		} `json:"candidates"`
	}
	if e := json.Unmarshal(raw, &wire); e != nil {
		return out, e
	}
	out.Model = wire.Model
	out.Usage = append(json.RawMessage(nil), wire.Usage...)
	if out.Model != b.Model {
		return out, fmt.Errorf("actual model identity differs from sealed binding")
	}
	if len(wire.Candidates) != 1 || wire.Candidates[0].Finish != "STOP" {
		return out, fmt.Errorf("incomplete grounded inference")
	}
	c := wire.Candidates[0]
	for _, p := range c.Content.Parts {
		if !p.Thought {
			out.Text += p.Text
		}
	}
	var g struct {
		Queries []string `json:"webSearchQueries"`
		Chunks  []struct {
			Web struct {
				URI   string `json:"uri"`
				Title string `json:"title"`
			} `json:"web"`
		} `json:"groundingChunks"`
	}
	if json.Unmarshal(c.Grounding, &g) != nil || len(g.Queries) == 0 || len(g.Chunks) == 0 || strings.TrimSpace(out.Text) == "" {
		return out, fmt.Errorf("Google search grounding evidence absent")
	}
	for _, q := range g.Queries {
		if strings.TrimSpace(q) == "" {
			return out, fmt.Errorf("empty grounding query")
		}
	}
	for _, s := range g.Chunks {
		if !strings.HasPrefix(s.Web.URI, "https://") || s.Web.Title == "" {
			return out, fmt.Errorf("invalid grounding reference")
		}
	}
	if len(out.Usage) == 0 {
		return out, fmt.Errorf("grounding usage absent")
	}
	out.Output = map[string]any{"generated_text": out.Text, "grounding_metadata": string(c.Grounding), "provider_response": string(raw), "search_query_count": json.Number(fmt.Sprint(len(g.Queries))), "source_count": json.Number(fmt.Sprint(len(g.Chunks)))}
	schema, e := inferenceSchema(req)
	if e != nil {
		return out, e
	}
	if schema != nil && len(schema.Validate(out.Output)) != 0 {
		return out, fmt.Errorf("grounding violates output contract")
	}
	return out, nil
}
