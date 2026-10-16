package egresscontract

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	PublicReadRequestSchema  = "praxis.public_read.request.v1"
	PublicReadResponseSchema = "praxis.public_read.response.v1"
	PublicReadReceiptSchema  = "praxis.public_read.receipt.v1"
	// v1 encodes frame length in exactly four bytes. Larger frames require a
	// new wire version; never silently truncate or wrap the length.
	MaxPublicReadFrameBytes = (1 << 32) - 1
)

// PublicReadClaim is the worker's assertion for one pipe request. The OE
// controller compares every field with authority read from the sealed package
// and adds its own package/event binding to the returned receipt.
type PublicReadClaim struct {
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

// PublicReadReceipt is the caller/controller's identity-bound public HTTP
// evidence, retained in the same Node egress receipt as the ordinary request
// accounting.
type PublicReadReceipt struct {
	Schema         string          `json:"schema"`
	Claim          PublicReadClaim `json:"claim"`
	Sequence       uint64          `json:"sequence"`
	Method         string          `json:"method"`
	URL            string          `json:"url"`
	RequestBytes   int             `json:"request_bytes"`
	ResponseStatus int             `json:"response_status"`
	ResponseBytes  int             `json:"response_bytes"`
	ResponseDigest string          `json:"response_digest"`
	RequestDigest  string          `json:"request_digest"`
	CapturedAt     string          `json:"captured_at"`
	ReceiptDigest  string          `json:"receipt_digest"`
}

type publicReadRequest struct {
	Schema  string                   `json:"schema"`
	Request publicReadRequestContent `json:"request"`
}

type publicReadRequestContent struct {
	Claim    PublicReadClaim `json:"claim"`
	Sequence uint64          `json:"sequence"`
	Method   string          `json:"method"`
	URL      string          `json:"url"`
}

type publicReadResponse struct {
	Schema        string             `json:"schema"`
	Sequence      uint64             `json:"sequence"`
	Claim         PublicReadClaim    `json:"claim"`
	StatusCode    int                `json:"status_code,omitempty"`
	URL           string             `json:"url,omitempty"`
	Body          []byte             `json:"body,omitempty"`
	ContentType   string             `json:"content_type,omitempty"`
	ContentSignal string             `json:"content_signal,omitempty"`
	Receipt       *PublicReadReceipt `json:"receipt,omitempty"`
	ErrorCode     string             `json:"error_code,omitempty"`
}

// PublicReadPipeClient consumes the two inherited anonymous descriptors. The
// shared mutex serializes all Node operations through one process pipe, so a
// response can only be paired with its exact monotonically numbered request.
type PublicReadPipeClient struct {
	read   *os.File
	write  *os.File
	mu     sync.Mutex
	seq    uint64
	refs   int
	closed bool
}

// OpenPublicReadPipeClient fails closed when the selected transport's fixed
// inherited descriptors are missing, not pipes, or otherwise unusable. There
// is no direct-network fallback from this constructor.
func OpenPublicReadPipeClient() (*PublicReadPipeClient, error) {
	reader := os.NewFile(uintptr(3), "public-read-response")
	writer := os.NewFile(uintptr(4), "public-read-request")
	if reader == nil || writer == nil {
		if reader != nil {
			_ = reader.Close()
		}
		if writer != nil {
			_ = writer.Close()
		}
		return nil, errors.New("public-read pipe descriptors are absent")
	}
	client, err := newPublicReadPipeClient(reader, writer)
	if err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, err
	}
	return client, nil
}

func newPublicReadPipeClient(reader, writer *os.File) (*PublicReadPipeClient, error) {
	if reader == nil || writer == nil || reader == writer || reader.Fd() == writer.Fd() {
		return nil, errors.New("public-read pipe descriptors are incomplete")
	}
	for _, file := range []*os.File{reader, writer} {
		info, err := file.Stat()
		if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
			return nil, errors.New("public-read descriptor is not an inherited pipe")
		}
	}
	return &PublicReadPipeClient{read: reader, write: writer}, nil
}

func (c *PublicReadPipeClient) retain() error {
	if c == nil {
		return errors.New("public-read pipe client is absent")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("public-read pipe client is closed")
	}
	c.refs++
	return nil
}

func (c *PublicReadPipeClient) release() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.refs > 0 {
		c.refs--
	}
	closeNow := c.refs == 0 && !c.closed
	if closeNow {
		c.closed = true
	}
	c.mu.Unlock()
	if !closeNow {
		return nil
	}
	return errors.Join(c.read.Close(), c.write.Close())
}

