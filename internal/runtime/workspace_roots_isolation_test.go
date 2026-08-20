package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/wsroot"
)

// workspace_roots_isolation_test.go — MU-021. The named isolation test for
// host filesystem access and privileged subprocess scratch space.
//
// Every test here fails if the derivation in workspace_roots.go is removed:
// with one process-global root, workspace A resolves B's absolute path
// successfully and the assertions below all flip.

func inWorkspace(ctx context.Context, workspaceID string) context.Context {
	return WithPrincipal(ctx, Principal{Subject: "u_" + workspaceID, Role: "admin", WorkspaceID: workspaceID})
}

func TestFilesystemRootsAreDisjointAcrossWorkspaces(t *testing.T) {
	root := isolationRoot(t)
	e := newMinimalEngine(t)
	if err := e.SetFilesystemRoots([]string{root}); err != nil {
		t.Fatal(err)
	}

	a, err := e.WorkspaceFilesystemRoots("ws-alpha")
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.WorkspaceFilesystemRoots("ws-beta")
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("expected one root each, got %v and %v", a, b)
	}
	if a[0] == b[0] {
		t.Fatalf("two workspaces share a filesystem root: %s", a[0])
	}
	for _, pair := range [][2]string{{a[0], b[0]}, {b[0], a[0]}} {
		if pathWithinRoot(pair[0], pair[1]) {
			t.Fatalf("workspace root %s is contained in %s", pair[0], pair[1])
		}
	}
}

// isolationRoot is isolationRoot(t) with symlinks resolved.
//
// WHY EVERY TEST IN THIS FILE NEEDS IT. The filesystem policy resolves
// symlinks before deciding whether a path is inside a workspace — it has to,
// or planting a symlink is the escape. So every root the engine derives comes
// back already resolved.
//
// On macOS isolationRoot(t) hands out a path under /var, which is a symlink to
// /private/var. The test then compares the engine's resolved /private/var/...
// against its own unresolved /var/..., they do not match, and the test fails
// for a reason that has nothing to do with what it is testing. On Linux
// /tmp is usually real, so it passes there and fails only on the machine
// somebody is developing on — the worst place for a test to be wrong.
//
// Resolving here rather than loosening the comparison: pathWithinRoot is the
// production containment check, and a test that compares with something weaker
// than production stops proving anything about production.
func isolationRoot(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	return resolved
}

