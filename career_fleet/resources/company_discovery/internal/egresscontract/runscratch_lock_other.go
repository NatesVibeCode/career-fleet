//go:build !unix

package egresscontract

import (
	"fmt"
	"os"
)

// Run scratch custody requires POSIX locks, matching the SQLite adapter's
// Unix CLI requirement. Non-Unix targets refuse closed.
func lockExclusiveNB(f *os.File) error {
	return fmt.Errorf("run scratch requires POSIX locks")
}

func tryLockExclusiveNB(f *os.File) bool {
	return false
}

func unlockExclusive(f *os.File) error {
	return fmt.Errorf("run scratch requires POSIX locks")
}
