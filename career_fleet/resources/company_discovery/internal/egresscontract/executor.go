package egresscontract

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Client is the narrow capability passed to trusted Node code. It contains no
// token, resolver, arbitrary headers, method override or operation selector.
type Client interface {
	Do(context.Context, OperationRequest) (OperationResponse, error)
}

// Accounting exposes only the current invocation's read-only receipt snapshot.
// It grants no cancellation, credential, or broader operation authority.
type Accounting struct {
	Requests       int    `json:"requests"`
	Responses      int    `json:"responses"`
	DeniedRequests int    `json:"denied_requests"`
	Transmission   string `json:"transmission"`
	Outcome        string `json:"outcome"`
	ReceiptDigest  string `json:"receipt_digest"`
}
type AccountingClient interface{ Accounting() Accounting }

func ReceiptAccounting(r Receipt) Accounting {
	raw, _ := json.Marshal(r)
	sum := sha256.Sum256(append([]byte("search.egress_receipt.v1\x00"), raw...))
	return Accounting{Requests: r.Requests, Responses: r.Responses, DeniedRequests: r.DeniedRequests, Transmission: r.Transmission, Outcome: r.Outcome, ReceiptDigest: hex.EncodeToString(sum[:])}
}
func (h *nativeHandle) Accounting() Accounting { return ReceiptAccounting(h.Receipt()) }

// EventStreamClient is an optional capability for adapters that consume a
// provider SSE response. Each callback receives one complete data frame; the
// response remains bounded by the selected operation's timeout and byte cap.
type EventStreamClient interface {
	Stream(context.Context, OperationRequest, EventStreamHandler) (OperationResponse, error)
}

// NativeHTTPS enforces operation boundaries for trusted in-memory adapters.
// It is NOT a sandbox against hostile Go code in the same address space.
// No edge fallback or broker-only enforcement is implied by this adapter.
type NativeHTTPS struct{}

func (NativeHTTPS) Name() string            { return "native.trusted_https.v1" }
func (NativeHTTPS) Supports(p Policy) error { return p.Validate() }
func (a NativeHTTPS) Start(ctx context.Context, inv Invocation) (InvocationHandle, error) {
	return startNativeHTTPS(ctx, inv, a.Name(), nil)
}

func startNativeHTTPS(ctx context.Context, inv Invocation, adapterName string, roundTripper http.RoundTripper) (InvocationHandle, error) {
	if inv.Policy.Mode != ModeHTTPS || len(inv.Binding.Routes) > 0 {
		return nil, fmt.Errorf("HTTPS requires a concrete operation, not inference routing")
	}
	if err := inv.Policy.Validate(); err != nil {
		return nil, err
	}
	binding, err := ValidateBinding(inv.Policy, inv.Binding)
	if err != nil {
		return nil, err
	}
	digest, _ := inv.Policy.Digest()
	id := inv.Identity
	if id.RunID == "" || id.ProcessDigest == "" || id.NodeKey == "" || id.NodeName == "" || id.NodeDigest == "" || id.NodeSpecDigest == "" || id.PolicyDigest != digest || id.Operation != binding.Operation {
		return nil, fmt.Errorf("incomplete or mismatched invocation identity")
	}
	var op Operation
	for _, candidate := range Normalize(inv.Policy).Operations {
		if candidate.ID == binding.Operation {
			op = candidate
		}
	}
	if binding.CredentialRef != "" && inv.Credentials == nil {
		return nil, fmt.Errorf("credential provider absent")
	}
	ctx, cancel := context.WithCancel(ctx)
	h := &nativeHandle{ctx: ctx, cancel: cancel, op: op, binding: binding, credentials: inv.Credentials, receipt: Receipt{Identity: id, PolicyDigest: digest, Operation: op.ID, Adapter: adapterName, Outcome: "unused", Transmission: "not_sent"}}
	if roundTripper != nil {
		h.roundTripper = roundTripper
	} else {
		h.transport = &http.Transport{Proxy: nil, DialContext: publicDial, DisableCompression: true, DisableKeepAlives: true, MaxResponseHeaderBytes: int64(^uint64(0) >> 1), TLSHandshakeTimeout: time.Duration(op.TimeoutMillis) * time.Millisecond}
	}
	return h, nil
}

