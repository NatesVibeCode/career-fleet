package match

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

func sourceFor(s State, id string) Source {
	for _, source := range s.Request.Sources {
		if source.ID == id {
			return source
		}
	}
	return Source{ID: id}
}
func InspectWork(s State) State {
	if s.CurrentAction < 0 || s.CurrentAction >= len(s.Actions) {
		return s
	}
	a := s.Actions[s.CurrentAction]
	rootKind := a.RootKind
	if rootKind == "" {
		rootKind = a.Kind
	}
	source := sourceFor(s, a.SourceID)
	d := s.Document
	health := d.Health
	if health == "" {
		switch {
		case s.Capture.Status == 401 || s.Capture.Status == 403 || s.Capture.Status == 429:
			health = "blocked"
		case s.Capture.Outcome != "success" || !s.Capture.Complete:
			health = "acquisition_failure"
		case d.Outcome != "parsed":
			health = "parse_failure"
		case len(d.Results) > 0 && strings.Contains(d.Coverage, "result_links"):
			health = "unmapped_links"
		case len(d.Results) > 0:
			health = "working"
		case rootKind == "detail_probe" || a.Kind == "identity_document" || a.RootKind == "source_document":
			if strings.TrimSpace(d.Text) != "" {
				health = "document_read"
			} else {
				health = "incomplete"
			}
		default:
			health = "unconfirmed_empty"
		}
	}
	if rootKind == "detail_probe" && health != "blocked" && d.Outcome == "parsed" && (len(d.Results) > 0 || strings.TrimSpace(d.Text) != "") && s.Capture.Status >= 200 && s.Capture.Status < 300 {
		health = "document_read"
	}
	ids := []string{}
	for _, o := range d.Results {
		key := o.RecordKey
		if key == "" {
			key = o.LeadID
		}
		if key == "" {
			key = o.URL
		}
		ids = append(ids, key)
	}
	sort.Strings(ids)
	fingerprint := ""
	if len(ids) > 0 {
		fingerprint = Digest(ids)
	}
	repeated := false
	for _, prior := range s.SourceHealth {
		if prior.ActionID != a.ID && prior.SourceID == a.SourceID && prior.Query == a.Query && prior.Fingerprint != "" && prior.Fingerprint == fingerprint && rootKind == "search" {
			repeated = true
		}
	}
	if repeated {
		health = "repeated_page"
		s.Pending = nil
	}
	record := SourceHealth{Block: source.Block, Query: a.Query, ActionID: a.ID, SourceID: a.SourceID, Family: source.Family, SourceDigest: SourceRevision(source), URL: a.URL, Outcome: health, Records: len(d.Results), Requests: s.Capture.Requests, Fingerprint: fingerprint, NextURL: d.NextURL, Reason: d.Gap, CaptureID: s.Capture.ID}
	found := false
	for i := range s.SourceHealth {
		if s.SourceHealth[i].ActionID == a.ID {
			s.SourceHealth[i] = record
			found = true
		}
	}
	if !found {
		s.SourceHealth = append(s.SourceHealth, record)
	}
	if repeated || health == "confirmed_empty" {
		for i := range s.Actions {
			p := &s.Actions[i]
			if p.Status == "pending" && p.Kind == "search" && p.SourceID == a.SourceID && p.Query == a.Query {
				p.Status = "exhausted"
				p.Reason = health
			}
		}
	}
	if source.Pagination != nil && (source.Pagination.Mode == "cursor" || source.Pagination.Mode == "next_link") && health == "working" && d.NextURL != "" {
		pages := 0
		seen := false
		for _, prior := range s.Actions {
			if prior.SourceID == a.SourceID && prior.Query == a.Query && prior.Kind == "search" {
				pages++
				if prior.URL == d.NextURL {
					seen = true
				}
			}
		}
		if !seen && pages < source.Pages {
			s = AppendAction(s, Action{Kind: "search", SourceID: a.SourceID, Query: a.Query, URL: safeNextURL(a.URL, d.NextURL), Tier: 0})
		}
	}
	if s.Request.Mode == "sources" && rootKind == "search" {
		for _, o := range d.Results {
			if ValidateURL(o.URL) != nil {
				continue
			}
			id := "source-candidate:" + Digest(o.URL)
			exists := false
			origin := SourceOrigin{SourceID: a.SourceID, ActionID: a.ID, CaptureID: s.Capture.ID, Family: source.Family, Query: a.Query}
			for i := range s.SourceCandidates {
				prior := &s.SourceCandidates[i]
				if prior.ID == id {
					exists = true
					prior.CaptureIDs = appendUnique(prior.CaptureIDs, s.Capture.ID)
					found := false
					for _, p := range prior.Origins {
						if p.ActionID == a.ID {
							found = true
						}
					}
					if !found {
						prior.Origins = append(prior.Origins, origin)
					}
				}
			}
			if exists {
				continue
			}
			s.SourceCandidates = append(s.SourceCandidates, SourceCandidate{ID: id, URL: o.URL, Family: source.Family, Status: "pending", Origins: []SourceOrigin{origin}, CaptureIDs: []string{s.Capture.ID}, Reason: "collection candidate from selected search; not a company"})
			s = AppendAction(s, Action{Kind: "source_probe", SourceID: a.SourceID, CandidateID: id, URL: o.URL, Tier: 1})
		}
		s.Pending = nil
	}
	if rootKind == "source_probe" || s.Request.Mode == "verify" && rootKind == "search" {
		id := a.CandidateID
		if id == "" {
			id = "source-candidate:" + Digest(a.URL)
		}
		index := -1
		for i := range s.SourceCandidates {
			if s.SourceCandidates[i].ID == id {
				index = i
			}
		}
		if index < 0 {
			s.SourceCandidates = append(s.SourceCandidates, SourceCandidate{ID: id, URL: a.URL, Family: source.Family})
			index = len(s.SourceCandidates) - 1
		}
		candidate := &s.SourceCandidates[index]
		candidate.Block = source.Block
		candidate.Preset = d.Preset
		candidate.CaptureIDs = appendUnique(candidate.CaptureIDs, s.Capture.ID)
		candidate.Status = health
		candidate.Reason = d.Gap
		if health == "working" {
			recipe := source
			if source.Block == nil {
				recipe.ID = "collection:" + Digest(a.URL)
			}
			recipe.URLTemplate = a.URL
			recipe.Queries = []string{"{objective}"}
			recipe.Pages = 1
			recipe.Kind = "collection_html"
			recipe.Preset = d.Preset
			recipe.SourceDigest = ""
			recipe.RecordKind = "organization"
			recipe.ExternalLinksOnly = false
			recipe.Readiness = nil
			recipe.BrowserMapping = nil
			if rootKind == "source_probe" {
				recipe.Pagination = nil
			}
			if source.Kind == "json_mapped" || source.Kind == "embedded_json_mapped" {
				recipe.Kind = source.Kind
				recipe.Preset = "mapped_json"
			}
			if d.Preset == "ocds_suppliers" {
				recipe.Kind = "ocds_json"
			}
			recipe.SourceDigest = SourceRevision(recipe)
			if source.Block == nil {
				candidate.ProposedSource = &recipe
			}
			candidate.NegativeCheck = negativeMappingCheck(s.Capture, recipe)
			candidate.Status = "extraction_verified"
			candidate.Reason = "nonempty supported structure; representative detail read pending"
			for _, o := range d.Results {
				target := o.URL
				if o.LinkURL != "" {
					target = o.LinkURL
				}
				if target != a.URL && ValidateURL(target) == nil {
					candidate.DetailURLs = appendUnique(candidate.DetailURLs, target)
				}
			}
			if len(candidate.DetailURLs) > 0 {
				s = AppendAction(s, Action{Kind: "detail_probe", SourceID: a.SourceID, CandidateID: id, URL: candidate.DetailURLs[0], Tier: 2})
			}
		}
		if rootKind == "source_probe" {
			s.Pending = nil
		}
	}
	if rootKind == "detail_probe" {
		for i := range s.SourceCandidates {
			candidate := &s.SourceCandidates[i]
			if candidate.ID != a.CandidateID {
				continue
			}
			candidate.CaptureIDs = appendUnique(candidate.CaptureIDs, s.Capture.ID)
			if health == "document_read" && (candidate.ProposedSource != nil || candidate.Block != nil) && candidate.NegativeCheck {
				candidate.Status = "verified"
				if candidate.Block == nil {
					candidate.Status = "structure_verified"
				}
				candidate.Reason = "nonempty typed extraction, representative document read, and negative mapping check against the original listing capture"
			} else {
				candidate.Status = "detail_unverified"
				candidate.Reason = "representative detail acquisition did not establish a readable document"
				for _, target := range candidate.DetailURLs {
					attempted := false
					for _, prior := range s.Actions {
						if prior.Kind == "detail_probe" && prior.CandidateID == candidate.ID && prior.URL == target {
							attempted = true
						}
					}
					if !attempted && !(health == "blocked" && sameURLHost(a.URL, target)) {
						s = AppendAction(s, Action{Kind: "detail_probe", SourceID: a.SourceID, CandidateID: candidate.ID, URL: target, Tier: 2})
						break
					}
				}
			}
		}
		s.Pending = nil
	}
	if s.Capture.BodyRecord != "" {
		s.Capture.BodyBase64 = ""
	}
	s.Document.Health = health
	s.Document.Fingerprint = fingerprint
	return s
}
func appendUnique(values []string, value string) []string {
	for _, prior := range values {
		if prior == value {
			return values
		}
	}
	return append(values, value)
}

