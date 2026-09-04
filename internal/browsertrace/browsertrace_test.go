package browsertrace

import (
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

func call(agent, sess, name, id string, args map[string]any) message.Event {
	return message.Event{Type: "tool.call", AgentID: agent, SessionID: sess, Timestamp: time.Now(),
		Payload: message.ToolCall{Name: name, ID: id, Arguments: args}}
}

func result(agent, sess, callID string, isErr bool) message.Event {
	return message.Event{Type: "tool.result", AgentID: agent, SessionID: sess, Timestamp: time.Now(),
		Payload: message.ToolResult{CallID: callID, IsError: isErr}}
}

func resultContent(agent, sess, callID, content string) message.Event {
	return message.Event{Type: "tool.result", AgentID: agent, SessionID: sess, Timestamp: time.Now(),
		Payload: message.ToolResult{CallID: callID, Content: content}}
}

func TestBuild_ReconstructsBrowserSteps(t *testing.T) {
	events := []message.Event{
		call("a", "s1", "mcp__playwright__browser_navigate", "c1", map[string]any{"url": "https://example.com"}),
		call("a", "s1", "mcp__playwright__browser_click", "c2", map[string]any{"selector": "#login"}),
		call("a", "s1", "mcp__playwright__browser_type", "c3", map[string]any{"text": "hello"}),
		call("a", "s1", "kb_search", "c4", map[string]any{"query": "x"}), // not a browser tool
		call("a", "s1", "mcp__playwright__browser_navigate", "c5", map[string]any{"url": "https://example.com/next"}),
	}
	tr := Build("a", "s1", events)
	if len(tr.Steps) != 4 {
		t.Fatalf("steps = %d, want 4 (kb_search excluded)", len(tr.Steps))
	}
	if tr.Navigations != 2 {
		t.Fatalf("navigations = %d, want 2", tr.Navigations)
	}
	if tr.LastURL != "https://example.com/next" {
		t.Fatalf("last url = %q", tr.LastURL)
	}
	if tr.Steps[0].Action != "navigate" || tr.Steps[1].Action != "click" || tr.Steps[2].Action != "type" {
		t.Fatalf("actions misclassified: %+v", tr.Steps)
	}
}

func TestBuild_MarksErrorSteps(t *testing.T) {
	events := []message.Event{
		call("a", "s1", "mcp__browser__navigate", "c1", map[string]any{"url": "https://x.test"}),
		result("a", "s1", "c1", true),
	}
	tr := Build("a", "s1", events)
	if len(tr.Steps) != 1 || !tr.Steps[0].IsError {
		t.Fatalf("expected one errored step, got %+v", tr.Steps)
	}
}

func TestBuild_CapturesScreenshotRef(t *testing.T) {
	events := []message.Event{
		call("a", "s1", "mcp__playwright__browser_screenshot", "c1", map[string]any{"path": "shot1.png"}),
	}
	tr := Build("a", "s1", events)
	if tr.Screenshot != "shot1.png" {
		t.Fatalf("screenshot ref = %q", tr.Screenshot)
	}
}

func TestBuild_CapturesScreenshotFromResult(t *testing.T) {
	events := []message.Event{
		call("a", "s1", "mcp__playwright__browser_screenshot", "c1", map[string]any{}),
		resultContent("a", "s1", "c1", `{"artifact":{"path":"shot2.png"},"text":"captured screenshot"}`),
	}
	tr := Build("a", "s1", events)
	if tr.Screenshot != "shot2.png" {
		t.Fatalf("trace screenshot ref = %q", tr.Screenshot)
	}
	if len(tr.Steps) != 1 || tr.Steps[0].Screenshot != "shot2.png" || tr.Steps[0].Output == "" {
		t.Fatalf("step did not capture screenshot/output: %+v", tr.Steps)
	}
}

func TestBuild_CapturesShortObservation(t *testing.T) {
	events := []message.Event{
		call("a", "s1", "mcp__playwright__browser_get_text", "c1", map[string]any{"selector": "main"}),
		resultContent("a", "s1", "c1", `{"text":"Welcome to the app"}`),
	}
	tr := Build("a", "s1", events)
	if len(tr.Steps) != 1 || tr.Steps[0].Output != "Welcome to the app" {
		t.Fatalf("output preview = %+v", tr.Steps)
	}
}

func TestBuild_SessionFilter(t *testing.T) {
	events := []message.Event{
		call("a", "s1", "mcp__browser__navigate", "c1", map[string]any{"url": "https://a"}),
		call("a", "s2", "mcp__browser__navigate", "c2", map[string]any{"url": "https://b"}),
	}
	if got := Build("a", "s2", events); len(got.Steps) != 1 || got.LastURL != "https://b" {
		t.Fatalf("session filter failed: %+v", got)
	}
}
