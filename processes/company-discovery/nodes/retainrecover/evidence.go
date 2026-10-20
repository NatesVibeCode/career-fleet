package retainrecover

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

func ValidateRequest(r Request) error {
	if err := validateSources(r); err != nil {
		return err
	}
	if strings.TrimSpace(r.Objective) == "" && len(r.Seeds) == 0 && len(r.Prior) == 0 {
		return fmt.Errorf("discovery: objective or collection required")
	}
	if r.Policy.MaxRequests != nil && *r.Policy.MaxRequests < 0 {
		return fmt.Errorf("discovery: max_requests must be nonnegative")
	}
	switch r.Policy.ResultOrder {
	case "", "discovery", "retrieval_relevance":
	default:
		return fmt.Errorf("discovery: unsupported result order")
	}
	for _, field := range r.Policy.CompareFields {
		if strings.TrimSpace(field) == "" {
			return fmt.Errorf("discovery: comparison field must be nonempty")
		}
	}
	reusable := map[string]bool{}
	captureIDs := map[string]bool{}
	for _, capture := range r.ReusableCaptures {
		if capture.ID == "" || captureIDs[capture.ID] || reusable[capture.URL] || capture.Outcome != "success" || !capture.Complete {
			return fmt.Errorf("discovery: reuse selects one exact complete successful capture per URL")
		}
		if err := ValidateURL(capture.URL); err != nil {
			return err
		}
		if capture.BodyRecord != "" && capture.BodyBase64 == "" {
			if len(capture.BodyRecord) != 64 || capture.BodyRecord != capture.SHA256 || len(capture.BodyRecordDigest) != 64 {
				return fmt.Errorf("discovery: invalid retained body reference")
			}
			reusable[capture.URL] = true
			captureIDs[capture.ID] = true
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(capture.BodyBase64)
		if err != nil {
			return fmt.Errorf("discovery: invalid reusable capture bytes")
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != capture.SHA256 {
			return fmt.Errorf("discovery: reusable capture digest mismatch")
		}
		reusable[capture.URL] = true
		captureIDs[capture.ID] = true
	}
	if err := ValidateConcepts(r.Concepts); err != nil {
		return err
	}
	sources := map[string]bool{}
	for _, s := range r.Sources {
		if s.ID == "" || sources[s.ID] {
			return fmt.Errorf("discovery: source id must be unique and nonempty")
		}
		sources[s.ID] = true
		switch s.Kind {
		case "rss", "json", "json_mapped", "html", "html_search", "browser_html_search", "collection_html", "embedded_json_mapped", "ocds_json", "text":
		default:
			return fmt.Errorf("discovery: unsupported source kind %q", s.Kind)
		}
		pageable := strings.Contains(s.URLTemplate, "{page}") || strings.Contains(s.URLTemplate, "{offset}") || s.Pagination != nil && (s.Pagination.Mode == "next_link" || s.Pagination.Mode == "cursor")
		if s.Pages < 0 || s.PageSize < 0 || (s.Pages > 1 && !pageable) || (s.Pages > 1 && strings.Contains(s.URLTemplate, "{offset}") && s.PageSize == 0) {
			return fmt.Errorf("source %s: pagination requires nonnegative pages and page_size plus a {page} or {offset} template", s.ID)
		}
		target := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(s.URLTemplate, "{query}", "query"), "{page}", "1"), "{offset}", "0")
		if err := ValidateURL(target); err != nil {
			return fmt.Errorf("source %s: %w", s.ID, err)
		}
		if (s.Kind == "json_mapped" || s.Kind == "embedded_json_mapped") && s.Mapping == nil {
			return fmt.Errorf("source %s: json_mapped requires an explicit mapping", s.ID)
		}
		if s.ExternalLinksOnly && s.Kind != "html_search" && s.Kind != "browser_html_search" {
			return fmt.Errorf("source %s: external_links_only applies only to HTML search sources", s.ID)
		}
		if s.BrowserMapping != nil {
			if s.Kind != "browser_html_search" || strings.TrimSpace(s.BrowserMapping.ItemSelector) == "" || strings.TrimSpace(s.BrowserMapping.TitleSelector) == "" || (strings.TrimSpace(s.BrowserMapping.URLSelector) == "" && strings.TrimSpace(s.BrowserMapping.KeyAttribute) == "") {
				return fmt.Errorf("source %s: browser_mapping requires browser_html_search, item/title selectors, and either a URL selector or key attribute", s.ID)
			}
		}
		if s.Mapping != nil {
			if (s.Kind != "json_mapped" && s.Kind != "embedded_json_mapped") || !validJSONPointer(s.Mapping.ItemsPath) || !validJSONPointer(s.Mapping.TitlePath) || !validJSONPointer(s.Mapping.URLPath) || !validJSONPointer(s.Mapping.TextPath) || !validJSONPointer(s.Mapping.DatePath) || !validJSONPointer(s.Mapping.NamePath) || !validJSONPointer(s.Mapping.LeadKeyPath) {
				return fmt.Errorf("source %s: JSON mapping paths must be RFC 6901 pointers on a json_mapped source", s.ID)
			}
			if s.Mapping.URLPath == "" {
				return fmt.Errorf("source %s: JSON mapping requires url_path", s.ID)
			}
			if s.Mapping.ItemURLTemplate != "" {
				if !strings.Contains(s.Mapping.ItemURLTemplate, "{key}") || ValidateURL(strings.ReplaceAll(s.Mapping.ItemURLTemplate, "{key}", "record")) != nil {
					return fmt.Errorf("source %s: item_url_template must be HTTPS and contain {key}", s.ID)
				}
			}
		}
	}
	criteria := map[string]bool{}
	for _, c := range r.Criteria {
		if c.ID == "" || criteria[c.ID] || c.Field == "" {
			return fmt.Errorf("discovery: criterion needs unique id and field")
		}
		criteria[c.ID] = true
		switch c.Role {
		case "required", "preference", "descriptive":
		default:
			return fmt.Errorf("discovery: unsupported criterion role %q", c.Role)
		}
		switch c.Operator {
		case "equals", "contains", "exists":
		default:
			return fmt.Errorf("discovery: unsupported criterion operator %q", c.Operator)
		}
		if c.Operator != "exists" && strings.TrimSpace(c.Value) == "" {
			return fmt.Errorf("discovery: comparison criterion requires a nonempty value")
		}
	}
	feedbackIDs := map[string]bool{}
	for _, feedback := range r.Feedback {
		if feedback.ID == "" || feedbackIDs[feedback.ID] || feedback.ObservationID == "" || feedback.Reason == "" {
			return fmt.Errorf("discovery: scoped feedback requires unique id, observation and reason")
		}
		feedbackIDs[feedback.ID] = true
		if feedback.Decision != "withdrawn" && feedback.Decision != "disputed" {
			return fmt.Errorf("discovery: unsupported feedback decision")
		}
		if feedback.Kind != "retrieval" && feedback.Kind != "fact" {
			return fmt.Errorf("discovery: unsupported feedback kind")
		}
		if feedback.Kind == "fact" && feedback.Field == "" {
			return fmt.Errorf("discovery: fact feedback requires exact field and scope")
		}
		if feedback.Kind == "retrieval" && (feedback.Field != "" || feedback.Scope != "") {
			return fmt.Errorf("discovery: retrieval feedback selects an observation, not a fact scope")
		}
	}
	bindings := map[string]string{}
	for _, binding := range r.SubjectBindings {
		if err := ValidateURL(binding.URL); err != nil {
			return err
		}
		if binding.EntityID == "" {
			return fmt.Errorf("discovery: subject binding requires an explicit entity id")
		}
		if prior, ok := bindings[binding.URL]; ok && prior != Digest(binding) {
			return fmt.Errorf("discovery: conflicting subject bindings for exact URL")
		}
		bindings[binding.URL] = Digest(binding)
	}
	questions := map[string]bool{}
	rules := map[string]bool{}
	for _, q := range r.Questions {
		if q.ID == "" || questions[q.ID] || q.Field == "" {
			return fmt.Errorf("discovery: question needs unique id and field")
		}
		switch q.Acquisition {
		case "", "query", "source_document":
		default:
			return fmt.Errorf("discovery: unsupported question acquisition")
		}
		if q.Acquisition == "query" && (q.SourceID == "" || q.QueryTemplate == "") {
			return fmt.Errorf("discovery: query acquisition requires selected source and query template")
		}
		if q.Acquisition != "query" && q.QueryTemplate != "" {
			return fmt.Errorf("discovery: query template requires query acquisition")
		}
		questions[q.ID] = true
		for _, term := range q.LinkTerms {
			if strings.TrimSpace(term) == "" {
				return fmt.Errorf("discovery: selected link terms must be nonempty")
			}
		}
		if q.SourceID != "" && !sources[q.SourceID] {
			return fmt.Errorf("discovery: question %s refers to unknown source", q.ID)
		}
		for _, rule := range q.Rules {
			re, err := regexp.Compile(rule.Pattern)
			if err != nil {
				return fmt.Errorf("rule %s: %w", rule.ID, err)
			}
			if rule.ID == "" || rule.Field != q.Field || rule.Group < 0 || rule.Group > re.NumSubexp() {
				return fmt.Errorf("discovery: rule requires id, question field and valid capture group")
			}
			if rules[rule.ID] || rule.Scope != q.Scope {
				return fmt.Errorf("discovery: extraction rule ids must be unique and scope must match its question")
			}
			rules[rule.ID] = true
		}
	}
	observations := map[string]string{}
	all := append([]Observation{}, r.Seeds...)
	for _, c := range r.Prior {
		all = append(all, c.Observations...)
	}
	for _, o := range all {
		if o.ID == "" {
			continue
		}
		if old, ok := observations[o.ID]; ok && old != Digest(o) {
			return fmt.Errorf("discovery: observation id %q binds different evidence", o.ID)
		}
		observations[o.ID] = Digest(o)
	}
	return nil
}

