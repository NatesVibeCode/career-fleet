package egresscontract

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

// InferClient is the only inference capability given to task code. It has no
// model selector, URL, credential resolver, headers, retries or token controls.
type InferClient interface {
	Infer(context.Context, InferRequest) (InferResponse, error)
}

type InferRequest struct {
	Prompt       string          `json:"prompt"`
	Input        json.RawMessage `json:"input,omitempty"`
	OutputSchema map[string]any  `json:"output_schema,omitempty"`
	// RequestProfile is set only by ContractInferClient from the exact Infer
	// Node definition. Task code cannot select a provider wire profile itself.
	RequestProfile string `json:"request_profile,omitempty"`
	// Corpus is the sealed text a citation must quote. It is local authority
	// and is not sent to the provider.
	Corpus *CorpusPin `json:"corpus,omitempty"`
}

type InferResponse struct {
	FinishReason   string                    `json:"finish_reason,omitempty"`
	Provider       string                    `json:"provider,omitempty"`
	ProviderID     string                    `json:"provider_id,omitempty"`
	Text           string                    `json:"text,omitempty"`
	Output         any                       `json:"output,omitempty"`
	Decisions      map[string]DecisionAnswer `json:"decisions,omitempty"`
	RequestedModel string                    `json:"requested_model"`
	Model          string                    `json:"model"`
	Usage          json.RawMessage           `json:"usage,omitempty"`
	// Fallback labels a result served by a substitute binding after an
	// availability failure; WinningBinding names the exact binding that
	// produced it (id@digest). A substitute is never plain success.
	Fallback       bool   `json:"fallback"`
	WinningBinding string `json:"winning_binding,omitempty"`
}

// DecisionAnswer keeps the calibrated typed-decision data alongside the
// schema-constrained Output map. Flows can branch on Output without losing
// confidence and probabilities needed for receipts and tuning.
type DecisionAnswer struct {
	Type           string             `json:"type"`
	Choice         string             `json:"choice"`
	Confidence     float64            `json:"confidence"`
	ActProbability float64            `json:"act_probability"`
	Probabilities  map[string]float64 `json:"probabilities,omitempty"`
}

// ModelBinding is process execution configuration, never an Node definition.
// Protocol is an API encoding, not a provider-specific node kind.
type ModelBinding struct {
	MaxOutputTokens  int    `json:"max_output_tokens,omitempty"`
	ID               string `json:"id"`
	Model            string `json:"model"`
	ModelSHA256      string `json:"model_sha256,omitempty"`
	RuntimeID        string `json:"runtime_id,omitempty"`
	RuntimeRevision  string `json:"runtime_revision,omitempty"`
	RuntimeSHA256    string `json:"runtime_sha256,omitempty"`
	Transport        string `json:"transport,omitempty"`
	Protocol         string `json:"protocol"`
	Origin           string `json:"origin"`
	Path             string `json:"path"`
	CredentialRef    string `json:"credential_ref"`
	TimeoutMillis    int64  `json:"timeout_ms"`
	MaxRequestBytes  int    `json:"max_request_bytes"`
	MaxResponseBytes int    `json:"max_response_bytes"`
}

type InferencePolicy struct {
	Schema                string         `json:"schema"`
	Bindings              []ModelBinding `json:"bindings"`
	ShadowBindings        []ModelBinding `json:"shadow_bindings,omitempty"`
	FallbackOnUnavailable bool           `json:"fallback_on_unavailable"`
	// MaxAttempts is required and seals the parent attempt cap: one invocation
	// never transmits to more active bindings than this. The remaining trusted
	// tools refuse at the cap, so a long ladder cannot exceed this policy.
	MaxAttempts int `json:"max_attempts,omitempty"`
	// EgressDigest cross-binds this policy to the exact sealed `egress`
	// dimension of its Infer node spec. Print refuses a policy whose digest
	// does not match the spec it will ship with.
	EgressDigest string `json:"egress_digest,omitempty"`
	// BundleDigest names the execution runtime this marker artifact runs under:
	// the digest of the exact embedded egress bundle sources the print ships.
	BundleDigest string `json:"bundle_digest,omitempty"`
}

