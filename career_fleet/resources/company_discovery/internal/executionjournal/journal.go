// Package executionjournal supplies source copied by Forge into a sovereign
// Process. Records are completed authenticated facts, never execution claims.
//
// Ordering, cancellation/correction, and start-vs-terminal convergence:
//
//   - Execution records (opening, node start/attempt/settled, complete) carry
//     no caller sequence. Their identity is content-addressed and immutable.
//   - Cancellation and correction are ordered control records. The caller
//     supplies a strictly positive sequence that must exceed every durable
//     control sequence at publication, and readers order control records by
//     immutable predecessor lineage. No wall clock is ever read or fabricated.
//   - complete.json and a cancellation control edge are terminal. After a terminal fact:
//     StartNode refuses; a late SettleNode durably retains its actual
//     observation as an attempt (when the activation actually started) and
//     refuses the settled checkpoint with the stable ErrTerminal; a late
//     RecordNodeAttempt retains its observation for a started activation and
//     refuses with ErrTerminal; Complete refuses. Cancellation is authoritative
//     over completion when both exist, and opening a cancelled journal
//     suppresses the cached completed outcome so replay cannot resurrect
//     terminated execution. A journal that carries both terminal facts is
//     reported with the stable ErrConflict rather than hiding one.
//   - There are no locks, claims, leases or reservations. Interleavings
//     converge on the durable record set: facts published before the terminal
//     fact are prior facts, and any record that observes termination is either
//     retained as an attempt or refused with a truthful stable error.
package executionjournal

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
)

const Schema = "praxis.execution_journal.v2"

const (
	CancellationSchema      = "praxis.execution_cancellation.v1"
	CorrectionSchema        = "praxis.execution_correction.v1"
	RefusedCompletionSchema = "praxis.execution_completion_refused.v1"
)

// Terminal values returned by Journal.Terminal.
const (
	TerminalCompleted = "completed"
	TerminalCancelled = "cancelled"
)

// Stable refusal identities. ErrCancelled wraps ErrTerminal so callers can
// discriminate the precise cause while still matching generic termination.
var (
	ErrTerminal  = errors.New("execution journal is terminal")
	ErrCancelled = fmt.Errorf("execution is cancelled: %w", ErrTerminal)
	ErrConflict  = errors.New("journal record identity conflicts")
	ErrSequence  = errors.New("control record sequence is not strictly increasing")
)

type Outcome struct {
	Response              json.RawMessage `json:"response"`
	ExecutionResponseJSON string          `json:"execution_response_json,omitempty"`
	Failed                bool            `json:"failed"`
}
type NodeOutcome struct {
	Outputs    json.RawMessage `json:"outputs,omitempty"`
	Error      string          `json:"error,omitempty"`
	Failure    json.RawMessage `json:"failure,omitempty"`
	Receipts   json.RawMessage `json:"receipts,omitempty"`
	Resolution json.RawMessage `json:"resolution,omitempty"`
}
type NodeAttempt struct {
	ID         string          `json:"id"`
	Outputs    json.RawMessage `json:"outputs,omitempty"`
	Resolution json.RawMessage `json:"resolution,omitempty"`
	Error      string          `json:"error,omitempty"`
	Failure    json.RawMessage `json:"failure,omitempty"`
	Receipts   json.RawMessage `json:"receipts,omitempty"`
}
type NodeCheckpoint struct {
	Key         string        `json:"key"`
	InputDigest string        `json:"input_digest"`
	State       string        `json:"state"`
	Outcome     *NodeOutcome  `json:"outcome,omitempty"`
	Attempts    []NodeAttempt `json:"attempts"`
}
type opening struct {
	Schema  string `json:"schema"`
	Scope   string `json:"scope"`
	Event   string `json:"event"`
	Payload string `json:"payload"`
}
type nodeBinding struct {
	Key         string `json:"key"`
	InputDigest string `json:"input_digest"`
}
type settled struct {
	nodeBinding
	Outcome NodeOutcome `json:"outcome"`
}
type attempted struct {
	nodeBinding
	Attempt NodeAttempt `json:"attempt"`
}

// Cancellation is the durable terminal cancellation fact. It binds the exact
// execution opening identity and carries the caller-supplied control sequence
// that orders it against corrections.
type Cancellation struct {
	Schema        string `json:"schema"`
	Scope         string `json:"scope"`
	Event         string `json:"event"`
	PayloadDigest string `json:"payload_digest"`
	Sequence      uint64 `json:"sequence"`
	Reason        string `json:"reason"`
}