func validJSONPointer(path string) bool {
	if path == "" {
		return true
	}
	if !strings.HasPrefix(path, "/") {
		return false
	}
	for i := 0; i < len(path); i++ {
		if path[i] != '~' {
			continue
		}
		i++
		if i >= len(path) || (path[i] != '0' && path[i] != '1') {
			return false
		}
	}
	return true
}

func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("discovery: authorized source must be an absolute HTTPS URL without credentials")
	}
	return nil
}

// Identity is asserted only by an explicit entity id. An article URL, domain,
// title or shared name alone cannot establish that two companies are one.
func MergeObservation(candidates []Candidate, o Observation) []Candidate {
	if o.ID == "" {
		o.ID = "observation:" + Digest(o)
	}
	id := "mention:" + o.ID
	identity := "unresolved"
	if o.EntityID != "" {
		id = "entity:" + o.EntityID
		identity = "source_asserted"
	} else if o.LeadID != "" {
		// A stable source record key groups repeated mentions of that record.
		// It remains unresolved and does not assert company identity.
		id = o.LeadID
	}
	// Dossier membership does not establish company identity.
	if o.EntityID == "" && o.LeadID != "" {
		for _, c := range candidates {
			if c.ID == o.LeadID && c.IdentityState == "unresolved" {
				id = c.ID
			}
		}
	}
	index := -1
	for i := range candidates {
		if candidates[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		name := o.EntityName
		if name == "" {
			name = o.Title
		}
		candidates = append(candidates, Candidate{ID: id, Name: name, IdentityState: identity, Disposition: "unassessed"})
		index = len(candidates) - 1
	}
	c := &candidates[index]
	if o.EntityName != "" {
		c.Name = o.EntityName
	} else if c.Name == "" {
		c.Name = o.Title
	}
	for _, prior := range c.Observations {
		if prior.ID == o.ID {
			return candidates
		}
	}
	c.Observations = append(c.Observations, o)
	if o.URL != "" {
		found := false
		for _, u := range c.URLs {
			if u == o.URL {
				found = true
			}
		}
		if !found {
			c.URLs = append(c.URLs, o.URL)
		}
	}
	return candidates
}

// ExtractAssertions binds each value to the actual captured text and byte span.
// A regex is an explicit extraction rule, never independent verification of a claim.
func ExtractAssertions(c Candidate, questions []Question) []Assertion {
	out := []Assertion{}
	seen := map[string]bool{}
	for _, o := range c.Observations {
		// A page found while investigating a lead has no company assertion authority.
		if o.EntityID == "" {
			continue
		}
		for _, q := range questions {
			for _, rule := range q.Rules {
				if rule.Scope != o.Scope {
					continue
				}
				re, err := regexp.Compile(rule.Pattern)
				if err != nil {
					continue
				}
				for _, span := range re.FindAllStringSubmatchIndex(o.Text, -1) {
					begin, end := span[rule.Group*2], span[rule.Group*2+1]
					if begin < 0 || begin == end {
						continue
					}
					a := Assertion{SourceID: o.SourceID, CaptureID: o.CaptureID, URL: o.URL, PublishedAt: o.PublishedAt, EntityID: c.ID, Field: rule.Field, Value: o.Text[begin:end], Scope: o.Scope, ObservationID: o.ID, StartByte: begin, EndByte: end, Quote: o.Text[begin:end], RuleID: rule.ID}
					a.ID = "assertion:" + Digest(a)
					if !seen[a.ID] {
						out = append(out, a)
						seen[a.ID] = true
					}
				}
			}
		}
	}
	return out
}

func EvaluateConditions(assertions []Assertion, criteria []Criterion) []Condition {
	return ConditionsWithFeedback(assertions, criteria, nil)
}
func ConditionsWithFeedback(assertions []Assertion, criteria []Criterion, feedback []Feedback) []Condition {
	out := []Condition{}
	for _, c := range criteria {
		condition := Condition{Field: c.Field, Scope: c.Scope, Role: c.Role, CriterionID: c.ID, State: "unknown", Reason: "no assertion in the requested field and scope"}
		yes, no := false, false
		for _, a := range assertions {
			if a.Field != c.Field || a.Scope != c.Scope {
				continue
			}
			suppressed := false
			for _, f := range feedback {
				if f.Kind == "fact" && f.ObservationID == a.ObservationID && f.Field == a.Field && f.Scope == a.Scope {
					suppressed = true
					condition.FeedbackIDs = append(condition.FeedbackIDs, f.ID)
				}
			}
			if suppressed {
				continue
			}
			condition.AssertionIDs = append(condition.AssertionIDs, a.ID)
			match := false
			switch c.Operator {
			case "equals":
				match = strings.EqualFold(strings.TrimSpace(a.Value), strings.TrimSpace(c.Value))
			case "contains":
				match = strings.Contains(strings.ToLower(a.Value), strings.ToLower(c.Value))
			case "exists":
				match = strings.TrimSpace(a.Value) != ""
			}
			if match {
				yes = true
			} else {
				no = true
			}
		}
		switch {
		case yes && no:
			condition.State = "conflict"
			condition.Reason = "in-scope assertions disagree on this condition"
		case yes:
			condition.State = "supported"
			condition.Reason = "in-scope assertion satisfies the explicit condition"
		case no:
			condition.State = "contradicted"
			condition.Reason = "in-scope assertion contradicts the explicit condition"
		}
		out = append(out, condition)
	}
	return out
}

func AssessCandidate(c Candidate, r Request, concepts []Concept) Candidate {
	c = MatchCandidate(c, concepts)
	c.Assertions = ExtractAssertions(c, r.Questions)
	return ConditionCandidate(c, r, concepts)
}

func MatchCandidate(c Candidate, concepts []Concept) Candidate {
	c.Matches = []Match{}
	for _, o := range c.Observations {
		c.Matches = append(c.Matches, FindMatches(concepts, o)...)
	}
	return c
}

func ConditionCandidate(c Candidate, r Request, concepts []Concept) Candidate {
	c.Conditions = ConditionsWithFeedback(c.Assertions, r.Criteria, r.Feedback)
	c.Gaps = []string{}
	if c.IdentityState == "unresolved" {
		c.Gaps = append(c.Gaps, "company identity unresolved")
	}
	for _, field := range r.Policy.RequestedFields {
		found := false
		for _, a := range c.Assertions {
			if a.Field == field {
				found = true
			}
		}
		if !found {
			c.Gaps = append(c.Gaps, "missing requested field: "+field)
		}
	}
	c.Disposition = "eligible"
	c.Reason = "retained for comparison; missing evidence does not reject a candidate"
	if len(concepts) > 0 && !RelevantWithFeedback(c.Matches, r.Feedback) {
		c.Disposition = "unresolved_relevance"
		c.Reason = "no positive lexical cue; candidate remains visible"
	}
	for i, condition := range c.Conditions {
		if condition.State == "unknown" || condition.State == "conflict" {
			c.Gaps = append(c.Gaps, r.Criteria[i].ID+": "+condition.State)
		}
		if r.Criteria[i].Role == "required" && condition.State == "contradicted" {
			c.Disposition = "explicit_condition_contradicted"
			c.Reason = fmt.Sprintf("required condition %s contradicted at scope %q; candidate retained for unrelated research", r.Criteria[i].ID, r.Criteria[i].Scope)
		}
	}
	sort.Strings(c.Gaps)
	return c
}

func AssertionFeedback(a Assertion, feedback []Feedback) []string {
	ids := []string{}
	for _, f := range feedback {
		if f.Kind == "fact" && f.ObservationID == a.ObservationID && f.Field == a.Field && f.Scope == a.Scope {
			ids = append(ids, f.ID)
		}
	}
	return ids
}