const InferencePolicySchema = "praxis.inference_policy.v1"
const ProtocolCompletion = "chat.completions"
const ProtocolChoices = "decisions.choice"
const ProtocolGoogleSearch = "google.search_grounding"
const TransportHTTPS = "https"
const TransportLoopbackLocal = "loopback.local"
const InferenceRequestDigestSchema = "praxis.inference_request_digest.v1"

// Request profiles select closed, definition-pinned request encodings for
// semantic edges whose provider wire contract differs from ordinary JSON
// completion. They are not a provider passthrough surface.
const RequestProfileJSONCompletion = "completion.json.v1"
const RequestProfileScaleDownSummarize = "openrouter.scaledown.summarize.v1"
const RequestProfileScaleDownExtract = "openrouter.scaledown.extract.v1"

func (b ModelBinding) operationPolicy() Policy {
	mode := ModeHTTPS
	credentials := []Credential{{Ref: b.CredentialRef, Origins: []string{b.Origin}}}
	if b.Protocol == ProtocolGoogleSearch {
		credentials[0].Header = "x-goog-api-key"
	}
	if b.transport() == TransportLoopbackLocal {
		mode = ModeLoopbackHTTP
		credentials = nil
	}
	return Policy{Schema: SchemaV1, Mode: mode, Operations: []Operation{{ID: b.ID, Method: "POST", Destination: Destination{Kind: DestinationService, Origins: []string{b.Origin}, Path: b.Path}, Credentials: credentials, MaxRequests: 1, MaxRequestBytes: b.MaxRequestBytes, MaxResponseBytes: b.MaxResponseBytes, TimeoutMillis: b.TimeoutMillis, RedirectPolicy: RedirectNone}}}
}

func (b ModelBinding) transport() string {
	if b.Transport == "" {
		return TransportHTTPS
	}
	return b.Transport
}

// Reference is the sealed binding identity: id@digest over the exact binding
// document. A changed binding is a new reference; a name never denotes two
// different binding revisions in a receipt.
func (b ModelBinding) Reference() string {
	raw, err := json.Marshal(b)
	if err != nil {
		return b.ID
	}
	return b.ID + "@" + inferenceDigest("praxis.inference_binding.v1", raw)
}

// availabilityFallbackStatus is the exact fallback trigger set. Only a
// provider-reported availability failure (404, 429, or 5xx) may continue to a
// later active binding, and only while fallback_on_unavailable is declared and
// the sealed MaxAttempts cap has room. Transport failure, credential refusal,
// any other provider refusal, a result that fails its contract or identity, and
// an unusable task all terminate the invocation; they never fall through.
func availabilityFallbackStatus(status int) bool {
	return status == 404 || status == 429 || (status >= 500 && status <= 599)
}

// effectiveMaxAttempts is the required sealed parent cap.
func (p InferencePolicy) effectiveMaxAttempts() int { return p.MaxAttempts }

