// Package wsroot places one workspace's files on disk.
//
// Soulacy's file-backed stores each grew their own path helper, which is how a
// tenant boundary ends up enforced in a dozen places with a dozen slightly
// different rules. This package is the single rule: the implicit personal
// workspace keeps the exact path it has always had, and every other workspace
// is namespaced beneath it.
//
// Keeping the personal layout untouched is not a convenience — it is product
// invariant 7. An existing single-user installation must not notice that the
// storage layer became tenant-aware, so nothing on its disk moves.
//
// Deriving a store's location from the workspace, rather than storing the
// workspace inside the file, is what makes isolation structural: a file cannot
// declare itself into a tenant it is not filed under.
package wsroot

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// PersonalWorkspaceID is the implicit workspace of every Personal deployment.
// It matches the tenancy bootstrap ID so the two agree about what "personal"
// means without either importing the other.
const PersonalWorkspaceID = "ws_personal"

// NamespaceDir holds every non-personal workspace. The leading dot keeps it
// out of the way of directory listings that expect to see user content.
const NamespaceDir = ".workspaces"

// idPattern is deliberately narrow: these IDs become path segments, so the set
// of legal characters is the set that is unambiguous in a filename on every
// supported platform.
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Normalize maps an absent workspace to the implicit personal one, so a caller
// that has no tenant context keeps the behaviour it had before tenants existed.
func Normalize(workspaceID string) string {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return PersonalWorkspaceID
	}
	return workspaceID
}

// Validate rejects IDs that cannot safely become a path segment. "." and ".."
// are excluded explicitly: both match the character class, and both traverse.
func Validate(workspaceID string) error {
	if workspaceID == "." || workspaceID == ".." {
		return fmt.Errorf("workspace ID %q is not a usable directory name", workspaceID)
	}
	if !idPattern.MatchString(workspaceID) {
		return fmt.Errorf("workspace ID %q is not allowed: use 1-64 characters of a-z, 0-9, '.', '_' or '-'", workspaceID)
	}
	return nil
}

// Dir returns the directory holding one workspace's copy of a store rooted at
// base. The personal workspace is base itself.
//
// An unusable ID resolves to the personal root rather than to a crafted path.
// That is the safe failure: it can conflate a caller with personal state,
// which callers must prevent by validating first, but it can never write
// outside the root.
func Dir(base, workspaceID string) string {
	workspaceID = Normalize(workspaceID)
	if workspaceID == PersonalWorkspaceID || Validate(workspaceID) != nil {
		return base
	}
	return filepath.Join(base, NamespaceDir, workspaceID)
}

// File namespaces a single file path by workspace. Personal keeps the original
// path; every other workspace gets the same filename inside its own directory,
// so `<root>/studio-macros.json` becomes
// `<root>/.workspaces/<id>/studio-macros.json`.
func File(path, workspaceID string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	workspaceID = Normalize(workspaceID)
	if workspaceID == PersonalWorkspaceID || Validate(workspaceID) != nil {
		return path
	}
	return filepath.Join(filepath.Dir(path), NamespaceDir, workspaceID, filepath.Base(path))
}

// Of reports which workspace owns a path beneath base, and whether the path is
// a legitimate location at all.
//
// ok=false means the path escapes base, or sits in the namespace root owned by
// no one. Both must be ignored rather than guessed into a workspace: guessing
// is how a misconfigured or symlinked root silently injects state into a
// tenant.
func Of(base, path string) (string, bool) {
	relative, err := filepath.Rel(base, path)
	if err != nil {
		return "", false
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) == 0 || parts[0] == ".." {
		return "", false
	}
	if parts[0] != NamespaceDir {
		return PersonalWorkspaceID, true
	}
	if len(parts) < 3 || Validate(parts[1]) != nil {
		return "", false
	}
	return parts[1], true
}

// UserDir places state that is private to one user inside one workspace.
//
// The subject is hashed rather than used directly: an OIDC subject or an email
// address is not a safe path segment, and hashing keeps the identifier out of
// directory listings and backups while staying stable across restarts.
//
// A personal workspace with no distinct subject keeps the plain base path, so
// an existing installation's user-private state does not move.
func UserDir(base, workspaceID, subject string) string {
	dir := Dir(base, workspaceID)
	segment := UserSegment(subject)
	if segment == "" {
		return dir
	}
	return filepath.Join(dir, segment)
}

// personalSubjects are the well-known local identities a Personal deployment
// uses before real accounts exist. They map to the un-namespaced path so that
// upgrading does not orphan existing files.
var personalSubjects = map[string]bool{"": true, "local-owner": true, "admin": true, "api-key": true}

// UserSegment returns the directory segment for a subject, or "" when the
// subject is one of Personal's implicit local identities.
func UserSegment(subject string) string {
	subject = strings.TrimSpace(subject)
	if personalSubjects[strings.ToLower(subject)] {
		return ""
	}
	sum := sha256.Sum256([]byte(subject))
	return "u_" + hex.EncodeToString(sum[:6])
}