func TestPersonalWorkspaceRootsAreByteIdenticalToConfiguration(t *testing.T) {
	root := isolationRoot(t)
	resolvedRoot := root
	e := newMinimalEngine(t)
	if err := e.SetFilesystemRoots([]string{root}); err != nil {
		t.Fatal(err)
	}

	// Product invariant 7: a personal deployment must not notice that the
	// engine became tenant-aware. Same root, and no namespace directory is
	// brought into existence merely by resolving a path.
	for _, ctx := range []context.Context{
		context.Background(),
		inWorkspace(context.Background(), wsroot.PersonalWorkspaceID),
		inWorkspace(context.Background(), ""),
	} {
		roots, err := e.WorkspaceFilesystemRoots(WorkspaceFromContext(ctx))
		if err != nil {
			t.Fatal(err)
		}
		if len(roots) != 1 || roots[0] != filepath.Clean(resolvedRoot) {
			t.Fatalf("personal roots drifted from configuration: %v want %s", roots, resolvedRoot)
		}
	}

	target := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(target, []byte("personal"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := e.resolveFilesystemPath(context.Background(), target, false)
	if err != nil {
		t.Fatalf("personal path rejected: %v", err)
	}
	if got != filepath.Join(resolvedRoot, "notes.txt") {
		t.Fatalf("personal path was rewritten: %s", got)
	}
	if _, err := os.Stat(filepath.Join(root, wsroot.NamespaceDir)); !os.IsNotExist(err) {
		t.Fatalf("personal resolution created a namespace directory: %v", err)
	}
}

func TestOneWorkspaceCannotReadAnothersFileByAbsolutePath(t *testing.T) {
	root := isolationRoot(t)
	e := newMinimalEngine(t)
	if err := e.SetFilesystemRoots([]string{root}); err != nil {
		t.Fatal(err)
	}

	victimRoots, err := e.WorkspaceFilesystemRoots("ws-victim")
	if err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(victimRoots[0], "secret.txt")
	if err := os.WriteFile(secret, []byte("victim-only"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The victim can read its own file — the boundary is a boundary, not a ban.
	if _, err := e.resolveFilesystemPath(inWorkspace(context.Background(), "ws-victim"), secret, false); err != nil {
		t.Fatalf("owner denied its own file: %v", err)
	}

	attacker := inWorkspace(context.Background(), "ws-attacker")
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"read_file", map[string]any{"path": secret}},
		{"list_dir", map[string]any{"path": victimRoots[0]}},
		{"find_files", map[string]any{"path": victimRoots[0]}},
		{"write_file", map[string]any{"path": filepath.Join(victimRoots[0], "planted.sh"), "content": "no"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := systemTool(t, e, tc.name)
			if _, err := tool.Handler(attacker, tc.args); err == nil {
				t.Fatalf("%s reached another workspace's tree", tc.name)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(victimRoots[0], "planted.sh")); !os.IsNotExist(err) {
		t.Fatalf("cross-workspace write landed: %v", err)
	}
}

func TestPersonalWorkspaceCannotReachNamespacedTenantTrees(t *testing.T) {
	// The personal workspace's root physically CONTAINS every tenant tree, so
	// plain containment would let the no-principal fallback — the scheduler
	// and channel paths — read all of them. denyNamespaceEscape is what stops
	// it; delete that call and this test fails.
	root := isolationRoot(t)
	e := newMinimalEngine(t)
	if err := e.SetFilesystemRoots([]string{root}); err != nil {
		t.Fatal(err)
	}
	tenantRoots, err := e.WorkspaceFilesystemRoots("ws-tenant")
	if err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(tenantRoots[0], "secret.txt")
	if err := os.WriteFile(secret, []byte("tenant-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !pathWithinRoot(secret, root) {
		t.Fatalf("precondition failed: tenant tree is not under the personal root")
	}

	_, err = e.resolveFilesystemPath(context.Background(), secret, false)
	if err == nil {
		t.Fatal("the personal workspace read a tenant's file")
	}
	if !strings.Contains(err.Error(), "belongs to another workspace") {
		t.Fatalf("denied for the wrong reason: %v", err)
	}
}

func TestRelativePathsLandInTheCallersOwnWorkspace(t *testing.T) {
	// A relative path is joined to roots[0]. When that was the platform root,
	// every tenant's "notes.txt" was the SAME file.
	root := isolationRoot(t)
	e := newMinimalEngine(t)
	if err := e.SetFilesystemRoots([]string{root}); err != nil {
		t.Fatal(err)
	}
	a, err := e.resolveFilesystemPath(inWorkspace(context.Background(), "ws-alpha"), "notes.txt", true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.resolveFilesystemPath(inWorkspace(context.Background(), "ws-beta"), "notes.txt", true)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("two workspaces resolved the same relative path to one file: %s", a)
	}
}

func TestPrivilegedScratchDirectoriesAreDisjointAcrossWorkspaces(t *testing.T) {
	base := isolationRoot(t)
	e := newMinimalEngine(t)
	if err := e.SetFilesystemRoots([]string{base}); err != nil {
		t.Fatal(err)
	}
	e.SetPrivilegedWorkDir(base)

	personal := e.workspaceScratchDir(wsroot.PersonalWorkspaceID)
	if personal != filepath.Clean(base) {
		t.Fatalf("personal scratch moved: %s want %s", personal, base)
	}
	a := e.workspaceScratchDir("ws-alpha")
	b := e.workspaceScratchDir("ws-beta")
	if a == "" || b == "" {
		t.Fatalf("scratch directory unavailable: %q %q", a, b)
	}
	if a == b {
		t.Fatalf("two workspaces share a privileged scratch directory: %s", a)
	}
	for _, dir := range []string{a, b} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("scratch directory %s is mode %o, want 700", dir, perm)
		}
	}
}

func TestDockerRunnerRefusesAWorkspaceOutsideItsConfiguredTree(t *testing.T) {
	// req.Workspace may only NARROW the runner's mount. Without the check a
	// caller could name any host path and have it bind-mounted rw.
	outer := isolationRoot(t)
	elsewhere := isolationRoot(t)
	r := DockerPrivilegedRunner{Workspace: outer, Binary: "/nonexistent-docker"}
	ctx := context.WithValue(context.Background(), isolationReadyKey{}, true)
	_, err := r.Run(ctx, PrivilegedCommand{Argv: []string{"true"}, Workspace: elsewhere})
	if err == nil || !strings.Contains(err.Error(), "outside the configured workspace") {
		t.Fatalf("runner accepted a widened mount: %v", err)
	}
}

func TestPrivilegedCommandsCarryTheRunsWorkspaceWithoutTheBuiltinSayingSo(t *testing.T) {
	// The stamping lives in runPrivilegedCommand, so a builtin that forgets
	// the field still gets scoped. Assert the choke point, not the call sites.
	base := isolationRoot(t)
	e := newMinimalEngine(t)
	if err := e.SetFilesystemRoots([]string{base}); err != nil {
		t.Fatal(err)
	}
	e.SetPrivilegedWorkDir(base)
	rec := &recordingPrivilegedRunner{}
	e.SetPrivilegedCommandRunner(rec)

	if _, err := e.runPrivilegedCommand(inWorkspace(context.Background(), "ws-alpha"), PrivilegedCommand{Argv: []string{"true"}}, 0); err != nil {
		t.Fatal(err)
	}
	want := e.workspaceScratchDir("ws-alpha")
	if rec.last.Workspace != want {
		t.Fatalf("choke point did not stamp the workspace: got %q want %q", rec.last.Workspace, want)
	}
	if rec.last.Workspace == filepath.Clean(base) {
		t.Fatal("a named workspace was handed the platform-wide scratch tree")
	}
}

type recordingPrivilegedRunner struct{ last PrivilegedCommand }

func (*recordingPrivilegedRunner) Mode() string { return "docker" }
func (r *recordingPrivilegedRunner) Run(_ context.Context, req PrivilegedCommand) (string, error) {
	r.last = req
	return "", nil
}

func TestNamedWorkspaceWithNoScratchDirectoryRefusesRatherThanSharing(t *testing.T) {
	// A scratch root that cannot hold a namespaced subdirectory (here: a
	// regular file where the directory must go) must produce a refusal, not a
	// silent fall-through to the shared tree the runner is configured with.
	base := isolationRoot(t)
	if err := os.WriteFile(filepath.Join(base, wsroot.NamespaceDir), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := &recordingPrivilegedRunner{}
	e := newMinimalEngine(t)
	if err := e.SetFilesystemRoots([]string{base}); err != nil {
		t.Fatal(err)
	}
	e.SetPrivilegedWorkDir(base)
	e.SetPrivilegedCommandRunner(rec)

	_, err := e.runPrivilegedCommand(inWorkspace(context.Background(), "ws-alpha"), PrivilegedCommand{Argv: []string{"true"}}, 0)
	if err == nil || !strings.Contains(err.Error(), "no isolated scratch directory") {
		t.Fatalf("expected refusal, got %v", err)
	}
	if rec.last.Argv != nil {
		t.Fatal("the command ran despite having no isolated scratch directory")
	}

	// The personal workspace is unaffected: base itself is its scratch tree.
	if _, err := e.runPrivilegedCommand(context.Background(), PrivilegedCommand{Argv: []string{"true"}}, 0); err != nil {
		t.Fatalf("personal execution regressed: %v", err)
	}
}

func TestAWorkspaceCanRunTheScriptItJustWrote(t *testing.T) {
	// Coherence, not merely isolation: the tree a workspace's write_file
	// lands in must be the tree its privileged commands are mounted on.
	// When scratch space was a namespace BESIDE the workspace root, every
	// script a tenant wrote resolved fine and then failed to execute, because
	// the container runner refuses a working directory outside its mount.
	root := isolationRoot(t)
	e := newMinimalEngine(t)
	if err := e.SetFilesystemRoots([]string{root}); err != nil {
		t.Fatal(err)
	}
	e.SetPrivilegedWorkDir(root)
	rec := &recordingPrivilegedRunner{}
	e.SetPrivilegedCommandRunner(rec)

	ctx := inWorkspace(context.Background(), "ws-alpha")
	script, err := e.resolveFilesystemPath(ctx, "job.sh", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("echo hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := systemTool(t, e, "run_script").Handler(ctx, map[string]any{"path": script}); err != nil {
		t.Fatalf("run_script rejected the workspace's own script: %v", err)
	}
	if !pathWithinRoot(rec.last.WorkingDir, rec.last.Workspace) {
		t.Fatalf("working directory %s is outside the mount %s", rec.last.WorkingDir, rec.last.Workspace)
	}

	// And the mount the runner would receive is still contained by the
	// operator-configured bound, so narrowing — not widening — is what
	// happened.
	r := DockerPrivilegedRunner{Workspace: root, Root: root, Binary: "/nonexistent-docker"}
	isolationReady := context.WithValue(context.Background(), isolationReadyKey{}, true)
	_, err = r.Run(isolationReady, rec.last)
	if err == nil || strings.Contains(err.Error(), "outside") {
		t.Fatalf("mount was rejected by the configured bound: %v", err)
	}
}
