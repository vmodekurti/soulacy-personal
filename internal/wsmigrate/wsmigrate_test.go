// wsmigrate_test.go — migrating a legacy flat ~/.soulacy installation into
// the organized soulspace workspace.
package wsmigrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// legacyFixture builds a realistic pre-soulspace installation.
func legacyFixture(t *testing.T) (home, root string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SOULACY_WORKSPACE", "")
	root = filepath.Join(home, ".soulacy")

	dirs := []string{"agents", "skills/greeter", "plugins/weather", "templates",
		"memory/episodic", "logs", "audit", "secrets", "tools", "mcp-servers/rocketmoney"}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"config.yaml":                 "server:\n  port: 18789\nagent_dirs:\n    - " + filepath.Join(root, "agents") + "\nlog:\n    file: " + filepath.Join(root, "logs", "soulacy.log") + "\n",
		"agents/bot.yaml":             "id: bot\n",
		"skills/greeter/SKILL.md":     "# greeter",
		"plugins/weather/plugin.yaml": "id: weather\n",
		"memory/episodic/x.json":      "{}",
		"logs/soulacy.log":            "log line\n",
		"secrets/key":                 "shh",
		"tools/util.py":               "def f(): pass\n",
		"actions.db":                  "sqlite-bytes",
		"actions.db-wal":              "wal-bytes",
		"archive.db":                  "sqlite-bytes",
		"knowledge.db":                "sqlite-bytes",
		"workboard.db":                "sqlite-bytes",
		"credentials.db":              "vault-bytes",
		"unrelated-note.txt":          "keep me where I am",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home, root
}

func TestPlan_ListsEverything(t *testing.T) {
	_, root := legacyFixture(t)
	plan, err := Plan()
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.From != root || !strings.HasSuffix(plan.To, "soulspace") {
		t.Errorf("plan endpoints: %+v", plan)
	}
	var moves int
	for _, m := range plan.Moves {
		moves++
		if m.From == "" || m.To == "" {
			t.Errorf("incomplete move: %+v", m)
		}
	}
	if moves < 10 {
		t.Errorf("plan has %d moves, expected the full fixture", moves)
	}
	// Unknown files stay put and are reported.
	found := false
	for _, l := range plan.LeftInPlace {
		if strings.Contains(l, "unrelated-note.txt") {
			found = true
		}
	}
	if !found {
		t.Errorf("unrelated file not reported as left in place: %v", plan.LeftInPlace)
	}
}

func TestApply_MovesAndRewritesConfig(t *testing.T) {
	home, root := legacyFixture(t)
	plan, err := Plan()
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(plan); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	ss := filepath.Join(root, "soulspace")
	checks := map[string]string{
		"agents/bot.yaml":             "id: bot",
		"skills/greeter/SKILL.md":     "# greeter",
		"plugins/weather/plugin.yaml": "id: weather",
		"memory/episodic/x.json":      "{}",
		"logs/soulacy.log":            "log line",
		"tools/util.py":               "def f",
		"mcp-servers/rocketmoney":     "", // dir
		"data/actions.db":             "sqlite-bytes",
		"data/actions.db-wal":         "wal-bytes", // WAL siblings travel with the db
		"data/archive.db":             "sqlite-bytes",
		"data/knowledge.db":           "sqlite-bytes",
		"data/workboard.db":           "sqlite-bytes",
		"secrets/credentials.db":      "vault-bytes",
		"secrets/key":                 "shh",
	}
	for rel, want := range checks {
		p := filepath.Join(ss, rel)
		st, err := os.Stat(p)
		if err != nil {
			t.Errorf("missing after migration: %s", rel)
			continue
		}
		if want != "" && !st.IsDir() {
			body, _ := os.ReadFile(p)
			if !strings.Contains(string(body), want) {
				t.Errorf("%s content = %q", rel, body)
			}
		}
	}

	// config.yaml moved AND internal absolute paths rewritten.
	cfgBody, err := os.ReadFile(filepath.Join(ss, "config.yaml"))
	if err != nil {
		t.Fatalf("config.yaml not moved: %v", err)
	}
	if !strings.Contains(string(cfgBody), filepath.Join(ss, "agents")) {
		t.Errorf("agent_dirs not rewritten:\n%s", cfgBody)
	}
	if !strings.Contains(string(cfgBody), filepath.Join(ss, "logs", "soulacy.log")) {
		t.Errorf("log.file not rewritten:\n%s", cfgBody)
	}
	if strings.Contains(string(cfgBody), filepath.Join(home, ".soulacy", "agents")+"\n") &&
		!strings.Contains(string(cfgBody), "soulspace") {
		t.Errorf("legacy paths survived:\n%s", cfgBody)
	}

	// Unknown file stayed in the legacy root.
	if _, err := os.Stat(filepath.Join(root, "unrelated-note.txt")); err != nil {
		t.Errorf("unrelated file should stay put: %v", err)
	}

	// Legacy markers gone → the resolver now picks soulspace.
	if _, err := os.Stat(filepath.Join(root, "config.yaml")); !os.IsNotExist(err) {
		t.Error("legacy config.yaml must be gone after migration")
	}
	if _, err := os.Stat(filepath.Join(root, "actions.db")); !os.IsNotExist(err) {
		t.Error("legacy actions.db must be gone after migration")
	}
}

func TestPlan_RefusesWithoutLegacyInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SOULACY_WORKSPACE", "")
	if _, err := Plan(); err == nil {
		t.Error("Plan on a fresh (non-legacy) home must error")
	}

	// And after a successful migration the second run refuses too.
	legacyFixture(t)
	plan, err := Plan()
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(plan); err != nil {
		t.Fatal(err)
	}
	if _, err := Plan(); err == nil {
		t.Error("Plan after migration must refuse (workspace already soulspace)")
	}
}
