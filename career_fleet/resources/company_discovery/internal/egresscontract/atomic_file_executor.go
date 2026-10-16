//go:build darwin || linux

package egresscontract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"unicode/utf8"
)

// NativeAtomicFile performs one policy-pinned operation. It owns no envelope
// meaning, DAG, retry, recovery choice or source selection.
type NativeAtomicFile struct{}

func (NativeAtomicFile) Name() string { return "native.atomic_file.v1" }
func (NativeAtomicFile) Supports(p Policy) error {
	if p.Mode != ModeAtomicFile {
		return fmt.Errorf("atomic file policy required")
	}
	return p.Validate()
}
func (a NativeAtomicFile) Start(ctx context.Context, inv Invocation) (InvocationHandle, error) {
	if ctx == nil {
		return nil, fmt.Errorf("atomic file context absent")
	}
	if err := a.Supports(inv.Policy); err != nil {
		return nil, err
	}
	binding, err := ValidateBinding(inv.Policy, inv.Binding)
	if err != nil {
		return nil, err
	}
	pd, _ := inv.Policy.Digest()
	id := inv.Identity
	if id.RunID == "" || id.ProcessDigest == "" || id.NodeKey == "" || id.NodeName == "" || id.NodeDigest == "" || id.NodeSpecDigest == "" || id.PolicyDigest != pd || id.Operation != binding.Operation {
		return nil, fmt.Errorf("atomic file invocation identity mismatch")
	}
	op := inv.Policy.Operations[0]
	permission := *op.AtomicFile
	op.AtomicFile = &permission
	store, err := OpenAtomicFileStore(binding.Directory, permission.RecordFile, permission.LockFile)
	if err != nil {
		return nil, err
	}
	objectDigest, err := store.DirectoryObjectDigest()
	if err != nil {
		store.Close()
		return nil, err
	}
	child, cancel := context.WithCancel(ctx)
	receipt := Receipt{Identity: id, PolicyDigest: pd, Operation: op.ID, Adapter: a.Name(), Outcome: "unused", Transmission: "not_sent", AtomicFile: &AtomicFileReceipt{Schema: "praxis.atomic_file_receipt.v1", DirectorySHA256: atomicSHA([]byte(binding.Directory)), DirectoryObjectSHA256: objectDigest, RecordFile: permission.RecordFile, LockFile: permission.LockFile, Method: op.Method}}
	return &atomicFileHandle{ctx: child, cancel: cancel, store: store, op: op, receipt: receipt}, nil
}

type atomicFileHandle struct {
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	store   *AtomicFileStore
	op      Operation
	receipt Receipt
	closed  bool
}