type nativeHandle struct {
	mu            sync.Mutex
	ctx           context.Context
	cancel        context.CancelFunc
	op            Operation
	binding       Binding
	credentials   CredentialProvider
	transport     *http.Transport
	roundTripper  http.RoundTripper
	receipt       Receipt
	closed        bool
	timeoutCancel context.CancelFunc
}

func (h *nativeHandle) Cancel() error {
	h.cancel() // Interrupt active I/O before waiting for its lock.
	h.mu.Lock()
	defer h.mu.Unlock()
	wasClosed := h.closed
	h.closed = true
	h.credentials = nil
	if !wasClosed {
		if h.roundTripper != nil {
			closeRoundTripper(h.roundTripper)
		} else if h.transport != nil {
			h.transport.CloseIdleConnections()
		}
	}
	if h.timeoutCancel != nil {
		h.timeoutCancel()
	}
	return nil
}
func (h *nativeHandle) Receipt() Receipt {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.receipt
	r.FailureDiagnostic = cloneFailureDiagnostic(r.FailureDiagnostic)
	r.PublicReadReceipts = append([]PublicReadReceipt(nil), r.PublicReadReceipts...)
	r.ProviderHTTPSReceipts = append([]ProviderHTTPSReceipt(nil), r.ProviderHTTPSReceipts...)
	return r
}

func closeRoundTripper(transport http.RoundTripper) {
	if closeable, ok := transport.(interface{ Close() error }); ok {
		_ = closeable.Close()
		return
	}
	if idle, ok := transport.(interface{ CloseIdleConnections() }); ok {
		idle.CloseIdleConnections()
	}
}
func (h *nativeHandle) Invoke(ctx context.Context, raw []byte) (Result, error) {
	req, err := decodeOperationRequest(raw)
	if err != nil {
		return Result{}, err
	}
	response, err := h.Do(ctx, req)
	encoded, _ := json.Marshal(response)
	return Result{Response: encoded, Receipt: h.Receipt()}, err
}

func (h *nativeHandle) InvokeStream(ctx context.Context, raw []byte, onEvent EventStreamHandler) (Result, error) {
	req, err := decodeOperationRequest(raw)
	if err != nil {
		return Result{}, err
	}
	response, err := h.Stream(ctx, req, onEvent)
	encoded, _ := json.Marshal(response)
	return Result{Response: encoded, Receipt: h.Receipt()}, err
}

func decodeOperationRequest(raw []byte) (OperationRequest, error) {
	var req OperationRequest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil {
		return OperationRequest{}, fmt.Errorf("invalid request envelope")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return OperationRequest{}, fmt.Errorf("trailing request envelope")
	}
	return req, nil
}

func (h *nativeHandle) Do(caller context.Context, input OperationRequest) (OperationResponse, error) {
	return h.do(caller, input, nil)
}

func (h *nativeHandle) Stream(caller context.Context, input OperationRequest, onEvent EventStreamHandler) (OperationResponse, error) {
	if onEvent == nil {
		return OperationResponse{}, fmt.Errorf("event stream handler is required")
	}
	return h.do(caller, input, onEvent)
}

