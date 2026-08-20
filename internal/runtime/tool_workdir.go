package runtime

import (
	"context"
	"fmt"
)

// tool_workdir.go — where a tool subprocess starts.
//
// THE HOLE. Python tools and plugin tools are spawned with exec.CommandContext
// and no cmd.Dir, so they inherit the GATEWAY's working directory — whatever
// the process was started in, one directory for the whole deployment. Every
// relative path a tool opened resolved there. Two tenants running a tool that
// writes `out.csv` wrote one file, and the second read the first's data back
// as its own.
//
// It survived because the filesystem BUILTINS were fixed and the subprocesses
// were not. read_file and write_file resolve through the per-workspace roots
// MU-021 established and have a cross-tenant test to prove it. A Python tool
// calling open() goes nowhere near that policy — it is a different process
// with the OS's own path resolution — so the story that scoped the filesystem
// left the largest filesystem consumer in the deployment unscoped, and the
// isolation suite passed because it exercised the builtins.
//
// SAME TREE AS THE BUILTINS, not a sibling. A tool that writes a file the
// agent then cannot read with read_file is a tool that appears broken; the
// two must resolve the same relative path to the same file, which is what
// workspaceScratchDir already guarantees for privileged subprocesses.
//
// INVARIANT 7. For the personal workspace this resolves to the configured
// filesystem root — the same directory read_file and write_file have always
// used. That IS a change from the gateway's CWD, and it is the right one: the
// old location was wherever systemd or a shell happened to leave the process,
// which nothing documents and nobody chose. After this, a single-user
// install's tools write where its builtins write.

// toolWorkDir returns the directory a tool subprocess for this run must start
// in, or an error if the workspace has no tree.
//
// The error path is the point. An empty directory would mean "inherit", which
// is the shared directory this function exists to eliminate — so a workspace
// whose tree cannot be established runs no tools at all rather than running
// them in everybody's.
func (e *Engine) toolWorkDir(ctx context.Context) (string, error) {
	if e == nil {
		return "", fmt.Errorf("no engine")
	}
	workspaceID := WorkspaceFromContext(ctx)
	// runScratchDir, NOT workspaceScratchDir. MU-016 criterion 5 is about
	// "the RUN's authorized mounts", and the two differ in a way that matters
	// inside a single tenant: two concurrent runs of the same workspace both
	// writing `out.csv` are one file, and the second silently overwrites the
	// first. Per-workspace confinement is a tenant boundary and says nothing
	// about that.
	//
	// It is also what the privileged shell path already used. Having the
	// Python and plugin tool subprocesses resolve to the workspace tree while
	// shell_exec resolved to the run's scratch would mean a run's own tools
	// disagreed about where "here" is — a script written by run_script and
	// executed by shell_exec would be looked for in the wrong directory.
	//
	// The run-less case (chat, schedules, channels) falls through to the
	// workspace tree inside runScratchDir, so nothing that has no run pays
	// for this.
	dir := e.runScratchDir(ctx)
	if dir == "" {
		return "", fmt.Errorf("workspace %q has no filesystem tree, so no tool subprocess may run in it", workspaceID)
	}
	return dir, nil
}
