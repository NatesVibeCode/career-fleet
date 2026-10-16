package egresscontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// NewOperationHTTPClient connects an SDK's injectable HTTP client to one
// existing typed operation. It never opens a socket, obtains credentials,
// selects a workflow, retries, or follows redirects. Credentials and physical
// transport remain owned by the supplied scoped Client. SDKs needing streaming,
// other body encodings, dynamic headers or multiple operations must declare and
// qualify those boundaries separately; this bridge refuses them.
func NewOperationHTTPClient(client Client, policy Policy, binding Binding) (*http.Client, error) {
	if client == nil || (policy.Mode != ModeHTTPS && policy.Mode != ModeLoopbackHTTP) {
		return nil, fmt.Errorf("SDK HTTP bridge requires a scoped HTTP operation")
	}
	// Freeze author-owned maps, slices and deployment selections.
	raw, err := json.Marshal(Normalize(policy))
	if err != nil {
		return nil, err
	}
	var frozen Policy
	if err = json.Unmarshal(raw, &frozen); err != nil {
		return nil, err
	}
	policy = frozen
	binding, err = ValidateBinding(policy, binding)
	if err != nil {
		return nil, err
	}
	for _, op := range policy.Operations {
		if op.ID == binding.Operation {
			return &http.Client{Transport: &operationHTTPTransport{client: client, operation: op, binding: binding}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
		}
	}
	return nil, fmt.Errorf("SDK HTTP operation absent")
}

type operationHTTPTransport struct {
	client    Client
	operation Operation
	binding   Binding
}

func (t *operationHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req != nil && req.Body != nil {
		defer req.Body.Close()
	}
	if req == nil || req.URL == nil || req.Method != t.operation.Method || req.URL.User != nil || req.URL.Opaque != "" || req.URL.Fragment != "" || (req.Host != "" && req.Host != req.URL.Host) || len(req.Trailer) != 0 || len(req.TransferEncoding) != 0 {
		return nil, fmt.Errorf("SDK request outside declared method, destination or framing")
	}
	for name, values := range req.Header {
		name = strings.ToLower(name)
		if len(values) != 1 {
			return nil, fmt.Errorf("SDK header requires one declared value")
		}
		if name == "content-type" && values[0] == "application/json" {
			continue
		}
		declared, ok := t.operation.Headers[name]
		if !ok || values[0] != declared {
			return nil, fmt.Errorf("SDK header %q is not the declared fixed value", name)
		}
	}
	input := OperationRequest{Operation: t.operation.ID}
	if t.operation.Destination.Kind == DestinationPublicWeb {
		input.URL = req.URL.String()
	} else {
		canonical := CanonicalOrigin
		if req.URL.Scheme == "http" {
			canonical = CanonicalLoopbackOrigin
		}
		origin, err := canonical(req.URL.Scheme + "://" + req.URL.Host)
		if err != nil || len(t.binding.Origins) == 0 || origin != t.binding.Origins[0] {
			return nil, fmt.Errorf("SDK request changes selected service origin")
		}
		params, err := sdkPathParameters(t.operation.Destination.Path, req.URL.EscapedPath())
		if err != nil {
			return nil, err
		}
		input.PathParameters = params
		query, err := url.ParseQuery(req.URL.RawQuery)
		if err != nil {
			return nil, fmt.Errorf("SDK query invalid")
		}
		input.RepeatedQueryParameters = map[string][]string(query)
		if _, err = applyDeclaredQuery(req.URL, t.operation, input); err != nil {
			return nil, err
		}
		// The same native resolver independently checks scope and canonical
		// path reconstruction; no SDK URL can override the selected endpoint.
		if req.URL.Scheme == "https" {
			h := nativeHandle{op: t.operation, binding: t.binding}
			resolved, err := h.destination(input)
			if err != nil {
				return nil, err
			}
			if resolved.EscapedPath() != req.URL.EscapedPath() {
				return nil, fmt.Errorf("SDK path is not canonical")
			}
		} else if len(params) != 0 || req.URL.EscapedPath() != t.operation.Destination.Path {
			return nil, fmt.Errorf("loopback SDK path differs from declared literal")
		}
	}
	if req.Body != nil {
		raw, err := readHTTPResponse(req.Body, t.operation.MaxRequestBytes)
		if err != nil {
			return nil, err
		}
		if t.operation.MaxRequestBytes > 0 && len(raw) > t.operation.MaxRequestBytes {
			return nil, fmt.Errorf("SDK body exceeds authored request quota")
		}
		input.Body = raw
	}
	if len(input.Body) == 0 && req.Header.Get("Content-Type") != "" {
		return nil, fmt.Errorf("SDK bodyless content-type is not represented by this operation transport")
	}
	response, err := t.client.Do(req.Context(), input)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 100 || response.StatusCode > 599 {
		return nil, fmt.Errorf("scoped HTTP result status absent")
	}
	actualRequest := req.Clone(req.Context())
	if response.URL != "" {
		actualRequest.URL, err = url.Parse(response.URL)
		if err != nil {
			return nil, fmt.Errorf("scoped response URL invalid")
		}
	}
	return &http.Response{StatusCode: response.StatusCode, Status: fmt.Sprintf("%d %s", response.StatusCode, http.StatusText(response.StatusCode)), Header: http.Header(response.Headers).Clone(), Body: io.NopCloser(bytes.NewReader(response.Body)), ContentLength: int64(len(response.Body)), Request: actualRequest}, nil
}

func sdkPathParameters(pattern, path string) (map[string]string, error) {
	parts, wanted := strings.Split(path, "/"), strings.Split(pattern, "/")
	if len(parts) != len(wanted) {
		return nil, fmt.Errorf("SDK path differs from declaration")
	}
	params := map[string]string{}
	for i, part := range wanted {
		if !strings.HasPrefix(part, "{") {
			if part != parts[i] {
				return nil, fmt.Errorf("SDK path differs from declaration")
			}
			continue
		}
		end := strings.IndexByte(part, '}')
		if end < 1 {
			return nil, fmt.Errorf("invalid declared SDK path")
		}
		key, suffix := part[1:end], part[end+1:]
		if !strings.HasSuffix(parts[i], suffix) {
			return nil, fmt.Errorf("SDK path action changed")
		}
		value, err := url.PathUnescape(strings.TrimSuffix(parts[i], suffix))
		if err != nil || value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/%\\?#\x00\r\n") {
			return nil, fmt.Errorf("SDK path parameter invalid")
		}
		if previous, ok := params[key]; ok && previous != value {
			return nil, fmt.Errorf("SDK path parameter disagrees")
		}
		params[key] = value
	}
	return params, nil
}