func (p InferencePolicy) Validate() error {
	if p.Schema != InferencePolicySchema || len(p.Bindings) == 0 {
		return fmt.Errorf("invalid inference policy")
	}
	seen := map[string]bool{}
	for _, b := range append(append([]ModelBinding(nil), p.Bindings...), p.ShadowBindings...) {
		if !validToken(b.ID) || seen[b.ID] || strings.TrimSpace(b.Model) == "" {
			return fmt.Errorf("invalid inference binding")
		}
		identityFields := []string{b.ModelSHA256, b.RuntimeID, b.RuntimeRevision, b.RuntimeSHA256}
		identityCount := 0
		for _, value := range identityFields {
			if value != strings.TrimSpace(value) {
				return fmt.Errorf("inference model/runtime identity must not contain surrounding whitespace")
			}
			if value != "" {
				identityCount++
			}
		}
		if identityCount != 0 && identityCount != len(identityFields) {
			return fmt.Errorf("inference model and runtime identity must be supplied together")
		}
		if identityCount == len(identityFields) && (!validInferenceSHA256(b.ModelSHA256) || !validInferenceSHA256(b.RuntimeSHA256)) {
			return fmt.Errorf("inference model and runtime identity require SHA-256 digests")
		}
		if b.Protocol != ProtocolCompletion && b.Protocol != ProtocolChoices && b.Protocol != ProtocolGoogleSearch {
			return fmt.Errorf("unsupported inference protocol")
		}
		if b.Protocol == ProtocolGoogleSearch {
			if b.Model != "gemini-2.5-flash-lite" && b.Model != "gemini-2.5-flash" {
				return fmt.Errorf("Google search grounding protocol requires a supported non-thinking Gemini 2.5 binding")
			}
			if b.transport() != TransportHTTPS || b.Origin != "https://generativelanguage.googleapis.com" || !validToken(b.Model) || b.Path != "/v1beta/models/"+b.Model+":generateContent" || b.MaxOutputTokens < 1 || b.MaxOutputTokens > 8192 {
				return fmt.Errorf("invalid Google search grounding binding")
			}
		} else if b.Protocol == ProtocolCompletion {
			if b.MaxOutputTokens < 0 || b.MaxOutputTokens > 131072 {
				return fmt.Errorf("invalid completion output token bound")
			}
		} else if b.MaxOutputTokens != 0 {
			return fmt.Errorf("token control requires a completion or Google search grounding protocol")
		}
		switch b.transport() {
		case TransportHTTPS:
			if b.CredentialRef == "" {
				return fmt.Errorf("HTTPS inference binding requires a credential reference")
			}
		case TransportLoopbackLocal:
			if b.Protocol != ProtocolChoices || b.CredentialRef != "" || b.Path != "/api/decide" || !validToken(b.Model) {
				return fmt.Errorf("invalid local loopback decision binding")
			}
		default:
			return fmt.Errorf("unsupported inference transport")
		}
		if err := b.operationPolicy().Validate(); err != nil {
			return err
		}
		seen[b.ID] = true
	}
	if p.MaxAttempts < 1 || p.MaxAttempts > len(p.Bindings) {
		return fmt.Errorf("inference attempt cap must be between 1 and the active binding count")
	}
	for name, value := range map[string]string{"egress_digest": p.EgressDigest, "bundle_digest": p.BundleDigest} {
		if value != "" && !validInferenceSHA256(value) {
			return fmt.Errorf("inference policy %s must be a lowercase SHA-256 digest", name)
		}
	}
	// Shadows only observe. They carry no provider-side budget of their own, so
	// they may not open network egress: a shadow must run under loopback custody.
	// Loopback bindings also cannot carry credentials, so credentialed shadows
	// are refused with them.
	for _, b := range p.ShadowBindings {
		if b.transport() != TransportLoopbackLocal {
			return fmt.Errorf("shadow inference bindings require loopback custody; network shadows are refused")
		}
	}
	return nil
}

func (p InferencePolicy) Digest() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(append([]byte(InferencePolicySchema+"\x00"), b...))
	return hex.EncodeToString(h[:]), nil
}

type InferenceAttempt struct {
	Diagnostic                *FailureDiagnostic `json:"diagnostic,omitempty"`
	FinishReason              string             `json:"finish_reason,omitempty"`
	Provider                  string             `json:"provider,omitempty"`
	ProviderID                string             `json:"provider_id,omitempty"`
	Role                      string             `json:"role"`
	DecisionID                string             `json:"decision_id"`
	DigestSchema              string             `json:"digest_schema"`
	PromptDigest              string             `json:"prompt_digest"`
	InputDigest               string             `json:"input_digest"`
	OutputSchemaDigest        string             `json:"output_schema_digest"`
	RequestDigest             string             `json:"request_digest"`
	ConfiguredModelSHA256     string             `json:"configured_model_sha256,omitempty"`
	ConfiguredRuntimeID       string             `json:"configured_runtime_id,omitempty"`
	ConfiguredRuntimeRevision string             `json:"configured_runtime_revision,omitempty"`
	ConfiguredRuntimeSHA256   string             `json:"configured_runtime_sha256,omitempty"`
	Binding                   string             `json:"binding"`
	RequestedModel            string             `json:"requested_model"`
	Model                     string             `json:"model,omitempty"`
	// Fallback labels an attempt reached through the availability trigger set;
	// the winner (the attempt that produced the accepted result) also carries
	// its own Reference() as WinningBinding, copied into the parent receipt.
	Fallback       bool                      `json:"fallback"`
	WinningBinding string                    `json:"winning_binding,omitempty"`
	Status         int                       `json:"status,omitempty"`
	Usage          json.RawMessage           `json:"usage,omitempty"`
	Decisions      map[string]DecisionAnswer `json:"decisions,omitempty"`
	Error          string                    `json:"error,omitempty"`
	Transport      Receipt                   `json:"transport"`
}

func validInferenceSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

type inferenceRequestDigests struct {
	PromptDigest       string
	InputDigest        string
	OutputSchemaDigest string
	RequestDigest      string
}

func inferenceDigest(domain string, value []byte) string {
	h := sha256.Sum256(append([]byte(domain+"\x00"), value...))
	return hex.EncodeToString(h[:])
}

func normalizedRequestProfile(profile string) string {
	if profile == "" {
		return RequestProfileJSONCompletion
	}
	return profile
}

func digestInferenceRequest(req InferRequest) (inferenceRequestDigests, error) {
	var result inferenceRequestDigests
	input := req.Input
	if len(input) == 0 {
		input = json.RawMessage("null")
	}
	schema, err := json.Marshal(req.OutputSchema)
	if err != nil {
		return result, fmt.Errorf("encode inference output schema for receipt")
	}
	result.PromptDigest = inferenceDigest("praxis.inference_prompt.v1", []byte(req.Prompt))
	result.InputDigest = inferenceDigest("praxis.inference_input.v1", input)
	result.OutputSchemaDigest = inferenceDigest("praxis.inference_output_schema.v1", schema)
	corpusDigest := ""
	if req.Corpus != nil {
		corpusDigest = req.Corpus.Digest
	}
	identity, err := json.Marshal(struct {
		Schema             string
		RequestProfile     string
		PromptDigest       string
		InputDigest        string
		OutputSchemaDigest string
		CorpusDigest       string `json:",omitempty"`
	}{InferenceRequestDigestSchema, normalizedRequestProfile(req.RequestProfile), result.PromptDigest, result.InputDigest, result.OutputSchemaDigest, corpusDigest})
	if err != nil {
		return inferenceRequestDigests{}, fmt.Errorf("encode inference request digest identity")
	}
	result.RequestDigest = inferenceDigest(InferenceRequestDigestSchema, identity)
	return result, nil
}

func inferenceDecisionID(identity Identity, binding, requestDigest string) string {
	raw, _ := json.Marshal(struct {
		Schema        string
		Identity      Identity
		Binding       string
		RequestDigest string
	}{"praxis.inference_decision_id.v1", identity, binding, requestDigest})
	return inferenceDigest("praxis.inference_decision_id.v1", raw)
}

// StartInference retains one logical node identity and a receipt for every
// attempted binding. Only the process-owned policy permits availability
// fallback, and only for the trigger set in availabilityFallbackStatus
// (provider-reported 404, 429, or 5xx) within the sealed MaxAttempts cap.
// Transport failure, credential refusal, other provider refusals, a result
// that fails its contract or model identity, and an unusable task terminate
// this invocation; the fallback is never extended to credential refusal.
// There is no agent loop.
func StartInference(ctx context.Context, id Identity, p InferencePolicy, selection []string, credentials CredentialProvider) (InvocationHandle, error) {
	return startInferenceWithSelector(ctx, id, p, selection, credentials, func(binding ModelBinding) TargetAdapter {
		if binding.transport() == TransportLoopbackLocal {
			return NativeLoopbackHTTP{}
		}
		return NativeHTTPS{}
	})
}

func startInference(ctx context.Context, id Identity, p InferencePolicy, selection []string, credentials CredentialProvider, transport TargetAdapter) (InvocationHandle, error) {
	return startInferenceWithSelector(ctx, id, p, selection, credentials, func(ModelBinding) TargetAdapter { return transport })
}

