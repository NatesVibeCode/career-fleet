package open

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"

)

// SourceRevision pins data, including extraction, readiness and pagination.
func SourceRevision(source Source) string {
	source.SourceDigest = ""
	raw, _ := json.Marshal(source)
	var value map[string]any
	_ = json.Unmarshal(raw, &value)
	delete(value, "queries")
	delete(value, "pages")
	delete(value, "block")
	delete(value, "block_key")
	return Digest(value)
}

// CSSSubset is deliberately data-only: tags, classes, ids, attribute tests,
// descendant/child combinators and selector lists. Unsupported syntax refuses.
func selectorParts(selector string) ([]string, error) {
	var parts []string
	var current strings.Builder
	depth := 0
	var quote rune
	flush := func() {
		if current.Len() > 0 {
			parts = append(parts, current.String())
			current.Reset()
		}
	}
	for _, r := range strings.TrimSpace(selector) {
		if quote != 0 {
			current.WriteRune(r)
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			current.WriteRune(r)
			continue
		}
		if r == '[' {
			depth++
		}
		if r == ']' {
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("unbalanced CSS attribute")
			}
		}
		if depth == 0 && (r == ' ' || r == '\n' || r == '\t' || r == '>') {
			flush()
			if r == '>' {
				parts = append(parts, ">")
			}
			continue
		}
		current.WriteRune(r)
	}
	flush()
	if depth != 0 || quote != 0 || len(parts) == 0 {
		return nil, fmt.Errorf("invalid CSS selector")
	}
	return parts, nil
}
func selectorAtomRE() *regexp.Regexp {
	return regexp.MustCompile(`^(\*|[a-zA-Z][a-zA-Z0-9_-]*)?((?:[.#][a-zA-Z0-9_-]+|\[[a-zA-Z0-9_:-]+(?:[\^*$~|]?=(?:"[^"]*"|'[^']*'|[^\]]+))?\])*)$`)
}
func selectorTestRE() *regexp.Regexp { return regexp.MustCompile(`([.#][a-zA-Z0-9_-]+|\[[^\]]+\])`) }
func attributeTestRE() *regexp.Regexp {
	return regexp.MustCompile(`^([a-zA-Z0-9_:-]+)(?:([\^*$~|]?=)(.*))?$`)
}
func attr(node *recipeNode, key string) string {
	for _, a := range node.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func hasAttr(node *recipeNode, key string) bool {
	for _, a := range node.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}
func atomMatches(node *recipeNode, atom string) bool {
	if node.Type != recipeElementNode {
		return false
	}
	m := selectorAtomRE().FindStringSubmatch(atom)
	if m == nil {
		return false
	}
	if m[1] != "" && m[1] != "*" && !strings.EqualFold(node.Data, m[1]) {
		return false
	}
	for _, test := range selectorTestRE().FindAllString(m[2], -1) {
		if test[0] == '#' {
			if attr(node, "id") != test[1:] {
				return false
			}
			continue
		}
		if test[0] == '.' {
			if !strings.Contains(" "+attr(node, "class")+" ", " "+test[1:]+" ") {
				return false
			}
			continue
		}
		a := attributeTestRE().FindStringSubmatch(test[1 : len(test)-1])
		if a == nil || !hasAttr(node, a[1]) {
			return false
		}
		if a[2] == "" {
			continue
		}
		actual, wanted := attr(node, a[1]), strings.Trim(a[3], "\"'")
		switch a[2] {
		case "=":
			if actual != wanted {
				return false
			}
		case "^=":
			if !strings.HasPrefix(actual, wanted) {
				return false
			}
		case "$=":
			if !strings.HasSuffix(actual, wanted) {
				return false
			}
		case "*=":
			if !strings.Contains(actual, wanted) {
				return false
			}
		case "~=":
			if !strings.Contains(" "+actual+" ", " "+wanted+" ") {
				return false
			}
		case "|=":
			if actual != wanted && !strings.HasPrefix(actual, wanted+"-") {
				return false
			}
		}
	}
	return true
}
func selectNodes(root *recipeNode, selector string) ([]*recipeNode, error) {
	if strings.TrimSpace(selector) == "" {
		return nil, nil
	}
	var result []*recipeNode
	seen := map[*recipeNode]bool{}
	for _, group := range strings.Split(selector, ",") {
		parts, err := selectorParts(group)
		if err != nil {
			return nil, err
		}
		for i, p := range parts {
			if p == ">" {
				if i == 0 || i == len(parts)-1 || parts[i-1] == ">" {
					return nil, fmt.Errorf("invalid child combinator")
				}
				continue
			}
			if selectorAtomRE().FindStringSubmatch(p) == nil {
				return nil, fmt.Errorf("unsupported CSS selector %q", p)
			}
		}
		var matches func(*recipeNode, int) bool
		matches = func(n *recipeNode, i int) bool {
			if n == nil || !atomMatches(n, parts[i]) {
				return false
			}
			if i == 0 {
				return true
			}
			if parts[i-1] == ">" {
				return matches(n.Parent, i-2)
			}
			for parent := n.Parent; parent != nil; parent = parent.Parent {
				if matches(parent, i-1) {
					return true
				}
			}
			return false
		}
		var walk func(*recipeNode)
		walk = func(n *recipeNode) {
			if matches(n, len(parts)-1) && !seen[n] {
				result = append(result, n)
				seen[n] = true
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
		}
		walk(root)
	}
	return result, nil
}
func nodeText(n *recipeNode) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	var walk func(*recipeNode)
	walk = func(x *recipeNode) {
		if x.Type == recipeElementNode && (x.Data == "script" || x.Data == "style") {
			return
		}
		if x.Type == recipeTextNode {
			b.WriteString(x.Data)
			b.WriteByte(' ')
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}
func firstNode(root *recipeNode, selector string) *recipeNode {
	nodes, _ := selectNodes(root, selector)
	if len(nodes) > 0 {
		return nodes[0]
	}
	return nil
}
func mappedNodeText(root *recipeNode, selector string) string {
	if selector == "" {
		return nodeText(root)
	}
	return nodeText(firstNode(root, selector))
}
func nodeURL(root *recipeNode, selector, attribute, base string) string {
	n := firstNode(root, selector)
	if n == nil {
		return ""
	}
	if attribute == "" {
		attribute = "href"
	}
	raw := strings.TrimSpace(attr(n, attribute))
	if raw == "" {
		return ""
	}
	target := resolveURL(base, raw)
	if ValidateURL(target) != nil {
		return ""
	}
	return target
}

func recordObservation(source Source, c Capture, title, text, target, key, website string) Observation {
	o := Observation{SourceID: source.ID, CaptureID: c.ID, Title: title, Text: text, URL: target, Field: "search_result", EntityName: title, RecordKey: key, RecordKind: source.RecordKind, WebsiteURL: website}
	if key == "" {
		o.RecordKey = target
	}
	o.LeadID = leadIDForSourceRecord(source.ID, o.RecordKey)
	o.ID = "observation:" + Digest(o)
	return o
}
func mappedHTML(root *recipeNode, base string, c Capture, source Source) ([]Observation, error) {
	m := source.HTMLMapping
	if m == nil {
		return nil, fmt.Errorf("mapped HTML requires mapping")
	}
	items, err := selectNodes(root, m.ItemSelector)
	if err != nil {
		return nil, err
	}
	var out []Observation
	for _, item := range items {
		titleRoot := item
		if m.LabelSelector != "" {
			titleRoot = nil
			labels, _ := selectNodes(item, m.LabelSelector)
			for _, label := range labels {
				if strings.EqualFold(strings.TrimSpace(nodeText(label)), m.LabelText) {
					titleRoot = label.Parent
					break
				}
			}
			if titleRoot == nil {
				continue
			}
		}
		title := mappedNodeText(titleRoot, m.TitleSelector)
		if m.LabelSelector != "" && m.TitleSelector == "" {
			title = strings.TrimSpace(strings.TrimPrefix(title, m.LabelText))
		}
		if m.TitleAttribute != "" {
			n := firstNode(titleRoot, m.TitleSelector)
			if n != nil {
				title = attr(n, m.TitleAttribute)
			}
		}
		if title == "" {
			continue
		}
		target := nodeURL(item, m.URLSelector, m.URLAttribute, base)
		if source.ExternalLinksOnly {
			target = unwrapSearchURL(target)
			if sameURLHost(base, target) {
				continue
			}
		}
		key := attr(item, m.KeyAttribute)
		rawLink := ""
		if link := firstNode(item, m.URLSelector); link != nil {
			attribute := m.URLAttribute
			if attribute == "" {
				attribute = "href"
			}
			rawLink = resolveURL(base, attr(link, attribute))
		}
		if target == "" && key == "" && rawLink != "" {
			key = "unavailable-link:" + Digest([]string{rawLink, title})
		}
		if target == "" && key == "" {
			continue
		}
		if target == "" {
			target = base
		}
		website := nodeURL(item, m.WebsiteSelector, "href", base)
		names := []string{title}
		if m.SplitText != "" {
			names = strings.Split(title, m.SplitText)
		}
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			recordKey := key
			if recordKey == "" {
				recordKey = target
			}
			if m.SplitText != "" {
				recordKey += "#" + Digest(name)
			}
			text := name
			if m.TextSelector != "" {
				text = mappedNodeText(item, m.TextSelector)
			}
			o := recordObservation(source, c, name, text, target, recordKey, website)
			o.LinkURL = rawLink
			out = append(out, o)
		}
	}
	return out, nil
}

func structuredOrganizations(root *recipeNode, base string, c Capture, source Source, detail bool) []Observation {
	scripts, _ := selectNodes(root, `script[type="application/ld+json"]`)
	var out []Observation
	seen := map[string]bool{}
	var walk func(any, bool)
	walk = func(value any, inList bool) {
		switch item := value.(type) {
		case []any:
			for _, v := range item {
				walk(v, inList)
			}
		case map[string]any:
			kind := mappedText(item["@type"])
			isList := strings.Contains(kind, "ItemList") || strings.Contains(kind, "ListItem")
			organization := strings.Contains(kind, "Organization") || strings.Contains(kind, "LocalBusiness") || strings.Contains(kind, "Corporation") || strings.Contains(kind, "Store") || strings.Contains(kind, "ProfessionalService")
			name := mappedText(item["name"])
			if organization && (inList || detail && source.SubjectName != "" && strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(source.SubjectName))) {
				website := mappedText(item["url"])
				key := mappedText(item["@id"])
				if key == "" {
					key = mappedText(item["identifier"])
				}
				target := website
				if ValidateURL(target) != nil {
					target = base
				}
				if key == "" {
					key = target + "#" + name
				}
				if name != "" && !seen[key] {
					o := recordObservation(source, c, name, mappedText(item["description"]), target, key, "")
					o.RecordKind = "organization"
					if ValidateURL(website) == nil {
						o.WebsiteURL = website
					}
					if id := mappedText(item["@id"]); id != "" {
						o.EntityID = "schemaorg:" + id
					}
					o.ID = "observation:" + Digest(o)
					out = append(out, o)
					seen[key] = true
				}
			}
			keys := make([]string, 0, len(item))
			for k := range item {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				v := item[k]
				switch v.(type) {
				case map[string]any, []any:
					walk(v, inList || isList && k == "itemListElement" || isList && k == "item" || detail && strings.Contains(kind, "Product") && (k == "manufacturer" || k == "brand"))
				}
			}
		}
	}
	for _, script := range scripts {
		var b strings.Builder
		for child := script.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == recipeTextNode {
				b.WriteString(child.Data)
			}
		}
		var value any
		if json.Unmarshal([]byte(b.String()), &value) == nil {
			walk(value, false)
		}
	}
	return out
}
func tableOrganizations(root *recipeNode, base string, c Capture, source Source) []Observation {
	tables, _ := selectNodes(root, "table")
	var out []Observation
	for _, table := range tables {
		rows, _ := selectNodes(table, "tr")
		companyColumn := -1
		for _, row := range rows {
			cells, _ := selectNodes(row, "th,td")
			if len(cells) == 0 {
				continue
			}
			headers := false
			for i, cell := range cells {
				label := strings.ToLower(nodeText(cell))
				if cell.Data == "th" {
					headers = true
				}
				switch label {
				case "company", "company name", "business", "business name", "supplier", "supplier name", "vendor", "vendor name", "member", "member name", "exhibitor", "exhibitor name":
					companyColumn = i
				}
			}
			if headers || companyColumn < 0 || companyColumn >= len(cells) {
				continue
			}
			cell := cells[companyColumn]
			name := nodeText(cell)
			target := nodeURL(cell, "a[href]", "href", base)
			if name == "" || target == "" {
				continue
			}
			o := recordObservation(source, c, name, nodeText(row), target, "row:"+Digest([]string{name, target, nodeText(row)}), "")
			o.RecordKind = "organization"
			out = append(out, o)
		}
	}
	return out
}
func parseCollectionHTML(c Capture, source Source, body []byte, base string) (Document, error) {
	d := Document{CaptureID: c.ID, Coverage: "collection_html_records", Outcome: "parsed", Health: "mapping_failure", Preset: source.Preset}
	root, err := parseRecipeTree(bytes.NewReader(body))
	if err != nil {
		return d, err
	}
	d.Text, d.Title, d.Links, err = HTMLText(string(body), base)
	if err != nil {
		return d, err
	}
	if source.Readiness != nil {
		r := source.Readiness
		if firstNode(root, r.BlockedSelector) != nil {
			d.Health = "blocked"
			d.Gap = "declared blocked state matched"
			return d, nil
		}
		if firstNode(root, r.EmptySelector) != nil {
			d.Health = "confirmed_empty"
			return d, nil
		}
	}
	switch source.Preset {
	case "mapped_html":
		d.Results, err = mappedHTML(root, base, c, source)
	case "jsonld_organizations":
		d.Results = structuredOrganizations(root, base, c, source, false)
	case "html_table":
		d.Results = tableOrganizations(root, base, c, source)
	case "organization_detail":
		d.Results = structuredOrganizations(root, base, c, source, true)
	case "auto_collection", "":
		d.Results = structuredOrganizations(root, base, c, source, false)
		if len(d.Results) > 0 {
			d.Preset = "jsonld_organizations"
		} else {
			d.Results = tableOrganizations(root, base, c, source)
			if len(d.Results) > 0 {
				d.Preset = "html_table"
			}
		}
	default:
		return d, fmt.Errorf("unsupported HTML preset %q", source.Preset)
	}
	if err != nil {
		return d, err
	}
	if len(d.Results) > 0 {
		d.Health = "working"
	} else {
		d.Gap = "no records from supported structure; empty results are unconfirmed"
		if source.Preset == "auto_collection" || source.Preset == "" {
			d.Health = "unsupported_structure"
		}
	}
	if source.Pagination != nil && source.Pagination.Mode == "next_link" {
		d.NextURL = nodeURL(root, source.Pagination.NextSelector, "href", base)
		if source.Pagination.NextStrategy == "lexical_after_current" {
			d.NextURL = ""
			links, _ := selectNodes(root, source.Pagination.NextSelector)
			targets := []string{}
			current, _ := url.Parse(base)
			for _, link := range links {
				target := resolveURL(base, attr(link, "href"))
				next, e := url.Parse(target)
				if e == nil && sameURLHost(base, target) && ValidateURL(target) == nil && strings.TrimRight(strings.ToLower(next.Path), "/") > strings.TrimRight(strings.ToLower(current.Path), "/") {
					targets = appendUnique(targets, target)
				}
			}
			sort.Strings(targets)
			if len(targets) > 0 {
				d.NextURL = targets[0]
			}
		}
	}
	return d, nil
}
func recipeBody(c Capture) []byte {
	body, _ := base64.StdEncoding.DecodeString(c.BodyBase64)
	return body
}
func safeNextURL(base, next string) string {
	if strings.TrimSpace(next) == "" {
		return ""
	}
	value := resolveURL(base, next)
	if ValidateURL(value) != nil || !sameURLHost(base, value) {
		return ""
	}
	return value
}
func jsonNext(source Source, body []byte, base string) string {
	if source.Pagination == nil {
		return ""
	}
	var v any
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	p := source.Pagination
	if p.Mode == "next_link" {
		value, _ := jsonPointerValue(v, p.NextPath)
		return safeNextURL(base, mappedText(value))
	}
	if p.Mode == "cursor" {
		value, ok := jsonPointerValue(v, p.CursorPath)
		if !ok || mappedText(value) == "" {
			return ""
		}
		u, err := url.Parse(base)
		if err != nil {
			return ""
		}
		q := u.Query()
		q.Set(p.CursorParameter, mappedText(value))
		u.RawQuery = q.Encode()
		return u.String()
	}
	return ""
}

