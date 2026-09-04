package studio

// The vocabulary itself, and the guarantee that nothing emits outside it.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestFixActions_EveryEntryIsUsable(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range FixActions() {
		if a.ID == "" {
			t.Fatal("an action with no id can never be matched")
		}
		if seen[a.ID] {
			t.Fatalf("duplicate action id %q — the second entry's label is unreachable", a.ID)
		}
		seen[a.ID] = true
		if strings.TrimSpace(a.Label) == "" {
			t.Errorf("action %q has no label, so it renders no button", a.ID)
		}
		switch a.Kind {
		case FixKindNavigate, FixKindApply, FixKindFocus:
		default:
			t.Errorf("action %q has unknown kind %q", a.ID, a.Kind)
		}
	}
}

func TestFixActionLabel_UnknownIDReturnsEmpty(t *testing.T) {
	// A plausible-looking default would hide the bug behind a button that does
	// nothing, which is the failure this whole file exists to prevent.
	if got := FixActionLabel("not_a_real_action"); got != "" {
		t.Fatalf("unknown id should have no label, got %q", got)
	}
	if IsFixAction("not_a_real_action") {
		t.Fatal("unknown id should not validate")
	}
}

func TestResolveFixLabel_PrefersTheFindingsOwnWording(t *testing.T) {
	if got := resolveFixLabel(FixOpenDelivery, "Take me to the binding"); got != "Take me to the binding" {
		t.Fatalf("override should win, got %q", got)
	}
	if got := resolveFixLabel(FixOpenDelivery, "  "); got != "Open Delivery" {
		t.Fatalf("blank override should fall back to the vocabulary, got %q", got)
	}
}

// Nothing anywhere in the studio package may emit an action id the vocabulary
// does not declare — the client has no handler for it, so it renders a dead
// button or (since finishItem drops unknowns) silently loses the fix.
func TestNoActionIsEmittedOutsideTheVocabulary(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`Action:\s*"([a-z_]+)"`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			if !IsFixAction(m[1]) {
				t.Errorf("%s emits action %q, which is not in the vocabulary — add it to fixactions.go (and to the client)", f, m[1])
			}
		}
	}
}

// A node-scoped finding should point AT the node, not at the editor the user is
// already looking at.
func TestFinishItem_NodeScopedFindingsRevealTheNode(t *testing.T) {
	got := finishItem(ReadinessItem{Action: FixOpenStudio, NodeID: "parse_results"}, "")
	if got.Action != FixRevealNode {
		t.Fatalf("expected reveal_node for a node-scoped finding, got %q", got.Action)
	}
	if got.ActionLabel != "Show the step" {
		t.Fatalf("label should follow the resolved action, got %q", got.ActionLabel)
	}

	// Without a node there is nothing to reveal, so it stays as it was.
	if got := finishItem(ReadinessItem{Action: FixOpenStudio}, ""); got.Action != FixOpenStudio {
		t.Fatalf("expected open_studio to survive when there is no node, got %q", got.Action)
	}
}

func TestFinishItem_DropsAnUnhandleableAction(t *testing.T) {
	got := finishItem(ReadinessItem{Action: "teleport", Fix: "do the thing"}, "")
	if got.Action != "" {
		t.Fatalf("an id the client cannot handle must not reach it, got %q", got.Action)
	}
	if got.ActionLabel != "" {
		t.Fatalf("no action means no button, got label %q", got.ActionLabel)
	}
	if got.Fix == "" {
		t.Fatal("dropping the action must not drop the written fix — it is all the user has left")
	}
}

