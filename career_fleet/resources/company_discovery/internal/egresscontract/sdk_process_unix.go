//go:build darwin || linux

package egresscontract

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// Graceful owner interruption cancels the SDK process group and lets the
// printed Process retain its failed/uncertain receipt before exiting.
func SDKInvocationContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}

// ConfigureSDKCommand keeps the local SDK facade and its child processes in one
// cancellation group. It does not cancel a remote job or assert remote outcome.
func ConfigureSDKCommand(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}
