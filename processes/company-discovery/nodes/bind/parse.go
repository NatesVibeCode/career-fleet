package bind

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
	switch source.Kind {
	case "rss":
		type item struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			Description string `xml:"description"`
			Date        string `xml:"pubDate"`
			GUID        string `xml:"guid"`
		}
		var feed struct {
			XMLName xml.Name `xml:"rss"`
			Channel struct {
				Items []item `xml:"item"`
			} `xml:"channel"`
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
		d.Coverage = "feed_items" // Empty valid RSS differs from transport/parse failure.
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
	case "html":
		d.Text, d.Title, d.Links, err = HTMLText(string(body), base)
		if err != nil {
			return d, err
		}
		d.Coverage = "static_html_text"
		d.Gap = "static HTML does not establish rendered or script-loaded content coverage"
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
