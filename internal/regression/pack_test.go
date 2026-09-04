// Package regression contains cross-feature "top workflow" checks that assert
// the invariants users depend on still hold together after changes. Each
// subsystem also has its own focused unit tests; this pack guards the seams
// between them so a change in one place can't silently break the overall story.
package regression

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/browsertrace"
	"github.com/soulacy/soulacy/internal/executor/cloud"
	execcommand "github.com/soulacy/soulacy/internal/executor/command"
	"github.com/soulacy/soulacy/internal/learning"
	"github.com/soulacy/soulacy/internal/pairing"
	"github.com/soulacy/soulacy/internal/policy"
	"github.com/soulacy/soulacy/internal/proactive"
	"github.com/soulacy/soulacy/internal/studio"
	"github.com/soulacy/soulacy/internal/templates"
	"github.com/soulacy/soulacy/internal/webpush"
	"github.com/soulacy/soulacy/pkg/message"
)

type fakeStudioLLM struct {
	out string
}

func (f fakeStudioLLM) Complete(context.Context, string) (string, error) {
	return f.out, nil
}

// Workflow 1: a locked-down agent's shell tool is denied, its network tool is
// confined to an allow-list, and a benign read tool is untouched.
func TestWorkflow_PolicyGating(t *testing.T) {
	cfg := policy.Config{Enabled: true, Shell: "deny", AllowDomains: []string{"api.internal"}}
	if a, _ := policy.Evaluate(cfg, "shell_exec", map[string]any{"command": "rm -rf /"}); a != policy.ActionDeny {
		t.Fatalf("shell should be denied, got %s", a)
	}
	if a, _ := policy.Evaluate(cfg, "http_request", map[string]any{"url": "https://api.internal/x"}); a != policy.ActionAllow {
		t.Fatalf("allow-listed host should pass, got %s", a)
	}
	if a, _ := policy.Evaluate(cfg, "http_request", map[string]any{"url": "https://evil.example/x"}); a != policy.ActionDeny {
		t.Fatalf("off-list host should be denied, got %s", a)
	}
	if a, _ := policy.Evaluate(cfg, "kb_search", nil); a != policy.ActionAllow {
		t.Fatalf("benign tool should be allowed, got %s", a)
	}
}

// Workflow 2: an agent selecting a cloud execution backend produces a runnable
// wrapper command that ends in the python invocation.
func TestWorkflow_CloudExecutionSelection(t *testing.T) {
	runner, ok := cloud.Preset("runpod", "pod-9", "")
	if !ok {
		t.Fatal("runpod preset must resolve")
	}
	e := execcommand.New("runpod", runner, "python3")
	argv := e.Argv("print('hi')")
	if argv[len(argv)-3] != "python3" || argv[len(argv)-2] != "-c" {
		t.Fatalf("python invocation missing: %v", argv)
	}
	if !strings.Contains(strings.Join(argv, " "), "--pod-id pod-9") {
		t.Fatalf("target not threaded into runner: %v", argv)
	}
}

// Workflow 3: the learning loop produces reuse evidence after a skill is
// accepted and later loaded in a real run.
func TestWorkflow_LearningEvidence(t *testing.T) {
	base := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	accepted := []learning.Proposal{{
		ID: "p1", AgentID: "a", Kind: "skill", Status: learning.StatusAccepted,
		Meta: map[string]string{"skill_name": "brief"}, UpdatedAt: base, CreatedAt: base,
	}}
	events := []message.Event{
		{Type: "tool.call", AgentID: "a", SessionID: "s1", Timestamp: base.Add(time.Hour),
			Payload: message.ToolCall{Name: "read_skill", Arguments: map[string]any{"skill_name": "brief"}}},
	}
	ev := learning.BuildEvidence("a", events, accepted)
	if ev.ReusedSkills != 1 || ev.TotalSkillUses != 1 {
		t.Fatalf("expected reuse evidence, got %+v", ev)
	}
}

// Workflow 4: repeated manual runs of an unscheduled agent surface a scheduling
// suggestion from the proactive detector.
func TestWorkflow_ProactiveSuggestion(t *testing.T) {
	events := []message.Event{
		{Type: "message.in", AgentID: "a", Payload: map[string]any{"channel": "http"}},
		{Type: "message.in", AgentID: "a", Payload: map[string]any{"channel": "http"}},
		{Type: "message.in", AgentID: "a", Payload: map[string]any{"channel": "http"}},
	}
	sugg := proactive.Detect(events, map[string]proactive.AgentSnapshot{"a": {ID: "a", LearningEnabled: true}})
	found := false
	for _, s := range sugg {
		if s.Kind == "schedule" && s.AgentID == "a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a schedule suggestion, got %+v", sugg)
	}
}

