package egresscontract

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const ModeGoInputs = "go_inputs"

type GoInputsPermission struct {
	Profile string `json:"profile"`
}
type GoReadFile struct {
	Ref            GoResourcePath `json:"ref"`
	ExpectedDigest string         `json:"expected_digest"`
	AllowMissing   bool           `json:"allow_missing"`
}
type GoFileData struct {
	Ref          GoResourcePath `json:"ref"`
	OriginalPath string         `json:"original_path"`
	Digest       string         `json:"digest"`
	Data         string         `json:"data"`
	Size         int64          `json:"size"`
	Found        bool           `json:"found"`
}
type GoImageFile struct {
	Path         string          `json:"path"`
	Digest       string          `json:"digest"`
	Data         string          `json:"data"`
	Source       *GoResourcePath `json:"source,omitempty"`
	SourceDigest string          `json:"source_digest,omitempty"`
}
type GoInputsRequest struct {
	Action     string        `json:"action"`
	Files      []GoReadFile  `json:"files"`
	ImageFiles []GoImageFile `json:"image_files"`
	ImageKey   string        `json:"image_key"`
}
type GoInputsResult struct {
	Runtime     *GoRuntimePin    `json:"runtime,omitempty"`
	Roots       []GoResourceRoot `json:"roots"`
	Files       []GoFileData     `json:"files"`
	Image       *GoResourcePath  `json:"image,omitempty"`
	ImageDigest string           `json:"image_digest,omitempty"`
	Status      string           `json:"status"`
}

func validateGoInputsOperation(op Operation) error {
	if op.GoInputs == nil || !validToken(op.ID) || op.Method != "CAPTURE" || op.Destination.Kind != DestinationFilesystem || op.Destination.Path != "." || len(op.Destination.Origins) != 0 || op.GoCommand != nil || op.RecordStore != nil || op.Filesystem != nil || op.Browser != nil || op.AtomicFile != nil || op.WorkspaceDelivery != nil || len(op.Credentials) != 0 || len(op.Headers) != 0 || len(op.QueryParameters) != 0 || op.RedirectPolicy != RedirectNone || op.MaxRedirects != 0 || op.MaxRequestBytes < 0 || op.MaxResponseBytes < 0 || op.TimeoutMillis < 0 {
		return fmt.Errorf("invalid Go input profile")
	}
	switch op.GoInputs.Profile {
	case "runtime_context.v1", "analysis_evidence.v1":
		if op.MaxRequests != 1 {
			return fmt.Errorf("Go capture requires one request")
		}
	case "stage_inputs.v1":
		if op.MaxRequests != 2 {
			return fmt.Errorf("Go staging requires capture and immutable publication")
		}
	default:
		return fmt.Errorf("unsupported Go input profile")
	}
	return nil
}
func validateGoInputsBinding(p Policy, b Binding) (Binding, error) {
	return validateGoCommandBinding(p, b)
}

type NativeGoInputs struct{}

