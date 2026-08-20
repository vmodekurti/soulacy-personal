// loader_workspace_test.go — the cross-workspace isolation contract for agent
// definitions and their version history (MU-012).
package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

func newTestLoader(t *testing.T) (*Loader, string) {
	t.Helper()
	dir := t.TempDir()
	return NewLoader([]string{dir}), dir
}

func TestPurgeWorkspaceRemovesDefinitionsVersionsAndInMemoryState(t *testing.T) {
	loader, dir := newTestLoader(t)
	writeAgent(t, loader, "ws_a", dir, "support", "A v1", "alice")
	writeAgent(t, loader, "ws_a", dir, "support", "A v2", "alice")
	writeAgent(t, loader, "ws_b", dir, "support", "B", "bob")
	if versions, err := loader.AgentVersionsInWorkspace("ws_a", "support"); err != nil || len(versions) == 0 {
		t.Fatalf("test did not create version history: %+v %v", versions, err)
	}
	var exported bytes.Buffer
	if count, err := loader.ExportWorkspaceVersionsJSONL(context.Background(), "ws_a", &exported); err != nil || count == 0 {
		t.Fatalf("version export count=%d err=%v", count, err)
	}
	if !strings.Contains(exported.String(), "A v1") || strings.Contains(exported.String(), `name: B`) {
		t.Fatalf("version export crossed a workspace boundary: %s", exported.String())
	}

	removed, err := loader.PurgeWorkspace(context.Background(), "ws_a")
	if err != nil || removed.Bytes == 0 {
		t.Fatalf("purge=%+v err=%v", removed, err)
	}
	if got := loader.GetInWorkspace("ws_a", "support"); got != nil {
		t.Fatalf("deleted workspace remained in memory: %+v", got)
	}
	if got := loader.GetInWorkspace("ws_b", "support"); got == nil || got.Name != "B" {
		t.Fatalf("other workspace was removed: %+v", got)
	}
	if _, err := os.Stat(workspaceAgentRoot(dir, "ws_a")); !os.IsNotExist(err) {
		t.Fatalf("deleted workspace tree survived: %v", err)
	}
	if _, err := os.Stat(workspaceAgentRoot(dir, "ws_b")); err != nil {
		t.Fatalf("other workspace tree was removed: %v", err)
	}
}

func writeAgent(t *testing.T, loader *Loader, workspaceID, dir, id, name, actor string) {
	t.Helper()
	def := &agent.Definition{ID: id, Name: name, Enabled: true}
	if err := loader.UpsertInWorkspace(workspaceID, dir, def, actor); err != nil {
		t.Fatalf("upsert %s/%s: %v", workspaceID, id, err)
	}
}

// Agent IDs are human-chosen slugs. Two teams naming an agent "support-bot" is
// expected, not a conflict — so identity must be (workspace, id).
func TestSameAgentIDInTwoWorkspacesAreDistinctAgents(t *testing.T) {
	loader, dir := newTestLoader(t)
	writeAgent(t, loader, "ws_a", dir, "support-bot", "A's bot", "usr_a")
	writeAgent(t, loader, "ws_b", dir, "support-bot", "B's bot", "usr_b")

	a := loader.GetInWorkspace("ws_a", "support-bot")
	b := loader.GetInWorkspace("ws_b", "support-bot")
	if a == nil || b == nil {
		t.Fatalf("an agent was lost: a=%v b=%v", a, b)
	}
	if a.Name != "A's bot" || b.Name != "B's bot" {
		t.Fatalf("workspaces overwrote each other: %q / %q", a.Name, b.Name)
	}
	if a.SourcePath == b.SourcePath {
		t.Fatalf("both workspaces wrote the same file: %s", a.SourcePath)
	}

	// And they survive a full rescan, which is what the watcher performs.
	if errs := loader.LoadAll(); len(errs) > 0 {
		t.Fatalf("LoadAll: %v", errs)
	}
	if got := loader.GetInWorkspace("ws_a", "support-bot"); got == nil || got.Name != "A's bot" {
		t.Fatalf("after reload ws_a sees %v", got)
	}
	if got := loader.GetInWorkspace("ws_b", "support-bot"); got == nil || got.Name != "B's bot" {
		t.Fatalf("after reload ws_b sees %v", got)
	}
}

// A guessed ID must reveal nothing — not the definition, and not its existence.
func TestAgentFromAnotherWorkspaceIsInvisible(t *testing.T) {
	loader, dir := newTestLoader(t)
	writeAgent(t, loader, "ws_a", dir, "secret-bot", "Confidential", "usr_a")

	if got := loader.GetInWorkspace("ws_b", "secret-bot"); got != nil {
		t.Fatalf("another workspace's agent was returned: %+v", got)
	}
	for _, def := range loader.AllInWorkspace("ws_b") {
		if def.ID == "secret-bot" {
			t.Fatal("another workspace's agent appeared in a listing")
		}
	}
	versions, err := loader.AgentVersionsInWorkspace("ws_b", "secret-bot")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 0 {
		t.Fatalf("another workspace's version history was readable: %+v", versions)
	}
	if _, _, err := loader.ReadAgentVersionInWorkspace("ws_b", "secret-bot", "anything"); err == nil {
		t.Fatal("another workspace's snapshot was readable")
	}
}

