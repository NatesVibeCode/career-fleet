// Package egresscontract defines portable Node egress permissions. It contains
// no process, socket, filesystem, or operating-system policy details; target
// adapters lower the declared operations into their own enforcement model.
package egresscontract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	SchemaV1 = "praxis.egress_policy.v1"

	ModeNone         = "none"
	ModeHTTPS        = "https"
	ModeLoopbackHTTP = "loopback_http"
	ModeInfer        = "infer"
	ModeSQLite       = "sqlite"
	ModeFilesystem   = "filesystem"
	ModeBrowser      = "browser"

	DestinationService    = "service_origin"
	DestinationPublicWeb  = "public_web"
	DestinationSQLite     = "local_sqlite"
	DestinationFilesystem = "workspace_files"
	DestinationBrowser    = "local_browser"

	RedirectNone            = "none"
	RedirectSameOrigin      = "same_origin"
	RedirectDeclaredOrigins = "declared_origins"
)

// Policy is the portable permission granted to one Node. An empty or
// "none" policy grants no external network operation. HTTPS policies name
// each operation and its fixed method, destination rule, credentials, and
// explicit execution policy. Zero HTTP byte or timeout values impose no local quota;
// positive values are enforced exactly and must be justified by their author.
type Policy struct {
	Schema     string      `json:"schema"`
	Mode       string      `json:"mode"`
	Operations []Operation `json:"operations"`
}

type Operation struct {
	RuntimeMaterial   []RuntimeMaterialPermission  `json:"runtime_material,omitempty"`
	GoInputs          *GoInputsPermission          `json:"go_inputs,omitempty"`
	GoCommand         *GoCommandPermission         `json:"go_command,omitempty"`
	NativeExec        *NativeExecPermission        `json:"native_exec,omitempty"`
	ConditionalRecord *ConditionalRecordPermission `json:"conditional_record,omitempty"`
	AtomicFile        *AtomicFilePermission        `json:"atomic_file,omitempty"`
	RecordStore       *RecordStorePermission       `json:"record_store,omitempty"`
	WorkspaceDelivery *WorkspaceDeliveryPermission `json:"workspace_delivery,omitempty"`
	// Browser pins the implementation and primitive permissions of a local
	// browser operation. Source selection and page logic remain Node-owned.
	Browser          *BrowserPermission    `json:"browser,omitempty"`
	Filesystem       *FilesystemPermission `json:"filesystem,omitempty"`
	ID               string                `json:"id"`
	Method           string                `json:"method"`
	Destination      Destination           `json:"destination"`
	Credentials      []Credential          `json:"credentials"`
	MaxRequests      int                   `json:"max_requests"`
	MaxRequestBytes  int                   `json:"max_request_bytes"`
	MaxResponseBytes int                   `json:"max_response_bytes"`
	TimeoutMillis    int64                 `json:"timeout_ms"`
	MaxRedirects     int                   `json:"max_redirects"`
	RedirectPolicy   string                `json:"redirect_policy"`
	QueryParameters  []string              `json:"query_parameters,omitempty"`
	// Headers carries fixed policy-declared request headers (for example
	// provider version pins). Values are constant per operation: callers
	// cannot supply or override headers through OperationRequest, which
	// intentionally has no header field. Declared headers must never carry
	// credential material; credentials travel only via Credential refs,
	// which the executor attaches as Authorization: Bearer or as one of the
	// credential's fixed, validated API-key headers. Transport-integrity
	// headers (authorization, content-length, ...) are refused at
	// validation. Public-web operations may declare only fixed Accept and
	// User-Agent headers. Per-call header values (for example echoes of a path
	// parameter) are not expressible: a header value that must vary per call
	// needs a new echo-binding mechanism, not a fixed declaration.
	Headers map[string]string `json:"headers,omitempty"`
}

type Destination struct {
	Kind    string   `json:"kind"`
	Origins []string `json:"origins,omitempty"`
	Path    string   `json:"path,omitempty"`
}

// Credential binds a credential reference to the specific service origins
// where the executor may attach it. The credential value is never part of this
// contract.
type Credential struct {
	Ref     string   `json:"ref"`
	Origins []string `json:"origins"`
	// Header selects a supported provider API-key header. Empty preserves the
	// existing Authorization: Bearer behavior. Only the fixed allowlist below
	// is accepted; credentials never enter Operation.Headers or call inputs.
	Header string `json:"header,omitempty"`
}

