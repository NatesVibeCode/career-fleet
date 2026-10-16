package egresscontract

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"strings"
	"unicode/utf8"
)

const DiagnosticPayloadLimit = 16384

// FailureDiagnostic records evidence without turning a refused invocation into
// a successful output. Payload is a bounded, credential-redacted excerpt.
type FailureDiagnostic struct {
	Stage            string `json:"stage"`
	Code             string `json:"code"`
	Detail           string `json:"detail,omitempty"`
	StatusCode       int    `json:"status_code,omitempty"`
	RequestSHA256    string `json:"request_sha256,omitempty"`
	ResponseSHA256   string `json:"response_sha256,omitempty"`
	ResponseBytes    int    `json:"response_bytes"`
	ResponseLimit    int    `json:"response_limit,omitempty"`
	ProviderID       string `json:"provider_id,omitempty"`
	Provider         string `json:"provider,omitempty"`
	FinishReason     string `json:"finish_reason,omitempty"`
	Payload          string `json:"payload,omitempty"`
	PayloadTruncated bool   `json:"payload_truncated,omitempty"`
}

func diagnosticSHA(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func diagnosticText(raw []byte, secret []byte, limit int) (string, bool) {
	if len(secret) > 0 {
		raw = bytes.ReplaceAll(raw, secret, []byte("[credential redacted]"))
	}
	text := strings.ToValidUTF8(string(raw), "\uFFFD")
	truncated := len(text) > limit
	if truncated {
		text = text[:limit]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	return text, truncated
}

func responseDiagnostic(request, body, secret []byte, status, limit int, requestID string) *FailureDiagnostic {
	payload, truncated := diagnosticText(body, secret, DiagnosticPayloadLimit)
	id, _ := diagnosticText([]byte(requestID), secret, 256)
	return &FailureDiagnostic{Stage: "transport", Code: "response_received", StatusCode: status,
		RequestSHA256: diagnosticSHA(request), ResponseSHA256: diagnosticSHA(body), ResponseBytes: len(body),
		ResponseLimit: limit, ProviderID: id, Payload: payload, PayloadTruncated: truncated}
}

func diagnosticReadCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "response_timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "response_cancelled"
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return "response_timeout"
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return "response_unexpected_eof"
	}
	return "response_read_failed"
}

func cloneFailureDiagnostic(d *FailureDiagnostic) *FailureDiagnostic {
	if d == nil {
		return nil
	}
	copy := *d
	return &copy
}
