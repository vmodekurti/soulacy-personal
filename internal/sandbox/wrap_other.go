//go:build !linux && !darwin

package sandbox

import (
	"fmt"
	"os"
	"os/exec"
)

// applyLimits fails closed on platforms that cannot enforce POSIX limits.
// Personal deployments can explicitly disable the compatibility wrapper; a
// configured guard must never become a silent passthrough.
func applyLimits(_ Limits) error { return fmt.Errorf("POSIX resource limits are unavailable on this platform") }

// execCommand falls back to the os/exec stdlib: spawn the command,
// wait for it, mirror its exit code. We can't execve on Windows from
// pure Go (no syscall.Exec equivalent that releases the parent), so
// the wrapper stays in the process tree — that's fine because nothing
// downstream depends on the parent PID being the python process.
func execCommand(cmd []string, envAllow []string) error {
	if len(cmd) == 0 {
		return fmt.Errorf("empty command")
	}
	c := exec.Command(cmd[0], cmd[1:]...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	// SEC-5: scrub the environment to the base allowlist + agent-declared
	// names rather than inheriting the gateway's full env (and its secrets).
	c.Env = FilteredEnviron(envAllow)
	if err := c.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		return err
	}
	os.Exit(0)
	return nil
}

func syscallEnviron() []string { return FilteredEnviron(nil) }
