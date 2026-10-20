package egresscontract

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
)

// A Process has one inherited pipe lifetime, even when its frontier activates
// the same Node repeatedly. Responses are multiplexed by sequence so independent
// Nodes do not serialize provider calls behind one another.
type ProviderHTTPSPipeClient struct {
	read, write *os.File
	mu, writeMu sync.Mutex
	next        uint64
	pending     map[uint64]chan providerHTTPSResponseFrame
	err         error
	done        chan struct{}
}

var inheritedProviderHTTPS struct {
	once   sync.Once
	client *ProviderHTTPSPipeClient
	err    error
}

func OpenProviderHTTPSPipeClient() (*ProviderHTTPSPipeClient, error) {
	inheritedProviderHTTPS.once.Do(func() {
		// exec inherits ordinary blocking pipe descriptors. Register them with
		// Go's poller so cancellation/Close interrupts a blocked read or write.
		if e := prepareProviderHTTPSInheritedPipes(); e != nil {
			inheritedProviderHTTPS.err = ErrProviderHTTPS
			return
		}
		inheritedProviderHTTPS.client, inheritedProviderHTTPS.err = NewProviderHTTPSPipeClient(os.NewFile(3, "provider-https-response"), os.NewFile(4, "provider-https-request"))
	})
	return inheritedProviderHTTPS.client, inheritedProviderHTTPS.err
}
func NewProviderHTTPSPipeClient(read, write *os.File) (*ProviderHTTPSPipeClient, error) {
	if read == nil || write == nil || read == write {
		return nil, ErrProviderHTTPS
	}
	for _, f := range []*os.File{read, write} {
		i, e := f.Stat()
		if e != nil || i.Mode()&os.ModeNamedPipe == 0 {
			return nil, ErrProviderHTTPS
		}
	}
	c := &ProviderHTTPSPipeClient{read: read, write: write, pending: map[uint64]chan providerHTTPSResponseFrame{}, done: make(chan struct{})}
	go c.receive()
	return c, nil
}
func (c *ProviderHTTPSPipeClient) receive() {
	defer close(c.done)
	for {
		raw, e := readProviderHTTPSFrame(c.read)
		if e != nil {
			c.fail()
			return
		}
		var f providerHTTPSResponseFrame
		if strictProviderHTTPSJSON(raw, &f) != nil || f.Schema != ProviderHTTPSResponseSchema || f.Sequence == 0 {
			c.fail()
			return
		}
		c.mu.Lock()
		ch := c.pending[f.Sequence]
		delete(c.pending, f.Sequence)
		c.mu.Unlock()
		if ch != nil {
			ch <- f
		}
	}
}
func (c *ProviderHTTPSPipeClient) fail() {
	c.mu.Lock()
	if c.err == nil {
		c.err = ErrProviderHTTPS
	}
	for seq, ch := range c.pending {
		delete(c.pending, seq)
		close(ch)
	}
	c.mu.Unlock()
}
func (c *ProviderHTTPSPipeClient) Close() error {
	if c == nil {
		return nil
	}
	c.fail()
	c.write.Close()
	c.read.Close()
	<-c.done
	return nil
}
func (c *ProviderHTTPSPipeClient) exchange(ctx context.Context, r ProviderHTTPSRequest) (providerHTTPSResponseFrame, error) {
	if ctx == nil || ctx.Err() != nil {
		return providerHTTPSResponseFrame{}, ErrProviderHTTPS
	}
	// A blocked anonymous-pipe write must respect cancellation too. Closing the
	// transport leaves every affected exchange uncertain; it never retries.
	stop := context.AfterFunc(ctx, func() { c.fail(); c.read.Close(); c.write.Close() })
	defer stop()
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return providerHTTPSResponseFrame{}, ErrProviderHTTPS
	}
	c.next++
	r.Sequence = c.next
	ch := make(chan providerHTTPSResponseFrame, 1)
	c.pending[r.Sequence] = ch
	c.mu.Unlock()
	c.writeMu.Lock()
	e := writeProviderHTTPSFrame(c.write, providerHTTPSRequestFrame{Schema: ProviderHTTPSRequestSchema, Request: r})
	c.writeMu.Unlock()
	if e != nil {
		c.fail()
		return providerHTTPSResponseFrame{}, ErrProviderHTTPS
	}
	select {
	case f, ok := <-ch:
		if !ok {
			return f, ErrProviderHTTPS
		}
		return f, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, r.Sequence)
		c.mu.Unlock()
		return providerHTTPSResponseFrame{}, ErrProviderHTTPS
	}
}

