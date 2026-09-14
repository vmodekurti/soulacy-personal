package runtime

import (
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/llm"
)

func TestBestEffortFinalWritesToolResultsForAPerson(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "brief me"},
		{Role: "tool", Name: "mobile.invoke", Content: `{"id":"8672f00b","device_id":"79c2","command":"health.summary","params":{"window":"yesterday"},"status":"queued","created_at":"2026-09-14T23:47:51Z"}`},
		{Role: "tool", Name: "mobile.command_status", Content: `{"id":"8672f00b","status":"completed","result":{"steps":8421,"sleep_hours":6.5,"workouts":[{"type":"running","duration_minutes":32}],"available":true}}`},
	}
	out := bestEffortFinal(msgs)
	if !strings.HasPrefix(out, "I ran out of steps") {
		t.Fatalf("missing honest preface: %q", out)
	}
	for _, want := range []string{"**mobile invoke**", "command: health.summary", "status: completed", "steps: 8421", "sleep hours: 6.50", "workouts: running", "available: yes"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in digest:\n%s", want, out)
		}
	}
	for _, reject := range []string{"{", "device_id", "created_at", "params"} {
		if strings.Contains(out, reject) {
			t.Fatalf("raw payload leaked (%q):\n%s", reject, out)
		}
	}
	// An assistant message still wins over the digest.
	if got := bestEffortFinal(append(msgs, llm.ChatMessage{Role: "assistant", Content: "Slept 6.5h, 8,421 steps."})); got != "Slept 6.5h, 8,421 steps." {
		t.Fatalf("assistant message should win: %q", got)
	}
	if bestEffortFinal([]llm.ChatMessage{{Role: "user", Content: "hi"}}) != "" {
		t.Fatal("nothing gathered means empty")
	}
}
