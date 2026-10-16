package egresscontract

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

//go:embed browser_driver.mjs
var browserDriver []byte

// NativeBrowser owns one primitive browser runtime within the current Process.
// It does not choose sources, selectors, page programs, query values or retries.
// Chromium and its driver have no direct network permission. Real page requests
// are fulfilled from the scoped Go broker below, with public-address dialing.
// Like NativeHTTPS, this adapter trusts admitted Go Node code in its address space.
type NativeBrowser struct {
	mu      sync.Mutex
	manager *browserManager
}

func NewNativeBrowser() *NativeBrowser { return &NativeBrowser{} }
func (*NativeBrowser) Name() string    { return "native.scoped_browser.v1" }
func (*NativeBrowser) Supports(p Policy) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("native browser requires the qualified macOS network-denial adapter")
	}
	if p.Mode != ModeBrowser {
		return fmt.Errorf("browser policy required")
	}
	return p.Validate()
}
func (a *NativeBrowser) Start(ctx context.Context, inv Invocation) (InvocationHandle, error) {
	if ctx == nil {
		return nil, fmt.Errorf("browser context absent")
	}
	if err := a.Supports(inv.Policy); err != nil {
		return nil, err
	}
	binding, err := ValidateBinding(inv.Policy, inv.Binding)
	if err != nil {
		return nil, err
	}
	digest, _ := inv.Policy.Digest()
	id := inv.Identity
	if id.RunID == "" || id.ProcessDigest == "" || id.NodeKey == "" || id.NodeName == "" || id.NodeDigest == "" || id.NodeSpecDigest == "" || id.PolicyDigest != digest || id.Operation != binding.Operation {
		return nil, fmt.Errorf("browser identity mismatch")
	}
	op := inv.Policy.Operations[0]
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.manager == nil {
		if err := verifyBrowserRuntime(binding, op.Browser.Runtime); err != nil {
			return nil, err
		}
		child, cancel := context.WithCancel(context.Background())
		a.manager = &browserManager{binding: binding, pin: op.Browser.Runtime, ctx: child, cancel: cancel, pending: map[int64]*browserCall{}, ready: make(chan error, 1), done: make(chan struct{})}
	} else if !reflect.DeepEqual(a.manager.binding.Browser, binding.Browser) || a.manager.binding.Directory != binding.Directory || a.manager.pin != op.Browser.Runtime {
		return nil, fmt.Errorf("one Process browser runtime must use exact consistent bindings")
	}
	a.manager.mu.Lock()
	defer a.manager.mu.Unlock()
	if a.manager.closed {
		return nil, fmt.Errorf("browser runtime already released")
	}
	a.manager.refs++
	child, cancel := context.WithCancel(ctx)
	return &browserHandle{manager: a.manager, ctx: child, cancel: cancel, op: op, identity: id, receipt: Receipt{Identity: id, PolicyDigest: digest, Operation: op.ID, Adapter: a.Name(), Outcome: "unused", Transmission: "not_sent"}}, nil
}

func verifyBrowserRuntime(b Binding, p BrowserRuntimePin) error {
	for _, path := range []string{b.Directory, b.Browser.PlaywrightDirectory, b.Browser.BrowserDirectory} {
		info, e := os.Lstat(path)
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("browser owner/runtime directory unavailable")
		}
	}
	hash, e := browserFileDigest(b.Browser.NodeExecutable)
	if e != nil || hash != p.NodeSHA256 {
		return fmt.Errorf("browser Node executable pin mismatch")
	}
	hash, e = BrowserTreeDigest(b.Browser.PlaywrightDirectory)
	if e != nil || hash != p.DriverSHA256 {
		return fmt.Errorf("browser driver tree pin mismatch")
	}
	hash, e = BrowserTreeDigest(b.Browser.BrowserDirectory)
	if e != nil || hash != p.BundleSHA256 {
		return fmt.Errorf("browser bundle pin mismatch")
	}
	i, e := os.Lstat(b.Browser.BrowserExecutable)
	if e != nil || !i.Mode().IsRegular() || i.Mode()&0111 == 0 {
		return fmt.Errorf("browser executable unavailable")
	}
	raw, e := os.ReadFile(filepath.Join(b.Browser.PlaywrightDirectory, "package.json"))
	if e != nil {
		return fmt.Errorf("browser driver metadata unavailable")
	}
	var metadata struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(raw, &metadata) != nil || metadata.Version != p.PlaywrightVersion {
		return fmt.Errorf("Playwright version pin mismatch")
	}
	return nil
}

