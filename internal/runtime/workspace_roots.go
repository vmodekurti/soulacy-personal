package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/soulacy/soulacy/internal/wsroot"
)

// workspace_roots.go — MU-021. Per-workspace confinement for every host
// filesystem builtin and every privileged subprocess.
//
// THE HOLE THIS CLOSES. SetFilesystemRoots configures ONE process-global
// allowlist, and defaultPrivilegedWorkDir returns ONE process-global scratch
// directory. Every tenant's runs shared both. read_file in workspace A could
// read a file write_file created in workspace B by naming its absolute path,
// and shell_exec in A ran in the same container mount B's last command wrote
// to. The engine was already careful to scope brain memory, learning
// proposals, checkpoints and runs per workspace; the filesystem — the one
// store an agent can address by raw string — was not scoped at all.
//
// STRUCTURAL, NOT FILTERED. The fix is not a check bolted beside the existing
// containment test: it is a different root. A run in workspace A resolves
// against roots derived from A's ID, so a path outside them fails the
// containment test that was always there. There is no scoping predicate a
// future caller can forget, because there is no unscoped root left to pass.
//
// INVARIANT 7 — personal deployments must not notice. wsroot.Dir maps the
// personal workspace to the configured root itself, so a single-tenant
// install's roots are byte-identical to what it configured and existing files
// stay exactly where they are. Nothing is migrated, nothing moves.
//
// THE NAMESPACE HOLE INVARIANT 7 OPENS, AND ITS CLOSURE. Because personal
// resolves to the base root, and every named workspace lives UNDER that base
// root at <base>/.workspaces/<id>, the personal workspace structurally
// contains every tenant's tree. Left alone, a run with no principal — the
// scheduler and channel paths that fall back to personal — could read any
// tenant's files. denyNamespaceEscape rejects any resolved path whose first
// element below the matched root is the namespace directory. In a real
// personal deployment no such directory exists, so the rule never fires and
// invariant 7 holds; in a multi-tenant one it is the difference between the
// service paths being confined and being universal.

// workspaceRoots returns the canonical host trees a run acting in workspaceID
// may touch. The result is memoized: resolution creates directories and calls
// EvalSymlinks, and every filesystem builtin call would otherwise repeat both.
//
// Failure is returned, never swallowed into "use the shared root". A workspace
// whose tree cannot be created has no filesystem access at all, which is the
// fail-closed answer — the alternative is serving it the base root, which is
// precisely the cross-tenant read this function exists to prevent.
func (e *Engine) workspaceRoots(workspaceID string) ([]string, error) {
	if len(e.filesystemRoots) == 0 {
		return nil, fmt.Errorf("no workspace roots configured")
	}
	workspaceID = wsroot.Normalize(workspaceID)
	if workspaceID == wsroot.PersonalWorkspaceID {
		return e.filesystemRoots, nil
	}
	if err := wsroot.Validate(workspaceID); err != nil {
		return nil, err
	}

	e.workspaceRootsMu.Lock()
	defer e.workspaceRootsMu.Unlock()
	if cached, ok := e.workspaceRootsCache[workspaceID]; ok {
		return cached, nil
	}

	derived := make([]string, 0, len(e.filesystemRoots))
	for _, base := range e.filesystemRoots {
		dir := wsroot.Dir(base, workspaceID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("workspace %q filesystem root: %w", workspaceID, err)
		}
		// 0700 is re-applied because MkdirAll leaves an existing directory's
		// mode alone: a tree created before this story (or by an operator's
		// umask) would otherwise stay group- or world-readable.
		_ = os.Chmod(dir, 0o700)
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return nil, fmt.Errorf("workspace %q filesystem root: %w", workspaceID, err)
		}
		derived = append(derived, filepath.Clean(resolved))
	}
	if e.workspaceRootsCache == nil {
		e.workspaceRootsCache = make(map[string][]string)
	}
	e.workspaceRootsCache[workspaceID] = derived
	return derived, nil
}

// WorkspaceFilesystemRoots exposes one workspace's confinement set for
// diagnostics and for tests that must assert two workspaces do not overlap.
func (e *Engine) WorkspaceFilesystemRoots(workspaceID string) ([]string, error) {
	roots, err := e.workspaceRoots(workspaceID)
	if err != nil {
		return nil, err
	}
	return append([]string(nil), roots...), nil
}

// denyNamespaceEscape reports whether a path that is already inside root sits
// in the per-workspace namespace directory — i.e. belongs to some OTHER
// workspace that merely happens to live under this root on disk.
//
// Containment alone cannot answer this: <base>/.workspaces/tenant-b/x IS
// under <base>, which is exactly the personal workspace's root.
func denyNamespaceEscape(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return true
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == wsroot.NamespaceDir {
			return true
		}
	}
	return false
}

// workspaceScratchDir returns the host tree a privileged subprocess for this
// workspace starts in — and, under the container runner, the ONLY tree it can
// see. Two tenants sharing one would share a writable filesystem: a covert
// channel between runs and, for any tool that leaves a file behind, a plain
// disclosure.
//
// Personal keeps the configured scratch directory verbatim (invariant 7: the
// mount a single-tenant install has always had). A named workspace gets its
// own filesystem root — the SAME tree its read_file and write_file resolve
// against, not a separate namespace beside it.
//
// Why the same tree and not a sibling scratch namespace: run_script resolves
// its script through the filesystem policy and then runs it with the script's
// directory as the working directory. If the tenant's files and the tenant's
// mount were different trees, every script the tenant wrote would resolve
// successfully and then fail to execute, because the working directory would
// sit outside the mount. Unifying them is what makes write-then-run work at
// all inside a workspace, and it costs nothing in isolation: the tenant's root
// is already disjoint from every other tenant's.
func (e *Engine) workspaceScratchDir(workspaceID string) string {
	workspaceID = wsroot.Normalize(workspaceID)
	if workspaceID == wsroot.PersonalWorkspaceID || wsroot.Validate(workspaceID) != nil {
		base := strings.TrimSpace(e.privilegedWorkDir)
		if base == "" && len(e.filesystemRoots) > 0 {
			base = e.filesystemRoots[0]
		}
		return base
	}
	roots, err := e.workspaceRoots(workspaceID)
	if err != nil || len(roots) == 0 {
		// A tree that cannot be established must not silently become the
		// shared base — that is exactly the cross-tenant mount. Empty makes
		// runPrivilegedCommand refuse.
		return ""
	}
	return roots[0]
}