// Deleting an ID you do not own must not touch the workspace that does.
func TestDeleteCannotReachAnotherWorkspace(t *testing.T) {
	loader, dir := newTestLoader(t)
	writeAgent(t, loader, "ws_a", dir, "support-bot", "A's bot", "usr_a")

	if err := loader.DeleteInWorkspace("ws_b", "support-bot", "usr_b"); err != nil {
		t.Fatalf("delete of an unowned agent should be a silent no-op: %v", err)
	}
	if got := loader.GetInWorkspace("ws_a", "support-bot"); got == nil {
		t.Fatal("another workspace's delete removed this workspace's agent")
	}

	if err := loader.DeleteInWorkspace("ws_a", "support-bot", "usr_a"); err != nil {
		t.Fatal(err)
	}
	if got := loader.GetInWorkspace("ws_a", "support-bot"); got != nil {
		t.Fatal("the owning workspace's delete did not take effect")
	}
}

// Disabling an agent in one workspace must not disable another workspace's
// agent that happens to share the ID.
func TestSetEnabledIsWorkspaceScoped(t *testing.T) {
	loader, dir := newTestLoader(t)
	writeAgent(t, loader, "ws_a", dir, "shared-id", "A", "usr_a")
	writeAgent(t, loader, "ws_b", dir, "shared-id", "B", "usr_b")

	if !loader.SetEnabledInMemoryInWorkspace("ws_a", "shared-id", false) {
		t.Fatal("agent not found in its own workspace")
	}
	if loader.GetInWorkspace("ws_a", "shared-id").Enabled {
		t.Fatal("disable did not apply")
	}
	if !loader.GetInWorkspace("ws_b", "shared-id").Enabled {
		t.Fatal("disabling one workspace's agent disabled another's")
	}
	if loader.SetEnabledInMemoryInWorkspace("ws_c", "shared-id", false) {
		t.Fatal("a workspace that owns no such agent reported success")
	}
}

