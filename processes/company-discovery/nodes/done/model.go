package done

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
	SourceBlocks     []SourceBlockSelection `json:"source_blocks,omitempty"`
	Mode             string                 `json:"mode,omitempty"`
	SourceFamilies   []string               `json:"source_families,omitempty"`
	Feedback         []Feedback             `json:"feedback"`
	ReusableCaptures []Capture              `json:"reusable_captures"`
	Objective        string                 `json:"objective"`
	Concepts         []Concept              `json:"concepts"`
	Criteria         []Criterion            `json:"criteria"`
	Sources          []Source               `json:"sources"`
	Seeds            []Observation          `json:"seeds"`
	Questions        []Question             `json:"questions"`
	Policy           Policy                 `json:"policy"`
	Prior            []Candidate            `json:"prior"`
	SubjectBindings  []SubjectBinding       `json:"subject_bindings"`
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
	Block             *BlockReference    `json:"block,omitempty"`
	BlockKey          string             `json:"block_key,omitempty"`
	EmbeddedPrefix    string             `json:"embedded_prefix,omitempty"`
	SubjectName       string             `json:"subject_name,omitempty"`
	EmbeddedSelector  string             `json:"embedded_selector,omitempty"`
	DetailURLTemplate string             `json:"detail_url_template,omitempty"`
	Family            string             `json:"family,omitempty"`
	Preset            string             `json:"preset,omitempty"`
	SourceDigest      string             `json:"source_digest,omitempty"`
	RecordKind        string             `json:"record_kind,omitempty"`
	HTMLMapping       *HTMLResultMapping `json:"html_mapping,omitempty"`
	Readiness         *Readiness         `json:"readiness,omitempty"`
	Pagination        *Pagination        `json:"pagination,omitempty"`
	ID                string             `json:"id"`
	Kind              string             `json:"kind"`
	URLTemplate       string             `json:"url_template"`
	Queries           []string           `json:"queries"`
	Pages             int                `json:"pages,omitempty"`
	PageSize          int                `json:"page_size,omitempty"`
	ExternalLinksOnly bool               `json:"external_links_only,omitempty"`
	Mapping           *JSONMapping       `json:"mapping,omitempty"`
	BrowserMapping    *HTMLResultMapping `json:"browser_mapping,omitempty"`
}

// JSONMapping is an explicit RFC 6901 projection for one public JSON source.
// The source response remains byte-pinned in Capture; this mapping only chooses
// which returned fields become candidate observations.
type JSONMapping struct {
	ItemsPath       string `json:"items_path"`
	TitlePath       string `json:"title_path"`
	URLPath         string `json:"url_path"`
	ItemURLTemplate string `json:"item_url_template,omitempty"`
	TextPath        string `json:"text_path"`
	DatePath        string `json:"date_path"`
	NamePath        string `json:"name_path"`
	LeadKeyPath     string `json:"lead_key_path"`
}

