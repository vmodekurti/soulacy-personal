// rulesstore.go — versioned, auditable storage for the SOUL.yaml authoring
// rulebook (ST-09).
//
// Why this is a security control and not a nicety: the rules text is injected
// verbatim into EVERY generation prompt and EVERY AI fix (see RulesPromptBlock
// and the builder/fixer call sites). Whoever controls the rulebook controls
// what Studio writes into every agent it builds afterwards — tool choices,
// argument shapes, delivery behaviour. The previous implementation was a bare
// os.WriteFile over a single file: an edit left no trace, had no author, and
// could not be diffed or reverted. A rulebook silently altered between two
// builds is an unauditable supply-chain gap, and "the agent validated cleanly"
// says nothing when nobody can tell WHICH rulebook it validated against.
//
// So every save appends a new immutable version — number, content hash (the
// same RulesVersion content hash a deployment pins), timestamp, author and the
// full text — and history is never mutated: version files are created with
// O_EXCL and never rewritten, so a corrupted or malicious re-save cannot
// overwrite the record of what was in force before.
//
// Storage follows the existing library.go convention exactly: one JSON file per
// record under a caller-supplied root directory, written temp-then-rename, with
// no dependency on the gateway or config, so it is fully unit-testable with
// t.TempDir().
package studio

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RulesRecord is one immutable rulebook version as stored on disk.
type RulesRecord struct {
	// Version is a monotonically increasing counter, starting at 1.
	Version int `json:"version"`
	// Hash is RulesVersion(Rules) — the content identity a deployment pins, so
	// a stored version and a running agent can be compared without diffing text.
	Hash string `json:"hash"`
	// Saved is the RFC3339 UTC timestamp of the save.
	Saved string `json:"saved"`
	// Author is who saved it. "unknown" when the caller supplies nothing —
	// recorded as an explicit unknown rather than an empty field, because a
	// blank author is indistinguishable from a lost one.
	Author string `json:"author"`
	// Note is an optional free-text reason for the change.
	Note string `json:"note,omitempty"`
	// Rules is the FULL text. Stored in full, not as a diff against the
	// previous version: a chain of diffs is only as trustworthy as its oldest
	// link, and the whole point is to be able to reconstruct exactly what was
	// injected into a given build.
	Rules string `json:"rules"`
}

// RulesMeta is the listing shape — everything except the (potentially large)
// rules text, so history can be rendered without loading every version.
type RulesMeta struct {
	Version int    `json:"version"`
	Hash    string `json:"hash"`
	Saved   string `json:"saved"`
	Author  string `json:"author"`
	Note    string `json:"note,omitempty"`
	// Bytes is the size of the rules text, so a reviewer can spot a version
	// that suddenly dropped or doubled in size.
	Bytes int `json:"bytes"`
}

// rulesFileExt / rulesFilePrefix name the on-disk version files. Zero-padded so
// a plain directory listing sorts in version order.
const (
	rulesFilePrefix = "v"
	rulesFileExt    = ".json"
)

// SaveRules appends a new rulebook version under root and returns the stored
// record. History is append-only: existing versions are never rewritten.
//
// An empty author is recorded as "unknown" rather than left blank. An empty
// rules text is rejected — "no rules" is a real state, but it is expressed by
// deleting the active rulebook, not by storing an empty audited version that
// later reads as "someone approved having no rules".
//
// A save whose content is identical to the current head does NOT create a new
// version; the existing head is returned unchanged. Version identity is content
// identity (the same contract RulesVersion states), so an idempotent re-save —
// the GUI's "Save" on an untouched editor — must not manufacture audit noise
// that hides the real changes.
func SaveRules(root, rules, author string) (RulesRecord, error) {
	if strings.TrimSpace(root) == "" {
		return RulesRecord{}, fmt.Errorf("studio: rules root is required")
	}
	if strings.TrimSpace(rules) == "" {
		return RulesRecord{}, fmt.Errorf("studio: rules text is required")
	}
	return saveRulesAt(root, rules, author, "", time.Now().UTC())
}

// SaveRulesWithNote is SaveRules plus a change note (why the rules changed),
// which is the part of an audit trail a hash cannot supply.
func SaveRulesWithNote(root, rules, author, note string) (RulesRecord, error) {
	if strings.TrimSpace(root) == "" {
		return RulesRecord{}, fmt.Errorf("studio: rules root is required")
	}
	if strings.TrimSpace(rules) == "" {
		return RulesRecord{}, fmt.Errorf("studio: rules text is required")
	}
	return saveRulesAt(root, rules, author, note, time.Now().UTC())
}

