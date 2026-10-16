//go:build darwin || linux

package egresscontract

// Conditional records publish completed bytes, never an empty reservation.
// Each exact revision has one immutable successor. A hard link publishes the
// fully staged successor without replacement; other writers return contention.
// No work identity is leased, no lock is acquired, and staging grants no priority.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type conditionalRecordEdge struct {
	Schema   string               `json:"schema"`
	Record   string               `json:"record"`
	Previous ConditionalRecordPin `json:"previous"`
	Current  ConditionalRecordPin `json:"current"`
	UpdateID string               `json:"update_id"`
	Content  []byte               `json:"content"`
}
type ConditionalRecordStore struct {
	// Reuse the existing descriptor-containment and inode verification only.
	// Neither acquire nor any mutable AtomicFileStore operation is invoked.
	directory *AtomicFileStore
	record    string
}

// OpenConditionalRecordStore requires an existing explicit deployment root.
// It creates no root, head, marker, claim, reservation or lock file.
func OpenConditionalRecordStore(rootPath, record string) (*ConditionalRecordStore, error) {
	if !filepath.IsAbs(rootPath) || filepath.Clean(rootPath) != rootPath || !atomicBasename(record) {
		return nil, fmt.Errorf("clean absolute root and record basename required")
	}
	d := &AtomicFileStore{path: rootPath}
	current := string(os.PathSeparator)
	paths := []string{current}
	for _, part := range strings.Split(strings.TrimPrefix(rootPath, current), current) {
		if part != "" {
			current = filepath.Join(current, part)
			paths = append(paths, current)
		}
	}
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, fmt.Errorf("deployment root component changed: %s", path)
		}
		d.directories = append(d.directories, atomicDirectoryPin{path: path, info: info})
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	d.root = root
	if err = d.verifyRoot(); err != nil {
		root.Close()
		return nil, err
	}
	return &ConditionalRecordStore{directory: d, record: record}, nil
}
func (s *ConditionalRecordStore) Close() error { return s.directory.Close() }
func conditionalDigest(value any) string {
	raw, _ := json.Marshal(value)
	return atomicSHA(raw)
}
func (s *ConditionalRecordStore) genesis() ConditionalRecordPin {
	return ConditionalRecordPin{Revision: conditionalDigest([]string{ConditionalRecordSchema, s.record, "genesis"})}
}
func conditionalPinValid(pin ConditionalRecordPin) bool {
	return atomicPinValid(AtomicFilePin{Exists: true, SHA256: pin.Revision}) && atomicPinValid(AtomicFilePin{Exists: pin.Exists, SHA256: pin.SHA256})
}
func (s *ConditionalRecordStore) edgeName(previous ConditionalRecordPin) string {
	return ".conditional-" + atomicSHA([]byte(s.record)) + "-" + previous.Revision + ".json"
}
func (s *ConditionalRecordStore) prepare(previous ConditionalRecordPin, id string, content []byte) conditionalRecordEdge {
	if content == nil {
		content = []byte{}
	}
	content = bytes.Clone(content)
	edge := conditionalRecordEdge{Schema: ConditionalRecordSchema, Record: s.record, Previous: previous, UpdateID: id, Content: content}
	edge.Current = ConditionalRecordPin{Exists: true, SHA256: atomicSHA(content)}
	edge.Current.Revision = conditionalDigest([]any{ConditionalRecordSchema, s.record, previous, id, edge.Current.SHA256})
	return edge
}
func (s *ConditionalRecordStore) check(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("supplied context required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.directory.verifyRoot()
}
func (s *ConditionalRecordStore) edge(previous ConditionalRecordPin) (conditionalRecordEdge, *os.File, error) {
	var edge conditionalRecordEdge
	name := s.edgeName(previous)
	before, err := s.directory.regularInfo(name)
	if err != nil {
		return edge, nil, err
	}
	file, err := s.directory.root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return edge, nil, err
	}
	fail := func(err error) (conditionalRecordEdge, *os.File, error) {
		file.Close()
		return edge, nil, err
	}
	if _, err = s.directory.verifyNamedFile(name, file, before); err != nil {
		return fail(err)
	}
	raw, err := io.ReadAll(file)
	if err != nil {
		return fail(err)
	}
	if err = atomicDecode(raw, &edge); err != nil {
		return fail(err)
	}
	if edge.Schema != ConditionalRecordSchema || edge.Record != s.record || edge.Previous != previous || edge.UpdateID == "" || edge.Content == nil || edge.Current != s.prepare(previous, edge.UpdateID, edge.Content).Current {
		return fail(fmt.Errorf("conditional edge bindings changed"))
	}
	if _, err = s.directory.verifyNamedFile(name, file, before); err != nil {
		return fail(err)
	}
	return edge, file, nil
}
func (s *ConditionalRecordStore) Read(ctx context.Context) (ConditionalRecordSnapshot, error) {
	if err := s.check(ctx); err != nil {
		return ConditionalRecordSnapshot{}, err
	}
	current := ConditionalRecordSnapshot{Pin: s.genesis(), Content: []byte{}}
	for {
		if err := s.check(ctx); err != nil {
			return current, err
		}
		edge, file, err := s.edge(current.Pin)
		if errors.Is(err, os.ErrNotExist) {
			return current, s.check(ctx)
		}
		if err != nil {
			return current, err
		}
		if err = file.Close(); err != nil {
			return current, err
		}
		current = ConditionalRecordSnapshot{Pin: edge.Current, Content: edge.Content}
	}
}