// Binding is customer deployment input. Every selected origin and credential
// must already appear in the Node policy. Empty Origins selects all declared
// service origins, or leaves a public-web policy at its declared public-web
// scope. CredentialRef selects one declared reference and cannot add one.
type Binding struct {
	RuntimeMaterial []RuntimeMaterialBinding `json:"runtime_material,omitempty"`
	GoCommand       *GoCommandBinding        `json:"go_command,omitempty"`
	NativeExec      *NativeExecBinding       `json:"native_exec,omitempty"`
	Browser         *BrowserBinding          `json:"browser,omitempty"`
	// Directory selects customer-owned local database custody. SQL, table
	// names and database basename are fixed by the printed Node and policy.
	Directory     string   `json:"directory,omitempty"`
	Routes        []string `json:"routes,omitempty"`
	Operation     string   `json:"operation"`
	Origins       []string `json:"origins,omitempty"`
	CredentialRef string   `json:"credential_ref,omitempty"`
}

// OperationRequest is the only request shape exposed to an Node capability
// client. Service methods and destinations come from Policy; only public-web
// retrieval accepts URL. No caller-supplied headers or authorization values
// exist in this type.
type OperationRequest struct {
	// Operation selects one sealed Tool operation when a Node has more than
	// one. Single-operation clients may omit it; adapters reject a mismatched
	// selector before any request is sent.
	Operation       string            `json:"operation,omitempty"`
	URL             string            `json:"url,omitempty"`
	PathParameters  map[string]string `json:"path_parameters,omitempty"`
	QueryParameters map[string]string `json:"query_parameters,omitempty"`
	// RepeatedQueryParameters preserves repeated values such as event_deltas[]
	// without allowing a caller to add a new query name or destination.
	RepeatedQueryParameters map[string][]string `json:"repeated_query_parameters,omitempty"`
	Body                    []byte              `json:"body,omitempty"`
}

type OperationResponse struct {
	Headers map[string][]string `json:"headers,omitempty"`
	// ResourceIdentity binds local acquisition to its caller-selected canonical
	// resource. It is separate from the content digest of the response body.
	ResourceIdentity string             `json:"resource_identity,omitempty"`
	Diagnostic       *FailureDiagnostic `json:"diagnostic,omitempty"`
	StatusCode       int                `json:"status_code"`
	URL              string             `json:"url"`
	Body             []byte             `json:"body"`
	ContentType      string             `json:"content_type,omitempty"`
	ContentSignal    string             `json:"content_signal,omitempty"`
}

// EventStreamHandler receives one complete Server-Sent Events data payload.
// Returning stop=true closes the response body after the current event. The
// callback runs synchronously while the scoped request is active; it should
// stay bounded and must not re-enter that same client. The payload is borrowed
// for the callback and must be copied if it needs to outlive the call.
type EventStreamHandler func(data []byte) (stop bool, err error)

type Identity struct {
	InputDigest         string `json:"input_digest,omitempty"`
	ConfigurationDigest string `json:"configuration_digest,omitempty"`
	RunID               string `json:"run_id"`
	ProcessDigest       string `json:"process_digest"`
	NodeKey             string `json:"node_key"`
	NodeName            string `json:"node_name"`
	NodeDigest          string `json:"node_digest"`
	NodeSpecDigest      string `json:"node_spec_digest"`
	PolicyDigest        string `json:"policy_digest"`
	Operation           string `json:"operation"`
}

type Receipt struct {
	Selection         *InvocationSelection      `json:"selection,omitempty"`
	Calls             []OperationObservation    `json:"calls,omitempty"`
	AtomicFile        *AtomicFileReceipt        `json:"atomic_file,omitempty"`
	NativeExec        *NativeExecReceipt        `json:"native_exec,omitempty"`
	ConditionalRecord *ConditionalRecordReceipt `json:"conditional_record,omitempty"`
	FailureDiagnostic *FailureDiagnostic        `json:"failure_diagnostic,omitempty"`
	// BrowserCalls counts local primitive calls separately from Requests,
	// which counts transmitted HTTP requests for the browser adapter.
	BrowserCalls          int                    `json:"browser_calls,omitempty"`
	DeniedRequests        int                    `json:"denied_requests,omitempty"`
	InferenceAttempts     []InferenceAttempt     `json:"inference_attempts,omitempty"`
	PublicReadReceipts    []PublicReadReceipt    `json:"public_read_receipts,omitempty"`
	ProviderHTTPSReceipts []ProviderHTTPSReceipt `json:"provider_https_receipts,omitempty"`
	Transmission          string                 `json:"transmission"`
	Responses             int                    `json:"responses"`
	Identity              Identity               `json:"identity"`
	Requests              int                    `json:"requests"`
	PolicyDigest          string                 `json:"policy_digest"`
	Operation             string                 `json:"operation"`
	Adapter               string                 `json:"adapter"`
	Outcome               string                 `json:"outcome"`
}

// CredentialProvider resolves references inside a trusted target adapter.
// Values must not be copied into an Node child process or receipt.
type CredentialProvider interface {
	Resolve(context.Context, Identity, string) ([]byte, error)
}

