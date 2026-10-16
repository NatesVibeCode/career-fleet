package egresscontract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const ModeNativeExec = "native_exec"

type NativeExecPermission struct {
	ExecutableSHA256 string   `json:"executable_sha256"`
	Arguments        []string `json:"arguments"`
	Network          bool     `json:"network"`
}
type NativeExecBinding struct {
	Executable          string   `json:"executable"`
	WorkingDirectory    string   `json:"working_directory"`
	WritableDirectories []string `json:"writable_directories"`
	ReadOnlyDirectories []string `json:"read_only_directories"`
}
type NativeExecReceipt struct {
	ExecutableSHA256 string `json:"executable_sha256"`
	ArgumentsSHA256  string `json:"arguments_sha256"`
	InputSHA256      string `json:"input_sha256"`
	OutputSHA256     string `json:"output_sha256"`
	BindingSHA256    string `json:"binding_sha256"`
	DiagnosticSHA256 string `json:"diagnostic_sha256"`
	Started          bool   `json:"started"`
	ExitCode         int    `json:"exit_code"`
}

// The fixed executable and arguments are sealed by policy. Body is stdin data;
// callers cannot supply a command, shell expansion, environment or extra argv.
func validateNativeExecOperation(op Operation) error {
	p := op.NativeExec
	if p == nil || !validToken(op.ID) || op.Method != "RUN" || op.Destination.Kind != DestinationFilesystem || op.Destination.Path != "." || len(op.Credentials) != 0 || len(op.Headers) != 0 || len(op.QueryParameters) != 0 || len(op.Destination.Origins) != 0 || op.AtomicFile != nil || op.RecordStore != nil || op.WorkspaceDelivery != nil || op.Filesystem != nil || op.Browser != nil || op.MaxRequests != 1 || op.MaxRequestBytes != 0 || op.MaxResponseBytes != 0 || op.TimeoutMillis != 0 || op.MaxRedirects != 0 || op.RedirectPolicy != RedirectNone || p.Arguments == nil || !validInferenceSHA256(p.ExecutableSHA256) {
		return fmt.Errorf("one exact pinned native invocation required")
	}
	return nil
}
func validateNativeExecBinding(policy Policy, binding Binding) (Binding, error) {
	if binding.NativeExec == nil || len(policy.Operations) != 1 || binding.Directory != "" || binding.Browser != nil || len(binding.Origins) != 0 || len(binding.Routes) != 0 || binding.CredentialRef != "" {
		return Binding{}, fmt.Errorf("native executable binding missing")
	}
	op := policy.Operations[0]
	if binding.Operation != "" && binding.Operation != op.ID {
		return Binding{}, fmt.Errorf("native operation changed")
	}
	v := *binding.NativeExec
	for _, path := range append(append([]string{v.WorkingDirectory}, v.WritableDirectories...), v.ReadOnlyDirectories...) {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return Binding{}, fmt.Errorf("explicit clean native directory required")
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return Binding{}, fmt.Errorf("native directory differs")
		}
	}
	if !filepath.IsAbs(v.Executable) || v.WritableDirectories == nil || v.ReadOnlyDirectories == nil {
		return Binding{}, fmt.Errorf("native binding incomplete")
	}
	info, err := os.Lstat(v.Executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return Binding{}, fmt.Errorf("regular executable required")
	}
	raw, err := os.ReadFile(v.Executable)
	if err != nil || workspaceHash(raw) != op.NativeExec.ExecutableSHA256 {
		return Binding{}, fmt.Errorf("native executable changed")
	}
	for _, arg := range op.NativeExec.Arguments {
		if strings.ContainsRune(arg, 0) {
			return Binding{}, fmt.Errorf("native argument invalid")
		}
	}
	binding.NativeExec = &v
	binding.Operation = op.ID
	return binding, nil
}

type NativeExecutable struct{}

func (NativeExecutable) Name() string { return "native.exec.v1" }
func (NativeExecutable) Supports(p Policy) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("native scoped execution unsupported on this platform")
	}
	if p.Mode != ModeNativeExec {
		return fmt.Errorf("native execution policy required")
	}
	return p.Validate()
}
func (a NativeExecutable) Start(ctx context.Context, inv Invocation) (InvocationHandle, error) {
	if ctx == nil {
		return nil, fmt.Errorf("native context missing")
	}
	if err := a.Supports(inv.Policy); err != nil {
		return nil, err
	}
	binding, err := ValidateBinding(inv.Policy, inv.Binding)
	if err != nil {
		return nil, err
	}
	pin, _ := inv.Policy.Digest()
	if inv.Identity.RunID == "" || inv.Identity.ProcessDigest == "" || inv.Identity.NodeKey == "" || inv.Identity.NodeName == "" || inv.Identity.NodeDigest == "" || inv.Identity.NodeSpecDigest == "" || inv.Identity.PolicyDigest != pin || inv.Identity.Operation != binding.Operation {
		return nil, fmt.Errorf("native invocation identity changed")
	}
	child, cancel := context.WithCancel(ctx)
	args, _ := json.Marshal(inv.Policy.Operations[0].NativeExec.Arguments)
	bindingBytes, _ := json.Marshal(binding.NativeExec)
	return &nativeExecHandle{ctx: child, cancel: cancel, binding: binding, operation: inv.Policy.Operations[0], receipt: Receipt{Identity: inv.Identity, PolicyDigest: pin, Operation: binding.Operation, Adapter: a.Name(), Outcome: "unused", Transmission: "not_sent", NativeExec: &NativeExecReceipt{ExecutableSHA256: inv.Policy.Operations[0].NativeExec.ExecutableSHA256, ArgumentsSHA256: workspaceHash(args), BindingSHA256: workspaceHash(bindingBytes), ExitCode: -1}}}, nil
}