// CompareAndPublish performs one conditional publication of complete bytes.
// Publication and durability acknowledgement are distinct outcomes: callers
// must not infer acknowledged use from visibility or matching postimage bytes.
// A failed precondition publishes nothing; the Process can rebase its same update.
func (s *ConditionalRecordStore) CompareAndPublish(ctx context.Context, previous ConditionalRecordPin, id string, content []byte) (ConditionalRecordPublication, error) {
	result := ConditionalRecordPublication{Previous: previous}
	if !conditionalPinValid(previous) || id == "" {
		return result, fmt.Errorf("exact predecessor and update identity required")
	}
	edge := s.prepare(previous, id, content)
	result.Prepared = edge.Current
	observed, err := s.Read(ctx)
	if err != nil {
		return result, err
	}
	result.Observed = observed
	if observed.Pin != previous {
		prior, file, e := s.edge(previous)
		if e == nil {
			file.Close()
			if prior.Current == edge.Current && prior.UpdateID == id && bytes.Equal(prior.Content, edge.Content) {
				result.Status = "duplicate"
				return result, nil
			}
			if prior.UpdateID == id {
				return result, fmt.Errorf("conditional update identity conflict")
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			return result, e
		}
		result.Status = "contended"
		return result, nil
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return result, err
	}
	name := ".conditional-stage-" + hex.EncodeToString(nonce[:])
	file, err := s.directory.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return result, err
	}
	defer s.directory.root.Remove(name)
	raw, err := json.Marshal(edge)
	if err == nil {
		_, err = file.Write(raw)
	}
	if err == nil {
		err = file.Chmod(0400)
	}
	if err == nil {
		err = file.Sync()
	}
	if err = errors.Join(err, file.Close()); err != nil {
		return result, err
	}
	if err = s.check(ctx); err != nil {
		return result, err
	}
	if err = s.directory.root.Link(name, s.edgeName(previous)); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return result, err
		}
		// No effect retry: observe the successor published by the other writer.
		prior, file, e := s.edge(previous)
		if e != nil {
			return result, e
		}
		file.Close()
		result.Observed = ConditionalRecordSnapshot{Pin: prior.Current, Content: prior.Content}
		result.Status = "contended"
		if prior.Current == edge.Current && prior.UpdateID == id && bytes.Equal(prior.Content, edge.Content) {
			result.Status = "duplicate"
		} else if prior.UpdateID == id {
			return result, fmt.Errorf("conditional update identity conflict")
		}
		return result, nil
	}
	result.Status = "published"
	readback, publishedFile, readErr := s.edge(previous)
	if readErr != nil {
		result.Status = "uncertain"
		return result, readErr
	}
	if err = publishedFile.Close(); err != nil {
		result.Status = "uncertain"
		return result, err
	}
	if readback.Current != edge.Current || readback.UpdateID != id || !bytes.Equal(readback.Content, edge.Content) {
		result.Status = "uncertain"
		return result, fmt.Errorf("conditional publication readback changed")
	}
	result.Observed = ConditionalRecordSnapshot{Pin: readback.Current, Content: readback.Content}
	// Retain the truthful published result even if root readback now fails.
	return result, s.directory.verifyRoot()
}

