package parse

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func Open(r Request) (State, error) {
	r = SourceDiscoveryRequest(r)
	if err := ValidateRequest(r); err != nil {
		return State{}, err
	}
	concepts := AddGenericQueryConcepts(r.Objective, r.Concepts)
	s := State{Request: r, RequestDigest: Digest(r), Concepts: concepts, CurrentAction: -1, CurrentCandidate: -1, Phase: "select"}
	// Prior records are evidence inputs, never permission to reuse old judgments.
	for _, c := range r.Prior {
		for _, o := range c.Observations {
			s.Candidates = MergeObservation(s.Candidates, o)
		}
	}
	for _, o := range r.Seeds {
		s.Candidates = MergeObservation(s.Candidates, o)
	}
	for _, source := range r.Sources {
		patterns := source.Queries
		if len(patterns) == 0 && r.Objective != "" {
			patterns = []string{"{objective}"}
		}
		pages := source.Pages
		if pages == 0 {
			pages = 1
		}
		seenQueries := map[string]bool{}
		seenTargets := map[string]bool{}
		for _, pattern := range patterns {
			seed := strings.TrimSpace(strings.ReplaceAll(pattern, "{objective}", r.Objective))
			if seed == "" {
				continue
			}
			for _, query := range ExpandedQueries(seed, concepts) {
				if seenQueries[query] {
					continue
				}
				seenQueries[query] = true
				for page := 1; page <= pages; page++ {
					if source.Pagination != nil && (source.Pagination.Mode == "next_link" || source.Pagination.Mode == "cursor") && page > 1 {
						break
					}
					target := strings.ReplaceAll(source.URLTemplate, "{query}", url.QueryEscape(query))
					target = strings.ReplaceAll(target, "{page}", strconv.Itoa(page))
					target = strings.ReplaceAll(target, "{offset}", strconv.Itoa((page-1)*source.PageSize))
					// Enumeration endpoints may not accept a query parameter. They still
					// participate once per exact page while lexical matching happens later.
					if seenTargets[target] {
						continue
					}
					seenTargets[target] = true
					s = AppendAction(s, Action{Kind: "search", SourceID: source.ID, Query: query, URL: target, Tier: 0})
				}
			}
		}
	}
	return s, nil
}

func AppendAction(s State, a Action) State {
	a.Status = "pending"
	if a.RootKind == "" {
		a.RootKind = a.Kind
	}
	a.ID = "action:" + Digest(struct{ Kind, RootKind, Source, Candidate, Question, Query, URL string }{a.Kind, a.RootKind, a.SourceID, a.CandidateID, a.QuestionID, a.Query, a.URL})
	for _, prior := range s.Actions {
		if prior.ID == a.ID {
			return s
		}
	}
	if err := ValidateURL(a.URL); err != nil {
		a.Status = "unsupported"
		a.Reason = err.Error()
	}
	s.Actions = append(s.Actions, a)
	return s
}