type browserManager struct {
	mu        sync.Mutex
	writeMu   sync.Mutex
	startMu   sync.Mutex
	binding   Binding
	pin       BrowserRuntimePin
	ctx       context.Context
	cancel    context.CancelFunc
	refs      int
	closed    bool
	started   bool
	sequence  int64
	cmd       *exec.Cmd
	input     io.WriteCloser
	directory string
	pending   map[int64]*browserCall
	ready     chan error
	done      chan struct{}
}
type browserCall struct {
	requests sync.WaitGroup
	handle   *browserHandle
	ctx      context.Context
	response chan browserFrame
}
type browserFrame struct {
	Type         string            `json:"type"`
	ID           int64             `json:"id"`
	CallID       int64             `json:"call_id"`
	RequestID    int64             `json:"request_id"`
	Error        string            `json:"error"`
	Result       json.RawMessage   `json:"result"`
	URL          string            `json:"url"`
	Method       string            `json:"method"`
	ResourceType string            `json:"resource_type"`
	Redirects    int               `json:"redirects"`
	Headers      map[string]string `json:"headers"`
}
type browserBody struct {
	ContextID    string          `json:"context_id"`
	IntentDigest string          `json:"intent_digest,omitempty"`
	Generation   int64           `json:"generation,omitempty"`
	URL          string          `json:"url,omitempty"`
	TextExact    string          `json:"text_exact,omitempty"`
	ExpectedURL  string          `json:"expected_url,omitempty"`
	Selector     string          `json:"selector,omitempty"`
	Values       []browserValue  `json:"values,omitempty"`
	Program      string          `json:"program,omitempty"`
	Arguments    json.RawMessage `json:"arguments,omitempty"`
}
type browserValue struct {
	Selector string `json:"selector"`
	Kind     string `json:"kind"`
	Value    string `json:"value"`
}
type browserHandle struct {
	mu       sync.Mutex
	manager  *browserManager
	ctx      context.Context
	cancel   context.CancelFunc
	op       Operation
	identity Identity
	receipt  Receipt
	closed   bool
}

