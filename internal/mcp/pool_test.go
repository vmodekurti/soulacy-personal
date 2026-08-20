// pool_test.go — two workspaces, two sets of processes, two directories.
//
// The tests run a real subprocess (`/bin/sh` speaking JSON-RPC on stdin) rather
// than a fake transport, because the property under test is a property of the
// SPAWNED PROCESS — its working directory and its environment. A fake
// transport would let the assertions pass while cmd.Dir stayed unset, which is
// precisely the bug.
package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/sandbox"
)

// echoServer is a minimal MCP server in shell: it answers initialize and
// tools/list, and writes its working directory to a file so the test can see
// where the kernel actually put it.
func echoServer(marker string) ServerConfig {
	script := `
printf '' > ` + marker + `
pwd > ` + marker + `
while IFS= read -r line; do
  case "$line" in
    *'"initialize"'*) printf '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"t","version":"1"}}}\n' ;;
    *'"tools/list"'*) printf '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"ping","description":"p","inputSchema":{}}]}}\n' ;;
  esac
done
`
	return ServerConfig{Transport: "stdio", Command: "/bin/sh", Args: []string{"-c", script}}
}

type fixedConfinement struct {
	dirs map[string]string
}

func (f fixedConfinement) WorkspaceConfinement(workspaceID string) (string, sandbox.Limits, string, error) {
	dir, ok := f.dirs[workspaceID]
	if !ok {
		return "", sandbox.Limits{}, "", os.ErrNotExist
	}
	return dir, sandbox.Limits{}, "", nil
}

// resolvedTempDir is t.TempDir() with symlinks resolved.
//
// A subprocess reports the directory it is really in, so a test comparing that
// against an unresolved temp path fails wherever the temp root is a link:
// /var on macOS, or any TMPDIR pointed at one. Resolving here keeps the
// comparison exact instead of loosening it to a suffix.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	return resolved
}

func TestTwoWorkspacesGetTwoProcessesInTwoDirectories(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("sh-based test")
	}
	// Resolved, because the subprocess reports the directory it is ACTUALLY
	// in and the kernel gives it the real path. Comparing an unresolved temp
	// dir against that fails wherever the temp root is a symlink — /var on
	// macOS, and any TMPDIR pointed at a link.
	dirA, dirB := resolvedTempDir(t), resolvedTempDir(t)
	pool := NewPool(
		Config{Servers: map[string]ServerConfig{"echo": echoServer("marker.txt")}},
		fixedConfinement{dirs: map[string]string{"ws-a": dirA, "ws-b": dirB}},
		zap.NewNop(),
	)
	defer pool.Close()

	for _, ws := range []string{"ws-a", "ws-b"} {
		if got := len(pool.For(ws).AllTools()); got != 1 {
			t.Fatalf("%s: %d tools, want 1 — the server did not start, so this test proves nothing", ws, got)
		}
	}

	// The marker is a RELATIVE path, which is the whole point: before this,
	// both workspaces' servers resolved it against the gateway's directory and
	// it was one file. Two files with different contents is the isolation.
	for dir, want := range map[string]string{dirA: dirA, dirB: dirB} {
		raw, err := os.ReadFile(filepath.Join(dir, "marker.txt"))
		if err != nil {
			t.Fatalf("no marker in %s — the server did not start there: %v", dir, err)
		}
		got := strings.TrimSpace(string(raw))
		// An exact comparison now that both sides are resolved. The suffix
		// match this replaces was tuned to macOS's /private prefix and passed
		// on any path that merely ENDED the right way — including, in
		// principle, the wrong tree with a coincidental tail.
		if got != want {
			t.Errorf("server for %s started in %q, want %q", dir, got, want)
		}
	}
}

func TestTheSameWorkspaceReusesItsClient(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("sh-based test")
	}
	dir := t.TempDir()
	pool := NewPool(
		Config{Servers: map[string]ServerConfig{"echo": echoServer("marker.txt")}},
		fixedConfinement{dirs: map[string]string{"ws-a": dir}},
		zap.NewNop(),
	)
	defer pool.Close()

	// Not merely an optimisation: a second client would mean a second set of
	// subprocesses, so every tool call would have a duplicate side effect and
	// nothing would report it.
	if pool.For("ws-a") != pool.For("ws-a") {
		t.Error("the same workspace got two clients, so its servers are running twice")
	}
}

func TestAWorkspaceWithNoConfinementGetsNoServers(t *testing.T) {
	pool := NewPool(
		Config{Servers: map[string]ServerConfig{"echo": echoServer("marker.txt")}},
		fixedConfinement{dirs: map[string]string{"ws-a": t.TempDir()}},
		zap.NewNop(),
	)
	defer pool.Close()

	// FAIL CLOSED. The tempting fallback — start the servers in the default
	// directory — is the shared directory this whole file exists to remove,
	// wearing the name of a default.
	client := pool.For("ws-unknown")
	if client == nil {
		t.Fatal("For returned nil; a dozen call sites would panic on that")
	}
	if got := len(client.AllTools()); got != 0 {
		t.Errorf("%d tools for a workspace with no confinement, want 0", got)
	}
}

func TestRevokingAServerInOneWorkspaceLeavesTheOtherRunning(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("sh-based test")
	}
	pool := NewPool(
		Config{Servers: map[string]ServerConfig{"echo": echoServer("marker.txt")}},
		fixedConfinement{dirs: map[string]string{"ws-a": t.TempDir(), "ws-b": t.TempDir()}},
		zap.NewNop(),
	)
	defer pool.Close()

	if len(pool.For("ws-a").AllTools()) != 1 || len(pool.For("ws-b").AllTools()) != 1 {
		t.Fatal("servers did not start")
	}
	if err := pool.RemoveServer("ws-a", "echo"); err != nil {
		t.Fatal(err)
	}
	if got := len(pool.For("ws-a").AllTools()); got != 0 {
		t.Errorf("ws-a still has %d tools after revocation", got)
	}
	// The half that is easy to get wrong: revocation is per workspace, and a
	// revoke that reached the shared client would silently disable the
	// extension for every tenant.
	if got := len(pool.For("ws-b").AllTools()); got != 1 {
		t.Errorf("ws-b lost its server when ws-a revoked theirs: %d tools", got)
	}
}

func TestARevokedServerDoesNotComeBackOnReload(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("sh-based test")
	}
	template := Config{Servers: map[string]ServerConfig{"echo": echoServer("marker.txt")}}
	pool := NewPool(template, fixedConfinement{dirs: map[string]string{"ws-a": t.TempDir()}}, zap.NewNop())
	defer pool.Close()

	pool.For("ws-a")
	if err := pool.RemoveServer("ws-a", "echo"); err != nil {
		t.Fatal(err)
	}
	// A config reload rebuilds every client from the template. Without the
	// per-workspace tombstone the revoked server would be back, and the
	// revocation would look like it had silently failed at an unrelated
	// moment — an operator edits config.yaml and an extension somebody
	// removed starts running again.
	pool.ReplaceTemplate(template)
	if got := len(pool.For("ws-a").AllTools()); got != 0 {
		t.Errorf("a revoked server came back after a config reload: %d tools", got)
	}
}
