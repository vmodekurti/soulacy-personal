package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/message"
)

func TestIsSideEffectingTool(t *testing.T) {
	sideEffecting := []string{"shell_exec", "run_script", "install_library", "package_install", "write_file", "download_file", "http_request", "mcp__playwright__navigate", "plugin__x__do"}
	for _, n := range sideEffecting {
		if !isSideEffectingTool(n) {
			t.Fatalf("%q should be side-effecting", n)
		}
	}
	readOnly := []string{"read_file", "fetch_url", "kb_search", "web_search", "read_skill"}
	for _, n := range readOnly {
		if isSideEffectingTool(n) {
			t.Fatalf("%q should NOT be side-effecting", n)
		}
	}
}

func TestDryRunResult_Describes(t *testing.T) {
	out := dryRunResult(message.ToolCall{Name: "write_file", Arguments: map[string]any{"path": "/tmp/x"}})
	if !strings.Contains(out, "DRY RUN") || !strings.Contains(out, "write_file") || !strings.Contains(out, "/tmp/x") {
		t.Fatalf("dry-run result missing detail: %s", out)
	}
}

func TestDryRunContext(t *testing.T) {
	if dryRunFrom(context.Background()) {
		t.Fatalf("default should not be dry-run")
	}
	if !dryRunFrom(WithDryRun(context.Background(), true)) {
		t.Fatalf("WithDryRun(true) should report dry-run")
	}
}