func (h *browserHandle) Accounting() Accounting { return ReceiptAccounting(h.Receipt()) }
func (h *browserHandle) Receipt() Receipt       { h.mu.Lock(); defer h.mu.Unlock(); return h.receipt }
func (h *browserHandle) Cancel() error {
	h.cancel()
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	h.mu.Unlock()
	m := h.manager
	m.mu.Lock()
	m.refs--
	last := m.refs == 0
	m.mu.Unlock()
	if last {
		m.stop()
	}
	return nil
}
func (h *browserHandle) Invoke(ctx context.Context, raw []byte) (Result, error) {
	var req OperationRequest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF {
		return Result{}, fmt.Errorf("invalid browser envelope")
	}
	response, e := h.Do(ctx, req)
	encoded, _ := json.Marshal(response)
	return Result{Response: encoded, Receipt: h.Receipt()}, e
}
func (h *browserHandle) Do(ctx context.Context, req OperationRequest) (OperationResponse, error) {
	empty := OperationResponse{}
	if ctx == nil || ctx.Err() != nil || h.ctx.Err() != nil {
		return empty, fmt.Errorf("browser invocation cancelled")
	}
	if (req.Operation != "" && req.Operation != h.op.ID) || req.URL != "" || len(req.PathParameters) > 0 || len(req.QueryParameters) > 0 || len(req.RepeatedQueryParameters) > 0 || len(req.Body) == 0 || len(req.Body) > h.op.MaxRequestBytes || !utf8.Valid(req.Body) {
		return empty, fmt.Errorf("browser primitive refused before execution")
	}
	body, err := h.decodeBody(req.Body)
	if err != nil {
		return empty, err
	}
	h.mu.Lock()
	if h.closed || h.receipt.BrowserCalls != 0 {
		h.mu.Unlock()
		return empty, fmt.Errorf("browser primitive already consumed")
	}
	h.receipt.BrowserCalls++
	h.mu.Unlock()
	callCtx, cancel := context.WithTimeout(h.ctx, time.Duration(h.op.TimeoutMillis)*time.Millisecond)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	result, err := h.manager.call(callCtx, h, body)
	h.mu.Lock()
	defer h.mu.Unlock()
	if err != nil {
		h.receipt.Outcome = "execution_failed"
		return empty, err
	}
	if len(result) == 0 || len(result) > h.op.MaxResponseBytes {
		h.receipt.Outcome = "response_bound"
		return empty, fmt.Errorf("browser response bound")
	}
	var value map[string]any
	if json.Unmarshal(result, &value) != nil {
		return empty, fmt.Errorf("browser response invalid")
	}
	value["network_requests"] = h.receipt.Requests
	value["network_responses"] = h.receipt.Responses
	value["denied_requests"] = h.receipt.DeniedRequests
	output, err := json.Marshal(value)
	if err != nil || len(output) > h.op.MaxResponseBytes {
		return empty, fmt.Errorf("browser response bound")
	}
	h.receipt.Outcome = "completed"
	if h.receipt.Requests == 0 {
		h.receipt.Transmission = "not_sent"
	} else if h.receipt.Requests == h.receipt.Responses {
		h.receipt.Transmission = "response_received"
	}
	return OperationResponse{StatusCode: 0, URL: DestinationBrowser, ContentType: "application/json", Body: output}, nil
}
func (h *browserHandle) decodeBody(raw []byte) (browserBody, error) {
	var body browserBody
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&body) != nil || d.Decode(new(any)) != io.EOF || !browserHex64(body.ContextID) {
		return body, fmt.Errorf("invalid browser primitive body")
	}
	var fields map[string]json.RawMessage
	json.Unmarshal(raw, &fields)
	keys := []string{"context_id"}
	switch h.op.Method {
	case "OPEN":
		keys = append(keys, "intent_digest")
		if !browserHex64(body.IntentDigest) {
			return body, fmt.Errorf("invalid browser intent")
		}
	case "CLOSE":
	case "NAVIGATE":
		keys = append(keys, "generation", "url")
		if !h.permittedURL(body.URL) {
			return body, fmt.Errorf("browser navigation outside declared origins")
		}
	case "SUBMIT":
		keys = append(keys, "generation", "selector", "values", "expected_url")
		if !browserSelector(body.Selector) || !h.permittedURL(body.ExpectedURL) || len(body.Values) < 1 {
			return body, fmt.Errorf("invalid browser submission")
		}
		seen := map[string]bool{}
		for _, v := range body.Values {
			if !browserSelector(v.Selector) || seen[v.Selector] || !utf8.ValidString(v.Value) || strings.ContainsRune(v.Value, 0) || (v.Kind != "text" && v.Kind != "select") {
				return body, fmt.Errorf("invalid browser control")
			}
			seen[v.Selector] = true
		}
	case "ADVANCE":
		keys = append(keys, "generation", "selector", "text_exact", "expected_url")
		if !browserSelector(body.Selector) || !h.permittedURL(body.ExpectedURL) || !utf8.ValidString(body.TextExact) || strings.ContainsRune(body.TextExact, 0) {
			return body, fmt.Errorf("invalid browser continuation")
		}
	case "CAPTURE":
		keys = append(keys, "generation", "program", "arguments")
		sum := sha256.Sum256([]byte(body.Program))
		if hex.EncodeToString(sum[:]) != h.op.Browser.ProgramSHA256 || len(body.Arguments) == 0 || body.Arguments[0] != '{' || !json.Valid(body.Arguments) {
			return body, fmt.Errorf("browser capture program or arguments refused")
		}
	}
	allowed := map[string]bool{}
	for _, key := range keys {
		allowed[key] = true
	}
	for key := range fields {
		if !allowed[key] {
			return body, fmt.Errorf("unexpected browser primitive field")
		}
	}
	if body.Generation > 0 && uint64(body.Generation) > (1<<53)-1 {
		return body, fmt.Errorf("browser generation is not exactly representable by the v1 driver JSON number")
	}
	if len(fields) != len(keys) || body.Generation < 0 {
		return body, fmt.Errorf("incomplete browser primitive")
	}
	return body, nil
}
func browserSelector(s string) bool {
	return s != "" && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}