type Invocation struct {
	Identity    Identity
	Policy      Policy
	Binding     Binding
	Credentials CredentialProvider
}

type Result struct {
	Response []byte
	Receipt  Receipt
}

// InvocationHandle gives target adapters explicit cancellation and receipt
// surfaces for a single invocation. Implementations must close invocation
// channels and release credentials when cancelled or complete.
type InvocationHandle interface {
	Invoke(context.Context, []byte) (Result, error)
	Cancel() error
	Receipt() Receipt
}

// EventStreamInvocationHandle is an optional extension for targets that can
// safely deliver bounded SSE data events while the scoped request is active.
// It does not widen the method, route, headers, credential, or byte/time limits
// sealed in the operation policy.
type EventStreamInvocationHandle interface {
	InvokeStream(context.Context, []byte, EventStreamHandler) (Result, error)
}

// TargetAdapter separates Node permission meaning from native or future edge
// enforcement. Unsupported policies must return an error; implementations
// must not fall back to broader networking.
type TargetAdapter interface {
	Name() string
	Supports(Policy) error
	Start(context.Context, Invocation) (InvocationHandle, error)
}

func EmptyPolicy() Policy {
	return Policy{Schema: SchemaV1, Mode: ModeNone, Operations: []Operation{}}
}

func Parse(raw []byte) (Policy, error) {
	var policy Policy
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&policy); err != nil {
		return Policy{}, fmt.Errorf("egress policy: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return Policy{}, fmt.Errorf("egress policy: trailing JSON")
	}
	return Normalize(policy), policy.Validate()
}

func Normalize(policy Policy) Policy {
	normalizeOrigin := canonicalOrigins
	if policy.Mode == ModeLoopbackHTTP {
		normalizeOrigin = canonicalLoopbackOrigins
	}
	policy.Operations = append([]Operation(nil), policy.Operations...)
	sort.Slice(policy.Operations, func(i, j int) bool { return policy.Operations[i].ID < policy.Operations[j].ID })
	for i := range policy.Operations {
		op := &policy.Operations[i]
		if op.GoInputs != nil {
			p := *op.GoInputs
			op.GoInputs = &p
		}
		if op.GoCommand != nil {
			p := *op.GoCommand
			op.GoCommand = &p
		}
		if op.ConditionalRecord != nil {
			permission := *op.ConditionalRecord
			op.ConditionalRecord = &permission
		}
		if op.NativeExec != nil {
			copy := *op.NativeExec
			copy.Arguments = append([]string{}, copy.Arguments...)
			op.NativeExec = &copy
		}
		if op.RecordStore != nil {
			permission := *op.RecordStore
			op.RecordStore = &permission
		}
		if op.WorkspaceDelivery != nil {
			permission := *op.WorkspaceDelivery
			permission.Files = append([]string{}, permission.Files...)
			permission.ConsumerFiles = append([]string{}, permission.ConsumerFiles...)
			permission.Procedures = append([]WorkspaceProcedure{}, permission.Procedures...)
			for j := range permission.Procedures {
				permission.Procedures[j].Arguments = append([]string{}, permission.Procedures[j].Arguments...)
				permission.Procedures[j].ReadOnlyPaths = append([]string{}, permission.Procedures[j].ReadOnlyPaths...)
				permission.Procedures[j].Environment = append([]WorkspaceEnvironment{}, permission.Procedures[j].Environment...)
			}
			op.WorkspaceDelivery = &permission
		}
		if op.Browser != nil {
			permission := *op.Browser
			permission.ResourceTypes = append([]string{}, permission.ResourceTypes...)
			op.Browser = &permission
		}
		op.Destination.Origins = normalizeOrigin(op.Destination.Origins)
		op.RuntimeMaterial = append([]RuntimeMaterialPermission(nil), op.RuntimeMaterial...)
		sort.Slice(op.RuntimeMaterial, func(i, j int) bool { return op.RuntimeMaterial[i].ID < op.RuntimeMaterial[j].ID })
		op.QueryParameters = append([]string(nil), op.QueryParameters...)
		sort.Strings(op.QueryParameters)
		if len(op.Headers) > 0 {
			lowered := make(map[string]string, len(op.Headers))
			for name, value := range op.Headers {
				lowered[strings.ToLower(name)] = value
			}
			op.Headers = lowered
		}
		op.Credentials = append([]Credential(nil), op.Credentials...)
		sort.Slice(op.Credentials, func(i, j int) bool { return op.Credentials[i].Ref < op.Credentials[j].Ref })
		for j := range op.Credentials {
			op.Credentials[j].Origins = normalizeOrigin(op.Credentials[j].Origins)
			op.Credentials[j].Header = strings.ToLower(strings.TrimSpace(op.Credentials[j].Header))
		}
	}
	return policy
}

