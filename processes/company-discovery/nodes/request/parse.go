package request

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

)

// ParseCapture preserves the original binary capture separately. Text offsets in
// later observations refer to this explicit derived representation, not raw HTML.
func ParseCapture(c Capture, source Source) (Document, error) {
	d := Document{CaptureID: c.ID, Outcome: "unavailable", Coverage: "none"}
	if c.Outcome != "success" || !c.Complete {
		d.Gap = "source acquisition did not produce a complete successful response"
		return d, nil
	}
	body, err := base64.StdEncoding.DecodeString(c.BodyBase64)
	if err != nil {
		return d, fmt.Errorf("discovery: invalid capture encoding: %w", err)
	}
	h := sha256.Sum256(body)
	if hex.EncodeToString(h[:]) != c.SHA256 {
		return d, fmt.Errorf("discovery: capture digest mismatch")
	}
	if !utf8.Valid(body) {
		d.Outcome = "unsupported_encoding"
		d.Gap = "text adapter requires UTF-8; original bytes retained"
		return d, nil
	}
	base := c.FinalURL
	if base == "" {
		base = c.URL
	}
	if source.Kind == "ocds_json" || source.Preset == "ocds_suppliers" {
		return parseOCDSSuppliers(c, source, body, base)
	}
	if source.Kind == "collection_html" && source.Preset == "auto_collection" && strings.Contains(c.MediaType, "json") {
		var shape struct {
			Version  string            `json:"version"`
			Releases []json.RawMessage `json:"releases"`
		}
		if json.Unmarshal(body, &shape) == nil && strings.HasPrefix(shape.Version, "1.") && shape.Releases != nil {
			return parseOCDSSuppliers(c, source, body, base)
		}
	}
	if source.Kind == "embedded_json_mapped" {
		root, e := parseRecipeTree(bytes.NewReader(body))
		if e != nil {
			return d, e
		}
		scripts, e := selectNodes(root, source.EmbeddedSelector)
		if e != nil {
			return d, e
		}
		for _, script := range scripts {
			var content strings.Builder
			for child := script.FirstChild; child != nil; child = child.NextSibling {
				if child.Type == recipeTextNode {
					content.WriteString(child.Data)
				}
			}
			text := strings.TrimSpace(content.String())
			if source.EmbeddedPrefix != "" {
				index := strings.Index(text, source.EmbeddedPrefix)
				if index < 0 {
					continue
				}
				text = strings.TrimSpace(text[index+len(source.EmbeddedPrefix):])
			}
			var value any
			decoder := json.NewDecoder(strings.NewReader(text))
			decoder.UseNumber()
			if decoder.Decode(&value) != nil {
				continue
			}
			data, _ := json.Marshal(value)
			d, e = parseMappedJSON(data, c, source, base)
			d.Preset = "mapped_json"
			d.Coverage = "embedded_json_mapped_records"
			return d, e
		}
		d.Outcome = "parse_failure"
		d.Health = "mapping_failure"
		d.Gap = "declared embedded JSON element or prefix absent"
		return d, nil
	}
	if source.Kind == "collection_html" || source.Kind == "html_search" && source.HTMLMapping != nil {
		return parseCollectionHTML(c, source, body, base)
	}
	if source.Readiness != nil && (source.Kind == "html_search" || source.Kind == "html") {
		root, e := parseRecipeTree(bytes.NewReader(body))
		if e != nil {
			return d, e
		}
		if firstNode(root, source.Readiness.BlockedSelector) != nil {
			d.Outcome = "parsed"
			d.Health = "blocked"
			d.Gap = "declared blocked document state matched"
			return d, nil
		}
		if firstNode(root, source.Readiness.EmptySelector) != nil {
			d.Outcome = "parsed"
			d.Health = "confirmed_empty"
			return d, nil
		}
	}
	switch source.Kind {
	case "rss":
		type item struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			Description string `xml:"description"`
			Date        string `xml:"pubDate"`
			GUID        string `xml:"guid"`
		}
		type atomLink struct {
			Href string `xml:"href,attr"`
			Rel  string `xml:"rel,attr"`
		}
		type atomEntry struct {
			Title   string     `xml:"title"`
			Links   []atomLink `xml:"link"`
			Summary string     `xml:"summary"`
			Content string     `xml:"content"`
			Updated string     `xml:"updated"`
			Created string     `xml:"published"`
			ID      string     `xml:"id"`
		}
		var feed struct {
			Channel struct {
				Items []item `xml:"item"`
			} `xml:"channel"`
			Entries []atomEntry `xml:"entry"`
		}
		if err = xml.Unmarshal(body, &feed); err != nil {
			d.Outcome = "parse_failure"
			d.Gap = err.Error()
			return d, nil
		}
		for _, it := range feed.Channel.Items {
			text, _, _, e := HTMLText(it.Description, base)
			if e != nil {
				return d, e
			}
			o := Observation{SourceID: source.ID, CaptureID: c.ID, URL: resolveURL(base, it.Link), Title: it.Title, Text: text, Field: "snippet", PublishedAt: it.Date}
			o.ID = "observation:" + Digest(o)
			d.Results = append(d.Results, o)
		}
		for _, entry := range feed.Entries {
			link := ""
			for _, candidate := range entry.Links {
				if candidate.Rel == "" || candidate.Rel == "alternate" {
					link = candidate.Href
					break
				}
			}
			content := entry.Summary
			if content == "" {
				content = entry.Content
			}
			text, _, _, e := HTMLText(content, base)
			if e != nil {
				return d, e
			}
			published := entry.Created
			if published == "" {
				published = entry.Updated
			}
			if link == "" && strings.HasPrefix(entry.ID, "https://") {
				link = entry.ID
			}
			o := Observation{SourceID: source.ID, CaptureID: c.ID, URL: resolveURL(base, link), Title: entry.Title, Text: text, Field: "snippet", PublishedAt: published}
			o.ID = "observation:" + Digest(o)
			d.Results = append(d.Results, o)
		}
		d.Coverage = "feed_items_and_atom_entries" // A valid empty feed differs from transport/parse failure.
	case "json":
		// This adapter accepts the declared observation envelope only. Arbitrary JSON
		// requires a separate typed mapping, rather than guessed provider fields.
		var envelope struct {
			Observations []Observation `json:"observations"`
		}
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&envelope); err != nil {
			d.Outcome = "parse_failure"
			d.Gap = err.Error()
			return d, nil
		}
		if decoder.Decode(new(any)) != io.EOF {
			d.Outcome = "parse_failure"
			d.Gap = "trailing JSON"
			return d, nil
		}
		for _, o := range envelope.Observations {
			o.SourceID = source.ID
			o.CaptureID = c.ID
			o.ID = ""
			o.ID = "observation:" + Digest(o)
			d.Results = append(d.Results, o)
		}
		d.Coverage = "declared_observations"
	case "json_mapped":
		return parseMappedJSON(body, c, source, base)
	case "html":
		d.Text, d.Title, d.Links, err = HTMLText(string(body), base)
		if err != nil {
			return d, err
		}
		d.Coverage = "static_html_text"
		d.Gap = "static HTML does not establish rendered or script-loaded content coverage"
	case "html_search":
		d.Text, d.Title, d.Links, err = HTMLText(string(body), base)
		if err != nil {
			return d, err
		}
		for _, link := range d.Links {
			title := strings.TrimSpace(link.Text)
			target := link.URL
			if source.ExternalLinksOnly {
				target = unwrapSearchURL(target)
				if sameURLHost(base, target) {
					continue
				}
			}
			if title == "" || ValidateURL(target) != nil {
				continue
			}
			o := Observation{SourceID: source.ID, CaptureID: c.ID, URL: target, Title: title, Text: title, Field: "search_result", LeadID: leadIDForURL(target)}
			o.EntityName = title
			o.ID = "observation:" + Digest(o)
			d.Results = append(d.Results, o)
		}
		d.Coverage = "static_search_result_links"
		d.Gap = "static HTML link results only; page snippets and script-loaded results may be absent"
	case "browser_html_search":
		var rendered browserSearchCapture
		if err = json.Unmarshal(body, &rendered); err != nil {
			d.Outcome = "parse_failure"
			d.Gap = "browser capture envelope could not be decoded"
			return d, nil
		}
		staticRecovery := rendered.Mode == "static_recovery"
		statusOK := rendered.Status == "read" || staticRecovery && rendered.Status == "static_recovery"
		payloadStatusOK := !staticRecovery && (rendered.Payload.Status == "" || rendered.Payload.Status == "read") || staticRecovery && rendered.Payload.Status == "read"
		if !statusOK || !payloadStatusOK || rendered.StatusCode < 200 || rendered.StatusCode >= 300 {
			d.Outcome = "partial"
			d.Gap = "browser did not complete a successful rendered document read"
			return d, nil
		}
		for _, response := range rendered.SourceResponses {
			if !response.Complete || response.Outcome != "success" {
				continue
			}
			raw, decodeErr := base64.StdEncoding.DecodeString(response.BodyBase64)
			if decodeErr != nil {
				return d, fmt.Errorf("discovery: invalid browser source response encoding")
			}
			sum := sha256.Sum256(raw)
			if hex.EncodeToString(sum[:]) != response.SHA256 {
				return d, fmt.Errorf("discovery: browser source response digest mismatch")
			}
		}
		renderedHTML := rendered.Payload.HTML
		finalURL := rendered.Payload.FinalURL
		if renderedHTML == "" {
			renderedHTML = rendered.Payload.LegacyPayload.HTML
		}
		if finalURL == "" {
			finalURL = rendered.Payload.LegacyPayload.FinalURL
		}
		if renderedHTML == "" {
			d.Outcome = "partial"
			d.Gap = "browser returned no rendered document HTML"
			return d, nil
		}
		if finalURL != "" && ValidateURL(finalURL) == nil {
			base = finalURL
		}
		d.Text, d.Title, d.Links, err = HTMLText(renderedHTML, base)
		if err != nil {
			return d, err
		}
		if source.BrowserMapping != nil && !staticRecovery {
			embeddedURLs := embeddedRecordURLs(renderedHTML, source.DetailURLTemplate)
			for _, item := range rendered.Payload.Results {
				title := strings.TrimSpace(item.Title)
				if title == "" {
					continue
				}
				target := strings.TrimSpace(item.URL)
				if target == "" && len(embeddedURLs) > 0 {
					key := item.Key
					if i := strings.LastIndex(key, "-"); i >= 0 {
						key = key[i+1:]
					}
					target = embeddedURLs[key]
				}
				leadID := ""
				if target == "" {
					if strings.TrimSpace(item.Key) == "" {
						continue
					}
					target = base
					leadID = leadIDForSourceRecord(source.ID, item.Key)
				} else {
					if ValidateURL(target) != nil {
						continue
					}
					leadID = leadIDForURL(target)
				}
				text := strings.TrimSpace(item.Text)
				if text == "" {
					text = title
				}
				o := Observation{SourceID: source.ID, CaptureID: c.ID, URL: target, Title: title, Text: text, Field: "search_result", LeadID: leadID, EntityName: title}
				o.RecordKind = source.RecordKind
				o.RecordKey = item.Key
				if o.RecordKey == "" {
					o.RecordKey = target
				}
				o.WebsiteURL = item.Website
				o.ID = "observation:" + Digest(o)
				d.Results = append(d.Results, o)
			}
			d.Coverage = "rendered_dom_search_result_cards"
			d.Health = rendered.Payload.Readiness
			if d.Health == "ready" {
				d.Health = "working"
			}
			if d.Health == "empty" {
				d.Health = "confirmed_empty"
			}
			if d.Health == "timeout" {
				d.Health = "incomplete_rendering"
			}
			if d.Health == "" && len(d.Results) == 0 {
				d.Health = "mapping_failure"
			}
			if source.Pagination != nil {
				d.NextURL = safeNextURL(base, rendered.Payload.NextURL)
			}
			d.Gap = "mapped rendered result cards only; source listing pages and linked company records remain unverified"
			if len(d.Results) == 0 {
				d.Gap += "; no cards matched the declared source mapping"
			}
		} else {
			for _, link := range d.Links {
				title := strings.TrimSpace(link.Text)
				target := link.URL
				if source.ExternalLinksOnly {
					target = unwrapSearchURL(target)
					if sameURLHost(base, target) {
						continue
					}
				}
				if title == "" || ValidateURL(target) != nil {
					continue
				}
				o := Observation{SourceID: source.ID, CaptureID: c.ID, URL: target, Title: title, Text: title, Field: "search_result", LeadID: leadIDForURL(target)}
				o.EntityName = title
				o.ID = "observation:" + Digest(o)
				d.Results = append(d.Results, o)
			}
		}
		if source.BrowserMapping != nil && !staticRecovery {
			// Coverage and partiality are set from the declared card mapping above.
		} else if staticRecovery {
			d.Coverage = "static_recovery_search_result_links"
			d.Gap = "static HTML used for recovery; JavaScript-rendered search coverage was not obtained"
		} else {
			d.Coverage = "rendered_dom_search_result_links"
			d.Gap = "rendered DOM links only; page snippets and linked documents remain unverified"
		}
		if rendered.DeniedRequests > 0 {
			d.Gap += fmt.Sprintf("; %d browser resource requests were denied and retained in the capture", rendered.DeniedRequests)
		}
	case "text":
		d.Text = string(body)
		d.Coverage = "complete_utf8_text"
	default:
		d.Outcome = "unsupported_format"
		d.Gap = "no selected parser for source format"
		return d, nil
	}
	d.Outcome = "parsed"
	return d, nil
}

