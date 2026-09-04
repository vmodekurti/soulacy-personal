package eval

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestGoldenSuitesParse ensures every shipped golden suite under evals/golden
// loads via LoadSuite (JSON or YAML) and satisfies basic structural invariants.
// This is a non-secret, offline test suitable for CI.
func TestGoldenSuitesParse(t *testing.T) {
	dir := filepath.Join("..", "..", "evals", "golden")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read golden dir: %v", err)
	}
	seen := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".json", ".yaml", ".yml":
		default:
			continue
		}
		seen++
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("%s: read: %v", e.Name(), err)
		}
		suite, err := LoadSuite(data)
		if err != nil {
			t.Fatalf("%s: LoadSuite: %v", e.Name(), err)
		}
		if strings.TrimSpace(suite.Name) == "" {
			t.Errorf("%s: suite has no name", e.Name())
		}
		if len(suite.Cases) == 0 {
			t.Errorf("%s: suite has no cases", e.Name())
		}
		for _, c := range suite.Cases {
			// Every regex in a case must compile.
			for _, rx := range append(append([]string{}, c.ExpectedRegex...), c.ExpectedNotRegex...) {
				if _, err := regexp.Compile(rx); err != nil {
					t.Errorf("%s/%s: invalid regex %q: %v", e.Name(), c.Name, rx, err)
				}
			}
		}
	}
	if seen == 0 {
		t.Fatal("no golden suites found")
	}
	// The epic requires golden suites for these areas; guard against accidental deletion.
	required := []string{
		"weather", "stock-screener", "deal-finder", "research-librarian",
		"kb-ingestion", "queues", "studio-repair", "telegram", "slack",
		"discord", "schedules",
	}
	for _, name := range required {
		if _, err := os.Stat(filepath.Join(dir, name+".yaml")); err != nil {
			if _, err2 := os.Stat(filepath.Join(dir, name+".json")); err2 != nil {
				t.Errorf("missing required golden suite: %s", name)
			}
		}
	}
}

// TestMissingSecretsSkips verifies the skip-on-missing-secret contract.
func TestMissingSecretsSkips(t *testing.T) {
	const key = "SOULACY_EVAL_TEST_SECRET_UNSET"
	os.Unsetenv(key)
	if got := missingSecrets([]string{key}); len(got) != 1 || got[0] != key {
		t.Fatalf("expected %s reported missing, got %v", key, got)
	}
	t.Setenv(key, "value")
	if got := missingSecrets([]string{key}); len(got) != 0 {
		t.Fatalf("expected no missing secrets once set, got %v", got)
	}
}