// Decode JSON data only; never execute script source. Streaming SSR calls may
// wrap one JSON object in .push(...); the decoder consumes that object alone.
func embeddedRecordURLs(document, template string) map[string]string {
	result := map[string]string{}
	if template == "" {
		return result
	}
	root, e := parseRecipeTree(strings.NewReader(document))
	if e != nil {
		return result
	}
	scripts, _ := selectNodes(root, "script")
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, v := range x {
				walk(v)
			}
		case map[string]any:
			id, slug := mappedText(x["id"]), mappedText(x["slug"])
			if id != "" && slug != "" {
				target := strings.ReplaceAll(strings.ReplaceAll(template, "{id}", url.PathEscape(id)), "{slug}", url.PathEscape(slug))
				if ValidateURL(target) == nil {
					result[id] = target
				}
			}
			keys := []string{}
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(x[k])
			}
		}
	}
	for _, script := range scripts {
		var b strings.Builder
		for c := script.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == recipeTextNode {
				b.WriteString(c.Data)
			}
		}
		raw := strings.TrimSpace(b.String())
		if i := strings.Index(raw, ".push("); i >= 0 {
			raw = strings.TrimSpace(raw[i+6:])
		}
		var value any
		d := json.NewDecoder(strings.NewReader(raw))
		d.UseNumber()
		if d.Decode(&value) == nil {
			walk(value)
		}
	}
	return result
}

