package discovery

import (
	"strings"
	"testing"
)

func TestCollectionTablesDoNotMergeSharedDomains(t *testing.T) {
	body := `<html><title>Members</title><table><tr><th>Company</th><th>Region</th></tr><tr><td><a href="https://shared.example/">North Brand</a></td><td>North</td></tr><tr><td><a href="https://shared.example/">South Brand</a></td><td>South</td></tr></table></html>`
	c := testCapture("https://directory.example/members", body)
	source := Source{ID: "members", Kind: "collection_html", Preset: "html_table", RecordKind: "organization", URLTemplate: c.URL}
	d, e := ParseCapture(c, source)
	if e != nil || d.Health != "working" || len(d.Results) != 2 {
		t.Fatal(d, e)
	}
	s := IdentityWork(State{Pending: d.Results})
	s = MergeWork(s)
	if len(s.Candidates) != 2 || s.Candidates[0].ID == s.Candidates[1].ID {
		t.Fatal("shared site merged unrelated brands", s.Candidates)
	}
}
func TestCollectionEmptyBlockedAndBrokenAreDifferent(t *testing.T) {
	source := Source{ID: "s", Kind: "collection_html", Preset: "mapped_html", HTMLMapping: &HTMLResultMapping{ItemSelector: "article.card", TitleSelector: "h2", URLSelector: "a[href]"}, Readiness: &Readiness{EmptySelector: ".empty", BlockedSelector: "#blocked", TimeoutMS: 1000}}
	for _, item := range []struct{ body, want string }{{`<div class="empty">No companies</div>`, "confirmed_empty"}, {`<div id="blocked">Restricted</div>`, "blocked"}, {`<a href="https://irrelevant.example">Footer</a>`, "mapping_failure"}} {
		d, e := ParseCapture(testCapture("https://directory.example/", item.body), source)
		if e != nil || d.Health != item.want || len(d.Results) != 0 {
			t.Fatal(d, e)
		}
	}
	source.HTMLMapping.ItemSelector = "article:has(h2)"
	if validateSources(Request{Sources: []Source{source}}) == nil {
		t.Fatal("unsupported selector accepted")
	}
}
func TestCollectionMappedCardsPreserveWebsiteAndExplicitIdentity(t *testing.T) {
	source := Source{ID: "s", Kind: "collection_html", Preset: "mapped_html", RecordKind: "organization", HTMLMapping: &HTMLResultMapping{ItemSelector: "article.card", TitleSelector: "h2", URLSelector: "a.profile", WebsiteSelector: "a.website", KeyAttribute: "data-id"}}
	c := testCapture("https://directory.example/", `<article class="card" data-id="123"><h2>Acme</h2><a class="profile" href="/company/123">Profile</a><a class="website" href="https://acme.example">Website</a></article>`)
	d, e := ParseCapture(c, source)
	if e != nil || len(d.Results) != 1 {
		t.Fatal(d, e)
	}
	s := IdentityWork(State{Pending: d.Results})
	if len(s.Relationships) != 2 || s.Pending[0].EntityID != "source:s:123" || s.Pending[0].WebsiteURL != "https://acme.example" {
		t.Fatal(s)
	}
}
func TestCollectionJSONLDNeedsCollectionStructure(t *testing.T) {
	source := Source{ID: "s", Kind: "collection_html", Preset: "auto_collection"}
	c := testCapture("https://directory.example/", `<script type="application/ld+json">{"@type":"Organization","name":"Publisher","url":"https://directory.example"}</script>`)
	d, e := ParseCapture(c, source)
	if e != nil || d.Health != "unsupported_structure" {
		t.Fatal("publisher mistaken for listing", d, e)
	}
	c = testCapture(c.URL, `<script type="application/ld+json">{"@type":"ItemList","itemListElement":[{"@type":"ListItem","item":{"@type":"Organization","@id":"urn:org:123","name":"Acme","url":"https://acme.example"}}]}</script>`)
	d, e = ParseCapture(c, source)
	if e != nil || d.Preset != "jsonld_organizations" || len(d.Results) != 1 || d.Results[0].EntityID != "schemaorg:urn:org:123" {
		t.Fatal(d, e)
	}
}
func TestCollectionPinChangesWithExtraction(t *testing.T) {
	s := Source{ID: "s", Kind: "collection_html", Preset: "html_table", URLTemplate: "https://directory.example/"}
	s.SourceDigest = SourceRevision(s)
	if e := validateSources(Request{Sources: []Source{s}}); e != nil {
		t.Fatal(e)
	}
	s.Preset = "auto_collection"
	if validateSources(Request{Sources: []Source{s}}) == nil {
		t.Fatal("changed recipe accepted")
	}
}
func TestCollectionPaginationStopsRepeatedRecords(t *testing.T) {
	source := Source{ID: "s", Kind: "collection_html", Preset: "html_table", Pages: 4, Pagination: &Pagination{Mode: "next_link", NextSelector: "a.next"}}
	s := State{Request: Request{Sources: []Source{source}}, Actions: []Action{{ID: "a", Kind: "search", SourceID: "s", URL: "https://directory.example/page1", Status: "completed"}}, CurrentAction: 0, Document: Document{Outcome: "parsed", Health: "working", NextURL: "https://directory.example/page2", Results: []Observation{{RecordKey: "same"}}}, Capture: Capture{ID: "c1", Outcome: "success", Complete: true}}
	s = InspectWork(s)
	if len(s.Actions) != 2 {
		t.Fatal(s.Actions)
	}
	s.CurrentAction = 1
	s.Actions[1].Status = "completed"
	s.Document.NextURL = "https://directory.example/page3"
	s.Capture.ID = "c2"
	s = InspectWork(s)
	if len(s.Actions) != 2 || s.SourceHealth[1].Outcome != "repeated_page" {
		t.Fatal(s.Actions, s.SourceHealth)
	}
	if safeNextURL("https://directory.example/", "") != "" {
		t.Fatal("empty next URL became current page")
	}
}
func TestCollectionSourceDiscoveryIsNotCompanyDiscovery(t *testing.T) {
	r := SourceDiscoveryRequest(Request{Mode: "sources", Objective: "water", SourceFamilies: []string{"associations"}, Sources: []Source{{ID: "web.test", Kind: "html_search", URLTemplate: "https://search.example/?q={query}"}}})
	if len(r.Sources) != 1 || !strings.Contains(r.Sources[0].Queries[0], "member directory") {
		t.Fatal(r)
	}
	s := InspectWork(State{Request: r, CurrentAction: 0, Actions: []Action{{ID: "a", Kind: "search", SourceID: r.Sources[0].ID, Status: "completed"}}, Capture: Capture{ID: "c", Outcome: "success", Complete: true}, Document: Document{Outcome: "parsed", Results: []Observation{{URL: "https://association.example/members", Title: "Members"}}}, Pending: []Observation{{Title: "Members"}}})
	if len(s.SourceCandidates) != 1 || len(s.Pending) != 0 || len(s.Candidates) != 0 || len(s.Actions) != 2 {
		t.Fatal(s)
	}
}
func TestEmbeddedProductSlugsAreDataOnly(t *testing.T) {
	html := `<script>(window[Symbol.for("ApolloSSRDataTransport")] ??= []).push({"rehydrate":{"data":{"products":[{"id":"123","slug":"acme","name":"Acme"}]}}});</script>`
	urls := embeddedRecordURLs(html, "https://www.producthunt.com/products/{slug}")
	if urls["123"] != "https://www.producthunt.com/products/acme" {
		t.Fatal(urls)
	}
}

