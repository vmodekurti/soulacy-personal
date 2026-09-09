package gateway

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegistrySkillCandidatesSingleSkill(t *testing.T) {
	root := t.TempDir()
	writeCandidateFile(t, filepath.Join(root, "SKILL.md"))
	got, err := registrySkillCandidates(root)
	if err != nil || len(got) != 1 || got[0] != root {
		t.Fatalf("registrySkillCandidates() = %v, %v", got, err)
	}
}

func TestRegistrySkillCandidatesCollection(t *testing.T) {
	root := t.TempDir()
	writeCandidateFile(t, filepath.Join(root, "skills", "alpha", "SKILL.md"))
	writeCandidateFile(t, filepath.Join(root, "skills", "beta", "SKILL.md"))
	writeCandidateFile(t, filepath.Join(root, "skills", "ignored", "README.md"))
	got, err := registrySkillCandidates(root)
	if err != nil || len(got) != 2 {
		t.Fatalf("registrySkillCandidates() = %v, %v", got, err)
	}
	if filepath.Base(got[0]) != "alpha" || filepath.Base(got[1]) != "beta" {
		t.Fatalf("unexpected candidates: %v", got)
	}
}

func TestRegistrySkillCandidatesRejectsPackageWithoutSkills(t *testing.T) {
	root := t.TempDir()
	writeCandidateFile(t, filepath.Join(root, "README.md"))
	if _, err := registrySkillCandidates(root); err == nil {
		t.Fatal("expected package without skills to be rejected")
	}
}

func writeCandidateFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
