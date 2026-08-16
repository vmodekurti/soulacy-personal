package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// runscratch.go — MU-021 criteria 2 and 5: "each run has ... bounded writable
// scratch space" and "secret material ... removed when the run ends."
//
// Per-WORKSPACE confinement (workspace_roots.go) stops one tenant reaching
// another's files. It says nothing about one run reaching another's, and
// within a workspace that still matters: a run writes a temporary file holding
// a decrypted secret, an intermediate result, a downloaded artifact, and by
// default leaves it in the tree where the next run — possibly triggered by a
// different member — will find it.
//
// Two things fix that together and neither is sufficient alone. A run gets its
// own directory to work in, and something deletes the directory when the run
// ends. A per-run directory nobody cleans up is just a slower version of the
// same leak, with more of them.
//
// WHY SCRATCH IS INSIDE THE WORKSPACE TREE, NOT BESIDE IT. The container mount
// is the workspace's own root (see workspaceScratchDir, and the design note
// there about run_script). Putting run scratch outside that tree would put the
// working directory outside the mount and break every privileged command; the
// nesting is what lets the run's default working directory be BOTH private to
// the run and reachable from the tools that write to it.
//
// WHAT THIS IS NOT. It is not a boundary between runs of the same workspace: a
// run that names another run's scratch path by hand still resolves it, because
// they are the same tenant and the filesystem policy is a tenant boundary.
// It is a default location and a lifetime — enough that a run has to go out of
// its way to leave something behind, and that what it leaves is deleted.

// RunScratchDirName is the namespace directory holding per-run scratch space.
// A dot prefix keeps it out of an agent's ordinary listing of its workspace.
const RunScratchDirName = ".runs"

type runIDContextKey struct{}

// WithRunID marks a context as belonging to a durable run.
//
// Carried on the context rather than passed down because the two places that
// need it — the scratch directory resolver and the side-effect recorder — sit
// several layers below anything that knows a run exists, and every other
// caller of that same code (chat, schedules, channels) legitimately has no run
// at all.
func WithRunID(ctx context.Context, runID string) context.Context {
	if strings.TrimSpace(runID) == "" {
		return ctx
	}
	return context.WithValue(ctx, runIDContextKey{}, strings.TrimSpace(runID))
}

// RunIDFromContext returns the durable run a call belongs to, or "".
func RunIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(runIDContextKey{}).(string)
	return id
}

// runScratchDir returns the working directory a privileged command should
// start in: the run's own scratch directory when there is a run, and the
// workspace tree otherwise.
//
// Falls back to the workspace tree rather than failing when the scratch
// directory cannot be created. The workspace tree is already the correct
// tenant boundary — the fallback loses per-run cleanliness, not isolation —
// and refusing to execute because a mkdir failed would turn a full disk into
// a total outage.
func (e *Engine) runScratchDir(ctx context.Context) string {
	workspaceTree := e.workspaceScratchDir(WorkspaceFromContext(ctx))
	runID := RunIDFromContext(ctx)
	if workspaceTree == "" || runID == "" {
		return workspaceTree
	}
	dir, err := RunScratchPath(workspaceTree, runID)
	if err != nil {
		return workspaceTree
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return workspaceTree
	}
	_ = os.Chmod(dir, 0o700)
	return dir
}

// RunScratchPath returns where one run's scratch directory lives under a
// workspace tree, refusing an ID that would escape it.
//
// The run ID reaches this from a durable record whose ID the gateway
// generates, so a traversal attempt is not the expected case — but "the caller
// upstream is careful" is the kind of reasoning that stops being true after a
// refactor, and joining an unchecked string into a path is how a cleanup
// routine ends up deleting the workspace.
func RunScratchPath(workspaceTree, runID string) (string, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return "", fmt.Errorf("run scratch: a run id is required")
	}
	if runID != filepath.Base(runID) || runID == "." || runID == ".." ||
		strings.ContainsAny(runID, `/\`) {
		return "", fmt.Errorf("run scratch: %q is not a usable directory name", runID)
	}
	return filepath.Join(workspaceTree, RunScratchDirName, runID), nil
}

// RemoveRunScratch deletes one run's scratch directory.
//
// Called when the run ends, whatever the outcome — a failed or cancelled run
// is exactly as likely to have written a decrypted secret to disk as a
// successful one, and rather more likely to have written a half-finished
// something nobody will ever look at.
func (e *Engine) RemoveRunScratch(ctx context.Context, workspaceID, runID string) error {
	workspaceTree := e.workspaceScratchDir(workspaceID)
	if workspaceTree == "" {
		return nil
	}
	dir, err := RunScratchPath(workspaceTree, runID)
	if err != nil {
		return err
	}
	// Resolve before removing. A symlink planted at the scratch path by the
	// run itself would otherwise make RemoveAll follow it out of the tree —
	// the cleanup turning into the escape.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !pathWithinRoot(resolved, workspaceTree) {
		return fmt.Errorf("run scratch: %s resolves outside its workspace; refusing to remove", dir)
	}
	return os.RemoveAll(resolved)
}
