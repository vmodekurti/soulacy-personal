//go:build !linux && !darwin

package sandbox

import (
	"fmt"
	"os"
	"os/exec"
)

// applyLimits is a no-op on platforms that don't support POSIX rlimits
// the way Linux/Darwin do (currently: Windows). The sandbox subcommand
// still passes the wrapped command through to exec.LookPath/exec.Command
// so the engine doesn't have to branch on GOOS.
func applyLimits(_ Limits) error { return nil }

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