// Close releases an unused client if egress setup refuses before any Node
// adapter retains it.
func (c *PublicReadPipeClient) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	return errors.Join(c.read.Close(), c.write.Close())
}

// PublicReadPipeHTTPS is the opt-in HTTPS adapter for caller-selected public
// GETs. Selecting it never falls back to NativeHTTPS if a frame fails.
type PublicReadPipeHTTPS struct {
	Client *PublicReadPipeClient
	Claim  PublicReadClaim
}

func (PublicReadPipeHTTPS) Name() string { return "inherited.public_read_pipe.v1" }

func (a PublicReadPipeHTTPS) Supports(policy Policy) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	if policy.Mode != ModeHTTPS || len(policy.Operations) == 0 {
		return errors.New("public-read pipe requires HTTPS operations")
	}
	for _, operation := range Normalize(policy).Operations {
		if operation.Destination.Kind != DestinationPublicWeb || operation.Method != "GET" || len(operation.Credentials) != 0 ||
			operation.MaxRedirects != 0 || operation.RedirectPolicy != RedirectNone || len(operation.QueryParameters) != 0 {
			return errors.New("public-read pipe permits only credential-free redirect-free public GETs")
		}
	}
	return nil
}

func (a PublicReadPipeHTTPS) Start(ctx context.Context, invocation Invocation) (InvocationHandle, error) {
	if a.Client == nil {
		return nil, errors.New("selected public-read pipe client is absent")
	}
	if err := a.Supports(invocation.Policy); err != nil {
		return nil, err
	}
	binding, err := ValidateBinding(invocation.Policy, invocation.Binding)
	if err != nil {
		return nil, err
	}
	var operation Operation
	for _, candidate := range Normalize(invocation.Policy).Operations {
		if candidate.ID == binding.Operation {
			operation = candidate
		}
	}
	if operation.ID == "" || !publicReadClaimMatches(a.Claim, invocation.Identity, operation.ID) || binding.CredentialRef != "" {
		return nil, errors.New("public-read pipe claim does not match the exact Node operation")
	}
	if err := a.Client.retain(); err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = a.Client.release()
		}
	}()
	roundTripper := &publicReadRoundTripper{client: a.Client, claim: a.Claim, operation: operation}
	handle, err := startNativeHTTPS(ctx, invocation, a.Name(), roundTripper)
	if err != nil {
		return nil, err
	}
	keep = true
	return handle, nil
}

func publicReadClaimMatches(claim PublicReadClaim, identity Identity, operation string) bool {
	return strings.HasPrefix(claim.PackageDigest, "sha256:") && len(claim.PackageDigest) == 71 &&
		claim.ProcessName != "" && len(claim.ProcessDigest) == 64 && claim.EventID != "" &&
		claim.NodeKey == identity.NodeKey && claim.NodeName == identity.NodeName && claim.NodeDigest == identity.NodeDigest &&
		claim.NodeSpecDigest == identity.NodeSpecDigest && len(claim.PolicyDigest) == 64 && claim.Operation == operation && identity.Operation == operation
}

type publicReadRoundTripper struct {
	client    *PublicReadPipeClient
	claim     PublicReadClaim
	operation Operation
	last      *PublicReadReceipt
}