func testCapture(target, body string) Capture {
	c := captured(body)
	c.URL = target
	c.FinalURL = target
	c.Status = 200
	return c
}

func TestAwardSuppliersAreRecordsNotContractTitles(t *testing.T) {
	source := Source{ID: "awards", Kind: "collection_html", Preset: "mapped_html", RecordKind: "organization", HTMLMapping: &HTMLResultMapping{ItemSelector: "div.search-result", TitleSelector: "dd", LabelSelector: "dt", LabelText: "Suppliers", SplitText: ";", URLSelector: "h2 a[href]"}}
	c := testCapture("https://awards.example/results", `<div class="search-result"><h2><a href="/award/1">Equipment contract</a></h2><dl><div><dt>Notice type</dt><dd>Award</dd></div><div><dt>Suppliers</dt><dd>Acme Ltd; Other Ltd</dd></div></dl></div>`)
	d, e := ParseCapture(c, source)
	if e != nil || len(d.Results) != 2 || d.Results[0].Title != "Acme Ltd" || d.Results[0].RecordKey == d.Results[1].RecordKey {
		t.Fatal(d, e)
	}
	if !negativeMappingCheck(c, source) {
		t.Fatal("real capture did not demonstrate missing-mapping refusal")
	}
}
func TestEmbeddedExhibitorRecordsUseDeclaredJSONPath(t *testing.T) {
	source := Source{ID: "show", Kind: "embedded_json_mapped", Preset: "mapped_json", EmbeddedSelector: "script#__NEXT_DATA__", Mapping: &JSONMapping{ItemsPath: "/props/exhibitors", TitlePath: "/name", NamePath: "/name", URLPath: "/url", LeadKeyPath: "/slug", ItemURLTemplate: "https://show.example/exhibitors/{key}"}, RecordKind: "organization"}
	c := testCapture("https://show.example/exhibitors", `<script id="__NEXT_DATA__" type="application/json">{"props":{"exhibitors":[{"name":"Acme","slug":"acme"}]}}</script>`)
	d, e := ParseCapture(c, source)
	if e != nil || len(d.Results) != 1 || d.Results[0].URL != "https://show.example/exhibitors/acme" {
		t.Fatal(d, e)
	}
}
func TestIdentityDetailDoesNotIdentifyPublisherAsTheCompany(t *testing.T) {
	source := Source{ID: "profiles", Kind: "collection_html", Preset: "organization_detail", SubjectName: "Acme"}
	c := testCapture("https://directory.example/company/acme", `<script type="application/ld+json">{"@graph":[{"@type":"Organization","name":"Directory Publisher","@id":"https://directory.example/#organization"},{"@type":"Organization","name":"Acme","@id":"urn:company:123","url":"https://acme.example/"}]}</script>`)
	d, e := ParseCapture(c, source)
	if e != nil || len(d.Results) != 1 || d.Results[0].EntityName != "Acme" {
		t.Fatal(d, e)
	}
}
func TestBingResultUnwrapDoesNotFollowRelativeOrUnsafeTargets(t *testing.T) {
	encoded := "https://www.bing.com/ck/a?u=a1aHR0cHM6Ly9hY21lLmV4YW1wbGUv"
	if unwrapSearchURL(encoded) != "https://acme.example/" {
		t.Fatal(unwrapSearchURL(encoded))
	}
	unsafe := "https://www.bing.com/ck/a?u=a1aHR0cDovLzEyNy4wLjAuMS8"
	if unwrapSearchURL(unsafe) != unsafe {
		t.Fatal("unsafe redirect accepted")
	}
}

