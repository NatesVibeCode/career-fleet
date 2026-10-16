package capabilitycontract

// This file is deliberately stdlib-only. The printer embeds this exact source
// in the sovereign worker so authoring and execution share one evaluator.
import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
)

const RequirementsSchema = "aeltum.requirements.v1"

type SemanticExample struct {
	ID       string `json:"id"`
	Present  bool   `json:"present"`
	Value    any    `json:"value"`
	Accepted bool   `json:"accepted"`
	Source   string `json:"source"`
}

// Definition is a scoped meaning contract, carried with port requirements into
// composition and printing. It is not a global ontology or a conversion rule.
type SemanticDefinition struct {
	ID          string            `json:"id"`
	Description string            `json:"description"`
	Entity      string            `json:"entity"`
	Unit        string            `json:"unit"`
	Currency    string            `json:"currency"`
	TimeBasis   string            `json:"time_basis"`
	Missing     string            `json:"missing"`
	Null        string            `json:"null"`
	Examples    []SemanticExample `json:"examples"`
}

func ValidateSemanticDefinition(d *SemanticDefinition) error {
	if d == nil {
		return nil
	}
	for _, v := range []string{d.ID, d.Description, d.Entity, d.Unit, d.Currency, d.TimeBasis} {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("definition needs identity, description and explicit qualifiers (use not_applicable or unresolved)")
		}
	}
	for _, v := range []string{d.Missing, d.Null} {
		if v != "allow" && v != "reject" && v != "unresolved" {
			return fmt.Errorf("missing/null meaning must be allow, reject or unresolved")
		}
	}
	seen := map[string]bool{}
	for _, e := range d.Examples {
		if e.ID == "" || e.Source == "" || seen[e.ID] {
			return fmt.Errorf("examples require unique identities and sources")
		}
		seen[e.ID] = true
		if !e.Present && e.Value != nil {
			return fmt.Errorf("missing example cannot carry a value")
		}
	}
	return nil
}
func semanticDefinitionDigest(d *SemanticDefinition) string {
	if d == nil {
		return ""
	}
	raw, _ := json.Marshal(d)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type Requirements struct {
	Definition *SemanticDefinition `json:"definition,omitempty"`
	Schema     string              `json:"schema"`
	Rules      []Requirement       `json:"rules"`
}

// Requirement is attached to a typed public or mechanism port. Paths select
// values within that port, never arbitrary fields elsewhere in the graph.
type Requirement struct {
	ID        string     `json:"id"`
	Purpose   string     `json:"purpose"`
	Rationale string     `json:"rationale"`
	Origin    string     `json:"origin"`
	Source    string     `json:"source"`
	Parents   []string   `json:"parents,omitempty"`
	Cases     []string   `json:"cases"`
	Mode      string     `json:"mode"`    // runtime, evidence, unresolved
	Missing   string     `json:"missing"` // unknown, error
	Condition *Condition `json:"condition,omitempty"`
	Verifier  string     `json:"verifier,omitempty"`
}

type Condition struct {
	Op    string      `json:"op"`
	Path  string      `json:"path,omitempty"`
	Value any         `json:"value,omitempty"`
	Args  []Condition `json:"args,omitempty"`
}

func (c *Condition) UnmarshalJSON(raw []byte) error {
	type plain Condition
	var value plain
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	d.DisallowUnknownFields()
	if err := d.Decode(&value); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("trailing condition JSON")
	}
	*c = Condition(value)
	return nil
}

type RequirementResult struct {
	ID     string `json:"id"`
	State  string `json:"state"` // satisfied, violated, unknown, unproved
	Reason string `json:"reason"`
}

