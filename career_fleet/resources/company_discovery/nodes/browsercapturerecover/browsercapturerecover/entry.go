package browsercapturerecover

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"discovery.local/process/internal/egresscontract"
)

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

type recoveryState struct {
	CurrentAction   int `json:"current_action"`
	NetworkRequests int `json:"network_requests"`
	Request         struct {
		Policy struct {
			AcquisitionEnabled bool `json:"acquisition_enabled"`
			MaxRequests        *int `json:"max_requests"`
		} `json:"policy"`
		Sources []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"sources"`
	} `json:"request"`
	Actions []struct {
		ID       string `json:"id"`
		SourceID string `json:"source_id"`
		URL      string `json:"url"`
	} `json:"actions"`
}

type responseEvidence struct {
	RequestID    int64               `json:"request_id"`
	URL          string              `json:"url"`
	Method       string              `json:"method"`
	ResourceType string              `json:"resource_type"`
	Status       int                 `json:"status"`
	Headers      map[string][]string `json:"headers"`
	BodyBase64   string              `json:"body_base64"`
	SHA256       string              `json:"sha256"`
	Outcome      string              `json:"outcome"`
	Complete     bool                `json:"complete"`
}

type staticRecoveryEnvelope struct {
	Mode            string             `json:"mode"`
	Status          string             `json:"status"`
	StatusCode      int                `json:"status_code"`
	DeniedRequests  int                `json:"denied_requests"`
	SourceResponses []responseEvidence `json:"source_responses"`
	Payload         struct {
		Status     string `json:"status"`
		StatusCode int    `json:"status_code"`
		Payload    struct {
			FinalURL string `json:"final_url"`
			HTML     string `json:"html"`
			Text     string `json:"text"`
			Title    string `json:"title"`
		} `json:"payload"`
	} `json:"payload"`
}

func Entry(client egresscontract.Client, in map[string]any) (map[string]any, error) {
	urlValue, ok := in["url"].(string)
	if !ok || strings.TrimSpace(urlValue) == "" {
		return nil, fmt.Errorf("discovery: browser recovery URL is required")
	}
	parsed, err := url.Parse(urlValue)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return nil, fmt.Errorf("discovery: browser recovery requires an absolute public HTTPS URL")
	}
	requestDigest, ok := in["request_digest"].(string)
	if !ok || requestDigest == "" {
		return nil, fmt.Errorf("discovery: browser recovery request identity is required")
	}
	stateRaw, err := json.Marshal(in["state"])
	if err != nil {
		return nil, fmt.Errorf("discovery: browser recovery state unavailable")
	}
	var state recoveryState
	if err := json.Unmarshal(stateRaw, &state); err != nil {
		return nil, fmt.Errorf("discovery: browser recovery state invalid")
	}
	if state.CurrentAction < 0 || state.CurrentAction >= len(state.Actions) {
		return nil, fmt.Errorf("discovery: browser recovery has no selected action")
	}
	action := state.Actions[state.CurrentAction]
	if action.URL != urlValue {
		return nil, fmt.Errorf("discovery: browser recovery URL does not match selected action")
	}
	selectedBrowserSource := false
	for _, source := range state.Request.Sources {
		if source.ID == action.SourceID && source.Kind == "browser_html_search" {
			selectedBrowserSource = true
			break
		}
	}
	if !selectedBrowserSource {
		return nil, fmt.Errorf("discovery: browser recovery source is not browser-backed")
	}
	if !state.Request.Policy.AcquisitionEnabled || (state.Request.Policy.MaxRequests != nil && state.NetworkRequests >= *state.Request.Policy.MaxRequests) {
		c := incompleteCapture(urlValue, "explicit read allowance exhausted")
		return recoveryOutput(c, requestDigest, "unknown"), nil
	}
	if client == nil {
		return nil, fmt.Errorf("discovery: scoped HTTPS recovery client absent")
	}
	accountant, ok := client.(egresscontract.AccountingClient)
	if !ok {
		return nil, fmt.Errorf("discovery: browser recovery receipt accounting unavailable")
	}
	response, operationErr := client.Do(context.Background(), egresscontract.OperationRequest{Operation: "reconcile", URL: urlValue})
	accounting := accountant.Accounting()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	raw := response.Body
	responseHash := sha256.Sum256(raw)
	item := responseEvidence{
		RequestID: 1, URL: urlValue, Method: "GET", ResourceType: "document", Status: response.StatusCode,
		Headers: response.Headers, BodyBase64: base64.StdEncoding.EncodeToString(raw),
		SHA256: hex.EncodeToString(responseHash[:]), Outcome: "transport_failure", Complete: false,
	}
	if item.Headers == nil {
		item.Headers = map[string][]string{}
	}
	envelope := staticRecoveryEnvelope{Mode: "static_recovery", Status: "static_recovery", StatusCode: response.StatusCode, SourceResponses: []responseEvidence{item}}
	envelope.Payload.Status = "read"
	envelope.Payload.StatusCode = response.StatusCode
	envelope.Payload.Payload.FinalURL = urlValue
	envelope.Payload.Payload.HTML = string(raw)
	envelope.Payload.Payload.Text = string(raw)
	if response.URL != "" {
		if final, parseErr := url.Parse(response.URL); parseErr == nil && final.Scheme == "https" && final.Hostname() != "" && final.User == nil {
			envelope.Payload.Payload.FinalURL = final.String()
		}
	}
	for name, values := range response.Headers {
		if strings.EqualFold(name, "Location") && len(values) > 0 {
			item.Headers[name] = values
		}
	}
	c := capture{
		URL: urlValue, FinalURL: envelope.Payload.Payload.FinalURL, CapturedAt: now,
		Status: response.StatusCode, MediaType: "application/vnd.career-fleet.browser-search+json",
		Outcome: "transport_failure", ErrorCode: "browser_recovery_read_failed",
		ReceiptRefs: []string{}, Requests: accounting.Requests,
	}
	if accounting.ReceiptDigest != "" {
		c.ReceiptRefs = append(c.ReceiptRefs, accounting.ReceiptDigest)
	}
	if len(raw) > 0 {
		item.Outcome = "success"
		item.Complete = true
		envelope.SourceResponses[0] = item
	}
	if accounting.Requests == 1 && accounting.Responses == 1 && accounting.ReceiptDigest != "" && operationErr == nil && response.StatusCode >= 200 && response.StatusCode < 300 && utf8.Valid(raw) {
		c.Outcome = "success"
		c.ErrorCode = ""
		c.Complete = true
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	c.BodyBase64 = base64.StdEncoding.EncodeToString(body)
	outerHash := sha256.Sum256(body)
	c.SHA256 = hex.EncodeToString(outerHash[:])
	c.ID = captureDigest(c)
	status := "unknown"
	if c.Outcome == "success" && c.Complete {
		status = "completed"
	}
	return recoveryOutput(c, requestDigest, status), nil
}

func incompleteCapture(rawURL, reason string) capture {
	c := capture{URL: rawURL, FinalURL: rawURL, CapturedAt: time.Now().UTC().Format(time.RFC3339Nano),
		MediaType: "application/vnd.career-fleet.browser-search+json", Outcome: "unavailable", ErrorCode: reason,
		ReceiptRefs: []string{}}
	bodyHash := sha256.Sum256(nil)
	c.SHA256 = hex.EncodeToString(bodyHash[:])
	c.ID = captureDigest(c)
	return c
}

func recoveryOutput(c capture, requestDigest, status string) map[string]any {
	return map[string]any{"capture": c, "request_digest": requestDigest, "status": status}
}

func captureDigest(c capture) string {
	c.ID = ""
	raw, _ := json.Marshal(c)
	sum := sha256.Sum256(raw)
	return "capture:" + hex.EncodeToString(sum[:])
}
