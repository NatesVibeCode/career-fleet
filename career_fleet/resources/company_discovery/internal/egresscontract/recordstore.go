package egresscontract

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const ModeRecordStore = "record_store"

type RecordStorePermission struct {
	Namespace  string `json:"namespace"`
	WaitMillis int64  `json:"wait_ms"`
}
type LocalRecordRequest struct {
	ScopeKey       string `json:"scope_key"`
	Key            string `json:"key"`
	Content        string `json:"content"`
	ExpectedDigest string `json:"expected_digest"`
}

// LocalRecordCommitRequest names an exact desired value. Content is absent for
// retention confirmation and present (including an empty string) for publication.
type LocalRecordCommitRequest struct {
	ScopeKey       string  `json:"scope_key"`
	Key            string  `json:"key"`
	ExpectedDigest string  `json:"expected_digest"`
	DesiredDigest  string  `json:"desired_digest"`
	Content        *string `json:"content,omitempty"`
}

// LocalRecordCommitResult is a compact witness over the retained bytes. It
// never transports the record's content back through the commit boundary.
type LocalRecordCommitResult struct {
	Status string `json:"status"`
	Key    string `json:"key"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
	Found  bool   `json:"found"`
}
type LocalRecord struct {
	Key     string `json:"key"`
	Content string `json:"content"`
	Digest  string `json:"digest"`
	Found   bool   `json:"found"`
}
type LocalRecordResult struct {
	Status  string        `json:"status"`
	Record  LocalRecord   `json:"record"`
	Records []LocalRecord `json:"records"`
}

func recordKey(key string) bool {
	if len(key) != 64 {
		return false
	}
	for _, r := range key {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}
func validateRecordStoreOperation(op Operation) error {
	if op.RecordStore == nil || !validToken(op.RecordStore.Namespace) || !validToken(op.ID) || op.Destination.Kind != DestinationFilesystem || op.Destination.Path != "." || len(op.Destination.Origins) != 0 || len(op.Credentials) != 0 || op.WorkspaceDelivery != nil || op.Filesystem != nil || op.Browser != nil || len(op.Headers) != 0 || len(op.QueryParameters) != 0 || op.RedirectPolicy != RedirectNone || op.MaxRedirects != 0 {
		return fmt.Errorf("record store operation invalid")
	}
	switch op.Method {
	case "READ", "WRITE", "COMMIT", "LIST":
		if op.RecordStore.WaitMillis != 0 {
			return fmt.Errorf("record delay only belongs to wait")
		}
	case "WAIT":
		if op.RecordStore.WaitMillis <= 0 {
			return fmt.Errorf("record wait requires its configured observation cadence")
		}
	default:
		return fmt.Errorf("record store method invalid")
	}
	return nil
}

type NativeRecordStore struct{}

func (NativeRecordStore) Name() string { return "native.local_record_store.v1" }
func (NativeRecordStore) Supports(p Policy) error {
	if p.Mode != ModeRecordStore {
		return fmt.Errorf("record store policy required")
	}
	return p.Validate()
}
func (a NativeRecordStore) Start(ctx context.Context, inv Invocation) (InvocationHandle, error) {
	if ctx == nil {
		return nil, fmt.Errorf("record context absent")
	}
	if e := a.Supports(inv.Policy); e != nil {
		return nil, e
	}
	b, e := validateWorkspaceDeliveryBinding(inv.Policy, inv.Binding)
	if e != nil {
		return nil, e
	}
	pd, _ := inv.Policy.Digest()
	id := inv.Identity
	if id.RunID == "" || id.ProcessDigest == "" || id.NodeKey == "" || id.NodeName == "" || id.NodeDigest == "" || id.NodeSpecDigest == "" || id.PolicyDigest != pd || id.Operation != b.Operation {
		return nil, fmt.Errorf("record identity mismatch")
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
	return &recordHandle{ctx: child, cancel: cancel, root: root, op: op, receipt: Receipt{Identity: id, PolicyDigest: pd, Operation: op.ID, Adapter: a.Name(), Outcome: "unused", Transmission: "not_sent"}}, nil
}

type recordHandle struct {
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	root    *os.Root
	op      Operation
	receipt Receipt
	closed  bool
}

func (h *recordHandle) Cancel() error {
	h.cancel()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	return h.root.Close()
}
func (h *recordHandle) Receipt() Receipt       { h.mu.Lock(); defer h.mu.Unlock(); return h.receipt }
func (h *recordHandle) Accounting() Accounting { return ReceiptAccounting(h.Receipt()) }
func (h *recordHandle) Invoke(ctx context.Context, raw []byte) (Result, error) {
	var req OperationRequest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(&req); e != nil {
		return Result{}, e
	}
	if d.Decode(new(any)) != io.EOF {
		return Result{}, fmt.Errorf("record envelope invalid")
	}
	out, e := h.Do(ctx, req)
	b, _ := json.Marshal(out)
	return Result{Response: b, Receipt: h.Receipt()}, e
}
func (h *recordHandle) read(scope, key string) (LocalRecord, error) {
	r := LocalRecord{Key: key}
	raw, e := h.root.ReadFile(".collaboration/" + h.op.RecordStore.Namespace + "/" + scope + "/" + key + ".json")
	if os.IsNotExist(e) {
		return r, nil
	}
	if e != nil {
		return r, e
	}
	r.Found = true
	r.Content = string(raw)
	r.Digest = workspaceHash(raw)
	return r, nil
}

type recordCommitReader struct {
	caller, invocation context.Context
	file               *os.File
}

func (r recordCommitReader) Read(p []byte) (int, error) {
	if err := r.caller.Err(); err != nil {
		return 0, err
	}
	if err := r.invocation.Err(); err != nil {
		return 0, err
	}
	return r.file.Read(p)
}

// Retention needs an exact digest and size, not a second in-memory copy of the
// index. Hash the held regular file, then confirm its named storage identity.
func (h *recordHandle) retainedWitness(ctx context.Context, scope, key string) (LocalRecord, int64, error) {
	record := LocalRecord{Key: key}
	name := ".collaboration/" + h.op.RecordStore.Namespace + "/" + scope + "/" + key + ".json"
	file, err := h.root.Open(name)
	if os.IsNotExist(err) {
		return record, 0, nil
	}
	if err != nil {
		return record, 0, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return record, 0, err
	}
	if !before.Mode().IsRegular() {
		return record, 0, fmt.Errorf("record commit requires retained regular bytes")
	}
	digest := sha256.New()
	size, err := io.Copy(digest, recordCommitReader{caller: ctx, invocation: h.ctx, file: file})
	if err != nil {
		return record, 0, err
	}
	after, err := file.Stat()
	if err != nil {
		return record, 0, err
	}
	named, err := h.root.Stat(name)
	if err != nil {
		return record, 0, err
	}
	if !os.SameFile(before, named) || !os.SameFile(before, after) || before.Size() != size || after.Size() != size || !before.ModTime().Equal(after.ModTime()) || !after.ModTime().Equal(named.ModTime()) || named.Size() != size {
		return record, 0, fmt.Errorf("record commit retained bytes changed during observation")
	}
	record.Found, record.Digest = true, hex.EncodeToString(digest.Sum(nil))
	return record, size, nil
}

// Commit waits for the existing publication lock until it becomes available or
// its caller cancels. The observation cadence is not a retry or runtime limit.
func (h *recordHandle) commitLock(ctx context.Context, file *os.File) error {
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN && err != syscall.EINTR {
			return err
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-h.ctx.Done():
			timer.Stop()
			return h.ctx.Err()
		case <-timer.C:
		}
	}
}

// Open an existing lock first. For concurrent first publication, exclusive
// creation followed by an existing-file open avoids the observed Darwin/Go
// Root.OpenFile O_CREATE race without changing the operation's lock identity.
func (h *recordHandle) commitPublicationFile(name string) (*os.File, error) {
	file, err := h.root.OpenFile(name, os.O_RDWR, 0o600)
	if err == nil || !os.IsNotExist(err) {
		return file, err
	}
	file, err = h.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if os.IsExist(err) {
		return h.root.OpenFile(name, os.O_RDWR, 0o600)
	}
	return file, err
}

func (h *recordHandle) Do(ctx context.Context, envelope OperationRequest) (OperationResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	refuse := func(e error) (OperationResponse, error) {
		h.receipt.Outcome = "refused"
		h.receipt.DeniedRequests++
		return OperationResponse{}, e
	}
	if h.closed {
		return refuse(fmt.Errorf("record closed"))
	}
	if e := ctx.Err(); e != nil {
		return refuse(e)
	}
	if e := h.ctx.Err(); e != nil {
		return refuse(e)
	}
	if envelope.Operation != "" && envelope.Operation != h.op.ID || envelope.URL != "" || len(envelope.PathParameters) != 0 || len(envelope.QueryParameters) != 0 || len(envelope.RepeatedQueryParameters) != 0 {
		return refuse(fmt.Errorf("record operation mismatch"))
	}
	var req LocalRecordRequest
	var commit *LocalRecordCommitRequest
	d := json.NewDecoder(bytes.NewReader(envelope.Body))
	d.DisallowUnknownFields()
	if h.op.Method == "COMMIT" {
		var requested LocalRecordCommitRequest
		if e := d.Decode(&requested); e != nil {
			return refuse(e)
		}
		var fields map[string]json.RawMessage
		if e := json.Unmarshal(envelope.Body, &fields); e != nil {
			return refuse(e)
		}
		if _, present := fields["content"]; present && requested.Content == nil {
			return refuse(fmt.Errorf("record commit content must be a string when present"))
		}
		if !recordKey(requested.DesiredDigest) || requested.ExpectedDigest != "" && !recordKey(requested.ExpectedDigest) {
			return refuse(fmt.Errorf("record commit digest invalid"))
		}
		if requested.Content != nil && workspaceHash([]byte(*requested.Content)) != requested.DesiredDigest {
			return refuse(fmt.Errorf("record commit content differs from desired digest"))
		}
		commit = &requested
		req.ScopeKey, req.Key, req.ExpectedDigest = requested.ScopeKey, requested.Key, requested.ExpectedDigest
		if requested.Content != nil {
			req.Content = *requested.Content
		}
	} else {
		if e := d.Decode(&req); e != nil {
			return refuse(e)
		}
	}
	if d.Decode(new(any)) != io.EOF || !recordKey(req.ScopeKey) || h.op.Method != "LIST" && h.op.Method != "WAIT" && !recordKey(req.Key) {
		return refuse(fmt.Errorf("record request invalid"))
	}
	result := LocalRecordResult{Status: "read", Records: []LocalRecord{}}
	var witness *LocalRecordCommitResult
	directory := ".collaboration/" + h.op.RecordStore.Namespace + "/" + req.ScopeKey
	if h.op.Method == "LIST" || h.op.Method == "WAIT" {
		if h.op.Method == "WAIT" {
			timer := time.NewTimer(time.Duration(h.op.RecordStore.WaitMillis) * time.Millisecond)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return refuse(ctx.Err())
			case <-h.ctx.Done():
				return refuse(h.ctx.Err())
			case <-timer.C:
			}
		}
		if req.Key != "" || req.Content != "" || req.ExpectedDigest != "" {
			return refuse(fmt.Errorf("record list selectors invalid"))
		}
		dir, e := h.root.Open(directory)
		if os.IsNotExist(e) {
			result.Status = "empty"
		} else if e != nil {
			return refuse(e)
		} else {
			entries, e := dir.ReadDir(-1)
			dir.Close()
			if e != nil {
				return refuse(e)
			}
			for _, entry := range entries {
				key := strings.TrimSuffix(entry.Name(), ".json")
				if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && recordKey(key) && entry.Name() == key+".json" {
					r, e := h.read(req.ScopeKey, key)
					if e != nil {
						return refuse(e)
					}
					result.Records = append(result.Records, r)
				}
			}
			sort.Slice(result.Records, func(i, j int) bool { return result.Records[i].Key < result.Records[j].Key })
		}
	} else if h.op.Method == "READ" {
		if req.Content != "" || req.ExpectedDigest != "" {
			return refuse(fmt.Errorf("record read payload invalid"))
		}
		r, e := h.read(req.ScopeKey, req.Key)
		if e != nil {
			return refuse(e)
		}
		result.Record = r
	} else {
		if e := h.root.MkdirAll(directory, 0o700); e != nil {
			return refuse(e)
		}
		var lock *os.File
		var e error
		if commit != nil {
			lock, e = h.commitPublicationFile(directory + "/publication.lock")
		} else {
			lock, e = h.root.OpenFile(directory+"/publication.lock", os.O_CREATE|os.O_RDWR, 0o600)
		}
		if e != nil {
			return refuse(e)
		}
		defer lock.Close()
		var lockError error
		if commit != nil {
			lockError = h.commitLock(ctx, lock)
		} else {
			lockError = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		}
		if lockError != nil {
			if commit != nil {
				return refuse(fmt.Errorf("record commit lock: %w", lockError))
			}
			return refuse(fmt.Errorf("record publication in progress"))
		}
		defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		if commit != nil {
			if e := ctx.Err(); e != nil {
				return refuse(e)
			}
			if e := h.ctx.Err(); e != nil {
				return refuse(e)
			}
		}
		var old LocalRecord
		var retainedSize int64
		if commit != nil {
			old, retainedSize, e = h.retainedWitness(ctx, req.ScopeKey, req.Key)
		} else {
			old, e = h.read(req.ScopeKey, req.Key)
			retainedSize = int64(len(old.Content))
		}
		if e != nil {
			return refuse(e)
		}
		if commit != nil && old.Found && old.Digest == commit.DesiredDigest {
			// No temporary file, file sync, or replacement belongs to this path.
			result.Status = "retained"
			result.Record = old
		} else if old.Digest != req.ExpectedDigest || commit != nil && commit.Content == nil {
			result.Status = "stale"
			result.Record = old
		} else {
			temporary := directory + "/" + req.Key + "-" + workspaceHash([]byte(req.Content)) + ".pending"
			if commit != nil {
				defer h.root.Remove(temporary)
			}
			file, e := h.root.OpenFile(temporary, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if e != nil {
				return refuse(e)
			}
			_, we := file.WriteString(req.Content)
			se := file.Sync()
			ce := file.Close()
			if we != nil {
				return refuse(we)
			}
			if se != nil {
				return refuse(se)
			}
			if ce != nil {
				return refuse(ce)
			}
			if commit != nil {
				if e := ctx.Err(); e != nil {
					return refuse(e)
				}
				if e := h.ctx.Err(); e != nil {
					return refuse(e)
				}
			}
			if e := h.root.Rename(temporary, path.Join(directory, req.Key+".json")); e != nil {
				return refuse(e)
			}
			result.Status = "written"
			result.Record = LocalRecord{Key: req.Key, Content: req.Content, Digest: workspaceHash([]byte(req.Content)), Found: true}
			retainedSize = int64(len(req.Content))
		}
		if commit != nil {
			witness = &LocalRecordCommitResult{Status: result.Status, Key: result.Record.Key, Digest: result.Record.Digest, Size: retainedSize, Found: result.Record.Found}
		}
	}
	h.receipt.Requests++
	h.receipt.Responses++
	h.receipt.Transmission = "sent"
	h.receipt.Outcome = "completed"
	var body []byte
	if witness != nil {
		body, _ = json.Marshal(witness)
	} else {
		body, _ = json.Marshal(result)
	}
	return OperationResponse{StatusCode: 200, Body: body, ContentType: "application/json"}, nil
}
