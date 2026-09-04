package agentmemory

// Story E23: versioned agent rulebooks. Every rule write — auto_update or
// manual — appends an immutable version; locks freeze an agent's rules;
// rollback is itself a new version (never history rewriting).

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func newTestRuleLog(t *testing.T) *RuleLog {
	t.Helper()
	rl, err := OpenRuleLog(filepath.Join(t.TempDir(), "rulebook.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rl.Close() })
	return rl
}

func TestRuleLog_AppendAndVersions(t *testing.T) {
	rl := newTestRuleLog(t)

	v1, err := rl.Append("bot", "# Rules v1\n- be kind", "auto_update")
	if err != nil || v1 != 1 {
		t.Fatalf("first append: v=%d err=%v", v1, err)
	}
	v2, err := rl.Append("bot", "# Rules v2\n- be kind\n- cite sources", "manual")
	if err != nil || v2 != 2 {
		t.Fatalf("second append: v=%d err=%v", v2, err)
	}
	// Another agent's numbering is independent.
	if v, _ := rl.Append("other", "# other", "manual"); v != 1 {
		t.Errorf("other agent first version = %d, want 1", v)
	}

	versions, err := rl.Versions("bot")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 {
		t.Fatalf("versions = %d, want 2", len(versions))
	}
	// Newest first for the GUI.
	if versions[0].Version != 2 || versions[0].Source != "manual" {
		t.Errorf("versions[0] = %+v", versions[0])
	}
	if versions[1].Version != 1 || versions[1].Source != "auto_update" {
		t.Errorf("versions[1] = %+v", versions[1])
	}
	if versions[0].CreatedAt.IsZero() || versions[0].Size == 0 {
		t.Errorf("metadata incomplete: %+v", versions[0])
	}

	body, err := rl.Get("bot", 1)
	if err != nil || !strings.Contains(body, "v1") {
		t.Errorf("Get v1 = %q err=%v", body, err)
	}
	if _, err := rl.Get("bot", 99); err == nil {
		t.Error("unknown version must error")
	}
}

func TestRuleLog_LockSemantics(t *testing.T) {
	rl := newTestRuleLog(t)
	if rl.Locked("bot") {
		t.Error("fresh agent must be unlocked")
	}
	if err := rl.SetLocked("bot", true); err != nil {
		t.Fatal(err)
	}
	if !rl.Locked("bot") {
		t.Error("lock did not stick")
	}
	if err := rl.SetLocked("bot", false); err != nil {
		t.Fatal(err)
	}
	if rl.Locked("bot") {
		t.Error("unlock did not stick")
	}
}

// ── CompositeStore integration ───────────────────────────────────────────────

func newTestComposite(t *testing.T) *CompositeStore {
	t.Helper()
	c := NewCompositeStore(t.TempDir(), nil)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestComposite_VersionedUpdateWritesFileAndLog(t *testing.T) {
	c := newTestComposite(t)

	v, err := c.UpdateProceduralVersioned("bot", "# R1", "auto_update")
	if err != nil || v != 1 {
		t.Fatalf("update: v=%d err=%v", v, err)
	}
	if got := c.ProceduralRules("bot"); got != "# R1" {
		t.Errorf("serving copy = %q", got)
	}
	v, err = c.UpdateProceduralVersioned("bot", "# R2", "manual")
	if err != nil || v != 2 {
		t.Fatalf("second update: v=%d err=%v", v, err)
	}
	versions, err := c.RulebookVersions("bot")
	if err != nil || len(versions) != 2 {
		t.Fatalf("versions = %v err=%v", versions, err)
	}
}

func TestComposite_LockedRefusesWrites(t *testing.T) {
	c := newTestComposite(t)
	if _, err := c.UpdateProceduralVersioned("bot", "# R1", "manual"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetRulebookLocked("bot", true); err != nil {
		t.Fatal(err)
	}

	// Both auto and manual writes are refused while locked.
	if _, err := c.UpdateProceduralVersioned("bot", "# drift", "auto_update"); !errors.Is(err, ErrRulebookLocked) {
		t.Errorf("auto_update on locked agent: err = %v, want ErrRulebookLocked", err)
	}
	if _, err := c.UpdateProceduralVersioned("bot", "# manual edit", "manual"); !errors.Is(err, ErrRulebookLocked) {
		t.Errorf("manual write on locked agent: err = %v, want ErrRulebookLocked", err)
	}
	// Serving copy unchanged.
	if got := c.ProceduralRules("bot"); got != "# R1" {
		t.Errorf("locked rules mutated: %q", got)
	}

	// Unlock → writes flow again.
	if err := c.SetRulebookLocked("bot", false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateProceduralVersioned("bot", "# R2", "manual"); err != nil {
		t.Errorf("post-unlock write refused: %v", err)
	}
}

func TestComposite_RollbackCreatesNewVersion(t *testing.T) {
	c := newTestComposite(t)
	_, _ = c.UpdateProceduralVersioned("bot", "# good rules", "manual")
	_, _ = c.UpdateProceduralVersioned("bot", "# drifted rules", "auto_update")

	v, err := c.RollbackProcedural("bot", 1)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if v != 3 {
		t.Errorf("rollback version = %d, want 3 (a NEW version, not history rewrite)", v)
	}
	if got := c.ProceduralRules("bot"); got != "# good rules" {
		t.Errorf("serving copy after rollback = %q", got)
	}
	versions, _ := c.RulebookVersions("bot")
	if len(versions) != 3 || versions[0].Source != "rollback" {
		t.Errorf("history after rollback = %+v", versions)
	}

	// Rollback to an unknown version errors; locked agents refuse rollback.
	if _, err := c.RollbackProcedural("bot", 42); err == nil {
		t.Error("rollback to unknown version must error")
	}
	_ = c.SetRulebookLocked("bot", true)
	if _, err := c.RollbackProcedural("bot", 1); !errors.Is(err, ErrRulebookLocked) {
		t.Errorf("rollback on locked agent: err = %v, want ErrRulebookLocked", err)
	}
}

// Legacy UpdateProcedural (GUI PUT path) now versions too.
func TestComposite_LegacyUpdateGoesThroughVersioning(t *testing.T) {
	c := newTestComposite(t)
	if err := c.UpdateProcedural("bot", "# via legacy api"); err != nil {
		t.Fatal(err)
	}
	versions, err := c.RulebookVersions("bot")
	if err != nil || len(versions) != 1 || versions[0].Source != "manual" {
		t.Errorf("legacy update not versioned: %+v err=%v", versions, err)
	}
}