func ValidateRequirements(r *Requirements) error {
	if r == nil {
		return nil
	}
	if err := ValidateSemanticDefinition(r.Definition); err != nil {
		return err
	}
	if r.Schema != RequirementsSchema || (len(r.Rules) == 0 && r.Definition == nil) {
		return fmt.Errorf("requirements schema and rules required")
	}
	seen := map[string]bool{}
	for _, rule := range r.Rules {
		if strings.TrimSpace(rule.ID) == "" || seen[rule.ID] || strings.TrimSpace(rule.Purpose) == "" || strings.TrimSpace(rule.Rationale) == "" || strings.TrimSpace(rule.Source) == "" {
			return fmt.Errorf("rule needs unique ID, purpose, rationale and source")
		}
		seen[rule.ID] = true
		switch rule.Origin {
		case "explicit", "derived", "inherited", "assumption":
		default:
			return fmt.Errorf("rule %s has invalid origin", rule.ID)
		}
		if rule.Missing != "unknown" && rule.Missing != "error" {
			return fmt.Errorf("rule %s requires missing-data behavior", rule.ID)
		}
		switch rule.Mode {
		case "runtime":
			if rule.Condition == nil || rule.Verifier != "" {
				return fmt.Errorf("runtime rule %s requires condition only", rule.ID)
			}
		case "evidence":
			if rule.Verifier == "" || rule.Condition != nil {
				return fmt.Errorf("evidence rule %s requires verifier only", rule.ID)
			}
		case "unresolved":
			if rule.Condition != nil || rule.Verifier != "" {
				return fmt.Errorf("unresolved rule must not claim enforcement")
			}
		default:
			return fmt.Errorf("rule %s has invalid mode", rule.ID)
		}
		if rule.Origin == "assumption" && rule.Mode != "unresolved" {
			return fmt.Errorf("assumption %s cannot discharge a requirement", rule.ID)
		}
		if rule.Mode != "unresolved" && len(rule.Cases) == 0 {
			return fmt.Errorf("rule %s needs acceptance cases", rule.ID)
		}
		for _, id := range append(append([]string{}, rule.Cases...), rule.Parents...) {
			if strings.TrimSpace(id) == "" {
				return fmt.Errorf("empty rule reference")
			}
		}
		if rule.Condition != nil {
			count := 0
			if err := validateCondition(*rule.Condition, 0, &count); err != nil {
				return fmt.Errorf("rule %s: %w", rule.ID, err)
			}
		}
	}
	return nil
}

func validateCondition(c Condition, depth int, count *int) error {
	*count++
	if depth > 16 || *count > 256 {
		return fmt.Errorf("condition complexity exceeded")
	}
	if _, err := json.Marshal(c); err != nil {
		return err
	}
	if c.Op == "all" || c.Op == "any" || c.Op == "not" {
		if c.Path != "" || c.Value != nil || len(c.Args) == 0 || (c.Op == "not" && len(c.Args) != 1) {
			return fmt.Errorf("invalid logical condition")
		}
		for _, a := range c.Args {
			if err := validateCondition(a, depth+1, count); err != nil {
				return err
			}
		}
		return nil
	}
	if len(c.Args) != 0 || (c.Path != "" && !strings.HasPrefix(c.Path, "/")) {
		return fmt.Errorf("invalid value condition")
	}
	for i := 0; i < len(c.Path); i++ {
		if c.Path[i] == '~' {
			i++
			if i == len(c.Path) || (c.Path[i] != '0' && c.Path[i] != '1') {
				return fmt.Errorf("invalid JSON pointer escape")
			}
		}
	}
	switch c.Op {
	case "present":
		if c.Value != nil {
			return fmt.Errorf("present takes no value")
		}
	case "json_equals":
		// The containing condition was already checked as serializable JSON.
	case "equals", "not_equals":
		if !scalar(c.Value) {
			return fmt.Errorf("comparison requires scalar")
		}
	case "lt", "lte", "gt", "gte":
		if _, ok := number(c.Value); !ok {
			return fmt.Errorf("ordering requires finite number")
		}
	case "in":
		values, ok := c.Value.([]any)
		if !ok || len(values) == 0 {
			return fmt.Errorf("membership needs values")
		}
		for _, v := range values {
			if !scalar(v) {
				return fmt.Errorf("membership requires scalars")
			}
			if !sameScalarKind(values[0], v) {
				return fmt.Errorf("membership values must share a type")
			}
		}
	default:
		return fmt.Errorf("unsupported condition %q", c.Op)
	}
	return nil
}

