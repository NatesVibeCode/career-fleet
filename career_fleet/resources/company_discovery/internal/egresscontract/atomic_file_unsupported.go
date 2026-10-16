//go:build !darwin && !linux

package egresscontract

import (
	"context"
	"fmt"
)

// The platform must implement descriptor containment, nonsymlink inode checks,
// cross-process locking and durable atomic replacement before this effect can run.
type NativeAtomicFile struct{}

func (NativeAtomicFile) Name() string { return "native.atomic_file.v1" }
func (NativeAtomicFile) Supports(Policy) error {
	return fmt.Errorf("atomic file effects require supported Darwin/Linux filesystem mechanics")
}
func (a NativeAtomicFile) Start(context.Context, Invocation) (InvocationHandle, error) {
	return nil, a.Supports(Policy{})
}