// Correction is an immutable fact describing why one exact activation (key and
// input digest) is being replaced. It never rewrites a prior start, attempt or
// settled outcome; it only appends the correction and its ordering sequence.
type Correction struct {
	Schema        string `json:"schema"`
	Scope         string `json:"scope"`
	Event         string `json:"event"`
	PayloadDigest string `json:"payload_digest"`
	Sequence      uint64 `json:"sequence"`
	Activation    string `json:"activation"`
	InputDigest   string `json:"input_digest"`
	Reason        string `json:"reason"`
}

// refusedCompletion retains the exact observed-but-refused completion proposal
// so a losing complete-vs-complete race never silently discards a fact.
type refusedCompletion struct {
	Schema        string  `json:"schema"`
	Scope         string  `json:"scope"`
	Event         string  `json:"event"`
	PayloadDigest string  `json:"payload_digest"`
	Outcome       Outcome `json:"outcome"`
}

// access selects what a successfully opened journal may write:
// accessExecute runs work, accessControl only appends cancellation/correction,
// accessObserve is read-only. None of them grants authority to execute.
type access uint8

const (
	accessExecute access = iota
	accessControl
	accessObserve
)

// Journal holds descriptor identity and cryptographic material only. Opening
// it never grants authority to execute, reserves a Node or excludes other openers.
type Journal struct {
	dir       *os.Root
	path      string
	identity  os.FileInfo
	aead      cipher.AEAD
	opening   opening
	resumed   bool
	instance  string
	sequence  atomic.Uint64
	ancestors []directoryPin
	access    access
}
type directoryPin struct {
	path string
	info os.FileInfo
}

func hash(raw []byte) string { s := sha256.Sum256(raw); return hex.EncodeToString(s[:]) }
func exact(raw []byte, dst any) error {
	// Reject duplicate object members before decoding authenticated structures.
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var scan func() error
	scan = func() error {
		t, err := d.Token()
		if err != nil {
			return err
		}
		if delim, ok := t.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					key, err := d.Token()
					if err != nil {
						return err
					}
					s, ok := key.(string)
					if !ok || seen[s] {
						return fmt.Errorf("duplicate or invalid journal member")
					}
					seen[s] = true
					if err = scan(); err != nil {
						return err
					}
				}
			case '[':
				for d.More() {
					if err := scan(); err != nil {
						return err
					}
				}
			default:
				return fmt.Errorf("unexpected journal delimiter")
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := scan(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing journal data")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(dst)
}
func private(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("journal object must be private and nonsymlink: %s", path)
	}
	return nil
}
func Open(root string, key []byte, scope, event, payload string) (*Journal, *Outcome, error) {
	return open(root, key, scope, event, payload, accessExecute)
}

// OpenControl selects an existing execution journal for authenticated terminal
// control facts only: cancellation and correction. It never creates an opening,
// never writes node or completion facts, and never grants execution. The
// caller must present the same execution key, and every control record binds
// the durable opening identity it observed.
func OpenControl(root string, key []byte, scope, event string) (*Journal, error) {
	j, _, err := open(root, key, scope, event, "", accessControl)
	if err != nil {
		return nil, err
	}
	return j, nil
}

