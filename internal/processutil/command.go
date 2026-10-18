// Package processutil keeps cancellation attached to a local tool's process tree.
package processutil

import (
	"context"
	"os/exec"
	"time"
)

func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 2 * time.Second
	configureCancellation(cmd)
	return cmd
}
