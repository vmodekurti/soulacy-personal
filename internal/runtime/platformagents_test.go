package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

func loaderWithoutPlatformAgents(t *testing.T, dirs ...string) *Loader {
	t.Helper()
	l := NewLoader(dirs)
	l.SetPlatformAgentsEnabled(false)
	return l
}

// The whole point: with platform agents off, the System agent does not exist
// in ANY workspace — including the personal one it is seeded into, because a
// multi-user deployment has no personal workspace for it to belong to.
func TestTheSystemAgentIsGoneEverywhereWhenPlatformAgentsAreOff(t *testing.T) {
	l := loaderWithoutPlatformAgents(t)
	for _, ws := range []string{PersonalWorkspaceID, "ws_tenant", ""} {
		if def := l.GetInWorkspace(ws, SystemAgentID); def != nil {
			t.Errorf("workspace %q still resolves the System agent", ws)
		}
		for _, listed := range l.AllInWorkspace(ws) {
			if listed.ID == SystemAgentID {
				t.Errorf("workspace %q still lists the System agent", ws)
			}
		}
	}
}

// Removing the map entry is what closes chat, streaming, manual trigger,
// replay, schedules and channel delivery at once. A filter at the entry points
// would have to be repeated in each and missing from the next one added.
func TestTurningPlatformAgentsBackOnRestoresIt(t *testing.T) {
	l := loaderWithoutPlatformAgents(t)
	l.SetPlatformAgentsEnabled(true)
	if def := l.GetInWorkspace(PersonalWorkspaceID, SystemAgentID); def == nil {
		t.Fatal("the acknowledgement path must restore the agent; an operator who waives this " +
			"and gets nothing will edit the binary")
	}
	if !l.IsBuiltinInWorkspace("ws_tenant", SystemAgentID) {
		t.Error("restored but not reported as a built-in")
	}
}

// Answering "yes, that is a built-in" for an agent the loader will not return
// makes peer expansion, the delete guard and the GUI badge all describe
// something that is not there.
func TestItIsNotReportedAsABuiltinWhileItDoesNotExist(t *testing.T) {
	l := loaderWithoutPlatformAgents(t)
	if l.IsBuiltinInWorkspace("ws_tenant", SystemAgentID) {
		t.Fatal("reported as a built-in while absent")
	}
}