// The readiness view is the one place every finding type meets, so it is also
// the one place a finding's own fix can be silently replaced by a generic one.
// It was: security findings had their action overwritten by a category-derived
// "go to this screen", so the panel offered navigation for a fix Studio could
// have applied outright.
func TestReadinessItems_KeepAFindingsOwnAction(t *testing.T) {
	items := securityItems(SecurityReview{
		Warnings: []SecurityFinding{{
			Severity: "warn", Category: "channel",
			Message:     "shared channel exposure",
			Fix:         "…",
			Action:      FixInternalChannelsOnly,
			ActionLabel: "Use internal channels only",
		}},
	})
	if len(items) != 1 {
		t.Fatalf("expected one item, got %d", len(items))
	}
	if items[0].Action != FixInternalChannelsOnly {
		t.Fatalf("the finding's own fix was replaced by a category default: %q", items[0].Action)
	}
	if items[0].ActionLabel != "Use internal channels only" {
		t.Fatalf("the finding's own button text was lost: %q", items[0].ActionLabel)
	}
}

// A finding with no opinion still gets a sensible destination from its category.
func TestReadinessItems_FallBackToTheCategoryMapping(t *testing.T) {
	items := securityItems(SecurityReview{
		Warnings: []SecurityFinding{{Severity: "warn", Category: "channel", Message: "…"}},
	})
	if items[0].Action != FixOpenDelivery {
		t.Fatalf("expected the category fallback, got %q", items[0].Action)
	}
	if items[0].ActionLabel == "" {
		t.Fatal("a fallback action still needs button text")
	}
}

// Contract checks get the same treatment: their own action wins, the id-derived
// mapping is only the fallback.
func TestReadinessItems_KeepAContractChecksOwnAction(t *testing.T) {
	items := contractItems(ContractResult{Checks: []ContractCheck{
		{ID: "security.intent_gate", Status: "warn", Message: "…", Action: FixIntentGateDeny},
		{ID: "runtime.provider", Status: "block", Message: "…"},
	}})
	if items[0].Action != FixIntentGateDeny {
		t.Fatalf("the check's own fix was replaced: %q", items[0].Action)
	}
	if items[1].Action != FixOpenProviders {
		t.Fatalf("expected the id-derived fallback for runtime.provider, got %q", items[1].Action)
	}
}

// Every apply-action must be reachable from a real finding, or the client is
// carrying a handler for something nothing sends. Apply-actions now come from
// two places — the security review and the generation contract — so this has to
// exercise both.
func TestEveryApplyActionIsEmittedBySomeFinding(t *testing.T) {
	emitted := map[string]bool{}
	note := func(action string) {
		if action != "" {
			emitted[action] = true
		}
	}

	for _, d := range []Draft{
		{Tools: []string{"shell_exec"}, Channels: []string{"telegram", "http"}},
		{Tools: []string{"web_search", "shell_exec"}, Channels: []string{"http"}},
	} {
		rev := SecurityPreflight(d, &agent.Definition{ID: "x", Capabilities: []string{"system"}}, "")
		for _, f := range append(append([]SecurityFinding{}, rev.Blockers...), rev.Warnings...) {
			note(f.Action)
		}
	}

	thin := Draft{
		Name:      "Travel Advisor",
		Channels:  []string{"http"},
		NewAgents: []NewAgent{{ID: "summarizer", Name: "Summarizer", SystemPrompt: "Summarise things."}},
		Flow: Flow{Entry: "fmt", Nodes: []sdkr.FlowNode{
			{ID: "fmt", Kind: "agent", Agent: "summarizer", Description: "Format results", Output: "reply"},
		}},
	}
	for _, c := range AssessContract(thin, Catalog{}, PreflightInput{}).Checks {
		note(c.Action)
	}

	// A fan-out whose branches reconverge without naming the barrier. Kept in
	// this corpus because the coverage rule is only as good as the shapes it is
	// shown: this action existed and passed every other test while no draft here
	// could produce it.
	converging := Draft{
		Name: "Digest",
		Flow: Flow{Entry: "fan", Nodes: []sdkr.FlowNode{
			{ID: "fan", Kind: sdkr.FlowNodeParallel, Join: "all"},
			{ID: "a", Kind: "llm", Output: "a_out"},
			{ID: "b", Kind: "llm", Output: "b_out"},
			{ID: "join", Kind: "llm", Input: "{{ .a_out }} {{ .b_out }}", Output: "final"},
		}, Edges: []sdkr.FlowEdge{
			{From: "fan", To: "a"}, {From: "fan", To: "b"},
			{From: "a", To: "join"}, {From: "b", To: "join"},
		}},
	}
	for _, c := range AssessContract(converging, Catalog{}, PreflightInput{}).Checks {
		note(c.Action)
	}

	for _, a := range FixActions() {
		if a.Kind != FixKindApply {
			continue
		}
		if !emitted[a.ID] {
			t.Errorf("apply-action %q is declared but no finding emits it", a.ID)
		}
	}
}