func open(root string, key []byte, scope, event, payload string, mode access) (*Journal, *Outcome, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || len(key) != 32 || strings.TrimSpace(scope) == "" || strings.TrimSpace(event) == "" || (mode == accessExecute && strings.TrimSpace(payload) == "") {
		return nil, nil, fmt.Errorf("exact execution journal binding required")
	}
	// Reject symlinked ancestors before selecting the descriptor-contained root.
	var ancestors []directoryPin
	for p := root; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return nil, nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, nil, fmt.Errorf("journal ancestor changed")
		}
		ancestors = append(ancestors, directoryPin{p, info})
		if p == filepath.Dir(p) {
			break
		}
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, nil, err
	}
	if err = private(root, info); err != nil {
		return nil, nil, err
	}
	base, err := os.OpenRoot(root)
	if err != nil {
		return nil, nil, err
	}
	defer base.Close()
	listing, err := base.Open(".")
	if err != nil {
		return nil, nil, err
	}
	entries, err := listing.ReadDir(-1)
	err = errors.Join(err, listing.Close())
	if err != nil {
		return nil, nil, err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "journal-") || !entry.IsDir() {
			return nil, nil, fmt.Errorf("selected execution journal root contains another contract; explicit historical recovery is required")
		}
	}
	id := hash([]byte(Schema + "\x00" + scope + "\x00" + event))
	name := "journal-" + id
	if mode == accessExecute {
		if err = base.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, nil, err
		}
	}
	path := filepath.Join(root, name)
	info, err = os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("journal directory changed")
	}
	if err = private(path, info); err != nil {
		return nil, nil, err
	}
	dir, err := base.OpenRoot(name)
	if err != nil {
		return nil, nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		dir.Close()
		return nil, nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		dir.Close()
		return nil, nil, err
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		dir.Close()
		return nil, nil, err
	}
	j := &Journal{dir: dir, path: path, identity: info, aead: aead, opening: opening{Schema, scope, event, payload}, instance: hex.EncodeToString(nonce), ancestors: ancestors, access: mode}
	prior, err := j.read("opening.json")
	if err == nil {
		var old opening
		if err = exact(prior, &old); err != nil || old.Schema != Schema || old.Scope != scope || old.Event != event || old.Payload == "" || (mode == accessExecute && old.Payload != payload) {
			j.Close()
			return nil, nil, fmt.Errorf("journal opening or payload identity conflicts")
		}
		j.opening = old
		j.resumed = true
	} else if errors.Is(err, os.ErrNotExist) {
		if mode != accessExecute {
			j.Close()
			return nil, nil, fmt.Errorf("this opening requires an existing execution journal: %w", err)
		}
		if err = j.publish("opening.json", j.opening); err != nil {
			j.Close()
			return nil, nil, err
		}
	} else {
		j.Close()
		return nil, nil, err
	}
	// The namespace entry must also be durable before an effect may begin.
	if mode == accessExecute {
		parent, err := base.Open(".")
		if err != nil {
			j.Close()
			return nil, nil, err
		}
		err = errors.Join(parent.Sync(), parent.Close())
		if err != nil {
			j.Close()
			return nil, nil, err
		}
	}
	// The terminal cancellation fact is read before the completion cache so a
	// tampered or conflicting terminal record is never hidden by an early
	// return, and so replay is suppressed for a cancelled execution.
	cancel, err := j.cancellationRecord()
	if err != nil {
		j.Close()
		return nil, nil, err
	}
	raw, err := j.read("complete.json")
	if errors.Is(err, os.ErrNotExist) {
		return j, nil, nil
	}
	if err != nil {
		j.Close()
		return nil, nil, err
	}
	var out Outcome
	if err = exact(raw, &out); err != nil || !json.Valid(out.Response) {
		j.Close()
		return nil, nil, fmt.Errorf("invalid completed journal outcome")
	}
	if cancel != nil {
		return j, nil, nil
	}
	return j, &out, nil
}
func (j *Journal) verify() error {
	for _, pin := range j.ancestors {
		info, err := os.Lstat(pin.path)
		if err != nil {
			return err
		}
		if !os.SameFile(info, pin.info) || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("journal ancestor identity changed")
		}
	}
	info, err := os.Lstat(j.path)
	if err != nil {
		return err
	}
	if !os.SameFile(info, j.identity) || !info.IsDir() {
		return fmt.Errorf("journal directory identity changed")
	}
	return private(j.path, info)
}
func (j *Journal) aad(name string) []byte {
	identity := j.opening
	if name == "opening.json" {
		identity.Payload = ""
	}
	raw, _ := json.Marshal(identity)
	return append(append(raw, 0), []byte(name)...)
}
func (j *Journal) read(name string) ([]byte, error) {
	if err := j.verify(); err != nil {
		return nil, err
	}
	info, err := j.dir.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("journal record is not regular")
	}
	if err = private(name, info); err != nil {
		return nil, err
	}
	f, err := j.dir.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("journal record changed during read")
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	after, err := j.dir.Lstat(name)
	if err != nil || !os.SameFile(after, opened) {
		return nil, fmt.Errorf("journal record replaced")
	}
	if len(raw) < j.aead.NonceSize() {
		return nil, fmt.Errorf("invalid journal envelope")
	}
	plain, err := j.aead.Open(nil, raw[:j.aead.NonceSize()], raw[j.aead.NonceSize():], j.aad(name))
	if err != nil {
		return nil, fmt.Errorf("journal authentication failed")
	}
	if err = j.verify(); err != nil {
		return nil, err
	}
	return plain, nil
}
func (j *Journal) publish(name string, value any) error {
	if err := j.requireExecute(); err != nil {
		return err
	}
	return j.publishAuthorized(name, value)
}

