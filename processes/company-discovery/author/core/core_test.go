package discovery

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

func TestAnonymousTasksKeepSparseCandidates(t *testing.T) {
	for _, objective := range []string{"ceramic filtration suppliers", "cooperative translation publishers"} {
		r := Request{Objective: objective, Seeds: []Observation{{EntityID: "new", EntityName: "New organization", Text: "Early announcement", Field: "snippet"}, {EntityID: "known", EntityName: "Established organization", Text: objective, Field: "snippet"}}}
		s, err := Open(r)
		if err != nil {
			t.Fatal(err)
		}
		s = ConditionsWork(ExtractWork(MatchWork(s)))
		if len(s.Candidates) != 2 {
			t.Fatal("candidate dropped")
		}
		for _, c := range s.Candidates {
			if c.Disposition == "explicit_condition_contradicted" {
				t.Fatal("missing data rejected a candidate")
			}
		}
		if got := Report(s).InferenceCalls; got != 0 {
			t.Fatal("model call on deterministic path")
		}
	}
}

func TestMatchOffsetsAndDeclaredSynonyms(t *testing.T) {
	o := Observation{ID: "o", Text: "λ We use ceramic membranes, not plastic filters.", Field: "snippet"}
	concepts := []Concept{{ID: "filtration", Terms: []string{"filtration"}, Relations: []Relation{{Term: "ceramic membranes", Kind: "equivalent", Provenance: "caller lexicon"}}}}
	matches := FindMatches(concepts, o)
	if len(matches) != 1 || matches[0].Quote != "ceramic membranes" || o.Text[matches[0].StartByte:matches[0].EndByte] != matches[0].Quote {
		t.Fatal(matches)
	}
	if !FindMatches([]Concept{{ID: "p", Terms: []string{"plastic filters"}}}, o)[0].Negated {
		t.Fatal("negation cue lost")
	}
}

func TestIdentityDoesNotMergeSharedDomainsOrNames(t *testing.T) {
	c := MergeObservation(nil, Observation{ID: "one", URL: "https://news.example/article", EntityName: "Common"})
	c = MergeObservation(c, Observation{ID: "two", URL: "https://news.example/article", EntityName: "Common"})
	if len(c) != 2 {
		t.Fatal("unproven identity merged")
	}
	c = MergeObservation(c, Observation{ID: "three", EntityID: "explicit"})
	c = MergeObservation(c, Observation{ID: "four", EntityID: "explicit"})
	if len(c) != 3 || len(c[2].Observations) != 2 {
		t.Fatal("explicit identity did not merge")
	}
}

func TestScopedConditionsAndSafeFailures(t *testing.T) {
	criteria := []Criterion{{ID: "location", Field: "location", Scope: "company", Role: "required", Operator: "equals", Value: "North"}}
	a := []Assertion{{ID: "role", Field: "location", Scope: "role", Value: "South"}}
	if EvaluateConditions(a, criteria)[0].State != "unknown" {
		t.Fatal("role scope leaked into company condition")
	}
	a = append(a, Assertion{ID: "company", Field: "location", Scope: "company", Value: "North"})
	if EvaluateConditions(a, criteria)[0].State != "supported" {
		t.Fatal("explicit condition lost")
	}
	a = append(a, Assertion{ID: "conflicting", Field: "location", Scope: "company", Value: "South"})
	if EvaluateConditions(a, criteria)[0].State != "conflict" {
		t.Fatal("contradictory evidence suppressed")
	}
	if ValidateRequest(Request{}) == nil {
		t.Fatal("empty task accepted")
	}
	if ValidateURL("http://example.com") == nil {
		t.Fatal("insecure source accepted")
	}
}

func captured(body string) Capture {
	h := sha256.Sum256([]byte(body))
	return Capture{ID: "c", URL: "https://example.com", Outcome: "success", Complete: true, BodyBase64: base64.StdEncoding.EncodeToString([]byte(body)), SHA256: hex.EncodeToString(h[:])}
}

func TestLosslessRSSAndSourceFailure(t *testing.T) {
	long := strings.Repeat("source text ", 1000)
	d, err := ParseCapture(captured("<rss><channel><item><title>One</title><link>https://example.com/one</link><description>"+long+"</description></item></channel></rss>"), Source{ID: "s", Kind: "rss"})
	if err != nil || len(d.Results) != 1 || !strings.Contains(d.Results[0].Text, long) {
		t.Fatal("RSS text clipped", err)
	}
	d, err = ParseCapture(Capture{Outcome: "transport_failure"}, Source{Kind: "rss"})
	if err != nil || d.Outcome == "parsed" {
		t.Fatal("failure became empty success")
	}
	bad := captured("text")
	bad.SHA256 = "wrong"
	if _, err = ParseCapture(bad, Source{Kind: "text"}); err == nil {
		t.Fatal("digest mismatch accepted")
	}
}

