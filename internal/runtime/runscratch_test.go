// runscratch_test.go — MU-021 criteria 2 and 5: a run has its own writable
// scratch space, inside its workspace's tree, and it goes away when the run
// ends.
package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func inRun(ctx context.Context, workspaceID, runID string) context.Context {
	return WithRunID(inWorkspace(ctx, workspaceID), runID)
}

func TestEachRunGetsItsOwnScratchDirectoryInsideItsWorkspace(t *testing.T) {
	root := t.TempDir()
	e := newMinimalEngine(t)
	if err := e.SetFilesystemRoots([]string{root}); err != nil {
		t.Fatal(err)
	}

	tree := e.workspaceScratchDir("ws-alpha")
	a := e.runScratchDir(inRun(context.Background(), "ws-alpha", "run_1"))
	b := e.runScratchDir(inRun(context.Background(), "ws-alpha", "run_2"))
	if a == b {
		t.Fatalf("two runs share a scratch directory: %s", a)
	}
	for _, dir := range []string{a, b} {
		// Inside the workspace tree on purpose: the container mount IS that
		// tree, so scratch outside it would put the working directory outside
		// the mount and break every privileged command.
		if !pathWithinRoot(dir, tree) {
			t.Fatalf("run scratch %s is outside the workspace mount %s", dir, tree)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("%s is mode %o, want 700", dir, perm)
		}
	}
}

// A call with no run — chat, a schedule, a channel message — keeps the
// workspace tree. Inventing a scratch directory for them would litter.
func TestACallWithNoRunUsesTheWorkspaceTree(t *testing.T) {
	root := t.TempDir()
	e := newMinimalEngine(t)
	if err := e.SetFilesystemRoots([]string{root}); err != nil {
		t.Fatal(err)
	}
	ctx := inWorkspace(context.Background(), "ws-alpha")
	tree := e.workspaceScratchDir("ws-alpha")
	if got := e.runScratchDir(ctx); got != tree {
		t.Fatalf("runScratchDir = %s, want the workspace tree %s", got, tree)
	}
	if _, err := os.Stat(filepath.Join(tree, RunScratchDirName)); !os.IsNotExist(err) {
		t.Fatalf("a run-less call created the scratch namespace: %v", err)
	}
}

func TestRunScratchIsRemovedWhenTheRunEnds(t *testing.T) {
	root := t.TempDir()
	e := newMinimalEngine(t)
	if err := e.SetFilesystemRoots([]string{root}); err != nil {
		t.Fatal(err)
	}
	ctx := inRun(context.Background(), "ws-alpha", "run_secret")
	dir := e.runScratchDir(ctx)
	secret := filepath.Join(dir, "decrypted.txt")
	if err := os.WriteFile(secret, []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := e.RemoveRunScratch(context.Background(), "ws-alpha", "run_secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(secret); !os.IsNotExist(err) {
		t.Fatalf("the run's scratch file outlived the run: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the run's scratch directory outlived the run: %v", err)
	}
	// The workspace tree itself must survive. Cleanup that takes the tenant's
	// files with it is worse than no cleanup.
	if _, err := os.Stat(e.workspaceScratchDir("ws-alpha")); err != nil {
		t.Fatalf("cleanup removed the workspace tree: %v", err)
	}
	// Removing again is not an error: a crash between the record's outcome
	// and this call must not make the next attempt fail.
	if err := e.RemoveRunScratch(context.Background(), "ws-alpha", "run_secret"); err != nil {
		t.Fatalf("second removal: %v", err)
	}
}

// The run ID reaches this from a generated record today. "The caller upstream
// is careful" stops being true after a refactor, and joining an unchecked
// string into a path is how a cleanup routine deletes a workspace.
func TestARunIDCannotEscapeTheScratchNamespace(t *testing.T) {
	for _, id := range []string{"..", ".", "../escape", "a/b", `a\b`, "", "   "} {
		if _, err := RunScratchPath("/tmp/ws", id); err == nil {
			t.Errorf("run id %q was accepted as a directory name", id)
		}
	}
	got, err := RunScratchPath("/tmp/ws", "run_ok")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join("/tmp/ws", RunScratchDirName, "run_ok") {
		t.Fatalf("path = %s", got)
	}
}

// Cleanup follows the path it is given. A run that replaces its own scratch
// directory with a symlink would turn the cleanup into the escape.
func TestCleanupRefusesToFollowASymlinkOutOfTheWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	victim := filepath.Join(outside, "keepme.txt")
	if err := os.WriteFile(victim, []byte("not the run's"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := newMinimalEngine(t)
	if err := e.SetFilesystemRoots([]string{root}); err != nil {
		t.Fatal(err)
	}
	tree := e.workspaceScratchDir("ws-alpha")
	scratch, err := RunScratchPath(tree, "run_evil")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(scratch), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, scratch); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	err = e.RemoveRunScratch(context.Background(), "ws-alpha", "run_evil")
	if err == nil || !strings.Contains(err.Error(), "outside its workspace") {
		t.Fatalf("cleanup followed the link: %v", err)
	}
	if _, statErr := os.Stat(victim); statErr != nil {
		t.Fatalf("cleanup deleted a file outside the workspace: %v", statErr)
	}
}