// Each explicitly supplied question earns one finite query per identified
// candidate. A missing field never creates an unbounded generic research loop.
func PlanQuestions(s State) State {
	s = PlanIdentity(s)
	for _, candidate := range s.Candidates {
		for _, q := range s.Request.Questions {
			known := false
			for _, a := range candidate.Assertions {
				if a.Field == q.Field && a.Scope == q.Scope && len(AssertionFeedback(a, s.Request.Feedback)) == 0 {
					known = true
				}
			}
			if known {
				continue
			}
			if q.Acquisition == "source_document" {
				for _, observation := range candidate.Observations {
					if observation.URL == "" {
						continue
					}
					// Only source result records earn a document read. Derived documents do
					// not recursively create a new discovery plan.
					if observation.Field == "document" {
						continue
					}
					sourceID := q.SourceID
					if sourceID == "" {
						sourceID = observation.SourceID
					}
					s = AppendAction(s, Action{Kind: "source_document", SourceID: sourceID, CandidateID: candidate.ID, QuestionID: q.ID, URL: observation.URL, Tier: 1})
				}
				continue
			}
			if q.Acquisition != "query" || q.SourceID == "" || q.QueryTemplate == "" || candidate.Name == "" {
				continue
			}
			for _, source := range s.Request.Sources {
				if source.ID == q.SourceID {
					query := strings.ReplaceAll(q.QueryTemplate, "{entity}", candidate.Name)
					pages := source.Pages
					if pages == 0 {
						pages = 1
					}
					for page := 1; page <= pages; page++ {
						target := strings.ReplaceAll(source.URLTemplate, "{query}", url.QueryEscape(query))
						target = strings.ReplaceAll(target, "{page}", strconv.Itoa(page))
						target = strings.ReplaceAll(target, "{offset}", strconv.Itoa((page-1)*source.PageSize))
						s = AppendAction(s, Action{Kind: "question", SourceID: source.ID, CandidateID: candidate.ID, QuestionID: q.ID, Query: query, URL: target, Tier: 1})
					}
				}
			}
		}
	}
	return s
}

// SelectWork spends no resources. It first exhausts discovery queries, then
// chooses the least-served candidate so sparse candidates cannot be starved by
// a large dossier. The caller's explicit request allowance is the only budget.
func ReusableCapture(r Request, target string) (Capture, bool) {
	for _, capture := range r.ReusableCaptures {
		if capture.URL == target && reusableForSource(r, capture, target) {
			return capture, true
		}
	}
	return Capture{}, false
}
func SelectWork(s State) (State, string) {
	index := -1
	for i, a := range s.Actions {
		if a.Status != "pending" {
			continue
		}
		if index < 0 {
			index = i
			continue
		}
		_, cheap := ReusableCapture(s.Request, a.URL)
		_, priorCheap := ReusableCapture(s.Request, s.Actions[index].URL)
		if cheap && !priorCheap {
			index = i
			continue
		}
		if cheap != priorCheap {
			continue
		}
		if a.Tier < s.Actions[index].Tier {
			index = i
			continue
		}
		if a.Tier != s.Actions[index].Tier {
			continue
		}
		servedSource := func(id string) int {
			if id == "" {
				return 0
			}
			count := 0
			for _, prior := range s.Actions {
				if prior.SourceID == id && prior.Tier == a.Tier && prior.Status != "pending" && prior.Status != "deferred" && prior.Status != "unsupported" {
					count++
				}
			}
			return count
		}
		if servedSource(a.SourceID) < servedSource(s.Actions[index].SourceID) {
			index = i
			continue
		}
		if servedSource(a.SourceID) > servedSource(s.Actions[index].SourceID) {
			continue
		}
		served := func(id string) int {
			for _, c := range s.Candidates {
				if c.ID == id {
					return c.ResearchActions
				}
			}
			return 0
		}
		if served(a.CandidateID) < served(s.Actions[index].CandidateID) {
			index = i
		}
	}
	if index < 0 {
		s.Phase = "done"
		return s, "done"
	}
	// Explicitly selected retained bytes are cheap evidence, not a fresh read.
	for _, capture := range s.Request.ReusableCaptures {
		if capture.URL != s.Actions[index].URL || !reusableForSource(s.Request, capture, s.Actions[index].URL) {
			continue
		}
		s.CurrentAction = index
		s.Capture = capture
		seen := false
		for _, id := range s.ReusedCaptureIDs {
			if id == capture.ID {
				seen = true
			}
		}
		if !seen {
			s.Captures = append(s.Captures, capture)
			s.ReusedCaptureIDs = append(s.ReusedCaptureIDs, capture.ID)
		}
		s.Actions[index].Status = "captured"
		s.Actions[index].CaptureID = capture.ID
		s.Actions[index].Reason = "caller-selected retained capture; no fresh acquisition"
		phase := selectedSourcePhase(s, "parse")
		s.Phase = phase
		return s, phase
	}
	if !s.Request.Policy.AcquisitionEnabled || (s.Request.Policy.MaxRequests != nil && s.NetworkRequests >= *s.Request.Policy.MaxRequests) {
		reason := "acquisition disabled by request"
		if s.Request.Policy.AcquisitionEnabled {
			reason = "explicit request allowance exhausted"
		}
		for i := range s.Actions {
			if s.Actions[i].Status == "pending" {
				s.Actions[i].Status = "deferred"
				s.Actions[i].Reason = reason
			}
		}
		s.Gaps = append(s.Gaps, reason)
		s.Phase = "done"
		return s, "done"
	}
	s.CurrentAction = index
	phase := "acquire"
	for _, source := range s.Request.Sources {
		if source.ID == s.Actions[index].SourceID && source.Kind == "browser_html_search" {
			phase = "acquire_browser"
			break
		}
	}
	phase = selectedSourcePhase(s, phase)
	s.Phase = phase
	return s, phase
}