func TestFairSelectionAndExplicitBudget(t *testing.T) {
	one := 1
	s := State{Request: Request{Policy: Policy{AcquisitionEnabled: true, MaxRequests: &one}}, Candidates: []Candidate{{ID: "rich", ResearchActions: 4}, {ID: "sparse", ResearchActions: 0}}, Actions: []Action{{ID: "rich", CandidateID: "rich", Status: "pending", Tier: 1}, {ID: "sparse", CandidateID: "sparse", Status: "pending", Tier: 1}}}
	selected, next := SelectWork(s)
	if next != "acquire" || selected.CurrentAction != 1 {
		t.Fatal("sparse candidate starved")
	}
	selected.NetworkRequests = 1
	selected, next = SelectWork(selected)
	if next != "done" || selected.Actions[1].Status != "deferred" {
		t.Fatal("budget lost visible deferral")
	}
}

func TestStableReplayAndHTMLCoverage(t *testing.T) {
	r := Request{Objective: "water", Seeds: []Observation{{EntityID: "a", Text: "water", Field: "snippet"}}}
	a, e := Open(r)
	if e != nil {
		t.Fatal(e)
	}
	b, e := Open(r)
	if e != nil || Digest(a) != Digest(b) {
		t.Fatal("unstable replay")
	}
	d, e := ParseCapture(captured("<title>Water</title><p>Visible source</p><script>hidden()</script><a href='/about'>About</a>"), Source{Kind: "html"})
	if e != nil || strings.Contains(d.Text, "hidden") || d.Title != "Water" || len(d.Links) != 1 || d.Links[0].URL != "https://example.com/about" || d.Gap == "" {
		t.Fatal("HTML coverage or links invalid", e, d)
	}
}

func TestExplicitFollowupAndAccounting(t *testing.T) {
	s := State{Request: Request{Sources: []Source{{ID: "feed", Kind: "rss"}}, Questions: []Question{{ID: "about", LinkTerms: []string{"About"}}}}, CurrentAction: 0, Actions: []Action{{Kind: "question_link", SourceID: "feed", QuestionID: "about", URL: "https://example.com", Status: "pending"}}}
	c := captured("<p>Document evidence</p><a href='/next'>About</a>")
	c.MediaType = "text/html; charset=utf-8"
	c.Requests = 1
	var err error
	s, err = BindCapture(s, c)
	if err != nil {
		t.Fatal(err)
	}
	s, err = ParseWork(s)
	if err != nil || s.Document.Coverage != "static_html_text" || len(s.Pending) != 1 || len(s.Actions) != 1 || s.NetworkRequests != 1 {
		t.Fatal("followup parser, routing or accounting", s, err)
	}
	s.Capture.MediaType = "application/octet-stream"
	s, err = ParseWork(s)
	if err != nil || s.Document.Outcome != "unsupported_format" {
		t.Fatal("unsupported media guessed", err)
	}
}

func TestExplicitQueryExpansionAndInputIdentity(t *testing.T) {
	r := Request{Objective: "Ceramic filtration", Concepts: []Concept{{ID: "f", Terms: []string{"filtration"}, Relations: []Relation{{Term: "membranes", Kind: "equivalent", Provenance: "request"}}}}, Sources: []Source{{ID: "s", Kind: "rss", URLTemplate: "https://example.com/?q={query}"}}}
	s, err := Open(r)
	if err != nil || len(s.Actions) != 2 || s.Actions[1].Query != "Ceramic membranes" {
		t.Fatal(s.Actions, err)
	}
	r.Sources[0].Queries = []string{"exact query"}
	s, err = Open(r)
	if err != nil || len(s.Actions) != 1 || s.Actions[0].Query != "exact query" {
		t.Fatal("explicit query rewritten", err)
	}
	r.Seeds = []Observation{{ID: "same", Text: "one"}, {ID: "same", Text: "two"}}
	if ValidateRequest(r) == nil {
		t.Fatal("changed observation identity accepted")
	}
	r.Seeds = nil
	r.Questions = []Question{{ID: "q", Field: "field", LinkTerms: []string{" "}}}
	if ValidateRequest(r) == nil {
		t.Fatal("empty link selection accepted")
	}
}