// Browser capture keeps the broker's original network responses alongside
// the DOM produced by the exact pinned extraction program.
type browserSearchCapture struct {
	Mode            string `json:"mode"`
	DeniedRequests  int    `json:"denied_requests"`
	Status          string `json:"status"`
	StatusCode      int    `json:"status_code"`
	SourceResponses []struct {
		BodyBase64 string `json:"body_base64"`
		SHA256     string `json:"sha256"`
		Outcome    string `json:"outcome"`
		Complete   bool   `json:"complete"`
	} `json:"source_responses"`
	Payload struct {
		Readiness     string                `json:"readiness"`
		NextURL       string                `json:"next_url"`
		Status        string                `json:"status"`
		FinalURL      string                `json:"final_url"`
		HTML          string                `json:"html"`
		Results       []browserSearchResult `json:"results"`
		LegacyPayload struct {
			FinalURL string `json:"final_url"`
			HTML     string `json:"html"`
		} `json:"payload"`
	} `json:"payload"`
}

type browserSearchResult struct {
	Key     string `json:"key"`
	Website string `json:"website"`
	Title   string `json:"title"`
	Text    string `json:"text"`
	URL     string `json:"url"`
}

func parseMappedJSON(body []byte, capture Capture, source Source, base string) (Document, error) {
	d := Document{CaptureID: capture.ID, Outcome: "parse_failure", Coverage: "none"}
	if source.Mapping == nil {
		d.Gap = "json_mapped source has no field mapping"
		return d, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		d.Gap = "JSON response could not be decoded"
		return d, nil
	}
	if decoder.Decode(new(any)) != io.EOF {
		d.Gap = "JSON response contains trailing data"
		return d, nil
	}
	itemsValue, ok := jsonPointerValue(root, source.Mapping.ItemsPath)
	if !ok {
		d.Gap = "mapped JSON item path is absent"
		return d, nil
	}
	items, ok := itemsValue.([]any)
	if !ok {
		d.Gap = "mapped JSON item path is not an array"
		return d, nil
	}
	invalidURLs := 0
	for _, item := range items {
		urlValue, exists := jsonPointerValue(item, source.Mapping.URLPath)
		rawURL := mappedText(urlValue)
		if (!exists || strings.TrimSpace(rawURL) == "") && source.Mapping.ItemURLTemplate != "" {
			keyValue, keyExists := jsonPointerValue(item, source.Mapping.LeadKeyPath)
			key := strings.TrimSpace(mappedText(keyValue))
			if keyExists && key != "" {
				rawURL = strings.ReplaceAll(source.Mapping.ItemURLTemplate, "{key}", url.QueryEscape(key))
				exists = true
			}
		}
		if !exists || strings.TrimSpace(rawURL) == "" {
			invalidURLs++
			continue
		}
		target := resolveURL(base, strings.TrimSpace(rawURL))
		if ValidateURL(target) != nil {
			invalidURLs++
			continue
		}
		read := func(pointer string) string {
			if pointer == "" {
				return ""
			}
			value, found := jsonPointerValue(item, pointer)
			if !found {
				return ""
			}
			return strings.TrimSpace(mappedText(value))
		}
		title := read(source.Mapping.TitlePath)
		name := read(source.Mapping.NamePath)
		if title == "" {
			title = name
		}
		if title == "" {
			title = target
		}
		if name == "" {
			name = title
		}
		text := read(source.Mapping.TextPath)
		if text == "" {
			text = title
		}
		leadID := leadIDForURL(target)
		if key := read(source.Mapping.LeadKeyPath); key != "" {
			leadID = "mention:key:" + source.ID + ":" + key
		}
		o := Observation{SourceID: source.ID, CaptureID: capture.ID, URL: target, Title: title, Text: text, Field: "snippet", PublishedAt: read(source.Mapping.DatePath), EntityName: name, LeadID: leadID}
		o.RecordKind = source.RecordKind
		o.RecordKey = read(source.Mapping.LeadKeyPath)
		if o.RecordKey == "" {
			o.RecordKey = target
		}
		o.ID = "observation:" + Digest(o)
		d.Results = append(d.Results, o)
	}
	d.NextURL = jsonNext(source, body, base)
	d.Outcome = "parsed"
	d.Coverage = "json_mapped_records"
	if invalidURLs > 0 {
		d.Gap = fmt.Sprintf("%d mapped records lacked an absolute public HTTPS result URL", invalidURLs)
	}
	return d, nil
}

