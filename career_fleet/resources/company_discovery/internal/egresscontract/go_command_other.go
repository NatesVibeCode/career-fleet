//go:build !darwin

package egresscontract

import (
	"context"
	"fmt"
	"os/exec"
)

func goSandboxCommand(context.Context, GoRuntimePin, []string, map[string]string, []string, string) (*exec.Cmd, error) {
	return nil, fmt.Errorf("qualified Go command sandbox unavailable on this target")
}