func (p Policy) Validate() error {
	for _, op := range p.Operations {
		if err := validateRuntimeMaterialPermissions(op.RuntimeMaterial); err != nil {
			return err
		}
		if op.Browser != nil && p.Mode != ModeBrowser || op.GoInputs != nil && p.Mode != ModeGoInputs || op.GoCommand != nil && p.Mode != ModeGoCommand || op.ConditionalRecord != nil && p.Mode != ModeConditionalRecord || op.NativeExec != nil && p.Mode != ModeNativeExec || op.RecordStore != nil && p.Mode != ModeRecordStore || op.WorkspaceDelivery != nil && p.Mode != ModeWorkspaceDelivery || op.AtomicFile != nil && p.Mode != ModeAtomicFile {
			return fmt.Errorf("local permission mode mismatch")
		}
	}
	if p.Schema != SchemaV1 {
		return fmt.Errorf("schema must be %q", SchemaV1)
	}
	loopback := false
	switch p.Mode {
	case ModeGoInputs:
		if len(p.Operations) != 1 {
			return fmt.Errorf("Go inputs require one sealed profile")
		}
		return validateGoInputsOperation(p.Operations[0])
	case ModeGoCommand:
		if len(p.Operations) != 1 {
			return fmt.Errorf("Go command requires one sealed profile")
		}
		return validateGoCommandOperation(p.Operations[0])
	case ModeNativeExec:
		if len(p.Operations) != 1 {
			return fmt.Errorf("native execution requires one operation")
		}
		return validateNativeExecOperation(p.Operations[0])
	case ModeConditionalRecord:
		if len(p.Operations) != 1 {
			return fmt.Errorf("conditional record requires one exact operation")
		}
		return validateConditionalRecordOperation(p.Operations[0])
	case ModeAtomicFile:
		if len(p.Operations) != 1 {
			return fmt.Errorf("atomic file requires one exact operation")
		}
		return validateAtomicFileOperation(p.Operations[0])
	case ModeRecordStore:
		if len(p.Operations) != 1 {
			return fmt.Errorf("record store requires one exact operation")
		}
		return validateRecordStoreOperation(p.Operations[0])
	case ModeWorkspaceDelivery:
		if len(p.Operations) != 1 {
			return fmt.Errorf("workspace delivery requires one exact operation")
		}
		return validateWorkspaceDeliveryOperation(p.Operations[0])
	case ModeBrowser:
		if len(p.Operations) != 1 {
			return fmt.Errorf("browser policy requires one exact primitive operation")
		}
		return validateBrowserOperation(p.Operations[0])
	case ModeFilesystem:
		if len(p.Operations) != 1 {
			return fmt.Errorf("workspace snapshot requires one operation")
		}
		return validateFilesystemOperation(p.Operations[0])
	case ModeSQLite:
		if len(p.Operations) == 0 {
			return fmt.Errorf("SQLite policy must declare operations")
		}
		seen := map[string]bool{}
		for _, op := range p.Operations {
			if seen[op.ID] {
				return fmt.Errorf("duplicate SQLite operation")
			}
			seen[op.ID] = true
			if err := validateSQLiteOperation(op); err != nil {
				return err
			}
		}
		return nil
	case ModeNone, ModeInfer:
		if len(p.Operations) != 0 {
			return fmt.Errorf("no-network policy must not declare operations")
		}
		return nil
	case ModeHTTPS:
		if len(p.Operations) == 0 {
			return fmt.Errorf("HTTPS policy must declare operations")
		}
	case ModeLoopbackHTTP:
		if len(p.Operations) == 0 {
			return fmt.Errorf("loopback HTTP policy must declare operations")
		}
		loopback = true
	default:
		return fmt.Errorf("unsupported egress mode %q", p.Mode)
	}
	seen := make(map[string]struct{}, len(p.Operations))
	for _, op := range p.Operations {
		if !validToken(op.ID) {
			return fmt.Errorf("operation id is invalid")
		}
		if _, exists := seen[op.ID]; exists {
			return fmt.Errorf("duplicate operation %q", op.ID)
		}
		seen[op.ID] = struct{}{}
		if err := op.validate(loopback); err != nil {
			return fmt.Errorf("operation %q: %w", op.ID, err)
		}
	}
	return nil
}

func (op Operation) Validate() error {
	if op.NativeExec != nil {
		return validateNativeExecOperation(op)
	}
	if op.AtomicFile != nil {
		return validateAtomicFileOperation(op)
	}
	if op.Destination.Kind == DestinationBrowser {
		return validateBrowserOperation(op)
	}
	if op.Destination.Kind == DestinationSQLite {
		return validateSQLiteOperation(op)
	}
	return op.validate(false)
}