// IdentityWork consumes only explicit organization records and attribution.
func IdentityWork(s State) State {
	for i := range s.Pending {
		o := &s.Pending[i]
		if o.RecordKind == "organization" && o.RecordKey != "" && o.EntityID == "" {
			o.EntityID = "source:" + o.SourceID + ":" + o.RecordKey
		}
		if o.RecordKind != "organization" || o.EntityID == "" {
			continue
		}
		subject := "entity:" + o.EntityID
		add := func(predicate, object, target, state, reason string) {
			r := IdentityRelation{SubjectID: subject, Predicate: predicate, ObjectID: object, Name: o.EntityName, URL: target, SourceID: o.SourceID, CaptureID: o.CaptureID, ObservationID: o.ID, State: state, Reason: reason}
			r.ID = "relationship:" + Digest(r)
			for _, p := range s.Relationships {
				if p.ID == r.ID {
					return
				}
			}
			s.Relationships = append(s.Relationships, r)
		}
		add("source_record_identifies", o.LeadID, o.URL, "source_asserted", "explicit organization record and source identifier; no name/domain-only merge")
		if ValidateURL(o.WebsiteURL) == nil {
			add("official_website", o.WebsiteURL, o.WebsiteURL, "source_asserted", "selected organization record explicitly supplies this website; source attribution is retained")
		}
	}
	return s
}
func PlanIdentity(s State) State {
	if !s.Request.Policy.ResolveIdentity {
		return s
	}
	for _, c := range s.Candidates {
		for _, o := range c.Observations {
			if o.Field == "document" || o.Field == "identity_document" || ValidateURL(o.URL) != nil {
				continue
			}
			target := o.URL
			if o.LinkURL != "" {
				if ValidateURL(o.LinkURL) != nil {
					continue
				}
				target = o.LinkURL
			}
			s = AppendAction(s, Action{Kind: "identity_document", SourceID: o.SourceID, CandidateID: c.ID, URL: target, Tier: 2})
		}
	}
	return s
}
func Summarize(s State) DiscoverySummary {
	summary := DiscoverySummary{SourceRecords: len(s.Candidates), NetworkRequests: s.NetworkRequests}
	prior := map[string]bool{}
	for _, c := range s.Request.Prior {
		prior[c.ID] = true
	}
	domains := map[string]map[string]bool{}
	websites := map[string]bool{}
	families := map[string]bool{}
	for _, c := range s.Candidates {
		if c.IdentityState == "unresolved" {
			summary.UnresolvedOrganizations++
		} else {
			summary.ResolvedOrganizations++
			if !prior[c.ID] {
				summary.NewOrganizations++
			}
		}
	}
	for _, r := range s.Relationships {
		if r.Predicate == "official_website" {
			websites[r.SubjectID+"\n"+r.URL] = true
			u, e := url.Parse(r.URL)
			if e == nil {
				if domains[u.Hostname()] == nil {
					domains[u.Hostname()] = map[string]bool{}
				}
				domains[u.Hostname()][r.SourceID] = true
			}
		}
	}
	for _, h := range s.SourceHealth {
		source := sourceFor(s, h.SourceID)
		if h.Outcome == "working" && h.Family != "" && source.RecordKind == "organization" && source.Block != nil {
			families[h.Family] = true
		}
	}
	summary.OfficialWebsites = len(websites)
	for _, sources := range domains {
		if len(sources) > 1 {
			summary.CrossSourceOverlaps++
		}
	}
	for family := range families {
		summary.VerifiedFamilies = append(summary.VerifiedFamilies, family)
	}
	sort.Strings(summary.VerifiedFamilies)
	return summary
}
func validateSources(r Request) error {
	for _, family := range r.SourceFamilies {
		switch family {
		case "associations", "exhibitors", "suppliers", "procurement", "geographic", "ecosystems":
		default:
			return fmt.Errorf("unsupported source family %q", family)
		}
	}
	switch r.Mode {
	case "", "companies", "sources", "verify":
	default:
		return fmt.Errorf("unsupported discovery mode %q", r.Mode)
	}
	for _, s := range r.Sources {
		if s.SourceDigest != "" && s.SourceDigest != SourceRevision(s) {
			return fmt.Errorf("source %s: source configuration digest mismatch", s.ID)
		}
		if s.Kind == "embedded_json_mapped" && s.EmbeddedSelector == "" {
			return fmt.Errorf("embedded JSON requires a declared element selector")
		}
		switch s.RecordKind {
		case "", "organization", "product", "mention":
		default:
			return fmt.Errorf("unsupported record kind")
		}
		switch s.Preset {
		case "", "auto_collection", "mapped_html", "mapped_json", "html_table", "jsonld_organizations", "organization_detail", "ocds_suppliers":
		default:
			return fmt.Errorf("unsupported recipe preset")
		}
		if s.Readiness != nil {
			if s.Readiness.TimeoutMS <= 0 {
				return fmt.Errorf("source %s: readiness timeout must be explicitly positive", s.ID)
			}
			if s.Kind == "browser_html_search" && s.Readiness.ResultsSelector == "" && s.Readiness.EmptySelector == "" {
				return fmt.Errorf("browser readiness requires a result or empty selector")
			}
		}
		if s.BrowserMapping != nil && (s.BrowserMapping.LabelSelector != "" || s.BrowserMapping.LabelText != "" || s.BrowserMapping.SplitText != "" || s.BrowserMapping.URLAttribute != "") {
			return fmt.Errorf("source %s: label splitting and custom URL attributes require the static mapped_html preset", s.ID)
		}
		for _, m := range []*HTMLResultMapping{s.HTMLMapping, s.BrowserMapping} {
			if m == nil {
				continue
			}
			for _, selector := range []string{m.ItemSelector, m.TitleSelector, m.URLSelector, m.TextSelector, m.WebsiteSelector, m.LabelSelector} {
				if selector != "" {
					_, err := selectNodes(&recipeNode{Type: recipeDocumentNode}, selector)
					if err != nil {
						return fmt.Errorf("source %s: %w", s.ID, err)
					}
				}
			}
		}
		if s.HTMLMapping != nil && s.Preset != "mapped_html" {
			return fmt.Errorf("HTML mapping requires mapped_html preset")
		}
		if s.Preset == "mapped_html" && (s.HTMLMapping == nil || s.HTMLMapping.ItemSelector == "" || s.HTMLMapping.TitleSelector == "" && s.HTMLMapping.LabelSelector == "" || s.HTMLMapping.URLSelector == "" && s.HTMLMapping.KeyAttribute == "") {
			return fmt.Errorf("mapped_html requires item/title and URL or key mapping")
		}
		if s.Pagination != nil {
			p := s.Pagination
			if p.NextStrategy != "" && p.NextStrategy != "lexical_after_current" {
				return fmt.Errorf("unsupported next-link strategy")
			}
			switch p.Mode {
			case "next_link":
				if p.NextSelector == "" && p.NextPath == "" {
					return fmt.Errorf("next_link pagination requires selector or JSON path")
				}
			case "cursor":
				if p.CursorPath == "" || p.CursorParameter == "" {
					return fmt.Errorf("cursor pagination requires path and parameter")
				}
			case "page", "offset":
			default:
				return fmt.Errorf("unsupported pagination mode")
			}
		}
	}
	return nil
}

func SourceDiscoveryRequest(r Request) Request {
	if r.Mode != "sources" {
		return r
	}
	families := r.SourceFamilies
	if len(families) == 0 {
		families = []string{"associations", "exhibitors", "suppliers", "procurement", "geographic", "ecosystems"}
	}
	terms := map[string]string{"associations": "association member directory", "exhibitors": "conference exhibitor directory", "suppliers": "supplier manufacturer directory", "procurement": "public contract awardees companies", "geographic": "chamber of commerce business directory", "ecosystems": "partners customers company directory"}
	sources := []Source{}
	for _, source := range r.Sources {
		if !strings.HasPrefix(source.ID, "web.") {
			continue
		}
		for _, family := range families {
			term, ok := terms[family]
			if !ok {
				continue
			}
			copy := source
			copy.ID = source.ID + ".collections." + family
			copy.Family = family
			copy.Queries = []string{"{objective} " + term}
			copy.SourceDigest = ""
			sources = append(sources, copy)
		}
	}
	r.Sources = sources
	r.SourceFamilies = families
	return r
}