// saveRulesAt is the testable core: the clock is a parameter so history
// ordering can be asserted deterministically.
func saveRulesAt(root, rules, author, note string, now time.Time) (RulesRecord, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return RulesRecord{}, fmt.Errorf("studio: create rules dir: %w", err)
	}
	hash := RulesVersion(rules)

	head, found, err := LatestRules(root)
	if err != nil {
		return RulesRecord{}, err
	}
	if found && head.Hash == hash {
		return head, nil
	}

	rec := RulesRecord{
		Version: 1,
		Hash:    hash,
		Saved:   now.UTC().Format(time.RFC3339),
		Author:  strings.TrimSpace(author),
		Note:    strings.TrimSpace(note),
		Rules:   rules,
	}
	if rec.Author == "" {
		rec.Author = "unknown"
	}
	if found {
		rec.Version = head.Version + 1
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return RulesRecord{}, fmt.Errorf("studio: marshal rules version: %w", err)
	}
	path := rulesPath(root, rec.Version)
	// O_EXCL is the append-only guard: if a file for this version already
	// exists (a racing writer, or a caller replaying a save), we refuse rather
	// than overwrite the historical record.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return RulesRecord{}, fmt.Errorf("studio: rules version %d already exists (history is append-only)", rec.Version)
		}
		return RulesRecord{}, fmt.Errorf("studio: create rules version: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return RulesRecord{}, fmt.Errorf("studio: write rules version: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return RulesRecord{}, fmt.Errorf("studio: close rules version: %w", err)
	}
	return rec, nil
}

// RulesHistory returns the metadata of every stored version, NEWEST FIRST
// (matching ListDrafts' most-recent-first convention). A missing root is not an
// error — an un-edited workspace simply has no history.
func RulesHistory(root string) ([]RulesMeta, error) {
	recs, err := allRulesRecords(root)
	if err != nil {
		return nil, err
	}
	out := make([]RulesMeta, 0, len(recs))
	for _, r := range recs {
		out = append(out, RulesMeta{
			Version: r.Version, Hash: r.Hash, Saved: r.Saved,
			Author: r.Author, Note: r.Note, Bytes: len(r.Rules),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

// RulesAt returns one stored version in full. A missing version is an error —
// silently returning empty rules would let a caller inject "no rules" into a
// build while believing it pinned a specific rulebook.
func RulesAt(root string, version int) (RulesRecord, error) {
	if strings.TrimSpace(root) == "" {
		return RulesRecord{}, fmt.Errorf("studio: rules root is required")
	}
	if version <= 0 {
		return RulesRecord{}, fmt.Errorf("studio: invalid rules version %d", version)
	}
	data, err := os.ReadFile(rulesPath(root, version))
	if err != nil {
		if os.IsNotExist(err) {
			return RulesRecord{}, fmt.Errorf("studio: rules version %d not found", version)
		}
		return RulesRecord{}, fmt.Errorf("studio: read rules version: %w", err)
	}
	var rec RulesRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return RulesRecord{}, fmt.Errorf("studio: parse rules version %d: %w", version, err)
	}
	return rec, nil
}

// LatestRules returns the current head version. found=false (with no error)
// means the workspace has never saved a rulebook, which is the normal state for
// a fresh install running on DefaultSOULRules.
func LatestRules(root string) (RulesRecord, bool, error) {
	recs, err := allRulesRecords(root)
	if err != nil {
		return RulesRecord{}, false, err
	}
	if len(recs) == 0 {
		return RulesRecord{}, false, nil
	}
	head := recs[0]
	for _, r := range recs[1:] {
		if r.Version > head.Version {
			head = r
		}
	}
	return head, true, nil
}

// DiffRules renders the change between two stored versions using the same line
// diff the workflow repair preview uses, so "what changed in the rules" reads
// exactly like "what changed in the workflow". Returns the structured lines,
// the add/remove counts, and the unified-style text.
func DiffRules(root string, from, to int) ([]DiffLine, DiffStats, string, error) {
	a, err := RulesAt(root, from)
	if err != nil {
		return nil, DiffStats{}, "", err
	}
	b, err := RulesAt(root, to)
	if err != nil {
		return nil, DiffStats{}, "", err
	}
	lines, stats, text := DiffYAML(a.Rules, b.Rules)
	return lines, stats, text, nil
}

// allRulesRecords loads every version file under root. A corrupt or unrelated
// file is skipped rather than failing the whole read — the same tolerance
// ListDrafts applies — because losing access to the entire audit history
// because of one bad file is the worse outcome.
func allRulesRecords(root string) ([]RulesRecord, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("studio: rules root is required")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("studio: read rules dir: %w", err)
	}
	var out []RulesRecord
	for _, e := range entries {
		if e.IsDir() || !isRulesVersionFile(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, e.Name()))
		if err != nil {
			continue
		}
		var rec RulesRecord
		if err := json.Unmarshal(data, &rec); err != nil || rec.Version <= 0 {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

// isRulesVersionFile recognises "v000001.json" style version files.
func isRulesVersionFile(name string) bool {
	if !strings.HasPrefix(name, rulesFilePrefix) || !strings.HasSuffix(name, rulesFileExt) {
		return false
	}
	num := strings.TrimSuffix(strings.TrimPrefix(name, rulesFilePrefix), rulesFileExt)
	if num == "" {
		return false
	}
	n, err := strconv.Atoi(num)
	return err == nil && n > 0
}

// rulesPath is the single place a version number becomes a filesystem path.
// The version is an int, so there is no traversal surface to guard here (unlike
// the id-keyed draft store).
func rulesPath(root string, version int) string {
	return filepath.Join(root, fmt.Sprintf("%s%06d%s", rulesFilePrefix, version, rulesFileExt))
}