// The static recipe tree is built from the pinned HTML tokenizer. It supports
// the declared selector subset, not JavaScript execution or HTML5 browser repair.
const (
	recipeDocumentNode = iota
	recipeElementNode
	recipeTextNode
)

type recipeNode struct {
	Type                                       int
	Data                                       string
	Attr                                       []fc_fe63640163f18bb75b2718540dff620e5da9febcd05ceaf48e3cfba1e6cb99eb_Attribute
	Parent, FirstChild, LastChild, NextSibling *recipeNode
}

func parseRecipeTree(reader io.Reader) (*recipeNode, error) {
	root := &recipeNode{Type: recipeDocumentNode}
	stack := []*recipeNode{root}
	z := fc_fe63640163f18bb75b2718540dff620e5da9febcd05ceaf48e3cfba1e6cb99eb_NewTokenizer(reader)
	appendNode := func(n *recipeNode) {
		parent := stack[len(stack)-1]
		n.Parent = parent
		if parent.LastChild == nil {
			parent.FirstChild = n
		} else {
			parent.LastChild.NextSibling = n
		}
		parent.LastChild = n
	}
	for {
		kind := z.Next()
		if kind == fc_fe63640163f18bb75b2718540dff620e5da9febcd05ceaf48e3cfba1e6cb99eb_ErrorToken {
			if z.Err() != io.EOF {
				return nil, z.Err()
			}
			return root, nil
		}
		token := z.Token()
		switch kind {
		case fc_fe63640163f18bb75b2718540dff620e5da9febcd05ceaf48e3cfba1e6cb99eb_StartTagToken, fc_fe63640163f18bb75b2718540dff620e5da9febcd05ceaf48e3cfba1e6cb99eb_SelfClosingTagToken:
			tag := token.Data
			if len(stack) > 1 {
				top := stack[len(stack)-1].Data
				if tag == "tr" && top == "tr" || (tag == "td" || tag == "th") && (top == "td" || top == "th") || tag == "li" && top == "li" {
					stack = stack[:len(stack)-1]
				}
			}
			n := &recipeNode{Type: recipeElementNode, Data: tag, Attr: token.Attr}
			appendNode(n)
			if kind == fc_fe63640163f18bb75b2718540dff620e5da9febcd05ceaf48e3cfba1e6cb99eb_StartTagToken && !strings.Contains(" area base br col embed hr img input link meta param source track wbr ", " "+tag+" ") {
				stack = append(stack, n)
			}
		case fc_fe63640163f18bb75b2718540dff620e5da9febcd05ceaf48e3cfba1e6cb99eb_EndTagToken:
			for i := len(stack) - 1; i > 0; i-- {
				if stack[i].Data == token.Data {
					stack = stack[:i]
					break
				}
			}
		case fc_fe63640163f18bb75b2718540dff620e5da9febcd05ceaf48e3cfba1e6cb99eb_TextToken:
			appendNode(&recipeNode{Type: recipeTextNode, Data: token.Data})
		}
	}
}

