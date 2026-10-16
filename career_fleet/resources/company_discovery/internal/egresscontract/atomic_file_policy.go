package egresscontract

import (
	"fmt"
	"path/filepath"
	"strings"
)

const ModeAtomicFile = "atomic_file"

// AtomicFilePermission pins filenames in one deployment-owned directory.
// LockMetadataWrite explicitly permits creation/use of the persistent lock,
// including for read/reconciliation; it does not grant record publication.
type AtomicFilePermission struct {
	RecordFile        string `json:"record_file"`
	LockFile          string `json:"lock_file"`
	LockMetadataWrite bool   `json:"lock_metadata_write"`
}

// AtomicFilePin is an exact existence/raw-byte precondition, not parsed content
// or authority. The scoped adapter binds deployment root and both filenames.
type AtomicFilePin struct {
	Exists bool   `json:"exists"`
	SHA256 string `json:"sha256"`
}
type AtomicFileSnapshot struct {
	Pin   AtomicFilePin `json:"pin"`
	Bytes []byte        `json:"bytes"`
}
type AtomicFileWriteResult struct {
	Before          AtomicFilePin `json:"before"`
	BeforeObserved  bool          `json:"before_observed"`
	After           AtomicFilePin `json:"after"`
	AfterObserved   bool          `json:"after_observed"`
	Prepared        AtomicFilePin `json:"prepared"`
	Published       bool          `json:"published"`
	DirectorySynced bool          `json:"directory_synced"`
}
type AtomicFileReconciliation struct {
	Verdict        string             `json:"verdict"`
	Observed       AtomicFileSnapshot `json:"observed"`
	PreparedSHA256 string             `json:"prepared_sha256"`
}

// AtomicFileAcknowledgement records a new durability observation on existing
// exact bytes. It never asserts that any earlier REPLACE published/succeeded.
// Partial sync flags survive technical failure and do not imply acknowledgement
// of Expected unless exact final observation and all identity checks succeed.
type AtomicFileAcknowledgement struct {
	Expected              AtomicFilePin `json:"expected"`
	Observed              AtomicFilePin `json:"observed"`
	BeforeObserved        bool          `json:"before_observed"`
	FileSynced            bool          `json:"file_synced"`
	DirectorySynced       bool          `json:"directory_synced"`
	AfterObserved         bool          `json:"after_observed"`
	DirectoryObjectSHA256 string        `json:"directory_object_sha256"`
}

// AtomicFileRequest carries only exact values, never a root/path or a claimed
// durability acknowledgement. RECONCILE is conservative readback; retained
// canonical write receipts are evaluated by the printed Process separately.
type AtomicFileRequest struct {
	Expected              *AtomicFilePin `json:"expected,omitempty"`
	Content               []byte         `json:"content,omitempty"`
	ContentSHA256         string         `json:"content_sha256,omitempty"`
	DirectoryObjectSHA256 string         `json:"directory_object_sha256,omitempty"`
}
type AtomicFileResult struct {
	Schema          string                     `json:"schema"`
	Method          string                     `json:"method"`
	Snapshot        *AtomicFileSnapshot        `json:"snapshot,omitempty"`
	Write           *AtomicFileWriteResult     `json:"write,omitempty"`
	Reconciliation  *AtomicFileReconciliation  `json:"reconciliation,omitempty"`
	Acknowledgement *AtomicFileAcknowledgement `json:"acknowledgement,omitempty"`
}
type AtomicFileReceipt struct {
	Schema                string                     `json:"schema"`
	DirectorySHA256       string                     `json:"directory_sha256"`
	DirectoryObjectSHA256 string                     `json:"directory_object_sha256"`
	RecordFile            string                     `json:"record_file"`
	LockFile              string                     `json:"lock_file"`
	Method                string                     `json:"method"`
	Expected              *AtomicFilePin             `json:"expected,omitempty"`
	Prepared              *AtomicFilePin             `json:"prepared,omitempty"`
	Observed              *AtomicFilePin             `json:"observed,omitempty"`
	Write                 *AtomicFileWriteResult     `json:"write,omitempty"`
	Acknowledgement       *AtomicFileAcknowledgement `json:"acknowledgement,omitempty"`
	FailureStage          string                     `json:"failure_stage,omitempty"`
}

func atomicFileName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && !strings.ContainsAny(name, "/\\\x00\r\n")
}
func validateAtomicFileOperation(op Operation) error {
	p := op.AtomicFile
	if p == nil || !validToken(op.ID) || !atomicFileName(p.RecordFile) || !atomicFileName(p.LockFile) || p.RecordFile == p.LockFile || !p.LockMetadataWrite || op.Destination.Kind != DestinationFilesystem || op.Destination.Path != "." || len(op.Destination.Origins) != 0 || len(op.Credentials) != 0 || op.RecordStore != nil || op.WorkspaceDelivery != nil || op.Filesystem != nil || op.Browser != nil || len(op.Headers) != 0 || len(op.QueryParameters) != 0 || op.MaxRedirects != 0 || op.RedirectPolicy != RedirectNone {
		return fmt.Errorf("one exact atomic record/lock permission required")
	}
	if op.Method != "READ" && op.Method != "REPLACE" && op.Method != "RECONCILE" && op.Method != "ACKNOWLEDGE" {
		return fmt.Errorf("unsupported atomic file method")
	}
	// One scoped invocation is one operation. There are no byte/time/retry quotas.
	if op.MaxRequests != 1 || op.MaxRequestBytes != 0 || op.MaxResponseBytes != 0 || op.TimeoutMillis != 0 {
		return fmt.Errorf("atomic file operation is one uncapped context-cancellable effect")
	}
	return nil
}
func validateAtomicFileBinding(p Policy, b Binding) (Binding, error) {
	if b.Browser != nil || b.Directory == "" || !filepath.IsAbs(b.Directory) || filepath.Clean(b.Directory) != b.Directory || strings.ContainsAny(b.Directory, "\x00\r\n") || len(b.Origins) != 0 || len(b.Routes) != 0 || b.CredentialRef != "" {
		return Binding{}, fmt.Errorf("explicit deployment-owned atomic directory required without network credentials")
	}
	for _, op := range p.Operations {
		if op.ID == b.Operation {
			return b, nil
		}
	}
	return Binding{}, fmt.Errorf("undeclared atomic file operation")
}
