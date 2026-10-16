package egresscontract

import (
	"fmt"
	"path/filepath"
	"strings"
)

const ConditionalRecordSchema = "praxis.conditional_record.v2"

type ConditionalRecordPin struct {
	Revision string `json:"revision"`
	Exists   bool   `json:"exists"`
	SHA256   string `json:"sha256"`
}
type ConditionalRecordSnapshot struct {
	Pin     ConditionalRecordPin `json:"pin"`
	Content []byte               `json:"content"`
}
type ConditionalRecordPublication struct {
	Status   string                    `json:"status"`
	Previous ConditionalRecordPin      `json:"previous"`
	Prepared ConditionalRecordPin      `json:"prepared"`
	Observed ConditionalRecordSnapshot `json:"observed"`
}

// Observation identifies an exact historical publication. It proves visibility, never durability.
type ConditionalRecordObservation struct {
	Status        string                        `json:"status"`
	Previous      ConditionalRecordPin          `json:"previous"`
	UpdateID      string                        `json:"update_id"`
	ContentSHA256 string                        `json:"content_sha256"`
	Publication   *ConditionalRecordPublication `json:"publication,omitempty"`
}

type ConditionalRecordAcknowledgement struct {
	Schema          string               `json:"schema"`
	Record          string               `json:"record"`
	Pin             ConditionalRecordPin `json:"pin"`
	DirectoryObject string               `json:"directory_object"`
	FileSynced      bool                 `json:"file_synced"`
	DirectorySynced bool                 `json:"directory_synced"`
}

const ModeConditionalRecord = "conditional_record"

type ConditionalRecordPermission struct {
	Record string `json:"record"`
}
type ConditionalRecordRequest struct {
	Expected        *ConditionalRecordPin `json:"expected,omitempty"`
	UpdateID        string                `json:"update_id,omitempty"`
	Content         []byte                `json:"content,omitempty"`
	ContentSHA256   string                `json:"content_sha256,omitempty"`
	DirectoryObject string                `json:"directory_object,omitempty"`
}
type ConditionalRecordResult struct {
	Schema          string                            `json:"schema"`
	Method          string                            `json:"method"`
	Snapshot        *ConditionalRecordSnapshot        `json:"snapshot,omitempty"`
	Publication     *ConditionalRecordPublication     `json:"publication,omitempty"`
	Acknowledgement *ConditionalRecordAcknowledgement `json:"acknowledgement,omitempty"`
	Observation     *ConditionalRecordObservation     `json:"observation,omitempty"`
}
type ConditionalRecordReceipt struct {
	Schema          string                   `json:"schema"`
	Record          string                   `json:"record"`
	DirectorySHA256 string                   `json:"directory_sha256"`
	DirectoryObject string                   `json:"directory_object"`
	Method          string                   `json:"method"`
	Result          *ConditionalRecordResult `json:"result,omitempty"`
	FailureStage    string                   `json:"failure_stage,omitempty"`
}

func validateConditionalRecordOperation(op Operation) error {
	p := op.ConditionalRecord
	if p == nil || !validToken(op.ID) || !atomicFileName(p.Record) || op.Destination.Kind != DestinationFilesystem || op.Destination.Path != "." || len(op.Destination.Origins) != 0 || len(op.Credentials) != 0 || op.AtomicFile != nil || op.NativeExec != nil || op.RecordStore != nil || op.WorkspaceDelivery != nil || op.Filesystem != nil || op.Browser != nil || len(op.Headers) != 0 || len(op.QueryParameters) != 0 || op.MaxRedirects != 0 || op.RedirectPolicy != RedirectNone {
		return fmt.Errorf("one exact conditional record permission required")
	}
	if op.Method != "READ" && op.Method != "PUBLISH" && op.Method != "ACKNOWLEDGE" && op.Method != "OBSERVE" {
		return fmt.Errorf("unsupported conditional record method")
	}
	if op.MaxRequests != 1 || op.MaxRequestBytes != 0 || op.MaxResponseBytes != 0 || op.TimeoutMillis != 0 {
		return fmt.Errorf("one uncapped context-cancellable record effect required")
	}
	return nil
}
func validateConditionalRecordBinding(p Policy, b Binding) (Binding, error) {
	if b.NativeExec != nil || b.Browser != nil || b.Directory == "" || !filepath.IsAbs(b.Directory) || filepath.Clean(b.Directory) != b.Directory || strings.ContainsAny(b.Directory, "\x00\r\n") || len(b.Origins) != 0 || len(b.Routes) != 0 || b.CredentialRef != "" {
		return Binding{}, fmt.Errorf("exact local conditional record directory required")
	}
	for _, op := range p.Operations {
		if op.ID == b.Operation {
			return b, nil
		}
	}
	return Binding{}, fmt.Errorf("undeclared conditional record operation")
}
