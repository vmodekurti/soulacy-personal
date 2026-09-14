package updates

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// InstallDir reports where updates would be written (the running
// executable's directory unless overridden).
func InstallDir(explicit string) (string, error) { return resolveUpdateInstallDir(explicit) }

// InstallDirWritable reports whether the process can replace binaries in dir.
// It creates and removes a probe file rather than trusting mode bits, which
// lie under ACLs, read-only mounts and container overlays.
func InstallDirWritable(dir string) bool {
	probe, err := os.CreateTemp(dir, ".soulacy-update-probe-*")
	if err != nil {
		return false
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return true
}

// InContainer reports whether the gateway is running inside a container. In
// that case binaries are owned by the image, so the right upgrade path is a
// new image, never an in-place replacement.
func InContainer() bool {
	if v := strings.TrimSpace(os.Getenv("SOULACY_IN_CONTAINER")); v != "" {
		return v != "0" && !strings.EqualFold(v, "false")
	}
	if os.Getenv("container") != "" {
		return true
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(data)
		for _, marker := range []string{"docker", "containerd", "kubepods", "libpod", "lxc"} {
			if strings.Contains(s, marker) {
				return true
			}
		}
	}
	return false
}

// VerifyInstalledBinary runs the freshly installed gateway binary with
// --version and checks that it starts and reports the expected version. This
// is the last gate before the running process replaces itself: a binary that
// cannot even print its version must never be exec'd as the new gateway.
func VerifyInstalledBinary(ctx context.Context, installDir, wantVersion string) error {
	bin := filepath.Join(installDir, "soulacy")
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("new binary failed to run: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	got := strings.TrimSpace(string(out))
	want := strings.TrimPrefix(strings.TrimSpace(wantVersion), "v")
	if strings.TrimPrefix(got, "v") != want {
		return fmt.Errorf("new binary reports version %q, expected %q", got, wantVersion)
	}
	return nil
}

// RestoreBackups puts the previous binaries back after a failed post-install
// verification. Backups are named <dest>.bak-<stamp> by installUpdateFiles.
func RestoreBackups(backups []string) error {
	var errs []error
	for _, backup := range backups {
		i := strings.LastIndex(backup, ".bak-")
		if i <= 0 {
			errs = append(errs, fmt.Errorf("unrecognised backup name %q", backup))
			continue
		}
		dest := backup[:i]
		if err := renameUpdateFile(backup, dest); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", dest, err))
		}
	}
	return errors.Join(errs...)
}
