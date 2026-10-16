//go:build darwin || linux

package egresscontract

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
)

// acquireAcknowledgementLock requires the persistent lock already to exist.
// Recreating an erased lock could split cooperating writers into two locks.
func (s *AtomicFileStore) acquireAcknowledgementLock(ctx context.Context) (*os.File, error) {
	if ctx == nil {
		return nil, fmt.Errorf("supplied acknowledgement context required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.verifyRoot(); err != nil {
		return nil, err
	}
	before, err := s.regularInfo(s.lock)
	if err != nil {
		return nil, err
	}
	file, err := s.root.OpenFile(s.lock, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	refuse := func(err error) (*os.File, error) { return nil, errors.Join(err, file.Close()) }
	if _, err = s.verifyNamedFile(s.lock, file, before); err != nil {
		return refuse(err)
	}
	for {
		if err = ctx.Err(); err != nil {
			return refuse(err)
		}
		err = lockExclusiveNB(file)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return refuse(err)
		}
		// Lock observation cadence only; no effect retry or deadline is invented.
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return refuse(ctx.Err())
		case <-timer.C:
		}
	}
	if _, err = s.verifyNamedFile(s.lock, file, before); err != nil {
		return nil, errors.Join(err, atomicRelease(file))
	}
	if err = s.verifyRoot(); err != nil {
		return nil, errors.Join(err, atomicRelease(file))
	}
	return file, nil
}

// Acknowledge verifies an existing exact record under its existing scoped lock,
// syncs that opened record and its same opened deployment directory, and checks
// exact readback/inodes. It creates/writes/renames neither record nor lock.
// Original publication intent and receipt authority remain separate owners.
func (s *AtomicFileStore) Acknowledge(ctx context.Context, expected AtomicFilePin) (AtomicFileAcknowledgement, error) {
	return s.acknowledge(ctx, expected, func(file *os.File) error { return file.Sync() })
}

// The private sync boundary allows safe-failure tests on actual closed local
// descriptors. No caller/operation wire can substitute a sync implementation.
func (s *AtomicFileStore) acknowledge(ctx context.Context, expected AtomicFilePin, syncFile func(*os.File) error) (result AtomicFileAcknowledgement, err error) {
	result.Expected = expected
	fail := func(stage string, cause error) (AtomicFileAcknowledgement, error) {
		return result, &AtomicFileError{Stage: stage, Published: false, DirectorySynced: result.DirectorySynced, Cause: cause}
	}
	if !expected.Exists || !atomicPinValid(expected) {
		return fail("precondition", fmt.Errorf("exact expected existing raw content digest required"))
	}
	lock, err := s.acquireAcknowledgementLock(ctx)
	if err != nil {
		return fail("lock", err)
	}
	lockInfo, err := s.verifyNamedFile(s.lock, lock, nil)
	if err != nil {
		return fail("lock", errors.Join(err, atomicRelease(lock)))
	}
	defer func() {
		_, identityErr := s.verifyNamedFile(s.lock, lock, lockInfo)
		releaseErr := errors.Join(identityErr, s.verifyRoot(), atomicRelease(lock))
		if releaseErr != nil {
			if err == nil {
				err = &AtomicFileError{Stage: "unlock", Published: false, DirectorySynced: result.DirectorySynced, Cause: releaseErr}
			} else {
				err = errors.Join(err, releaseErr)
			}
		}
	}()
	before, recordInfo, err := s.readLocked()
	if err != nil {
		return fail("read", err)
	}
	result.Observed = before.Pin
	result.BeforeObserved = true
	if before.Pin != expected || recordInfo == nil {
		return fail("precondition", fmt.Errorf("expected existing exact record differs"))
	}
	result.DirectoryObjectSHA256, err = s.DirectoryObjectDigest()
	if err != nil {
		return fail("identity", err)
	}
	record, err := s.root.OpenFile(s.record, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fail("record_open", err)
	}
	defer func() {
		if closeErr := record.Close(); closeErr != nil {
			if err == nil {
				err = &AtomicFileError{Stage: "record_close", Published: false, DirectorySynced: result.DirectorySynced, Cause: closeErr}
			} else {
				err = errors.Join(err, closeErr)
			}
		}
	}()
	opened, err := s.verifyNamedFile(s.record, record, recordInfo)
	if err != nil {
		return fail("record_open", err)
	}
	raw, err := io.ReadAll(record)
	if err != nil {
		return fail("read", err)
	}
	latest, err := s.verifyNamedFile(s.record, record, opened)
	if err != nil {
		return fail("precondition", err)
	}
	if latest.Size() != opened.Size() || !latest.ModTime().Equal(opened.ModTime()) || int64(len(raw)) != latest.Size() || atomicSHA(raw) != expected.SHA256 {
		return fail("precondition", fmt.Errorf("record changed before durability effects"))
	}
	if err = s.verifyRoot(); err != nil {
		return fail("identity", err)
	}
	if _, err = s.verifyNamedFile(s.lock, lock, lockInfo); err != nil {
		return fail("lock", err)
	}
	if err = ctx.Err(); err != nil {
		return fail("before_sync", err)
	}
	if err = syncFile(record); err != nil {
		return fail("record_sync", err)
	}
	result.FileSynced = true
	if err = ctx.Err(); err != nil {
		return fail("after_record_sync", err)
	}
	if err = s.verifyRoot(); err != nil {
		return fail("identity", err)
	}
	if _, err = s.verifyNamedFile(s.record, record, opened); err != nil {
		return fail("identity", err)
	}
	if _, err = s.verifyNamedFile(s.lock, lock, lockInfo); err != nil {
		return fail("lock", err)
	}
	directory, err := s.root.OpenFile(".", os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fail("directory_open", err)
	}
	dirInfo, statErr := directory.Stat()
	if statErr != nil || !dirInfo.IsDir() || !os.SameFile(dirInfo, s.directories[len(s.directories)-1].info) {
		return fail("directory_open", errors.Join(statErr, fmt.Errorf("opened directory identity differs"), directory.Close()))
	}
	syncErr := syncFile(directory)
	closeErr := directory.Close()
	if syncErr != nil {
		return fail("directory_sync", errors.Join(syncErr, closeErr))
	}
	result.DirectorySynced = true
	if closeErr != nil {
		return fail("directory_close", closeErr)
	}
	actual, actualInfo, err := s.readLocked()
	if err != nil {
		return fail("readback", err)
	}
	result.Observed = actual.Pin
	if actualInfo == nil || !os.SameFile(opened, actualInfo) || actual.Pin != expected {
		return fail("readback", fmt.Errorf("acknowledged bytes/inode changed during durability effects"))
	}
	if _, err = s.verifyNamedFile(s.record, record, opened); err != nil {
		return fail("readback", err)
	}
	if err = s.verifyRoot(); err != nil {
		return fail("readback", err)
	}
	if _, err = s.verifyNamedFile(s.lock, lock, lockInfo); err != nil {
		return fail("readback", err)
	}
	result.AfterObserved = true
	if err = ctx.Err(); err != nil {
		return fail("after_acknowledge", err)
	}
	return result, nil
}
