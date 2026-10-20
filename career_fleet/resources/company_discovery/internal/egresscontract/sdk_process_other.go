//go:build !darwin && !linux

package egresscontract

import (
	"context"
	"fmt"
	"os/exec"
)

func SDKInvocationContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(parent)
}

func ConfigureSDKCommand(cmd *exec.Cmd) error {
	return fmt.Errorf("SDK process group cancellation is unsupported on this target")
}
