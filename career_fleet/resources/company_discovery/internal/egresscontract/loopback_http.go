package egresscontract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// NativeLoopbackHTTP is a separate adapter for explicitly configured local
// services. It never resolves DNS, uses no proxy, accepts no credentials, and
// dials only the exact 127.0.0.1 port sealed into its policy.
type NativeLoopbackHTTP struct{}

func (NativeLoopbackHTTP) Name() string { return "native.loopback_http.v1" }

func (NativeLoopbackHTTP) Supports(p Policy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if p.Mode != ModeLoopbackHTTP {
		return fmt.Errorf("loopback adapter requires loopback HTTP policy")
	}
	return nil
}

func (a NativeLoopbackHTTP) Start(ctx context.Context, inv Invocation) (InvocationHandle, error) {
	if len(inv.Binding.Routes) > 0 {
		return nil, fmt.Errorf("loopback HTTP requires a concrete operation")
	}
	if err := a.Supports(inv.Policy); err != nil {
		return nil, err
	}
	binding, err := ValidateBinding(inv.Policy, inv.Binding)
	if err != nil {
		return nil, err
	}
	if binding.CredentialRef != "" {
		return nil, fmt.Errorf("loopback HTTP cannot resolve credentials")
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
			break
		}
	}
	if op.ID == "" || len(op.Credentials) != 0 || len(binding.Origins) != 1 {
		return nil, fmt.Errorf("invalid loopback operation")
	}
	destination, err := url.Parse(binding.Origins[0] + op.Destination.Path)
	if err != nil || destination.Hostname() != "127.0.0.1" || destination.Port() == "" {
		return nil, fmt.Errorf("invalid loopback destination")
	}
	ctx, cancel := context.WithCancel(ctx)
	h := &loopbackHTTPHandle{
		ctx:     ctx,
		cancel:  cancel,
		op:      op,
		url:     destination,
		port:    destination.Port(),
		receipt: Receipt{Identity: id, PolicyDigest: digest, Operation: op.ID, Adapter: a.Name(), Outcome: "unused", Transmission: "not_sent"},
	}
	h.transport = &http.Transport{
		Proxy:                  nil,
		DialContext:            h.dial,
		DisableCompression:     true,
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: int64(^uint64(0) >> 1),
	}
	return h, nil
}

type loopbackHTTPHandle struct {
	mu            sync.Mutex
	ctx           context.Context
	cancel        context.CancelFunc
	op            Operation
	url           *url.URL
	port          string
	transport     *http.Transport
	receipt       Receipt
	closed        bool
	timeoutCancel context.CancelFunc
}

func (h *loopbackHTTPHandle) Cancel() error {
	h.cancel()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	h.transport.CloseIdleConnections()
	if h.timeoutCancel != nil {
		h.timeoutCancel()
	}
	return nil
}

// Accounting is a read-only snapshot of this invocation, matching the HTTPS
// and browser clients without exposing cancellation or destination authority.
func (h *loopbackHTTPHandle) Accounting() Accounting { return ReceiptAccounting(h.Receipt()) }

func (h *loopbackHTTPHandle) Receipt() Receipt {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.receipt
	r.FailureDiagnostic = cloneFailureDiagnostic(r.FailureDiagnostic)
	return r
}

func (h *loopbackHTTPHandle) Invoke(ctx context.Context, raw []byte) (Result, error) {
	var req OperationRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return Result{}, fmt.Errorf("invalid request envelope")
	}
	if dec.Decode(new(any)) != io.EOF {
		return Result{}, fmt.Errorf("trailing request envelope")
	}
	response, err := h.Do(ctx, req)
	encoded, _ := json.Marshal(response)
	return Result{Response: encoded, Receipt: h.Receipt()}, err
}

func (h *loopbackHTTPHandle) Do(caller context.Context, input OperationRequest) (OperationResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fail := func(message string) (OperationResponse, error) {
		h.receipt.Outcome = "refused"
		return OperationResponse{}, fmt.Errorf("%s", message)
	}
	if h.closed || h.ctx.Err() != nil || caller.Err() != nil {
		return fail("invocation closed or expired")
	}
	if input.URL != "" || len(input.PathParameters) != 0 {
		return fail("loopback operation cannot override destination")
	}
	requestURL, err := applyDeclaredQuery(h.url, h.op, input)
	if err != nil {
		return fail("loopback query exceeds operation scope")
	}
	if (h.op.MaxRequestBytes > 0 && len(input.Body) > h.op.MaxRequestBytes) || ((h.op.Method == "GET" || h.op.Method == "HEAD") && len(input.Body) > 0) {
		return fail("request body exceeds operation scope")
	}
	if h.timeoutCancel == nil && h.op.TimeoutMillis > 0 {
		h.ctx, h.timeoutCancel = context.WithTimeout(h.ctx, time.Duration(h.op.TimeoutMillis)*time.Millisecond)
	}
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	stop := context.AfterFunc(caller, cancel)
	defer stop()
	if h.receipt.Requests >= h.op.MaxRequests {
		return fail("request budget exhausted")
	}
	req, err := http.NewRequestWithContext(ctx, h.op.Method, requestURL.String(), bytes.NewReader(input.Body))
	if err != nil {
		return fail("invalid operation request")
	}
	if len(input.Body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	setDeclaredHeaders(req, h.op.Headers)
	h.receipt.Requests++
	h.receipt.Transmission = "attempted_unknown"
	response, err := h.transport.RoundTrip(req)
	if err != nil {
		return fail("loopback HTTP transport failed")
	}
	h.receipt.Responses++
	if h.receipt.Responses == h.receipt.Requests {
		h.receipt.Transmission = "response_received"
	}
	body, readErr := readHTTPResponse(response.Body, h.op.MaxResponseBytes)
	response.Body.Close()
	d := responseDiagnostic(input.Body, body, nil, response.StatusCode, h.op.MaxResponseBytes, response.Header.Get("X-Request-ID"))
	if readErr != nil || (h.op.MaxResponseBytes > 0 && len(body) > h.op.MaxResponseBytes) {
		if readErr != nil {
			d.Code = diagnosticReadCode(readErr)
			d.Detail, _ = diagnosticText([]byte(readErr.Error()), nil, 1024)
		} else {
			d.Code = "response_byte_limit_exceeded"
		}
		h.receipt.FailureDiagnostic = d
		h.receipt.Outcome = "refused"
		return OperationResponse{StatusCode: response.StatusCode, URL: requestURL.String(), Diagnostic: d}, fmt.Errorf("%s", d.Code)
	}
	if response.StatusCode >= 300 && response.StatusCode <= 399 {
		return fail("redirect refused")
	}
	h.receipt.Outcome = "completed"
	return OperationResponse{Headers: response.Header.Clone(), StatusCode: response.StatusCode, URL: requestURL.String(), Body: body, ContentType: response.Header.Get("Content-Type"), Diagnostic: d}, nil
}

func (h *loopbackHTTPHandle) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" || port != h.port || (network != "tcp" && network != "tcp4") {
		return nil, fmt.Errorf("loopback destination refused")
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", h.port))
}

var _ TargetAdapter = NativeLoopbackHTTP{}
var _ InvocationHandle = (*loopbackHTTPHandle)(nil)