func (h *nativeHandle) do(caller context.Context, input OperationRequest, onEvent EventStreamHandler) (OperationResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fail := func(message string) (OperationResponse, error) {
		h.receipt.Outcome = "refused"
		return OperationResponse{}, fmt.Errorf("%s", message)
	}
	if input.Operation != "" && input.Operation != h.op.ID {
		return fail("request selects a different operation")
	}
	if h.closed || h.ctx.Err() != nil || caller.Err() != nil {
		return fail("invocation closed or expired")
	}
	if h.timeoutCancel == nil && h.op.TimeoutMillis > 0 {
		h.ctx, h.timeoutCancel = context.WithTimeout(h.ctx, time.Duration(h.op.TimeoutMillis)*time.Millisecond)
	}
	if (h.op.MaxRequestBytes > 0 && len(input.Body) > h.op.MaxRequestBytes) || ((h.op.Method == "GET" || h.op.Method == "HEAD") && len(input.Body) > 0) {
		return fail("request body exceeds operation scope")
	}
	u, err := h.destination(input)
	if err != nil {
		return fail("destination outside operation scope")
	}
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	stop := context.AfterFunc(caller, cancel)
	defer stop()
	// One operation handle has a single cumulative wire-request budget, including
	// redirects and failed sends. No retry or proxy can expand it.
	if h.receipt.Requests >= h.op.MaxRequests {
		return fail("request budget exhausted")
	}
	var secret []byte
	if h.binding.CredentialRef != "" {
		secret, err = h.credentials.Resolve(ctx, h.receipt.Identity, h.binding.CredentialRef)
		defer func() { clear(secret) }()
		if err != nil || len(secret) == 0 || len(secret) > 16384 || bytes.ContainsAny(secret, "\r\n") {
			return fail("credential unavailable or invalid")
		}
	}
	initial := OriginOf(u)
	for redirects := 0; ; redirects++ {
		if ctx.Err() != nil {
			return fail("invocation cancelled or expired")
		}
		if h.receipt.Requests >= h.op.MaxRequests {
			return fail("request budget exhausted")
		}
		req, err := http.NewRequestWithContext(ctx, h.op.Method, u.String(), bytes.NewReader(input.Body))
		if err != nil {
			return fail("invalid operation request")
		}
		if len(secret) > 0 {
			header, value := credentialHeader(h.op, h.binding.CredentialRef, secret)
			req.Header.Set(header, value)
		}
		if len(input.Body) > 0 {
			req.Header.Set("Content-Type", "application/json")
		}
		setDeclaredHeaders(req, h.op.Headers)
		h.receipt.Requests++
		h.receipt.Transmission = "attempted_unknown"
		var response *http.Response
		if h.roundTripper != nil {
			response, err = h.roundTripper.RoundTrip(req)
		} else {
			response, err = h.transport.RoundTrip(req)
		}
		if receiptSource, ok := h.roundTripper.(interface{ TakePublicReadReceipt() *PublicReadReceipt }); ok {
			if receipt := receiptSource.TakePublicReadReceipt(); receipt != nil {
				h.receipt.PublicReadReceipts = append(h.receipt.PublicReadReceipts, *receipt)
			}
		}
		if source, ok := h.roundTripper.(interface{ TakeProviderHTTPSReceipt() *ProviderHTTPSReceipt }); ok {
			if receipt := source.TakeProviderHTTPSReceipt(); receipt != nil {
				h.receipt.ProviderHTTPSReceipts = append(h.receipt.ProviderHTTPSReceipts, *receipt)
			}
		}
		if err != nil {
			d := responseDiagnostic(input.Body, nil, secret, 0, h.op.MaxResponseBytes, "")
			d.Code = diagnosticReadCode(err)
			if d.Code == "response_read_failed" {
				d.Code = "http_transport_failed"
			}
			d.Stage = "transport_send"
			d.Detail, _ = diagnosticText([]byte(err.Error()), secret, 1024)
			h.receipt.FailureDiagnostic = d
			h.receipt.Outcome = "refused"
			return OperationResponse{Diagnostic: d}, fmt.Errorf("%s", d.Code)
		} // Never leak URL, token, or provider error text.
		h.receipt.Responses++
		if h.receipt.Responses == h.receipt.Requests {
			h.receipt.Transmission = "response_received"
		}
		if response.StatusCode == 301 || response.StatusCode == 302 || response.StatusCode == 303 || response.StatusCode == 307 || response.StatusCode == 308 {
			response.Body.Close()
			if h.op.RedirectPolicy != RedirectSameOrigin || redirects >= h.op.MaxRedirects || h.op.Method != "GET" && h.op.Method != "HEAD" {
				return fail("redirect refused")
			}
			next, err := u.Parse(response.Header.Get("Location"))
			if err != nil || response.Header.Get("Location") == "" {
				return fail("invalid redirect")
			}
			next, err = ValidateURL(next.String())
			if err != nil || OriginOf(next) != initial {
				return fail("cross-origin redirect refused")
			}
			// Named services may not redirect outside their exact authorized path.
			if h.op.Destination.Kind == DestinationService && (next.EscapedPath() != u.EscapedPath() || next.RawQuery != u.RawQuery) {
				return fail("service redirect widens path")
			}
			u = next
			continue
		}
		if onEvent != nil {
			if response.StatusCode < 200 || response.StatusCode > 299 {
				response.Body.Close()
				h.receipt.Outcome = "completed"
				return OperationResponse{StatusCode: response.StatusCode, URL: u.String()}, nil
			}
			mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
			if mediaErr != nil || !strings.EqualFold(mediaType, "text/event-stream") {
				response.Body.Close()
				h.receipt.Outcome = "failed"
				return OperationResponse{StatusCode: response.StatusCode, URL: u.String()}, fmt.Errorf("provider response is not an event stream")
			}
			streamErr := consumeSSE(response.Body, h.op.MaxResponseBytes, onEvent)
			response.Body.Close()
			if streamErr != nil {
				h.receipt.Outcome = "failed"
				return OperationResponse{StatusCode: response.StatusCode, URL: u.String()}, fmt.Errorf("event stream read or handler failed")
			}
			h.receipt.Outcome = "completed"
			return OperationResponse{StatusCode: response.StatusCode, URL: u.String()}, nil
		}
		body, readErr := readHTTPResponse(response.Body, h.op.MaxResponseBytes)
		response.Body.Close()
		requestID := response.Header.Get("X-Request-Id")
		if requestID == "" {
			requestID = response.Header.Get("Request-Id")
		}
		diagnostic := responseDiagnostic(input.Body, body, secret, response.StatusCode, h.op.MaxResponseBytes, requestID)
		if readErr != nil {
			diagnostic.Code = diagnosticReadCode(readErr)
			diagnostic.Detail, _ = diagnosticText([]byte(readErr.Error()), secret, 1024)
			h.receipt.FailureDiagnostic = diagnostic
			h.receipt.Outcome = "refused"
			return OperationResponse{StatusCode: response.StatusCode, URL: u.String(), Diagnostic: diagnostic}, fmt.Errorf("%s", diagnostic.Code)
		}
		if h.op.MaxResponseBytes > 0 && len(body) > h.op.MaxResponseBytes {
			diagnostic.Code = "response_byte_limit_exceeded"
			h.receipt.FailureDiagnostic = diagnostic
			h.receipt.Outcome = "refused"
			return OperationResponse{StatusCode: response.StatusCode, URL: u.String(), Diagnostic: diagnostic}, fmt.Errorf("%s", diagnostic.Code)
		}
		contentType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if mediaErr != nil || len(contentType) > 128 {
			contentType = ""
		}
		contentSignal := response.Header.Get("Content-Signal")
		if len(contentSignal) > 512 || strings.ContainsAny(contentSignal, "\r\n\x00") {
			contentSignal = "!invalid"
		}
		h.receipt.Outcome = "completed"
		return OperationResponse{Headers: response.Header.Clone(), StatusCode: response.StatusCode, URL: u.String(), Body: body, ContentType: contentType, ContentSignal: contentSignal, Diagnostic: diagnostic}, nil
	}
}

