// workspace_test.go — MU-017 criteria 1 and 2 on the install path: an install
// belongs to the workspace that approved it, and a git install pins the exact
// code that was approved.
package plugininstall

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/wsroot"
)

func writeStagePlugin(t *testing.T, dir, id string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "id: " + id + "\nname: " + id + "\nversion: 1.0.0\n"
	if err := os.WriteFile(filepath.Join(dir, "plugin.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

// An approval is an act by one workspace's operator. Under a single shared
// root it would activate the plugin for the whole deployment: the prompt names
// one tenant, the consequence lands on all of them.
func TestAnApprovedPluginLandsOnlyInTheApprovingWorkspace(t *testing.T) {
	base := filepath.Join(t.TempDir(), "plugins")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	all := NewInstallers(base)

	insA := all.For("ws_a")
	insB := all.For("ws_b")
	if insA == nil || insB == nil {
		t.Fatal("installers were not created")
	}

	source := filepath.Join(t.TempDir(), "src")
	writeStagePlugin(t, source, "reporter")

	preview, err := insA.Stage(context.Background(), source, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := insA.Approve(preview.StagedID, source, "", preview.Revision); err != nil {
		t.Fatal(err)
	}

	listA, err := insA.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listA) != 1 || listA[0].ID != "reporter" {
		t.Fatalf("the approving workspace does not have its plugin: %+v", listA)
	}
	listB, err := insB.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listB) != 0 {
		t.Fatalf("one workspace's approval installed into another: %+v", listB)
	}

	// On disk, the same-named plugin from two tenants is two directories.
	if _, err := os.Stat(filepath.Join(all.Root("ws_a"), "reporter")); err != nil {
		t.Fatalf("plugin not in the approving workspace's root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(all.Root("ws_b"), "reporter")); !os.IsNotExist(err) {
		t.Fatalf("plugin appeared in another workspace's root: %v", err)
	}

	// The second workspace may install its own plugin with the same ID
	// without colliding — under a shared root this failed with "already
	// installed", which is a cross-tenant name conflict.
	previewB, err := insB.Stage(context.Background(), source, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := insB.Approve(previewB.StagedID, source, "", previewB.Revision); err != nil {
		t.Fatalf("a second workspace could not install a plugin with the same id: %v", err)
	}
}

// Product invariant 7: personal installs into the root itself.
func TestPersonalInstallsAtTheRoot(t *testing.T) {
	base := filepath.Join(t.TempDir(), "plugins")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	all := NewInstallers(base)
	if got := all.Root(""); got != base {
		t.Fatalf("personal plugin root moved to %q, want %q", got, base)
	}
	if got := all.Root(wsroot.PersonalWorkspaceID); got != base {
		t.Fatalf("explicit personal root = %q", got)
	}
	if all.Root("ws_a") == base {
		t.Fatal("a tenant shares the personal plugin root")
	}
}

// MU-017 criterion 2: the install pins an immutable revision.
//
// A shallow clone of a branch is a moving target — "install from that URL"
// resolves to different code tomorrow, so an approval recorded today attests
// to nothing identifiable. The recorded commit is what makes "what exactly did
// we approve" answerable, and it cannot be edited by whoever controls the
// remote.
func TestAGitInstallRecordsTheExactCommitApproved(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	repo := filepath.Join(t.TempDir(), "repo")
	writeStagePlugin(t, repo, "reporter")
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init", "--quiet", "-b", "main")
	runGit("add", ".")
	runGit("commit", "--quiet", "-m", "first")
	first := runGit("rev-parse", "HEAD")

	base := filepath.Join(t.TempDir(), "plugins")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	ins, err := New(base)
	if err != nil {
		t.Fatal(err)
	}

	preview, err := ins.Stage(context.Background(), repo+"/.git", "")
	if err != nil {
		t.Fatalf("stage from a git source: %v", err)
	}
	if preview.Revision != first {
		t.Fatalf("preview revision = %q, want the tip commit %q", preview.Revision, first)
	}

	if _, err := ins.Approve(preview.StagedID, repo, "", preview.Revision); err != nil {
		t.Fatal(err)
	}
	meta, ok := ReadMeta(filepath.Join(base, "reporter"))
	if !ok {
		t.Fatal("no install metadata was written")
	}
	if meta.Revision != first {
		t.Fatalf("recorded revision = %q, want %q", meta.Revision, first)
	}

	// The branch moves. The record still names the commit that was approved,
	// which is the whole point of pinning.
	if err := os.WriteFile(filepath.Join(repo, "plugin.yaml"),
		[]byte("id: reporter\nname: reporter\nversion: 2.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "--quiet", "-m", "second")
	second := runGit("rev-parse", "HEAD")
	if second == first {
		t.Fatal("test setup did not actually advance the branch")
	}
	meta, _ = ReadMeta(filepath.Join(base, "reporter"))
	if meta.Revision != first {
		t.Fatalf("the recorded revision followed the branch: %q", meta.Revision)
	}
}

// A source that names a revision is fetched at that revision, so an operator
// can approve a specific commit rather than whatever the tip happens to be.
func TestAPinnedSourceInstallsThatExactRevision(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	repo := filepath.Join(t.TempDir(), "repo")
	writeStagePlugin(t, repo, "reporter")
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init", "--quiet", "-b", "main")
	runGit("add", ".")
	runGit("commit", "--quiet", "-m", "first")
	first := runGit("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "plugin.yaml"),
		[]byte("id: reporter\nname: reporter\nversion: 2.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "--quiet", "-m", "second")

	base := filepath.Join(t.TempDir(), "plugins")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	ins, err := New(base)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := ins.Stage(context.Background(), repo+"/.git#"+first, "")
	if err != nil {
		t.Fatalf("stage at a pinned revision: %v", err)
	}
	if preview.Revision != first {
		t.Fatalf("pinned install resolved to %q, want %q", preview.Revision, first)
	}
	staged, err := os.ReadFile(filepath.Join(ins.StagedDir(preview.StagedID), "plugin.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(staged), "2.0.0") {
		t.Fatalf("the pinned install fetched the branch tip instead:\n%s", staged)
	}
}