func BindCapture(s State, c Capture) (State, error) {
	if s.CurrentAction < 0 || s.CurrentAction >= len(s.Actions) {
		return s, fmt.Errorf("discovery: no selected action")
	}
	a := &s.Actions[s.CurrentAction]
	if a.Status == "captured" && a.URL == c.URL {
		if a.CaptureID != c.ID || s.Capture.ID != c.ID || s.Capture.SHA256 != c.SHA256 {
			return s, fmt.Errorf("discovery: retained capture binding changed")
		}
		s.Capture = c
		return s, nil
	}
	if a.Status != "pending" || a.URL != c.URL {
		return s, fmt.Errorf("discovery: capture does not bind selected pending URL")
	}
	if c.ID == "" {
		return s, fmt.Errorf("discovery: capture requires exact identity")
	}
	a.Status = "captured"
	a.CaptureID = c.ID
	source := sourceFor(s, a.SourceID)
	c.SourceDigest = SourceRevision(source)
	if source.Kind == "browser_html_search" {
		c.AcquisitionMode = "browser"
	} else {
		c.AcquisitionMode = "static"
	}
	s.NetworkRequests += c.Requests
	s.Captures = append(s.Captures, c)
	s.Capture = c
	for i := range s.Candidates {
		if s.Candidates[i].ID == a.CandidateID {
			s.Candidates[i].ResearchActions++
		}
	}
	s.Phase = "parse"
	return s, nil
}