// The safe-failure probe uses the same real capture and an intentionally absent
// mapping. It proves refusal to convert a missing structure into valid records;
// it is a Function check, not an assertion of provider admission.
func negativeMappingCheck(c Capture, source Source) bool {
	negative := source
	negative.Readiness = nil
	negative.SourceDigest = ""
	if source.Kind == "ocds_json" {
		negative.Kind = "json_mapped"
		negative.Preset = "mapped_json"
		negative.Mapping = &JSONMapping{ItemsPath: "/__career_fleet_negative_" + c.SHA256, TitlePath: "/name", URLPath: "/url"}
	} else if (source.Kind == "json_mapped" || source.Kind == "embedded_json_mapped") && source.Mapping != nil {
		mapping := *source.Mapping
		mapping.ItemsPath = "/__career_fleet_negative_" + c.SHA256
		negative.Mapping = &mapping
	} else {
		negative.Kind = "collection_html"
		negative.Preset = "mapped_html"
		negative.HTMLMapping = &HTMLResultMapping{ItemSelector: "#career-fleet-negative-" + c.SHA256, TitleSelector: "h2", URLSelector: "a[href]"}
	}
	d, err := ParseCapture(c, negative)
	return err == nil && len(d.Results) == 0 && (d.Health == "mapping_failure" || d.Outcome == "parse_failure")
}
