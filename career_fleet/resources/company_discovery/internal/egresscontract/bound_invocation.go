package egresscontract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type RuntimeMaterialPermission struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256"`
}
type RuntimeMaterialBinding struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}
type InvocationSelection struct {
	InputDigest           string `json:"input_digest,omitempty"`
	ConfigurationDigest   string `json:"configuration_digest"`
	BindingDigest         string `json:"binding_digest"`
	RuntimeMaterialDigest string `json:"runtime_material_digest"`
}

// These are adapter observations of transport, not assertions of resource
// authorization, semantic quality, or remote terminal completion.
type OperationObservation struct {
	InputDigest      string `json:"input_digest,omitempty"`
	RequestDigest    string `json:"request_digest"`
	ResponseDigest   string `json:"response_digest"`
	ResourceIdentity string `json:"resource_identity,omitempty"`
	Origin           string `json:"origin"`
	Status           int    `json:"status"`
	Error            string `json:"error,omitempty"`
}

func ObservationDigest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func ConfigurationSelectionDigest(configuration *string) (string, error) {
	// Absence differs from an explicit JSON null configuration. Digest exact
	// typed JSON values after decoding so whitespace does not choose behavior.
	if configuration == nil {
		return ObservationDigest(struct {
			Present bool `json:"present"`
		}{false})
	}
	var value any
	if err := atomicDecode([]byte(*configuration), new(any)); err != nil {
		return "", err
	}
	decoder := json.NewDecoder(strings.NewReader(*configuration))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", fmt.Errorf("configuration contains trailing JSON")
	}
	return ObservationDigest(struct {
		Present bool `json:"present"`
		Value   any  `json:"value"`
	}{true, value})
}

func validateRuntimeMaterialBinding(policy Policy, binding Binding) error {
	if binding.Operation == "" && len(policy.Operations) == 1 {
		binding.Operation = policy.Operations[0].ID
	}
	var selected *Operation
	for i := range policy.Operations {
		if policy.Operations[i].ID == binding.Operation {
			selected = &policy.Operations[i]
		}
	}
	if selected == nil {
		if len(binding.RuntimeMaterial) > 0 {
			return fmt.Errorf("runtime material selects unknown operation")
		}
		return nil
	}
	permissions := map[string]RuntimeMaterialPermission{}
	for _, p := range selected.RuntimeMaterial {
		if !validToken(p.ID) || !validInferenceSHA256(p.SHA256) || (p.Kind != "executable" && p.Kind != "library" && p.Kind != "asset") || permissions[p.ID].ID != "" {
			return fmt.Errorf("invalid runtime material permission")
		}
		permissions[p.ID] = p
	}
	seen := map[string]bool{}
	for _, b := range binding.RuntimeMaterial {
		p, ok := permissions[b.ID]
		if !ok || seen[b.ID] || !filepath.IsAbs(b.Path) || filepath.Clean(b.Path) != b.Path {
			return fmt.Errorf("runtime material binding mismatch")
		}
		seen[b.ID] = true
		info, err := os.Lstat(b.Path)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("runtime material unavailable or indirect")
		}
		if p.Kind == "executable" && info.Mode().Perm()&0111 == 0 {
			return fmt.Errorf("runtime material is not executable")
		}
		raw, err := os.ReadFile(b.Path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != p.SHA256 {
			return fmt.Errorf("runtime material changed: %s", b.ID)
		}
	}
	if len(seen) != len(permissions) {
		return fmt.Errorf("runtime material closure incomplete")
	}
	return nil
}

func validateRuntimeMaterialPermissions(material []RuntimeMaterialPermission) error {
	seen := map[string]bool{}
	for _, p := range material {
		if !validToken(p.ID) || seen[p.ID] || !validInferenceSHA256(p.SHA256) || (p.Kind != "executable" && p.Kind != "library" && p.Kind != "asset") {
			return fmt.Errorf("invalid runtime material permission")
		}
		seen[p.ID] = true
	}
	return nil
}

// StartBound is the shared printed/proof dispatch boundary, not a new runtime.
// Unsupported adapters refuse before credentials reach Start. Every call
// rechecks declared local material and records its exact request/result identity.
func StartBound(ctx context.Context, adapter TargetAdapter, inv Invocation) (InvocationHandle, error) {
	if adapter == nil || ctx == nil {
		return nil, fmt.Errorf("execution boundary absent")
	}
	// Freeze caller-owned slices/maps before the adapter or later invocations
	// use them. Mutating a deployment value cannot rewrite the selected policy.
	policyRaw, err := json.Marshal(inv.Policy)
	if err != nil {
		return nil, err
	}
	var policy Policy
	if err = json.Unmarshal(policyRaw, &policy); err != nil {
		return nil, err
	}
	inv.Policy = policy
	bindingRaw, err := json.Marshal(inv.Binding)
	if err != nil {
		return nil, err
	}
	var selectedBinding Binding
	if err = json.Unmarshal(bindingRaw, &selectedBinding); err != nil {
		return nil, err
	}
	inv.Binding = selectedBinding
	if err := adapter.Supports(inv.Policy); err != nil {
		return nil, err
	}
	binding, err := ValidateBinding(inv.Policy, inv.Binding)
	if err != nil {
		return nil, err
	}
	inv.Binding = binding
	if inv.Identity.ConfigurationDigest == "" {
		inv.Identity.ConfigurationDigest, _ = ConfigurationSelectionDigest(nil)
	}
	if !validInferenceSHA256(inv.Identity.ConfigurationDigest) {
		return nil, fmt.Errorf("configuration selection identity invalid")
	}
	if inv.Identity.InputDigest != "" && !validInferenceSHA256(inv.Identity.InputDigest) {
		return nil, fmt.Errorf("input selection identity invalid")
	}
	bindingDigest, err := ObservationDigest(binding)
	if err != nil {
		return nil, err
	}
	var material []RuntimeMaterialPermission
	for _, op := range inv.Policy.Operations {
		if op.ID == binding.Operation {
			material = append(material, op.RuntimeMaterial...)
		}
	}
	sort.Slice(material, func(i, j int) bool { return material[i].ID < material[j].ID })
	materialDigest, err := ObservationDigest(material)
	if err != nil {
		return nil, err
	}
	selected, cleanup, err := snapshotRuntimeMaterial(inv.Policy, inv.Binding)
	if err != nil {
		return nil, err
	}
	adapterInvocation := inv
	adapterInvocation.Binding = selected
	h, err := adapter.Start(ctx, adapterInvocation)
	if err != nil {
		cleanup()
		return nil, err
	}
	stopCleanup := context.AfterFunc(ctx, cleanup)
	return &boundInvocation{inner: h, inv: inv, selected: selected, cleanup: func() { stopCleanup(); cleanup() }, selection: InvocationSelection{InputDigest: inv.Identity.InputDigest, ConfigurationDigest: inv.Identity.ConfigurationDigest, BindingDigest: bindingDigest, RuntimeMaterialDigest: materialDigest}}, nil
}

