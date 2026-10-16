//go:build darwin || linux

package egresscontract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

type NativeConditionalRecord struct{}

func (NativeConditionalRecord) Name() string { return "native.conditional_record.v2" }
func (NativeConditionalRecord) Supports(p Policy) error {
	if p.Mode != ModeConditionalRecord {
		return fmt.Errorf("conditional record policy required")
	}
	return p.Validate()
}
func (a NativeConditionalRecord) Start(ctx context.Context, inv Invocation) (InvocationHandle, error) {
	if ctx == nil {
		return nil, fmt.Errorf("invocation context required")
	}
	if err := a.Supports(inv.Policy); err != nil {
		return nil, err
	}
	binding, err := ValidateBinding(inv.Policy, inv.Binding)
	if err != nil {
		return nil, err
	}
	digest, _ := inv.Policy.Digest()
	id := inv.Identity
	if id.RunID == "" || id.ProcessDigest == "" || id.NodeKey == "" || id.NodeName == "" || id.NodeDigest == "" || id.NodeSpecDigest == "" || id.PolicyDigest != digest || id.Operation != binding.Operation {
		return nil, fmt.Errorf("conditional invocation identity mismatch")
	}
	op := inv.Policy.Operations[0]
	permission := *op.ConditionalRecord
	op.ConditionalRecord = &permission
	store, err := OpenConditionalRecordStore(binding.Directory, permission.Record)
	if err != nil {
		return nil, err
	}
	object, err := store.directory.DirectoryObjectDigest()
	if err != nil {
		store.Close()
		return nil, err
	}
	child, cancel := context.WithCancel(ctx)
	receipt := Receipt{Identity: id, PolicyDigest: digest, Operation: op.ID, Adapter: a.Name(), Outcome: "unused", Transmission: "not_sent", ConditionalRecord: &ConditionalRecordReceipt{Schema: ConditionalRecordSchema, Record: permission.Record, DirectorySHA256: atomicSHA([]byte(binding.Directory)), DirectoryObject: object, Method: op.Method}}
	return &conditionalRecordHandle{ctx: child, cancel: cancel, store: store, op: op, receipt: receipt}, nil
}

type conditionalRecordHandle struct {
	mu      sync.Mutex // invocation accounting only; not a work or cross-process lock
	ctx     context.Context
	cancel  context.CancelFunc
	store   *ConditionalRecordStore
	op      Operation
	receipt Receipt
	closed  bool
}

