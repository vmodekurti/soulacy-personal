//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package processutil

import "os/exec"

// Other platforms retain exec.CommandContext's parent-process cancellation.
func configureCancellation(cmd *exec.Cmd) {}