// HTTP quotas are explicit policy. An absent quota must not inherit a framing
// ceiling from the factory. Cancellation still follows the caller and handle.
func readHTTPResponse(body io.Reader, limit int) ([]byte, error) {
	if limit > 0 && int64(limit) < int64(^uint64(0)>>1) {
		body = io.LimitReader(body, int64(limit)+1)
	}
	return io.ReadAll(body)
}

func consumeSSE(body io.Reader, maxBytes int, onEvent EventStreamHandler) error {
	if maxBytes < 0 || onEvent == nil {
		return fmt.Errorf("invalid event stream bound")
	}
	if maxBytes > 0 && int64(maxBytes) < int64(^uint64(0)>>1) {
		body = io.LimitReader(body, int64(maxBytes)+1)
	}
	reader := bufio.NewReaderSize(body, 4096)
	var wireBytes int64
	data := make([]byte, 0, 1024)
	line := make([]byte, 0, 1024)
	hasData := false
	onLine := func() (bool, error) {
		if len(line) == 0 {
			if !hasData {
				return false, nil
			}
			stop, err := onEvent(data)
			clear(data)
			data = data[:0]
			hasData = false
			if err != nil {
				return false, fmt.Errorf("event stream handler failed")
			}
			return stop, nil
		}
		if line[0] == ':' {
			return false, nil
		}
		field, value, found := bytes.Cut(line, []byte(":"))
		if !found {
			value = nil
		} else if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
		if !bytes.Equal(field, []byte("data")) {
			return false, nil
		}
		if hasData {
			data = append(data, '\n')
		}
		data = append(data, value...)
		if maxBytes > 0 && len(data) > maxBytes {
			return false, fmt.Errorf("event data exceeds operation response limit")
		}
		hasData = true
		return false, nil
	}
	for {
		b, err := reader.ReadByte()
		if err == io.EOF {
			// SSE dispatches a data event only after its blank-line delimiter. A
			// dangling frame at EOF is incomplete and intentionally not delivered.
			return nil
		}
		if err != nil {
			return fmt.Errorf("event stream read failed")
		}
		wireBytes++
		if maxBytes > 0 && wireBytes > int64(maxBytes) {
			return fmt.Errorf("event stream exceeds operation response limit")
		}
		if b == '\r' {
			if next, peekErr := reader.Peek(1); peekErr == nil && next[0] == '\n' {
				_, _ = reader.ReadByte()
				wireBytes++
				if maxBytes > 0 && wireBytes > int64(maxBytes) {
					return fmt.Errorf("event stream exceeds operation response limit")
				}
			}
			stop, lineErr := onLine()
			clear(line)
			line = line[:0]
			if lineErr != nil {
				return lineErr
			}
			if stop {
				return nil
			}
			continue
		}
		if b == '\n' {
			stop, lineErr := onLine()
			clear(line)
			line = line[:0]
			if lineErr != nil {
				return lineErr
			}
			if stop {
				return nil
			}
			continue
		}
		line = append(line, b)
		if maxBytes > 0 && len(line) > maxBytes {
			return fmt.Errorf("event stream line exceeds operation response limit")
		}
	}
}