type boundInvocation struct {
	mu        sync.Mutex
	inner     InvocationHandle
	inv       Invocation
	selection InvocationSelection
	calls     []OperationObservation
	selected  Binding
	cleanup   func()
}

func (h *boundInvocation) Cancel() error           { err := h.inner.Cancel(); h.cleanup(); return err }
func (h *boundInvocation) InvocationRunID() string { return h.inv.Identity.RunID }

// BindInputs is called by the Process before an activation. It is not exposed
// through the Node's typed operation client.
func (h *boundInvocation) BindInputs(digest string) error {
	if !validInferenceSHA256(digest) {
		return fmt.Errorf("input selection identity invalid")
	}
	h.mu.Lock()
	h.selection.InputDigest = digest
	h.mu.Unlock()
	return nil
}
func (h *boundInvocation) Receipt() Receipt {
	r := h.inner.Receipt()
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.selection
	r.Identity.InputDigest = s.InputDigest
	r.Selection = &s
	r.Calls = append([]OperationObservation(nil), h.calls...)
	return r
}
func (h *boundInvocation) record(request any, response OperationResponse, err error) {
	a, _ := ObservationDigest(request)
	b, _ := ObservationDigest(response)
	observation := OperationObservation{RequestDigest: a, ResponseDigest: b, ResourceIdentity: response.ResourceIdentity, Origin: "adapter_observed", Status: response.StatusCode}
	if err != nil {
		observation.Error = err.Error()
	}
	h.mu.Lock()
	observation.InputDigest = h.selection.InputDigest
	h.calls = append(h.calls, observation)
	h.mu.Unlock()
}
func (h *boundInvocation) validateMaterial() error {
	if err := validateRuntimeMaterialBinding(h.inv.Policy, h.inv.Binding); err != nil {
		return err
	}
	return validateRuntimeMaterialBinding(h.inv.Policy, h.selected)
}
func (h *boundInvocation) Do(ctx context.Context, request OperationRequest) (OperationResponse, error) {
	if err := h.validateMaterial(); err != nil {
		h.record(request, OperationResponse{}, err)
		return OperationResponse{}, err
	}
	client, ok := h.inner.(Client)
	if !ok {
		return OperationResponse{}, fmt.Errorf("selected adapter has no typed client")
	}
	response, err := client.Do(ctx, request)
	h.record(request, response, err)
	return response, err
}
func (h *boundInvocation) Stream(ctx context.Context, request OperationRequest, handler EventStreamHandler) (OperationResponse, error) {
	if err := h.validateMaterial(); err != nil {
		return OperationResponse{}, err
	}
	client, ok := h.inner.(EventStreamClient)
	if !ok {
		return OperationResponse{}, fmt.Errorf("selected adapter has no stream client")
	}
	response, err := client.Stream(ctx, request, handler)
	h.record(request, response, err)
	return response, err
}
func (h *boundInvocation) Invoke(ctx context.Context, raw []byte) (Result, error) {
	if err := h.validateMaterial(); err != nil {
		return Result{Receipt: h.Receipt()}, err
	}
	result, err := h.inner.Invoke(ctx, raw)
	h.record(json.RawMessage(raw), OperationResponse{Body: result.Response}, err)
	result.Receipt = h.Receipt()
	return result, err
}
func (h *boundInvocation) Accounting() Accounting { return ReceiptAccounting(h.Receipt()) }

func (h *boundInvocation) InvokeStream(ctx context.Context, raw []byte, handler EventStreamHandler) (Result, error) {
	if err := h.validateMaterial(); err != nil {
		return Result{Receipt: h.Receipt()}, err
	}
	inner, ok := h.inner.(EventStreamInvocationHandle)
	if !ok {
		return Result{}, fmt.Errorf("selected adapter has no streaming invocation")
	}
	result, err := inner.InvokeStream(ctx, raw, handler)
	h.record(json.RawMessage(raw), OperationResponse{Body: result.Response}, err)
	result.Receipt = h.Receipt()
	return result, err
}
