package egresscontract

import (
	"context"
	"fmt"
)

const Interactive = "interactive"
const Triggered = "triggered"

// CredentialGrant is separate from network permission. It authorizes customer
// custody resolution for exactly one process revision, node and operation.
// Interactive grants additionally expire with one run. Triggered grants are
// customer-managed preauthorization, never an implicit fallback.
type CredentialGrant struct {
	ProcessDigest string `json:"process_digest"`
	NodeKey       string `json:"node_key"`
	PolicyDigest  string `json:"policy_digest"`
	Operation     string `json:"operation"`
	Ref           string `json:"ref"`
	Mode          string `json:"mode"`
	RunID         string `json:"run_id,omitempty"`
	Source        string `json:"source"`
}

// SecretSource is customer custody, not a factory service. A source may resolve
// an environment slot, a node-keyed shard group, or a customer vault reference.
// Successful resolution transfers ownership of a fresh byte slice to the caller.
type SecretSource interface {
	ResolveSecret(context.Context, string) ([]byte, error)
}

// GrantedCredentials never caches secrets. Construction copies grants so caller
// mutation cannot widen an already bound invocation.
type GrantedCredentials struct {
	grants []CredentialGrant
	source SecretSource
	mode   string
}

func NewGrantedCredentials(mode string, grants []CredentialGrant, source SecretSource) (*GrantedCredentials, error) {
	if mode != Interactive && mode != Triggered {
		return nil, fmt.Errorf("unsupported credential mode")
	}
	seen := map[string]bool{}
	for _, g := range grants {
		if g.Mode != mode || g.ProcessDigest == "" || g.NodeKey == "" || g.PolicyDigest == "" || g.Operation == "" || g.Ref == "" || g.Source == "" || (mode == Interactive && g.RunID == "") || (mode == Triggered && g.RunID != "") {
			return nil, fmt.Errorf("incomplete credential grant")
		}
		key := g.ProcessDigest + "\x00" + g.NodeKey + "\x00" + g.PolicyDigest + "\x00" + g.Operation + "\x00" + g.Ref
		if seen[key] {
			return nil, fmt.Errorf("ambiguous credential grant")
		}
		seen[key] = true
	}
	return &GrantedCredentials{append([]CredentialGrant(nil), grants...), source, mode}, nil
}

// Authorize checks exact custody scope without fetching a credential. It is
// shared by setup preflight and execution; key presence never grants access.
func (p *GrantedCredentials) Authorize(id Identity, ref string) (string, error) {
	if p == nil || id.RunID == "" {
		return "", fmt.Errorf("credential unavailable")
	}
	for _, g := range p.grants {
		if g.ProcessDigest == id.ProcessDigest && g.NodeKey == id.NodeKey && g.PolicyDigest == id.PolicyDigest && g.Operation == id.Operation && g.Ref == ref && (p.mode != Interactive || g.RunID == id.RunID) {
			return g.Source, nil
		}
	}
	return "", fmt.Errorf("credential grant absent for node and operation")
}
func (p *GrantedCredentials) Resolve(ctx context.Context, id Identity, ref string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source, err := p.Authorize(id, ref)
	if err != nil {
		return nil, err
	}
	if p.source == nil {
		return nil, fmt.Errorf("credential unavailable")
	}
	value, err := p.source.ResolveSecret(ctx, source)
	if err != nil || len(value) == 0 {
		return nil, fmt.Errorf("credential unavailable")
	}
	return value, nil
}
