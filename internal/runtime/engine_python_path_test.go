package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

func TestRunTool_ResolvesPythonFileFromSOULDirectory(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}

	agentDir := t.TempDir()
	toolsDir := filepath.Join(agentDir, "tools")
	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	toolPath := filepath.Join(toolsDir, "relative_tool.py")
	if err := os.WriteFile(toolPath, []byte("def relative_tool(value):\n    return {'echo': value}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	e := newMinimalEngine(t)
	e.pythonBin = py
	e.SetAllowedToolDirs([]string{agentDir})
	def := &agent.Definition{
		ID:         "relative-tool-agent",
		SourcePath: filepath.Join(agentDir, "SOUL.yaml"),
		Tools: []agent.ToolDef{{
			Name:       "relative_tool",
			PythonFile: filepath.Join("tools", "relative_tool.py"),
		}},
	}

	out, err := e.runTool(context.Background(), def, "sess-relative", message.ToolCall{
		ID:        "call-relative",
		Name:      "relative_tool",
		Arguments: map[string]any{"value": "works"},
	})
	if err != nil {
		t.Fatalf("runTool: %v", err)
	}
	if !strings.Contains(out, `"echo": "works"`) {
		t.Fatalf("tool output = %q, want echoed value", out)
	}
}

func TestResolvePythonFile_PreservesAbsoluteAndSourceLessPaths(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "tool.py")
	if got := resolvePythonFile(&agent.Definition{SourcePath: "/different/SOUL.yaml"}, abs); got != abs {
		t.Fatalf("absolute path = %q, want %q", got, abs)
	}
	if got := resolvePythonFile(&agent.Definition{}, "tools/tool.py"); got != filepath.Clean("tools/tool.py") {
		t.Fatalf("source-less path = %q, want process-relative path", got)
	}
}