// Acknowledge syncs and reads back the immutable lineage to the exact selected
// revision, then syncs its opened directory. It neither republishes nor creates
// a record. A later successor does not erase an earlier durability receipt.
func (s *ConditionalRecordStore) Acknowledge(ctx context.Context, expected ConditionalRecordPin) (ConditionalRecordAcknowledgement, error) {
	result := ConditionalRecordAcknowledgement{Schema: ConditionalRecordSchema, Record: s.record, Pin: expected}
	if !conditionalPinValid(expected) || !expected.Exists {
		return result, fmt.Errorf("exact existing revision required")
	}
	if err := s.check(ctx); err != nil {
		return result, err
	}
	current := s.genesis()
	for current != expected {
		if err := s.check(ctx); err != nil {
			return result, err
		}
		edge, file, err := s.edge(current)
		if err != nil {
			return result, err
		}
		err = errors.Join(file.Sync(), file.Close())
		if err != nil {
			return result, err
		}
		current = edge.Current
	}
	result.FileSynced = true
	directory, err := s.directory.root.Open(".")
	if err != nil {
		return result, err
	}
	err = errors.Join(directory.Sync(), directory.Close())
	if err != nil {
		return result, err
	}
	result.DirectorySynced = true
	if err = s.check(ctx); err != nil {
		return result, err
	}
	// Re-read the exact lineage after syncing. Visibility and fsync flags alone
	// must not acknowledge a record whose named bytes changed during the call.
	current = s.genesis()
	for current != expected {
		if err = s.check(ctx); err != nil {
			return result, err
		}
		edge, file, readErr := s.edge(current)
		if readErr != nil {
			return result, readErr
		}
		if err = file.Close(); err != nil {
			return result, err
		}
		current = edge.Current
	}
	result.DirectoryObject, err = s.directory.DirectoryObjectDigest()
	return result, err
}

// Observe reads the exact successor of a verified predecessor, including historical
// successors. It does not publish, synchronize, infer acknowledgement or select work.
func (s *ConditionalRecordStore) Observe(ctx context.Context, previous ConditionalRecordPin, id, contentSHA256 string) (ConditionalRecordObservation, error) {
	result := ConditionalRecordObservation{Previous: previous, UpdateID: id, ContentSHA256: contentSHA256}
	if err := s.check(ctx); err != nil {
		return result, err
	}
	if !conditionalPinValid(previous) || id == "" || !atomicPinValid(AtomicFilePin{Exists: true, SHA256: contentSHA256}) {
		return result, fmt.Errorf("exact observation bindings required")
	}
	current := s.genesis()
	for current != previous {
		if err := s.check(ctx); err != nil {
			return result, err
		}
		edge, file, err := s.edge(current)
		if errors.Is(err, os.ErrNotExist) {
			return result, fmt.Errorf("observation predecessor is not in selected lineage")
		}
		if err != nil {
			return result, err
		}
		if err = file.Close(); err != nil {
			return result, err
		}
		current = edge.Current
	}
	edge, file, err := s.edge(previous)
	if errors.Is(err, os.ErrNotExist) {
		result.Status = "absent"
		return result, s.check(ctx)
	}
	if err != nil {
		return result, err
	}
	if err = file.Close(); err != nil {
		return result, err
	}
	if edge.UpdateID == id && edge.Current.SHA256 != contentSHA256 {
		return result, fmt.Errorf("observed update identity conflicts with completed content")
	}
	result.Status = "contended"
	if edge.UpdateID == id {
		result.Status = "recorded"
	}
	result.Publication = &ConditionalRecordPublication{Status: result.Status, Previous: previous, Prepared: edge.Current, Observed: ConditionalRecordSnapshot{Pin: edge.Current, Content: edge.Content}}
	return result, s.check(ctx)
}
