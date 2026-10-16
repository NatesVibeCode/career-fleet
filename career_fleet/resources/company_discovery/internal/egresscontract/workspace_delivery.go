package egresscontract

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

const ModeWorkspaceDelivery = "workspace_delivery"

type WorkspaceProcedure struct {
	ID               string                 `json:"id"`
	ReadOnlyPaths    []string               `json:"read_only_paths"`
	Executable       string                 `json:"executable"`
	ExecutableDigest string                 `json:"executable_digest"`
	Arguments        []string               `json:"arguments"`
	Environment      []WorkspaceEnvironment `json:"environment"`
}
type WorkspaceEnvironment struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Files are exact selected business inputs. The adapter implements local
// mechanics; consumer meaning, result acceptance and scheduling stay in Nodes.
type WorkspaceDeliveryPermission struct {
	Files         []string             `json:"files"`
	ConsumerFiles []string             `json:"consumer_files"`
	Procedures    []WorkspaceProcedure `json:"procedures"`
}
type WorkspaceDelta struct {
	Path          string `json:"path"`
	BeforePresent bool   `json:"before_present"`
	BeforeDigest  string `json:"before_digest"`
	AfterPresent  bool   `json:"after_present"`
	AfterContent  string `json:"after_content"`
}
type WorkspaceInput struct {
	Path    string `json:"path"`
	Present bool   `json:"present"`
	Digest  string `json:"digest"`
}
type WorkspaceDeliveryRequest struct {
	CheckID        string           `json:"check_id"`
	Changes        []WorkspaceDelta `json:"changes"`
	CandidateID    string           `json:"candidate_id"`
	ProcedureID    string           `json:"procedure_id"`
	ExpectedInputs []WorkspaceInput `json:"expected_inputs"`
}
type WorkspaceDeliveryResult struct {
	CandidateID     string           `json:"candidate_id"`
	Status          string           `json:"status"`
	ProcedureID     string           `json:"procedure_id"`
	ProcedureDigest string           `json:"procedure_digest"`
	Output          string           `json:"output"`
	ResultDigest    string           `json:"result_digest"`
	Changed         []string         `json:"changed"`
	SourceInputs    []WorkspaceInput `json:"source_inputs"`
}
type workspaceIntent struct {
	CandidateID string           `json:"candidate_id"`
	Changes     []WorkspaceDelta `json:"changes"`
	State       string           `json:"state"`
}

func workspaceHash(raw []byte) string       { s := sha256.Sum256(raw); return hex.EncodeToString(s[:]) }
func workspaceValueDigest(value any) string { raw, _ := json.Marshal(value); return workspaceHash(raw) }
func workspacePath(p string) bool {
	return p != "" && p != "." && path.Clean(p) == p && !strings.HasPrefix(p, "/") && p != ".." && !strings.HasPrefix(p, "../") && !strings.ContainsAny(p, "\\\x00") && p != ".collaboration" && !strings.HasPrefix(p, ".collaboration/")
}
func validateWorkspaceDeliveryOperation(op Operation) error {
	if op.WorkspaceDelivery == nil || !validToken(op.ID) || op.Destination.Kind != DestinationFilesystem || op.Destination.Path != "." || len(op.Destination.Origins) != 0 || len(op.Credentials) != 0 || len(op.Headers) != 0 || len(op.QueryParameters) != 0 || op.Filesystem != nil || op.Browser != nil || op.MaxRedirects != 0 || op.RedirectPolicy != RedirectNone {
		return fmt.Errorf("workspace delivery operation invalid")
	}
	switch op.Method {
	case "CHECK", "APPLY", "RECONCILE":
	default:
		return fmt.Errorf("workspace delivery method invalid")
	}
	p := op.WorkspaceDelivery
	if p.Files == nil || p.ConsumerFiles == nil || p.Procedures == nil {
		return fmt.Errorf("workspace selected inputs required")
	}
	seen := map[string]bool{}
	for _, f := range p.Files {
		if !workspacePath(f) || seen[f] {
			return fmt.Errorf("workspace input path invalid")
		}
		seen[f] = true
	}
	consumerNames := map[string]bool{}
	for _, name := range p.ConsumerFiles {
		if !seen[name] || consumerNames[name] {
			return fmt.Errorf("consumer definition input invalid")
		}
		consumerNames[name] = true
	}
	seen = map[string]bool{}
	for _, p := range p.Procedures {
		if p.ID == "" || seen[p.ID] || !filepath.IsAbs(p.Executable) || len(p.ExecutableDigest) != 64 || p.Arguments == nil || p.Environment == nil || p.ReadOnlyPaths == nil {
			return fmt.Errorf("workspace procedure invalid")
		}
		seen[p.ID] = true
		for _, path := range p.ReadOnlyPaths {
			if !filepath.IsAbs(path) || filepath.Clean(path) != path {
				return fmt.Errorf("consumer read grant invalid")
			}
		}
		names := map[string]bool{}
		for _, e := range p.Environment {
			if e.Name == "" || strings.ContainsAny(e.Name, "=\x00") || strings.ContainsRune(e.Value, 0) || names[e.Name] {
				return fmt.Errorf("workspace environment invalid")
			}
			names[e.Name] = true
		}
	}
	// No new byte, file-count, duration or retry ceiling is introduced here.
	// Cancellation comes from the owning invocation context.
	return nil
}
func validateWorkspaceDeliveryBinding(p Policy, b Binding) (Binding, error) {
	if b.Directory == "" || !filepath.IsAbs(b.Directory) || filepath.Clean(b.Directory) != b.Directory || b.Browser != nil || len(b.Origins) != 0 || len(b.Routes) != 0 || b.CredentialRef != "" {
		return Binding{}, fmt.Errorf("explicit workspace deployment required")
	}
	for _, op := range p.Operations {
		if op.ID == b.Operation {
			return b, nil
		}
	}
	return Binding{}, fmt.Errorf("workspace operation undeclared")
}

