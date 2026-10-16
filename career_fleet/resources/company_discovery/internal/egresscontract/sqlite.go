package egresscontract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

func validateSQLiteOperation(op Operation) error {
	if op.Filesystem != nil || op.Browser != nil || !validToken(op.ID) || (op.Method != "READ" && op.Method != "WRITE") || op.Destination.Kind != DestinationSQLite || !validSQLiteName(op.Destination.Path) || len(op.Destination.Origins) != 0 || len(op.Credentials) != 0 || len(op.Headers) != 0 || len(op.QueryParameters) != 0 || op.MaxRedirects != 0 || op.RedirectPolicy != RedirectNone {
		return fmt.Errorf("invalid SQLite operation")
	}
	if op.MaxRequests < 1 || op.MaxRequestBytes < 0 || op.MaxResponseBytes < 0 || op.TimeoutMillis < 0 || op.TimeoutMillis > int64(^uint64(0)>>1)/int64(time.Millisecond) {
		return fmt.Errorf("invalid SQLite operation bounds")
	}

	pinned := false
	for _, material := range op.RuntimeMaterial {
		if material.ID == "sqlite" && material.Kind == "executable" && validInferenceSHA256(material.SHA256) {
			pinned = true
		}
	}
	if !pinned {
		return fmt.Errorf("SQLite requires an exact sqlite executable permission")
	}
	return nil
}
func validSQLiteName(s string) bool {
	if len(s) < 8 || !strings.HasSuffix(s, ".sqlite") {
		return false
	}
	for _, r := range strings.TrimSuffix(s, ".sqlite") {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
func validateSQLiteBinding(p Policy, b Binding) (Binding, error) {
	if b.Browser != nil || b.Directory == "" || !filepath.IsAbs(b.Directory) || filepath.Clean(b.Directory) != b.Directory || strings.ContainsAny(b.Directory, "\x00\r\n") || len(b.Origins) > 0 || len(b.Routes) > 0 || b.CredentialRef != "" {
		return Binding{}, fmt.Errorf("SQLite requires an explicit local directory without network or credential bindings")
	}
	for _, op := range p.Operations {
		if op.ID == b.Operation {
			return b, nil
		}
	}
	return Binding{}, fmt.Errorf("undeclared SQLite operation")
}

// NativeSQLite is a local service adapter. It executes SQL authored by the
// Process Node using the OS SQLite CLI; it does not select tables or outcomes.
// Safe mode confines SQL to the one policy-pinned database. No shell is used.
type NativeSQLite struct{}

func (NativeSQLite) Name() string { return "native.trusted_sqlite.v1" }
func (NativeSQLite) Supports(p Policy) error {
	if p.Mode != ModeSQLite {
		return fmt.Errorf("SQLite policy required")
	}
	if runtime.GOOS == "windows" {
		return fmt.Errorf("native SQLite currently requires a Unix SQLite CLI")
	}
	return p.Validate()
}
func (a NativeSQLite) Start(ctx context.Context, inv Invocation) (InvocationHandle, error) {
	if ctx == nil {
		return nil, fmt.Errorf("SQLite context absent")
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
		return nil, fmt.Errorf("SQLite invocation identity mismatch")
	}
	root, err := filepath.EvalSymlinks(b.Directory)
	if err != nil {
		return nil, fmt.Errorf("SQLite owner directory unavailable")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("SQLite owner directory invalid")
	}
	b, cleanup, err := snapshotRuntimeMaterial(inv.Policy, b)
	if err != nil {
		return nil, err
	}
	binary := ""
	for _, material := range b.RuntimeMaterial {
		if material.ID == "sqlite" {
			binary = material.Path
		}
	}
	if binary == "" {
		cleanup()
		return nil, fmt.Errorf("SQLite executable binding absent")
	}
	var op Operation
	for _, candidate := range inv.Policy.Operations {
		if candidate.ID == b.Operation {
			op = candidate
		}
	}
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(child, cleanup)
	return &sqliteHandle{ctx: child, cancel: cancel, cleanup: func() { stop(); cleanup() }, policy: inv.Policy, binding: b, op: op, path: filepath.Join(root, op.Destination.Path), binary: binary, receipt: Receipt{Identity: id, PolicyDigest: digest, Operation: op.ID, Adapter: a.Name(), Outcome: "unused", Transmission: "not_sent"}}, nil
}

type sqliteHandle struct {
	policy       Policy
	binding      Binding
	mu           sync.Mutex
	ctx          context.Context
	cancel       context.CancelFunc
	cleanup      func()
	op           Operation
	path, binary string
	closed       bool
	receipt      Receipt
}

func (h *sqliteHandle) Cancel() error {
	h.cancel()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	h.cleanup()
	return nil
}
func (h *sqliteHandle) Receipt() Receipt { h.mu.Lock(); defer h.mu.Unlock(); return h.receipt }
func (h *sqliteHandle) Invoke(ctx context.Context, raw []byte) (Result, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var req OperationRequest
	if d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF {
		return Result{}, fmt.Errorf("invalid SQLite request")
	}
	response, err := h.Do(ctx, req)
	encoded, _ := json.Marshal(response)
	return Result{Response: encoded, Receipt: h.Receipt()}, err
}

type sqliteBoundedBuffer struct {
	bytes.Buffer
	max int
}

func (b *sqliteBoundedBuffer) Write(p []byte) (int, error) {
	if b.max > 0 && len(p) > b.max-b.Len() {
		return 0, fmt.Errorf("SQLite response bounds exceeded")
	}
	return b.Buffer.Write(p)
}
func (h *sqliteHandle) Do(ctx context.Context, req OperationRequest) (OperationResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	empty := OperationResponse{}
	if ctx == nil || h.closed || h.ctx.Err() != nil || ctx.Err() != nil {
		return empty, fmt.Errorf("SQLite invocation cancelled")
	}
	if (req.Operation != "" && req.Operation != h.op.ID) || req.URL != "" || len(req.PathParameters) > 0 || len(req.QueryParameters) > 0 || len(req.RepeatedQueryParameters) > 0 || len(req.Body) == 0 || (h.op.MaxRequestBytes > 0 && len(req.Body) > h.op.MaxRequestBytes) || !utf8.Valid(req.Body) || bytes.ContainsRune(req.Body, 0) {
		return empty, fmt.Errorf("SQLite request refused before execution")
	}
	for _, line := range strings.Split(string(req.Body), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), ".") {
			return empty, fmt.Errorf("SQLite CLI commands forbidden")
		}
	}
	if h.receipt.Requests >= h.op.MaxRequests {
		return empty, fmt.Errorf("SQLite request budget exhausted")
	}
	for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
		if info, err := os.Lstat(h.path + suffix); err == nil {
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return empty, fmt.Errorf("SQLite database custody invalid")
			}
		} else if !os.IsNotExist(err) {
			return empty, fmt.Errorf("SQLite database unavailable")
		}
	}
	if _, err := os.Stat(h.path); os.IsNotExist(err) {
		if h.op.Method == "READ" {
			h.receipt.Requests++
			h.receipt.Responses++
			h.receipt.Outcome = "database_absent"
			h.receipt.Transmission = "response_received"
			return OperationResponse{URL: h.op.Destination.Path, Body: []byte("[]"), ContentType: "application/json", ContentSignal: "database_absent"}, nil
		}
		file, e := os.OpenFile(h.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return empty, fmt.Errorf("SQLite creation failed")
		}
		if e = file.Close(); e != nil {
			return empty, e
		}
	}
	if h.op.Method == "WRITE" {
		if err := os.Chmod(h.path, 0600); err != nil {
			return empty, fmt.Errorf("SQLite custody permissions unavailable")
		}
	}
	callCtx, cancel := context.WithCancel(h.ctx)
	if h.op.TimeoutMillis > 0 {
		cancel()
		callCtx, cancel = context.WithTimeout(h.ctx, time.Duration(h.op.TimeoutMillis)*time.Millisecond)
	}
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	args := []string{"-safe", "-nofollow", "-batch", "-bail", "-json", "-init", "/dev/null"}
	if h.op.Method == "READ" {
		args = append(args, "-readonly")
	}
	args = append(args, h.path)
	if err := validateRuntimeMaterialBinding(h.policy, h.binding); err != nil {
		return OperationResponse{}, err
	}
	cmd := exec.CommandContext(callCtx, h.binary, args...)
	// Deployment environment variables cannot inject loader paths, startup
	// configuration, credentials, or provider behavior into the selected CLI.
	cmd.Env = []string{"LANG=C.UTF-8"}
	cmd.Stdin = bytes.NewReader(req.Body)
	output := &sqliteBoundedBuffer{max: h.op.MaxResponseBytes}
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	h.receipt.Requests++
	h.receipt.Transmission = "attempted_unknown"
	if err := cmd.Run(); err != nil {
		h.receipt.Outcome = "execution_failed"
		return empty, fmt.Errorf("SQLite execution failed or refused")
	}
	body := bytes.TrimSpace(output.Bytes())
	if len(body) == 0 {
		body = []byte("[]")
	}
	if !json.Valid(body) {
		h.receipt.Outcome = "response_invalid"
		return empty, fmt.Errorf("SQLite result must be one JSON document")
	}
	h.receipt.Responses++
	h.receipt.Outcome = "success"
	h.receipt.Transmission = "response_received"
	return OperationResponse{URL: h.op.Destination.Path, Body: append([]byte(nil), body...), ContentType: "application/json"}, nil
}