func (op Operation) validate(loopback bool) error {
	if op.Filesystem != nil {
		return fmt.Errorf("workspace permissions require filesystem mode")
	}
	if op.Browser != nil {
		return fmt.Errorf("HTTPS operation cannot declare a browser runtime")
	}
	if !validToken(op.ID) {
		return fmt.Errorf("invalid id")
	}
	if op.Method != strings.ToUpper(op.Method) || !allowedMethod(op.Method) {
		return fmt.Errorf("unsupported HTTPS method %q", op.Method)
	}
	if op.MaxRequests < 1 {
		return fmt.Errorf("max_requests must be positive")
	}
	if op.MaxRequestBytes < 0 {
		return fmt.Errorf("max_request_bytes cannot be negative")
	}
	if op.MaxResponseBytes < 0 {
		return fmt.Errorf("max_response_bytes cannot be negative")
	}
	if op.TimeoutMillis < 0 || op.TimeoutMillis > int64(^uint64(0)>>1)/int64(time.Millisecond) {
		return fmt.Errorf("timeout_ms must be a nonnegative representable duration")
	}
	if op.MaxRedirects < 0 {
		return fmt.Errorf("max_redirects cannot be negative")
	}
	if op.MaxRedirects == 0 && op.RedirectPolicy != RedirectNone {
		return fmt.Errorf("redirect_policy must be %q when redirects are disabled", RedirectNone)
	}
	if op.MaxRedirects > 0 && op.RedirectPolicy != RedirectSameOrigin {
		return fmt.Errorf("redirect_policy must be %q", RedirectSameOrigin)
	}

	switch op.Destination.Kind {
	case DestinationService:
		if len(op.Destination.Origins) == 0 {
			return fmt.Errorf("service destination must name origins")
		}
		validate := validateOrigins
		canonicalize := canonicalOrigins
		if loopback {
			if len(op.Credentials) != 0 {
				return fmt.Errorf("loopback service cannot declare credentials")
			}
			validate = validateLoopbackOrigins
			canonicalize = canonicalLoopbackOrigins
		}
		if err := validate(op.Destination.Origins); err != nil {
			return err
		}
		if err := ValidateServicePath(op.Destination.Path); err != nil {
			return err
		}
		seen := map[string]struct{}{}
		for _, credential := range op.Credentials {
			if !validToken(credential.Ref) {
				return fmt.Errorf("invalid credential reference")
			}
			if _, exists := seen[credential.Ref]; exists {
				return fmt.Errorf("duplicate credential reference %q", credential.Ref)
			}
			seen[credential.Ref] = struct{}{}
			if credential.Header != "" {
				if _, ok := credentialHeaders[credential.Header]; !ok {
					return fmt.Errorf("credential %q uses unsupported auth header", credential.Ref)
				}
				if _, duplicate := op.Headers[credential.Header]; duplicate {
					return fmt.Errorf("credential %q header duplicates a fixed header", credential.Ref)
				}
			}
			if len(credential.Origins) == 0 {
				return fmt.Errorf("credential %q has no destination origins", credential.Ref)
			}
			if err := validate(credential.Origins); err != nil {
				return fmt.Errorf("credential %q: %w", credential.Ref, err)
			}
			for _, origin := range canonicalize(credential.Origins) {
				if !contains(canonicalize(op.Destination.Origins), origin) {
					return fmt.Errorf("credential %q widens service origins", credential.Ref)
				}
			}
		}
	case DestinationPublicWeb:
		if loopback {
			return fmt.Errorf("loopback policy requires a service destination")
		}
		if len(op.Destination.Origins) != 0 || op.Destination.Path != "" {
			return fmt.Errorf("public-web destination cannot pin service origins or paths")
		}
		if op.Method != "GET" && op.Method != "HEAD" {
			return fmt.Errorf("public-web retrieval permits GET or HEAD only")
		}
		if len(op.Credentials) != 0 {
			return fmt.Errorf("public-web retrieval cannot declare credentials")
		}
	default:
		return fmt.Errorf("unsupported destination kind %q", op.Destination.Kind)
	}
	if op.Destination.Kind != DestinationService && len(op.QueryParameters) > 0 {
		return fmt.Errorf("query parameters require a service destination")
	}
	seen := map[string]struct{}{}
	for _, name := range op.QueryParameters {
		if !validQueryParameterName(name) {
			return fmt.Errorf("invalid query parameter %q", name)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate query parameter %q", name)
		}
		seen[name] = struct{}{}
	}
	seenHeaders := map[string]struct{}{}
	for name, value := range op.Headers {
		lowered := strings.ToLower(name)
		if op.Destination.Kind == DestinationPublicWeb && lowered != "accept" && lowered != "user-agent" {
			return fmt.Errorf("public-web header %q is not allowed", name)
		}
		if !validToken(name) {
			return fmt.Errorf("invalid header name %q", name)
		}
		if forbiddenHeader(lowered) {
			return fmt.Errorf("forbidden header %q", name)
		}
		if _, exists := seenHeaders[lowered]; exists {
			return fmt.Errorf("duplicate header %q", name)
		}
		seenHeaders[lowered] = struct{}{}
		if err := validHeaderValue(value); err != nil {
			return fmt.Errorf("invalid header value for %q: %w", name, err)
		}
	}
	return nil
}

