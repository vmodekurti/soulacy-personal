package workspacepurge

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/soulacy/soulacy/internal/wsroot"
)

// TreePurger removes one workspace's directory tree beneath a file-backed
// root.
//
// THE PERSONAL WORKSPACE IS REFUSED, unconditionally and without a way to opt
// in. `wsroot.Dir` resolves the personal workspace — and, deliberately, any
// UNUSABLE workspace id — to the base directory itself, because that is the
// safe failure for a read: it can conflate a caller with personal state, but
// it can never write outside the root. Under a DELETE that same resolution is
// the opposite of safe: it turns "remove this tenant's tree" into "remove
// every tenant's tree, and the installation's own data with it". The one place
// that rule flips is here, so the refusal is here.
//
// A personal deployment deleting its only workspace is not a case this
// function needs to serve; that is uninstalling, and it is done by removing
// the data directory, not by an API call.
func TreePurger(resource, base string) Purger {
	return Purger{
		Resource: resource,
		Purge: func(ctx context.Context, workspaceID string) (Removed, error) {
			return PurgeTree(ctx, base, workspaceID)
		},
	}
}

// PurgeSubtree removes a named directory inside one workspace's tree.
//
// For stores that place their data at `wsroot.Dir(base, id)/<sub>` rather than
// owning the workspace directory outright — which is most of them, since each
// store applies wsroot to its own base.
func PurgeSubtree(ctx context.Context, base, workspaceID, sub string) (Removed, error) {
	return purgeTree(ctx, base, workspaceID, sub)
}

// PurgeTree removes one workspace's subtree and reports what it freed.
func PurgeTree(ctx context.Context, base, workspaceID string) (Removed, error) {
	return purgeTree(ctx, base, workspaceID, "")
}

func purgeTree(ctx context.Context, base, workspaceID, sub string) (Removed, error) {
	normalized := wsroot.Normalize(workspaceID)
	dir := wsroot.Dir(base, normalized)

	// The shared-root check is on the WORKSPACE directory, before `sub` is
	// appended. Checking the final path instead would let a personal workspace
	// through whenever sub is non-empty — `<base>/exports` is not `<base>`, so
	// the comparison passes, and the removal takes every workspace's exports.
	// ONE check, not three.
	//
	// The obvious way to write this is a personal-workspace check, then a
	// Validate check, then a base-directory backstop. Mutation testing shows
	// that shape is a lie: each of the first two can be deleted with every
	// test still passing, because the backstop catches what they catch. Three
	// checks that look like defence in depth and are really one check with two
	// decorations is worse than one honest check — the next reader trusts a
	// redundancy that is not there.
	//
	// So the condition is the single fact that matters (did this resolve to
	// the shared root?) and the BRANCHES exist only to say why, because the
	// three causes send an operator to three different places.
	if dir == base || dir == "" {
		switch {
		case normalized == wsroot.PersonalWorkspaceID:
			return Removed{}, fmt.Errorf(
				"refusing to purge the personal workspace's tree: wsroot resolves it to the base "+
					"directory %q, so this would delete every workspace's files. Uninstalling a "+
					"personal deployment means removing its data directory, not calling this", base)
		case wsroot.Validate(normalized) != nil:
			return Removed{}, fmt.Errorf(
				"refusing to purge tree for workspace id %q: it is not a usable directory name, and "+
					"wsroot resolves an unusable id to the base directory %q", workspaceID, base)
		default:
			return Removed{}, fmt.Errorf(
				"refusing to purge %q: it resolves to the shared base directory", dir)
		}
	}

	if sub != "" {
		dir = filepath.Join(dir, sub)
	}

	var files int64
	var bytes int64
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		files++
		if info, statErr := entry.Info(); statErr == nil {
			bytes += info.Size()
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return Removed{}, err
	}
	if err := os.RemoveAll(dir); err != nil {
		return Removed{}, err
	}
	// A tree that was already absent is a purge that succeeded: the obligation
	// is that the data is gone, not that this call is what removed it.
	return Removed{Rows: files, Bytes: bytes, Note: "removed " + dir}, nil
}