// A thin helper prompt is the one warning a user cannot act on without knowing
// what a good agent prompt looks like — so the finding carries one.
func TestContract_ThinHelperPromptCarriesAWrittenPrompt(t *testing.T) {
	draft := Draft{
		Name:      "Travel Advisor",
		Channels:  []string{"http"},
		NewAgents: []NewAgent{{ID: "summarizer", Name: "Summarizer", SystemPrompt: "Summarise."}},
		Flow: Flow{Entry: "fmt", Nodes: []sdkr.FlowNode{
			{ID: "fmt", Kind: "agent", Agent: "summarizer", Description: "Turn travel results into prose", Output: "reply"},
		}},
	}
	var found *ContractCheck
	checks := AssessContract(draft, Catalog{}, PreflightInput{}).Checks
	for i, c := range checks {
		if c.ID == "agents.prompts" && c.Status == "warn" {
			found = &checks[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected a thin-helper-prompt warning")
	}
	if found.Action != FixWriteHelperPrompt {
		t.Fatalf("expected the write-prompt action, got %q", found.Action)
	}
	if got := found.ActionParams["agent"]; got != "summarizer" {
		t.Fatalf("the fix must name which helper it is for, got %q", got)
	}
	prompt := found.ActionParams["prompt"]
	if len(strings.Fields(prompt)) < 18 {
		t.Fatalf("the offered prompt is as thin as the one it replaces: %q", prompt)
	}
	// It should be about THIS step, not a generic stub.
	if !strings.Contains(prompt, "Summarizer") {
		t.Fatalf("the offered prompt should be written for this helper: %q", prompt)
	}
}

// The delivery blocker used to be reported twice — once under graph integrity
// with a remedy about "broken graph structure" that had nothing to do with it.
func TestContract_DeliveryBlockerIsReportedOnceWithAMatchingFix(t *testing.T) {
	draft := Draft{
		Name:     "Digest",
		Intent:   "every morning summarise the news and send it to me on telegram",
		Channels: []string{"http"},
		Flow: Flow{Entry: "step", Nodes: []sdkr.FlowNode{
			{ID: "step", Kind: "tool", Tool: "web_search", Input: `{"query":"news"}`, Output: "results"},
		}},
	}
	var hits []ContractCheck
	for _, c := range AssessContract(draft, Catalog{}, PreflightInput{}).Checks {
		if strings.Contains(c.Message, "no routable output channel") {
			hits = append(hits, c)
		}
	}
	if len(hits) != 1 {
		ids := []string{}
		for _, h := range hits {
			ids = append(ids, h.ID)
		}
		t.Fatalf("the same finding was reported %d times (%v) — once is enough", len(hits), ids)
	}
	if strings.Contains(hits[0].Fix, "broken graph structure") {
		t.Fatalf("the remedy does not match the finding: %q", hits[0].Fix)
	}
	if !strings.Contains(hits[0].Fix, "Channels & delivery") {
		t.Fatalf("the remedy should name where to change it, got %q", hits[0].Fix)
	}
	if hits[0].ActionLabel == "" {
		t.Fatal("a blocker this specific should offer a button")
	}
}