func credentialHeader(op Operation, ref string, secret []byte) (string, string) {
	for _, credential := range op.Credentials {
		if credential.Ref != ref {
			continue
		}
		if credential.Header != "" {
			return credential.Header, string(secret)
		}
		return "Authorization", "Bearer " + string(secret)
	}
	return "Authorization", "Bearer " + string(secret)
}

// setDeclaredHeaders applies fixed policy-declared headers to an outgoing
// request. Declared names are already lowercased by Normalize; validation
// forbids every header the executor owns, so declared headers cannot widen
// transport authority.
func setDeclaredHeaders(req *http.Request, headers map[string]string) {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		req.Header.Set(name, headers[name])
	}
}

func (h *nativeHandle) destination(input OperationRequest) (*url.URL, error) {
	if h.op.Destination.Kind == DestinationPublicWeb {
		if len(input.PathParameters) > 0 {
			return nil, fmt.Errorf("unexpected path parameters")
		}
		u, err := ValidateURL(input.URL)
		if err != nil {
			return nil, err
		}
		if len(h.binding.Origins) > 0 && !contains(h.binding.Origins, OriginOf(u)) {
			return nil, fmt.Errorf("origin refused")
		}
		return applyDeclaredQuery(u, h.op, input)
	}
	if input.URL != "" {
		return nil, fmt.Errorf("service URL cannot be overridden")
	}
	path := h.op.Destination.Path
	used := map[string]bool{}
	for _, segment := range strings.Split(path, "/") {
		if !strings.HasPrefix(segment, "{") {
			continue
		}
		rest := strings.TrimPrefix(segment, "{")
		end := strings.Index(rest, "}")
		if end <= 0 {
			return nil, fmt.Errorf("invalid path parameter")
		}
		key := rest[:end]
		if suffix := rest[end+1:]; suffix != "" {
			// Provider action suffixes (for example `{range}:clear`):
			// only the parameter is substituted; the fixed verb stays.
			if !strings.HasPrefix(suffix, ":") || !validPathLiteral(suffix[1:]) || !validToken(key) {
				return nil, fmt.Errorf("invalid path parameter")
			}
		}
		value, ok := input.PathParameters[key]
		if !ok || value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/%\\?#\x00\r\n") {
			return nil, fmt.Errorf("invalid path parameter")
		}
		used[key] = true
		path = strings.ReplaceAll(path, "{"+key+"}", url.PathEscape(value))
	}
	if strings.Contains(path, "{") {
		return nil, fmt.Errorf("invalid path parameter")
	}
	if len(used) != len(input.PathParameters) {
		return nil, fmt.Errorf("undeclared path parameters")
	}
	u, err := ValidateURL(h.binding.Origins[0] + path)
	if err != nil {
		return nil, err
	}
	return applyDeclaredQuery(u, h.op, input)
}

