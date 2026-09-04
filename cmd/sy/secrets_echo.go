package main

import (
	"os"
	"os/exec"
)

// disableEcho turns off terminal echo for the controlling TTY so a prompted
// secret is not displayed as it is typed. It returns true if echo was disabled
// (and therefore must be restored). It is best-effort: if stdin is not a
// terminal or `stty` is unavailable, it returns false and the caller proceeds
// with a plain read.
func disableEcho() bool {
	fi, err := os.Stdin.Stat()
	if err != nil || (fi.Mode()&os.ModeCharDevice) == 0 {
		return false // not a terminal (piped/redirected)
	}
	cmd := exec.Command("stty", "-echo")
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

// restoreEcho re-enables terminal echo if disableEcho previously turned it off.
func restoreEcho(was bool) {
	if !was {
		return
	}
	cmd := exec.Command("stty", "echo")
	cmd.Stdin = os.Stdin
	_ = cmd.Run()
}
