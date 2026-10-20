package browsercapture

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/NatesVibeCode/forge/egresscontract"
)

// browserSearchProgram is pinned by the Node's egress policy. It only reads
// rendered document state and anchor links; it has no caller-supplied code.
const browserSearchProgram = `async args => {
  let readiness = "not_declared";
  if (args.readiness) {
    const r = args.readiness;
    const deadline = Date.now() + r.timeout_ms;
    for (;;) {
      if (r.blocked_selector && document.querySelector(r.blocked_selector)) { readiness = "blocked"; break; }
      if (r.results_selector && document.querySelector(r.results_selector)) { readiness = "ready"; break; }
      if (r.empty_selector && document.querySelector(r.empty_selector)) { readiness = "empty"; break; }
      if (Date.now() >= deadline) { readiness = "timeout"; break; }
      await new Promise(resolve => requestAnimationFrame(resolve));
    }
  }
  return {
    readiness,
    next_url: args.pagination && args.pagination.next_selector ? (document.querySelector(args.pagination.next_selector)?.href || "") : "",
    final_url: location.href,
    html: document.documentElement ? document.documentElement.outerHTML : "",
    text: document.body ? document.body.innerText : "",
    title: document.title,
    links: [...document.querySelectorAll('a[href]')].map(a => ({url:a.href,text:a.innerText})),
    results: args.mapping ? [...document.querySelectorAll(args.mapping.item_selector)].map(item => {
      const title = item.querySelector(args.mapping.title_selector);
      const text = args.mapping.text_selector ? item.querySelector(args.mapping.text_selector) : null;
      const link = args.mapping.url_selector ? item.querySelector(args.mapping.url_selector) : item.querySelector('a[href]');
      const website = args.mapping.website_selector ? item.querySelector(args.mapping.website_selector) : null;
      return {
        website: website && website.href ? website.href : "",
        key: args.mapping.key_attribute ? item.getAttribute(args.mapping.key_attribute) || "" : "",
        title: title ? (args.mapping.title_attribute ? title.getAttribute(args.mapping.title_attribute) || "" : title.innerText.trim()) : "",
        text: text ? text.innerText.trim() : "",
        url: link && link.href ? link.href : ""
      };
    }).filter(item => item.title) : []
  };
}`

