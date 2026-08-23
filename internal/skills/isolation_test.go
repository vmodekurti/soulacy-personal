// isolation_test.go — the cross-tenant isolation contract for skill inventory
// (MU-017 criterion 1). internal/ownership/catalog.go names this file as the
// isolation evidence for internal/skills/loader.go.
//
// A skill is executable instruction text an agent follows. A shared inventory
// is therefore not a metadata leak: installing a skill in one workspace would
// change what every other tenant's agents do, and two tenants' same-named
// skills would be resolved by scan order rather than by ownership.
package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/wsroot"
)

func writeSkill(t *testing.T, dir, name, description string) {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# " + name + "\n\nInstructions.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTestStores(t *testing.T) (*Stores, string, string) {
	t.Helper()
	root := t.TempDir()
	platform := filepath.Join(root, "platform")
	base := filepath.Join(root, "skills")
	if err := os.MkdirAll(platform, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	return NewStores([]string{platform}, base, zap.NewNop()), platform, base
}

// A workspace sees the platform templates plus its own skills, and nothing
// belonging to another tenant.
func TestASkillInstalledInOneWorkspaceIsInvisibleToAnother(t *testing.T) {
	stores, platform, _ := newTestStores(t)
	writeSkill(t, platform, "shared-helper", "a platform template")

	dirA, err := stores.EnsureDir("ws_a")
	if err != nil {
		t.Fatal(err)
	}
	writeSkill(t, dirA, "alpha-only", "workspace A's own skill")

	a := stores.For("ws_a")
	b := stores.For("ws_b")

	if a.Get("alpha-only") == nil {
		t.Fatal("a workspace cannot see its own skill")
	}
	if b.Get("alpha-only") != nil {
		t.Fatal("one workspace's skill was visible to another")
	}
	// The platform template is a read-only template for both.
	if a.Get("shared-helper") == nil || b.Get("shared-helper") == nil {
		t.Fatal("a platform skill was not available as a template")
	}
}

// A workspace may shadow a platform skill by name. The platform copy is
// untouched, so another tenant keeps seeing the original — the override is a
// local decision, not an edit to shared state.
func TestAWorkspaceShadowsAPlatformSkillWithoutChangingIt(t *testing.T) {
	stores, platform, _ := newTestStores(t)
	writeSkill(t, platform, "search", "the platform version")

	dirA, err := stores.EnsureDir("ws_a")
	if err != nil {
		t.Fatal(err)
	}
	writeSkill(t, dirA, "search", "workspace A's version")

	a := stores.For("ws_a")
	b := stores.For("ws_b")
	if got := a.Get("search"); got == nil || got.Description != "workspace A's version" {
		t.Fatalf("the workspace's own skill did not win: %+v", got)
	}
	if got := b.Get("search"); got == nil || got.Description != "the platform version" {
		t.Fatalf("one workspace's override leaked into another: %+v", got)
	}
	if !stores.PlatformSkill("ws_a", "search") {
		t.Fatal("a shadowed platform skill was not reported as platform-owned")
	}
	if stores.PlatformSkill("ws_a", "alpha-only") {
		t.Fatal("a workspace-local name was reported as platform-owned")
	}
}

// Product invariant 7: personal resolves to the base directory itself, so a
// single-user installation's skills stay where they are and keep their scan
// position.
func TestPersonalSkillsStayAtTheBaseDirectory(t *testing.T) {
	stores, platform, base := newTestStores(t)
	writeSkill(t, platform, "shared-helper", "a platform template")
	writeSkill(t, base, "local", "the single user's own skill")

	if got := stores.Dir(""); got != base {
		t.Fatalf("personal skills directory moved to %q, want %q", got, base)
	}
	if got := stores.Dir(wsroot.PersonalWorkspaceID); got != base {
		t.Fatalf("explicit personal resolved to %q, want %q", got, base)
	}
	personal := stores.For("")
	if personal.Get("local") == nil || personal.Get("shared-helper") == nil {
		t.Fatal("personal lost sight of its own or the platform skills")
	}
	// A tenant is namespaced beneath the base and does not see personal's.
	tenant := stores.Dir("ws_a")
	if tenant == base {
		t.Fatal("a tenant shares the personal skills directory")
	}
	if stores.For("ws_a").Get("local") != nil {
		t.Fatal("a tenant could see the personal workspace's skill")
	}
}

// The workspace's own directory is scanned last. That ordering is the whole
// override rule, and it is easy to reverse by accident when the list is built.
func TestTheWorkspaceDirectoryIsScannedLast(t *testing.T) {
	stores, platform, _ := newTestStores(t)
	dirs := stores.ScanDirs("ws_a")
	if len(dirs) != 2 {
		t.Fatalf("scan list = %v, want the platform dir then the workspace dir", dirs)
	}
	if dirs[0] != platform {
		t.Fatalf("platform directory is not first: %v", dirs)
	}
	if dirs[len(dirs)-1] != stores.Dir("ws_a") {
		t.Fatalf("the workspace directory is not last: %v", dirs)
	}
}

// Rescan is per workspace. An install in one tenant must not make every other
// tenant pay for a re-read, and must not silently pick up a neighbour's files.
func TestRescanAffectsOnlyItsOwnWorkspace(t *testing.T) {
	stores, _, _ := newTestStores(t)
	a := stores.For("ws_a")
	b := stores.For("ws_b")
	if a.Count() != 0 || b.Count() != 0 {
		t.Fatal("expected both workspaces to start empty")
	}
	dirA, err := stores.EnsureDir("ws_a")
	if err != nil {
		t.Fatal(err)
	}
	writeSkill(t, dirA, "late-arrival", "installed after the first scan")

	stores.Rescan("ws_a")
	if a.Get("late-arrival") == nil {
		t.Fatal("a rescan did not pick up the workspace's new skill")
	}
	if b.Count() != 0 {
		t.Fatalf("another workspace's inventory changed: %d skills", b.Count())
	}
}

func TestCanonicalWorkspacePurgeRemovesOnlyItsOwnSkills(t *testing.T) {
	root := t.TempDir()
	platform := filepath.Join(root, "platform")
	base := filepath.Join(root, "skills")
	stores := NewStores([]string{platform}, base, zap.NewNop())
	stores.SetWorkspaceLayoutRoot(root)

	if err := os.MkdirAll(platform, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, platform, "shared-helper", "a platform template")
	for _, workspaceID := range []string{"ws_a", "ws_b"} {
		dir, err := stores.EnsureDir(workspaceID)
		if err != nil {
			t.Fatal(err)
		}
		writeSkill(t, dir, workspaceID+"-only", workspaceID+" private skill")
	}
	if stores.For("ws_a").Get("ws_a-only") == nil || stores.For("ws_b").Get("ws_b-only") == nil {
		t.Fatal("test setup did not load both workspace inventories")
	}

	removed, err := stores.PurgeWorkspace(context.Background(), "ws_a")
	if err != nil {
		t.Fatal(err)
	}
	if removed.Rows == 0 || removed.Bytes == 0 {
		t.Fatalf("purge reported no removed files: %+v", removed)
	}
	if _, err := os.Stat(stores.Dir("ws_a")); !os.IsNotExist(err) {
		t.Fatalf("purged workspace directory still exists: %v", err)
	}
	if stores.For("ws_a").Get("ws_a-only") != nil {
		t.Fatal("purged workspace skill survived in the loader cache")
	}
	if stores.For("ws_b").Get("ws_b-only") == nil {
		t.Fatal("purging ws_a changed ws_b's skill inventory")
	}
	if stores.For("ws_b").Get("shared-helper") == nil {
		t.Fatal("purging a workspace removed the platform skill template")
	}
}

func TestPersonalSkillsCannotBePurgedAsAWorkspace(t *testing.T) {
	stores, _, base := newTestStores(t)
	writeSkill(t, base, "local", "the single user's own skill")

	if _, err := stores.PurgeWorkspace(context.Background(), wsroot.PersonalWorkspaceID); err == nil {
		t.Fatal("personal skill inventory was accepted as a tenant purge target")
	}
	if stores.For(wsroot.PersonalWorkspaceID).Get("local") == nil {
		t.Fatal("a refused personal purge removed or uncached the personal skill")
	}
	if _, err := os.Stat(filepath.Join(base, "local", "SKILL.md")); err != nil {
		t.Fatalf("a refused personal purge changed the personal filesystem: %v", err)
	}
}