func (t *publicReadRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || request.Method != "GET" || request.Body != nil && request.Body != http.NoBody || request.ContentLength > 0 {
		return nil, errors.New("public-read pipe refuses a non-GET request or body")
	}
	if request.URL.String() == "" || request.URL.User != nil || request.URL.Scheme != "https" || request.URL.Fragment != "" {
		return nil, errors.New("public-read pipe refuses an unsafe URL")
	}
	if err := requestHeadersMatch(t.operation, request.Header); err != nil {
		return nil, err
	}
	t.client.mu.Lock()
	defer t.client.mu.Unlock()
	if t.client.closed {
		return nil, errors.New("public-read pipe is closed")
	}
	t.client.seq++
	requestContent := publicReadRequestContent{Claim: t.claim, Sequence: t.client.seq, Method: request.Method, URL: request.URL.String()}
	if err := writePublicReadFrame(t.client.write, publicReadRequest{Schema: PublicReadRequestSchema, Request: requestContent}, MaxPublicReadFrameBytes); err != nil {
		return nil, errors.New("public-read request transport failed")
	}
	raw, err := readPublicReadFrame(t.client.read, MaxPublicReadFrameBytes)
	if err != nil {
		return nil, errors.New("public-read response transport failed")
	}
	var response publicReadResponse
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&response) != nil {
		return nil, errors.New("public-read response frame is invalid")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, errors.New("public-read response frame has trailing data")
	}
	if response.ErrorCode != "" {
		return nil, fmt.Errorf("%s", response.ErrorCode)
	}
	if response.Schema != PublicReadResponseSchema || response.Sequence != requestContent.Sequence || response.Claim != t.claim || response.URL != request.URL.String() || response.Receipt == nil {
		return nil, errors.New("public-read response identity mismatch")
	}
	if response.StatusCode < 100 || response.StatusCode > 599 || isRedirectStatus(response.StatusCode) || (t.operation.MaxResponseBytes > 0 && len(response.Body) > t.operation.MaxResponseBytes) {
		return nil, errors.New("public-read response is outside operation bounds")
	}
	if response.Receipt.Claim != t.claim || response.Receipt.Sequence != requestContent.Sequence || response.Receipt.Method != request.Method ||
		response.Receipt.URL != request.URL.String() || response.Receipt.ResponseStatus != response.StatusCode || response.Receipt.ResponseBytes != len(response.Body) ||
		response.Receipt.RequestBytes != 0 || !equalDigest(response.Receipt.ResponseDigest, sha256.Sum256(response.Body)) ||
		response.Receipt.RequestDigest != digestRequest(requestContent) || !validPublicReadReceipt(*response.Receipt) {
		return nil, errors.New("public-read receipt identity or digest mismatch")
	}
	if len(response.ContentType) > 128 || strings.ContainsAny(response.ContentType, "\r\n\x00") || len(response.ContentSignal) > 512 || strings.ContainsAny(response.ContentSignal, "\r\n\x00") {
		return nil, errors.New("public-read response metadata exceeds its bound")
	}
	receipt := *response.Receipt
	t.last = &receipt
	header := make(http.Header)
	if response.ContentType != "" {
		header.Set("Content-Type", response.ContentType)
	}
	if response.ContentSignal != "" {
		header.Set("Content-Signal", response.ContentSignal)
	}
	return &http.Response{Status: fmt.Sprintf("%d %s", response.StatusCode, http.StatusText(response.StatusCode)), StatusCode: response.StatusCode,
		Header: header, Body: io.NopCloser(bytes.NewReader(response.Body)), ContentLength: int64(len(response.Body)), Request: request}, nil
}

func (t *publicReadRoundTripper) TakePublicReadReceipt() *PublicReadReceipt {
	if t.last == nil {
		return nil
	}
	receipt := *t.last
	t.last = nil
	return &receipt
}

func (t *publicReadRoundTripper) Close() error { return t.client.release() }

func requestHeadersMatch(operation Operation, headers http.Header) error {
	expected := make(map[string]string, len(operation.Headers))
	for name, value := range operation.Headers {
		lower := strings.ToLower(name)
		if lower != "accept" && lower != "user-agent" {
			return errors.New("public-read policy contains a forbidden header")
		}
		expected[http.CanonicalHeaderKey(name)] = value
	}
	for name, values := range headers {
		canonical := http.CanonicalHeaderKey(name)
		value, ok := expected[canonical]
		if !ok || len(values) != 1 || values[0] != value {
			return errors.New("public-read request header is not sealed by policy")
		}
		delete(expected, canonical)
	}
	if len(expected) != 0 {
		return errors.New("public-read request omitted a sealed header")
	}
	return nil
}

func writePublicReadFrame(writer io.Writer, value any, max uint64) error {
	frame, err := json.Marshal(value)
	if err != nil || len(frame) == 0 || uint64(len(frame)) > max || uint64(len(frame)) > MaxPublicReadFrameBytes {
		return errors.New("public-read frame exceeds its bound")
	}
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(frame)))
	if err := writeAll(writer, length[:]); err != nil {
		return err
	}
	return writeAll(writer, frame)
}