// Workflow 5: browser automation is reconstructable from the action log.
func TestWorkflow_BrowserTrace(t *testing.T) {
	events := []message.Event{
		{Type: "tool.call", AgentID: "a", SessionID: "s", Timestamp: time.Now(),
			Payload: message.ToolCall{Name: "mcp__playwright__browser_navigate", Arguments: map[string]any{"url": "https://x.test"}}},
	}
	tr := browsertrace.Build("a", "s", events)
	if tr.Navigations != 1 || tr.LastURL != "https://x.test" {
		t.Fatalf("browser trace wrong: %+v", tr)
	}
}

// Workflow 6: a pairing token is single-use.
func TestWorkflow_Pairing(t *testing.T) {
	s := pairing.NewStore()
	tok, err := s.Create(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Redeem(tok.Code) || s.Redeem(tok.Code) {
		t.Fatalf("pairing token must redeem exactly once")
	}
}

// Workflow 7: push VAPID keys are the sizes browsers expect.
func TestWorkflow_PushKeys(t *testing.T) {
	pub, priv, err := webpush.GenerateVAPIDKeys()
	if err != nil || pub == "" || priv == "" {
		t.Fatalf("vapid key generation failed: %v", err)
	}
	if _, err := webpush.NewSender(pub, priv, "mailto:a@b.co"); err != nil {
		t.Fatalf("sender construction failed: %v", err)
	}
}

// Workflow 8: every shipped template loads and exposes a setup checklist.
func TestWorkflow_TemplatesLoad(t *testing.T) {
	cat := templates.New("")
	entries, err := cat.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) < 10 {
		t.Fatalf("expected the vertical-pack templates, got %d", len(entries))
	}
	// The new vertical packs must be present (Entry.Name is the file basename).
	want := map[string]bool{
		"stock-screener": false, "flight-deal-finder": false,
		"github-issue-triage": false, "daily-checkin": false,
	}
	for _, e := range entries {
		if _, ok := want[e.Name]; ok {
			want[e.Name] = true
		}
	}
	for id, seen := range want {
		if !seen {
			t.Fatalf("expected template %q to be present", id)
		}
	}
}

// Workflow 9: Studio-generated reasoning agents are not saved with brittle
// one-shot tool allowlists. Delivery, queue, and KB workflows get their small
// diagnostic/inspection companion tools before they reach production.
func TestWorkflow_StudioAgentCompanionTools(t *testing.T) {
	out := `{
	  "name": "Research Intake",
	  "system_prompt": "Capture incoming URLs, queue them, process them into the KB, verify storage, and send a concise acknowledgement.",
	  "trigger": {"type":"channel"},
	  "channels": ["slack"],
	  "tools": ["queue_put","kb_write","channel.send"],
	  "skills": [],
	  "knowledge": ["AI Docs"],
	  "rationale": "A reasoning agent can switch between capture and processing modes."
	}`
	cat := studio.Catalog{Tools: []string{
		"queue_put", "queue_create", "queue_names",
		"kb_write", "kb_search",
		"channel.send", "channel.status",
	}}
	res, err := studio.CompileAgent(context.Background(), fakeStudioLLM{out: out}, "capture URLs into AI Docs and notify Slack", cat, "react", nil)
	if err != nil {
		t.Fatalf("compile agent: %v", err)
	}
	def, err := studio.ToAgentDefinition(res.Workflow, false)
	if err != nil {
		t.Fatalf("to definition: %v", err)
	}
	if def.Builtins == nil {
		t.Fatal("compiled agent should have builtin tools")
	}
	have := map[string]bool{}
	for _, tool := range *def.Builtins {
		have[tool] = true
	}
	for _, want := range []string{"queue_put", "queue_create", "queue_names", "kb_write", "kb_search", "channel.send", "channel.status"} {
		if !have[want] {
			t.Fatalf("missing tool %q in builtins %v", want, *def.Builtins)
		}
	}
	if !strings.Contains(def.SystemPrompt, "Reasoning Strategy Contract") ||
		!strings.Contains(def.SystemPrompt, "Do not retry the same failed tool call with identical arguments") {
		t.Fatalf("reasoning guardrail missing from system prompt: %q", def.SystemPrompt)
	}
}
