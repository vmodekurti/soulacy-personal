package mcppackage

import (
	"strings"
	"testing"
)

func TestUpgradeLegacyNodeRunnerArgs(t *testing.T) {
	got := UpgradeLegacyNodeRunnerArgs([]string{"npx", "--yes", "--ignore-scripts", "@dangahagan/weather-mcp@1.23.0", "--units", "metric"})
	joined := strings.Join(got, " ")
	for _, want := range []string{"npm exec --yes --ignore-scripts", "--package @dangahagan/weather-mcp@1.23.0", "node -e", "@dangahagan/weather-mcp", "--units metric"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("upgraded command missing %q: %s", want, joined)
		}
	}
}

func TestUpgradeLegacyNodeRunnerArgsLeavesUnknownCommandsAlone(t *testing.T) {
	for _, input := range [][]string{
		{"node", "server.js"},
		{"npx", "--yes", "other@latest"},
		{"npx", "--yes", "--ignore-scripts", "unsafe@latest"},
	} {
		got := UpgradeLegacyNodeRunnerArgs(input)
		if strings.Join(got, "\x00") != strings.Join(input, "\x00") {
			t.Fatalf("unexpected rewrite: %v -> %v", input, got)
		}
	}
}