func startInferenceWithSelector(ctx context.Context, id Identity, p InferencePolicy, selection []string, credentials CredentialProvider, selectTransport func(ModelBinding) TargetAdapter) (InvocationHandle, error) {
	digest, err := p.Digest()
	if err != nil {
		return nil, err
	}
	if id.RunID == "" || id.NodeKey == "" || id.ProcessDigest == "" || id.NodeName == "" || id.NodeDigest == "" || id.NodeSpecDigest == "" || id.PolicyDigest != digest || id.Operation != "infer" || selectTransport == nil {
		return nil, fmt.Errorf("invalid inference invocation authority")
	}
	// Detach caller-owned configuration before preparing any operation.
	p.Bindings = append([]ModelBinding(nil), p.Bindings...)
	p.ShadowBindings = append([]ModelBinding(nil), p.ShadowBindings...)
	if selection != nil {
		if len(selection) == 0 {
			return nil, fmt.Errorf("empty inference selection")
		}
		selected := []ModelBinding{}
		seen := map[string]bool{}
		for _, name := range selection {
			found := false
			for _, b := range p.Bindings {
				if b.ID == name && !seen[name] {
					selected = append(selected, b)
					seen[name] = true
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("inference selection exceeds sealed authority")
			}
		}
		p.Bindings = selected
	}
	for _, b := range p.Bindings {
		if b.CredentialRef != "" && credentials == nil {
			return nil, fmt.Errorf("credential provider absent")
		}
	}
	for _, b := range p.ShadowBindings {
		if b.CredentialRef != "" && credentials == nil {
			return nil, fmt.Errorf("shadow inference credential provider absent")
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	h := &inferenceHandle{ctx: ctx, cancel: cancel, policy: p, receipt: Receipt{Identity: id, PolicyDigest: digest, Operation: "infer", Adapter: "native.inference.v1", Outcome: "unused", Transmission: "not_sent"}}
	for _, b := range p.Bindings {
		childPolicy := b.operationPolicy()
		childDigest, _ := childPolicy.Digest()
		childID := id
		childID.PolicyDigest = childDigest
		childID.Operation = b.ID
		parentID := id
		parentID.Operation = b.ID
		transport := selectTransport(b)
		if transport == nil {
			h.Cancel()
			return nil, fmt.Errorf("inference transport unavailable")
		}
		child, err := transport.Start(ctx, Invocation{Identity: childID, Policy: childPolicy, Binding: Binding{Operation: b.ID, CredentialRef: b.CredentialRef}, Credentials: inferenceCredentials{parent: parentID, child: childID, ref: b.CredentialRef, provider: credentials}})
		if err != nil {
			h.Cancel()
			return nil, err
		}
		h.children = append(h.children, child)
	}
	for _, b := range p.ShadowBindings {
		childPolicy := b.operationPolicy()
		childDigest, _ := childPolicy.Digest()
		childID := id
		childID.PolicyDigest = childDigest
		childID.Operation = b.ID
		parentID := id
		parentID.Operation = b.ID
		transport := selectTransport(b)
		if transport == nil {
			h.Cancel()
			return nil, fmt.Errorf("shadow inference transport unavailable")
		}
		child, err := transport.Start(ctx, Invocation{Identity: childID, Policy: childPolicy, Binding: Binding{Operation: b.ID, CredentialRef: b.CredentialRef}, Credentials: inferenceCredentials{parent: parentID, child: childID, ref: b.CredentialRef, provider: credentials}})
		if err != nil {
			h.Cancel()
			return nil, err
		}
		h.shadowChildren = append(h.shadowChildren, child)
	}
	return h, nil
}

// The HTTPS child may resolve only its own parent node/binding grant. This
// bridge does not change or broaden GrantedCredentials matching rules.
type inferenceCredentials struct {
	parent, child Identity
	ref           string
	provider      CredentialProvider
}

func (c inferenceCredentials) Resolve(ctx context.Context, id Identity, ref string) ([]byte, error) {
	if id != c.child || ref != c.ref {
		return nil, fmt.Errorf("inference credential scope mismatch")
	}
	return c.provider.Resolve(ctx, c.parent, ref)
}

type inferenceHandle struct {
	mu             sync.Mutex
	ctx            context.Context
	cancel         context.CancelFunc
	policy         InferencePolicy
	children       []InvocationHandle
	shadowChildren []InvocationHandle
	receipt        Receipt
	used           bool
}

func (h *inferenceHandle) Cancel() error {
	h.cancel()
	for _, c := range h.children {
		c.Cancel()
	}
	for _, c := range h.shadowChildren {
		c.Cancel()
	}
	return nil
}
func (h *inferenceHandle) Receipt() Receipt {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.receipt
	r.InferenceAttempts = append([]InferenceAttempt(nil), r.InferenceAttempts...)
	for i := range r.InferenceAttempts {
		r.InferenceAttempts[i].Diagnostic = cloneFailureDiagnostic(r.InferenceAttempts[i].Diagnostic)
		r.InferenceAttempts[i].Transport.FailureDiagnostic = cloneFailureDiagnostic(r.InferenceAttempts[i].Transport.FailureDiagnostic)
		r.InferenceAttempts[i].Usage = append(json.RawMessage(nil), r.InferenceAttempts[i].Usage...)
		r.InferenceAttempts[i].Decisions = cloneDecisionAnswers(r.InferenceAttempts[i].Decisions)
	}
	return r
}
func (h *inferenceHandle) Invoke(ctx context.Context, raw []byte) (Result, error) {
	var req InferRequest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	d.UseNumber()
	if d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF {
		return Result{}, fmt.Errorf("invalid inference envelope")
	}
	out, err := h.Infer(ctx, req)
	b, _ := json.Marshal(out)
	return Result{Response: b, Receipt: h.Receipt()}, err
}
func (h *inferenceHandle) Infer(ctx context.Context, req InferRequest) (InferResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fail := func(s string) (InferResponse, error) {
		h.receipt.Outcome = "refused"
		return InferResponse{}, fmt.Errorf("%s", s)
	}
	if h.used || h.ctx.Err() != nil || ctx.Err() != nil {
		return fail("inference invocation used or expired")
	}
	if req.Corpus != nil {
		if err := VerifyCorpusPin(*req.Corpus); err != nil {
			return fail(err.Error())
		}
	}
	h.used = true
	// Validate and encode every active route before transmission. An active route
	// that cannot express the task is refused, not silently weakened or skipped.
	bodies := make([][]byte, len(h.policy.Bindings))
	for i, b := range h.policy.Bindings {
		body, err := encodeInference(b, req)
		if err != nil {
			return fail(fmt.Sprintf("binding %s cannot express this inference task: %v", b.ID, err))
		}
		bodies[i] = body
	}
	digests, err := digestInferenceRequest(req)
	if err != nil {
		return fail("inference request identity unavailable")
	}
	maxAttempts := h.policy.effectiveMaxAttempts()
	for i, b := range h.policy.Bindings {
		raw, _ := json.Marshal(OperationRequest{Body: bodies[i]})
		result, err := h.children[i].Invoke(ctx, raw)
		transport := h.children[i].Receipt()
		var response OperationResponse
		if json.Unmarshal(result.Response, &response) != nil && err == nil {
			err = fmt.Errorf("invalid transport response")
		}
		attempt := InferenceAttempt{
			Role:                      "active",
			DecisionID:                inferenceDecisionID(h.receipt.Identity, b.ID, digests.RequestDigest),
			DigestSchema:              InferenceRequestDigestSchema,
			PromptDigest:              digests.PromptDigest,
			InputDigest:               digests.InputDigest,
			OutputSchemaDigest:        digests.OutputSchemaDigest,
			RequestDigest:             digests.RequestDigest,
			ConfiguredModelSHA256:     b.ModelSHA256,
			ConfiguredRuntimeID:       b.RuntimeID,
			ConfiguredRuntimeRevision: b.RuntimeRevision,
			ConfiguredRuntimeSHA256:   b.RuntimeSHA256,
			Binding:                   b.ID,
			RequestedModel:            b.Model,
			Fallback:                  i > 0,
			Status:                    response.StatusCode,
			Transport:                 transport,
		}
		h.receipt.Requests += transport.Requests
		h.receipt.Responses += transport.Responses
		if h.receipt.Requests > 0 {
			h.receipt.Transmission = "attempted_unknown"
			if h.receipt.Responses == h.receipt.Requests {
				h.receipt.Transmission = "response_received"
			}
		}
		if err != nil {
			// A transport or credential refusal ends the invocation. The
			// availability fallback is never extended to credential refusal.
			attempt.Error = "transport_or_credential_refused"
			attempt.Diagnostic = cloneFailureDiagnostic(response.Diagnostic)
			if attempt.Diagnostic == nil {
				attempt.Diagnostic = cloneFailureDiagnostic(transport.FailureDiagnostic)
			}
			h.receipt.InferenceAttempts = append(h.receipt.InferenceAttempts, attempt)
			return fail(fmt.Sprintf("binding %s: inference transport or credential refused", b.ID))
		}
		if availabilityFallbackStatus(response.StatusCode) {
			attempt.Error = "provider_unavailable"
			h.receipt.InferenceAttempts = append(h.receipt.InferenceAttempts, attempt)
			if h.policy.FallbackOnUnavailable && i+1 < len(h.children) {
				if i+1 < maxAttempts {
					continue
				}
				return fail(fmt.Sprintf("binding %s: inference unavailable; sealed parent attempt cap %d reached", b.ID, maxAttempts))
			}
			return fail(fmt.Sprintf("binding %s: inference unavailable", b.ID))
		}
		if response.StatusCode < 200 || response.StatusCode > 299 {
			attempt.Error = "provider_refused"
			attempt.Diagnostic = inferenceFailureDiagnostic(bodies[i], response, "provider", fmt.Sprintf("http_%d", response.StatusCode), "provider returned a non-success HTTP status")
			h.receipt.InferenceAttempts = append(h.receipt.InferenceAttempts, attempt)
			return fail(fmt.Sprintf("binding %s: inference provider refused", b.ID))
		}
		out, parseErr := decodeInference(b, req, response.Body)
		attempt.Model = out.Model
		attempt.FinishReason, attempt.Provider, attempt.ProviderID = out.FinishReason, out.Provider, out.ProviderID
		attempt.Usage = append(json.RawMessage(nil), out.Usage...)
		attempt.Decisions = cloneDecisionAnswers(out.Decisions)
		if parseErr != nil {
			attempt.Error = "result_invalid"
			code := "protocol_decode_failed"
			var detail *inferenceDecodeError
			if errors.As(parseErr, &detail) {
				code = detail.code
			}
			attempt.Diagnostic = inferenceFailureDiagnostic(bodies[i], response, "decoder", code, parseErr.Error())
			attempt.Diagnostic.FinishReason, attempt.Diagnostic.Provider = out.FinishReason, out.Provider
			if out.ProviderID != "" {
				attempt.Diagnostic.ProviderID = out.ProviderID
			}
			h.receipt.InferenceAttempts = append(h.receipt.InferenceAttempts, attempt)
			return fail(fmt.Sprintf("binding %s: inference result invalid or incomplete", b.ID))
		}
		if err := BindCorpusCitations(req, out.Output); err != nil {
			attempt.Error = "corpus_pin_refused"
			h.receipt.InferenceAttempts = append(h.receipt.InferenceAttempts, attempt)
			return fail(fmt.Sprintf("binding %s: %v", b.ID, err))
		}
		// The parent attempt copies the winner: the exact binding revision that
		// produced the accepted result, plus its substitute label.
		attempt.WinningBinding = b.Reference()
		h.receipt.InferenceAttempts = append(h.receipt.InferenceAttempts, attempt)
		h.runInferenceShadows(ctx, req, digests, h.policy.ShadowBindings)
		out.Fallback = attempt.Fallback
		out.WinningBinding = attempt.WinningBinding
		h.receipt.Outcome = "succeeded"
		return out, nil
	}
	return fail("inference exhausted")
}

func inferenceFailureDiagnostic(request []byte, response OperationResponse, stage, code, detail string) *FailureDiagnostic {
	d := cloneFailureDiagnostic(response.Diagnostic)
	if d == nil {
		d = responseDiagnostic(request, response.Body, nil, response.StatusCode, 0, "")
	}
	d.Stage, d.Code = stage, code
	d.Detail, _ = diagnosticText([]byte(detail), nil, 1024)
	return d
}

// runInferenceShadows records observational attempts after an active result
// succeeded. Shadow accounting is separate: each attempt carries its own
// transport receipt (requests, responses, transmission, outcome) and never
// adds to the parent's network counters or outcome. Fail-closed holds: a shadow
// error, timeout, refusal, or unusable result is recorded as evidence only and
// can never steer the output, trigger a fallback, or change the parent result.
func (h *inferenceHandle) runInferenceShadows(ctx context.Context, req InferRequest, digests inferenceRequestDigests, bindings []ModelBinding) {
	for i, b := range bindings {
		attempt := InferenceAttempt{
			Role:                      "shadow",
			DecisionID:                inferenceDecisionID(h.receipt.Identity, "shadow:"+b.ID, digests.RequestDigest),
			DigestSchema:              InferenceRequestDigestSchema,
			PromptDigest:              digests.PromptDigest,
			InputDigest:               digests.InputDigest,
			OutputSchemaDigest:        digests.OutputSchemaDigest,
			RequestDigest:             digests.RequestDigest,
			ConfiguredModelSHA256:     b.ModelSHA256,
			ConfiguredRuntimeID:       b.RuntimeID,
			ConfiguredRuntimeRevision: b.RuntimeRevision,
			ConfiguredRuntimeSHA256:   b.RuntimeSHA256,
			Binding:                   b.ID,
			RequestedModel:            b.Model,
		}
		body, encodeErr := encodeInference(b, req)
		if encodeErr != nil {
			attempt.Error = "shadow_request_not_expressible"
			h.receipt.InferenceAttempts = append(h.receipt.InferenceAttempts, attempt)
			continue
		}
		if i >= len(h.shadowChildren) {
			attempt.Error = "shadow_transport_unavailable"
			h.receipt.InferenceAttempts = append(h.receipt.InferenceAttempts, attempt)
			continue
		}
		raw, _ := json.Marshal(OperationRequest{Body: body})
		result, invokeErr := h.shadowChildren[i].Invoke(ctx, raw)
		transport := h.shadowChildren[i].Receipt()
		attempt.Transport = transport
		var response OperationResponse
		if json.Unmarshal(result.Response, &response) != nil && invokeErr == nil {
			invokeErr = fmt.Errorf("invalid shadow transport response")
		}
		attempt.Status = response.StatusCode
		if invokeErr != nil {
			attempt.Error = "shadow_transport_refused"
			attempt.Diagnostic = cloneFailureDiagnostic(response.Diagnostic)
			if attempt.Diagnostic == nil {
				attempt.Diagnostic = cloneFailureDiagnostic(transport.FailureDiagnostic)
			}
			h.receipt.InferenceAttempts = append(h.receipt.InferenceAttempts, attempt)
			continue
		}
		if response.StatusCode < 200 || response.StatusCode > 299 {
			attempt.Error = "shadow_provider_refused"
			attempt.Diagnostic = inferenceFailureDiagnostic(body, response, "provider", fmt.Sprintf("http_%d", response.StatusCode), "shadow provider returned a non-success HTTP status")
			h.receipt.InferenceAttempts = append(h.receipt.InferenceAttempts, attempt)
			continue
		}
		out, parseErr := decodeInference(b, req, response.Body)
		attempt.Model = out.Model
		attempt.FinishReason, attempt.Provider, attempt.ProviderID = out.FinishReason, out.Provider, out.ProviderID
		attempt.Usage = append(json.RawMessage(nil), out.Usage...)
		attempt.Decisions = cloneDecisionAnswers(out.Decisions)
		if parseErr != nil {
			attempt.Error = "shadow_result_invalid"
			code := "protocol_decode_failed"
			var detail *inferenceDecodeError
			if errors.As(parseErr, &detail) {
				code = detail.code
			}
			attempt.Diagnostic = inferenceFailureDiagnostic(body, response, "decoder", code, parseErr.Error())
			attempt.Diagnostic.FinishReason, attempt.Diagnostic.Provider = out.FinishReason, out.Provider
			if out.ProviderID != "" {
				attempt.Diagnostic.ProviderID = out.ProviderID
			}
		}
		h.receipt.InferenceAttempts = append(h.receipt.InferenceAttempts, attempt)
	}
}

func cloneDecisionAnswers(input map[string]DecisionAnswer) map[string]DecisionAnswer {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]DecisionAnswer, len(input))
	for name, answer := range input {
		probabilities := make(map[string]float64, len(answer.Probabilities))
		for choice, probability := range answer.Probabilities {
			probabilities[choice] = probability
		}
		answer.Probabilities = probabilities
		output[name] = answer
	}
	return output
}
