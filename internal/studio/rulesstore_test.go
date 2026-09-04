package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveRules_AppendsAVersionPerChange(t *testing.T) {
	root := t.TempDir()

	v1, err := SaveRules(root, "rule one\n", "alice")
	if err != nil {
		t.Fatalf("SaveRules: %v", err)
	}
	if v1.Version != 1 || v1.Author != "alice" || v1.Hash == "" || v1.Saved == "" {
		t.Fatalf("first version incomplete: %+v", v1)
	}

	v2, err := SaveRules(root, "rule one\nrule two\n", "bob")
	if err != nil {
		t.Fatalf("SaveRules: %v", err)
	}
	if v2.Version != 2 {
		t.Fatalf("expected version 2, got %d", v2.Version)
	}
	if v2.Hash == v1.Hash {
		t.Fatal("different content must hash differently")
	}

	hist, err := RulesHistory(root)
	if err != nil {
		t.Fatalf("RulesHistory: %v", err)
	}
	if len(hist) != 2 || hist[0].Version != 2 || hist[1].Version != 1 {
		t.Fatalf("expected newest-first history of 2, got %+v", hist)
	}
	if hist[0].Author != "bob" || hist[1].Author != "alice" {
		t.Errorf("history lost the author: %+v", hist)
	}
	if hist[1].Bytes != len("rule one\n") {
		t.Errorf("history should record the size, got %d", hist[1].Bytes)
	}
}

// The audit guarantee: an edit may never rewrite what was in force before.
func TestSaveRules_HistoryIsAppendOnly(t *testing.T) {
	root := t.TempDir()
	if _, err := SaveRules(root, "original rules", "alice"); err != nil {
		t.Fatalf("SaveRules: %v", err)
	}
	if _, err := SaveRules(root, "replacement rules", "mallory"); err != nil {
		t.Fatalf("SaveRules: %v", err)
	}

	v1, err := RulesAt(root, 1)
	if err != nil {
		t.Fatalf("RulesAt(1): %v", err)
	}
	if v1.Rules != "original rules" || v1.Author != "alice" {
		t.Fatalf("version 1 was mutated by a later save: %+v", v1)
	}

	// The version files themselves are immutable records: if the slot the next
	// save would take is already occupied, the save must be refused rather than
	// overwrite whatever is there (the O_EXCL guard).
	path := filepath.Join(root, "v000003.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := SaveRules(root, "another change", "carol"); err == nil {
		t.Fatal("expected a refusal rather than an overwrite of an existing version file")
	}
}

func TestRulesAt_RoundTripsAndRejectsMissing(t *testing.T) {
	root := t.TempDir()
	body := "# rules\n- R1. do the thing\n"
	saved, err := SaveRules(root, body, "alice")
	if err != nil {
		t.Fatalf("SaveRules: %v", err)
	}
	got, err := RulesAt(root, saved.Version)
	if err != nil {
		t.Fatalf("RulesAt: %v", err)
	}
	if got.Rules != body {
		t.Fatalf("round trip lost content: %q", got.Rules)
	}
	if got.Hash != RulesVersion(body) {
		t.Fatalf("stored hash %q != RulesVersion %q", got.Hash, RulesVersion(body))
	}
	if _, err := RulesAt(root, 99); err == nil {
		t.Fatal("a missing version must be an error, not empty rules")
	}
}

func TestSaveRules_IdenticalContentDoesNotAppend(t *testing.T) {
	root := t.TempDir()
	if _, err := SaveRules(root, "same text", "alice"); err != nil {
		t.Fatalf("SaveRules: %v", err)
	}
	again, err := SaveRules(root, "same text", "bob")
	if err != nil {
		t.Fatalf("SaveRules: %v", err)
	}
	if again.Version != 1 {
		t.Fatalf("identical content must not create a new version, got %d", again.Version)
	}
	hist, _ := RulesHistory(root)
	if len(hist) != 1 {
		t.Fatalf("expected 1 version, got %d", len(hist))
	}
}

func TestLatestRules_EmptyRootIsNotAnError(t *testing.T) {
	root := filepath.Join(t.TempDir(), "never-written")
	rec, found, err := LatestRules(root)
	if err != nil {
		t.Fatalf("LatestRules on a fresh workspace must not error: %v", err)
	}
	if found || rec.Version != 0 {
		t.Fatalf("expected no head, got %+v", rec)
	}
	if hist, err := RulesHistory(root); err != nil || len(hist) != 0 {
		t.Fatalf("expected empty history, got %v (%v)", hist, err)
	}
}

func TestDiffRules_BetweenVersions(t *testing.T) {
	root := t.TempDir()
	if _, err := SaveRules(root, "line a\nline b\n", "alice"); err != nil {
		t.Fatalf("SaveRules: %v", err)
	}
	if _, err := SaveRules(root, "line a\nline c\n", "bob"); err != nil {
		t.Fatalf("SaveRules: %v", err)
	}
	lines, stats, text, err := DiffRules(root, 1, 2)
	if err != nil {
		t.Fatalf("DiffRules: %v", err)
	}
	if stats.Added != 1 || stats.Removed != 1 {
		t.Fatalf("expected 1 added / 1 removed, got %+v", stats)
	}
	if !strings.Contains(text, "-line b") || !strings.Contains(text, "+line c") {
		t.Fatalf("diff text missing the change: %q", text)
	}
	if len(lines) == 0 {
		t.Fatal("expected structured diff lines")
	}
	if _, _, _, err := DiffRules(root, 1, 7); err == nil {
		t.Fatal("diff against a missing version must error")
	}
}

func TestSaveRules_RejectsEmptyRulesAndRoot(t *testing.T) {
	if _, err := SaveRules("", "x", "alice"); err == nil {
		t.Error("empty root must be rejected")
	}
	if _, err := SaveRules(t.TempDir(), "   \n", "alice"); err == nil {
		t.Error("empty rules must be rejected — 'no rules' is expressed by deleting, not by an audited empty version")
	}
}

func TestSaveRulesWithNote_RecordsWhy(t *testing.T) {
	root := t.TempDir()
	rec, err := SaveRulesWithNote(root, "tightened tool contracts", "alice", "add notebooklm output shape")
	if err != nil {
		t.Fatalf("SaveRulesWithNote: %v", err)
	}
	if rec.Note != "add notebooklm output shape" {
		t.Fatalf("note lost: %+v", rec)
	}
	hist, _ := RulesHistory(root)
	if len(hist) != 1 || hist[0].Note != rec.Note {
		t.Fatalf("history lost the note: %+v", hist)
	}
}

func TestSaveRules_UnknownAuthorIsExplicit(t *testing.T) {
	root := t.TempDir()
	rec, err := SaveRules(root, "some rules", "  ")
	if err != nil {
		t.Fatalf("SaveRules: %v", err)
	}
	if rec.Author != "unknown" {
		t.Fatalf("a blank author must be recorded as an explicit unknown, got %q", rec.Author)
	}
}