// The workspace comes from the file's location, never from its content. This
// is what stops a watcher event or a hand-authored SOUL.yaml from loading an
// agent into a workspace that does not own the directory it sits in.
func TestWorkspaceIsDerivedFromPathNotFileContent(t *testing.T) {
	loader, dir := newTestLoader(t)
	// A file dropped into ws_a's directory, whose YAML tries to look like it
	// belongs elsewhere. There is no field for that, and there must not be one.
	agentDir := filepath.Join(dir, workspaceRootDir, "ws_a", "planted")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := "id: planted\nname: Planted\nworkspace_id: ws_b\nworkspace: ws_b\nenabled: true\n"
	if err := os.WriteFile(filepath.Join(agentDir, "SOUL.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	loader.LoadAll()

	if got := loader.GetInWorkspace("ws_a", "planted"); got == nil {
		t.Fatal("the agent did not load into the workspace that owns its directory")
	}
	if got := loader.GetInWorkspace("ws_b", "planted"); got != nil {
		t.Fatalf("file content moved an agent across the workspace boundary: %+v", got)
	}
}

func TestWorkspaceForPathRejectsMalformedLocations(t *testing.T) {
	dir := "/agents"
	cases := map[string]struct {
		path      string
		want      string
		wantValid bool
	}{
		// The flat legacy layout stays valid inside a workspace, exactly as it
		// is at the personal root.
		"personal nested":  {"/agents/support/SOUL.yaml", PersonalWorkspaceID, true},
		"personal flat":    {"/agents/support.yaml", PersonalWorkspaceID, true},
		"workspace nested": {"/agents/.workspaces/ws_a/support/SOUL.yaml", "ws_a", true},
		"workspace flat":   {"/agents/.workspaces/ws_a/support.yaml", "ws_a", true},
		// A file sitting directly in the namespace root belongs to no
		// workspace; guessing one would be inventing ownership.
		"namespace root": {"/agents/.workspaces/SOUL.yaml", "", false},
		// An unusable workspace segment must not become a directory name.
		"bad workspace id": {"/agents/.workspaces/UPPER/support/SOUL.yaml", "", false},
		// Outside the configured root entirely. Walk never yields this, but
		// classifying it as personal would let a misconfigured or symlinked
		// root inject an agent.
		"escapes root": {"/elsewhere/support/SOUL.yaml", "", false},
	}
	for name, tc := range cases {
		got, ok := workspaceForPath(dir, tc.path)
		if ok != tc.wantValid || (ok && got != tc.want) {
			t.Errorf("%s: workspaceForPath(%q) = %q,%v want %q,%v", name, tc.path, got, ok, tc.want, tc.wantValid)
		}
	}
}

func TestWorkspaceIDsThatCannotBeDirectoryNamesAreRefused(t *testing.T) {
	loader, dir := newTestLoader(t)
	for _, bad := range []string{"..", ".", "../escape", "ws/../../etc", "WS_UPPER", strings.Repeat("a", 65)} {
		err := loader.UpsertInWorkspace(bad, dir, &agent.Definition{ID: "x", Name: "X"}, "usr")
		if err == nil {
			t.Errorf("workspace ID %q was accepted as a path segment", bad)
		}
	}
}

// Personal installations must not notice that this package became
// workspace-aware: the on-disk location has to stay exactly where it was.
func TestPersonalLayoutIsUnchanged(t *testing.T) {
	loader, dir := newTestLoader(t)
	writeAgent(t, loader, "", dir, "legacy-bot", "Legacy", "")

	expected := filepath.Join(dir, "legacy-bot", "SOUL.yaml")
	if _, err := os.Stat(expected); err != nil {
		t.Fatalf("personal agent is not at its historical path %s: %v", expected, err)
	}
	// The unscoped legacy accessors must keep resolving it.
	if got := loader.Get("legacy-bot"); got == nil || got.Name != "Legacy" {
		t.Fatalf("legacy Get did not resolve the personal agent: %+v", got)
	}
	found := false
	for _, def := range loader.All() {
		if def.ID == "legacy-bot" {
			found = true
		}
	}
	if !found {
		t.Fatal("legacy All did not include the personal agent")
	}
	if got := loader.GetInWorkspace(PersonalWorkspaceID, "legacy-bot"); got == nil {
		t.Fatal("the personal workspace ID does not resolve the legacy agent")
	}
}

// An existing installation's agents must survive the upgrade: files already on
// disk in the flat layout load as personal-workspace agents.
func TestPreexistingFlatLayoutLoadsAsPersonal(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "old-bot")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "SOUL.yaml"), []byte("id: old-bot\nname: Old\nenabled: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loader := NewLoader([]string{dir})
	loader.LoadAll()
	if got := loader.Get("old-bot"); got == nil || got.Name != "Old" {
		t.Fatalf("a pre-existing agent did not load: %+v", got)
	}
}

// Platform built-ins belong to the installation, not to a tenant, so every
// workspace sees them — but a tenant's own agent of the same ID wins.
func TestBuiltinsAreVisibleInEveryWorkspace(t *testing.T) {
	loader, dir := newTestLoader(t)
	if got := loader.GetInWorkspace("ws_a", SystemAgentID); got == nil {
		t.Fatal("the built-in system agent is not visible in a non-personal workspace")
	}
	if !loader.IsBuiltinInWorkspace("ws_a", SystemAgentID) {
		t.Fatal("the system agent is not reported as a built-in")
	}
	listed := false
	for _, def := range loader.AllInWorkspace("ws_a") {
		if def.ID == SystemAgentID {
			listed = true
		}
	}
	if !listed {
		t.Fatal("the built-in did not appear in a workspace listing")
	}
	writeAgent(t, loader, "ws_a", dir, "custom", "Custom", "usr_a")
	if loader.IsBuiltinInWorkspace("ws_a", "custom") {
		t.Fatal("a workspace's own agent was reported as a built-in")
	}
}

// Version history is per workspace and records who caused each snapshot.
func TestVersionHistoryIsScopedAndRecordsItsActor(t *testing.T) {
	loader, dir := newTestLoader(t)
	writeAgent(t, loader, "ws_a", dir, "bot", "First", "usr_alice")
	writeAgent(t, loader, "ws_a", dir, "bot", "Second", "usr_bob")
	writeAgent(t, loader, "ws_b", dir, "bot", "Other tenant", "usr_carol")

	versions, err := loader.AgentVersionsInWorkspace("ws_a", "bot")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 {
		t.Fatalf("expected one snapshot of the overwritten definition, got %d: %+v", len(versions), versions)
	}
	snapshot := versions[0]
	if snapshot.Actor != "usr_bob" {
		t.Fatalf("snapshot actor = %q, want the principal that caused the overwrite", snapshot.Actor)
	}
	if snapshot.WorkspaceID != "ws_a" {
		t.Fatalf("snapshot workspace = %q", snapshot.WorkspaceID)
	}
	if snapshot.CreatedAt.IsZero() {
		t.Fatal("snapshot has no recorded creation time")
	}

	// The snapshot captured the *previous* content, and it is that workspace's
	// content — not the other tenant's.
	data, _, err := loader.ReadAgentVersionInWorkspace("ws_a", "bot", snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "First") {
		t.Fatalf("snapshot content is not the overwritten definition: %s", data)
	}
	if strings.Contains(string(data), "Other tenant") {
		t.Fatal("snapshot leaked another workspace's definition")
	}
}

// Provenance lives in a sidecar so a restored definition is byte-identical to
// what was deployed.
func TestSnapshotYAMLCarriesNoInjectedProvenance(t *testing.T) {
	loader, dir := newTestLoader(t)
	writeAgent(t, loader, "ws_a", dir, "bot", "First", "usr_alice")
	writeAgent(t, loader, "ws_a", dir, "bot", "Second", "usr_bob")

	versions, err := loader.AgentVersionsInWorkspace("ws_a", "bot")
	if err != nil || len(versions) == 0 {
		t.Fatalf("no versions: %v %+v", err, versions)
	}
	data, _, err := loader.ReadAgentVersionInWorkspace("ws_a", "bot", versions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "usr_bob") || strings.Contains(string(data), "workspace_id") {
		t.Fatalf("provenance was injected into the snapshot YAML: %s", data)
	}
	sidecar := strings.TrimSuffix(versions[0].Path, ".yaml") + ".meta.json"
	raw, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("no provenance sidecar at %s: %v", sidecar, err)
	}
	var metadata versionMetadata
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Actor != "usr_bob" || metadata.WorkspaceID != "ws_a" {
		t.Fatalf("sidecar metadata = %+v", metadata)
	}
}

