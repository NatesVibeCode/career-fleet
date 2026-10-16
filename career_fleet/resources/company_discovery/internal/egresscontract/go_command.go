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
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const ModeGoCommand = "go_command"

// Profile is selected and sealed by the Node. Adapters lower the fixed profile
// to structured arguments; they do not select packages or build settings.
type GoCommandPermission struct {
	Profile string `json:"profile"`
}
type GoComponentPin struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}
type GoRuntimePin struct {
	CCompiler        *GoCCompilerPin    `json:"c_compiler,omitempty"`
	Version          string             `json:"version"`
	Defaults         GoBuildEnvironment `json:"defaults"`
	QualifiedTargets []string           `json:"qualified_targets"`
	Executable       string             `json:"executable"`
	ExecutableDigest string             `json:"executable_digest"`
	GOROOT           string             `json:"goroot"`
	Components       []GoComponentPin   `json:"components"`
	Identity         string             `json:"identity"`
}
type GoResourceRoot struct {
	ID        string `json:"id"`
	Directory string `json:"directory"`
}
type GoCommandBinding struct {
	Runtime       GoRuntimePin     `json:"runtime"`
	ReadRoots     []GoResourceRoot `json:"read_roots"`
	Scratch       string           `json:"scratch"`
	CompilerCache string           `json:"compiler_cache"`
	ModuleCache   string           `json:"module_cache"`
}
type GoResourcePath struct {
	Root string `json:"root"`
	Path string `json:"path"`
}
type GoBuildEnvironment struct {
	GOOS         string   `json:"goos"`
	GOARCH       string   `json:"goarch"`
	CgoEnabled   bool     `json:"cgo_enabled"`
	Tags         []string `json:"tags"`
	IncludeTests bool     `json:"include_tests"`
}
type GoCommandRequest struct {
	ToolchainImage *GoResourcePath    `json:"toolchain_image,omitempty"`
	ImmutableImage *GoResourcePath    `json:"immutable_image,omitempty"`
	Directory      GoResourcePath     `json:"directory"`
	WorkspaceFile  *GoResourcePath    `json:"workspace_file,omitempty"`
	Context        GoBuildEnvironment `json:"context"`
	Packages       []string           `json:"packages"`
	ModuleMode     string             `json:"module_mode"`
}
type GoCommandResult struct {
	CCompilerIdentity string `json:"c_compiler_identity,omitempty"`
	Stdout            string `json:"stdout"`
	Stderr            string `json:"stderr"`
	ExitCode          int    `json:"exit_code"`
	RuntimeIdentity   string `json:"runtime_identity"`
	Profile           string `json:"profile"`
	EffectiveGOROOT   string `json:"effective_goroot"`
}