func TestSparseHTTPMembersRemainSeparate(t *testing.T) {
	s := Source{ID: "members", Kind: "collection_html", Preset: "mapped_html", RecordKind: "organization", HTMLMapping: &HTMLResultMapping{ItemSelector: "a.member", TitleSelector: "img", TitleAttribute: "alt", URLSelector: "a[href]"}}
	d, e := ParseCapture(testCapture("https://directory.example/", `<a class="member" href="http://one.example"><img alt="One"></a><a class="member" href="http://two.example"><img alt="Two"></a>`), s)
	if e != nil || len(d.Results) != 2 || d.Results[0].RecordKey == d.Results[1].RecordKey || d.Results[0].LinkURL != "http://one.example" {
		t.Fatal(d, e)
	}
}
func TestOCDSSuppliersExcludeBuyersAndBindRegistry(t *testing.T) {
	c := testCapture("https://procurement.example/api", `{"version":"1.1","releases":[{"parties":[{"id":"s1","identifier":{"scheme":"GB-COH","id":"123"}},{"id":"buyer","name":"Buyer"}],"awards":[{"status":"active","suppliers":[{"id":"s1","name":"Supplier"}]},{"status":"cancelled","suppliers":[{"id":"s2","name":"Cancelled"}]},{"status":"pending","suppliers":[{"id":"s3","name":"Pending"}]}]}],"links":{"next":"https://procurement.example/api?page=2"}}`)
	s := Source{ID: "awards", Kind: "ocds_json", Preset: "ocds_suppliers", Pagination: &Pagination{Mode: "next_link", NextPath: "/links/next"}}
	d, e := ParseCapture(c, s)
	if e != nil || len(d.Results) != 1 || d.Results[0].EntityID != "registry:GB-COH:123" || d.NextURL != "https://procurement.example/api?page=2" {
		t.Fatal(d, e)
	}
	if !negativeMappingCheck(c, s) {
		t.Fatal("OCDS missing mapping did not refuse")
	}
}
func TestAlphabetPaginationUsesNextDeclaredLetter(t *testing.T) {
	s := Source{ID: "geo", Kind: "collection_html", Preset: "mapped_html", HTMLMapping: &HTMLResultMapping{ItemSelector: "article", TitleSelector: "h2", URLSelector: "a"}, Pagination: &Pagination{Mode: "next_link", NextSelector: "a.alpha", NextStrategy: "lexical_after_current"}}
	c := testCapture("https://directory.example/list/searchalpha/a", `<a class="alpha" href="/list/searchalpha/0-9">0-9</a><a class="alpha" href="/list/searchalpha/a">a</a><a class="alpha" href="/list/searchalpha/b">b</a><article><h2>Acme</h2><a href="/acme">Acme</a></article>`)
	d, e := ParseCapture(c, s)
	if e != nil || d.NextURL != "https://directory.example/list/searchalpha/b" {
		t.Fatal(d, e)
	}
}