func jsonPointerValue(value any, pointer string) (any, bool) {
	if pointer == "" {
		return value, true
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	current := value
	for _, part := range strings.Split(pointer[1:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch item := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = item[part]
			if !ok {
				return nil, false
			}
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(item) {
				return nil, false
			}
			current = item[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func mappedText(value any) string {
	switch item := value.(type) {
	case nil:
		return ""
	case string:
		return item
	case json.Number:
		return item.String()
	case bool:
		return strconv.FormatBool(item)
	default:
		encoded, err := json.Marshal(item)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}

func leadIDForURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.Fragment = ""
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	return "mention:url:" + u.String()
}

func leadIDForSourceRecord(sourceID, recordKey string) string {
	sum := sha256.Sum256([]byte("career-fleet.source-record.v1\n" + sourceID + "\n" + recordKey))
	return "lead:" + hex.EncodeToString(sum[:])
}

func sameURLHost(left, right string) bool {
	a, aErr := url.Parse(left)
	b, bErr := url.Parse(right)
	return aErr == nil && bErr == nil && a.Host != "" && strings.EqualFold(a.Host, b.Host)
}

func unwrapSearchURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	for _, key := range []string{"uddg", "url", "q"} {
		candidate := strings.TrimSpace(u.Query().Get(key))
		if candidate != "" && ValidateURL(candidate) == nil {
			return candidate
		}
	}
	if strings.EqualFold(u.Hostname(), "www.bing.com") || strings.EqualFold(u.Hostname(), "bing.com") {
		encoded := u.Query().Get("u")
		if strings.HasPrefix(encoded, "a1") {
			encoded = encoded[2:]
			for _, encoding := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
				body, e := encoding.DecodeString(encoded)
				if e == nil && ValidateURL(string(body)) == nil {
					return string(body)
				}
			}
		}
	}
	return raw
}

// DocumentKind maps an actual response declaration to an implemented parser.
func DocumentKind(mediaType string) string {
	kind := strings.ToLower(strings.TrimSpace(strings.Split(mediaType, ";")[0]))
	switch kind {
	case "text/html", "application/xhtml+xml":
		return "html"
	case "text/plain":
		return "text"
	case "application/json":
		return "json"
	case "application/rss+xml":
		return "rss"
	}
	return "unsupported"
}

func resolveURL(base, ref string) string {
	b, e := url.Parse(base)
	if e != nil {
		return ref
	}
	u, e := url.Parse(ref)
	if e != nil {
		return ref
	}
	return b.ResolveReference(u).String()
}

func HTMLText(input, base string) (string, string, []Link, error) {
	z := fc_fe63640163f18bb75b2718540dff620e5da9febcd05ceaf48e3cfba1e6cb99eb_NewTokenizer(strings.NewReader(input))
	var text, title strings.Builder
	links := []Link{}
	skip := ""
	inTitle := false
	anchor := -1
	for {
		kind := z.Next()
		if kind == fc_fe63640163f18bb75b2718540dff620e5da9febcd05ceaf48e3cfba1e6cb99eb_ErrorToken {
			if z.Err() != io.EOF {
				return "", "", nil, z.Err()
			}
			break
		}
		token := z.Token()
		switch kind {
		case fc_fe63640163f18bb75b2718540dff620e5da9febcd05ceaf48e3cfba1e6cb99eb_StartTagToken, fc_fe63640163f18bb75b2718540dff620e5da9febcd05ceaf48e3cfba1e6cb99eb_SelfClosingTagToken:
			if skip != "" {
				continue
			}
			switch token.Data {
			case "script", "style", "template", "noscript":
				if kind != fc_fe63640163f18bb75b2718540dff620e5da9febcd05ceaf48e3cfba1e6cb99eb_SelfClosingTagToken {
					skip = token.Data
				}
			case "title":
				inTitle = true
			case "a":
				for _, a := range token.Attr {
					if a.Key == "href" {
						links = append(links, Link{URL: resolveURL(base, a.Val)})
						anchor = len(links) - 1
					}
				}
			}
		case fc_fe63640163f18bb75b2718540dff620e5da9febcd05ceaf48e3cfba1e6cb99eb_EndTagToken:
			if skip != "" {
				if token.Data == skip {
					skip = ""
				}
				continue
			}
			if token.Data == "title" {
				inTitle = false
			}
			if token.Data == "a" {
				anchor = -1
			}
		case fc_fe63640163f18bb75b2718540dff620e5da9febcd05ceaf48e3cfba1e6cb99eb_TextToken:
			if skip != "" {
				continue
			}
			if inTitle {
				title.WriteString(token.Data)
			} else {
				text.WriteString(token.Data)
				text.WriteByte(10)
			}
			if anchor >= 0 {
				links[anchor].Text += token.Data
			}
		}
	}
	return text.String(), title.String(), links, nil
}
