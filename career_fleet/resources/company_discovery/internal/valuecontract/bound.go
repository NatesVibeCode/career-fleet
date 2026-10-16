package valuecontract

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

func boundNumber(v any) (float64, error) {
	n, err := Normalize(v)
	if err != nil {
		return 0, err
	}
	f, ok := n.(float64)
	if !ok {
		return 0, fmt.Errorf("numeric bound observation required")
	}
	return f, nil
}

func ValidateBound(kind string, params map[string]any) error {
	allowed := map[string][]string{"count": {"min", "max"}, "range": {"lo", "hi"}, "set": {"members"}}
	if keys, ok := allowed[kind]; ok {
		unknown := []string{}
		for key := range params {
			known := false
			for _, k := range keys {
				known = known || key == k
			}
			if !known {
				unknown = append(unknown, key)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			return fmt.Errorf("unknown %s bound parameter %q", kind, unknown[0])
		}
	}
	switch kind {
	case "count":
		found := false
		for _, key := range []string{"min", "max"} {
			if v, ok := params[key]; ok {
				found = true
				if err := CheckIntegers(map[string]any{"type": "integer"}, v); err != nil {
					return err
				}
				n, err := boundNumber(v)
				if err != nil || math.Trunc(n) != n {
					return fmt.Errorf("count min/max must be supported integers")
				}
			}
		}
		if !found {
			return fmt.Errorf("count needs min or max")
		}
		lo, lok := params["min"]
		hi, hok := params["max"]
		if lok && hok {
			l, _ := boundNumber(lo)
			h, _ := boundNumber(hi)
			if l > h {
				return fmt.Errorf("count min exceeds max")
			}
		}
	case "range":
		lo, e1 := exactBoundNumber(params["lo"])
		hi, e2 := exactBoundNumber(params["hi"])
		if e1 != nil || e2 != nil {
			return fmt.Errorf("range lo/hi must be supported numbers")
		}
		if compareBoundDecimal(lo, hi) > 0 {
			return fmt.Errorf("range lo exceeds hi")
		}
	case "set":
		members, ok := params["members"].([]any)
		if !ok || len(members) == 0 {
			return fmt.Errorf("set members required")
		}
		for _, member := range members {
			if s, ok := member.(string); !ok || strings.TrimSpace(s) == "" {
				return fmt.Errorf("set members must be non-blank strings")
			}
		}
	default:
		return fmt.Errorf("unknown bound kind %q", kind)
	}
	return nil
}

// CheckBound evaluates an already-resolved runtime measurement. Metadata and
// structural bound selection belongs to the resolver, not this evaluator.
func CheckBound(kind, measurement string, params map[string]any, value any) error {
	plan, err := PlanBound(kind, params)
	if err != nil {
		return err
	}
	return plan.Check(measurement, value)
}

// BoundPlan is a validated bound: validate once, evaluate many values
// without re-validating the kind and parameters per evaluation.
type BoundPlan struct {
	kind   string
	params map[string]any
}

// PlanBound validates a bound kind and parameters once for repeated checks.
func PlanBound(kind string, params map[string]any) (BoundPlan, error) {
	if err := ValidateBound(kind, params); err != nil {
		return BoundPlan{}, err
	}
	return BoundPlan{kind: kind, params: params}, nil
}

// Check evaluates one already-resolved runtime measurement against the plan.
func (p BoundPlan) Check(measurement string, value any) error {
	kind, params := p.kind, p.params
	if measurement == "length" {
		if kind != "count" {
			return fmt.Errorf("length requires count bound")
		}
		switch v := value.(type) {
		case string:
			// String length is measured in characters (UTF-8 runes), never
			// bytes: a multibyte character counts once. Arrays and objects
			// stay element counts.
			value = utf8.RuneCountInString(v)
		case []any:
			value = len(v)
		case map[string]any:
			value = len(v)
		default:
			return fmt.Errorf("length bound requires string, array or object")
		}
	} else if measurement != "value" {
		return fmt.Errorf("unknown bound measurement")
	}
	switch kind {
	case "range":
		n, err := exactBoundNumber(value)
		if err != nil {
			return err
		}
		lo, _ := exactBoundNumber(params["lo"])
		hi, _ := exactBoundNumber(params["hi"])
		if compareBoundDecimal(n, lo) < 0 {
			return fmt.Errorf("bound_exceeded: below lo")
		}
		if compareBoundDecimal(n, hi) > 0 {
			return fmt.Errorf("bound_exceeded: above hi")
		}
	case "count":
		n, err := boundNumber(value)
		if err != nil {
			return err
		}
		if math.Trunc(n) != n {
			return fmt.Errorf("count observation must be integer")
		}
		low, high := "min", "max"
		if v, ok := params[low]; ok {
			lo, _ := boundNumber(v)
			if n < lo {
				return fmt.Errorf("bound_exceeded: below %s", low)
			}
		}
		if v, ok := params[high]; ok {
			hi, _ := boundNumber(v)
			if n > hi {
				return fmt.Errorf("bound_exceeded: above %s", high)
			}
		}
	case "set":
		if b, ok := value.(bool); ok {
			value = strconv.FormatBool(b)
		}
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("set observation must be string or boolean")
		}
		for _, v := range params["members"].([]any) {
			if s == v.(string) {
				return nil
			}
		}
		return fmt.Errorf("bound_exceeded: value outside set")
	}
	return nil
}