func validQueryParameterName(value string) bool {
	if value == "" {
		return false
	}
	if open := strings.IndexByte(value, '['); open >= 0 {
		if !strings.HasSuffix(value, "]") || strings.Count(value, "[") != 1 || strings.Count(value, "]") != 1 {
			return false
		}
		base := value[:open]
		member := value[open+1 : len(value)-1]
		if !queryNameChars(base) || (member != "" && !queryNameChars(member)) {
			return false
		}
		return true
	}
	return queryNameChars(value)
}

func queryNameChars(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.:-", r)) {
			return false
		}
	}
	return true
}

// forbiddenHeaders names the transport-integrity, credential, and framing
// headers a policy may never declare. The executor owns authorization,
// content framing, and routing; declared headers only carry provider
// metadata such as version pins.
var forbiddenHeaders = map[string]struct{}{
	"authorization": {}, "proxy-authorization": {}, "proxy-authenticate": {},
	"x-api-key": {}, "x-goog-api-key": {}, "x-subscription-token": {},
	"host": {}, "content-length": {}, "content-type": {}, "transfer-encoding": {},
	"te": {}, "trailer": {}, "upgrade": {}, "connection": {}, "keep-alive": {},
	"cookie": {}, "set-cookie": {}, "expect": {},
}

var credentialHeaders = map[string]struct{}{
	"x-api-key":            {},
	"x-goog-api-key":       {},
	"x-subscription-token": {},
}

func forbiddenHeader(lowerName string) bool {
	_, forbidden := forbiddenHeaders[lowerName]
	return forbidden
}

func validHeaderValue(value string) error {
	if value == "" {
		return fmt.Errorf("header value must not be empty")
	}
	if value != strings.Trim(value, " ") {
		return fmt.Errorf("header value must not have leading or trailing spaces")
	}
	for _, r := range value {
		if r < 0x20 || r > 0x7e || r == 0x7f {
			return fmt.Errorf("header value must be printable ASCII")
		}
	}
	return nil
}

