package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

func TestStudioSessionEvidence_ExtractsLatestErrorAndContext(t *testing.T) {
	events := []message.Event{
		{
			Type:      "message.in",
			AgentID:   "agent-a",
			SessionID: "sess-a",
			Payload:   message.Message{Parts: message.Text("run the report")},
			Timestamp: time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
		},
		{
			Type:      "tool.result",
			AgentID:   "agent-a",
			SessionID: "sess-a",
			Payload:   map[string]any{"name": "python_1", "error": "FileNotFoundError: tools/stock_screener.py"},
			Timestamp: time.Date(2026, 7, 1, 12, 0, 1, 0, time.UTC),
		},
		{
			Type:      "error",
			AgentID:   "agent-a",
			SessionID: "sess-a",
			Payload:   map[string]any{"stage": "workflow", "error": "python_1 failed"},
			Timestamp: time.Date(2026, 7, 1, 12, 0, 2, 0, time.UTC),
		},
		{
			Type:      "error",
			AgentID:   "other",
			SessionID: "sess-a",
			Payload:   map[string]any{"error": "ignore me"},
		},
	}

	evidence, errText, found := studioSessionEvidence(events, "agent-a", "sess-a")
	if !found {
		t.Fatal("expected evidence to be found")
	}
	if errText != "workflow: python_1 failed" {
		t.Fatalf("errText = %q", errText)
	}
	if !strings.Contains(evidence, "message.in") ||
		!strings.Contains(evidence, "FileNotFoundError") ||
		strings.Contains(evidence, "ignore me") {
		t.Fatalf("unexpected evidence:\n%s", evidence)
	}
}
