// Package binpath locates executables shipped alongside the Soulacy gateway.
package binpath

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Find first consults PATH, then checks beside the running executable. Service
// managers intentionally start Soulacy with a minimal PATH, while release
// archives install companion binaries such as sy in the same directory.
func Find(name string) (string, error) {
	self, _ := os.Executable()
	return find(name, self, runtime.GOOS, exec.LookPath)
}

func find(name, self, goos string, lookPath func(string) (string, error)) (string, error) {
	if found, err := lookPath(name); err == nil {
		if absolute, absErr := filepath.Abs(found); absErr == nil {
			return absolute, nil
		}
		return found, nil
	}

	if self != "" {
		candidate := filepath.Join(filepath.Dir(self), name)
		if goos == "windows" && filepath.Ext(candidate) == "" {
			candidate += ".exe"
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() &&
			(goos == "windows" || info.Mode()&0o111 != 0) {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("%s executable was not found on PATH or beside the Soulacy gateway", name)
}
