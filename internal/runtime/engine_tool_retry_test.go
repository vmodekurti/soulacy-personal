package runtime

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

func TestRunTool_PythonToolRetriesTransientFailure(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}

	e := newMinimalEngine(t)
	e.pythonBin = py

	statePath := filepath.Join(t.TempDir(), "attempt-state")
	inline := strings.Join([]string{
		"import json, pathlib, sys",
		"args = json.loads(sys.stdin.read() or '{}')",
		"path = pathlib.Path(args['state_path'])",
		"if not path.exists():",
		"    path.write_text('seen', encoding='utf-8')",
		"    raise RuntimeError('temporary upstream failure')",
		"print('ok-after-retry')",
	}, "\n")

	def := &agent.Definition{
		ID: "retry-agent",
		Tools: []agent.ToolDef{{
			Name:         "flaky_fetch",
			Inline:       inline,
			Retries:      1,
			RetryBackoff: "1ms",
		}},
	}

	out, err := e.runTool(context.Background(), def, "sess-retry", message.ToolCall{
		ID:   "call-retry",
		Name: "flaky_fetch",
		Arguments: map[string]any{
			"state_path": statePath,
		},
	})
	if err != nil {
		t.Fatalf("runTool: %v", err)
	}
	if strings.TrimSpace(out) != "ok-after-retry" {
		t.Fatalf("tool output = %q, want ok-after-retry", out)
	}
}