type nativeExecHandle struct {
	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	binding   Binding
	operation Operation
	receipt   Receipt
}

func (h *nativeExecHandle) Cancel() error { h.cancel(); return nil }
func (h *nativeExecHandle) Receipt() Receipt {
	h.mu.Lock()
	defer h.mu.Unlock()
	raw, _ := json.Marshal(h.receipt)
	var copy Receipt
	_ = json.Unmarshal(raw, &copy)
	return copy
}
func (h *nativeExecHandle) Accounting() Accounting { return ReceiptAccounting(h.Receipt()) }
func (h *nativeExecHandle) Invoke(ctx context.Context, raw []byte) (Result, error) {
	var request OperationRequest
	if err := decodeNativeRequest(raw, &request); err != nil {
		return Result{Receipt: h.Receipt()}, err
	}
	response, err := h.Do(ctx, request)
	body, _ := json.Marshal(response)
	return Result{Response: body, Receipt: h.Receipt()}, err
}
func (h *nativeExecHandle) Do(caller context.Context, r OperationRequest) (OperationResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	refuse := func(err error) (OperationResponse, error) {
		h.receipt.DeniedRequests++
		return OperationResponse{}, err
	}
	if caller == nil || caller.Err() != nil || h.ctx.Err() != nil || h.receipt.Requests != 0 || r.Operation != "" && r.Operation != h.operation.ID || r.URL != "" || len(r.QueryParameters) != 0 || len(r.RepeatedQueryParameters) != 0 || len(r.PathParameters) != 0 {
		return refuse(fmt.Errorf("native invocation outside sealed operation"))
	}
	if _, err := validateNativeExecBinding(Policy{Operations: []Operation{h.operation}}, h.binding); err != nil {
		return refuse(err)
	}
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	stop := context.AfterFunc(caller, cancel)
	defer stop()
	if h.operation.NativeExec.Network {
		return refuse(fmt.Errorf("networked native execution requires a separate admitted route transport"))
	}
	// Execute the verified copy rather than reopening the deployment path after
	// its hash check. The existing sandbox grants the copy read-only access.
	selectedExecutable := h.binding.NativeExec.Executable
	if !immutableMaterialPath(selectedExecutable) {
		selectedDir, err := os.MkdirTemp("", "praxis-selected-executable-")
		if err != nil {
			return refuse(err)
		}
		defer os.RemoveAll(selectedDir)
		selectedExecutable = filepath.Join(selectedDir, filepath.Base(h.binding.NativeExec.Executable))
		if err = copySelectedMaterial(h.binding.NativeExec.Executable, selectedExecutable, h.operation.NativeExec.ExecutableSHA256, true); err != nil {
			return refuse(err)
		}
	}
	// Reuse the existing confined consumer launcher; no ambient user-data reads.
	cmd, err := consumerCommand(ctx, WorkspaceProcedure{Executable: selectedExecutable, Arguments: h.operation.NativeExec.Arguments, ReadOnlyPaths: append([]string{selectedExecutable}, h.binding.NativeExec.ReadOnlyDirectories...)}, h.binding.NativeExec.WorkingDirectory, h.binding.NativeExec.WritableDirectories)
	if err != nil {
		return refuse(err)
	}
	cmd.Dir = h.binding.NativeExec.WorkingDirectory
	cmd.Env = []string{"LANG=C.UTF-8", "TMPDIR=" + h.binding.NativeExec.WorkingDirectory}
	cmd.Stdin = bytes.NewReader(r.Body)
	var output, diagnostic bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &diagnostic
	h.receipt.Requests = 1
	h.receipt.Transmission = "sent"
	h.receipt.Outcome = "pending"
	h.receipt.NativeExec.InputSHA256 = workspaceHash(r.Body)
	err = cmd.Start()
	if err == nil {
		h.receipt.NativeExec.Started = true
		err = cmd.Wait()
		h.receipt.NativeExec.ExitCode = cmd.ProcessState.ExitCode()
	}
	h.receipt.NativeExec.OutputSHA256 = workspaceHash(output.Bytes())
	h.receipt.NativeExec.DiagnosticSHA256 = workspaceHash(diagnostic.Bytes())
	h.receipt.Responses = 1
	h.receipt.Outcome = "succeeded"
	if err != nil {
		h.receipt.Outcome = "failed"
	}
	response := OperationResponse{Body: output.Bytes(), ContentType: "application/json", StatusCode: 200}
	if err != nil {
		response.StatusCode = 500
		return response, fmt.Errorf("native invocation failed; inspect effect receipt: %w", err)
	}
	return response, nil
}

func decodeNativeRequest(raw []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return fmt.Errorf("native request trailing data")
	}
	return nil
}
