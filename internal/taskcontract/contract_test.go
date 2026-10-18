package taskcontract

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

func contractMessage(text string) message.Message {
	return message.Message{
		ID: "message-1", AgentID: "genie", SessionID: "session-1",
		Channel: "http", UserID: "user-1", Role: message.RoleUser,
		Parts: message.Text(text), CreatedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
	}
}

func TestDirectAnswerCompletesWithoutToolEvidence(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 1, 0, time.UTC)
	c := New("run-1", contractMessage("Explain Soulacy"), now)
	c.Configure("auto", 10, 20_000, 20)

	got := c.Complete(nil, true, false, now.Add(time.Second))
	if got.State != "completed" || got.Outcome != OutcomeDirectAnswer {
		t.Fatalf("terminal contract = state %q outcome %q", got.State, got.Outcome)
	}
	if got.Perception.ContentSHA256 == "" || got.Goal != "Explain Soulacy" {
		t.Fatalf("perception was not normalized: %+v", got.Perception)
	}
}

func TestToolEvidenceDoesNotImplyVerifiedAction(t *testing.T) {
	now := time.Now().UTC()
	c := New("run-2", contractMessage("Find a restaurant"), now)
	c.ObserveTool(message.ToolCall{Name: "web_search"}, `{"results":["A"]}`, false)

	got := c.Complete(nil, true, false, now.Add(time.Second))
	if got.Outcome != OutcomeEvidence {
		t.Fatalf("ordinary tool evidence outcome = %q, want %q", got.Outcome, OutcomeEvidence)
	}
	if len(got.Evidence) != 1 || got.Evidence[0].Verified {
		t.Fatalf("ordinary search was treated as verification: %+v", got.Evidence)
	}
}

func TestExternalActionWithoutEvidenceIsBlocked(t *testing.T) {
	now := time.Now().UTC()
	c := New("run-action", contractMessage("Can you book an Uber home?"), now)
	got := c.Complete(nil, true, false, now.Add(time.Second))
	if got.Mode != "external_action" || got.Outcome != OutcomeBlocked || got.State != "blocked" {
		t.Fatalf("unsupported action was not blocked: %+v", got)
	}
}

func TestExplicitTrustedEvidenceCanVerifyAction(t *testing.T) {
	now := time.Now().UTC()
	c := New("run-3", contractMessage("Book the ride"), now)
	c.ObserveTool(message.ToolCall{Name: "channel.send"}, `{"verified":true,"status":"delivered"}`, false)

	got := c.Complete(nil, true, false, now.Add(time.Second))
	if got.Outcome != OutcomeVerified || !got.Evidence[0].Verified {
		t.Fatalf("commit evidence did not verify action: %+v", got)
	}
}

func TestSubmittedWebsiteActionRemainsAttemptedUntilConfirmed(t *testing.T) {
	now := time.Now().UTC()
	c := New("run-submitted", contractMessage("Book the ride"), now)
	c.ObserveTool(message.ToolCall{Name: "commit_website_action"}, `{"status":"submitted","observation":{"text":"Please wait"}}`, false)
	got := c.Complete(nil, true, false, now.Add(time.Second))
	if got.Outcome != OutcomeAttempted || got.State != "incomplete" {
		t.Fatalf("unconfirmed submission was overstated: %+v", got)
	}
}

func TestActionCanPauseForRequiredInput(t *testing.T) {
	now := time.Now().UTC()
	c := New("run-input", contractMessage("Reserve a table"), now)
	c.ObserveTool(message.ToolCall{Name: "plan_action"}, `{"status":"needs_input"}`, false)
	got := c.Complete(nil, true, false, now.Add(time.Second))
	if got.Outcome != OutcomeNeedsInput || got.State != "waiting_for_input" {
		t.Fatalf("input checkpoint was not represented: %+v", got)
	}
}

func TestFailedRoutesReplanWithinBoundThenBlock(t *testing.T) {
	now := time.Now().UTC()
	c := New("run-4", contractMessage("Complete an external action"), now)
	c.ObserveTool(message.ToolCall{Name: "native_api"}, "permission denied", true)
	first := c.ReplanDirective()
	c.ObserveTool(message.ToolCall{Name: "browser_action"}, "permission denied", true)
	second := c.ReplanDirective()
	c.ObserveTool(message.ToolCall{Name: "third_route"}, "permission denied", true)
	third := c.ReplanDirective()
	if first == "" || second == "" || third != "" {
		t.Fatalf("unexpected directives: first=%q second=%q third=%q", first, second, third)
	}
	got := c.Complete(nil, true, false, now.Add(time.Second))
	if got.Replans != 2 || got.Outcome != OutcomeBlocked {
		t.Fatalf("failed route contract = replans %d outcome %q", got.Replans, got.Outcome)
	}
}

func TestRecordsAreRedactedAndBounded(t *testing.T) {
	secret := "api_key=" + "sk-" + strings.Repeat("a1", 30)
	c := New("run-5", contractMessage(secret+" "+strings.Repeat("x", 900)), time.Now().UTC())
	c.ObserveTool(message.ToolCall{Name: "fetch"}, secret+" "+strings.Repeat("y", 900), false)
	got := c.Snapshot()

	if strings.Contains(got.Goal, "sk-") || strings.Contains(got.Evidence[0].Summary, "sk-") {
		t.Fatalf("secret leaked into task contract: %+v", got)
	}
	if len([]rune(got.Goal)) > maxSummaryRunes+3 || len([]rune(got.Evidence[0].Summary)) > maxEvidenceSummary+3 {
		t.Fatalf("bounded fields exceeded their limits")
	}

	failed := New("run-6", contractMessage("fail"), time.Now().UTC())
	got = failed.Complete(errors.New(secret), false, false, time.Now().UTC())
	if strings.Contains(got.Blocker, "sk-") {
		t.Fatalf("secret leaked into failure blocker: %q", got.Blocker)
	}
}