// A snapshot with no sidecar (taken before provenance existed) must degrade to
// an empty actor rather than a fabricated one.
func TestSnapshotsWithoutProvenanceReportNoActor(t *testing.T) {
	loader, dir := newTestLoader(t)
	writeAgent(t, loader, "ws_a", dir, "bot", "First", "usr_alice")
	writeAgent(t, loader, "ws_a", dir, "bot", "Second", "usr_bob")
	versions, err := loader.AgentVersionsInWorkspace("ws_a", "bot")
	if err != nil || len(versions) == 0 {
		t.Fatalf("no versions: %v", err)
	}
	if err := os.Remove(strings.TrimSuffix(versions[0].Path, ".yaml") + ".meta.json"); err != nil {
		t.Fatal(err)
	}
	reread, err := loader.AgentVersionsInWorkspace("ws_a", "bot")
	if err != nil {
		t.Fatal(err)
	}
	if len(reread) != 1 || reread[0].Actor != "" {
		t.Fatalf("a missing sidecar produced %+v", reread)
	}
	if reread[0].CreatedAt.IsZero() {
		t.Fatal("a missing sidecar lost the timestamp entirely")
	}
}

// Rolling back must restore this workspace's own history, never another's.
func TestRollbackRestoresOnlyTheOwningWorkspace(t *testing.T) {
	loader, dir := newTestLoader(t)
	writeAgent(t, loader, "ws_a", dir, "bot", "Original", "usr_alice")
	writeAgent(t, loader, "ws_a", dir, "bot", "Broken", "usr_bob")
	writeAgent(t, loader, "ws_b", dir, "bot", "B's bot", "usr_carol")

	versions, err := loader.AgentVersionsInWorkspace("ws_a", "bot")
	if err != nil || len(versions) == 0 {
		t.Fatalf("no versions: %v", err)
	}
	restored, _, err := loader.RestoreAgentVersionInWorkspace("ws_a", dir, "bot", versions[0].ID, "usr_alice")
	if err != nil {
		t.Fatal(err)
	}
	if restored.Name != "Original" {
		t.Fatalf("rollback restored %q", restored.Name)
	}
	if got := loader.GetInWorkspace("ws_b", "bot"); got == nil || got.Name != "B's bot" {
		t.Fatalf("rollback in one workspace changed another: %+v", got)
	}
	// Rolling back is itself a change, so it must be captured with its actor.
	after, err := loader.AgentVersionsInWorkspace("ws_a", "bot")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) < 2 || after[0].Actor != "usr_alice" {
		t.Fatalf("the rollback was not recorded with its actor: %+v", after)
	}
}

func TestAllWorkspacesEnumeratesOwners(t *testing.T) {
	loader, dir := newTestLoader(t)
	writeAgent(t, loader, "ws_a", dir, "a", "A", "usr")
	writeAgent(t, loader, "ws_b", dir, "b", "B", "usr")
	got := strings.Join(loader.AllWorkspaces(), ",")
	// The personal workspace is always present because built-ins are seeded there.
	for _, want := range []string{"ws_a", "ws_b", PersonalWorkspaceID} {
		if !strings.Contains(got, want) {
			t.Errorf("AllWorkspaces = %q, missing %q", got, want)
		}
	}
}