func applyDeclaredQuery(base *url.URL, operation Operation, input OperationRequest) (*url.URL, error) {
	if base == nil {
		return nil, fmt.Errorf("query destination is absent")
	}
	if operation.Destination.Kind != DestinationService && (len(input.QueryParameters) > 0 || len(input.RepeatedQueryParameters) > 0) {
		return nil, fmt.Errorf("query parameters require a service destination")
	}
	u := *base
	if len(input.QueryParameters) == 0 && len(input.RepeatedQueryParameters) == 0 {
		return &u, nil
	}
	allowed := make(map[string]bool, len(operation.QueryParameters))
	for _, name := range operation.QueryParameters {
		allowed[name] = true
	}
	values := url.Values{}
	used := make(map[string]bool, len(input.QueryParameters)+len(input.RepeatedQueryParameters))
	for key, value := range input.QueryParameters {
		if !allowed[key] {
			return nil, fmt.Errorf("undeclared query parameter")
		}
		used[key] = true
		values.Set(key, value)
	}
	for key, repeated := range input.RepeatedQueryParameters {
		if !allowed[key] {
			return nil, fmt.Errorf("undeclared query parameter")
		}
		if used[key] {
			return nil, fmt.Errorf("query parameter cannot be both scalar and repeated")
		}
		if len(repeated) == 0 {
			return nil, fmt.Errorf("repeated query values must not be empty")
		}
		for _, value := range repeated {
			values.Add(key, value)
		}
	}
	encoded := values.Encode()
	u.RawQuery = encoded
	return &u, nil
}

// Special-use ranges are rejected, including mapped IPv4 and transition ranges.
// DNS is resolved once per connection, every answer is checked, then the chosen
// numeric address is dialed without a second lookup. TLS still verifies hostname.
var deniedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "::/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20", "fc00::/7", "fe80::/10", "ff00::/8"} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

func publicAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.Zone() != "" {
		return false
	}
	for _, p := range deniedPrefixes {
		if p.Contains(addr) {
			return false
		}
	}
	return addr.Is4() || netip.MustParsePrefix("2000::/3").Contains(addr)
}

func publicDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" {
		return nil, fmt.Errorf("destination refused")
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, fmt.Errorf("DNS failed")
	}
	for _, addr := range addresses {
		if !publicAddress(addr) {
			return nil, fmt.Errorf("non-public destination refused")
		}
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", net.JoinHostPort(addresses[0].String(), "443"))
}
