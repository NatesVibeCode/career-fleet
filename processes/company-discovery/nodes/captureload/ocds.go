package captureload

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
)

// OCDS releases identify award suppliers separately from buyers and bidders.
// Only an explicit identifier scheme/id establishes cross-publisher identity.
func parseOCDSSuppliers(c Capture, source Source, body []byte, base string) (Document, error) {
	d := Document{CaptureID: c.ID, Outcome: "parse_failure", Health: "unsupported_structure", Preset: "ocds_suppliers", Coverage: "ocds_award_suppliers"}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var root map[string]any
	if decoder.Decode(&root) != nil {
		return d, nil
	}
	releases, ok := root["releases"].([]any)
	if !ok || !strings.HasPrefix(mappedText(root["version"]), "1.") {
		d.Gap = "OCDS 1.x release package required"
		return d, nil
	}
	for _, value := range releases {
		release, ok := value.(map[string]any)
		if !ok {
			continue
		}
		parties := map[string]map[string]any{}
		if list, ok := release["parties"].([]any); ok {
			for _, v := range list {
				if p, ok := v.(map[string]any); ok {
					parties[mappedText(p["id"])] = p
				}
			}
		}
		var documentURLs []string
		addDocuments := func(v any) {
			list, _ := v.([]any)
			for _, v := range list {
				document, _ := v.(map[string]any)
				target := mappedText(document["url"])
				if ValidateURL(target) == nil {
					documentURLs = appendUnique(documentURLs, target)
				}
			}
		}
		addDocuments(release["documents"])
		for _, field := range []string{"awards", "contracts"} {
			list, _ := release[field].([]any)
			for _, v := range list {
				object, _ := v.(map[string]any)
				addDocuments(object["documents"])
			}
		}
		target := base
		if len(documentURLs) > 0 {
			target = documentURLs[0]
		}
		if source.DetailURLTemplate != "" {
			detail := strings.ReplaceAll(source.DetailURLTemplate, "{release_id}", url.PathEscape(mappedText(release["id"])))
			detail = strings.ReplaceAll(detail, "{ocid}", url.PathEscape(mappedText(release["ocid"])))
			if ValidateURL(detail) == nil {
				target = detail
			}
		}
		awards, _ := release["awards"].([]any)
		for _, v := range awards {
			award, _ := v.(map[string]any)
			if mappedText(award["status"]) != "active" {
				continue
			}
			suppliers, _ := award["suppliers"].([]any)
			for _, v := range suppliers {
				supplier, _ := v.(map[string]any)
				name := mappedText(supplier["name"])
				key := mappedText(supplier["id"])
				if name == "" || key == "" {
					continue
				}
				o := recordObservation(source, c, name, name, target, key, "")
				o.RecordKind = "organization"
				if party := parties[key]; party != nil {
					identifier, _ := party["identifier"].(map[string]any)
					scheme, id := mappedText(identifier["scheme"]), mappedText(identifier["id"])
					if scheme != "" && id != "" {
						o.EntityID = "registry:" + scheme + ":" + id
					}
					if website := mappedText(party["url"]); ValidateURL(website) == nil {
						o.WebsiteURL = website
					}
				}
				o.ID = "observation:" + Digest(o)
				d.Results = append(d.Results, o)
			}
		}
	}
	d.NextURL = jsonNext(source, body, base)
	d.Outcome = "parsed"
	d.Health = "working"
	if len(d.Results) == 0 {
		d.Health = "confirmed_empty"
		d.Gap = "valid release package contains no active named award suppliers"
	}
	return d, nil
}
