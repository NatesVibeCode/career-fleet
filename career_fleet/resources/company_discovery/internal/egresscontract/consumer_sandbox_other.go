//go:build !darwin

package egresscontract

import (
	"context"
	"fmt"
	"os/exec"
)

// Other targets require their actual native isolation implementation before
// this consumer capability can qualify there. Never substitute an unrestricted
// command or label a denied execution as a successful consumer check.
func consumerCommand(context.Context, WorkspaceProcedure, string, []string) (*exec.Cmd, error) {
	return nil, fmt.Errorf("consumer_isolation_unavailable: native implementation required for this target")
}
