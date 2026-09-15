package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// starterDir is the agent directory the test gateway was given.
func starterDir(t *testing.T, s *Server) string {
	t.Helper()
	if len(s.cfg.AgentDirs) == 0 {
		t.Fatal("test gateway has no agent dir")
	}
	return s.cfg.AgentDirs[0]
}

func TestStarterAgentsArriveOnAFreshInstallation(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	dir := starterDir(t, s)

	s.SeedStarterAgents()

	for _, name := range StarterAgents {
		if s.loader.Get(name) == nil {
			t.Fatalf("a new installation should have %q under Deployed", name)
		}
		if _, err := os.Stat(filepath.Join(dir, name, "SOUL.yaml")); err != nil {
			t.Fatalf("%q should be an ordinary editable agent on disk: %v", name, err)
		}
	}

	// The starter must be usable, not a shell: it is an agent that talks to
	// the person, so it needs the person-model tools and nothing else.
	def := s.loader.Get("getting-to-know-you")
	if def.Builtins == nil {
		t.Fatal("the starter should declare its builtins explicitly")
	}
	tools := map[string]bool{}
	for _, name := range *def.Builtins {
		tools[name] = true
	}
	if !tools["person.model"] || !tools["person.observe"] {
		t.Fatalf("the starter needs the person model tools: %v", *def.Builtins)
	}
	if !def.Enabled {
		t.Fatal("a starter that arrives disabled helps nobody")
	}
}

func TestADeletedStarterStaysDeleted(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	dir := starterDir(t, s)
	s.SeedStarterAgents()

	// The person finishes onboarding and removes the agent.
	if err := os.RemoveAll(filepath.Join(dir, "getting-to-know-you")); err != nil {
		t.Fatal(err)
	}
	if err := s.loader.LoadAll(); err != nil {
		t.Fatal(err)
	}
	if s.loader.Get("getting-to-know-you") != nil {
		t.Fatal("precondition: the agent should be gone")
	}

	// Next boot must not resurrect it.
	s.SeedStarterAgents()
	if s.loader.Get("getting-to-know-you") != nil {
		t.Fatal("a starter the person deleted must not come back on restart")
	}
	if _, err := os.Stat(filepath.Join(dir, "getting-to-know-you", "SOUL.yaml")); !os.IsNotExist(err) {
		t.Fatalf("nothing should have been rewritten: %v", err)
	}
}

func TestANewStarterInALaterReleaseStillArrives(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	dir := starterDir(t, s)

	// An installation that was seeded before this starter existed.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal(starterRecord{Seeded: []string{"some-older-starter"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, starterRecordName), record, 0o600); err != nil {
		t.Fatal(err)
	}

	s.SeedStarterAgents()
	if s.loader.Get("getting-to-know-you") == nil {
		t.Fatal("a starter added in a later release should reach an existing installation")
	}
	// ...and the older one is still remembered, so it is not offered again.
	after := readStarterRecord(filepath.Join(dir, starterRecordName))
	found := map[string]bool{}
	for _, name := range after.Seeded {
		found[name] = true
	}
	if !found["some-older-starter"] || !found["getting-to-know-you"] {
		t.Fatalf("the record should remember both: %v", after.Seeded)
	}
}

func TestSeedingIsIdempotentAndLeavesEditsAlone(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	dir := starterDir(t, s)
	s.SeedStarterAgents()

	path := filepath.Join(dir, "getting-to-know-you", "SOUL.yaml")
	edited := "# a person edited this\n" + readFileString(t, path)
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}

	s.SeedStarterAgents()
	s.SeedStarterAgents()

	if got := readFileString(t, path); got != edited {
		t.Fatal("re-seeding must never overwrite what the person changed")
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
