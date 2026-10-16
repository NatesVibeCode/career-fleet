//go:build unix

package egresscontract

import (
	"os"
	"syscall"
)

// lockExclusiveNB takes an exclusive flock without blocking.
func lockExclusiveNB(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// tryLockExclusiveNB reports whether an exclusive flock is acquirable
// without blocking. A held lock reports false; any other outcome that
// still acquires reports true after releasing.
func tryLockExclusiveNB(f *os.File) bool {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil
}

// unlockExclusive releases a held flock.
func unlockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