func TestOCDSDetailTemplateBindsRelease(t *testing.T) {
	c := testCapture("https://procurement.example/api", `{"version":"1.1","releases":[{"id":"notice-123","awards":[{"status":"active","suppliers":[{"id":"s1","name":"Supplier"}]}]}]}`)
	s := Source{ID: "awards", Kind: "ocds_json", Preset: "ocds_suppliers", DetailURLTemplate: "https://procurement.example/api/{release_id}"}
	d, e := ParseCapture(c, s)
	if e != nil || len(d.Results) != 1 || d.Results[0].URL != "https://procurement.example/api/notice-123" {
		t.Fatal(d, e)
	}
	state := State{Request: Request{Sources: []Source{s}}, CurrentAction: 0, Actions: []Action{{ID: "detail", Kind: "detail_probe", SourceID: s.ID, URL: d.Results[0].URL}}, Document: d, Capture: c}
	state = InspectWork(state)
	if state.Document.Health != "document_read" {
		t.Fatal(state.Document)
	}
}

func TestSourceDiscoveryRetainsEveryChannelOrigin(t *testing.T) {
	sources := []Source{{ID: "web.one", Family: "associations"}, {ID: "web.two", Family: "exhibitors"}}
	s := State{Request: Request{Mode: "sources", Sources: sources}, CurrentAction: 0, Actions: []Action{{ID: "a1", Kind: "search", SourceID: "web.one", Query: "members"}, {ID: "a2", Kind: "search", SourceID: "web.two", Query: "exhibitors"}}, Capture: Capture{ID: "capture:one", Outcome: "success", Complete: true}, Document: Document{Outcome: "parsed", Health: "working", Results: []Observation{{URL: "https://directory.example/"}}}}
	s = InspectWork(s)
	s.CurrentAction = 1
	s.Capture.ID = "capture:two"
	s = InspectWork(s)
	if len(s.SourceCandidates) != 1 || len(s.SourceCandidates[0].Origins) != 2 || len(s.SourceCandidates[0].CaptureIDs) != 2 {
		t.Fatal(s.SourceCandidates)
	}
	if s.SourceCandidates[0].Origins[1].Family != "exhibitors" {
		t.Fatal(s.SourceCandidates)
	}
}
func TestBlockedDetailStopsSameHostFallback(t *testing.T) {
	s := State{CurrentAction: 0, Request: Request{Sources: []Source{{ID: "s"}}}, Actions: []Action{{ID: "a", Kind: "detail_probe", SourceID: "s", CandidateID: "candidate", URL: "https://directory.example/one"}}, Capture: Capture{Status: 403, Outcome: "http_failure"}, Document: Document{Outcome: "unavailable"}, SourceCandidates: []SourceCandidate{{ID: "candidate", ProposedSource: &Source{ID: "s"}, DetailURLs: []string{"https://directory.example/one", "https://directory.example/two"}}}}
	s = InspectWork(s)
	if len(s.Actions) != 1 || s.SourceCandidates[0].Status != "detail_unverified" {
		t.Fatal(s.Actions, s.SourceCandidates)
	}
}

