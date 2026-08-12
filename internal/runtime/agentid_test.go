package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/pkg/agent"
)

// The agent ID becomes a directory name — `filepath.Join(dir, def.ID)` — and
// not every ID is typed by a person: the package importer reads it from an
// uploaded archive, and Studio derives peer agent IDs from a MODEL-authored
// workflow draft. agentvalidate only WARNED about path separators, and a warning
// does not make a report invalid, so the import route's validity check passed.
func TestUpsert_RefusesAnIDThatEscapesTheAgentRoot(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	l := NewLoader([]string{agentsDir})
	l.log = zap.NewNop()

	for _, id := range []string{
		"../escaped",
		"../../../../etc/soulacy",
		"..",
		"nested/child",
		`windows\\style`,
		"has space",
	} {
		err := l.Upsert(agentsDir, &agent.Definition{ID: id, Name: "x"})
		if err == nil {
			t.Errorf("Upsert accepted the ID %q", id)
		}
	}

	// Nothing may have been created outside the agent root.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "agents" {
			t.Fatalf("%q was created outside the agent root", e.Name())
		}
	}
}

func TestUpsert_AcceptsOrdinaryIDs(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader([]string{dir})
	l.log = zap.NewNop()

	for _, id := range []string{"market-digest", "agent_2", "a.b.c", "x"} {
		if err := l.Upsert(dir, &agent.Definition{ID: id, Name: "x"}); err != nil {
			t.Errorf("a normal ID %q was refused: %v", id, err)
		}
	}
}

// The message has to say what to change. "invalid id" sends the operator to the
// source; naming the allowed shape does not.
func TestValidateAgentID_ErrorNamesTheAllowedShape(t *testing.T) {
	err := ValidateAgentID("../escaped")
	if err == nil {
		t.Fatal("no error")
	}
	if !strings.Contains(err.Error(), "a-z") {
		t.Errorf("the error does not say what an allowed ID looks like: %v", err)
	}
}