func (p Policy) Digest() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	normalized := Normalize(p)
	if err := normalized.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func ValidateBinding(policy Policy, binding Binding) (Binding, error) {
	if err := validateRuntimeMaterialBinding(policy, binding); err != nil {
		return Binding{}, err
	}

	if err := policy.Validate(); err != nil {
		return Binding{}, err
	}
	if policy.Mode == ModeWorkspaceDelivery {
		return validateWorkspaceDeliveryBinding(policy, binding)
	}
	if policy.Mode == ModeGoInputs {
		return validateGoInputsBinding(policy, binding)
	}
	if policy.Mode == ModeGoCommand {
		return validateGoCommandBinding(policy, binding)
	}
	if binding.GoCommand != nil {
		return Binding{}, fmt.Errorf("Go runtime requires Go command mode")
	}
	if policy.Mode == ModeNativeExec {
		return validateNativeExecBinding(policy, binding)
	}
	if policy.Mode == ModeConditionalRecord {
		return validateConditionalRecordBinding(policy, binding)
	}
	if policy.Mode == ModeAtomicFile {
		return validateAtomicFileBinding(policy, binding)
	}
	if policy.Mode == ModeRecordStore {
		return validateWorkspaceDeliveryBinding(policy, binding)
	}
	if policy.Mode == ModeFilesystem {
		return validateFilesystemBinding(policy, binding)
	}
	if policy.Mode == ModeSQLite {
		return validateSQLiteBinding(policy, binding)
	}
	if policy.Mode == ModeBrowser {
		return validateBrowserBinding(policy, binding)
	}
	if binding.Browser != nil {
		return Binding{}, fmt.Errorf("network binding cannot select a browser runtime")
	}
	if binding.Directory != "" {
		return Binding{}, fmt.Errorf("network binding cannot select local storage")
	}
	loopback := policy.Mode == ModeLoopbackHTTP
	validate := validateOrigins
	canonicalize := canonicalOrigins
	validateOrigin := ValidateOrigin
	if loopback {
		validate = validateLoopbackOrigins
		canonicalize = canonicalLoopbackOrigins
		validateOrigin = ValidateLoopbackOrigin
	}
	if err := validate(binding.Origins); err != nil {
		return Binding{}, err
	}
	policy = Normalize(policy)
	if err := policy.Validate(); err != nil {
		return Binding{}, err
	}
	if policy.Mode != ModeHTTPS && policy.Mode != ModeLoopbackHTTP {
		return Binding{}, fmt.Errorf("no-network policy has no egress binding")
	}
	var operation *Operation
	for i := range policy.Operations {
		if policy.Operations[i].ID == binding.Operation {
			operation = &policy.Operations[i]
			break
		}
	}
	if operation == nil {
		return Binding{}, fmt.Errorf("binding selects undeclared operation")
	}
	binding.Origins = canonicalize(binding.Origins)
	if operation.Destination.Kind == DestinationService && len(binding.Origins) == 0 {
		binding.Origins = append([]string(nil), operation.Destination.Origins...)
	}
	if len(binding.Origins) > 0 {
		for _, origin := range binding.Origins {
			if err := validateOrigin(origin); err != nil {
				return Binding{}, err
			}
			if operation.Destination.Kind == DestinationService && !contains(canonicalize(operation.Destination.Origins), origin) {
				return Binding{}, fmt.Errorf("binding widens service origins")
			}
		}
	}
	if loopback && binding.CredentialRef != "" {
		return Binding{}, fmt.Errorf("loopback binding cannot carry credentials")
	}
	if binding.CredentialRef != "" {
		var selected *Credential
		for i := range operation.Credentials {
			if operation.Credentials[i].Ref == binding.CredentialRef {
				selected = &operation.Credentials[i]
				break
			}
		}
		if selected == nil {
			return Binding{}, fmt.Errorf("binding selects undeclared credential reference")
		}
		if operation.Destination.Kind == DestinationPublicWeb {
			return Binding{}, fmt.Errorf("public-web binding cannot carry credentials")
		}
		allowed := canonicalize(selected.Origins)
		if len(binding.Origins) > 0 {
			for _, origin := range binding.Origins {
				if !contains(allowed, origin) {
					return Binding{}, fmt.Errorf("binding widens credential destinations")
				}
			}
		}
	} else if len(operation.Credentials) > 0 {
		return Binding{}, fmt.Errorf("binding must select one declared credential reference")
	}
	if operation.Destination.Kind == DestinationPublicWeb && len(binding.Origins) > 0 {
		for _, origin := range binding.Origins {
			if err := validateOrigin(origin); err != nil {
				return Binding{}, err
			}
		}
	}
	return binding, nil
}

func ValidateOrigin(origin string) error {
	canonical, err := CanonicalOrigin(origin)
	if err != nil {
		return err
	}
	if canonical != origin {
		return fmt.Errorf("origin %q must use canonical form %q", origin, canonical)
	}
	return nil
}

func CanonicalOrigin(origin string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || parsed == nil {
		return "", fmt.Errorf("invalid HTTPS origin")
	}
	if parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("destination origin must be an HTTPS origin without credentials, path, query, or fragment")
	}
	if parsed.Opaque != "" || parsed.Host == "" || strings.Contains(parsed.Host, "%") {
		return "", fmt.Errorf("invalid HTTPS origin host")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "" || strings.ContainsAny(host, " \t\r\n\x00") {
		return "", fmt.Errorf("invalid HTTPS origin host")
	}
	if addr := net.ParseIP(host); addr == nil {
		if !validDNSName(host) {
			return "", fmt.Errorf("invalid HTTPS origin host")
		}
	} else {
		host = addr.String()
	}
	port := parsed.Port()
	if port != "" && port != "443" {
		return "", fmt.Errorf("HTTPS origin port must be 443")
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return "https://" + host, nil
}

// Loopback origins are a separate, deliberately narrow class of destination.
// They permit only an IPv4 loopback HTTP server on an explicit port; names,
// IPv6 aliases, credentials, paths, and remote private ranges are refused.
func CanonicalLoopbackOrigin(origin string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || parsed == nil {
		return "", fmt.Errorf("invalid loopback HTTP origin")
	}
	if parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || parsed.Opaque != "" || parsed.Host == "" || strings.Contains(parsed.Host, "%") {
		return "", fmt.Errorf("destination origin must be a loopback HTTP origin without credentials, path, query, or fragment")
	}
	if parsed.Hostname() != "127.0.0.1" {
		return "", fmt.Errorf("loopback origin host must be 127.0.0.1")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("loopback origin must name an explicit valid port")
	}
	return "http://127.0.0.1:" + strconv.Itoa(port), nil
}

func ValidateLoopbackOrigin(origin string) error {
	canonical, err := CanonicalLoopbackOrigin(origin)
	if err != nil {
		return err
	}
	if canonical != origin {
		return fmt.Errorf("origin %q must use canonical form %q", origin, canonical)
	}
	return nil
}

func ValidateServicePath(path string) error {
	if path == "" || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "?#\\%\x00\r\n") {
		return fmt.Errorf("service path must be an absolute bounded path without query or fragment")
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return fmt.Errorf("service path cannot contain dot segments")
		}
		if strings.HasPrefix(segment, "{") || strings.HasSuffix(segment, "}") {
			if segment[0] != '{' {
				return fmt.Errorf("invalid service path parameter")
			}
			rest := segment[1:]
			end := strings.Index(rest, "}")
			name, suffix := rest, ""
			if end >= 0 {
				name, suffix = rest[:end], rest[end+1:]
			}
			if name == "" || !validToken(name) {
				return fmt.Errorf("invalid service path parameter")
			}
			if suffix != "" {
				// Provider action suffixes (for example Google's
				// `{range}:clear` custom verbs): one fixed literal verb
				// after the parameter. The verb is policy-fixed and
				// digested; callers cannot influence it.
				if !strings.HasPrefix(suffix, ":") || !validPathLiteral(suffix[1:]) {
					return fmt.Errorf("invalid service path action suffix")
				}
			} else if end != len(rest)-1 {
				return fmt.Errorf("invalid service path parameter")
			}
			continue
		}
		// Fixed provider action suffixes are policy literals, never caller
		// supplied paths. Preserve exact matching and forbid multiple verbs.
		if strings.Contains(segment, ":") {
			parts := strings.Split(segment, ":")
			if len(parts) != 2 || !validPathLiteral(parts[0]) || !validPathLiteral(parts[1]) {
				return fmt.Errorf("invalid fixed service path action suffix")
			}
			continue
		}
		for _, r := range segment {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.~", r)) {
				return fmt.Errorf("service path contains unsupported character")
			}
		}
	}
	return nil
}