// Escalation by filename: a SOUL.yaml named with the reserved ID used to be
// PROMOTED — enabled, given SystemTools and the full privileged confirm list.
// That turns "can write an agent file" into "has host shell".
func TestASOULFileCannotClaimTheReservedIDWhenPlatformAgentsAreOff(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, ".workspaces", "ws_tenant", SystemAgentID)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := "id: system\nname: Mine\ndescription: escalation attempt\ntrigger: channel\n"
	if err := os.WriteFile(filepath.Join(agentDir, "SOUL.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	l := loaderWithoutPlatformAgents(t, dir)
	if errs := l.LoadAll(); len(errs) > 0 {
		t.Fatalf("LoadAll: %v", errs)
	}
	if def := l.GetInWorkspace("ws_tenant", SystemAgentID); def != nil {
		t.Fatalf("a tenant's own file claimed the reserved ID and was loaded with system_tools=%v",
			def.SystemTools)
	}
}

// The same escalation through the API. UpsertInWorkspace is reached by the
// package importer and by Studio with IDs nobody typed, so the refusal belongs
// there rather than in each caller.
func TestTheAPICannotCreateTheReservedIDWhenPlatformAgentsAreOff(t *testing.T) {
	dir := t.TempDir()
	l := loaderWithoutPlatformAgents(t, dir)
	err := l.UpsertInWorkspace("ws_tenant", dir,
		&agent.Definition{ID: SystemAgentID, Name: "Mine", Trigger: agent.TriggerChannel}, "usr_a")
	if err == nil {
		t.Fatal("a tenant created an agent with the reserved platform ID; it would have been " +
			"granted SystemTools and the privileged confirm list")
	}
	if def := l.GetInWorkspace("ws_tenant", SystemAgentID); def != nil {
		t.Fatal("refused and stored anyway")
	}
}

// Personal installations must be untouched — invariant 7.
func TestPersonalInstallationsKeepTheSystemAgent(t *testing.T) {
	l := NewLoader(nil)
	if def := l.GetInWorkspace(PersonalWorkspaceID, SystemAgentID); def == nil {
		t.Fatal("the default Loader lost the System agent; every existing single-user install " +
			"reaches it by default")
	}
}

// seedBuiltins is called from NewLoader and from SetPlatformAgentsEnabled(true),
// so today its guard cannot fire. That is precisely why it is tested directly:
// an unreachable guard is a guess about the future, and the next caller — a
// reload path, a second seeding site — is the one that would reintroduce the
// agent into a deployment that switched it off. White-box on purpose; the
// invariant is "seeding never contradicts the switch", not "some public call
// produces this".
func TestSeedingNeverContradictsTheSwitch(t *testing.T) {
	l := NewLoader(nil)
	l.SetPlatformAgentsEnabled(false)

	l.mu.Lock()
	l.seedBuiltins()
	l.mu.Unlock()

	if def := l.GetInWorkspace(PersonalWorkspaceID, SystemAgentID); def != nil {
		t.Fatal("seedBuiltins re-added the System agent while platform agents were disabled")
	}
}

// The stale-file cleanup restores the built-in when a SOUL.yaml that had
// overridden it is deleted. It must not restore what the switch removed.
func TestTheStaleFileSweepDoesNotResurrectIt(t *testing.T) {
	dir := t.TempDir()
	l := loaderWithoutPlatformAgents(t, dir)
	if errs := l.LoadAll(); len(errs) > 0 {
		t.Fatalf("LoadAll: %v", errs)
	}
	if def := l.GetInWorkspace(PersonalWorkspaceID, SystemAgentID); def != nil {
		t.Fatal("the stale-file sweep restored the built-in System agent")
	}
}

// The upgrade case. An existing installation with a system/SOUL.yaml on disk
// holds a FILE-backed definition, promoted with SystemTools when it was
// loaded. Evicting only the seeded copy would leave that one running.
func TestDisablingAlsoRemovesAFileBackedSystemAgent(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, SystemAgentID)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := "id: system\nname: Ours\ndescription: pre-existing override\ntrigger: channel\n"
	if err := os.WriteFile(filepath.Join(agentDir, "SOUL.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	// Loaded first, exactly as an upgrading installation would have it.
	l := NewLoader([]string{dir})
	if errs := l.LoadAll(); len(errs) > 0 {
		t.Fatalf("LoadAll: %v", errs)
	}
	before := l.GetInWorkspace(PersonalWorkspaceID, SystemAgentID)
	if before == nil || before.SourcePath == builtinSourcePath {
		t.Fatalf("setup: expected a file-backed System agent, got %#v", before)
	}

	l.SetPlatformAgentsEnabled(false)

	if def := l.GetInWorkspace(PersonalWorkspaceID, SystemAgentID); def != nil {
		t.Fatalf("a file-backed System agent survived the switch with system_tools=%v",
			def.SystemTools)
	}
}

// The stale-file sweep restores the built-in when the SOUL.yaml that overrode
// it disappears. Planting the entry directly reaches that branch without
// depending on the eviction above — the invariant must hold whichever order
// the wiring calls these in.
func TestTheSweepWillNotRestoreABuiltinTheDeploymentDisabled(t *testing.T) {
	dir := t.TempDir()
	l := loaderWithoutPlatformAgents(t, dir)

	l.mu.Lock()
	l.agents[agentKey{PersonalWorkspaceID, SystemAgentID}] = &agent.Definition{
		ID: SystemAgentID, Name: "leftover", SourcePath: filepath.Join(dir, "system", "SOUL.yaml"),
	}
	l.mu.Unlock()

	if errs := l.LoadAll(); len(errs) > 0 {
		t.Fatalf("LoadAll: %v", errs)
	}
	if def := l.GetInWorkspace(PersonalWorkspaceID, SystemAgentID); def != nil {
		t.Fatalf("the sweep restored the built-in after its file vanished: %#v", def)
	}
}
