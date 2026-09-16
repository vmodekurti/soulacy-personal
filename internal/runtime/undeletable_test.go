package runtime

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

// Genie, System and Steward must survive any route anyone can reach.
func TestCoreAgentsCannotBeDeleted(t *testing.T) {
	for _, id := range []string{SystemAgentID, GenieAgentID, StewardAgentID} {
		if !IsUndeletableAgent(id) {
			t.Errorf("%s must not be deletable", id)
		}
		// Whitespace is not a way round it.
		if !IsUndeletableAgent("  " + id + " ") {
			t.Errorf("%s should be protected regardless of surrounding space", id)
		}
	}
}

func TestOrdinaryAgentsStayDeletable(t *testing.T) {
	for _, id := range []string{"getting-to-know-you", "week-notes", "", "steward-2", "genie-monitor-x"} {
		if IsUndeletableAgent(id) {
			t.Errorf("%q is an ordinary agent and must remain deletable", id)
		}
	}
}

// The guard lives in the loader, not only in the HTTP handler, because the
// handler is one of several callers: monitor cleanup, package import and
// rollback all reach Delete directly.
func TestLoaderRefusesToDeleteACoreAgent(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader([]string{dir})

	// Steward is an ordinary on-disk agent, unlike the two built-ins.
	def := &agent.Definition{ID: StewardAgentID, Name: "Steward", Enabled: true}
	if err := l.Upsert(dir, def); err != nil {
		t.Fatalf("seed steward: %v", err)
	}
	if l.Get(StewardAgentID) == nil {
		t.Fatal("steward should be loaded")
	}

	err := l.Delete(StewardAgentID)
	if err == nil {
		t.Fatal("deleting steward must fail")
	}
	if !strings.Contains(err.Error(), "cannot be deleted") {
		t.Errorf("the error should say why: %v", err)
	}
	if l.Get(StewardAgentID) == nil {
		t.Error("steward must still be loaded after a refused delete")
	}
	// And still on disk: a refused delete must not remove the file first.
	if _, statErr := filepath.Glob(filepath.Join(dir, StewardAgentID, "*")); statErr != nil {
		t.Errorf("glob: %v", statErr)
	}
}

// Steward is core, not frozen. Locking it down like System and Genie would
// take away an agent people are meant to rewrite.
func TestStewardStaysEditable(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader([]string{dir})
	if err := l.Upsert(dir, &agent.Definition{ID: StewardAgentID, Name: "Steward", Enabled: true}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := l.Upsert(dir, &agent.Definition{ID: StewardAgentID, Name: "My Steward", Enabled: false}); err != nil {
		t.Fatalf("steward must remain editable: %v", err)
	}
	got := l.Get(StewardAgentID)
	if got == nil || got.Name != "My Steward" {
		t.Fatalf("edit did not apply: %+v", got)
	}
	if got.Enabled {
		t.Error("steward must remain possible to switch off")
	}
}

// Deleting something that was never there is still success; the guard must
// not turn idempotent deletes into errors for ordinary agents.
func TestDeletingAnAbsentOrdinaryAgentIsStillFine(t *testing.T) {
	l := NewLoader([]string{t.TempDir()})
	if err := l.Delete("never-existed"); err != nil {
		t.Fatalf("delete of an absent agent should be a no-op, got %v", err)
	}
}
