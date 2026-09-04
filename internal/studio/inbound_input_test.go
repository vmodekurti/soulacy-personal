package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// The reported failure, exactly: a conversational agent was forced to a
// workflow, then asked "How is the weather for Buffalo Grove, IL for next 7
// days" and answered with a research digest about how to BUILD a Telegram
// weather bot. The query it ran was the build spec, frozen in at compile time;
// the user's message reached no node.
// Phrased so it routes to a deterministic digest template AND infers a channel
// trigger — the combination that produces a fixed graph for a conversational
// request, which is what forcing Workflow on a chat agent does.
const interactiveDigestIntent = "When a user messages, search for the best deals " +
	"they asked about and send the summary to telegram"

func TestInteractiveDigestReadsTheIncomingMessage(t *testing.T) {
	cat := Catalog{Tools: []string{"web_search", "channel.send"}, Channels: []string{"telegram"}}
	res, ok := CompileDeterministicWorkflow(interactiveDigestIntent, cat, nil)
	if !ok {
		t.Fatal("expected this intent to route to a deterministic digest template")
	}
	if !interactiveTrigger(res.Workflow.Trigger) {
		t.Fatalf("expected an interactive trigger for a conversational intent, got %q",
			res.Workflow.Trigger.Type)
	}

	var readsMessage bool
	for _, n := range res.Workflow.Flow.Nodes {
		if strings.Contains(n.Input, ".trigger.text") {
			readsMessage = true
		}
	}
	if !readsMessage {
		var inputs []string
		for _, n := range res.Workflow.Flow.Nodes {
			inputs = append(inputs, n.ID+": "+n.Input)
		}
		t.Errorf("no node reads the incoming message, so every run answers the build spec:\n%s",
			strings.Join(inputs, "\n"))
	}
}

// A scheduled digest has no incoming message, so baking the query in is right —
// the fix must not template a schedule into reading input that never exists.
func TestScheduledDigestKeepsItsCompileTimeQuery(t *testing.T) {
	const scheduled = "Every weekday at 7am send a digest of the latest AI research news to telegram"
	cat := Catalog{Tools: []string{"web_search", "channel.send"}, Channels: []string{"telegram"}}
	res, ok := CompileDeterministicWorkflow(scheduled, cat, nil)
	if !ok {
		t.Skip("this intent does not route to a deterministic template")
	}
	if interactiveTrigger(res.Workflow.Trigger) {
		t.Fatalf("a scheduled intent produced trigger %q", res.Workflow.Trigger.Type)
	}
	for _, n := range res.Workflow.Flow.Nodes {
		if strings.Contains(n.Input, ".trigger.text") {
			t.Errorf("scheduled node %q templates an inbound message that never arrives: %s", n.ID, n.Input)
		}
	}
}

// The general guard: ANY message-triggered workflow that ignores the message is
// flagged, however it was built. This is what covers model-designed graphs.
func TestContractWarnsWhenAMessageTriggeredWorkflowIgnoresInput(t *testing.T) {
	draft := Draft{
		Name:    "Ignores you",
		Trigger: Trigger{Type: "channel"},
		Flow: Flow{
			Entry: "search",
			Nodes: []sdkr.FlowNode{
				{ID: "search", Kind: "tool", Tool: "web_search",
					Input: `{"query":"how to build a telegram weather bot"}`, Output: "r"},
			},
		},
	}
	cat := Catalog{Tools: []string{"web_search"}}
	r := AssessContract(draft, cat, PreflightInput{Catalog: cat})
	if !hasContractCheck(r, "input.inbound", "warn") {
		t.Errorf("no warning that the incoming message is ignored; checks = %+v", r.Checks)
	}
	// A warning, not a blocker: refusing to save would break the legitimate
	// "when someone messages, post today's status" design.
	if r.Blockers > 0 {
		for _, c := range r.Checks {
			if c.ID == "input.inbound" && c.Status == "block" {
				t.Error("ignoring inbound input was raised as a blocker, not a warning")
			}
		}
	}
}

func TestContractPassesWhenTheMessageIsRead(t *testing.T) {
	draft := Draft{
		Name:    "Reads you",
		Trigger: Trigger{Type: "channel"},
		Flow: Flow{
			Entry: "search",
			Nodes: []sdkr.FlowNode{
				{ID: "search", Kind: "tool", Tool: "web_search",
					Input: `{"query":"{{ .trigger.text }}"}`, Output: "r"},
			},
		},
	}
	cat := Catalog{Tools: []string{"web_search"}}
	r := AssessContract(draft, cat, PreflightInput{Catalog: cat})
	if !hasContractCheck(r, "input.inbound", "pass") {
		t.Errorf("a workflow that reads the message should pass; checks = %+v", r.Checks)
	}
}

// The model has to be TOLD, not just permitted. The prompt already mentioned
// {{ .trigger.text }} as an alternative to inventing a start node, which a model
// can satisfy while still hardcoding a query from the build description.
func TestWorkflowPromptRequiresConsumingTheInboundMessage(t *testing.T) {
	p := BuildPrompt("when a user messages, look up what they asked", Catalog{
		Tools: []string{"web_search"},
	}, nil)
	low := strings.ToLower(p)
	for _, want := range []string{"must consume the inbound message", "{{ .trigger.text }}"} {
		if !strings.Contains(low, strings.ToLower(want)) {
			t.Errorf("prompt does not state the requirement %q", want)
		}
	}
	// It must also forbid the specific substitution that caused the failure.
	if !strings.Contains(low, "do not hardcode a query") {
		t.Error("prompt does not rule out hardcoding a query from the build description")
	}
	// And it must not tell a scheduled workflow to read input it cannot have.
	if !strings.Contains(low, "schedule trigger has no inbound message") {
		t.Error("prompt does not exempt scheduled workflows")
	}
}

// A schedule must never be warned about ignoring input it cannot have.
func TestScheduledWorkflowIsNotWarnedAboutInboundInput(t *testing.T) {
	draft := Draft{
		Name:    "Nightly",
		Trigger: Trigger{Type: "schedule", Config: map[string]any{"cron": "0 7 * * *"}},
		Flow: Flow{
			Entry: "search",
			Nodes: []sdkr.FlowNode{
				{ID: "search", Kind: "tool", Tool: "web_search", Input: `{"query":"ai news"}`, Output: "r"},
			},
		},
	}
	cat := Catalog{Tools: []string{"web_search"}}
	r := AssessContract(draft, cat, PreflightInput{Catalog: cat})
	for _, c := range r.Checks {
		if c.ID == "input.inbound" && c.Status == "warn" {
			t.Error("a scheduled workflow was warned about not reading an inbound message")
		}
	}
}