// validPathLiteral reports whether a fixed path segment (or action-suffix
// verb) uses only characters that survive URL paths unescaped.
func validPathLiteral(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.~", r)) {
			return false
		}
	}
	return true
}

func ValidateURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil {
		return nil, fmt.Errorf("invalid URL")
	}
	if parsed.Scheme != "https" || parsed.User != nil || parsed.Host == "" || parsed.Opaque != "" || parsed.Fragment != "" || strings.Contains(parsed.Host, "%") {
		return nil, fmt.Errorf("URL must be credential-free HTTPS without a fragment")
	}
	if parsed.Port() != "" && parsed.Port() != "443" {
		return nil, fmt.Errorf("HTTPS URL port must be 443")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "" || strings.ContainsAny(host, " \t\r\n\x00") {
		return nil, fmt.Errorf("invalid URL host")
	}
	if addr := net.ParseIP(host); addr == nil && !validDNSName(host) {
		return nil, fmt.Errorf("invalid URL host")
	}
	parsed.Host = host
	if strings.Contains(host, ":") {
		parsed.Host = "[" + host + "]"
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed, nil
}

func OriginOf(parsed *url.URL) string {
	if parsed == nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return "https://" + host
}

func validDNSName(host string) bool {
	if len(host) > 253 || host == "" {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

func validToken(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || (i > 0 && strings.ContainsRune("._:-", r))) {
			return false
		}
	}
	return true
}

func allowedMethod(method string) bool {
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE":
		return true
	default:
		return false
	}
}

func validateOrigins(origins []string) error {
	seen := map[string]struct{}{}
	for _, origin := range origins {
		if err := ValidateOrigin(origin); err != nil {
			return err
		}
		if _, exists := seen[origin]; exists {
			return fmt.Errorf("duplicate origin %q", origin)
		}
		seen[origin] = struct{}{}
	}
	return nil
}

func validateLoopbackOrigins(origins []string) error {
	seen := map[string]struct{}{}
	for _, origin := range origins {
		if err := ValidateLoopbackOrigin(origin); err != nil {
			return err
		}
		if _, exists := seen[origin]; exists {
			return fmt.Errorf("duplicate origin %q", origin)
		}
		seen[origin] = struct{}{}
	}
	return nil
}

func canonicalOrigins(origins []string) []string {
	out := append([]string(nil), origins...)
	for i, origin := range out {
		if normalized, err := CanonicalOrigin(origin); err == nil {
			out[i] = normalized
		}
	}
	sort.Strings(out)
	return out
}

func canonicalLoopbackOrigins(origins []string) []string {
	out := append([]string(nil), origins...)
	for i, origin := range out {
		if normalized, err := CanonicalLoopbackOrigin(origin); err == nil {
			out[i] = normalized
		}
	}
	sort.Strings(out)
	return out
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func Port(parsed *url.URL) string {
	if parsed == nil || parsed.Port() == "" {
		return strconv.Itoa(443)
	}
	return parsed.Port()
}
