package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SetFilesystemRoots configures the only host trees filesystem builtins may
// access. Roots are canonicalized once at startup. An invalid set fails closed.
func (e *Engine) SetFilesystemRoots(roots []string) error {
	canonical := make([]string, 0, len(roots))
	seen := make(map[string]struct{}, len(roots))
	for _, raw := range roots {
		raw = strings.TrimSpace(os.ExpandEnv(raw))
		if raw == "" {
			continue
		}
		abs, err := filepath.Abs(raw)
		if err != nil {
			e.filesystemRoots = nil
			return fmt.Errorf("filesystem root %q: %w", raw, err)
		}
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			e.filesystemRoots = nil
			return fmt.Errorf("filesystem root %q: %w", raw, err)
		}
		resolved = filepath.Clean(resolved)
		if _, ok := seen[resolved]; ok {
			continue
		}
		seen[resolved] = struct{}{}
		canonical = append(canonical, resolved)
	}
	e.filesystemRoots = canonical
	if len(canonical) == 0 {
		return fmt.Errorf("at least one existing filesystem root is required")
	}
	return nil
}

// FilesystemRoots returns a defensive copy for diagnostics.
func (e *Engine) FilesystemRoots() []string {
	return append([]string(nil), e.filesystemRoots...)
}

// resolveFilesystemPath is the single policy boundary for host filesystem
// builtins. Existing paths are fully symlink-resolved. For a new write target,
// the nearest existing ancestor is resolved before the missing suffix is
// appended, preventing a symlinked parent from escaping an allowed root.
//
// MU-021: the roots are the RUN'S roots, derived from the workspace on ctx,
// not the process-global platform set. Passing ctx is what makes the boundary
// per-tenant; a caller that cannot supply one has no business resolving a
// tenant path. See workspace_roots.go for why this is a different root rather
// than an extra check.
func (e *Engine) resolveFilesystemPath(ctx context.Context, raw string, forWrite bool) (string, error) {
	roots, err := e.workspaceRoots(WorkspaceFromContext(ctx))
	if err != nil {
		return "", fmt.Errorf("filesystem access denied: %w", err)
	}
	if len(roots) == 0 {
		return "", fmt.Errorf("filesystem access denied: no workspace roots configured")
	}
	raw = strings.TrimSpace(os.ExpandEnv(raw))
	if raw == "" {
		return "", fmt.Errorf("filesystem access denied: path is required")
	}
	if strings.HasPrefix(raw, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			raw = filepath.Join(home, raw[2:])
		}
	}
	if !filepath.IsAbs(raw) {
		raw = filepath.Join(roots[0], raw)
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("filesystem access denied: %w", err)
	}
	resolved, err := resolvePathSymlinks(abs, forWrite)
	if err != nil {
		return "", fmt.Errorf("filesystem access denied for %q: %w", raw, err)
	}
	for _, root := range roots {
		if !pathWithinRoot(resolved, root) {
			continue
		}
		// Containment is necessary but not sufficient. The personal
		// workspace's root physically contains every other workspace's tree,
		// so a path under <root>/.workspaces/<other> passes the test above
		// while belonging to someone else. Deny it by structure rather than
		// by knowing the other tenants' IDs.
		if e.denyWorkspaceNamespace(resolved, root) {
			return "", fmt.Errorf("filesystem access denied for %q: belongs to another workspace", raw)
		}
		return resolved, nil
	}
	return "", fmt.Errorf("filesystem access denied for %q: outside configured workspace roots", raw)
}

func resolvePathSymlinks(path string, allowMissing bool) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return filepath.Clean(resolved), nil
	}
	if !allowMissing || !os.IsNotExist(err) {
		return "", err
	}

	ancestor := filepath.Clean(path)
	var suffix []string
	for {
		if _, statErr := os.Lstat(ancestor); statErr == nil {
			break
		} else if !os.IsNotExist(statErr) {
			return "", statErr
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", err
		}
		suffix = append(suffix, filepath.Base(ancestor))
		ancestor = parent
	}
	ancestor, err = filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", err
	}
	for i := len(suffix) - 1; i >= 0; i-- {
		ancestor = filepath.Join(ancestor, suffix[i])
	}
	return filepath.Clean(ancestor), nil
}

func pathWithinRoot(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
