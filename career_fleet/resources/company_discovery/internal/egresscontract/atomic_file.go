//go:build darwin || linux

package egresscontract

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type AtomicFileError struct {
	Stage           string
	Published       bool
	DirectorySynced bool
	Cause           error
}

func (e *AtomicFileError) Error() string { return "atomic file " + e.Stage + ": " + e.Cause.Error() }
func (e *AtomicFileError) Unwrap() error { return e.Cause }

type atomicDirectoryPin struct {
	path string
	info os.FileInfo
}
type AtomicFileStore struct {
	root               *os.Root
	path, record, lock string
	directories        []atomicDirectoryPin
}

// OpenAtomicFileStore requires an existing absolute deployment directory and
// two distinct pinned basenames. It creates neither directories nor records.
// Every existing path component, record, and lock must be nonsymlink/regular.
// Darwin and Linux use descriptor containment plus O_NOFOLLOW inode checks.
func OpenAtomicFileStore(rootPath, recordName, lockName string) (*AtomicFileStore, error) {
	if !filepath.IsAbs(rootPath) || filepath.Clean(rootPath) != rootPath || !atomicBasename(recordName) || !atomicBasename(lockName) || recordName == lockName {
		return nil, fmt.Errorf("explicit clean absolute root and distinct basenames required")
	}
	s := &AtomicFileStore{path: rootPath, record: recordName, lock: lockName}
	parts := strings.Split(strings.TrimPrefix(rootPath, string(os.PathSeparator)), string(os.PathSeparator))
	current := string(os.PathSeparator)
	paths := []string{current}
	for _, part := range parts {
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
			return nil, fmt.Errorf("root component must be nonsymlink directory: %s", path)
		}
		s.directories = append(s.directories, atomicDirectoryPin{path, info})
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	s.root = root
	if err := s.verifyRoot(); err != nil {
		root.Close()
		return nil, err
	}
	for _, name := range []string{recordName, lockName} {
		if _, err := s.regularInfo(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			root.Close()
			return nil, err
		}
	}
	return s, nil
}
func atomicBasename(s string) bool {
	return s != "" && s != "." && s != ".." && filepath.Base(s) == s && !strings.ContainsAny(s, "/\\\x00")
}
func (s *AtomicFileStore) Close() error {
	if s == nil || s.root == nil {
		return nil
	}
	return s.root.Close()
}
func (s *AtomicFileStore) verifyRoot() error {
	if s == nil || s.root == nil {
		return fmt.Errorf("atomic store absent")
	}
	for _, pin := range s.directories {
		current, err := os.Lstat(pin.path)
		if err != nil {
			return err
		}
		if current.Mode()&os.ModeSymlink != 0 || !current.IsDir() || !os.SameFile(pin.info, current) {
			return fmt.Errorf("deployment root component changed: %s", pin.path)
		}
	}
	opened, err := s.root.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(opened, s.directories[len(s.directories)-1].info) {
		return fmt.Errorf("opened root identity changed")
	}
	return nil
}
func (s *AtomicFileStore) regularInfo(name string) (os.FileInfo, error) {
	info, err := s.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("pinned file must be nonsymlink regular file: %s", name)
	}
	return info, nil
}
func (s *AtomicFileStore) verifyNamedFile(name string, file *os.File, before os.FileInfo) (os.FileInfo, error) {
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	named, err := s.regularInfo(name)
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(opened, named) || before != nil && !os.SameFile(before, opened) {
		return nil, fmt.Errorf("pinned file inode changed: %s", name)
	}
	return opened, nil
}
func (s *AtomicFileStore) acquire(ctx context.Context) (*os.File, error) {
	if ctx == nil {
		return nil, fmt.Errorf("supplied context required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.verifyRoot(); err != nil {
		return nil, err
	}
	before, err := s.regularInfo(s.lock)
	var file *os.File
	if errors.Is(err, os.ErrNotExist) {
		file, err = s.root.OpenFile(s.lock, os.O_CREATE|os.O_EXCL|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
		// A concurrent cooperating opener may have created the same persistent
		// lock. This is lock acquisition, never a retry of a record mutation.
		if errors.Is(err, os.ErrExist) {
			before, err = s.regularInfo(s.lock)
			if err == nil {
				file, err = s.root.OpenFile(s.lock, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			}
		}
	} else if err == nil {
		file, err = s.root.OpenFile(s.lock, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	}
	if err != nil {
		return nil, err
	}
	if _, err := s.verifyNamedFile(s.lock, file, before); err != nil {
		file.Close()
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		err := lockExclusiveNB(file)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			file.Close()
			return nil, err
		}
		// Observation cadence is not a timeout, attempt limit, or write retry.
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if _, err := s.verifyNamedFile(s.lock, file, before); err != nil {
		unlockExclusive(file)
		file.Close()
		return nil, err
	}
	if err := s.verifyRoot(); err != nil {
		unlockExclusive(file)
		file.Close()
		return nil, err
	}
	return file, nil
}
func atomicRelease(file *os.File) error { return errors.Join(unlockExclusive(file), file.Close()) }
func atomicSHA(raw []byte) string       { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func atomicPinValid(pin AtomicFilePin) bool {
	if !pin.Exists {
		return pin.SHA256 == ""
	}
	if len(pin.SHA256) != 64 {
		return false
	}
	for _, c := range pin.SHA256 {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func (s *AtomicFileStore) readLocked() (AtomicFileSnapshot, os.FileInfo, error) {
	result := AtomicFileSnapshot{Bytes: []byte{}}
	before, err := s.regularInfo(s.record)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil, nil
	}
	if err != nil {
		return result, nil, err
	}
	file, err := s.root.OpenFile(s.record, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return result, nil, err
	}
	opened, err := s.verifyNamedFile(s.record, file, before)
	if err != nil {
		file.Close()
		return result, nil, err
	}
	raw, readErr := io.ReadAll(file)
	after, statErr := s.verifyNamedFile(s.record, file, opened)
	closeErr := file.Close()
	if err := errors.Join(readErr, statErr, closeErr); err != nil {
		return result, nil, err
	}
	if opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) || int64(len(raw)) != after.Size() {
		return result, nil, fmt.Errorf("record changed during exact read")
	}
	return AtomicFileSnapshot{AtomicFilePin{true, atomicSHA(raw)}, raw}, after, nil
}
func (s *AtomicFileStore) Read(ctx context.Context) (snapshot AtomicFileSnapshot, err error) {
	lock, err := s.acquire(ctx)
	if err != nil {
		return snapshot, err
	}
	defer func() { err = errors.Join(err, atomicRelease(lock)) }()
	snapshot, _, err = s.readLocked()
	if err == nil {
		err = s.verifyRoot()
	}
	if err == nil {
		err = ctx.Err()
	}
	return snapshot, err
}

// CompareAndReplace performs at most one publication. Staging, precondition
// recheck, file fsync, atomic rename and directory fsync occur under one flock.
// Cancellation before publication refuses; after publication it still finishes
// durability accounting and returns a typed published result. No uncertain
// outcome is replayed. Cooperating callers must never unlink the lock file.
func (s *AtomicFileStore) CompareAndReplace(ctx context.Context, expected AtomicFilePin, body []byte) (result AtomicFileWriteResult, err error) {
	body = bytes.Clone(body)
	result.Prepared = AtomicFilePin{true, atomicSHA(body)}
	fail := func(stage string, cause error) (AtomicFileWriteResult, error) {
		return result, &AtomicFileError{stage, result.Published, result.DirectorySynced, cause}
	}
	if !atomicPinValid(expected) {
		return fail("precondition", fmt.Errorf("exact existence/raw digest required"))
	}
	lock, err := s.acquire(ctx)
	if err != nil {
		return fail("lock", err)
	}
	defer func() {
		if e := atomicRelease(lock); e != nil {
			if err == nil {
				err = &AtomicFileError{"unlock", result.Published, result.DirectorySynced, e}
			} else {
				err = errors.Join(err, e)
			}
		}
	}()
	old, oldInfo, err := s.readLocked()
	if err != nil {
		return fail("read", err)
	}
	result.Before = old.Pin
	result.BeforeObserved = true
	result.After = old.Pin
	result.AfterObserved = true
	if old.Pin != expected {
		return fail("precondition", fmt.Errorf("record existence or raw digest changed"))
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fail("stage", err)
	}
	temporary := ".atomic-" + hex.EncodeToString(nonce[:])
	file, err := s.root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fail("stage", err)
	}
	stagedInfo, err := file.Stat()
	if err != nil {
		file.Close()
		// Without an inode observation, do not unlink a possibly substituted
		// pathname. The scoped adapter retains the staging failure for recovery.
		return fail("stage", err)
	}
	defer func() {
		if info, e := s.regularInfo(temporary); e == nil && os.SameFile(stagedInfo, info) {
			_ = s.root.Remove(temporary)
		}
	}()
	if err := ctx.Err(); err != nil {
		file.Close()
		return fail("stage", err)
	}
	n, writeErr := file.Write(body)
	if writeErr == nil && n != len(body) {
		writeErr = io.ErrShortWrite
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return fail("stage", err)
	}
	if err := ctx.Err(); err != nil {
		return fail("before_publish", err)
	}
	if err := s.verifyRoot(); err != nil {
		return fail("before_publish", err)
	}
	if info, err := s.regularInfo(temporary); err != nil || !os.SameFile(stagedInfo, info) {
		if err == nil {
			err = fmt.Errorf("staged inode changed")
		}
		return fail("before_publish", err)
	}
	now, nowInfo, err := s.readLocked()
	if err != nil {
		return fail("precondition", err)
	}
	result.After = now.Pin
	if now.Pin != expected || oldInfo != nil && (nowInfo == nil || !os.SameFile(oldInfo, nowInfo)) {
		return fail("precondition", fmt.Errorf("record changed during staging"))
	}
	if _, err := s.verifyNamedFile(s.lock, lock, nil); err != nil {
		return fail("before_publish", err)
	}
	if err := ctx.Err(); err != nil {
		return fail("before_publish", err)
	}
	if err := s.root.Rename(temporary, s.record); err != nil {
		return fail("publish", err)
	}
	result.Published = true
	result.AfterObserved = false
	// After rename, directory durability is necessary accounting for an effect
	// already performed. Context cancellation cannot turn it into no-effect.
	directory, err := s.root.Open(".")
	if err != nil {
		return fail("directory_sync", err)
	}
	syncErr = directory.Sync()
	closeErr = directory.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fail("directory_sync", err)
	}
	result.DirectorySynced = true
	actual, actualInfo, err := s.readLocked()
	if err != nil {
		return fail("readback", err)
	}
	result.After = actual.Pin
	result.AfterObserved = true
	if actual.Pin != result.Prepared || actualInfo == nil || !os.SameFile(stagedInfo, actualInfo) {
		return fail("readback", fmt.Errorf("published record differs from staged exact bytes/inode"))
	}
	if err := s.verifyRoot(); err != nil {
		return fail("readback", err)
	}
	if err := ctx.Err(); err != nil {
		return fail("after_publish", err)
	}
	return result, nil
}

// Reconcile only reads. An acknowledgement must come from the scoped adapter's
// retained receipt, not a caller's claim. Equal pre/post bytes cannot identify
// an uncertain no-op publication and therefore remain explicitly ambiguous.
func (s *AtomicFileStore) Reconcile(ctx context.Context, before AtomicFilePin, prepared []byte, directoryAcknowledged bool) (AtomicFileReconciliation, error) {
	if !atomicPinValid(before) {
		return AtomicFileReconciliation{}, fmt.Errorf("exact prior pin required")
	}
	observed, err := s.Read(ctx)
	if err != nil {
		return AtomicFileReconciliation{}, err
	}
	sha := atomicSHA(prepared)
	result := AtomicFileReconciliation{"conflict", observed, sha}
	if observed.Pin.Exists && observed.Pin.SHA256 == sha {
		result.Verdict = "present_unacknowledged"
		if before.Exists && before.SHA256 == sha {
			result.Verdict = "ambiguous"
		} else if directoryAcknowledged {
			result.Verdict = "applied"
		}
	} else if observed.Pin == before && !directoryAcknowledged {
		result.Verdict = "not_applied"
	}
	return result, nil
}

// DirectoryObjectDigest binds actual opened directory objects rather than only
// their reusable path. These private kernel identities never enter model content.
func (s *AtomicFileStore) DirectoryObjectDigest() (string, error) {
	if err := s.verifyRoot(); err != nil {
		return "", err
	}
	type identity struct {
		Path   string `json:"path"`
		Device uint64 `json:"device"`
		Inode  uint64 `json:"inode"`
	}
	pins := []identity{}
	for _, dir := range s.directories {
		st, ok := dir.info.Sys().(*syscall.Stat_t)
		if !ok {
			return "", fmt.Errorf("directory kernel identity unavailable")
		}
		pins = append(pins, identity{dir.path, uint64(st.Dev), uint64(st.Ino)})
	}
	raw, err := json.Marshal(pins)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("praxis.atomic_directory_object.v1\x00"), raw...))
	return hex.EncodeToString(sum[:]), nil
}
