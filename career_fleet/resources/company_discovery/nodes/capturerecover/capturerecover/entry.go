package capturerecover

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"discovery.local/process/internal/egresscontract"
	"strings"
	"time"
)

func captureOne(client egresscontract.Client, in map[string]any) (map[string]any, error) {
	var input struct {
		URL string `json:"url"`
	}
	if err := Decode(in, &input); err != nil {
		return nil, err
	}
	if err := ValidateURL(input.URL); err != nil {
		return nil, err
	}
	if client == nil {
		return nil, fmt.Errorf("discovery: scoped egress client absent")
	}
	accountant, ok := client.(egresscontract.AccountingClient)
	if !ok {
		return nil, fmt.Errorf("discovery: receipt accounting unavailable")
	}
	c := Capture{URL: input.URL, FinalURL: input.URL, CapturedAt: time.Now().UTC().Format(time.RFC3339Nano), Outcome: "unavailable"}
	response, err := client.Do(context.Background(), egresscontract.OperationRequest{Operation: "capture", URL: input.URL})
	accounting := accountant.Accounting()
	c.Requests = accounting.Requests
	c.ReceiptRefs = []string{}
	if accounting.ReceiptDigest != "" {
		c.ReceiptRefs = append(c.ReceiptRefs, accounting.ReceiptDigest)
	}
	c.Status = response.StatusCode
	c.MediaType = response.ContentType
	for name, values := range response.Headers {
		if strings.EqualFold(name, "Location") && len(values) > 0 {
			c.RedirectLocation = values[0]
		}
	}
	if response.URL != "" {
		c.FinalURL = response.URL
	}
	c.BodyBase64 = base64.StdEncoding.EncodeToString(response.Body)
	h := sha256.Sum256(response.Body)
	c.SHA256 = hex.EncodeToString(h[:])
	switch {
	case accounting.DeniedRequests > 0:
		c.Outcome = "policy_denied"
		c.ErrorCode = "sealed_egress_denied"
	case c.Status >= 300 && c.Status < 400 && c.RedirectLocation != "":
		c.Outcome = "redirect"
		c.ErrorCode = "explicit_redirect_decision_required"
	case err != nil:
		c.Outcome = "transport_failure"
		c.ErrorCode = "transport_or_policy_failure"
	case accounting.Requests != 1 || accounting.Responses != 1 || accounting.ReceiptDigest == "":
		c.Outcome = "accounting_failure"
		c.ErrorCode = "receipt_accounting_mismatch"
	case c.Status < 200 || c.Status >= 300:
		c.Outcome = "http_failure"
		c.Complete = true
	default:
		c.Outcome = "success"
		c.Complete = true
	}
	c.ID = "capture:" + Digest(c)
	return Output(&struct {
		Capture Capture `json:"capture"`
	}{c})
}

func Entry(client egresscontract.Client, in map[string]any) (map[string]any, error) {
	requestDigest, ok := in["request_digest"].(string)
	if !ok || len(requestDigest) != 64 {
		return nil, fmt.Errorf("discovery: recovery requires exact original request digest")
	}
	request := map[string]any{}
	for k, v := range in {
		if k != "request_digest" {
			request[k] = v
		}
	}
	out, err := captureOne(client, request)
	if err != nil {
		return nil, err
	}
	out["request_digest"] = requestDigest
	out["status"] = "completed"
	return out, nil
}
