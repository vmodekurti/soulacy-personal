package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

func TestPythonToolRejectsIncompleteReportBeforeWriting(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	e := newMinimalEngine(t)
	e.pythonBin = py
	sentinel := filepath.Join(t.TempDir(), "saved")
	def := &agent.Definition{ID: "report-agent", Tools: []agent.ToolDef{{Name: "save_report", Inline: fmt.Sprintf("from pathlib import Path\nPath(%q).write_text('saved')\nprint('saved')", sentinel), Parameters: map[string]any{
		"type": "object", "required": []string{"report"}, "properties": map[string]any{"report": map[string]any{"type": "object", "required": []string{"error_rate_multiplier"}, "properties": map[string]any{"error_rate_multiplier": map[string]any{"type": "number"}}}},
	}}}}
	call := message.ToolCall{Name: "save_report", Arguments: map[string]any{"report": map[string]any{}}}
	_, err = e.runTool(context.Background(), def, "test", call)
	if err == nil || !strings.Contains(err.Error(), "error_rate_multiplier") {
		t.Fatalf("missing report field accepted: %v", err)
	}
	if _, err = os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("invalid tool executed: %v", err)
	}
	call.Arguments["report"] = map[string]any{"error_rate_multiplier": 12}
	if _, err = e.runTool(context.Background(), def, "test", call); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(sentinel); err != nil {
		t.Fatal("valid report did not execute:", err)
	}
}