func (h *browserHandle) permittedURL(raw string) bool {
	u, e := ValidateURL(raw)
	return e == nil && containsOrigin(h.op.Destination.Origins, OriginOf(u))
}
func containsOrigin(list []string, s string) bool {
	want, err := CanonicalOrigin(s)
	if err != nil {
		return false
	}
	for _, v := range list {
		candidate, err := CanonicalOrigin(v)
		if err == nil && candidate == want {
			return true
		}
	}
	return false
}

func (m *browserManager) start(ctx context.Context) error {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return fmt.Errorf("browser runtime released")
	}
	if m.started {
		m.mu.Unlock()
		return nil
	}
	if err := verifyBrowserRuntime(m.binding, m.pin); err != nil {
		m.mu.Unlock()
		return err
	}
	directory, err := os.MkdirTemp(m.binding.Directory, "browser-")
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("browser owner directory unavailable")
	}
	if err = os.Chmod(directory, 0700); err != nil {
		os.RemoveAll(directory)
		m.mu.Unlock()
		return err
	}
	selected, err := snapshotBrowserRuntime(m.binding, m.pin, directory)
	if err != nil {
		os.RemoveAll(directory)
		m.mu.Unlock()
		return err
	}
	script := filepath.Join(directory, "driver.mjs")
	if err = os.WriteFile(script, browserDriver, 0600); err != nil {
		os.RemoveAll(directory)
		m.mu.Unlock()
		return err
	}
	command := exec.CommandContext(m.ctx, "/usr/bin/sandbox-exec", "-p", "(version 1)(allow default)(deny network*)", selected.Browser.NodeExecutable, script)
	command.Env = append(browserCleanEnvironment(), "TMPDIR="+directory)
	input, err := command.StdinPipe()
	if err != nil {
		os.RemoveAll(directory)
		m.mu.Unlock()
		return err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		input.Close()
		os.RemoveAll(directory)
		m.mu.Unlock()
		return err
	}
	command.Stderr = io.Discard
	if err = command.Start(); err != nil {
		input.Close()
		os.RemoveAll(directory)
		m.mu.Unlock()
		return fmt.Errorf("browser runtime start failed")
	}
	m.directory = directory
	m.cmd = command
	m.input = input
	m.started = true
	m.mu.Unlock()
	go m.read(output)
	go func() { command.Wait(); m.failAll(); close(m.done) }()
	err = m.send(map[string]any{"type": "initialize", "playwright_directory": selected.Browser.PlaywrightDirectory, "browser_executable": selected.Browser.BrowserExecutable, "playwright_version": m.pin.PlaywrightVersion, "browser_version": m.pin.BrowserVersion})
	if err != nil {
		m.stop()
		return err
	}
	select {
	case err := <-m.ready:
		if err != nil {
			m.stop()
		}
		return err
	case <-ctx.Done():
		m.stop()
		return fmt.Errorf("browser runtime initialization timed out")
	case <-m.done:
		return fmt.Errorf("browser runtime exited")
	}
}
func browserCleanEnvironment() []string {
	// No NODE_OPTIONS, package preload, proxy, credential or profile inheritance.
	list := []string{}
	for _, name := range []string{"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "TZ"} {
		if v, ok := os.LookupEnv(name); ok {
			list = append(list, name+"="+v)
		}
	}
	return list
}
func (m *browserManager) send(value any) error {
	raw, e := json.Marshal(value)
	if e != nil {
		return fmt.Errorf("browser frame bound")
	}
	raw = append(raw, '\n')
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	m.mu.Lock()
	input := m.input
	closed := m.closed
	m.mu.Unlock()
	if input == nil || closed {
		return fmt.Errorf("browser runtime lost")
	}
	_, e = input.Write(raw)
	return e
}
func (m *browserManager) call(ctx context.Context, h *browserHandle, body browserBody) (json.RawMessage, error) {
	if err := m.start(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.sequence++
	id := m.sequence
	call := &browserCall{handle: h, ctx: ctx, response: make(chan browserFrame, 1)}
	m.pending[id] = call
	m.mu.Unlock()
	defer func() { m.mu.Lock(); delete(m.pending, id); m.mu.Unlock(); call.requests.Wait() }()
	origins := append([]string(nil), h.op.Destination.Origins...)
	sort.Strings(origins)
	raw, _ := json.Marshal(origins)
	sum := sha256.Sum256(raw)
	message := map[string]any{"type": "call", "id": id, "owner": h.identity.RunID + "/" + h.identity.ProcessDigest, "origin_hash": hex.EncodeToString(sum[:]), "method": h.op.Method, "body": body, "timeout_ms": h.op.TimeoutMillis, "max_contexts": h.op.Browser.MaxContexts, "lease_ms": h.op.Browser.LeaseMillis}
	// Keep generation=0 explicit: zero is a real page generation, not omission.
	var payload map[string]any
	json.Unmarshal(mustBrowserJSON(body), &payload)
	if h.op.Method != "OPEN" && h.op.Method != "CLOSE" {
		payload["generation"] = body.Generation
	}
	if h.op.Method == "ADVANCE" {
		payload["text_exact"] = body.TextExact
		payload["expected_url"] = body.ExpectedURL
	}
	message["body"] = payload
	if err := m.send(message); err != nil {
		return nil, err
	}
	select {
	case frame := <-call.response:
		if frame.Error != "" {
			return nil, fmt.Errorf("browser: %s", frame.Error)
		}
		return frame.Result, nil
	case <-ctx.Done():
		m.stop()
		return nil, fmt.Errorf("browser operation deadline or cancellation")
	case <-m.done:
		return nil, fmt.Errorf("browser runtime exited")
	}
}
func mustBrowserJSON(v any) []byte { raw, _ := json.Marshal(v); return raw }
func (m *browserManager) read(reader io.Reader) {
	lines := bufio.NewReader(reader)
	for {
		line, err := lines.ReadBytes('\n')
		if err != nil {
			break
		}
		var frame browserFrame
		if json.Unmarshal(line, &frame) != nil {
			m.failAll()
			return
		}
		if frame.Type == "initialized" {
			select {
			case m.ready <- nil:
			default:
			}
			continue
		}
		if frame.Type == "result" && frame.ID == 0 {
			select {
			case m.ready <- fmt.Errorf("browser initialization refused"):
			default:
			}
			continue
		}
		m.mu.Lock()
		key := frame.ID
		if frame.Type == "request" {
			key = frame.CallID
		}
		call := m.pending[key]
		if call != nil && frame.Type == "request" {
			call.requests.Add(1)
		}
		m.mu.Unlock()
		if call == nil {
			continue
		}
		if frame.Type == "request" {
			go func() { defer call.requests.Done(); m.network(call, frame) }()
		} else if frame.Type == "result" {
			select {
			case call.response <- frame:
			default:
			}
		}
	}
	m.failAll()
}
func (m *browserManager) failAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, call := range m.pending {
		select {
		case call.response <- browserFrame{Type: "result", ID: id, Error: "session_lost"}:
		default:
		}
	}
}
func (m *browserManager) stop() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	input := m.input
	cmd := m.cmd
	directory := m.directory
	started := m.started
	m.mu.Unlock()
	if input != nil {
		input.Close()
	}
	if started {
		select {
		case <-m.done:
		case <-time.After(2 * time.Second):
			m.cancel()
			if cmd != nil && cmd.Process != nil {
				cmd.Process.Kill()
			}
			select {
			case <-m.done:
			case <-time.After(time.Second):
			}
		}
	}
	m.cancel()
	m.failAll()
	if directory != "" {
		os.RemoveAll(directory)
	}
}
func (m *browserManager) network(call *browserCall, frame browserFrame) {
	h := call.handle
	response := map[string]any{"type": "network_response", "request_id": frame.RequestID, "denied": true}
	deny := func() { h.mu.Lock(); h.receipt.DeniedRequests++; h.mu.Unlock(); m.send(response) }
	resource := false
	for _, v := range h.op.Browser.ResourceTypes {
		if v == frame.ResourceType {
			resource = true
		}
	}
	if call.ctx.Err() != nil || !resource || !h.permittedURL(frame.URL) || (frame.Method != "GET" && frame.Method != "HEAD") || frame.Redirects < 0 || frame.Redirects > h.op.MaxRedirects {
		deny()
		return
	}
	h.mu.Lock()
	if h.closed || h.receipt.Requests >= h.op.MaxRequests {
		h.mu.Unlock()
		deny()
		return
	}
	h.receipt.Requests++
	h.receipt.Transmission = "attempted_unknown"
	h.mu.Unlock()
	request, e := http.NewRequestWithContext(call.ctx, frame.Method, frame.URL, nil)
	if e != nil {
		deny()
		return
	}
	request.Header.Set("User-Agent", "PraxisSearch/1.0")
	request.Header.Set("Accept-Encoding", "identity")
	for _, name := range []string{"accept", "accept-language", "cookie"} {
		if v := frame.Headers[name]; v != "" && !strings.ContainsAny(v, "\x00\r\n") {
			request.Header.Set(name, v)
		}
	}
	transport := &http.Transport{Proxy: nil, DialContext: publicDial, DisableCompression: true, DisableKeepAlives: true, MaxResponseHeaderBytes: int64(^uint64(0) >> 1), TLSHandshakeTimeout: time.Duration(h.op.TimeoutMillis) * time.Millisecond}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	result, e := client.Do(request)
	if e != nil {
		m.send(response)
		return
	}
	defer result.Body.Close()
	h.mu.Lock()
	h.receipt.Responses++
	if h.receipt.Requests == h.receipt.Responses {
		h.receipt.Transmission = "response_received"
	}
	h.mu.Unlock()
	// Chromium's response interception receives decoded entity bytes. A server
	// can send a cached compressed entity even after Accept-Encoding: identity.
	// Decode under the same response cap and remove the encoding header before
	// fulfillment; raw compressed bytes would become a binary DOM document.
	var entity io.Reader = result.Body
	var decompressor io.ReadCloser
	switch strings.ToLower(strings.TrimSpace(result.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "gzip":
		decompressor, e = gzip.NewReader(result.Body)
	case "deflate":
		decompressor, e = zlib.NewReader(result.Body)
	default:
		m.send(response)
		return
	}
	if e != nil {
		m.send(response)
		return
	}
	if decompressor != nil {
		defer decompressor.Close()
		entity = decompressor
	}
	body, e := io.ReadAll(io.LimitReader(entity, int64(h.op.MaxResponseBytes)+1))
	if e != nil || len(body) > h.op.MaxResponseBytes {
		m.send(response)
		return
	}
	headers := map[string]string{}
	for name, values := range result.Header {
		lower := strings.ToLower(name)
		switch lower {
		case "connection", "transfer-encoding", "content-length", "content-encoding", "proxy-authenticate", "proxy-authorization":
			continue
		}
		if len(values) == 1 && !strings.ContainsAny(values[0], "\x00\r\n") {
			headers[lower] = values[0]
		}
	}
	response["denied"] = false
	response["status"] = result.StatusCode
	response["headers"] = headers
	response["body_base64"] = base64.StdEncoding.EncodeToString(body)
	m.send(response)
}