func validateGoCommandOperation(op Operation) error {
	if op.GoCommand == nil || !validToken(op.ID) || op.Method != "LIST" || op.Destination.Kind != DestinationFilesystem || op.Destination.Path != "." || len(op.Destination.Origins) != 0 || op.MaxRequests != 1 || op.GoInputs != nil || op.RecordStore != nil || op.Filesystem != nil || op.Browser != nil || op.AtomicFile != nil || op.WorkspaceDelivery != nil || len(op.Headers) != 0 || len(op.QueryParameters) != 0 || len(op.Credentials) != 0 || op.RedirectPolicy != RedirectNone || op.MaxRedirects != 0 || op.MaxRequestBytes < 0 || op.MaxResponseBytes < 0 || op.TimeoutMillis < 0 {
		return fmt.Errorf("invalid sealed Go command operation")
	}
	switch op.GoCommand.Profile {
	case "package_graph.v1", "compiler_exports.v1":
		return nil
	default:
		return fmt.Errorf("unsupported Go command profile")
	}
}
func cleanGoPath(p string) bool {
	return p != "" && !filepath.IsAbs(p) && filepath.Clean(p) == p && p != ".." && !strings.HasPrefix(p, "../") && !strings.ContainsAny(p, "\\\x00\r\n")
}
func absoluteGoRoot(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p && !strings.ContainsAny(p, "\x00\r\n")
}
func validateGoCommandBinding(policy Policy, b Binding) (Binding, error) {
	if b.GoCommand == nil || b.Browser != nil || !absoluteGoRoot(b.Directory) || b.CredentialRef != "" || len(b.Origins) != 0 || len(b.Routes) != 0 {
		return Binding{}, fmt.Errorf("explicit Go runtime and workspace binding required")
	}
	g := b.GoCommand
	if !absoluteGoRoot(g.Runtime.Executable) || !absoluteGoRoot(g.Runtime.GOROOT) || !validHex(g.Runtime.ExecutableDigest) || !validHex(g.Runtime.Identity) || len(g.Runtime.Components) == 0 || !absoluteGoRoot(g.Scratch) || !absoluteGoRoot(g.CompilerCache) || !absoluteGoRoot(g.ModuleCache) {
		return Binding{}, fmt.Errorf("Go runtime pin or caller scratch absent")
	}
	roots := map[string]bool{"workspace": true, "stage": true, "toolchain": true, "module_cache": true, "compiler_cache": true, "runtime_image": true, "c_runtime_image": true}
	for _, r := range g.ReadRoots {
		if !validToken(r.ID) || roots[r.ID] || !absoluteGoRoot(r.Directory) {
			return Binding{}, fmt.Errorf("invalid declared Go read root")
		}
		roots[r.ID] = true
	}
	seen := map[string]bool{}
	for _, p := range g.Runtime.Components {
		if !cleanGoPath(p.Path) || !validHex(p.Digest) || seen[p.Path] {
			return Binding{}, fmt.Errorf("invalid Go component pin")
		}
		seen[p.Path] = true
	}
	found := false
	for _, op := range policy.Operations {
		if op.ID == b.Operation {
			found = true
		}
	}
	if !found {
		return Binding{}, fmt.Errorf("undeclared Go operation")
	}
	return b, nil
}
func validHex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
func goFileDigest(p string) (string, error) {
	f, e := os.Open(p)
	if e != nil {
		return "", e
	}
	defer f.Close()
	before, e := f.Stat()
	if e != nil || !before.Mode().IsRegular() {
		return "", fmt.Errorf("Go pin is not regular")
	}
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	after, e := f.Stat()
	named, ne := os.Stat(p)
	if e != nil || ne != nil || !os.SameFile(before, after) || !os.SameFile(after, named) || before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		return "", fmt.Errorf("Go pin changed during acquisition")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func verifyGoRuntime(p GoRuntimePin) error {
	if p.CCompiler != nil {
		if _, e := readGoCCompilerManifest(p.CCompiler); e != nil {
			return e
		}
	}
	got, e := goFileDigest(p.Executable)
	if e != nil || got != p.ExecutableDigest {
		return fmt.Errorf("Go executable pin mismatch: %w", e)
	}
	seen := map[string]bool{}
	goroot, err := filepath.EvalSymlinks(p.GOROOT)
	if err != nil {
		return err
	}
	selectedExe, err := filepath.EvalSymlinks(p.Executable)
	if err != nil {
		return err
	}
	rootExe, err := filepath.EvalSymlinks(filepath.Join(goroot, "bin/go"))
	if err != nil || selectedExe != rootExe {
		return fmt.Errorf("Go executable does not belong to selected toolchain")
	}
	for _, c := range p.Components {
		actual, err := filepath.EvalSymlinks(filepath.Join(goroot, c.Path))
		if err != nil || !goWithin(goroot, actual) {
			return fmt.Errorf("Go component escapes selected toolchain")
		}

		got, e := goFileDigest(filepath.Join(p.GOROOT, c.Path))
		if e != nil || got != c.Digest {
			return fmt.Errorf("Go component pin mismatch: %s", c.Path)
		}
		seen[c.Path] = true
	}
	// These command families may invoke compile, asm, cgo and link. Every one
	// must be pinned before any command executes, including safe error paths.
	for _, tool := range []string{"compile", "asm", "cgo", "link"} {
		if !seen[filepath.Join("pkg/tool", runtime.GOOS+"_"+runtime.GOARCH, tool)] {
			return fmt.Errorf("Go component pin missing: %s", tool)
		}
	}
	copy := p
	copy.Identity = ""
	raw, _ := json.Marshal(copy)
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != p.Identity {
		return fmt.Errorf("Go runtime identity mismatch")
	}
	return nil
}

type NativeGoCommand struct{}

func (NativeGoCommand) Name() string { return "native.go_command.v1" }
func (NativeGoCommand) Supports(p Policy) error {
	if p.Mode != ModeGoCommand {
		return fmt.Errorf("Go command mode required")
	}
	return p.Validate()
}
func (a NativeGoCommand) Start(ctx context.Context, inv Invocation) (InvocationHandle, error) {
	if ctx == nil {
		return nil, fmt.Errorf("Go context absent")
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
		return nil, fmt.Errorf("Go invocation identity mismatch")
	}
	// Copy all binding collections so the caller cannot alter grants after Start.
	copy := *b.GoCommand
	copy.ReadRoots = append([]GoResourceRoot{}, copy.ReadRoots...)
	copy.Runtime.CCompiler = cloneGoCCompilerPin(copy.Runtime.CCompiler)
	copy.Runtime.Components = append([]GoComponentPin{}, copy.Runtime.Components...)
	if copy.Runtime.QualifiedTargets != nil {
		copy.Runtime.QualifiedTargets = append([]string{}, copy.Runtime.QualifiedTargets...)
	}
	if copy.Runtime.Defaults.Tags != nil {
		copy.Runtime.Defaults.Tags = append([]string{}, copy.Runtime.Defaults.Tags...)
	}
	b.GoCommand = &copy
	if e := verifyGoRuntime(copy.Runtime); e != nil {
		return nil, e
	}
	roots := map[string]string{"workspace": b.Directory, "stage": copy.Scratch, "toolchain": copy.Runtime.GOROOT, "module_cache": copy.ModuleCache}
	for _, r := range copy.ReadRoots {
		roots[r.ID] = r.Directory
	}
	canonical := map[string]string{}
	for id, p := range roots {
		real, e := filepath.EvalSymlinks(p)
		if e != nil {
			return nil, e
		}
		st, e := os.Stat(real)
		if e != nil || !st.IsDir() {
			return nil, fmt.Errorf("Go read root unavailable: %s", id)
		}
		canonical[id] = real
	}
	writes := []string{copy.Scratch, copy.CompilerCache}
	for i, p := range writes {
		real, e := filepath.EvalSymlinks(p)
		if e != nil {
			return nil, e
		}
		writes[i] = real
		if c := copy.Runtime.CCompiler; c != nil && (goWithin(c.Root, real) || goWithin(real, c.Root)) {
			return nil, fmt.Errorf("Go writable state overlaps selected C source resources")
		}
		for id, root := range canonical {
			if id == "stage" {
				continue
			}
			if goWithin(root, real) || goWithin(real, root) {
				return nil, fmt.Errorf("Go scratch overlaps read resource: %s", id)
			}
		}
	}
	if goWithin(writes[0], writes[1]) || goWithin(writes[1], writes[0]) {
		return nil, fmt.Errorf("Go scratch/cache must be distinct")
	}
	child, cancel := context.WithCancel(ctx)
	op := inv.Policy.Operations[0]
	return &goCommandHandle{ctx: child, cancel: cancel, op: op, binding: b, roots: canonical, writes: writes, receipt: Receipt{Identity: id, PolicyDigest: digest, Operation: op.ID, Adapter: a.Name(), Outcome: "unused", Transmission: "not_sent"}}, nil
}
func goWithin(root, p string) bool {
	rel, e := filepath.Rel(root, p)
	return e == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

type goCommandHandle struct {
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	op      Operation
	binding Binding
	roots   map[string]string
	writes  []string
	receipt Receipt
	used    bool
}

func (h *goCommandHandle) Cancel() error    { h.cancel(); return nil }
func (h *goCommandHandle) Receipt() Receipt { h.mu.Lock(); defer h.mu.Unlock(); return h.receipt }
func (h *goCommandHandle) path(ref GoResourcePath) (string, error) {
	root, ok := h.roots[ref.Root]
	if !ok || !cleanGoPath(ref.Path) {
		return "", fmt.Errorf("unauthorized Go resource path")
	}
	p := filepath.Join(root, ref.Path)
	real, e := filepath.EvalSymlinks(p)
	if e != nil || !goWithin(root, real) {
		return "", fmt.Errorf("Go resource escapes declared root")
	}
	return real, nil
}
func (h *goCommandHandle) Invoke(ctx context.Context, raw []byte) (Result, error) {
	h.mu.Lock()
	if h.used {
		h.mu.Unlock()
		return Result{}, fmt.Errorf("Go operation already used")
	}
	h.used = true
	h.mu.Unlock()
	fail := func(e error) (Result, error) {
		h.mu.Lock()
		h.receipt.Outcome = "refused"
		h.receipt.DeniedRequests++
		r := h.receipt
		h.mu.Unlock()
		return Result{Receipt: r}, e
	}
	if h.ctx.Err() != nil {
		return fail(h.ctx.Err())
	}
	if ctx == nil {
		return fail(fmt.Errorf("Go invocation context absent"))
	}
	if ctx.Err() != nil {
		return fail(ctx.Err())
	}
	var req OperationRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF || req.Operation != "" && req.Operation != h.op.ID || req.URL != "" || len(req.PathParameters) != 0 || len(req.QueryParameters) != 0 || len(req.RepeatedQueryParameters) != 0 {
		return fail(fmt.Errorf("invalid Go command request"))
	}
	var in GoCommandRequest
	dec = json.NewDecoder(bytes.NewReader(req.Body))
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil || dec.Decode(new(any)) != io.EOF || h.op.MaxRequestBytes > 0 && len(req.Body) > h.op.MaxRequestBytes {
		return fail(fmt.Errorf("invalid typed Go command input"))
	}
	dir, e := h.path(in.Directory)
	if e != nil {
		return fail(e)
	}
	if in.Directory.Root != "workspace" && in.Directory.Root != "stage" {
		return fail(fmt.Errorf("Go command directory is not analysis workspace"))
	}
	if h.op.GoCommand.Profile == "compiler_exports.v1" && in.Directory.Root != "stage" {
		return fail(fmt.Errorf("compiler exports require captured analysis image"))
	}
	if len(in.Packages) == 0 || (in.ModuleMode != "readonly" && in.ModuleMode != "vendor") {
		return fail(fmt.Errorf("Go package selection or module mode absent"))
	}
	for _, p := range in.Packages {
		if p == "" || strings.HasPrefix(p, "-") || strings.ContainsAny(p, "\\\x00\r\n ") || p == ".." || strings.HasPrefix(p, "../") || filepath.IsAbs(p) {
			return fail(fmt.Errorf("invalid Go package selector"))
		}
	}
	for _, value := range []string{in.Context.GOOS, in.Context.GOARCH} {
		if !validToken(value) {
			return fail(fmt.Errorf("invalid Go target"))
		}
	}
	tags := append([]string{}, in.Context.Tags...)
	sort.Strings(tags)
	for _, tag := range tags {
		if !validToken(tag) {
			return fail(fmt.Errorf("invalid Go build tag"))
		}
	}
	gowork := "off"
	if in.WorkspaceFile != nil {
		gowork, e = h.path(*in.WorkspaceFile)
		if e != nil {
			return fail(e)
		}
		if in.WorkspaceFile.Root != in.Directory.Root || !goWithin(dir, gowork) || filepath.Base(gowork) != "go.work" {
			return fail(fmt.Errorf("Go workspace file must belong to selected analysis resource"))
		}
	}
	args := []string{"list", "-e", "-deps", "-json", "-compiled", "-mod=" + in.ModuleMode}
	if h.op.GoCommand.Profile == "compiler_exports.v1" {
		args = append(args, "-export")
	}
	if in.Context.IncludeTests {
		args = append(args, "-test")
	}
	if len(tags) > 0 {
		args = append(args, "-tags="+strings.Join(tags, ","))
	}
	args = append(args, "--")
	args = append(args, in.Packages...)
	if e := verifyGoRuntime(h.binding.GoCommand.Runtime); e != nil {
		return fail(e)
	}
	temp, e := os.MkdirTemp(h.writes[0], "go-command-")
	if e != nil {
		return fail(e)
	}
	defer os.RemoveAll(temp)
	commandCtx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	if h.op.TimeoutMillis > 0 {
		var tc context.CancelFunc
		commandCtx, tc = context.WithTimeout(commandCtx, time.Duration(h.op.TimeoutMillis)*time.Millisecond)
		defer tc()
	}
	image := ""
	if h.op.GoCommand.Profile == "compiler_exports.v1" {
		if in.ImmutableImage == nil || in.ImmutableImage.Root != "stage" {
			return fail(fmt.Errorf("immutable compiler image absent"))
		}
		image, e = h.path(*in.ImmutableImage)
		if e != nil || !goWithin(image, dir) || image == h.roots["stage"] {
			return fail(fmt.Errorf("invalid compiler image"))
		}
	}
	if image != "" {
		if err := verifyGoImageManifest(commandCtx, image); err != nil {
			return fail(err)
		}
	}
	effective, err := prepareGoRuntimeImage(commandCtx, h.binding.GoCommand.Runtime, h.writes[0])
	if err != nil {
		return fail(err)
	}
	if image != "" {
		if in.ToolchainImage == nil || in.ToolchainImage.Root != "stage" {
			return fail(fmt.Errorf("captured toolchain input image absent"))
		}
		dataRoot, e := h.path(*in.ToolchainImage)
		if e != nil || !goWithin(image, dataRoot) {
			return fail(fmt.Errorf("captured toolchain inputs escape immutable image"))
		}
		effective, e = prepareGoAnalysisRuntime(commandCtx, effective, dataRoot, image, h.writes[0])
		if e != nil {
			return fail(e)
		}
	}
	commandRoots := map[string]string{}
	for k, v := range h.roots {
		commandRoots[k] = v
	}
	commandRoots["runtime_image"] = effective.GOROOT
	commandRoots["runtime_binaries"] = filepath.Dir(filepath.Dir(effective.Executable))
	cIdentity := ""
	effective.CCompiler = nil
	selectedC := h.binding.GoCommand.Runtime.CCompiler
	if in.Context.CgoEnabled && selectedC != nil {
		qualified := false
		for _, target := range selectedC.QualifiedTargets {
			if target == in.Context.GOOS+"/"+in.Context.GOARCH {
				qualified = true
			}
		}
		if qualified {
			effective.CCompiler, e = prepareGoCCompilerImage(commandCtx, selectedC, h.writes[0])
			if e != nil {
				return fail(e)
			}
			cIdentity = selectedC.ManifestDigest
			commandRoots["c_runtime_image"] = effective.CCompiler.Root
		}
	}
	// Compiler exports consume only the captured analysis image and verified
	// native runtime images. Original source roots cannot supply late headers.
	if image != "" {
		for key := range commandRoots {
			if key != "stage" && key != "runtime_image" && key != "runtime_binaries" && key != "c_runtime_image" {
				delete(commandRoots, key)
			}
		}
	}

	cmd, e := goSandboxCommand(commandCtx, effective, args, commandRoots, h.writes, image)
	if e != nil {
		return fail(e)
	}
	cmd.Dir = dir
	cgo := "0"
	if in.Context.CgoEnabled {
		cgo = "1"
	}
	cmd.Env = []string{"PATH=/usr/bin:/bin", "GOROOT=" + effective.GOROOT, "GOTOOLCHAIN=local", "GOENV=off", "GOWORK=" + gowork, "GOPROXY=off", "GOSUMDB=off", "GOFLAGS=", "GODEBUG=", "GOCACHE=" + h.writes[1], "GOMODCACHE=" + h.roots["module_cache"], "GOPATH=" + temp, "HOME=" + temp, "TMPDIR=" + temp, "GOTMPDIR=" + temp, "GOOS=" + in.Context.GOOS, "GOARCH=" + in.Context.GOARCH, "CGO_ENABLED=" + cgo, "GOVCS=*:off", "GOTELEMETRY=off", "CC=/usr/bin/false", "CXX=/usr/bin/false", "PKG_CONFIG=/usr/bin/false"}
	if effective.CCompiler != nil {
		cc, err := goCCompilerCommand(effective.CCompiler, effective.CCompiler.CC)
		if err != nil {
			return fail(err)
		}
		cxx, err := goCCompilerCommand(effective.CCompiler, effective.CCompiler.CXX)
		if err != nil {
			return fail(err)
		}
		cmd.Env = append(cmd.Env, "CC="+cc, "CXX="+cxx)
		cmd.Env = append(cmd.Env, "CGO_CFLAGS=-O2 -g", "CGO_CPPFLAGS=", "CGO_CXXFLAGS=-O2 -g", "CGO_FFLAGS=", "CGO_LDFLAGS=-O2 -g")
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	h.mu.Lock()
	h.receipt.Requests++
	h.receipt.Transmission = "local_execution"
	h.mu.Unlock()
	e = cmd.Run()
	exit := 0
	if e != nil {
		if ex, ok := e.(*exec.ExitError); ok {
			exit = ex.ExitCode()
			fmt.Fprintf(&stderr, "\nGo command failed: %s\n", ex.Error())
		} else {
			return fail(e)
		}
	}
	if commandCtx.Err() != nil {
		return fail(commandCtx.Err())
	}
	out := GoCommandResult{CCompilerIdentity: cIdentity, Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exit, RuntimeIdentity: h.binding.GoCommand.Runtime.Identity, Profile: h.op.GoCommand.Profile, EffectiveGOROOT: effective.GOROOT}
	body, _ := json.Marshal(out)
	response, _ := json.Marshal(OperationResponse{StatusCode: 200, ContentType: "application/json", Body: body, ResourceIdentity: "sha256:" + goRootHash(h.roots["workspace"])})
	h.mu.Lock()
	h.receipt.Responses++
	h.receipt.Outcome = "completed"
	receipt := h.receipt
	h.mu.Unlock()
	return Result{Response: response, Receipt: receipt}, nil
}
func goRootHash(p string) string { s := sha256.Sum256([]byte(p)); return hex.EncodeToString(s[:]) }

func (h *goCommandHandle) Accounting() Accounting { return ReceiptAccounting(h.Receipt()) }
func (h *goCommandHandle) Do(ctx context.Context, req OperationRequest) (OperationResponse, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return OperationResponse{}, err
	}
	result, err := h.Invoke(ctx, raw)
	if err != nil {
		return OperationResponse{}, err
	}
	var response OperationResponse
	if err = json.Unmarshal(result.Response, &response); err != nil {
		return OperationResponse{}, err
	}
	return response, nil
}

// prepareGoRuntimeImage freezes the exact executable bytes before launching
// them. The Go command and its compiler children execute this caller-owned
// image, so a later replacement of the selected toolchain cannot substitute
// code between a digest check and exec. Toolchain data stays on declared
// read-only resources and becomes captured analysis evidence.
func prepareGoRuntimeImage(ctx context.Context, p GoRuntimePin, scratch string) (GoRuntimePin, error) {
	dir := filepath.Join(scratch, ".go-runtime-"+p.Identity)
	effective := p
	effective.GOROOT = dir
	effective.Executable = filepath.Join(dir, "bin/go")
	verify := func() error {
		for _, pin := range append([]GoComponentPin{{Path: "bin/go", Digest: p.ExecutableDigest}}, p.Components...) {
			got, e := goFileDigest(filepath.Join(dir, pin.Path))
			if e != nil || got != pin.Digest {
				return fmt.Errorf("retained Go runtime image changed: %s", pin.Path)
			}
		}
		return nil
	}
	if _, e := os.Stat(dir); e == nil {
		real, e := filepath.EvalSymlinks(dir)
		if e != nil || real != dir {
			return GoRuntimePin{}, fmt.Errorf("Go runtime image root substituted")
		}
		if e = verify(); e != nil {
			return GoRuntimePin{}, e
		}
		return effective, nil
	} else if !os.IsNotExist(e) {
		return GoRuntimePin{}, e
	}
	pending, e := os.MkdirTemp(scratch, ".go-runtime-pending-")
	if e != nil {
		return GoRuntimePin{}, e
	}
	defer os.RemoveAll(pending)
	for _, pin := range append([]GoComponentPin{{Path: "bin/go", Digest: p.ExecutableDigest}}, p.Components...) {
		if ctx.Err() != nil {
			return GoRuntimePin{}, ctx.Err()
		}
		source := filepath.Join(p.GOROOT, pin.Path)
		if pin.Path == "bin/go" {
			source = p.Executable
		}
		raw, e := os.ReadFile(source)
		if e != nil {
			return GoRuntimePin{}, e
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != pin.Digest {
			return GoRuntimePin{}, fmt.Errorf("runtime source changed during freezing: %s", pin.Path)
		}
		target := filepath.Join(pending, pin.Path)
		if e = os.MkdirAll(filepath.Dir(target), 0700); e != nil {
			return GoRuntimePin{}, e
		}
		if e = os.WriteFile(target, raw, 0500); e != nil {
			return GoRuntimePin{}, e
		}
	}
	entries, e := os.ReadDir(p.GOROOT)
	if e != nil {
		return GoRuntimePin{}, e
	}
	for _, entry := range entries {
		if entry.Name() == "bin" || entry.Name() == "pkg" {
			continue
		}
		if e = os.Symlink(filepath.Join(p.GOROOT, entry.Name()), filepath.Join(pending, entry.Name())); e != nil {
			return GoRuntimePin{}, e
		}
	}
	entries, e = os.ReadDir(filepath.Join(p.GOROOT, "pkg"))
	if e != nil {
		return GoRuntimePin{}, e
	}
	for _, entry := range entries {
		if entry.Name() == "tool" {
			continue
		}
		if e = os.Symlink(filepath.Join(p.GOROOT, "pkg", entry.Name()), filepath.Join(pending, "pkg", entry.Name())); e != nil {
			return GoRuntimePin{}, e
		}
	}
	if e = os.Rename(pending, dir); e != nil {
		if ve := verify(); ve != nil {
			return GoRuntimePin{}, e
		}
	}
	if e = verify(); e != nil {
		return GoRuntimePin{}, e
	}
	return effective, nil
}

func verifyGoImageManifest(ctx context.Context, dir string) error {
	real, e := filepath.EvalSymlinks(dir)
	if e != nil || real != dir {
		return fmt.Errorf("analysis image root is not canonical")
	}
	root, e := os.OpenRoot(dir)
	if e != nil {
		return e
	}
	defer root.Close()
	raw, e := root.ReadFile(".analysis-manifest.json")
	if e != nil {
		return fmt.Errorf("analysis image manifest absent")
	}
	sha := sha256.Sum256(raw)
	if filepath.Base(dir) != "analysis-image-"+hex.EncodeToString(sha[:]) {
		return fmt.Errorf("analysis image manifest identity differs")
	}
	var files []GoImageFile
	if json.Unmarshal(raw, &files) != nil || len(files) == 0 {
		return fmt.Errorf("invalid analysis image manifest")
	}
	expected := map[string]string{}
	for _, file := range files {
		if !cleanGoPath(file.Path) || file.Data != "" || expected[file.Path] != "" {
			return fmt.Errorf("invalid analysis image witness")
		}
		expected[file.Path] = file.Digest
	}
	seen := map[string]bool{}
	e = fs.WalkDir(root.FS(), ".", func(p string, entry fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("analysis image symlink refused")
		}
		if entry.IsDir() || p == ".analysis-manifest.json" {
			return nil
		}
		digest, ok := expected[p]
		if !ok {
			return fmt.Errorf("analysis image unplanned file: %s", p)
		}
		data, e := root.ReadFile(p)
		if e != nil || goDataHash(data) != digest {
			return fmt.Errorf("analysis image bytes differ: %s", p)
		}
		seen[p] = true
		return nil
	})
	if e != nil {
		return e
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("analysis image capture incomplete")
	}
	return nil
}

func prepareGoAnalysisRuntime(ctx context.Context, runtime GoRuntimePin, dataRoot, image, scratch string) (GoRuntimePin, error) {
	dir := filepath.Join(scratch, ".go-analysis-runtime-"+runtime.Identity+"-"+strings.TrimPrefix(filepath.Base(image), "analysis-image-"))
	out := runtime
	out.GOROOT = dir
	if _, e := os.Stat(dir); os.IsNotExist(e) {
		pending, e := os.MkdirTemp(scratch, ".go-analysis-runtime-pending-")
		if e != nil {
			return GoRuntimePin{}, e
		}
		defer os.RemoveAll(pending)
		if e = os.Mkdir(filepath.Join(pending, "pkg"), 0700); e != nil {
			return GoRuntimePin{}, e
		}
		if e = os.Symlink(filepath.Join(runtime.GOROOT, "pkg/tool"), filepath.Join(pending, "pkg/tool")); e != nil {
			return GoRuntimePin{}, e
		}
		entries, e := os.ReadDir(dataRoot)
		if e != nil {
			return GoRuntimePin{}, e
		}
		for _, entry := range entries {
			if entry.Name() == "pkg" {
				continue
			}
			if e = os.Symlink(filepath.Join(dataRoot, entry.Name()), filepath.Join(pending, entry.Name())); e != nil {
				return GoRuntimePin{}, e
			}
		}
		entries, e = os.ReadDir(filepath.Join(dataRoot, "pkg"))
		if e != nil && !os.IsNotExist(e) {
			return GoRuntimePin{}, e
		}
		for _, entry := range entries {
			if entry.Name() == "tool" {
				return GoRuntimePin{}, fmt.Errorf("analysis inputs cannot provide executable tools")
			}
			if e = os.Symlink(filepath.Join(dataRoot, "pkg", entry.Name()), filepath.Join(pending, "pkg", entry.Name())); e != nil {
				return GoRuntimePin{}, e
			}
		}
		if ctx.Err() != nil {
			return GoRuntimePin{}, ctx.Err()
		}
		if e = os.Rename(pending, dir); e != nil {
			if _, se := os.Stat(dir); se != nil {
				return GoRuntimePin{}, e
			}
		}
	} else if e != nil {
		return GoRuntimePin{}, e
	}
	real, e := filepath.EvalSymlinks(dir)
	if e != nil || real != dir {
		return GoRuntimePin{}, fmt.Errorf("analysis runtime root substituted")
	}
	tool, e := filepath.EvalSymlinks(filepath.Join(dir, "pkg/tool"))
	if e != nil || tool != filepath.Join(runtime.GOROOT, "pkg/tool") {
		return GoRuntimePin{}, fmt.Errorf("analysis runtime tool pin substituted")
	}
	source, e := filepath.EvalSymlinks(filepath.Join(dir, "src"))
	if e != nil || source != filepath.Join(dataRoot, "src") {
		return GoRuntimePin{}, fmt.Errorf("analysis runtime input mapping substituted")
	}
	return out, nil
}
