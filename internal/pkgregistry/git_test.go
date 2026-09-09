package pkgregistry

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	sdkpkg "github.com/soulacy/soulacy/sdk/pkgregistry"
)

func TestGitSourceForGitHubTreeURL(t *testing.T) {
	source, ref, subdir, ok := gitSourceFor("https://github.com/alirezarezvani/claude-skills/tree/main/c-level-advisor")
	if !ok {
		t.Fatal("GitHub tree URL was not recognized")
	}
	if source != "https://github.com/alirezarezvani/claude-skills.git" {
		t.Fatalf("source = %q", source)
	}
	if ref != "main" || subdir != "c-level-advisor" {
		t.Fatalf("ref/subdir = %q/%q", ref, subdir)
	}
}

func TestGitProviderFetchesSelectedSubdirectory(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	if err := os.MkdirAll(filepath.Join(repo, "skills", "advisor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "skills", "advisor", "SKILL.md"), []byte("# Advisor\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("not part of skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "fixture")

	dst := filepath.Join(t.TempDir(), "installed")
	p := &gitProvider{id: "github"}
	err := p.Fetch(context.Background(), sdkpkg.Package{
		Slug:         "advisor",
		Source:       repo,
		SourceRef:    "main",
		SourceSubdir: filepath.Join("skills", "advisor"),
	}, dst)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "README.md")); !os.IsNotExist(err) {
		t.Fatalf("repository root leaked into selected package: %v", err)
	}
}

func TestGitProviderRejectsEscapingSubdirectory(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "installed")
	p := &gitProvider{id: "github"}
	err := p.Fetch(context.Background(), sdkpkg.Package{
		Slug:         "bad",
		Source:       t.TempDir(),
		SourceSubdir: "../outside",
	}, dst)
	if err == nil {
		t.Fatal("expected traversal to be rejected")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