func readPublicReadFrame(reader io.Reader, max uint64) ([]byte, error) {
	var length [4]byte
	n, err := io.ReadFull(reader, length[:])
	if errors.Is(err, io.EOF) && n == 0 {
		return nil, io.EOF
	}
	if err != nil {
		return nil, errors.New("truncated public-read frame header")
	}
	size := binary.BigEndian.Uint32(length[:])
	if size == 0 || uint64(size) > max {
		return nil, errors.New("public-read frame length exceeds its bound")
	}
	frame, err := io.ReadAll(io.LimitReader(reader, int64(size)))
	if err != nil || uint64(len(frame)) != uint64(size) {
		return nil, errors.New("truncated public-read frame body")
	}
	return frame, nil
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func digestRequest(request publicReadRequestContent) string {
	raw, _ := json.Marshal(request)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func equalDigest(actual string, sum [sha256.Size]byte) bool {
	return actual == hex.EncodeToString(sum[:])
}

func validPublicReadReceipt(receipt PublicReadReceipt) bool {
	if receipt.Schema != PublicReadReceiptSchema || receipt.Sequence == 0 || receipt.CapturedAt == "" || receipt.ReceiptDigest == "" {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, receipt.CapturedAt); err != nil {
		return false
	}
	unsigned := receipt
	unsigned.ReceiptDigest = ""
	raw, err := json.Marshal(unsigned)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(append([]byte(PublicReadReceiptSchema+"\x00"), raw...))
	return receipt.ReceiptDigest == hex.EncodeToString(sum[:])
}

func isRedirectStatus(status int) bool {
	switch status {
	case 301, 302, 303, 307, 308:
		return true
	default:
		return false
	}
}

// PerformPublicRead is the caller-side bounded HTTPS implementation used by
// the owner broker. It is intentionally limited to one credential-free,
// redirect-free public GET and reuses publicDial's DNS and IP checks.
func PerformPublicRead(ctx context.Context, operation Operation, rawURL string) (OperationResponse, error) {
	if ctx == nil || operation.Validate() != nil || operation.Destination.Kind != DestinationPublicWeb || operation.Method != "GET" ||
		len(operation.Credentials) != 0 || operation.MaxRedirects != 0 || operation.RedirectPolicy != RedirectNone {
		return OperationResponse{}, errors.New("public-read broker operation is outside sealed GET scope")
	}
	transport := &http.Transport{Proxy: nil, DialContext: publicDial, DisableCompression: true, DisableKeepAlives: true,
		MaxResponseHeaderBytes: int64(^uint64(0) >> 1), TLSHandshakeTimeout: time.Duration(operation.TimeoutMillis) * time.Millisecond}
	defer transport.CloseIdleConnections()
	return performPublicReadWithTransport(ctx, operation, rawURL, transport)
}

func performPublicReadWithTransport(ctx context.Context, operation Operation, rawURL string, roundTripper http.RoundTripper) (OperationResponse, error) {
	if ctx == nil || operation.Validate() != nil || operation.Destination.Kind != DestinationPublicWeb || operation.Method != "GET" ||
		len(operation.Credentials) != 0 || operation.MaxRedirects != 0 || operation.RedirectPolicy != RedirectNone {
		return OperationResponse{}, errors.New("public-read broker operation is outside sealed GET scope")
	}
	urlValue, err := ValidateURL(rawURL)
	if err != nil || urlValue.String() != rawURL || roundTripper == nil {
		return OperationResponse{}, errors.New("public-read broker URL or transport is invalid")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(operation.TimeoutMillis)*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return OperationResponse{}, errors.New("public-read broker request is invalid")
	}
	setDeclaredHeaders(request, operation.Headers)
	client := &http.Client{Transport: roundTripper, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return OperationResponse{}, errors.New("public-read broker transport failed")
	}
	defer response.Body.Close()
	if isRedirectStatus(response.StatusCode) {
		return OperationResponse{}, errors.New("public-read broker redirect refused")
	}
	body, err := readHTTPResponse(response.Body, operation.MaxResponseBytes)
	if err != nil || (operation.MaxResponseBytes > 0 && len(body) > operation.MaxResponseBytes) {
		return OperationResponse{}, errors.New("public-read broker response exceeded its bound")
	}
	contentType, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if parseErr != nil || len(contentType) > 128 {
		contentType = ""
	}
	contentSignal := response.Header.Get("Content-Signal")
	if len(contentSignal) > 512 || strings.ContainsAny(contentSignal, "\r\n\x00") {
		contentSignal = "!invalid"
	}
	diagnostic := responseDiagnostic(nil, body, nil, response.StatusCode, operation.MaxResponseBytes, "")
	return OperationResponse{StatusCode: response.StatusCode, URL: rawURL, Body: body, ContentType: contentType,
		ContentSignal: contentSignal, Diagnostic: diagnostic}, nil
}