func scalar(v any) bool {
	switch v.(type) {
	case string, bool:
		return true
	}
	_, ok := number(v)
	return ok
}
func number(v any) (*big.Rat, bool) {
	switch v.(type) {
	case json.Number, float64, float32, int, int64, int32, uint, uint64, uint32:
	default:
		return nil, false
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(string(raw))
	return r, ok
}
func equal(a, b any) bool {
	if x, ok := number(a); ok {
		y, ok := number(b)
		return ok && x.Cmp(y) == 0
	}
	switch x := a.(type) {
	case string:
		y, ok := b.(string)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	}
	return false
}
func pointer(v any, path string) (any, bool) {
	if path == "" {
		return v, v != nil
	}
	for _, s := range strings.Split(path[1:], "/") {
		s = strings.ReplaceAll(strings.ReplaceAll(s, "~1", "/"), "~0", "~")
		switch x := v.(type) {
		case map[string]any:
			var ok bool
			v, ok = x[s]
			if !ok {
				return nil, false
			}
		case []any:
			i, err := strconv.Atoi(s)
			if err != nil || i < 0 || i >= len(x) || strconv.Itoa(i) != s {
				return nil, false
			}
			v = x[i]
		default:
			return nil, false
		}
	}
	return v, v != nil
}
func evaluateCondition(c Condition, value any) string {
	if c.Op == "all" || c.Op == "any" || c.Op == "not" {
		unknown := false
		for _, a := range c.Args {
			s := evaluateCondition(a, value)
			if c.Op == "not" {
				if s == "unknown" {
					return s
				}
				if s == "satisfied" {
					return "violated"
				}
				return "satisfied"
			}
			if c.Op == "all" && s == "violated" {
				return s
			}
			if c.Op == "any" && s == "satisfied" {
				return s
			}
			unknown = unknown || s == "unknown"
		}
		if unknown {
			return "unknown"
		}
		if c.Op == "all" {
			return "satisfied"
		}
		return "violated"
	}
	if c.Op == "json_equals" {
		v, present := jsonPointerPresent(value, c.Path)
		if !present {
			return "unknown"
		}
		if equalJSONValue(v, c.Value) {
			return "satisfied"
		}
		return "violated"
	}
	v, ok := pointer(value, c.Path)
	if c.Op == "present" {
		if ok {
			return "satisfied"
		}
		return "violated"
	}
	if !ok {
		return "unknown"
	}
	if !scalar(v) {
		return "unknown"
	}
	if c.Op == "equals" || c.Op == "not_equals" {
		if !sameScalarKind(v, c.Value) {
			return "unknown"
		}
	}
	match := false
	switch c.Op {
	case "equals":
		match = equal(v, c.Value)
	case "not_equals":
		if !scalar(v) {
			return "unknown"
		}
		match = !equal(v, c.Value)
	case "in":
		if !sameScalarKind(v, c.Value.([]any)[0]) {
			return "unknown"
		}
		for _, item := range c.Value.([]any) {
			match = match || equal(v, item)
		}
	default:
		a, aok := number(v)
		b, bok := number(c.Value)
		if !aok || !bok {
			return "unknown"
		}
		cmp := a.Cmp(b)
		switch c.Op {
		case "lt":
			match = cmp < 0
		case "lte":
			match = cmp <= 0
		case "gt":
			match = cmp > 0
		case "gte":
			match = cmp >= 0
		}
	}
	if match {
		return "satisfied"
	}
	return "violated"
}

func sameScalarKind(a, b any) bool {
	if _, ok := number(a); ok {
		_, ok = number(b)
		return ok
	}
	switch a.(type) {
	case string:
		_, ok := b.(string)
		return ok
	case bool:
		_, ok := b.(bool)
		return ok
	}
	return false
}

// EvaluateRequirements never upgrades absent facts or proof to success. inner
// means the string port transports JSON, as declared by its inner schema.
func EvaluateRequirements(r *Requirements, value any, inner bool) ([]RequirementResult, error) {
	plan, err := PlanRequirements(r)
	if err != nil {
		return nil, err
	}
	return plan.Evaluate(value, inner)
}

// RequirementsPlan is a validated requirements contract: validate once,
// evaluate many values without re-validating the rules per evaluation.
type RequirementsPlan struct {
	requirements *Requirements
}

// PlanRequirements validates a requirements contract once for repeated
// evaluation.
func PlanRequirements(r *Requirements) (RequirementsPlan, error) {
	if err := ValidateRequirements(r); err != nil {
		return RequirementsPlan{}, err
	}
	return RequirementsPlan{requirements: r}, nil
}

// Evaluate runs the planned contract against one value.
func (p RequirementsPlan) Evaluate(value any, inner bool) ([]RequirementResult, error) {
	return evaluateRequirements(p.requirements, value, inner)
}

func evaluateRequirements(r *Requirements, value any, inner bool) ([]RequirementResult, error) {
	if r == nil {
		return nil, nil
	}
	if inner && value != nil {
		s, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("inner value must be JSON string")
		}
		d := json.NewDecoder(strings.NewReader(s))
		d.UseNumber()
		if err := d.Decode(&value); err != nil {
			return nil, err
		}
		if d.Decode(new(any)) != io.EOF {
			return nil, fmt.Errorf("trailing JSON")
		}
	}
	var results []RequirementResult
	if r.Definition != nil && value == nil {
		state := "satisfied"
		if r.Definition.Null == "reject" {
			state = "violated"
		}
		if r.Definition.Null == "unresolved" {
			state = "unknown"
		}
		results = append(results, RequirementResult{ID: r.Definition.ID + ".null", State: state, Reason: "scoped null policy"})
	}
	for _, rule := range r.Rules {
		state, reason := "unproved", "behavioral evidence required"
		if rule.Mode == "unresolved" {
			reason = "decision policy unresolved"
		}
		if rule.Mode == "runtime" {
			state = evaluateCondition(*rule.Condition, value)
			reason = "condition " + state
			if state == "unknown" && rule.Missing == "error" {
				state = "violated"
				reason = "required fact missing or incompatible"
			}
		}
		results = append(results, RequirementResult{rule.ID, state, reason})
	}
	return results, nil
}