func ParseWork(s State) (State, error) {
	if s.CurrentAction < 0 || s.CurrentAction >= len(s.Actions) {
		return s, fmt.Errorf("discovery: no action to parse")
	}
	a := s.Actions[s.CurrentAction]
	source := Source{ID: a.SourceID}
	origin := a.RootKind
	if origin == "" {
		origin = a.Kind
	}
	for _, item := range s.Request.Sources {
		if item.ID == a.SourceID {
			source = item
		}
	}
	// A selected result link is a document acquisition, not another feed query.
	// Choose only a supported response media type; do not guess from body text.
	if origin == "question_link" || origin == "source_document" || origin == "detail_probe" || origin == "identity_document" {
		source.Kind = DocumentKind(s.Capture.MediaType)
	}
	if origin == "source_probe" {
		source.Kind = "collection_html"
		source.Preset = "auto_collection"
		source.Mapping = nil
		source.HTMLMapping = nil
		source.Readiness = nil
	}
	if origin == "identity_document" && DocumentKind(s.Capture.MediaType) == "html" {
		source.Kind = "collection_html"
		source.Preset = "organization_detail"
		for _, candidate := range s.Candidates {
			if candidate.ID == a.CandidateID {
				source.SubjectName = candidate.Name
			}
		}
		source.HTMLMapping = nil
		source.Readiness = nil
	}
	d, err := ParseCapture(s.Capture, source)
	if err != nil {
		return s, err
	}
	s.Document = d
	if d.Outcome != "parsed" {
		s.Actions[s.CurrentAction].Status = d.Outcome
		s.Actions[s.CurrentAction].Reason = d.Gap
		if s.Capture.Outcome == "redirect" && s.Request.Policy.FollowRedirects {
			target := resolveURL(s.Capture.URL, s.Capture.RedirectLocation)
			s = AppendAction(s, Action{Kind: "redirect", RootKind: origin, SourceID: a.SourceID, CandidateID: a.CandidateID, QuestionID: a.QuestionID, Query: a.Query, URL: target, Tier: a.Tier})
		}
		s.Phase = "select"
		return s, nil
	}
	if a.QuestionID != "" && (origin == "question" || origin == "source_document") {
		for _, q := range s.Request.Questions {
			if q.ID != a.QuestionID {
				continue
			}
			for _, link := range d.Links {
				selected := false
				for _, term := range q.LinkTerms {
					if strings.Contains(strings.ToLower(link.Text), strings.ToLower(term)) {
						selected = true
					}
				}
				if selected {
					s = AppendAction(s, Action{Kind: "question_link", SourceID: a.SourceID, CandidateID: a.CandidateID, QuestionID: a.QuestionID, URL: link.URL, Tier: 2})
				}
			}
		}
	}
	s.Pending = d.Results
	for i := range s.Pending {
		s.Pending[i].ActionID = a.ID
	}
	if source.Kind == "text" || source.Kind == "html" {
		o := Observation{ActionID: a.ID, LeadID: a.CandidateID, SourceID: source.ID, CaptureID: s.Capture.ID, URL: s.Capture.URL, Title: d.Title, Text: d.Text, Field: "document"}
		for _, binding := range s.Request.SubjectBindings {
			if binding.URL == o.URL {
				o.EntityID = binding.EntityID
				o.EntityName = binding.EntityName
				o.Scope = binding.Scope
			}
		}
		// Reading a URL in a question is not enough to attribute its text to a company.
		// A dedicated entity mapping adapter must supply that attribution explicitly.
		o.ID = "observation:" + Digest(o)
		s.Pending = append(s.Pending, o)
	}
	s.Actions[s.CurrentAction].Status = "completed"
	s.Actions[s.CurrentAction].Reason = d.Gap
	s.Phase = "evidence"
	return s, nil
}

// The finite branches come only from the supplied lexicon. Retrieval hints do
// not become company requirements, and callers can supply exact source queries.
func ExpandedQueries(objective string, concepts []Concept) []string {
	queries := []string{objective}
	seen := map[string]bool{objective: true}
	tokens := Tokens(objective)
	for _, concept := range concepts {
		for _, term := range concept.Terms {
			pattern := Tokens(term)
			if len(pattern) == 0 {
				continue
			}
			for i := 0; i+len(pattern) <= len(tokens); i++ {
				matched := true
				for j := range pattern {
					if tokens[i+j].Text != pattern[j].Text {
						matched = false
						break
					}
				}
				if !matched {
					continue
				}
				for _, relation := range concept.Relations {
					query := objective[:tokens[i].Start] + relation.Term + objective[tokens[i+len(pattern)-1].End:]
					if !seen[query] {
						seen[query] = true
						queries = append(queries, query)
					}
				}
			}
		}
	}
	return queries
}

func MergeWork(s State) State {
	for _, o := range s.Pending {
		s.Candidates = MergeObservation(s.Candidates, o)
	}
	s.Pending = nil
	return s
}

func MatchWork(s State) State {
	for i := range s.Candidates {
		s.Candidates[i] = MatchCandidate(s.Candidates[i], s.Concepts)
	}
	return s
}

func ExtractWork(s State) State {
	for i := range s.Candidates {
		s.Candidates[i].Assertions = ExtractAssertions(s.Candidates[i], s.Request.Questions)
	}
	return s
}