func TestRedirectCycleAndAcquisitionDisabled(t *testing.T) {
	s := State{Request: Request{Policy: Policy{FollowRedirects: true}, Sources: []Source{{ID: "s", Kind: "rss"}}}, CurrentAction: 0, Actions: []Action{{Kind: "search", SourceID: "s", Query: "q", URL: "https://example.com/a", Status: "pending"}}, Capture: Capture{URL: "https://example.com/a", Outcome: "redirect", RedirectLocation: "/b"}}
	var err error
	s, err = ParseWork(s)
	if err != nil {
		t.Fatal(err)
	}
	s.CurrentAction = 1
	s.Capture = Capture{URL: "https://example.com/b", Outcome: "redirect", RedirectLocation: "/a"}
	s, err = ParseWork(s)
	if err != nil {
		t.Fatal(err)
	}
	s.CurrentAction = 2
	s.Capture = Capture{URL: "https://example.com/a", Outcome: "redirect", RedirectLocation: "/b"}
	s, err = ParseWork(s)
	if err != nil || len(s.Actions) != 3 {
		t.Fatal("cycle rescheduled", err, len(s.Actions))
	}
	s = AppendAction(s, Action{Kind: "question", URL: "https://example.com/new"})
	s, next := SelectWork(s)
	if next != "done" || s.Actions[3].Status != "deferred" {
		t.Fatal("disabled acquisition lost visible deferral")
	}
}

func TestUnidentifiedLeadEarnsDocumentVerification(t *testing.T) {
	r := Request{Objective: "ceramic filtration", Seeds: []Observation{{ID: "sparse", SourceID: "feed", URL: "https://example.com/new", Text: "Ceramic membranes", Field: "snippet"}}, Questions: []Question{{ID: "verify", Field: "offering", Scope: "company", Acquisition: "source_document", Rules: []ExtractRule{{ID: "offering", Field: "offering", Scope: "company", Pattern: "provides (.+)", Group: 1}}}}}
	s, err := Open(r)
	if err != nil {
		t.Fatal(err)
	}
	s = PlanQuestions(ConditionsWork(ExtractWork(MatchWork(s))))
	if len(s.Actions) != 1 || s.Actions[0].CandidateID != s.Candidates[0].ID {
		t.Fatal("sparse unidentified lead denied verification")
	}
	s.CurrentAction = 0
	c := captured("<p>Acme provides ceramic membranes</p>")
	c.URL = "https://example.com/new"
	c.MediaType = "text/html"
	s, err = BindCapture(s, c)
	if err != nil {
		t.Fatal(err)
	}
	s, err = ParseWork(s)
	if err != nil {
		t.Fatal(err)
	}
	s = PlanQuestions(ConditionsWork(ExtractWork(MatchWork(MergeWork(s)))))
	if len(s.Candidates) != 1 || len(s.Candidates[0].Observations) != 2 || len(s.Candidates[0].Assertions) != 0 || len(s.Actions) != 1 || s.Candidates[0].IdentityState != "unresolved" {
		t.Fatal("dossier became guessed company identity or recursive research", s.Candidates)
	}
}

func TestExplicitDocumentSubjectAndAssertionProvenance(t *testing.T) {
	r := Request{Objective: "ceramic filtration", SubjectBindings: []SubjectBinding{{URL: "https://example.com/new", EntityID: "acme", EntityName: "Acme", Scope: "company"}}, Questions: []Question{{ID: "offering", Field: "offering", Scope: "company", Rules: []ExtractRule{{ID: "offering", Field: "offering", Scope: "company", Pattern: "provides ([^\\n]+)", Group: 1}}}}}
	s, err := Open(r)
	if err != nil {
		t.Fatal(err)
	}
	s.CurrentAction = 0
	s.Actions = []Action{{Kind: "source_document", RootKind: "source_document", URL: "https://example.com/new", Status: "pending"}}
	c := captured("<p>Acme provides ceramic membranes</p>")
	c.URL = "https://example.com/new"
	c.MediaType = "text/html"
	s, err = BindCapture(s, c)
	if err != nil {
		t.Fatal(err)
	}
	s, err = ParseWork(s)
	if err != nil {
		t.Fatal(err)
	}
	s = ExtractWork(MergeWork(s))
	if len(s.Candidates) != 1 || len(s.Candidates[0].Assertions) != 1 {
		t.Fatal("explicit attribution lost", s.Candidates)
	}
	a := s.Candidates[0].Assertions[0]
	if a.CaptureID != "c" || a.URL != c.URL || a.Quote != "ceramic membranes" || a.EntityID != "entity:acme" {
		t.Fatal("assertion lost exact provenance", a)
	}
	r.SubjectBindings = append(r.SubjectBindings, SubjectBinding{URL: c.URL, EntityID: "other"})
	if ValidateRequest(r) == nil {
		t.Fatal("conflicting attribution accepted")
	}
}