func (NativeGoInputs) Name() string { return "native.go_inputs.v1" }
func (NativeGoInputs) Supports(p Policy) error {
	if p.Mode != ModeGoInputs {
		return fmt.Errorf("Go inputs mode required")
	}
	return p.Validate()
}
func (a NativeGoInputs) Start(ctx context.Context, inv Invocation) (InvocationHandle, error) {
	if ctx == nil {
		return nil, fmt.Errorf("Go input context absent")
	}
	if e := a.Supports(inv.Policy); e != nil {
		return nil, e
	}
	b, e := ValidateBinding(inv.Policy, inv.Binding)
	if e != nil {
		return nil, e
	}
	digest, _ := inv.Policy.Digest()
	id := inv.Identity
	if id.RunID == "" || id.ProcessDigest == "" || id.NodeKey == "" || id.NodeName == "" || id.NodeDigest == "" || id.NodeSpecDigest == "" || id.PolicyDigest != digest || id.Operation != b.Operation {
		return nil, fmt.Errorf("Go input identity mismatch")
	}
	g := *b.GoCommand
	g.Runtime.CCompiler = cloneGoCCompilerPin(g.Runtime.CCompiler)
	g.Runtime.Components = append([]GoComponentPin{}, g.Runtime.Components...)
	if g.Runtime.QualifiedTargets != nil {
		g.Runtime.QualifiedTargets = append([]string{}, g.Runtime.QualifiedTargets...)
	}
	if g.Runtime.Defaults.Tags != nil {
		g.Runtime.Defaults.Tags = append([]string{}, g.Runtime.Defaults.Tags...)
	}
	g.ReadRoots = append([]GoResourceRoot{}, g.ReadRoots...)
	b.GoCommand = &g
	if c := g.Runtime.CCompiler; c != nil {
		for _, write := range []string{g.Scratch, g.CompilerCache} {
			real, e := filepath.EvalSymlinks(write)
			if e != nil {
				return nil, e
			}
			if goWithin(c.Root, real) || goWithin(real, c.Root) {
				return nil, fmt.Errorf("Go writable state overlaps selected C source resources")
			}
		}
	}
	if e := verifyGoRuntime(g.Runtime); e != nil {
		return nil, e
	}
	roots := map[string]string{"workspace": b.Directory, "stage": g.Scratch, "toolchain": g.Runtime.GOROOT, "module_cache": g.ModuleCache, "compiler_cache": g.CompilerCache}
	for _, r := range g.ReadRoots {
		if _, exists := roots[r.ID]; exists {
			return nil, fmt.Errorf("Go root repeats reserved identity")
		}
		roots[r.ID] = r.Directory
	}
	opened := map[string]*os.Root{}
	clean := map[string]string{}
	for id, p := range roots {
		real, e := filepath.EvalSymlinks(p)
		if e != nil {
			for _, r := range opened {
				r.Close()
			}
			return nil, e
		}
		root, e := os.OpenRoot(real)
		if e != nil {
			for _, r := range opened {
				r.Close()
			}
			return nil, e
		}
		opened[id] = root
		clean[id] = real
	}
	for _, writeID := range []string{"stage", "compiler_cache"} {
		for id, root := range clean {
			if id == writeID {
				continue
			}
			if goWithin(root, clean[writeID]) || goWithin(clean[writeID], root) {
				for _, r := range opened {
					r.Close()
				}
				return nil, fmt.Errorf("Go scratch/cache overlaps %s", id)
			}
		}
	}
	child, cancel := context.WithCancel(ctx)
	return &goInputsHandle{ctx: child, cancel: cancel, binding: b, op: inv.Policy.Operations[0], roots: opened, canonical: clean, captured: map[GoResourcePath]string{}, receipt: Receipt{Identity: id, PolicyDigest: digest, Operation: b.Operation, Adapter: a.Name(), Outcome: "unused", Transmission: "not_sent"}}, nil
}

type goInputsHandle struct {
	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	binding   Binding
	op        Operation
	roots     map[string]*os.Root
	canonical map[string]string
	captured  map[GoResourcePath]string
	receipt   Receipt
	phase     int
	closed    bool
}