// publishControl appends an authenticated control record. It is available to
// execution openings and control openings, never to observation, and it never
// publishes node or execution facts.
func (j *Journal) publishControl(name string, value any) error {
	if j.access == accessObserve {
		return fmt.Errorf("observation cannot publish control facts")
	}
	return j.publishAuthorized(name, value)
}

func (j *Journal) requireExecute() error {
	if j.access != accessExecute {
		return fmt.Errorf("this journal opening does not grant execution; it may only observe or cancel")
	}
	return nil
}

func (j *Journal) publishAuthorized(name string, value any) error {
	if err := j.verify(); err != nil {
		return err
	}
	plain, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if old, err := j.read(name); err == nil {
		if !bytes.Equal(old, plain) {
			return ErrConflict
		}
		return j.ack(name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	nonce := make([]byte, j.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	raw := j.aead.Seal(nonce, nonce, plain, j.aad(name))
	temporary := ".stage-" + j.instance + "-" + fmt.Sprint(j.sequence.Add(1))
	f, err := j.dir.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer j.dir.Remove(temporary)
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = j.dir.Link(temporary, name); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	got, err := j.read(name)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, plain) {
		return ErrConflict
	}
	return j.ack(name)
}
func (j *Journal) ack(name string) error {
	if err := j.verify(); err != nil {
		return err
	}
	f, err := j.dir.Open(name)
	if err != nil {
		return err
	}
	err = errors.Join(f.Sync(), f.Close())
	if err != nil {
		return err
	}
	d, err := j.dir.Open(".")
	if err != nil {
		return err
	}
	err = errors.Join(d.Sync(), d.Close())
	if err != nil {
		return err
	}
	return j.verify()
}
func (j *Journal) binding(key string) string { return "node-" + hash([]byte(key)) }

// started reports whether the exact activation (key + input digest) has a
// durable start binding.
func (j *Journal) started(key, input string) bool {
	raw, err := j.read(j.binding(key) + "-start.json")
	if err != nil {
		return false
	}
	var b nodeBinding
	return exact(raw, &b) == nil && b.Key == key && b.InputDigest == input
}

func (j *Journal) StartNode(key, input string) error {
	if err := j.requireExecute(); err != nil {
		return err
	}
	if key == "" || input == "" {
		return fmt.Errorf("exact Node binding required")
	}
	terminal, err := j.Terminal()
	if err != nil {
		return err
	}
	if terminal != "" {
		return fmt.Errorf("%w: no Node may start after %s", ErrTerminal, terminal)
	}
	if err := j.publish(j.binding(key)+"-start.json", nodeBinding{key, input}); err != nil {
		return err
	}
	// A terminal fact that became durable during publication stays truthful:
	// the start is retained, and the caller is told it raced termination.
	if terminal, err := j.Terminal(); err != nil {
		return err
	} else if terminal != "" {
		return fmt.Errorf("%w: Node start raced %s; the durable start is retained", ErrTerminal, terminal)
	}
	return nil
}
func validOutcome(out NodeOutcome) error {
	for _, raw := range []json.RawMessage{out.Outputs, out.Failure, out.Receipts, out.Resolution} {
		if len(raw) != 0 && !json.Valid(raw) {
			return fmt.Errorf("invalid Node artifact JSON")
		}
	}
	if out.Error == "" && len(out.Outputs) == 0 {
		return fmt.Errorf("Node outcome requires outputs or failure")
	}
	return nil
}

// publishAttempt stores one immutable attempt observation. It performs no
// terminal checks: callers decide whether a late observation is retained.
func (j *Journal) publishAttempt(key, input string, attempt NodeAttempt) error {
	return j.publish(j.binding(key)+"-attempt-"+hash([]byte(attempt.ID))+".json", attempted{nodeBinding{key, input}, attempt})
}

func (j *Journal) settleAttempt(out NodeOutcome) NodeAttempt {
	bound, _ := json.Marshal(out)
	return NodeAttempt{ID: "completed-" + hash(bound), Outputs: out.Outputs, Resolution: out.Resolution, Error: out.Error, Failure: out.Failure, Receipts: out.Receipts}
}

// SettleNode records a completed activation. When a terminal fact is already
// durable, the observed outcome is retained as an attempt for a started
// activation and the settled checkpoint is refused with ErrTerminal; work is
// never restarted or resurrected by a late settle.
func (j *Journal) SettleNode(key, input string, out NodeOutcome) error {
	if err := j.requireExecute(); err != nil {
		return err
	}
	if err := validOutcome(out); err != nil {
		return err
	}
	terminal, err := j.Terminal()
	if err != nil {
		return err
	}
	if terminal != "" {
		if j.started(key, input) {
			if err := j.publishAttempt(key, input, j.settleAttempt(out)); err != nil {
				return err
			}
		}
		return fmt.Errorf("%w: late settle retained as an attempt; no settled checkpoint after %s", ErrTerminal, terminal)
	}
	if err := j.StartNode(key, input); err != nil {
		// A terminal fact observed here means the existing activation stays on
		// the record; retain the observation when it can be bound.
		if terminal, terr := j.Terminal(); terr == nil && terminal != "" && j.started(key, input) {
			if rerr := j.publishAttempt(key, input, j.settleAttempt(out)); rerr != nil {
				return rerr
			}
		}
		return err
	}
	// Retain every distinct observed outcome before selecting the checkpoint.
	// A conflicting completion can refuse without discarding its actual receipt.
	if err := j.publishAttempt(key, input, j.settleAttempt(out)); err != nil {
		return err
	}
	if err := j.publish(j.binding(key)+"-settled.json", settled{nodeBinding{key, input}, out}); err != nil {
		if terminal, terr := j.Terminal(); terr == nil && terminal != "" {
			return fmt.Errorf("%w: settle raced %s; the durable attempt is retained", ErrTerminal, terminal)
		}
		return err
	}
	if terminal, err := j.Terminal(); err != nil {
		return err
	} else if terminal != "" {
		return fmt.Errorf("%w: settle raced %s; the durable attempt is retained", ErrTerminal, terminal)
	}
	return nil
}

// RecordNodeAttempt stores an interrupted or reconciliation attempt. A late
// attempt against a terminal journal is retained for an activation that
// actually started and is refused with ErrTerminal; no new activation starts.
func (j *Journal) RecordNodeAttempt(key, input string, attempt NodeAttempt) error {
	if err := j.requireExecute(); err != nil {
		return err
	}
	if key == "" || input == "" {
		return fmt.Errorf("exact Node binding required")
	}
	if attempt.ID == "" {
		attempt.ID = j.instance + "-" + fmt.Sprint(j.sequence.Add(1))
	}
	for _, raw := range []json.RawMessage{attempt.Outputs, attempt.Resolution, attempt.Failure, attempt.Receipts} {
		if len(raw) != 0 && !json.Valid(raw) {
			return fmt.Errorf("invalid attempt artifact JSON")
		}
	}
	terminal, err := j.Terminal()
	if err != nil {
		return err
	}
	if terminal != "" {
		if j.started(key, input) {
			if err := j.publishAttempt(key, input, attempt); err != nil {
				return err
			}
		}
		return fmt.Errorf("%w: attempt observed after %s; no new activation may start", ErrTerminal, terminal)
	}
	if err := j.StartNode(key, input); err != nil {
		return err
	}
	return j.publishAttempt(key, input, attempt)
}
func (j *Journal) Node(key string) (*NodeCheckpoint, error) { return j.DurableNode(key) }
func (j *Journal) DurableNode(key string) (*NodeCheckpoint, error) {
	name := j.binding(key)
	raw, err := j.read(name + "-start.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var b nodeBinding
	if err = exact(raw, &b); err != nil || b.Key != key || b.InputDigest == "" {
		return nil, fmt.Errorf("Node checkpoint identity changed")
	}
	result := &NodeCheckpoint{Key: b.Key, InputDigest: b.InputDigest, State: "started", Attempts: []NodeAttempt{}}
	raw, err = j.read(name + "-settled.json")
	if err == nil {
		var value settled
		if err = exact(raw, &value); err != nil || value.nodeBinding != b {
			return nil, fmt.Errorf("settled checkpoint binding changed")
		}
		if err = validOutcome(value.Outcome); err != nil {
			return nil, err
		}
		result.State = "settled"
		result.Outcome = &value.Outcome
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	d, err := j.dir.Open(".")
	if err != nil {
		return nil, err
	}
	entries, err := d.ReadDir(-1)
	err = errors.Join(err, d.Close())
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), name+"-attempt-") {
			continue
		}
		raw, err = j.read(entry.Name())
		if err != nil {
			return nil, err
		}
		var a attempted
		if err = exact(raw, &a); err != nil || a.nodeBinding != b || a.Attempt.ID == "" || entry.Name() != name+"-attempt-"+hash([]byte(a.Attempt.ID))+".json" {
			return nil, fmt.Errorf("attempt identity changed")
		}
		result.Attempts = append(result.Attempts, a.Attempt)
	}
	sort.Slice(result.Attempts, func(a, b int) bool { return result.Attempts[a].ID < result.Attempts[b].ID })
	return result, j.verify()
}

// entries lists the durable record names of the selected execution journal.
func (j *Journal) entries() ([]os.DirEntry, error) {
	d, err := j.dir.Open(".")
	if err != nil {
		return nil, err
	}
	entries, err := d.ReadDir(-1)
	return entries, errors.Join(err, d.Close())
}

// A control chain publishes one completed fact per predecessor. Competing
// writers contend on the same immutable name, so sequence acceptance is a
// cross-process conditional transition, never a read-then-append claim.
const ControlSchema = "praxis.execution_control.v1"

type controlRecord struct {
	Schema       string        `json:"schema"`
	Previous     string        `json:"previous"`
	Sequence     uint64        `json:"sequence"`
	Cancellation *Cancellation `json:"cancellation,omitempty"`
	Correction   *Correction   `json:"correction,omitempty"`
	name         string
	digest       string
}

func controlGenesis() string             { return hash([]byte(ControlSchema)) }
func controlName(previous string) string { return "control-" + previous + ".json" }
func (j *Journal) controlRecords() ([]controlRecord, error) {
	out := []controlRecord{}
	previous := controlGenesis()
	sequence := uint64(0)
	cancelled := false
	for {
		name := controlName(previous)
		raw, err := j.read(name)
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		var record controlRecord
		if err = exact(raw, &record); err != nil {
			return nil, fmt.Errorf("invalid control record: %w", err)
		}
		if cancelled || record.Schema != ControlSchema || record.Previous != previous || record.Sequence <= sequence || (record.Cancellation == nil) == (record.Correction == nil) {
			return nil, fmt.Errorf("control lineage or sequence changed")
		}
		if c := record.Cancellation; c != nil {
			if c.Schema != CancellationSchema || c.Scope != j.opening.Scope || c.Event != j.opening.Event || c.PayloadDigest != j.opening.Payload || c.Sequence != record.Sequence || strings.TrimSpace(c.Reason) == "" {
				return nil, fmt.Errorf("cancellation identity changed")
			}
			cancelled = true
		}
		if c := record.Correction; c != nil {
			if c.Schema != CorrectionSchema || c.Scope != j.opening.Scope || c.Event != j.opening.Event || c.PayloadDigest != j.opening.Payload || c.Sequence != record.Sequence || c.Activation == "" || c.InputDigest == "" || strings.TrimSpace(c.Reason) == "" {
				return nil, fmt.Errorf("correction identity changed")
			}
		}
		record.name = name
		record.digest = hash(raw)
		out = append(out, record)
		previous = record.digest
		sequence = record.Sequence
	}
}
func (j *Journal) cancellationRecord() (*Cancellation, error) {
	records, err := j.controlRecords()
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.Cancellation != nil {
			return record.Cancellation, nil
		}
	}
	return nil, nil
}
func (j *Journal) Cancellation() (*Cancellation, error) { return j.cancellationRecord() }
func (j *Journal) Cancelled() (bool, string, error) {
	c, err := j.cancellationRecord()
	if err != nil {
		return false, "", err
	}
	if c == nil {
		return false, "", nil
	}
	if _, err = j.read("complete.json"); err == nil {
		return false, "", fmt.Errorf("%w: completion and cancellation are both durable; cancellation is terminal", ErrConflict)
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, "", err
	}
	return true, c.Reason, nil
}
func (j *Journal) Terminal() (string, error) {
	cancelled, _, err := j.Cancelled()
	if err != nil {
		return "", err
	}
	if cancelled {
		return TerminalCancelled, nil
	}
	if _, err = j.read("complete.json"); err == nil {
		return TerminalCompleted, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return "", nil
}
func (j *Journal) Corrections() ([]Correction, error) {
	records, err := j.controlRecords()
	if err != nil {
		return nil, err
	}
	out := []Correction{}
	for _, record := range records {
		if record.Correction != nil {
			out = append(out, *record.Correction)
		}
	}
	return out, nil
}

// ControlSequence is an authenticated observation. The caller supplies the
// sequence of its next request; this read grants no publication authority.
func (j *Journal) ControlSequence() (uint64, error) {
	records, err := j.controlRecords()
	if err != nil {
		return 0, err
	}
	if len(records) == 0 {
		return 0, nil
	}
	return records[len(records)-1].Sequence, nil
}
func (j *Journal) appendControl(sequence uint64, cancel *Cancellation, correction *Correction) error {
	if j.access == accessObserve {
		return fmt.Errorf("observation cannot publish control facts")
	}
	for {
		records, err := j.controlRecords()
		if err != nil {
			return err
		}
		previous := controlGenesis()
		max := uint64(0)
		for _, record := range records {
			if cancel != nil && record.Cancellation != nil {
				if *cancel == *record.Cancellation {
					return j.ack(record.name)
				}
				return fmt.Errorf("%w: a different cancellation is already durable", ErrConflict)
			}
			if correction != nil && record.Correction != nil && *correction == *record.Correction {
				return j.ack(record.name)
			}
			previous = record.digest
			max = record.Sequence
		}
		if len(records) > 0 && records[len(records)-1].Cancellation != nil {
			return fmt.Errorf("%w: cancelled execution cannot be corrected", ErrCancelled)
		}
		if _, err = j.read("complete.json"); err == nil {
			return fmt.Errorf("%w: completed execution cannot accept new control facts", ErrTerminal)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if sequence <= max {
			return fmt.Errorf("%w: control sequence %d does not exceed %d", ErrSequence, sequence, max)
		}
		record := controlRecord{Schema: ControlSchema, Previous: previous, Sequence: sequence, Cancellation: cancel, Correction: correction}
		if err = j.publishControl(controlName(previous), record); errors.Is(err, ErrConflict) {
			continue
		} else if err != nil {
			return err
		}
		if _, err = j.read("complete.json"); err == nil {
			return fmt.Errorf("%w: control publication raced completion; the control fact is retained", ErrConflict)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
}
func (j *Journal) Cancel(sequence uint64, reason string) error {
	if sequence == 0 || strings.TrimSpace(reason) == "" {
		return fmt.Errorf("cancellation requires a strictly positive sequence and a reason")
	}
	c := Cancellation{CancellationSchema, j.opening.Scope, j.opening.Event, j.opening.Payload, sequence, reason}
	return j.appendControl(sequence, &c, nil)
}
func (j *Journal) Correct(sequence uint64, activation, inputDigest, reason string) error {
	if sequence == 0 || strings.TrimSpace(activation) == "" || strings.TrimSpace(inputDigest) == "" || strings.TrimSpace(reason) == "" {
		return fmt.Errorf("correction requires a sequence, exact activation binding and reason")
	}
	raw, err := j.read(j.binding(activation) + "-start.json")
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("correction requires a started activation: %s", activation)
	}
	if err != nil {
		return err
	}
	var binding nodeBinding
	if err = exact(raw, &binding); err != nil || binding.Key != activation || binding.InputDigest != inputDigest {
		return fmt.Errorf("correction activation binding changed")
	}
	c := Correction{CorrectionSchema, j.opening.Scope, j.opening.Event, j.opening.Payload, sequence, activation, inputDigest, reason}
	return j.appendControl(sequence, nil, &c)
}

// retainRefusedCompletion keeps the losing completion proposal, bound to the
// execution opening, so both facts of a complete-vs-complete race survive.
func (j *Journal) retainRefusedCompletion(out Outcome, plain []byte) error {
	record := refusedCompletion{RefusedCompletionSchema, j.opening.Scope, j.opening.Event, j.opening.Payload, out}
	name := "complete-refused-" + hash(plain) + ".json"
	if raw, err := j.read(name); err == nil {
		if bytes.Equal(raw, plain) {
			return j.ack(name)
		}
		return fmt.Errorf("%w: refused completion identity conflicts", ErrConflict)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return j.publish(name, record)
}

func (j *Journal) Complete(out Outcome) error {
	if err := j.requireExecute(); err != nil {
		return err
	}
	if !json.Valid(out.Response) {
		return fmt.Errorf("complete public response required")
	}
	if cancel, err := j.cancellationRecord(); err != nil {
		return err
	} else if cancel != nil {
		return fmt.Errorf("%w: cancelled execution cannot complete", ErrCancelled)
	}
	proposed, err := json.Marshal(out)
	if err != nil {
		return err
	}
	if raw, err := j.read("complete.json"); err == nil {
		if bytes.Equal(raw, proposed) {
			return j.ack("complete.json")
		}
		if err := j.retainRefusedCompletion(out, proposed); err != nil {
			return err
		}
		return fmt.Errorf("%w: a different completed outcome is already durable; the refused proposal is retained", ErrConflict)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	entries, err := j.entries()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "node-") || !strings.HasSuffix(entry.Name(), "-start.json") {
			continue
		}
		raw, err := j.read(entry.Name())
		if err != nil {
			return err
		}
		var b nodeBinding
		if err = exact(raw, &b); err != nil {
			return err
		}
		cp, err := j.DurableNode(b.Key)
		if err != nil {
			return err
		}
		if cp == nil || cp.State != "settled" {
			return fmt.Errorf("unfinished Node prevents completion: %s", b.Key)
		}
	}
	if err := j.publish("complete.json", out); err != nil {
		if raw, rerr := j.read("complete.json"); rerr == nil && !bytes.Equal(raw, proposed) {
			if rerr := j.retainRefusedCompletion(out, proposed); rerr != nil {
				return rerr
			}
			return fmt.Errorf("%w: completion raced a different completed outcome; the refused proposal is retained", ErrConflict)
		}
		return err
	}
	if cancel, err := j.cancellationRecord(); err != nil {
		return err
	} else if cancel != nil {
		return fmt.Errorf("%w: cancellation was recorded concurrently; cancellation is terminal", ErrConflict)
	}
	return nil
}
func (j *Journal) Close() error          { return j.dir.Close() }
func (j *Journal) Resumed() bool         { return j.resumed }
func (j *Journal) PayloadDigest() string { return j.opening.Payload }

// Observation returns authenticated facts for an explicitly selected execution.
// The observed payload and inputs must be checked by their consumer; observing
// them neither resumes execution nor authorizes replay. No records are created.
type Observation struct {
	Schema        string          `json:"schema"`
	Scope         string          `json:"scope"`
	Event         string          `json:"event"`
	PayloadDigest string          `json:"payload_digest"`
	Node          *NodeCheckpoint `json:"node"`
}

func ObserveNode(root string, key []byte, scope, event, node string) (Observation, error) {
	j, _, err := open(root, key, scope, event, "", accessObserve)
	if err != nil {
		return Observation{}, err
	}
	defer j.Close()
	cp, err := j.DurableNode(node)
	if err != nil {
		return Observation{}, err
	}
	return Observation{Schema, scope, event, j.PayloadDigest(), cp}, nil
}

type ExecutionObservation struct {
	Schema        string           `json:"schema"`
	Scope         string           `json:"scope"`
	Event         string           `json:"event"`
	PayloadDigest string           `json:"payload_digest"`
	Nodes         []NodeCheckpoint `json:"nodes"`
	Cancellation  *Cancellation    `json:"cancellation,omitempty"`
}

// ObserveExecution returns the authenticated facts actually encountered. It
// is not an atomic whole-case snapshot and cannot establish work acceptance.
func ObserveExecution(root string, key []byte, scope, event string) (ExecutionObservation, error) {
	j, _, err := open(root, key, scope, event, "", accessObserve)
	if err != nil {
		return ExecutionObservation{}, err
	}
	defer j.Close()
	cancel, err := j.cancellationRecord()
	if err != nil {
		return ExecutionObservation{}, err
	}
	out := ExecutionObservation{Schema: Schema, Scope: scope, Event: event, PayloadDigest: j.PayloadDigest(), Nodes: []NodeCheckpoint{}, Cancellation: cancel}
	entries, err := j.entries()
	if err != nil {
		return out, err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "node-") || !strings.HasSuffix(entry.Name(), "-start.json") {
			continue
		}
		raw, err := j.read(entry.Name())
		if err != nil {
			return out, err
		}
		var b nodeBinding
		if err = exact(raw, &b); err != nil {
			return out, err
		}
		if entry.Name() != j.binding(b.Key)+"-start.json" {
			return out, fmt.Errorf("observed Node name changed")
		}
		cp, err := j.DurableNode(b.Key)
		if err != nil {
			return out, err
		}
		out.Nodes = append(out.Nodes, *cp)
	}
	sort.Slice(out.Nodes, func(a, b int) bool { return out.Nodes[a].Key < out.Nodes[b].Key })
	return out, j.verify()
}