func TestSourceBlocksRejectStaleReferencesAndMappingOverrides(t *testing.T) {
	source := Source{ID: "directory.test", Kind: "collection_html", Preset: "mapped_html", URLTemplate: "https://directory.example/?page={page}", Queries: []string{"{objective}"}, Pages: 1, HTMLMapping: &HTMLResultMapping{ItemSelector: "article", TitleSelector: "h2", URLSelector: "a"}}
	ref := BlockReference{Name: "career.source.directory.test.v1", Digest: "exact-block-revision"}
	defs := []SourceBlockDefinition{{Block: ref, Key: "source_directory_test", Source: source}}
	request, err := ResolveSourceBlocks(Request{Objective: "organizations", Policy: Policy{AcquisitionEnabled: true}, SourceBlocks: []SourceBlockSelection{{Block: ref, Pages: 3}}}, defs)
	if err != nil || len(request.Sources) != 1 || request.Sources[0].Pages != 3 {
		t.Fatal(request, err)
	}
	state, err := Open(request)
	if err != nil {
		t.Fatal(err)
	}
	state, phase := SelectWork(state)
	if phase != "source_directory_test" {
		t.Fatal(phase)
	}
	source.BlockKey = "source_directory_test"
	if _, err = BindSourceBlock(state, source); err != nil {
		t.Fatal(err)
	}
	mapping := *state.Request.Sources[0].HTMLMapping
	mapping.ItemSelector = "footer"
	state.Request.Sources[0].HTMLMapping = &mapping
	if _, err = BindSourceBlock(state, source); err == nil {
		t.Fatal("mapping override accepted")
	}
	stale := ref
	stale.Digest = "stale"
	if _, err = ResolveSourceBlocks(Request{SourceBlocks: []SourceBlockSelection{{Block: stale}}}, defs); err == nil {
		t.Fatal("stale reference accepted")
	}
	source.Block = &ref
	if _, err = ResolveSourceBlocks(Request{Sources: []Source{source}}, defs); err == nil {
		t.Fatal("caller supplied Block configuration accepted")
	}
}
func TestSelectedRetainedSourceCaptureStillRunsItsBlock(t *testing.T) {
	source := Source{ID: "s", Kind: "collection_html", Preset: "html_table", URLTemplate: "https://directory.example/", Queries: []string{"{objective}"}, Pages: 1}
	ref := BlockReference{Name: "source.test", Digest: "exact"}
	request, err := ResolveSourceBlocks(Request{Objective: "organizations", SourceBlocks: []SourceBlockSelection{{Block: ref}}, ReusableCaptures: []Capture{testCapture(source.URLTemplate, `<table><tr><th>Company</th></tr><tr><td><a href="https://company.example/">Company</a></td></tr></table>`)}}, []SourceBlockDefinition{{Block: ref, Key: "source_s", Source: source}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := Open(request)
	if err != nil {
		t.Fatal(err)
	}
	state, phase := SelectWork(state)
	if phase != "source_s" {
		t.Fatal(phase)
	}
	before := state.NetworkRequests
	if _, err = BindCapture(state, state.Capture); err != nil || state.NetworkRequests != before {
		t.Fatal(state, err)
	}
}
