// library.go — Studio draft library (Story S6.2). A small, PURE, file-backed
// store for user-saved workflow drafts: each draft is one JSON file under a
// caller-supplied root directory, named by its stable id. The store has no
// dependency on the gateway or config — every function takes the root dir as a
// parameter — so it is fully unit-testable with t.TempDir().
//
// On disk a draft is {id, name, workflow, updated}; the id is a slug of the
// name plus a short content hash, so re-saving the same name+workflow lands on
// the same file (idempotent overwrite) while a different workflow under the
// same name gets a distinct id. Listing and loading are cheap directory scans.
package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// StoredDraft is the on-disk (and GET-by-id) shape of a saved draft.
type StoredDraft struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Workflow Draft  `json:"workflow"`
	// Updated is the RFC3339 timestamp of the last save.
	Updated string `json:"updated"`
}

// DraftMeta is the lightweight listing shape: enough to populate a library
// picker without loading every full workflow.
type DraftMeta struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Updated string `json:"updated"`
}

// draftFileExt is the on-disk extension for a stored draft.
const draftFileExt = ".json"

// SaveDraft writes the draft under root as {id,name,workflow,updated} JSON and
// returns its id. The id is slug(name)+"-"+shortHash(workflow); saving the same
// name+workflow again overwrites the same file (idempotent). root is created if
// missing. An empty name is rejected (the slug would be empty).
func SaveDraft(root, name string, workflow Draft) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("studio: drafts root is required")
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("studio: draft name is required")
	}
	base := slug(trimmed)
	if base == "" {
		return "", fmt.Errorf("studio: draft name %q yields an empty id", name)
	}

	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("studio: create drafts dir: %w", err)
	}

	id := base + "-" + shortHash(workflow)
	stored := StoredDraft{
		ID:       id,
		Name:     trimmed,
		Workflow: workflow,
		Updated:  time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return "", fmt.Errorf("studio: marshal draft: %w", err)
	}

	path, err := draftPath(root, id)
	if err != nil {
		return "", err
	}
	// Atomic-ish write: temp file in the same dir, then rename over the target.
	tmp, err := os.CreateTemp(root, base+"-*.tmp")
	if err != nil {
		return "", fmt.Errorf("studio: temp draft: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", fmt.Errorf("studio: write draft: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("studio: close draft: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("studio: persist draft: %w", err)
	}
	return id, nil
}

// ListDrafts returns the metadata of every stored draft under root, sorted by
// Updated descending (most recent first), ties broken by id for determinism. A
// missing root is not an error — it lists as empty.
func ListDrafts(root string) ([]DraftMeta, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("studio: drafts root is required")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return []DraftMeta{}, nil
		}
		return nil, fmt.Errorf("studio: read drafts dir: %w", err)
	}

	out := make([]DraftMeta, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), draftFileExt) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, e.Name()))
		if err != nil {
			continue // tolerate a transient/corrupt file rather than failing the list
		}
		var sd StoredDraft
		if err := json.Unmarshal(data, &sd); err != nil {
			continue
		}
		out = append(out, DraftMeta{ID: sd.ID, Name: sd.Name, Updated: sd.Updated})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Updated != out[j].Updated {
			return out[i].Updated > out[j].Updated // most recent first
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// LoadDraft reads one stored draft by id. A missing draft is an error. The id
// is validated against path traversal before touching the filesystem.
func LoadDraft(root, id string) (StoredDraft, error) {
	path, err := draftPath(root, id)
	if err != nil {
		return StoredDraft{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return StoredDraft{}, fmt.Errorf("studio: draft %q not found", id)
		}
		return StoredDraft{}, fmt.Errorf("studio: read draft: %w", err)
	}
	var sd StoredDraft
	if err := json.Unmarshal(data, &sd); err != nil {
		return StoredDraft{}, fmt.Errorf("studio: parse stored draft %q: %w", id, err)
	}
	return sd, nil
}

// DeleteDraft removes one stored draft by id. Deleting a missing draft returns
// a not-found error. The id is validated against path traversal first.
func DeleteDraft(root, id string) error {
	path, err := draftPath(root, id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("studio: draft %q not found", id)
		}
		return fmt.Errorf("studio: delete draft: %w", err)
	}
	return nil
}

// shortHash returns the first 8 hex chars of the SHA-256 of the canonical JSON
// encoding of a draft. Canonical because json.Marshal of a struct emits fields
// in declaration order deterministically, so identical drafts hash identically.
func shortHash(workflow Draft) string {
	b, err := json.Marshal(workflow)
	if err != nil {
		// A draft that won't marshal is pathological; fall back to a name-only
		// id by hashing the error so we still produce a stable token.
		sum := sha256.Sum256([]byte(err.Error()))
		return hex.EncodeToString(sum[:])[:8]
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:8]
}

// validDraftID reports whether id is a safe, single path segment: non-empty,
// no separators, no "." / ".." traversal, and no NUL. This is the traversal
// guard for the by-id endpoints.
func validDraftID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	if strings.ContainsAny(id, `/\`) || strings.ContainsRune(id, 0) {
		return false
	}
	// Reject anything that isn't a clean basename (defense in depth).
	if filepath.Base(id) != id {
		return false
	}
	return true
}

// draftPath validates id and joins it onto root with the draft extension. It is
// the single chokepoint that guards every filesystem touch against traversal.
func draftPath(root, id string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("studio: drafts root is required")
	}
	if !validDraftID(id) {
		return "", fmt.Errorf("studio: invalid draft id %q", id)
	}
	return filepath.Join(root, id+draftFileExt), nil
}