func (h *conditionalRecordHandle) Cancel() error {
	h.cancel()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	h.closed = true
	return h.store.Close()
}
func (h *conditionalRecordHandle) Receipt() Receipt {
	h.mu.Lock()
	defer h.mu.Unlock()
	raw, _ := json.Marshal(h.receipt)
	var out Receipt
	_ = json.Unmarshal(raw, &out)
	return out
}
func (h *conditionalRecordHandle) Accounting() Accounting { return ReceiptAccounting(h.Receipt()) }
func (h *conditionalRecordHandle) Invoke(ctx context.Context, raw []byte) (Result, error) {
	var req OperationRequest
	if err := atomicDecode(raw, &req); err != nil {
		return Result{Receipt: h.Receipt()}, err
	}
	out, err := h.Do(ctx, req)
	encoded, marshalErr := json.Marshal(out)
	return Result{Response: encoded, Receipt: h.Receipt()}, errors.Join(err, marshalErr)
}
func (h *conditionalRecordHandle) Do(caller context.Context, envelope OperationRequest) (OperationResponse, error) {
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
		return refuse(fmt.Errorf("conditional invocation closed or cancelled"))
	}
	if h.receipt.Requests != 0 || envelope.Operation != "" && envelope.Operation != h.op.ID || envelope.URL != "" || len(envelope.PathParameters) != 0 || len(envelope.QueryParameters) != 0 || len(envelope.RepeatedQueryParameters) != 0 {
		return refuse(fmt.Errorf("conditional operation outside selected scope"))
	}
	var args ConditionalRecordRequest
	if err := atomicDecode(envelope.Body, &args); err != nil {
		return refuse(err)
	}
	var members map[string]json.RawMessage
	_ = json.Unmarshal(envelope.Body, &members)
	if members == nil {
		return refuse(fmt.Errorf("conditional object required"))
	}
	if h.op.Method == "READ" {
		if len(members) != 0 {
			return refuse(fmt.Errorf("read accepts no caller selectors"))
		}
	} else {
		required := []string{"expected", "directory_object"}
		if h.op.Method == "OBSERVE" {
			required = append(required, "update_id", "content_sha256")
		}
		if h.op.Method == "PUBLISH" {
			required = append(required, "update_id", "content", "content_sha256")
		}
		if len(members) != len(required) {
			return refuse(fmt.Errorf("exact conditional fields required"))
		}
		for _, name := range required {
			if members[name] == nil || bytes.Equal(bytes.TrimSpace(members[name]), []byte("null")) {
				return refuse(fmt.Errorf("explicit conditional field required: %s", name))
			}
		}
		if args.Expected == nil || !conditionalPinValid(*args.Expected) || args.DirectoryObject != h.receipt.ConditionalRecord.DirectoryObject {
			return refuse(fmt.Errorf("exact revision and original directory identity required"))
		}
		var pin map[string]json.RawMessage
		_ = json.Unmarshal(members["expected"], &pin)
		if len(pin) != 3 {
			return refuse(fmt.Errorf("explicit revision, existence and digest required"))
		}
		for _, name := range []string{"revision", "exists", "sha256"} {
			if pin[name] == nil || bytes.Equal(bytes.TrimSpace(pin[name]), []byte("null")) {
				return refuse(fmt.Errorf("explicit predecessor field required"))
			}
		}
		if h.op.Method == "PUBLISH" && (args.UpdateID == "" || args.Content == nil || args.ContentSHA256 != atomicSHA(args.Content)) {
			return refuse(fmt.Errorf("exact update identity and content digest required"))
		}
		if h.op.Method == "OBSERVE" && (args.UpdateID == "" || !atomicPinValid(AtomicFilePin{Exists: true, SHA256: args.ContentSHA256})) {
			return refuse(fmt.Errorf("exact observed update identity and content digest required"))
		}
		if h.op.Method == "ACKNOWLEDGE" && !args.Expected.Exists {
			return refuse(fmt.Errorf("existing revision required for acknowledgement"))
		}
	}
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	stop := context.AfterFunc(caller, cancel)
	defer stop()
	h.receipt.Requests = 1
	result := ConditionalRecordResult{Schema: ConditionalRecordSchema, Method: h.op.Method}
	var err error
	switch h.op.Method {
	case "READ":
		var value ConditionalRecordSnapshot
		value, err = h.store.Read(ctx)
		result.Snapshot = &value
	case "OBSERVE":
		var value ConditionalRecordObservation
		value, err = h.store.Observe(ctx, *args.Expected, args.UpdateID, args.ContentSHA256)
		result.Observation = &value
	case "PUBLISH":
		var value ConditionalRecordPublication
		value, err = h.store.CompareAndPublish(ctx, *args.Expected, args.UpdateID, args.Content)
		result.Publication = &value
	case "ACKNOWLEDGE":
		var value ConditionalRecordAcknowledgement
		value, err = h.store.Acknowledge(ctx, *args.Expected)
		result.Acknowledgement = &value
	}
	h.receipt.ConditionalRecord.Result = &result
	raw, marshalErr := json.Marshal(result)
	err = errors.Join(err, marshalErr)
	response := OperationResponse{Body: raw, ContentType: "application/json"}
	if err != nil {
		h.receipt.Outcome = "failed"
		h.receipt.ConditionalRecord.FailureStage = "operation"
		return response, err
	}
	h.receipt.Responses = 1
	h.receipt.Outcome = "succeeded"
	response.StatusCode = 200
	return response, nil
}