func (h *goInputsHandle) Cancel() error {
	h.cancel()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	h.closed = true
	for _, r := range h.roots {
		r.Close()
	}
	return nil
}
func (h *goInputsHandle) Receipt() Receipt       { h.mu.Lock(); defer h.mu.Unlock(); return h.receipt }
func (h *goInputsHandle) Accounting() Accounting { return ReceiptAccounting(h.Receipt()) }
func (h *goInputsHandle) Invoke(ctx context.Context, raw []byte) (Result, error) {
	var req OperationRequest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF {
		return Result{}, fmt.Errorf("invalid Go input envelope")
	}
	res, e := h.Do(ctx, req)
	out, _ := json.Marshal(res)
	return Result{Response: out, Receipt: h.Receipt()}, e
}
func goDataHash(b []byte) string { s := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(s[:]) }
func (h *goInputsHandle) unchangedRoots() error {
	for id, r := range h.roots {
		held, e := r.Stat(".")
		named, ne := os.Stat(h.canonical[id])
		if e != nil || ne != nil || !os.SameFile(held, named) {
			return fmt.Errorf("Go resource changed: %s", id)
		}
	}
	return nil
}
func (h *goInputsHandle) read(ctx context.Context, in GoReadFile) (GoFileData, error) {
	r, ok := h.roots[in.Ref.Root]
	if !ok || !cleanGoPath(in.Ref.Path) || in.ExpectedDigest != "" && (!strings.HasPrefix(in.ExpectedDigest, "sha256:") || !validHex(strings.TrimPrefix(in.ExpectedDigest, "sha256:"))) {
		return GoFileData{}, fmt.Errorf("unauthorized Go input path or digest")
	}
	out := GoFileData{Ref: in.Ref, OriginalPath: filepath.Join(h.canonical[in.Ref.Root], in.Ref.Path)}
	f, e := r.Open(in.Ref.Path)
	if e != nil {
		if in.AllowMissing && os.IsNotExist(e) && in.ExpectedDigest == "" {
			return out, nil
		}
		return out, e
	}
	defer f.Close()
	before, e := f.Stat()
	if e != nil || !before.Mode().IsRegular() {
		return out, fmt.Errorf("Go input is not a regular file")
	}
	data, e := io.ReadAll(recordCommitReader{caller: ctx, invocation: h.ctx, file: f})
	if e != nil {
		return out, e
	}
	after, e := f.Stat()
	named, ne := r.Stat(in.Ref.Path)
	if e != nil || ne != nil || !os.SameFile(before, after) || !os.SameFile(after, named) || before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		return out, fmt.Errorf("Go input changed during capture")
	}
	out.Found = true
	out.Size = int64(len(data))
	out.Digest = goDataHash(data)
	out.Data = base64.StdEncoding.EncodeToString(data)
	if in.ExpectedDigest != "" && in.ExpectedDigest != out.Digest {
		return out, fmt.Errorf("Go input differs from acquired source: %s", in.Ref.Path)
	}
	return out, nil
}
func (h *goInputsHandle) Do(caller context.Context, req OperationRequest) (OperationResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fail := func(e error) (OperationResponse, error) {
		h.receipt.Outcome = "refused"
		h.receipt.DeniedRequests++
		return OperationResponse{}, e
	}
	if caller == nil || h.ctx.Err() != nil || caller.Err() != nil || h.closed {
		return fail(fmt.Errorf("Go input invocation closed"))
	}
	if req.Operation != "" && req.Operation != h.op.ID || req.URL != "" || len(req.PathParameters) != 0 || len(req.QueryParameters) != 0 || len(req.RepeatedQueryParameters) != 0 || h.op.MaxRequestBytes > 0 && len(req.Body) > h.op.MaxRequestBytes {
		return fail(fmt.Errorf("invalid Go input request"))
	}
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	stop := context.AfterFunc(caller, cancel)
	defer stop()
	var in GoInputsRequest
	d := json.NewDecoder(bytes.NewReader(req.Body))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
		return fail(fmt.Errorf("invalid typed Go capture input"))
	}
	if e := h.unchangedRoots(); e != nil {
		return fail(e)
	}
	profile := h.op.GoInputs.Profile
	out := GoInputsResult{Roots: []GoResourceRoot{}, Files: []GoFileData{}, Status: "captured"}
	if in.Action == "read" {
		if h.phase != 0 || len(in.ImageFiles) != 0 || in.ImageKey != "" {
			return fail(fmt.Errorf("Go capture phase mismatch"))
		}
		seen := map[GoResourcePath]bool{}
		for _, file := range in.Files {
			if seen[file.Ref] {
				return fail(fmt.Errorf("repeated Go capture reference"))
			}
			seen[file.Ref] = true
			data, e := h.read(ctx, file)
			if e != nil {
				return fail(e)
			}
			out.Files = append(out.Files, data)
			if data.Found {
				h.captured[data.Ref] = data.Digest
			}
		}
		h.phase = 1
		if profile == "runtime_context.v1" {
			runtime := h.binding.GoCommand.Runtime
			out.Runtime = &runtime
			ids := make([]string, 0, len(h.canonical))
			for id := range h.canonical {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				out.Roots = append(out.Roots, GoResourceRoot{ID: id, Directory: h.canonical[id]})
			}
		}
	} else if in.Action == "write_image" {
		if profile != "stage_inputs.v1" || h.phase != 1 || len(in.Files) != 0 || !validHex(in.ImageKey) || len(in.ImageFiles) == 0 {
			return fail(fmt.Errorf("immutable stage publication requires captured inputs"))
		}
		seen := map[string]bool{}
		for _, file := range in.ImageFiles {
			if !cleanGoPath(file.Path) || file.Path == "." || file.Path == ".analysis-manifest.json" || seen[file.Path] {
				return fail(fmt.Errorf("invalid or repeated analysis image path"))
			}
			seen[file.Path] = true
			raw, e := base64.StdEncoding.DecodeString(file.Data)
			if e != nil || goDataHash(raw) != file.Digest {
				return fail(fmt.Errorf("analysis image data digest mismatch"))
			}
			if file.Source != nil {
				if h.captured[*file.Source] != file.SourceDigest || file.SourceDigest == "" {
					return fail(fmt.Errorf("image source was not captured by this invocation"))
				}
			} else if file.SourceDigest != "" {
				return fail(fmt.Errorf("unbound analysis image source digest"))
			}
		}
		// Recheck the original named bytes before publication. A capture/stage race
		// must refuse rather than label changed bytes as an immutable input image.
		for ref, digest := range h.captured {
			if _, e := h.read(ctx, GoReadFile{Ref: ref, ExpectedDigest: digest}); e != nil {
				return fail(e)
			}
		}
		canonical := append([]GoImageFile{}, in.ImageFiles...)
		sort.Slice(canonical, func(i, j int) bool { return canonical[i].Path < canonical[j].Path })
		witness := make([]GoImageFile, len(canonical))
		copy(witness, canonical)
		for i := range witness {
			witness[i].Data = ""
		}
		wr, _ := json.Marshal(witness)
		if strings.TrimPrefix(goDataHash(wr), "sha256:") != in.ImageKey {
			return fail(fmt.Errorf("analysis image key mismatch"))
		}
		scratch := h.canonical["stage"]
		name := "analysis-image-" + in.ImageKey
		final := filepath.Join(scratch, name)
		out.Image = &GoResourcePath{Root: "stage", Path: name}
		out.ImageDigest = "sha256:" + in.ImageKey
		if _, e := os.Stat(final); e == nil {
			if e = h.verifyImage(ctx, final, canonical); e != nil {
				return fail(e)
			}
			out.Status = "retained"
		} else if !os.IsNotExist(e) {
			return fail(e)
		} else {
			pending, e := os.MkdirTemp(scratch, ".analysis-pending-")
			if e != nil {
				return fail(e)
			}
			defer os.RemoveAll(pending)
			root, e := os.OpenRoot(pending)
			if e != nil {
				return fail(e)
			}
			defer root.Close()
			for _, file := range canonical {
				if ctx.Err() != nil {
					return fail(ctx.Err())
				}
				if e = root.MkdirAll(filepath.Dir(file.Path), 0700); e != nil {
					return fail(e)
				}
				raw, _ := base64.StdEncoding.DecodeString(file.Data)
				f, e := root.OpenFile(file.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
				if e != nil {
					return fail(e)
				}
				_, e = f.Write(raw)
				ce := f.Close()
				if e != nil {
					return fail(e)
				}
				if ce != nil {
					return fail(ce)
				}
			}
			if e = root.WriteFile(".analysis-manifest.json", wr, 0400); e != nil {
				return fail(e)
			}
			if e := h.unchangedRoots(); e != nil {
				return fail(e)
			}
			if e = os.Rename(pending, final); e != nil {
				if ve := h.verifyImage(ctx, final, canonical); ve != nil {
					return fail(e)
				}
				out.Status = "retained"
			} else {
				out.Status = "staged"
			}
		}
		h.phase = 2
	} else {
		return fail(fmt.Errorf("unsupported Go input action"))
	}
	if e := h.unchangedRoots(); e != nil {
		return fail(e)
	}
	h.receipt.Requests++
	h.receipt.Responses++
	h.receipt.Transmission = "local_resource"
	h.receipt.Outcome = "completed"
	body, _ := json.Marshal(out)
	if h.op.MaxResponseBytes > 0 && len(body) > h.op.MaxResponseBytes {
		return fail(fmt.Errorf("Go capture response exceeds sealed bound"))
	}
	return OperationResponse{StatusCode: 200, ContentType: "application/json", Body: body, ResourceIdentity: "sha256:" + goRootHash(h.canonical["workspace"])}, nil
}
func (h *goInputsHandle) verifyImage(ctx context.Context, dir string, files []GoImageFile) error {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil || !goWithin(h.canonical["stage"], real) || real != dir {
		return fmt.Errorf("analysis image root substituted")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	expected := map[string]string{}
	for _, file := range files {
		expected[filepath.ToSlash(file.Path)] = file.Digest
	}
	seen := map[string]bool{}
	err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("analysis image contains a symlink")
		}
		if entry.IsDir() {
			return nil
		}
		if path == ".analysis-manifest.json" {
			return nil
		}
		digest, ok := expected[path]
		if !ok {
			return fmt.Errorf("analysis image has an unplanned file: %s", path)
		}
		raw, e := root.ReadFile(path)
		if e != nil || goDataHash(raw) != digest {
			return fmt.Errorf("analysis image digest mismatch: %s", path)
		}
		seen[path] = true
		return nil
	})
	if err != nil {
		return err
	}
	if e := verifyGoImageManifest(ctx, dir); e != nil {
		return e
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("analysis image is missing captured inputs")
	}
	return nil
}
