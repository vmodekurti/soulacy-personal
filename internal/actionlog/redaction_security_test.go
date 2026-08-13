package actionlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/pkg/message"
)

func TestAppendRedactsToolArgumentsAndResultsBeforeEveryPersistenceSink(t *testing.T) {
	root := t.TempDir()
	l, err := New(filepath.Join(root, "logs"), filepath.Join(root, "actions.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	secret := "tool-persistence-secret-abcdefghijklmnopqrstuvwxyz"
	l.Append(message.Event{Type: "tool.call", AgentID: "demo", SessionID: "s", Timestamp: time.Now(), Payload: map[string]any{
		"arguments": map[string]any{"headers": map[string]any{"X-Custom": secret}},
	}})
	l.Append(message.Event{Type: "tool.result", AgentID: "demo", SessionID: "s", Timestamp: time.Now(), Payload: message.ToolResult{Content: "token=" + secret}})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(root, "logs", "demo.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("JSONL persisted secret: %s", raw)
	}

	// Reopen to exercise the independent SQLite read path.
	l, err = New(filepath.Join(root, "logs"), filepath.Join(root, "actions.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	events, err := l.QueryEvents("demo", "s", 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	for _, ev := range events {
		if strings.Contains(toJSON(ev.Payload), secret) {
			t.Fatalf("SQLite persisted secret in %#v", ev.Payload)
		}
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