// RequirementsExecutable excludes undefined policy, not unknown runtime facts.
func RequirementsExecutable(r *Requirements) error {
	if r != nil && r.Definition != nil {
		d := r.Definition
		for _, v := range []string{d.Entity, d.Unit, d.Currency, d.TimeBasis, d.Missing, d.Null} {
			if v == "unresolved" {
				return fmt.Errorf("semantic definition remains unresolved")
			}
		}
	}
	if err := ValidateRequirements(r); err != nil {
		return err
	}
	if r != nil {
		for _, rule := range r.Rules {
			if rule.Mode == "unresolved" {
				return fmt.Errorf("requirement %s: undefined decision policy", rule.ID)
			}
		}
	}
	return nil
}

// RequirementsAssignable is intentionally conservative. Only an identical
// executable contract establishes a guarantee; prose similarity is not proof.
func RequirementsAssignable(from, to *Requirements) error {
	if err := ValidateRequirements(to); err != nil {
		return err
	}
	if err := ValidateRequirements(from); err != nil {
		return err
	}
	if to == nil {
		return nil
	}
	if to.Definition != nil && (from == nil || semanticDefinitionDigest(from.Definition) != semanticDefinitionDigest(to.Definition)) {
		return fmt.Errorf("semantic definitions differ or are unproved; declare and prove a conversion")
	}
	for _, need := range to.Rules {
		found := false
		if from != nil {
			for _, have := range from.Rules {
				if need.Mode != "unresolved" && have.Origin != "assumption" && RuleBehaviorDigest(have) == RuleBehaviorDigest(need) {
					found = true
					break
				}
			}
		}
		if !found {
			return fmt.Errorf("requirement %s guarantee unproved", need.ID)
		}
	}
	return nil
}

func RuleBehaviorDigest(r Requirement) string {
	r.Purpose = ""
	r.Rationale = ""
	r.Source = ""
	r.Origin = ""
	r.Parents = nil
	b, _ := json.Marshal(r)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func RequirementsBehaviorDigest(r *Requirements) string {
	if r == nil {
		return ""
	}
	var b bytes.Buffer
	b.WriteString(semanticDefinitionDigest(r.Definition))
	for _, rule := range r.Rules {
		b.WriteString(RuleBehaviorDigest(rule))
	}
	s := sha256.Sum256(b.Bytes())
	return hex.EncodeToString(s[:])
}

// json_equals is exact structural JSON equality. Existing scalar operators
// retain their original semantics; object keys, array cardinality and nulls
// are compared explicitly, and numbers retain the existing exact arithmetic.
func equalJSONValue(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			other, present := y[k]
			if !present || !equalJSONValue(v, other) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i, v := range x {
			if !equalJSONValue(v, y[i]) {
				return false
			}
		}
		return true
	default:
		return equal(a, b)
	}
}
func jsonPointerPresent(value any, path string) (any, bool) {
	if path == "" {
		return value, true
	}
	for _, part := range strings.Split(path[1:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch x := value.(type) {
		case map[string]any:
			var present bool
			value, present = x[part]
			if !present {
				return nil, false
			}
		case []any:
			i, e := strconv.Atoi(part)
			if e != nil || i < 0 || i >= len(x) || strconv.Itoa(i) != part {
				return nil, false
			}
			value = x[i]
		default:
			return nil, false
		}
	}
	return value, true
}