// HTMLResultMapping declares source-owned CSS extraction for rendered result
// cards that do not expose their target as an anchor link.
type HTMLResultMapping struct {
	TitleAttribute  string `json:"title_attribute,omitempty"`
	LabelSelector   string `json:"label_selector,omitempty"`
	LabelText       string `json:"label_text,omitempty"`
	SplitText       string `json:"split_text,omitempty"`
	WebsiteSelector string `json:"website_selector,omitempty"`
	URLAttribute    string `json:"url_attribute,omitempty"`
	ItemSelector    string `json:"item_selector"`
	TitleSelector   string `json:"title_selector"`
	URLSelector     string `json:"url_selector,omitempty"`
	TextSelector    string `json:"text_selector,omitempty"`
	KeyAttribute    string `json:"key_attribute,omitempty"`
}
type Policy struct {
	ResolveIdentity    bool     `json:"resolve_identity,omitempty"`
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
	LinkURL     string `json:"link_url,omitempty"`
	RecordKind  string `json:"record_kind,omitempty"`
	RecordKey   string `json:"record_key,omitempty"`
	WebsiteURL  string `json:"website_url,omitempty"`
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
	BodyRecord       string   `json:"body_record,omitempty"`
	BodyRecordDigest string   `json:"body_record_digest,omitempty"`
	SourceDigest     string   `json:"source_digest,omitempty"`
	AcquisitionMode  string   `json:"acquisition_mode,omitempty"`
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
	Preset      string        `json:"preset"`
	Health      string        `json:"health"`
	NextURL     string        `json:"next_url"`
	Fingerprint string        `json:"fingerprint"`
	CaptureID   string        `json:"capture_id"`
	Outcome     string        `json:"outcome"`
	Title       string        `json:"title"`
	Text        string        `json:"text"`
	Links       []Link        `json:"links"`
	Results     []Observation `json:"results"`
	Coverage    string        `json:"coverage"`
	Gap         string        `json:"gap"`
}
type State struct {
	SourceCandidates []SourceCandidate  `json:"source_candidates"`
	SourceHealth     []SourceHealth     `json:"source_health"`
	Relationships    []IdentityRelation `json:"relationships"`
	ReusedCaptureIDs []string           `json:"reused_capture_ids"`
	Request          Request            `json:"request"`
	RequestDigest    string             `json:"request_digest"`
	Concepts         []Concept          `json:"concepts"`
	Candidates       []Candidate        `json:"candidates"`
	Actions          []Action           `json:"actions"`
	Captures         []Capture          `json:"captures"`
	Pending          []Observation      `json:"pending"`
	CurrentAction    int                `json:"current_action"`
	CurrentCandidate int                `json:"current_candidate"`
	Capture          Capture            `json:"capture"`
	Document         Document           `json:"document"`
	NetworkRequests  int                `json:"network_requests"`
	Gaps             []string           `json:"gaps"`
	Phase            string             `json:"phase"`
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
	SourceCandidates []SourceCandidate  `json:"source_candidates"`
	SourceHealth     []SourceHealth     `json:"source_health"`
	Relationships    []IdentityRelation `json:"relationships"`
	Summary          DiscoverySummary   `json:"summary"`
	Feedback         []Feedback         `json:"feedback"`
	Comparisons      []FieldComparison  `json:"comparisons"`
	ReusedCaptureIDs []string           `json:"reused_capture_ids"`
	RequestDigest    string             `json:"request_digest"`
	Candidates       []Candidate        `json:"candidates"`
	Actions          []Action           `json:"actions"`
	Captures         []Capture          `json:"captures"`
	Gaps             []string           `json:"gaps"`
	Coverage         string             `json:"coverage"`
	NetworkRequests  int                `json:"network_requests"`
	InferenceCalls   int                `json:"inference_calls"`
	Report           string             `json:"report"`
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

// Readiness declares observable completion. The timeout is recipe data, not a sleep.
type Readiness struct {
	ResultsSelector string `json:"results_selector"`
	EmptySelector   string `json:"empty_selector"`
	BlockedSelector string `json:"blocked_selector"`
	TimeoutMS       int    `json:"timeout_ms"`
}
type Pagination struct {
	NextStrategy    string `json:"next_strategy,omitempty"`
	Mode            string `json:"mode"`
	NextSelector    string `json:"next_selector"`
	NextPath        string `json:"next_path"`
	CursorPath      string `json:"cursor_path"`
	CursorParameter string `json:"cursor_parameter"`
}
type SourceOrigin struct {
	SourceID  string `json:"source_id"`
	ActionID  string `json:"action_id"`
	CaptureID string `json:"capture_id"`
	Family    string `json:"family"`
	Query     string `json:"query"`
}
type SourceCandidate struct {
	Block          *BlockReference `json:"block,omitempty"`
	Origins        []SourceOrigin  `json:"origins,omitempty"`
	DetailURLs     []string        `json:"detail_urls,omitempty"`
	NegativeCheck  bool            `json:"negative_check"`
	ID             string          `json:"id"`
	URL            string          `json:"url"`
	Family         string          `json:"family"`
	Status         string          `json:"status"`
	Preset         string          `json:"preset"`
	CaptureIDs     []string        `json:"capture_ids"`
	ProposedSource *Source         `json:"proposed_source,omitempty"`
	Reason         string          `json:"reason"`
}
type SourceHealth struct {
	Block        *BlockReference `json:"block,omitempty"`
	Query        string          `json:"query"`
	ActionID     string          `json:"action_id"`
	SourceID     string          `json:"source_id"`
	Family       string          `json:"family"`
	SourceDigest string          `json:"source_digest"`
	URL          string          `json:"url"`
	Outcome      string          `json:"outcome"`
	Records      int             `json:"records"`
	Requests     int             `json:"requests"`
	Fingerprint  string          `json:"fingerprint"`
	NextURL      string          `json:"next_url"`
	Reason       string          `json:"reason"`
	CaptureID    string          `json:"capture_id"`
}
type IdentityRelation struct {
	ID            string `json:"id"`
	SubjectID     string `json:"subject_id"`
	Predicate     string `json:"predicate"`
	ObjectID      string `json:"object_id"`
	Name          string `json:"name"`
	URL           string `json:"url"`
	SourceID      string `json:"source_id"`
	CaptureID     string `json:"capture_id"`
	ObservationID string `json:"observation_id"`
	State         string `json:"state"`
	Reason        string `json:"reason"`
}
type DiscoverySummary struct {
	SourceRecords           int      `json:"source_records"`
	UnresolvedOrganizations int      `json:"unresolved_organizations"`
	ResolvedOrganizations   int      `json:"resolved_organizations"`
	OfficialWebsites        int      `json:"official_websites"`
	NewOrganizations        int      `json:"new_organizations"`
	CrossSourceOverlaps     int      `json:"cross_source_overlaps"`
	NetworkRequests         int      `json:"network_requests"`
	VerifiedFamilies        []string `json:"verified_families"`
}

// Block identity is the exact Forge-published reference, not a mapping digest.
type BlockReference struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}
type SourceBlockSelection struct {
	Block   BlockReference `json:"block"`
	Pages   int            `json:"pages,omitempty"`
	Queries []string       `json:"queries,omitempty"`
}
type SourceBlockDefinition struct {
	Block  BlockReference
	Key    string
	Source Source
}