type ProviderHTTPSPipe struct {
	Client *ProviderHTTPSPipeClient
	Claim  ProviderHTTPSClaim
}

func (ProviderHTTPSPipe) Name() string { return "inherited.provider_https_pipe.v1" }
func (ProviderHTTPSPipe) Supports(p Policy) error {
	if p.Mode != ModeHTTPS || p.Validate() != nil {
		return ErrProviderHTTPS
	}
	for _, op := range p.Operations {
		if op.Method != "POST" || op.Destination.Kind != DestinationService || op.MaxRedirects != 0 || op.RedirectPolicy != RedirectNone || len(op.QueryParameters) != 0 {
			return ErrProviderHTTPS
		}
	}
	return nil
}
func (a ProviderHTTPSPipe) Start(ctx context.Context, inv Invocation) (InvocationHandle, error) {
	// The claim binds the typed Process reference. Native egress identity's
	// ProcessDigest binds the sealed composition, a distinct identity. The
	// caller verifies package/Process identity against its installed allow table.
	if a.Client == nil || a.Supports(inv.Policy) != nil || a.Claim.PackageDigest == "" || a.Claim.ProcessDigest == "" || a.Claim.EventID == "" || a.Claim.NodeKey != inv.Identity.NodeKey || a.Claim.NodeName != inv.Identity.NodeName || a.Claim.NodeDigest != inv.Identity.NodeDigest || a.Claim.NodeSpecDigest != inv.Identity.NodeSpecDigest || a.Claim.Operation != inv.Binding.Operation || a.Claim.PolicyDigest == "" {
		return nil, ErrProviderHTTPS
	}
	var selected Operation
	for _, op := range Normalize(inv.Policy).Operations {
		if op.ID == inv.Binding.Operation {
			selected = op
		}
	}
	return startNativeHTTPS(ctx, inv, a.Name(), &providerHTTPSRoundTripper{client: a.Client, claim: a.Claim, runID: inv.Identity.RunID, op: selected})
}

type providerHTTPSRoundTripper struct {
	client *ProviderHTTPSPipeClient
	claim  ProviderHTTPSClaim
	runID  string
	op     Operation
	last   *ProviderHTTPSReceipt
}

func (t *providerHTTPSRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.Method != t.op.Method {
		return nil, ErrProviderHTTPS
	}
	body, e := io.ReadAll(request.Body)
	if e != nil {
		return nil, ErrProviderHTTPS
	}
	r := ProviderHTTPSRequest{Claim: t.claim, RunID: t.runID, Method: request.Method, URL: request.URL.String(), Body: body}
	f, e := t.client.exchange(request.Context(), r)
	if e != nil {
		return nil, e
	}
	if f.Claim != t.claim || f.Receipt == nil {
		return nil, ErrProviderHTTPS
	}
	receipt := *f.Receipt
	if receipt.Schema != ProviderHTTPSReceiptSchema || receipt.Claim != t.claim || receipt.RunID != t.runID || receipt.Sequence != f.Sequence || receipt.RequestDigest != providerHTTPSDigest(body) || receipt.RequestBytes != len(body) || receipt.CapturedAt == "" || receipt.ReceiptDigest != providerHTTPSReceiptDigest(receipt) {
		return nil, ErrProviderHTTPS
	}
	if f.ErrorCode != "" {
		if receipt.Transmission != "attempted_unknown" || receipt.StatusCode != 0 || receipt.ResponseDigest != "" || receipt.ResponseBytes != 0 || len(f.Body) != 0 || f.StatusCode != 0 {
			return nil, ErrProviderHTTPS
		}
		t.last = &receipt
		return nil, ErrProviderHTTPS
	}
	if f.StatusCode < 100 || f.StatusCode > 599 || receipt.Transmission != "response_received" || receipt.StatusCode != f.StatusCode || receipt.ResponseBytes != len(f.Body) || receipt.ResponseDigest != providerHTTPSDigest(f.Body) || (t.op.MaxResponseBytes > 0 && len(f.Body) > t.op.MaxResponseBytes) {
		return nil, ErrProviderHTTPS
	}
	t.last = &receipt
	return &http.Response{Status: fmt.Sprintf("%d %s", f.StatusCode, http.StatusText(f.StatusCode)), StatusCode: f.StatusCode, Header: f.Headers, Body: io.NopCloser(bytes.NewReader(f.Body)), ContentLength: int64(len(f.Body)), Request: request}, nil
}
func (t *providerHTTPSRoundTripper) TakeProviderHTTPSReceipt() *ProviderHTTPSReceipt {
	r := t.last
	t.last = nil
	return r
}
