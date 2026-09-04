package introspect

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/sandbox"
	"github.com/soulacy/soulacy/pkg/plugin"
)

// DryRunConfig controls the sandboxed dry-run (check 3).
type DryRunConfig struct {
	// SelfPath is the soulacy binary for the __exec-sandbox re-exec wrapper.
	// Empty (or Limits.Enabled false) runs the hook directly — rlimits are
	// best-effort, the timeout and write detection always apply.
	SelfPath string
	// Limits are the rlimits applied through the sandbox wrapper.
	Limits sandbox.Limits
	// Timeout bounds each hook execution (default 5s). Long-running sidecar
	// daemons hitting the timeout without crashing is treated as a healthy
	// outcome, not a failure.
	Timeout time.Duration
}

// DryRun executes the package's declared startup hooks (manifest v2 sidecar
// specs) inside the staging directory and reports exit status, runtime, and
// file writes. HTTP egress is pointed at a dead loopback proxy for the
// duration — well-behaved clients cannot phone home; raw-socket escapes are
// the static scan's job to flag.
func DryRun(ctx context.Context, dir string, m *plugin.Manifest, cfg DryRunConfig) []Finding {
	hooks := startupHooks(m)
	if len(hooks) == 0 {
		return []Finding{{
			Check: "dry_run", Severity: SeverityInfo,
			Message: "no startup hooks declared; dry-run skipped",
		}}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}

	var findings []Finding
	for _, h := range hooks {
		findings = append(findings, runHook(ctx, dir, h, cfg)...)
	}
	return findings
}

type hook struct {
	label   string
	command string
	args    []string
}

func startupHooks(m *plugin.Manifest) []hook {
	if m == nil {
		return nil
	}
	var hooks []hook
	for _, ch := range m.Channels {
		if ch.Sidecar != nil && ch.Sidecar.Command != "" {
			hooks = append(hooks, hook{
				label:   "channel " + ch.ID,
				command: ch.Sidecar.Command,
				args:    ch.Sidecar.Args,
			})
		}
	}
	return hooks
}

func runHook(ctx context.Context, dir string, h hook, cfg DryRunConfig) []Finding {
	before := snapshotDir(dir)

	runCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	argv := sandbox.Wrap(cfg.SelfPath, cfg.Limits, append([]string{h.command}, h.args...))
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = dir
	// Point HTTP egress at a dead loopback proxy: nothing listens on
	// 127.0.0.1:9 (discard), so http(s) clients that honour proxy env vars
	// fail fast instead of phoning home during the dry-run.
	cmd.Env = append(os.Environ(),
		"HTTP_PROXY=http://127.0.0.1:9",
		"HTTPS_PROXY=http://127.0.0.1:9",
		"http_proxy=http://127.0.0.1:9",
		"https_proxy=http://127.0.0.1:9",
		"NO_PROXY=", "no_proxy=",
		"SOULACY_DRY_RUN=1",
	)

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start).Round(time.Millisecond)

	var findings []Finding
	switch {
	case runCtx.Err() == context.DeadlineExceeded:
		findings = append(findings, Finding{
			Check: "dry_run", Severity: SeverityInfo,
			Message: fmt.Sprintf("%s: still running after %s (daemon-style sidecar) — terminated by the dry-run timeout", h.label, cfg.Timeout),
		})
	case err == nil:
		findings = append(findings, Finding{
			Check: "dry_run", Severity: SeverityInfo,
			Message: fmt.Sprintf("%s: exit 0 in %s", h.label, elapsed),
		})
	default:
		exitCode := -1
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		}
		findings = append(findings, Finding{
			Check: "dry_run", Severity: SeverityWarning,
			Message: fmt.Sprintf("%s: exit %d in %s (%v)", h.label, exitCode, elapsed, err),
		})
	}

	if writes := diffSnapshot(before, snapshotDir(dir)); len(writes) > 0 {
		findings = append(findings, Finding{
			Check: "dry_run", Severity: SeverityWarning,
			Message: fmt.Sprintf("%s: wrote files during startup: %s", h.label, strings.Join(writes, ", ")),
		})
	}
	return findings
}

// snapshotDir records (relative path → size@mtime) for write detection.
func snapshotDir(dir string) map[string]string {
	snap := map[string]string{}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			rel = path
		}
		snap[rel] = fmt.Sprintf("%d@%d", info.Size(), info.ModTime().UnixNano())
		return nil
	})
	return snap
}

func diffSnapshot(before, after map[string]string) []string {
	var changed []string
	for path, sig := range after {
		if prev, ok := before[path]; !ok || prev != sig {
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	return changed
}
