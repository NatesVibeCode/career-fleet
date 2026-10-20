package egresscontract

// Wire declarations mirror OE's caller-owned provider HTTPS transport. Printed
// workers compile these bytes into their own package; OE is not a runtime import.
import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const ProviderHTTPSRequestSchema = "praxis.provider_https.request.v1"
const ProviderHTTPSResponseSchema = "praxis.provider_https.response.v1"
const ProviderHTTPSReceiptSchema = "praxis.provider_https.receipt.v1"

var ErrProviderHTTPS = errors.New("sealed provider HTTPS transport refused")

type ProviderHTTPSClaim struct {
	PackageDigest  string `json:"package_digest"`
	ProcessName    string `json:"process_name"`
	ProcessDigest  string `json:"process_digest"`
	EventID        string `json:"event_id"`
	NodeKey        string `json:"node_key"`
	NodeName       string `json:"node_name"`
	NodeDigest     string `json:"node_digest"`
	NodeSpecDigest string `json:"node_spec_digest"`
	PolicyDigest   string `json:"policy_digest"`
	Operation      string `json:"operation"`
}

type ProviderHTTPSRequest struct {
	Claim    ProviderHTTPSClaim `json:"claim"`
	RunID    string             `json:"run_id"`
	Sequence uint64             `json:"sequence"`
	Method   string             `json:"method"`
	URL      string             `json:"url"`
	Body     []byte             `json:"body"`
}

type ProviderHTTPSReceipt struct {
	Schema         string             `json:"schema"`
	Claim          ProviderHTTPSClaim `json:"claim"`
	RunID          string             `json:"run_id"`
	Sequence       uint64             `json:"sequence"`
	RequestDigest  string             `json:"request_digest"`
	ResponseDigest string             `json:"response_digest,omitempty"`
	StatusCode     int                `json:"status_code,omitempty"`
	RequestBytes   int                `json:"request_bytes"`
	ResponseBytes  int                `json:"response_bytes"`
	Transmission   string             `json:"transmission"`
	CapturedAt     string             `json:"captured_at"`
	ReceiptDigest  string             `json:"receipt_digest"`
}

type providerHTTPSRequestFrame struct {
	Schema  string               `json:"schema"`
	Request ProviderHTTPSRequest `json:"request"`
}
type providerHTTPSResponseFrame struct {
	Schema     string                `json:"schema"`
	Sequence   uint64                `json:"sequence"`
	Claim      ProviderHTTPSClaim    `json:"claim"`
	StatusCode int                   `json:"status_code,omitempty"`
	Headers    http.Header           `json:"headers,omitempty"`
	Body       []byte                `json:"body,omitempty"`
	Receipt    *ProviderHTTPSReceipt `json:"receipt,omitempty"`
	ErrorCode  string                `json:"error_code,omitempty"`
}

func providerHTTPSDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func providerHTTPSReceiptDigest(r ProviderHTTPSReceipt) string {
	r.ReceiptDigest = ""
	raw, _ := json.Marshal(r)
	return providerHTTPSDigest(append([]byte(ProviderHTTPSReceiptSchema+"\x00"), raw...))
}

// The uint64 length is protocol framing, with the platform's representable
// slice length as its only transport bound. Authored byte limits stay in policy.
func writeProviderHTTPSFrame(w io.Writer, v any) error {
	raw, e := json.Marshal(v)
	if e != nil {
		return e
	}
	var h [8]byte
	binary.BigEndian.PutUint64(h[:], uint64(len(raw)))
	if e = writeAll(w, h[:]); e != nil {
		return e
	}
	return writeAll(w, raw)
}
func readProviderHTTPSFrame(r io.Reader) ([]byte, error) {
	var h [8]byte
	if _, e := io.ReadFull(r, h[:]); e != nil {
		return nil, e
	}
	n := binary.BigEndian.Uint64(h[:])
	if n == 0 || n > uint64(int(^uint(0)>>1)) {
		return nil, ErrProviderHTTPS
	}
	var out bytes.Buffer
	_, e := io.CopyN(&out, r, int64(n))
	return out.Bytes(), e
}
func strictProviderHTTPSJSON(raw []byte, v any) error {
	// Check duplicate keys recursively before typed decoding.
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func() error
	walk = func() error {
		t, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		if delim == '{' {
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				key, ok := k.(string)
				if !ok || seen[key] {
					return ErrProviderHTTPS
				}
				seen[key] = true
				if e = walk(); e != nil {
					return e
				}
			}
		} else if delim == '[' {
			for d.More() {
				if e := walk(); e != nil {
					return e
				}
			}
		} else {
			return ErrProviderHTTPS
		}
		_, e = d.Token()
		return e
	}
	if e := walk(); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return ErrProviderHTTPS
	}
	typed := json.NewDecoder(bytes.NewReader(raw))
	typed.DisallowUnknownFields()
	if e := typed.Decode(v); e != nil {
		return fmt.Errorf("%w: invalid frame", ErrProviderHTTPS)
	}
	return nil
}