func ConditionsWork(s State) State {
	for i := range s.Candidates {
		s.Candidates[i] = ConditionCandidate(s.Candidates[i], s.Request, s.Concepts)
	}
	return s
}

func MarkdownText(text string) string {
	for _, mark := range []string{"\\", "`", "*", "_", "[", "]", "<", ">", "#", "|"} {
		text = strings.ReplaceAll(text, mark, "\\"+mark)
	}
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r", " "), "\n", " ")
}

func ComparisonRows(candidates []Candidate, fields []string, feedback []Feedback) []FieldComparison {
	rows := []FieldComparison{}
	for _, c := range candidates {
		for _, field := range fields {
			scopes := []string{}
			seenScopes := map[string]bool{}
			for _, a := range c.Assertions {
				if a.Field == field && !seenScopes[a.Scope] {
					scopes = append(scopes, a.Scope)
					seenScopes[a.Scope] = true
				}
			}
			if len(scopes) == 0 {
				scopes = []string{""}
			}
			for _, scope := range scopes {
				row := FieldComparison{CandidateID: c.ID, Field: field, Scope: scope, State: "unknown"}
				seen := map[string]bool{}
				for _, a := range c.Assertions {
					if a.Field != field || a.Scope != scope {
						continue
					}
					ids := AssertionFeedback(a, feedback)
					if len(ids) > 0 {
						row.FeedbackIDs = append(row.FeedbackIDs, ids...)
						continue
					}
					row.AssertionIDs = append(row.AssertionIDs, a.ID)
					if !seen[a.Value] {
						row.Values = append(row.Values, a.Value)
						seen[a.Value] = true
					}
				}
				if len(row.Values) > 0 {
					row.State = "asserted"
				}
				if len(row.Values) > 1 {
					row.State = "multiple_asserted_values"
				}
				rows = append(rows, row)
			}
		}
	}
	return rows
}

