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
	"time"
	"unicode/utf8"
)

// FilesystemPermission bounds acquisition only. Search, interpretation, and
// repository relationships belong to the printed Nodes consuming these bytes.
type FilesystemPermission struct {
	MaxFiles           int      `json:"max_files"`
	MaxFileBytes       int      `json:"max_file_bytes"`
	MaxTotalBytes      int      `json:"max_total_bytes"`
	ExcludeDirectories []string `json:"exclude_directories"`
}

type WorkspaceFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Digest  string `json:"digest"`
	Size    int    `json:"size"`
}

type WorkspaceOmission struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type WorkspaceSnapshot struct {
	Path         string              `json:"path"`
	Patterns     []string            `json:"patterns"`
	Files        []WorkspaceFile     `json:"files"`
	Omissions    []WorkspaceOmission `json:"omissions"`
	Complete     bool                `json:"complete"`
	ScannedFiles int                 `json:"scanned_files"`
	ReadBytes    int                 `json:"read_bytes"`
	Digest       string              `json:"digest"`
	IgnoreDigest string              `json:"ignore_digest"`
}

func validateFilesystemOperation(op Operation) error {
	if op.Filesystem == nil || !validToken(op.ID) || op.Method != "SNAPSHOT" || op.Destination.Kind != DestinationFilesystem || op.Destination.Path != "." || len(op.Destination.Origins) != 0 || len(op.Credentials) != 0 || len(op.Headers) != 0 || len(op.QueryParameters) != 0 || op.Browser != nil || op.MaxRedirects != 0 || op.RedirectPolicy != RedirectNone {
		return fmt.Errorf("invalid read-only workspace operation")
	}
	p := op.Filesystem
	if op.MaxRequests != 1 || op.MaxRequestBytes < 1 || op.MaxResponseBytes < 1 || op.TimeoutMillis < 1 || op.TimeoutMillis > int64(^uint64(0)>>1)/int64(time.Millisecond) || p.MaxFiles < 1 || p.MaxFileBytes < 1 || p.MaxTotalBytes < p.MaxFileBytes {
		return fmt.Errorf("invalid workspace acquisition bounds")
	}
	for _, name := range p.ExcludeDirectories {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00\r\n") {
			return fmt.Errorf("invalid excluded directory")
		}
	}
	return nil
}

func validateFilesystemBinding(p Policy, b Binding) (Binding, error) {
	if b.Browser != nil || b.Directory == "" || !filepath.IsAbs(b.Directory) || filepath.Clean(b.Directory) != b.Directory || strings.ContainsAny(b.Directory, "\x00\r\n") || len(b.Origins) != 0 || len(b.Routes) != 0 || b.CredentialRef != "" {
		return Binding{}, fmt.Errorf("explicit caller-owned workspace required")
	}
	for _, op := range p.Operations {
		if op.ID == b.Operation {
			return b, nil
		}
	}
	return Binding{}, fmt.Errorf("undeclared workspace operation")
}

// NativeFilesystem reads through os.Root containment. It cannot write, spawn
// a process, select another root, or follow a symlink outside the root.
type NativeFilesystem struct{}

func (NativeFilesystem) Name() string { return "native.workspace_snapshot.v1" }
func (NativeFilesystem) Supports(p Policy) error {
	if p.Mode != ModeFilesystem {
		return fmt.Errorf("filesystem policy required")
	}
	return p.Validate()
}
func (a NativeFilesystem) Start(ctx context.Context, inv Invocation) (InvocationHandle, error) {
	if ctx == nil {
		return nil, fmt.Errorf("workspace context absent")
	}
	if err := a.Supports(inv.Policy); err != nil {
		return nil, err
	}
	b, err := ValidateBinding(inv.Policy, inv.Binding)
	if err != nil {
		return nil, err
	}
	digest, _ := inv.Policy.Digest()
	id := inv.Identity
	if id.RunID == "" || id.ProcessDigest == "" || id.NodeKey == "" || id.NodeName == "" || id.NodeDigest == "" || id.NodeSpecDigest == "" || id.PolicyDigest != digest || id.Operation != b.Operation {
		return nil, fmt.Errorf("workspace invocation identity mismatch")
	}
	canonical, err := filepath.EvalSymlinks(b.Directory)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, fmt.Errorf("workspace unavailable: %w", err)
	}
	child, cancel := context.WithCancel(ctx)
	var op Operation
	for _, candidate := range inv.Policy.Operations {
		if candidate.ID == b.Operation {
			op = candidate
			permission := *candidate.Filesystem
			permission.ExcludeDirectories = append([]string(nil), candidate.Filesystem.ExcludeDirectories...)
			op.Filesystem = &permission
		}
	}
	return &filesystemHandle{ctx: child, cancel: cancel, root: root, op: op, receipt: Receipt{Identity: id, PolicyDigest: digest, Operation: op.ID, Adapter: a.Name(), Outcome: "unused", Transmission: "not_sent"}}, nil
}

type filesystemHandle struct {
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	root    *os.Root
	op      Operation
	closed  bool
	receipt Receipt
}