func TestRetainedCaptureReusePrecedesNewAcquisition(t *testing.T) {
	c := captured("retained evidence")
	r := Request{Objective: "water", ReusableCaptures: []Capture{c}}
	if err := ValidateRequest(r); err != nil {
		t.Fatal(err)
	}
	s := State{Request: r, Actions: []Action{{URL: "https://example.com/new", Status: "pending", Tier: 0}, {URL: c.URL, Status: "pending", Tier: 1}}, CurrentAction: -1}
	s, next := SelectWork(s)
	if next != "parse" || s.CurrentAction != 1 || s.NetworkRequests != 0 || len(s.ReusedCaptureIDs) != 1 {
		t.Fatal("retained evidence lost to disabled acquisition", next, s)
	}
	s.Actions[1].Status = "completed"
	s, next = SelectWork(s)
	if next != "done" || s.Actions[0].Status != "deferred" {
		t.Fatal("new acquisition was authorized by reuse")
	}
	r.ReusableCaptures[0].SHA256 = "changed"
	if ValidateRequest(r) == nil {
		t.Fatal("changed retained bytes accepted")
	}
}

func TestComparisonKeepsUnknownsScopesAndNoSourceCountAdvantage(t *testing.T) {
	candidates := []Candidate{{ID: "sparse", Matches: []Match{{Quote: "water"}}, Assertions: []Assertion{{ID: "a", Field: "location", Scope: "company", Value: "North"}, {ID: "b", Field: "location", Scope: "role", Value: "South"}}}, {ID: "publicized", Matches: []Match{{Quote: "water"}, {Quote: "water"}, {Quote: "water"}}}, {ID: "ambiguous"}}
	rows := ComparisonRows(candidates, []string{"location"}, nil)
	if len(rows) != 4 || rows[0].Scope != "company" || rows[1].Scope != "role" || rows[2].State != "unknown" || len(rows[2].Values) != 0 {
		t.Fatal("comparison collapsed scope or filled missing values", rows)
	}
	result := Report(State{Request: Request{Policy: Policy{ResultOrder: "retrieval_relevance"}}, Candidates: candidates})
	if result.Candidates[0].ID != "sparse" || result.Candidates[1].ID != "publicized" || result.InferenceCalls != 0 {
		t.Fatal("publicity changed order")
	}
}

func TestFeedbackStaysWithinObservationFieldAndScope(t *testing.T) {
	feedback := []Feedback{{ID: "correction", ObservationID: "o", Kind: "fact", Field: "location", Scope: "company", Decision: "disputed", Reason: "caller disputes this attribution"}}
	a := []Assertion{{ID: "company", ObservationID: "o", Field: "location", Scope: "company", Value: "North"}, {ID: "role", ObservationID: "o", Field: "location", Scope: "role", Value: "South"}, {ID: "offering", ObservationID: "o", Field: "offering", Scope: "company", Value: "water"}}
	conditions := ConditionsWithFeedback(a, []Criterion{{ID: "company", Field: "location", Scope: "company", Operator: "equals", Value: "North"}, {ID: "role", Field: "location", Scope: "role", Operator: "equals", Value: "South"}, {ID: "offering", Field: "offering", Scope: "company", Operator: "equals", Value: "water"}}, feedback)
	if conditions[0].State != "unknown" || len(conditions[0].FeedbackIDs) != 1 || conditions[1].State != "supported" || conditions[2].State != "supported" {
		t.Fatal("feedback broadened", conditions)
	}
	if !RelevantWithFeedback([]Match{{ObservationID: "o"}}, feedback) {
		t.Fatal("fact correction changed retrieval policy")
	}
	feedback[0] = Feedback{ID: "local_relevance", ObservationID: "o", Kind: "retrieval", Decision: "withdrawn", Reason: "irrelevant here"}
	if RelevantWithFeedback([]Match{{ObservationID: "o"}}, feedback) || !RelevantWithFeedback([]Match{{ObservationID: "other"}}, feedback) {
		t.Fatal("retrieval correction generalized")
	}
	rows := ComparisonRows([]Candidate{{ID: "company", Assertions: a}}, []string{"location"}, []Feedback{{ID: "correction", ObservationID: "o", Kind: "fact", Field: "location", Scope: "company", Decision: "withdrawn"}})
	if rows[0].State != "unknown" || len(rows[0].FeedbackIDs) != 1 || rows[1].State != "asserted" {
		t.Fatal("withdrawn fact entered comparison", rows)
	}
}