func (h *atomicFileHandle) Cancel() error {
	h.cancel()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	h.closed = true
	return h.store.Close()
}
func (h *atomicFileHandle) Receipt() Receipt {
	h.mu.Lock()
	defer h.mu.Unlock()
	raw, _ := json.Marshal(h.receipt)
	var detached Receipt
	_ = json.Unmarshal(raw, &detached)
	return detached
}
func (h *atomicFileHandle) Accounting() Accounting { return ReceiptAccounting(h.Receipt()) }
func (h *atomicFileHandle) Invoke(ctx context.Context, raw []byte) (Result, error) {
	var req OperationRequest
	if err := atomicDecode(raw, &req); err != nil {
		return Result{Receipt: h.Receipt()}, err
	}
	out, err := h.Do(ctx, req)
	encoded, _ := json.Marshal(out)
	return Result{Response: encoded, Receipt: h.Receipt()}, err
}
func (h *atomicFileHandle) Do(caller context.Context, envelope OperationRequest) (OperationResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	refuse := func(err error) (OperationResponse, error) {
		h.receipt.DeniedRequests++
		if h.receipt.Requests == 0 {
			h.receipt.Outcome = "refused"
		}
		return OperationResponse{}, err
	}
	if caller == nil || h.closed || h.ctx.Err() != nil || caller.Err() != nil {
		return refuse(fmt.Errorf("atomic invocation closed or cancelled"))
	}
	if h.receipt.Requests != 0 || envelope.Operation != "" && envelope.Operation != h.op.ID || envelope.URL != "" || len(envelope.PathParameters) != 0 || len(envelope.QueryParameters) != 0 || len(envelope.RepeatedQueryParameters) != 0 {
		return refuse(fmt.Errorf("atomic operation outside sealed scope"))
	}
	var args AtomicFileRequest
	if err := atomicDecode(envelope.Body, &args); err != nil {
		return refuse(err)
	}
	var members map[string]json.RawMessage
	_ = json.Unmarshal(envelope.Body, &members)
	if members == nil {
		return refuse(fmt.Errorf("atomic object required"))
	}
	if h.op.Method == "READ" {
		if len(members) != 0 {
			return refuse(fmt.Errorf("atomic read accepts no selectors"))
		}
	} else {
		if h.op.Method == "ACKNOWLEDGE" {
			if len(members) != 2 || members["expected"] == nil || members["directory_object_sha256"] == nil || args.DirectoryObjectSHA256 != h.receipt.AtomicFile.DirectoryObjectSHA256 || args.Expected == nil || !args.Expected.Exists || !atomicPinValid(*args.Expected) {
				return refuse(fmt.Errorf("acknowledgement requires exact existing bytes and original directory identity"))
			}
		} else if len(members) != 3 || members["expected"] == nil || members["content"] == nil || members["content_sha256"] == nil || args.Expected == nil || args.Content == nil || !atomicPinValid(*args.Expected) || args.ContentSHA256 != atomicSHA(args.Content) {
			return refuse(fmt.Errorf("exact expected existence/digest and prepared raw bytes required"))
		}
		var expectedMembers map[string]json.RawMessage
		_ = json.Unmarshal(members["expected"], &expectedMembers)
		if len(expectedMembers) != 2 || expectedMembers["exists"] == nil || expectedMembers["sha256"] == nil || bytes.Equal(expectedMembers["exists"], []byte("null")) || bytes.Equal(expectedMembers["sha256"], []byte("null")) {
			return refuse(fmt.Errorf("explicit atomic precondition fields required"))
		}
		pin := *args.Expected
		h.receipt.AtomicFile.Expected = &pin
		if h.op.Method != "ACKNOWLEDGE" {
			prepared := AtomicFilePin{true, args.ContentSHA256}
			h.receipt.AtomicFile.Prepared = &prepared
		}
	}
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	stop := context.AfterFunc(caller, cancel)
	defer stop()
	h.receipt.Requests = 1
	h.receipt.Transmission = "sent"
	h.receipt.Outcome = "pending"
	result := AtomicFileResult{Schema: "praxis.atomic_file_result.v1", Method: h.op.Method}
	var err error
	switch h.op.Method {
	case "READ":
		var observed AtomicFileSnapshot
		observed, err = h.store.Read(ctx)
		if err == nil {
			result.Snapshot = &observed
			pin := observed.Pin
			h.receipt.AtomicFile.Observed = &pin
		}
	case "REPLACE":
		var written AtomicFileWriteResult
		written, err = h.store.CompareAndReplace(ctx, *args.Expected, args.Content)
		result.Write = &written
		copy := written
		h.receipt.AtomicFile.Write = &copy
		if written.AfterObserved {
			pin := written.After
			h.receipt.AtomicFile.Observed = &pin
		}
	case "ACKNOWLEDGE":
		var ack AtomicFileAcknowledgement
		ack, err = h.store.Acknowledge(ctx, *args.Expected)
		result.Acknowledgement = &ack
		copy := ack
		h.receipt.AtomicFile.Acknowledgement = &copy
		if ack.AfterObserved {
			pin := ack.Observed
			h.receipt.AtomicFile.Observed = &pin
		}
	case "RECONCILE":
		var reconciled AtomicFileReconciliation
		reconciled, err = h.store.Reconcile(ctx, *args.Expected, args.Content, false)
		if err == nil {
			result.Reconciliation = &reconciled
			pin := reconciled.Observed.Pin
			h.receipt.AtomicFile.Observed = &pin
		}
	}
	raw, marshalErr := json.Marshal(result)
	err = errors.Join(err, marshalErr)
	response := OperationResponse{Body: raw, ContentType: "application/json"}
	if err != nil {
		h.receipt.Outcome = "failed"
		var atomicErr *AtomicFileError
		if errors.As(err, &atomicErr) {
			h.receipt.AtomicFile.FailureStage = atomicErr.Stage
		} else {
			h.receipt.AtomicFile.FailureStage = "operation"
		}
		return response, err
	}
	h.receipt.Responses = 1
	h.receipt.Outcome = "succeeded"
	response.StatusCode = 200
	return response, nil
}

// Strict operation framing rejects duplicate keys, trailing values and repaired
// Unicode before typed decoding. Record content remains opaque exact bytes.
func atomicDecode(raw []byte, target any) error {
	if !utf8.Valid(raw) {
		return fmt.Errorf("exact UTF-8 operation envelope required")
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] != '"' {
			continue
		}
		i++
		for i < len(raw) && raw[i] != '"' {
			if raw[i] == '\\' {
				i++
				if i >= len(raw) {
					break
				}
				if raw[i] == 'u' {
					if i+4 >= len(raw) {
						return fmt.Errorf("invalid Unicode escape")
					}
					n, ok := atomicHex(raw[i+1 : i+5])
					if !ok {
						return fmt.Errorf("invalid Unicode escape")
					}
					i += 4
					if n >= 0xd800 && n <= 0xdbff {
						if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
							return fmt.Errorf("unpaired Unicode surrogate")
						}
						lo, ok := atomicHex(raw[i+3 : i+7])
						if !ok || lo < 0xdc00 || lo > 0xdfff {
							return fmt.Errorf("unpaired Unicode surrogate")
						}
						i += 6
					} else if n >= 0xdc00 && n <= 0xdfff {
						return fmt.Errorf("unpaired Unicode surrogate")
					}
				}
			}
			i++
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var walk func() error
	walk = func() error {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return fmt.Errorf("duplicate operation JSON key")
				}
				for _, c := range name {
					if c >= 'A' && c <= 'Z' {
						return fmt.Errorf("noncanonical operation JSON key")
					}
				}
				seen[name] = true
				if err := walk(); err != nil {
					return err
				}
			}
		case '[':
			for dec.More() {
				if err := walk(); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("invalid operation JSON")
		}
		_, err = dec.Token()
		return err
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("trailing operation JSON")
	}
	typed := json.NewDecoder(bytes.NewReader(raw))
	typed.DisallowUnknownFields()
	if err := typed.Decode(target); err != nil {
		return err
	}
	return nil
}
func atomicHex(raw []byte) (int, bool) {
	n := 0
	for _, c := range raw {
		n *= 16
		switch {
		case c >= '0' && c <= '9':
			n += int(c - '0')
		case c >= 'a' && c <= 'f':
			n += int(c - 'a' + 10)
		case c >= 'A' && c <= 'F':
			n += int(c - 'A' + 10)
		default:
			return 0, false
		}
	}
	return n, true
}