func (h *filesystemHandle) Cancel() error {
	h.cancel()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	h.closed = true
	return h.root.Close()
}
func (h *filesystemHandle) Receipt() Receipt       { h.mu.Lock(); defer h.mu.Unlock(); return h.receipt }
func (h *filesystemHandle) Accounting() Accounting { return ReceiptAccounting(h.Receipt()) }
func (h *filesystemHandle) Invoke(ctx context.Context, raw []byte) (Result, error) {
	var req OperationRequest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF {
		return Result{}, fmt.Errorf("invalid workspace envelope")
	}
	response, err := h.Do(ctx, req)
	out, _ := json.Marshal(response)
	return Result{Response: out, Receipt: h.Receipt()}, err
}
func (h *filesystemHandle) Do(caller context.Context, request OperationRequest) (OperationResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	refuse := func(message string) (OperationResponse, error) {
		h.receipt.Outcome = "refused"
		h.receipt.DeniedRequests++
		return OperationResponse{}, fmt.Errorf("%s", message)
	}
	if caller == nil || h.closed || h.ctx.Err() != nil || caller.Err() != nil {
		return refuse("workspace invocation closed")
	}
	if h.receipt.Requests != 0 || request.Operation != "" && request.Operation != h.op.ID || request.URL != "" || len(request.PathParameters) != 0 || len(request.QueryParameters) != 0 || len(request.RepeatedQueryParameters) != 0 || len(request.Body) > h.op.MaxRequestBytes {
		return refuse("workspace request exceeds sealed scope")
	}
	var args struct {
		Path            string   `json:"path"`
		Patterns        []string `json:"patterns"`
		IncludeExcluded bool     `json:"include_excluded"`
	}
	d := json.NewDecoder(bytes.NewReader(request.Body))
	d.DisallowUnknownFields()
	if d.Decode(&args) != nil || d.Decode(new(any)) != io.EOF || !fs.ValidPath(args.Path) {
		return refuse("invalid workspace selection")
	}
	for _, pattern := range args.Patterns {
		if strings.ContainsAny(pattern, "\\\x00\r\n") {
			return refuse("invalid workspace pattern")
		}
		if _, err := path.Match(pattern, "probe"); err != nil {
			return refuse("invalid workspace pattern")
		}
	}
	ctx, cancel := context.WithTimeout(h.ctx, time.Duration(h.op.TimeoutMillis)*time.Millisecond)
	defer cancel()
	stop := context.AfterFunc(caller, cancel)
	defer stop()
	p := h.op.Filesystem
	out := WorkspaceSnapshot{Path: args.Path, Patterns: append([]string{}, args.Patterns...), Files: []WorkspaceFile{}, Omissions: []WorkspaceOmission{}, Complete: true}
	excluded := map[string]bool{}
	for _, name := range p.ExcludeDirectories {
		excluded[name] = true
	}
	omit := func(name, why string) {
		out.Complete = false
		out.Omissions = append(out.Omissions, WorkspaceOmission{Path: name, Reason: why})
	}
	h.receipt.Requests = 1
	h.receipt.Transmission = "sent"
	readBytes := 0
	rulesByDirectory := map[string][]workspaceIgnoreRule{}
	ignoreWitnesses := []WorkspaceFile{}
	loadRules := func(directory string, inherited []workspaceIgnoreRule) ([]workspaceIgnoreRule, error) {
		name := path.Join(directory, ".gitignore")
		info, err := h.root.Lstat(name)
		if os.IsNotExist(err) {
			return inherited, nil
		}
		if err != nil {
			return nil, fmt.Errorf("workspace ignore rules unavailable: %s", name)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return inherited, nil
		}
		if !info.Mode().IsRegular() || info.Size() > int64(p.MaxFileBytes) || int(info.Size()) > p.MaxTotalBytes-readBytes {
			return nil, fmt.Errorf("workspace ignore rules exceed acquisition policy: %s", name)
		}
		file, err := h.root.Open(name)
		if err != nil {
			return nil, err
		}
		before, err := file.Stat()
		if err != nil {
			file.Close()
			return nil, err
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, int64(p.MaxFileBytes)+1))
		after, statErr := file.Stat()
		file.Close()
		readBytes += len(raw)
		if readErr != nil || statErr != nil || len(raw) > p.MaxFileBytes || readBytes > p.MaxTotalBytes || before.Size() != after.Size() || before.ModTime() != after.ModTime() || !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
			return nil, fmt.Errorf("workspace ignore rules changed or unreadable: %s", name)
		}
		sum := sha256.Sum256(raw)
		ignoreWitnesses = append(ignoreWitnesses, WorkspaceFile{Path: name, Content: string(raw), Digest: "sha256:" + hex.EncodeToString(sum[:]), Size: len(raw)})
		return append(append([]workspaceIgnoreRule{}, inherited...), workspaceIgnoreRules(directory, name, string(raw))...), nil
	}
	// A narrow selection still inherits every rule between its parent and the
	// caller's root. An excluded parent cannot be resurrected by a child rule.
	if !args.IncludeExcluded {
		var inherited []workspaceIgnoreRule
		for _, directory := range workspaceAncestors(path.Dir(args.Path)) {
			if directory != "." {
				if ignored, rule := workspaceIgnored(inherited, directory, true); ignored {
					return refuse(fmt.Sprintf("workspace selection excluded by %s:%d; set include_excluded to include it", rule.source, rule.line))
				}
			}
			var loadErr error
			inherited, loadErr = loadRules(directory, inherited)
			if loadErr != nil {
				h.receipt.Outcome = "failed"
				return OperationResponse{}, loadErr
			}
			rulesByDirectory[directory] = inherited
		}
	}
	err := fs.WalkDir(h.root.FS(), args.Path, func(name string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if name == args.Path {
				return walkErr
			}
			omit(name, "unreadable")
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			omit(name, "symlink_excluded")
			return nil
		}
		if !args.IncludeExcluded && name != "." {
			if ignored, rule := workspaceIgnored(rulesByDirectory[path.Dir(name)], name, entry.IsDir()); ignored {
				omit(name, fmt.Sprintf("gitignore_excluded: %s:%d; set include_excluded to include it", rule.source, rule.line))
				if entry.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
		}
		if entry.IsDir() {
			if !args.IncludeExcluded && name != args.Path && (strings.HasPrefix(entry.Name(), ".") || excluded[entry.Name()]) {
				omit(name, "directory_excluded; set include_excluded to include it")
				return fs.SkipDir
			}
			if !args.IncludeExcluded {
				// Root rules were already loaded for a file selection; for a
				// directory selection this acquires that directory's own rules.
				if _, loaded := rulesByDirectory[name]; !loaded {
					var err error
					rulesByDirectory[name], err = loadRules(name, rulesByDirectory[path.Dir(name)])
					if err != nil {
						return err
					}
				}
			}
			return nil
		}
		match := len(args.Patterns) == 0
		for _, pattern := range args.Patterns {
			full, _ := path.Match(pattern, name)
			base, _ := path.Match(pattern, path.Base(name))
			match = match || full || base
		}
		if !match {
			return nil
		}
		out.ScannedFiles++
		if out.ScannedFiles > p.MaxFiles {
			omit(name, "file_scan_limit")
			return fs.SkipAll
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			omit(name, "non_regular_or_unreadable")
			return nil
		}
		if info.Size() > int64(p.MaxFileBytes) {
			omit(name, "file_byte_limit")
			return nil
		}
		if len(out.Files) >= p.MaxFiles || int(info.Size()) > p.MaxTotalBytes-readBytes {
			omit(name, "snapshot_limit")
			return fs.SkipAll
		}
		file, err := h.root.Open(name)
		if err != nil {
			omit(name, "unreadable")
			return nil
		}
		before, statErr := file.Stat()
		if statErr != nil || !before.Mode().IsRegular() {
			file.Close()
			omit(name, "non_regular_or_unreadable")
			return nil
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, int64(p.MaxFileBytes)+1))
		readBytes += len(raw)
		after, afterErr := file.Stat()
		file.Close()
		if readErr != nil || afterErr != nil || len(raw) > p.MaxFileBytes || readBytes > p.MaxTotalBytes || before.Size() != after.Size() || before.ModTime() != after.ModTime() {
			omit(name, "changed_or_unreadable")
			return nil
		}
		if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
			omit(name, "non_text")
			return nil
		}
		sum := sha256.Sum256(raw)
		out.ReadBytes += len(raw)
		out.Files = append(out.Files, WorkspaceFile{Path: name, Content: string(raw), Digest: "sha256:" + hex.EncodeToString(sum[:]), Size: len(raw)})
		return nil
	})
	if err != nil {
		h.receipt.Outcome = "failed"
		return OperationResponse{}, fmt.Errorf("workspace snapshot failed: %w", err)
	}
	ignoreRaw, _ := json.Marshal(ignoreWitnesses)
	ignoreSum := sha256.Sum256(ignoreRaw)
	out.IgnoreDigest = "sha256:" + hex.EncodeToString(ignoreSum[:])
	body, err := json.Marshal(out)
	if err != nil {
		return OperationResponse{}, err
	}
	sum := sha256.Sum256(body)
	out.Digest = "sha256:" + hex.EncodeToString(sum[:])
	body, err = json.Marshal(out)
	if err != nil || len(body) > h.op.MaxResponseBytes {
		h.receipt.Outcome = "failed"
		return OperationResponse{}, fmt.Errorf("workspace snapshot response exceeds bound")
	}
	h.receipt.Responses = 1
	h.receipt.Outcome = "completed"
	held, heldError := h.root.Stat(".")
	named, namedError := os.Stat(h.root.Name())
	if heldError != nil || namedError != nil || !os.SameFile(held, named) {
		h.receipt.Outcome = "failed"
		return OperationResponse{}, fmt.Errorf("workspace canonical resource changed during acquisition")
	}
	identity := sha256.Sum256([]byte(h.root.Name()))
	return OperationResponse{StatusCode: 200, Body: body, ContentType: "application/json", ResourceIdentity: "sha256:" + hex.EncodeToString(identity[:])}, nil
}
