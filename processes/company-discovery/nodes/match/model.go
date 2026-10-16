package match

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
)

// Request contains task data, never an ambient profile or a personal default.
type Request struct {
	Feedback         []Feedback       `json:"feedback"`
	ReusableCaptures []Capture        `json:"reusable_captures"`
	Objective        string           `json:"objective"`
	Concepts         []Concept        `json:"concepts"`
	Criteria         []Criterion      `json:"criteria"`
	Sources          []Source         `json:"sources"`
	Seeds            []Observation    `json:"seeds"`
	Questions        []Question       `json:"questions"`
	Policy           Policy           `json:"policy"`
	Prior            []Candidate      `json:"prior"`
	SubjectBindings  []SubjectBinding `json:"subject_bindings"`
}

// SubjectBinding is explicit caller attribution for one exact document URL.
// It establishes the intended subject, not the truth or independence of claims.
type Feedback struct {
	ID            string `json:"id"`
	ObservationID string `json:"observation_id"`
	Kind          string `json:"kind"`
	Field         string `json:"field"`
	Scope         string `json:"scope"`
	Decision      string `json:"decision"`
	Reason        string `json:"reason"`
}
type SubjectBinding struct {
	URL        string `json:"url"`
	EntityID   string `json:"entity_id"`
	EntityName string `json:"entity_name"`
	Scope      string `json:"scope"`
}
type Concept struct {
	ID        string     `json:"id"`
	Terms     []string   `json:"terms"`
	Relations []Relation `json:"relations"`
}
type Relation struct {
	Term       string `json:"term"`
	Kind       string `json:"kind"`
	Provenance string `json:"provenance"`
}
type Criterion struct {
	ID       string `json:"id"`
	Field    string `json:"field"`
	Role     string `json:"role"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
	Scope    string `json:"scope"`
}
type Source struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	URLTemplate string   `json:"url_template"`
	Queries     []string `json:"queries"`
}
type Policy struct {
	ResultOrder        string   `json:"result_order"`
	CompareFields      []string `json:"compare_fields"`
	AcquisitionEnabled bool     `json:"acquisition_enabled"`
	MaxRequests        *int     `json:"max_requests,omitempty"`
	FollowRedirects    bool     `json:"follow_redirects"`
	RequestedFields    []string `json:"requested_fields"`
}
type Question struct {
	Acquisition   string        `json:"acquisition"`
	ID            string        `json:"id"`
	Field         string        `json:"field"`
	Scope         string        `json:"scope"`
	QueryTemplate string        `json:"query_template"`
	SourceID      string        `json:"source_id"`
	LinkTerms     []string      `json:"link_terms"`
	Rules         []ExtractRule `json:"rules"`
}
type ExtractRule struct {
	ID      string `json:"id"`
	Field   string `json:"field"`
	Pattern string `json:"pattern"`
	Group   int    `json:"group"`
	Scope   string `json:"scope"`
}
type Observation struct {
	LeadID      string `json:"lead_id"`
	ActionID    string `json:"action_id"`
	ID          string `json:"id"`
	SourceID    string `json:"source_id"`
	CaptureID   string `json:"capture_id"`
	URL         string `json:"url"`
	Title       string `json:"title"`
	Text        string `json:"text"`
	Field       string `json:"field"`
	PublishedAt string `json:"published_at"`
	EntityID    string `json:"entity_id"`
	EntityName  string `json:"entity_name"`
	Scope       string `json:"scope"`
}
type Capture struct {
	ID               string   `json:"id"`
	URL              string   `json:"url"`
	FinalURL         string   `json:"final_url"`
	CapturedAt       string   `json:"captured_at"`
	Status           int      `json:"status"`
	MediaType        string   `json:"media_type"`
	BodyBase64       string   `json:"body_base64"`
	SHA256           string   `json:"sha256"`
	Outcome          string   `json:"outcome"`
	ErrorCode        string   `json:"error_code"`
	RedirectLocation string   `json:"redirect_location"`
	ReceiptRefs      []string `json:"receipt_refs"`
	Complete         bool     `json:"complete"`
	Requests         int      `json:"requests"`
}
type Match struct {
	ConceptID     string `json:"concept_id"`
	Term          string `json:"term"`
	Relation      string `json:"relation"`
	Provenance    string `json:"provenance"`
	ObservationID string `json:"observation_id"`
	Field         string `json:"field"`
	StartByte     int    `json:"start_byte"`
	EndByte       int    `json:"end_byte"`
	Quote         string `json:"quote"`
	Negated       bool   `json:"negated"`
}
type Assertion struct {
	SourceID      string `json:"source_id"`
	CaptureID     string `json:"capture_id"`
	URL           string `json:"url"`
	PublishedAt   string `json:"published_at"`
	ID            string `json:"id"`
	EntityID      string `json:"entity_id"`
	Field         string `json:"field"`
	Value         string `json:"value"`
	Scope         string `json:"scope"`
	ObservationID string `json:"observation_id"`
	StartByte     int    `json:"start_byte"`
	EndByte       int    `json:"end_byte"`
	Quote         string `json:"quote"`
	RuleID        string `json:"rule_id"`
}
type Condition struct {
	FeedbackIDs  []string `json:"feedback_ids"`
	Field        string   `json:"field"`
	Scope        string   `json:"scope"`
	Role         string   `json:"role"`
	CriterionID  string   `json:"criterion_id"`
	State        string   `json:"state"`
	AssertionIDs []string `json:"assertion_ids"`
	Reason       string   `json:"reason"`
}
type Candidate struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	IdentityState   string        `json:"identity_state"`
	URLs            []string      `json:"urls"`
	Observations    []Observation `json:"observations"`
	Matches         []Match       `json:"matches"`
	Assertions      []Assertion   `json:"assertions"`
	Conditions      []Condition   `json:"conditions"`
	Gaps            []string      `json:"gaps"`
	Disposition     string        `json:"disposition"`
	Reason          string        `json:"reason"`
	ResearchActions int           `json:"research_actions"`
}
type Action struct {
	RootKind    string `json:"root_kind"`
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	SourceID    string `json:"source_id"`
	CandidateID string `json:"candidate_id"`
	QuestionID  string `json:"question_id"`
	Query       string `json:"query"`
	URL         string `json:"url"`
	Status      string `json:"status"`
	Reason      string `json:"reason"`
	CaptureID   string `json:"capture_id"`
	Tier        int    `json:"tier"`
}
type Link struct {
	URL  string `json:"url"`
	Text string `json:"text"`
}
type Document struct {
	CaptureID string        `json:"capture_id"`
	Outcome   string        `json:"outcome"`
	Title     string        `json:"title"`
	Text      string        `json:"text"`
	Links     []Link        `json:"links"`
	Results   []Observation `json:"results"`
	Coverage  string        `json:"coverage"`
	Gap       string        `json:"gap"`
}
type State struct {
	ReusedCaptureIDs []string      `json:"reused_capture_ids"`
	Request          Request       `json:"request"`
	RequestDigest    string        `json:"request_digest"`
	Concepts         []Concept     `json:"concepts"`
	Candidates       []Candidate   `json:"candidates"`
	Actions          []Action      `json:"actions"`
	Captures         []Capture     `json:"captures"`
	Pending          []Observation `json:"pending"`
	CurrentAction    int           `json:"current_action"`
	CurrentCandidate int           `json:"current_candidate"`
	Capture          Capture       `json:"capture"`
	Document         Document      `json:"document"`
	NetworkRequests  int           `json:"network_requests"`
	Gaps             []string      `json:"gaps"`
	Phase            string        `json:"phase"`
}
type FieldComparison struct {
	FeedbackIDs  []string `json:"feedback_ids"`
	Scope        string   `json:"scope"`
	CandidateID  string   `json:"candidate_id"`
	Field        string   `json:"field"`
	State        string   `json:"state"`
	Values       []string `json:"values"`
	AssertionIDs []string `json:"assertion_ids"`
}
type Result struct {
	Feedback         []Feedback        `json:"feedback"`
	Comparisons      []FieldComparison `json:"comparisons"`
	ReusedCaptureIDs []string          `json:"reused_capture_ids"`
	RequestDigest    string            `json:"request_digest"`
	Candidates       []Candidate       `json:"candidates"`
	Actions          []Action          `json:"actions"`
	Captures         []Capture         `json:"captures"`
	Gaps             []string          `json:"gaps"`
	Coverage         string            `json:"coverage"`
	NetworkRequests  int               `json:"network_requests"`
	InferenceCalls   int               `json:"inference_calls"`
	Report           string            `json:"report"`
}

func Digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func Decode(in map[string]any, value any) error {
	b, e := json.Marshal(in)
	if e != nil {
		return e
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(value); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("discovery: trailing input")
	}
	return nil
}

// Every nullable value in this closed model is an unset slice. Optional scalar
// pointers use omitempty and are absent before this normalization.
func normalizeArrays(v any) any {
	switch x := v.(type) {
	case nil:
		return []any{}
	case map[string]any:
		for k, value := range x {
			x[k] = normalizeArrays(value)
		}
		return x
	case []any:
		for i := range x {
			x[i] = normalizeArrays(x[i])
		}
		return x
	default:
		return v
	}
}
func Output(v any) (map[string]any, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	m := map[string]any{}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if e = d.Decode(&m); e != nil {
		return nil, e
	}
	normalizeArrays(m)
	return m, nil
}
