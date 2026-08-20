// dryrun_env_test.go — what an UNINSPECTED plugin can read from the gateway.
//
// The dry-run's job is to execute code the operator has not agreed to trust
// yet. It therefore has the strictest environment requirement of any
// subprocess in the repo, and had the loosest: os.Environ() verbatim.
//
// The test writes the child's environment to a file and asserts on the file
// rather than on the returned Findings, because the leak is invisible in the
// findings by construction — a hook that exfiltrates keys exits 0 and is
// reported as healthy. Asserting on what the dry-run SAYS would pass either
// way; only what the child could SEE distinguishes the two builds.
package introspect

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/plugin"
)

func TestAnUninspectedPluginCannotReadTheGatewaysSecrets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh-based test")
	}
	// Set on the PARENT, exactly as the gateway process holds a provider key.
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-LEAKED-IF-VISIBLE")
	t.Setenv("DATABASE_URL", "postgres://u:LEAKED-IF-VISIBLE@h/db")

	dir := t.TempDir()
	m := &plugin.Manifest{
		ID: "untrusted",
		Channels: []plugin.ChannelEntry{{
			ID: "c",
			Sidecar: &plugin.SidecarSpec{
				Command: "/bin/sh",
				// This is the whole attack: four characters, exit 0.
				Args: []string{"-c", "env > seen.txt"},
			},
		}},
	}

	DryRun(context.Background(), dir, m, DryRunConfig{Timeout: 5 * time.Second})

	seen, err := os.ReadFile(filepath.Join(dir, "seen.txt"))
	if err != nil {
		t.Fatalf("hook did not run, so this test proves nothing: %v", err)
	}
	if strings.Contains(string(seen), "LEAKED-IF-VISIBLE") {
		t.Errorf("an uninspected plugin's sidecar could read the gateway's credentials:\n%s", seen)
	}
	// PATH must survive, or the filter has broken the dry-run instead of
	// securing it — a hook that cannot resolve `python3` reports a spurious
	// failure and the operator learns to ignore dry-run findings.
	if !strings.Contains(string(seen), "PATH=") {
		t.Errorf("the child got no PATH, so the dry-run can no longer execute anything real:\n%s", seen)
	}
	// The dry-run's own egress block is an explicit addition, so it must
	// still be there on top of the filtered base.
	if !strings.Contains(string(seen), "HTTPS_PROXY=http://127.0.0.1:9") {
		t.Errorf("the loopback egress block was lost when the environment was filtered:\n%s", seen)
	}
}
