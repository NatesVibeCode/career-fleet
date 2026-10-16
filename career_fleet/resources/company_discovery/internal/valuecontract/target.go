package valuecontract

import (
	"fmt"
	"strings"
)

type FieldTarget struct {
	Side string `json:"side"`
	Name string `json:"name"`
}
type BoundTarget struct {
	Scope       string `json:"scope"`
	Side        string `json:"side,omitempty"`
	Field       string `json:"field,omitempty"`
	Metadata    string `json:"metadata,omitempty"`
	Measurement string `json:"measurement"`
}

func CheckMetadata(kind string, target BoundTarget, params map[string]any, value any) error {
	if target.Scope != "metadata" {
		return fmt.Errorf("metadata target required")
	}
	if target.Metadata == "tags" && target.Measurement == "value" {
		v, err := Normalize(value)
		if err != nil {
			return err
		}
		tags, ok := v.([]any)
		if !ok {
			return fmt.Errorf("metadata tags must be an array")
		}
		plan, err := PlanBound(kind, params)
		if err != nil {
			return err
		}
		for _, tag := range tags {
			if err := plan.Check("value", tag); err != nil {
				return err
			}
		}
		return nil
	}
	return CheckBound(kind, target.Measurement, params, value)
}

func ResolveBound(subject string, citing FieldTarget, fields []FieldTarget) (BoundTarget, error) {
	measurement := "value"
	selector := false
	for _, name := range []string{"length", "vocab"} {
		if strings.HasPrefix(subject, name+"(") && strings.HasSuffix(subject, ")") {
			subject = strings.TrimSuffix(strings.TrimPrefix(subject, name+"("), ")")
			selector = true
			if name == "length" {
				measurement = "length"
			}
			break
		}
	}
	if !selector {
		switch subject {
		case "rounds", "tools", "episodes", "ladder_steps", "episode_depth", "piece_size", "price_delta", "spend", "calls", "duration", "attempts":
			return BoundTarget{Scope: "structural", Field: subject, Measurement: measurement}, nil
		}
	}
	if selector {
		switch subject {
		case "intent", "tags", "meaning", "units", "source", "declared_by":
			return BoundTarget{Scope: "metadata", Side: citing.Side, Field: citing.Name, Metadata: subject, Measurement: measurement}, nil
		}
	}
	matches := []FieldTarget{}
	for _, f := range fields {
		if subject == "*" && selector && f == citing || subject == f.Side+"."+f.Name || selector && subject == f.Name {
			matches = append(matches, f)
		}
	}
	if len(matches) != 1 {
		return BoundTarget{}, fmt.Errorf("bound target %q must resolve to exactly one field", subject)
	}
	return BoundTarget{Scope: "runtime", Side: matches[0].Side, Field: matches[0].Name, Measurement: measurement}, nil
}