func Report(s State) Result {
	// Stable relevance buckets use a positive cue, never publicity or source count.
	if s.Request.Policy.ResultOrder == "retrieval_relevance" {
		ordered := []Candidate{}
		for _, positive := range []bool{true, false} {
			for _, c := range s.Candidates {
				if RelevantWithFeedback(c.Matches, s.Request.Feedback) == positive {
					ordered = append(ordered, c)
				}
			}
		}
		s.Candidates = ordered
	}
	comparisons := ComparisonRows(s.Candidates, s.Request.Policy.CompareFields, s.Request.Feedback)
	var report strings.Builder
	report.WriteString("# Discovery results\n\n")
	report.WriteString("Objective: " + MarkdownText(s.Request.Objective) + "\n\n")
	report.WriteString("Coverage: selected sources and the explicit acquisition policy. Identity and factual conditions remain separate from retrieval relevance. No exhaustive discovery or personal-fit claim.\n\n")
	for _, c := range s.Candidates {
		name := c.Name
		if name == "" {
			name = c.ID
		}
		report.WriteString(fmt.Sprintf("## %s\n\n%s. %s\n\nIdentity: %s.\n\n", MarkdownText(name), MarkdownText(c.Disposition), MarkdownText(c.Reason), MarkdownText(c.IdentityState)))
		report.WriteString("### Retrieval evidence\n\n")
		for _, m := range c.Matches {
			polarity := "positive cue"
			if m.Negated {
				polarity = "negation cue; not a factual exclusion"
			}
			report.WriteString(fmt.Sprintf("- %s: %s (%s; %s), observation %s, bytes %d–%d in %s.\n", MarkdownText(m.ConceptID), MarkdownText(m.Quote), MarkdownText(m.Relation), polarity, MarkdownText(m.ObservationID), m.StartByte, m.EndByte, MarkdownText(m.Field)))
		}
		if len(c.Matches) == 0 {
			report.WriteString("- Relevance unresolved; lead retained.\n")
		}
		report.WriteString("\n### Requested facts and conditions\n\n")
		for _, a := range c.Assertions {
			report.WriteString(fmt.Sprintf("- %s = %s; scope %s; source %s; publication date %s; observation %s.\n", MarkdownText(a.Field), MarkdownText(a.Value), MarkdownText(a.Scope), MarkdownText(a.URL), MarkdownText(a.PublishedAt), MarkdownText(a.ObservationID)))
		}
		for _, condition := range c.Conditions {
			report.WriteString(fmt.Sprintf("- %s (%s; scope %s): %s. %s.\n", MarkdownText(condition.CriterionID), MarkdownText(condition.Role), MarkdownText(condition.Scope), MarkdownText(condition.State), MarkdownText(condition.Reason)))
		}
		if len(c.Assertions) == 0 && len(c.Conditions) == 0 {
			report.WriteString("- No supported requested facts or supplied conditions.\n")
		}
		report.WriteString("\n### Sources and remaining work\n\n")
		for _, o := range c.Observations {
			report.WriteString(fmt.Sprintf("- Source %s; %s; capture %s; publication date %s.\n", MarkdownText(o.SourceID), MarkdownText(o.URL), MarkdownText(o.CaptureID), MarkdownText(o.PublishedAt)))
		}
		for _, action := range s.Actions {
			if action.CandidateID == c.ID {
				report.WriteString(fmt.Sprintf("- Question %s: %s. %s\n", MarkdownText(action.QuestionID), MarkdownText(action.Status), MarkdownText(action.Reason)))
			}
		}
		for _, gap := range c.Gaps {
			report.WriteString("- " + MarkdownText(gap) + "\n")
		}
		report.WriteString("\n")
	}
	report.WriteString("## Scoped feedback\n\n")
	for _, f := range s.Request.Feedback {
		report.WriteString(fmt.Sprintf("- %s: %s %s for observation %s, field %s, scope %s. %s\n", MarkdownText(f.ID), MarkdownText(f.Kind), MarkdownText(f.Decision), MarkdownText(f.ObservationID), MarkdownText(f.Field), MarkdownText(f.Scope), MarkdownText(f.Reason)))
	}
	report.WriteString("\n## Acquisition coverage\n\n")
	for _, action := range s.Actions {
		report.WriteString(fmt.Sprintf("- %s: %s; %s. %s\n", MarkdownText(action.SourceID), MarkdownText(action.Status), MarkdownText(action.URL), MarkdownText(action.Reason)))
	}
	for _, gap := range s.Gaps {
		report.WriteString("- " + MarkdownText(gap) + "\n")
	}
	return Result{SourceCandidates: s.SourceCandidates, SourceHealth: s.SourceHealth, Relationships: s.Relationships, Summary: Summarize(s), Feedback: s.Request.Feedback, Comparisons: comparisons, ReusedCaptureIDs: s.ReusedCaptureIDs, RequestDigest: s.RequestDigest, Candidates: s.Candidates, Actions: s.Actions, Captures: s.Captures, Gaps: s.Gaps, Coverage: "selected sources and explicit acquisition policy; no exhaustive discovery claim", NetworkRequests: s.NetworkRequests, InferenceCalls: 0, Report: report.String()}
}

func reusableForSource(r Request, c Capture, target string) bool {
	if c.URL != target {
		return false
	}
	for _, s := range r.Sources {
		if s.ID == "" {
			continue
		}
		if c.SourceDigest != "" && c.SourceDigest == SourceRevision(s) {
			return true
		}
	}
	// Unpinned legacy captures are only explicit replay inputs, never implicit freshness.
	return c.SourceDigest == ""
}

func selectedSourcePhase(s State, fallback string) string {
	if s.CurrentAction >= 0 && s.CurrentAction < len(s.Actions) {
		for _, source := range s.Request.Sources {
			if source.ID == s.Actions[s.CurrentAction].SourceID && source.Block != nil {
				return source.BlockKey
			}
		}
	}
	return fallback
}