type capture struct {
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

type browserState struct {
	RequestDigest   string `json:"request_digest"`
	CurrentAction   int    `json:"current_action"`
	NetworkRequests int    `json:"network_requests"`
	Actions         []struct {
		ID       string `json:"id"`
		SourceID string `json:"source_id"`
		URL      string `json:"url"`
	} `json:"actions"`
	Request struct {
		Policy struct {
			MaxRequests *int `json:"max_requests"`
		} `json:"policy"`
		Sources []struct {
			ID             string              `json:"id"`
			Kind           string              `json:"kind"`
			BrowserMapping *browserHTMLMapping `json:"browser_mapping"`
            Readiness *browserReadiness `json:"readiness"`
            Pagination *browserPagination `json:"pagination"`
		} `json:"sources"`
	} `json:"request"`
}

type browserReadiness struct {
 ResultsSelector string `json:"results_selector"`
 EmptySelector string `json:"empty_selector"`
 BlockedSelector string `json:"blocked_selector"`
 TimeoutMS int `json:"timeout_ms"`
}
type browserPagination struct { NextSelector string `json:"next_selector"` }
type browserHTMLMapping struct {
 WebsiteSelector string `json:"website_selector,omitempty"`
	ItemSelector  string `json:"item_selector"`
	TitleSelector string `json:"title_selector"`
 TitleAttribute string `json:"title_attribute,omitempty"`
	URLSelector   string `json:"url_selector,omitempty"`
	TextSelector  string `json:"text_selector,omitempty"`
	KeyAttribute  string `json:"key_attribute,omitempty"`
}

type renderedEnvelope struct {
	Status          string                                 `json:"status"`
	StatusCode      int                                    `json:"status_code"`
	SourceResponses []egresscontract.BrowserSourceResponse `json:"source_responses"`
	Payload         struct {
		FinalURL string `json:"final_url"`
		HTML     string `json:"html"`
		Results  []struct {
			Key   string `json:"key"`
			Title string `json:"title"`
			Text  string `json:"text"`
			URL   string `json:"url"`
		} `json:"results"`
	} `json:"payload"`
}

func Entry(client egresscontract.Client, in map[string]any) (map[string]any, error) {
	urlValue, ok := in["url"].(string)
	if !ok || strings.TrimSpace(urlValue) == "" {
		return nil, fmt.Errorf("discovery: browser capture URL is required")
	}
	parsed, err := url.Parse(urlValue)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return nil, fmt.Errorf("discovery: browser capture requires an absolute public HTTPS URL")
	}
	stateRaw, err := json.Marshal(in["state"])
	if err != nil {
		return nil, fmt.Errorf("discovery: browser capture state unavailable")
	}
	var state browserState
	if err := json.Unmarshal(stateRaw, &state); err != nil {
		return nil, fmt.Errorf("discovery: browser capture state invalid")
	}
	if state.CurrentAction < 0 || state.CurrentAction >= len(state.Actions) {
		return nil, fmt.Errorf("discovery: browser capture has no selected action")
	}
	action := state.Actions[state.CurrentAction]
	if action.URL != urlValue {
		return nil, fmt.Errorf("discovery: browser capture URL does not match selected action")
	}
	selectedBrowserSource := false
	var resultMapping *browserHTMLMapping
 var readiness *browserReadiness
 var pagination *browserPagination
	for _, source := range state.Request.Sources {
		if source.ID == action.SourceID && source.Kind == "browser_html_search" {
			selectedBrowserSource = true
			resultMapping = source.BrowserMapping
 readiness=source.Readiness
 pagination=source.Pagination
			break
		}
	}
	if !selectedBrowserSource {
		return nil, fmt.Errorf("discovery: selected source is not browser-backed")
	}
	if client == nil {
		return nil, fmt.Errorf("discovery: scoped browser egress client absent")
	}
	accountant, ok := client.(egresscontract.AccountingClient)
	if !ok {
		return nil, fmt.Errorf("discovery: browser receipt accounting unavailable")
	}
	contextBytes := make([]byte, 32)
	if _, err := rand.Read(contextBytes); err != nil {
		return nil, fmt.Errorf("discovery: browser context identity unavailable")
	}
	contextID := hex.EncodeToString(contextBytes)
	intent := sha256.Sum256([]byte("career-fleet.browser-search.v1\n" + state.RequestDigest + "\n" + action.ID + "\n" + urlValue))
	arguments := map[string]any{}
 if readiness!=nil{arguments["readiness"]=readiness}
 if pagination!=nil{arguments["pagination"]=pagination}
	if resultMapping != nil {
		arguments["mapping"] = resultMapping
	}
	argumentsJSON, err := json.Marshal(arguments)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(struct {
		RequestAllowance *int            `json:"request_allowance,omitempty"`
		ContextID        string          `json:"context_id"`
		IntentDigest     string          `json:"intent_digest"`
		URL              string          `json:"url"`
		Program          string          `json:"program"`
		Arguments        json.RawMessage `json:"arguments"`
	}{
		RequestAllowance: remainingRequestAllowance(state),
		ContextID:        contextID,
		IntentDigest:     hex.EncodeToString(intent[:]),
		URL:              urlValue,
		Program:          browserSearchProgram,
		Arguments:        argumentsJSON,
	})
	if err != nil {
		return nil, err
	}
	response, operationErr := client.Do(context.Background(), egresscontract.OperationRequest{Operation: "render", Body: body})
	accounting := accountant.Accounting()
	c := capture{
		URL:         urlValue,
		FinalURL:    urlValue,
		CapturedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		MediaType:   "application/vnd.career-fleet.browser-search+json",
		BodyBase64:  "",
		Outcome:     "unavailable",
		ReceiptRefs: []string{},
		Requests:    accounting.Requests,
	}
	if accounting.ReceiptDigest != "" {
		c.ReceiptRefs = append(c.ReceiptRefs, accounting.ReceiptDigest)
	}
	c.BodyBase64 = base64Encode(response.Body)
	bodyHash := sha256.Sum256(response.Body)
	c.SHA256 = hex.EncodeToString(bodyHash[:])
	var rendered renderedEnvelope
	decodeErr := json.Unmarshal(response.Body, &rendered)
	c.Status = rendered.StatusCode
	if rendered.Payload.FinalURL != "" {
		if final, parseErr := url.Parse(rendered.Payload.FinalURL); parseErr == nil && final.Scheme == "https" && final.Hostname() != "" && final.User == nil {
			c.FinalURL = final.String()
		}
	}
	switch {
	case operationErr != nil:
		c.Outcome = "transport_failure"
		c.ErrorCode = "browser_operation_failed"
	case decodeErr != nil || rendered.Status != "read" || rendered.Payload.FinalURL == "" || rendered.Payload.HTML == "":
		c.Outcome = "transport_failure"
		c.ErrorCode = "browser_document_read_incomplete"
	case accounting.ReceiptDigest == "" || accounting.Requests < 1 || accounting.Requests != accounting.Responses:
		c.Outcome = "accounting_failure"
		c.ErrorCode = "receipt_accounting_mismatch"
	case c.Status < 200 || c.Status >= 300:
		c.Outcome = "http_failure"
		c.Complete = true
	default:
		c.Outcome = "success"
		c.Complete = true
	}
	c.ID = captureDigest(c)
	return map[string]any{"capture": c}, nil
}

func remainingRequestAllowance(state browserState) *int {
	if state.Request.Policy.MaxRequests == nil {
		return nil
	}
	remaining := *state.Request.Policy.MaxRequests - state.NetworkRequests
	// Reserve one caller-authorized public GET for the Process's explicit
	// static recovery path if the browser activation loses its reply.
	if remaining > 0 {
		remaining--
	}
	if remaining < 0 {
		remaining = 0
	}
	return &remaining
}

func base64Encode(raw []byte) string {
	return base64.StdEncoding.EncodeToString(raw)
}

func captureDigest(c capture) string {
	c.ID = ""
	raw, _ := json.Marshal(c)
	sum := sha256.Sum256(raw)
	return "capture:" + hex.EncodeToString(sum[:])
}