type NativeWorkspaceDelivery struct{}

func (NativeWorkspaceDelivery) Name() string { return "native.workspace_delivery.v1" }
func (NativeWorkspaceDelivery) Supports(p Policy) error {
	if p.Mode != ModeWorkspaceDelivery {
		return fmt.Errorf("workspace delivery policy required")
	}
	return p.Validate()
}
func (a NativeWorkspaceDelivery) Start(ctx context.Context, inv Invocation) (InvocationHandle, error) {
	if ctx == nil {
		return nil, fmt.Errorf("workspace context absent")
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if e := a.Supports(inv.Policy); e != nil {
		return nil, e
	}
	b, e := ValidateBinding(inv.Policy, inv.Binding)
	if e != nil {
		return nil, e
	}
	pd, _ := inv.Policy.Digest()
	id := inv.Identity
	if id.RunID == "" || id.ProcessDigest == "" || id.NodeKey == "" || id.NodeName == "" || id.NodeDigest == "" || id.NodeSpecDigest == "" || id.PolicyDigest != pd || id.Operation != b.Operation {
		return nil, fmt.Errorf("workspace identity mismatch")
	}
	root, e := os.OpenRoot(b.Directory)
	if e != nil {
		return nil, e
	}
	var op Operation
	for _, x := range Normalize(inv.Policy).Operations {
		if x.ID == b.Operation {
			op = x
		}
	}
	child, cancel := context.WithCancel(ctx)
	return &workspaceDeliveryHandle{ctx: child, cancel: cancel, root: root, directory: b.Directory, op: op, receipt: Receipt{Identity: id, PolicyDigest: pd, Operation: op.ID, Adapter: a.Name(), Outcome: "unused", Transmission: "not_sent"}}, nil
}

type workspaceDeliveryHandle struct {
	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	root      *os.Root
	directory string
	op        Operation
	receipt   Receipt
	closed    bool
}

func (h *workspaceDeliveryHandle) Cancel() error {
	h.cancel()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	return h.root.Close()
}
func (h *workspaceDeliveryHandle) Receipt() Receipt {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.receipt
}
func (h *workspaceDeliveryHandle) Accounting() Accounting { return ReceiptAccounting(h.Receipt()) }
func (h *workspaceDeliveryHandle) Invoke(ctx context.Context, raw []byte) (Result, error) {
	var req OperationRequest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(&req); e != nil {
		return Result{}, e
	}
	if d.Decode(new(any)) != io.EOF {
		return Result{}, fmt.Errorf("workspace envelope invalid")
	}
	out, e := h.Do(ctx, req)
	b, _ := json.Marshal(out)
	return Result{Response: b, Receipt: h.Receipt()}, e
}
func (h *workspaceDeliveryHandle) Do(ctx context.Context, envelope OperationRequest) (OperationResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	refuse := func(e error) (OperationResponse, error) {
		h.receipt.Outcome = "refused"
		h.receipt.DeniedRequests++
		return OperationResponse{}, e
	}
	if h.closed {
		return refuse(fmt.Errorf("workspace closed"))
	}
	if e := ctx.Err(); e != nil {
		return refuse(e)
	}
	if e := h.ctx.Err(); e != nil {
		return refuse(e)
	}
	if envelope.Operation != "" && envelope.Operation != h.op.ID || envelope.URL != "" || len(envelope.PathParameters) != 0 || len(envelope.QueryParameters) != 0 || len(envelope.RepeatedQueryParameters) != 0 {
		return refuse(fmt.Errorf("workspace operation mismatch"))
	}
	var req WorkspaceDeliveryRequest
	d := json.NewDecoder(bytes.NewReader(envelope.Body))
	d.DisallowUnknownFields()
	if e := d.Decode(&req); e != nil {
		return refuse(e)
	}
	if d.Decode(new(any)) != io.EOF || req.Changes == nil || req.ExpectedInputs == nil || req.CandidateID != workspaceValueDigest(req.Changes) {
		return refuse(fmt.Errorf("workspace candidate binding invalid"))
	}
	paths := map[string]bool{}
	for _, c := range req.Changes {
		if !workspacePath(c.Path) || paths[c.Path] || c.BeforePresent && len(c.BeforeDigest) != 64 || !c.BeforePresent && c.BeforeDigest != "" || !c.AfterPresent && c.AfterContent != "" {
			return refuse(fmt.Errorf("workspace change invalid"))
		}
		paths[c.Path] = true
	}
	for _, change := range req.Changes {
		for _, protected := range h.op.WorkspaceDelivery.ConsumerFiles {
			if change.Path == protected {
				return refuse(fmt.Errorf("consumer_definition_change_requires_definition_work:%s", protected))
			}
		}
	}
	h.receipt.Requests++
	h.receipt.Transmission = "sent"
	var result WorkspaceDeliveryResult
	var e error
	switch h.op.Method {
	case "CHECK":
		if !recordKey(req.CheckID) {
			return refuse(fmt.Errorf("workspace check identity missing"))
		}
		result, e = h.check(ctx, req)
		if e == nil {
			e = h.recordCheck(req, result)
		}
	case "APPLY":
		result, e = h.apply(req)
	case "RECONCILE":
		if req.ProcedureID != "" {
			result, e = h.readCheck(req)
		} else {
			result, e = h.reconcile(req)
		}
	}
	if e != nil {
		h.receipt.Outcome = "failed"
		return OperationResponse{}, e
	}
	raw, _ := json.Marshal(result)
	h.receipt.Outcome = "completed"
	h.receipt.Responses++
	return OperationResponse{StatusCode: 200, Body: raw, ContentType: "application/json"}, nil
}
func (h *workspaceDeliveryHandle) read(p string) ([]byte, bool, error) {
	raw, e := h.root.ReadFile(p)
	if os.IsNotExist(e) {
		return nil, false, nil
	}
	if e != nil {
		return nil, false, e
	}
	return raw, true, nil
}
func (h *workspaceDeliveryHandle) matches(c WorkspaceDelta, after bool) (bool, error) {
	raw, present, e := h.read(c.Path)
	if e != nil {
		return false, e
	}
	if after {
		return present == c.AfterPresent && (!present || string(raw) == c.AfterContent), nil
	}
	return present == c.BeforePresent && (!present || workspaceHash(raw) == c.BeforeDigest), nil
}
func (h *workspaceDeliveryHandle) reconcile(req WorkspaceDeliveryRequest) (WorkspaceDeliveryResult, error) {
	result := WorkspaceDeliveryResult{CandidateID: req.CandidateID, Status: "used", Changed: []string{}, SourceInputs: []WorkspaceInput{}}
	for _, input := range req.ExpectedInputs {
		if !workspacePath(input.Path) {
			return result, fmt.Errorf("workspace input stamp invalid")
		}
		if pathsContain(req.Changes, input.Path) {
			continue
		}
		raw, present, err := h.read(input.Path)
		if err != nil {
			return result, err
		}
		if present != input.Present || present && workspaceHash(raw) != input.Digest {
			result.Status = "unknown"
			result.Output = "consumer_input_changed:" + input.Path
			return result, nil
		}
	}
	for _, c := range req.Changes {
		ok, e := h.matches(c, true)
		if e != nil {
			return result, e
		}
		if !ok {
			result.Status = "not_used"
			break
		}
		result.Changed = append(result.Changed, c.Path)
	}
	if result.Status == "used" {
		result.ResultDigest = workspaceValueDigest(req.Changes)
	}
	return result, nil
}
func (h *workspaceDeliveryHandle) apply(req WorkspaceDeliveryRequest) (WorkspaceDeliveryResult, error) {
	result := WorkspaceDeliveryResult{CandidateID: req.CandidateID, Status: "unknown", Changed: []string{}, SourceInputs: []WorkspaceInput{}}
	if e := h.root.MkdirAll(".collaboration/intents", 0o700); e != nil {
		return result, e
	}
	lock, e := h.root.OpenFile(".collaboration/application.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if e != nil {
		return result, e
	}
	defer lock.Close()
	if e := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		result.Output = "application_in_progress"
		return result, nil
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	for _, input := range req.ExpectedInputs {
		if !workspacePath(input.Path) {
			return result, fmt.Errorf("workspace input stamp invalid")
		}
		raw, present, e := h.read(input.Path)
		if e != nil {
			return result, e
		}
		if pathsContain(req.Changes, input.Path) {
			continue
		}
		if present != input.Present || present && workspaceHash(raw) != input.Digest {
			result.Output = "consumer_input_changed:" + input.Path
			return result, nil
		}
	}
	result, e = h.reconcile(req)
	if e != nil || result.Status == "used" {
		return result, e
	}
	result.Status = "unknown"
	result.Changed = []string{}
	// Reconcile both whole and partial prior application. Unexpected bytes are
	// left intact and produce repair; they are never overwritten for convenience.
	for _, c := range req.Changes {
		after, e := h.matches(c, true)
		if e != nil {
			return result, e
		}
		if after {
			continue
		}
		before, e := h.matches(c, false)
		if e != nil {
			return result, e
		}
		if !before {
			result.Output = "destination_changed:" + c.Path
			return result, nil
		}
	}
	backup := ".collaboration/intents/" + req.CandidateID + ".before"
	if e := h.root.MkdirAll(backup, 0o700); e != nil {
		return result, e
	}
	for _, c := range req.Changes {
		raw, present, e := h.read(c.Path)
		if e != nil {
			return result, e
		}
		if !present {
			continue
		}
		name := backup + "/" + workspaceHash([]byte(c.Path))
		if _, exists, e := h.read(name); e != nil {
			return result, e
		} else if !exists {
			if e := h.root.WriteFile(name, raw, 0o600); e != nil {
				return result, e
			}
		}
	}
	intentPath := ".collaboration/intents/" + req.CandidateID + ".json"
	raw, _ := json.Marshal(workspaceIntent{CandidateID: req.CandidateID, Changes: req.Changes, State: "applying"})
	if e := h.root.WriteFile(intentPath, raw, 0o600); e != nil {
		return result, e
	}
	for _, c := range req.Changes {
		after, e := h.matches(c, true)
		if e != nil {
			return result, e
		}
		if after {
			continue
		}
		before, e := h.matches(c, false)
		if e != nil {
			return result, e
		}
		if !before {
			result.Output = "destination_changed:" + c.Path
			return result, nil
		}
		if c.AfterPresent {
			if parent := path.Dir(c.Path); parent != "." {
				if e := h.root.MkdirAll(parent, 0o700); e != nil {
					return result, e
				}
			}
			mode := os.FileMode(0o600)
			if info, e := h.root.Stat(c.Path); e == nil {
				mode = info.Mode().Perm()
			}
			temporary := backup + "/" + workspaceHash([]byte(c.Path)) + ".after"
			f, e := h.root.OpenFile(temporary, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if e != nil {
				return result, e
			}
			_, writeErr := f.Write([]byte(c.AfterContent))
			syncErr := f.Sync()
			closeErr := f.Close()
			if writeErr != nil {
				return result, writeErr
			}
			if syncErr != nil {
				return result, syncErr
			}
			if closeErr != nil {
				return result, closeErr
			}
			before, e := h.matches(c, false)
			if e != nil {
				return result, e
			}
			if !before {
				result.Output = "destination_changed:" + c.Path
				return result, nil
			}
			if e := h.root.Rename(temporary, c.Path); e != nil {
				return result, e
			}
		} else {
			if e := h.root.Remove(c.Path); e != nil && !os.IsNotExist(e) {
				return result, e
			}
		}
		result.Changed = append(result.Changed, c.Path)
	}
	result, e = h.reconcile(req)
	if e != nil {
		return result, e
	}
	if result.Status != "used" {
		result.Status = "unknown"
		return result, nil
	}
	raw, _ = json.Marshal(workspaceIntent{CandidateID: req.CandidateID, Changes: req.Changes, State: "used"})
	if e := h.root.WriteFile(intentPath, raw, 0o600); e != nil {
		return result, e
	}
	return result, nil
}
func (h *workspaceDeliveryHandle) check(ctx context.Context, req WorkspaceDeliveryRequest) (WorkspaceDeliveryResult, error) {
	result := WorkspaceDeliveryResult{CandidateID: req.CandidateID, Status: "failed", ProcedureID: req.ProcedureID, Changed: []string{}, SourceInputs: []WorkspaceInput{}}
	var procedure WorkspaceProcedure
	for _, p := range h.op.WorkspaceDelivery.Procedures {
		if p.ID == req.ProcedureID {
			procedure = p
		}
	}
	if procedure.ID == "" {
		return result, fmt.Errorf("workspace procedure undeclared")
	}
	executable, e := os.ReadFile(procedure.Executable)
	if e != nil || workspaceHash(executable) != procedure.ExecutableDigest {
		return result, fmt.Errorf("workspace toolchain changed")
	}
	result.ProcedureDigest = workspaceValueDigest(procedure)
	stageRel := ".collaboration/candidates/" + req.CheckID
	stage := filepath.Join(h.directory, filepath.FromSlash(stageRel))
	if e := h.root.MkdirAll(".collaboration/candidates", 0o700); e != nil {
		return result, e
	}
	if e := h.root.Mkdir(stageRel, 0o700); e != nil {
		if os.IsExist(e) {
			return h.readCheck(req)
		}
		return result, e
	}
	candidate, e := h.root.OpenRoot(stageRel)
	if e != nil {
		return result, e
	}
	defer candidate.Close()
	// Rebuild the exact selected files, including deletions, before every check.
	selected := map[string]bool{}
	for _, name := range h.op.WorkspaceDelivery.Files {
		raw, present, e := h.read(name)
		if e != nil {
			return result, e
		}
		if !present {
			created := false
			for _, change := range req.Changes {
				if change.Path == name && !change.BeforePresent && change.AfterPresent {
					created = true
				}
			}
			if !created {
				return result, fmt.Errorf("workspace input missing:%s", name)
			}
			// An explicitly selected candidate input may be a new file. Retain
			// its actual absence so currentness remains bound before use.
			result.SourceInputs = append(result.SourceInputs, WorkspaceInput{Path: name, Present: false, Digest: ""})
			selected[name] = true
			continue
		}
		result.SourceInputs = append(result.SourceInputs, WorkspaceInput{Path: name, Present: true, Digest: workspaceHash(raw)})
		selected[name] = true
		if dir := path.Dir(name); dir != "." {
			if e := candidate.MkdirAll(dir, 0o700); e != nil {
				return result, e
			}
		}
		if e := candidate.WriteFile(name, raw, 0o600); e != nil {
			return result, e
		}
	}
	for _, c := range req.Changes {
		if !selected[c.Path] && c.BeforePresent {
			return result, fmt.Errorf("workspace prior input not selected:%s", c.Path)
		}
		ok, e := h.matches(c, false)
		if e != nil {
			return result, e
		}
		if !ok {
			result.Status = "stale"
			result.Output = "destination_changed:" + c.Path
			return result, nil
		}
		if c.AfterPresent {
			if dir := path.Dir(c.Path); dir != "." {
				if e := candidate.MkdirAll(dir, 0o700); e != nil {
					return result, e
				}
			}
			if e := candidate.WriteFile(c.Path, []byte(c.AfterContent), 0o600); e != nil {
				return result, e
			}
		} else {
			if e := candidate.Remove(c.Path); e != nil && !os.IsNotExist(e) {
				return result, e
			}
		}
	}
	// Refuse symlink inputs and pre-existing stage artifacts that escaped the
	// selected source set. The stage is execution-owned and not another checkout.
	if e := fs.WalkDir(candidate.FS(), ".", func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("workspace stage symlink")
		}
		if !d.IsDir() && !selected[p] && !pathsContain(req.Changes, p) {
			return fmt.Errorf("workspace stage unselected input:%s", p)
		}
		return nil
	}); e != nil {
		return result, e
	}
	var cmdErr error
	cmd, cmdErr := consumerCommand(ctx, procedure, stage, []string{filepath.Join(h.directory, ".collaboration/cache"), filepath.Join(h.directory, ".collaboration/tmp"), filepath.Join(h.directory, ".collaboration/gopath")})
	if cmdErr != nil {
		return result, cmdErr
	}
	cmd.Dir = stage
	env := []string{}
	for _, name := range []string{"PATH", "SYSTEMROOT", "SDKROOT", "DEVELOPER_DIR", "LANG"} {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	for _, v := range procedure.Environment {
		env = append(env, v.Name+"="+v.Value)
	}
	cache := filepath.Join(h.directory, ".collaboration", "cache")
	tmp := filepath.Join(h.directory, ".collaboration", "tmp")
	gopath := filepath.Join(h.directory, ".collaboration", "gopath")
	if e := h.root.MkdirAll(".collaboration/cache", 0o700); e != nil {
		return result, e
	}
	if e := h.root.MkdirAll(".collaboration/tmp", 0o700); e != nil {
		return result, e
	}
	if e := h.root.MkdirAll(".collaboration/gopath", 0o700); e != nil {
		return result, e
	}
	cmd.Env = append(env, "GOWORK=off", "GOCACHE="+cache, "GOPATH="+gopath, "TMPDIR="+tmp)
	output, e := cmd.CombinedOutput()
	result.Output = string(output)
	result.ResultDigest = workspaceHash(output)
	if e == nil {
		result.Status = "passed"
	} else if ctx.Err() != nil {
		result.Status = "unknown"
	} else {
		result.Output = string(output) + "\n" + e.Error()
	}
	return result, nil
}
func pathsContain(changes []WorkspaceDelta, p string) bool {
	for _, c := range changes {
		if c.Path == p {
			return true
		}
	}
	return false
}

// Completed local checks are retained before reply delivery. A lost reply can
// read the exact result; an incomplete check remains unknown, never rerun.
type workspaceCheckRecord struct {
	RequestDigest string                  `json:"request_digest"`
	Result        WorkspaceDeliveryResult `json:"result"`
}

func (h *workspaceDeliveryHandle) recordCheck(req WorkspaceDeliveryRequest, result WorkspaceDeliveryResult) error {
	if err := h.root.MkdirAll(".collaboration/checks", 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(workspaceCheckRecord{workspaceValueDigest(req), result})
	if err != nil {
		return err
	}
	name := ".collaboration/checks/" + req.CheckID + ".json"
	pending := name + ".pending"
	f, err := h.root.OpenFile(pending, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, we := f.Write(raw)
	se := f.Sync()
	ce := f.Close()
	if we != nil {
		return we
	}
	if se != nil {
		return se
	}
	if ce != nil {
		return ce
	}
	return h.root.Rename(pending, name)
}
func (h *workspaceDeliveryHandle) readCheck(req WorkspaceDeliveryRequest) (WorkspaceDeliveryResult, error) {
	result := WorkspaceDeliveryResult{CandidateID: req.CandidateID, ProcedureID: req.ProcedureID, Status: "unknown", Output: "consumer_check_unsealed", Changed: []string{}, SourceInputs: []WorkspaceInput{}}
	if !recordKey(req.CheckID) {
		return result, fmt.Errorf("workspace check identity missing")
	}
	for _, p := range h.op.WorkspaceDelivery.Procedures {
		if p.ID == req.ProcedureID {
			result.ProcedureDigest = workspaceValueDigest(p)
		}
	}
	if result.ProcedureDigest == "" {
		return result, fmt.Errorf("workspace procedure undeclared")
	}
	raw, err := h.root.ReadFile(".collaboration/checks/" + req.CheckID + ".json")
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	var held workspaceCheckRecord
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&held); err != nil {
		return result, err
	}
	if d.Decode(new(any)) != io.EOF {
		return result, fmt.Errorf("workspace check record invalid")
	}
	if held.RequestDigest != workspaceValueDigest(req) || held.Result.CandidateID != req.CandidateID || held.Result.ProcedureDigest != result.ProcedureDigest {
		return result, fmt.Errorf("workspace check record binding")
	}
	return held.Result, nil
}
