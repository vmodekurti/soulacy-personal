package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	reasoningpkg "github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/internal/studio"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// /studio/repair-live turns a live node trace into repair proposals. Here the
// producer returned its list under "items" but the formatter node read
// ".search.results" — the deterministic layer should propose a remap with no LLM.
func TestStudioRepairLive_DeterministicRemap(t *testing.T) {
	s, _ := studioFake(t)
	body := `{
	  "workflow": {"name":"News","trigger":{"type":"channel"},"flow":{
	    "nodes":[
	      {"id":"search","kind":"tool","tool":"web_search","output":"search"},
	      {"id":"fmt","kind":"agent","agent":"writer","input":"{{ toJson .search.results }}","output":"reply"}],
	    "edges":[{"from":"search","to":"fmt"},{"from":"fmt","to":"end"}],"entry":"search"}},
	  "node_trace":[
	    {"node_id":"search","kind":"tool","output":"{\"items\":[{\"title\":\"a\"}],\"meta\":{\"n\":1}}"},
	    {"node_id":"fmt","kind":"agent","input":"{{ toJson .search.results }}","error":"can't evaluate field results in type interface"}]
	}`
	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/repair-live", "k", body)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	props, _ := out["proposals"].([]any)
	if len(props) != 1 {
		t.Fatalf("want 1 proposal, got %v", out["proposals"])
	}
	p := props[0].(map[string]any)
	if p["node_id"] != "fmt" {
		t.Fatalf("wrong node: %v", p)
	}
	if nw, _ := p["new"].(string); !strings.Contains(nw, ".search.items") {
		t.Fatalf("expected remap to .search.items, got %q", nw)
	}
	if auto, _ := p["auto"].(bool); !auto {
		t.Error("deterministic remap should be auto")
	}
}

// applyRepairBody builds a /studio/apply-repair request for a draft whose
// formatter reads the wrong key off the search result, plus the failing run's
// real per-node evidence. `newInput` is the repair being proposed.
func applyRepairBody(newInput string) string {
	return `{
	  "workflow": {"name":"News","trigger":{"type":"channel"},"flow":{
	    "nodes":[
	      {"id":"search","kind":"tool","tool":"web_search","output":"search"},
	      {"id":"fmt","kind":"agent","agent":"writer","input":"{{ toJson .search.results }}","output":"reply"}],
	    "edges":[{"from":"search","to":"fmt"},{"from":"fmt","to":"end"}],"entry":"search"}},
	  "failing_input": "morning briefing",
	  "node_trace": [
	    {"node_id":"search","kind":"tool","output":"{\"items\":[{\"title\":\"a\"}],\"meta\":{\"n\":1}}"},
	    {"node_id":"fmt","kind":"agent","input":"{{ toJson .search.results }}","error":"can't evaluate field results in type interface"}],
	  "proposal": {"node_id":"fmt","field":"input","class":"shape_drift","old":"{{ toJson .search.results }}","new":"` + newInput + `"}
	}`
}

// /studio/apply-repair keeps a repair that survives the replay of the ORIGINAL
// failing input in the sandbox.
func TestStudioApplyRepair_PromotesWhenReplayPasses(t *testing.T) {
	s, _ := studioFake(t)
	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/apply-repair", "k",
		applyRepairBody(`{{ toJson .search.items }}`))
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	if valid, _ := out["valid"].(bool); !valid {
		t.Fatalf("patched draft should be valid, got %v", out["errors"])
	}
	if applied, _ := out["applied"].(bool); !applied {
		t.Fatalf("a repair proven by replay must be applied: %v", out["attempt"])
	}
	if rb, _ := out["rolled_back"].(bool); rb {
		t.Error("a promoted repair was not rolled back")
	}
	wf, _ := out["workflow"].(map[string]any)
	flow, _ := wf["flow"].(map[string]any)
	nodes, _ := flow["nodes"].([]any)
	n1 := nodes[1].(map[string]any)
	if in, _ := n1["input"].(string); !strings.Contains(in, ".search.items") {
		t.Fatalf("node input not patched: %q", in)
	}

	attempt, _ := out["attempt"].(map[string]any)
	for _, field := range []string{"validated", "replayed", "replay_passed", "promoted"} {
		if ok, _ := attempt[field].(bool); !ok {
			t.Errorf("attempt.%s should be true: %v", field, attempt)
		}
	}
	diff, _ := attempt["diff"].(map[string]any)
	if diff["old"] == "" || diff["new"] == "" {
		t.Errorf("the before/after must be reported: %v", diff)
	}
	rollback, _ := attempt["rollback"].(map[string]any)
	if rollback["value"] != "{{ toJson .search.results }}" {
		t.Errorf("the undo value must be the ORIGINAL input: %v", rollback)
	}

	// Verification must state that the proof came from the sandbox, never a real
	// run: proving a fix must not cost the user a real message.
	ver, _ := out["verification"].(map[string]any)
	if sb, _ := ver["sandboxed"].(bool); !sb {
		t.Error("the replay must be reported as sandboxed")
	}
	if passed, _ := ver["passed"].(bool); !passed {
		t.Errorf("verification should report the pass: %v", ver)
	}
}

// The failure this whole path exists to close: a patch that parses cleanly and
// still cannot render must be rolled back, and the client must be told so rather
// than being handed a draft that "compiles but doesn't fix it".
func TestStudioApplyRepair_RollsBackWhenReplayFails(t *testing.T) {
	s, _ := studioFake(t)
	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/apply-repair", "k",
		applyRepairBody(`{{ toJson .search.nowhere.deeper }}`))
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	if applied, _ := out["applied"].(bool); applied {
		t.Fatalf("an unproven repair must not be applied: %v", out["attempt"])
	}
	if rb, _ := out["rolled_back"].(bool); !rb {
		t.Errorf("the client must be told the change was rolled back: %v", out)
	}
	// The returned draft is the ORIGINAL, not the failed candidate.
	wf, _ := out["workflow"].(map[string]any)
	flow, _ := wf["flow"].(map[string]any)
	nodes, _ := flow["nodes"].([]any)
	n1 := nodes[1].(map[string]any)
	if in, _ := n1["input"].(string); in != "{{ toJson .search.results }}" {
		t.Fatalf("the rolled-back draft must be unchanged, got %q", in)
	}

	attempt, _ := out["attempt"].(map[string]any)
	if validated, _ := attempt["validated"].(bool); !validated {
		t.Error("the candidate DID validate — that is exactly why validation alone was not enough")
	}
	if passed, _ := attempt["replay_passed"].(bool); passed {
		t.Error("the replay did not pass")
	}
	if reason, _ := attempt["reason"].(string); !strings.Contains(reason, "replay") {
		t.Errorf("the reason should name the replay: %q", reason)
	}

	// The client gets an error CLASS, not the provider's prose.
	failure, _ := out["failure"].(map[string]any)
	if failure == nil {
		t.Fatalf("the original failure must be classified: %v", out)
	}
	if failure["class"] != "shape_drift" {
		t.Errorf("a template that cannot read the returned shape is shape drift, got %v", failure["class"])
	}
	if repairable, _ := failure["repairable"].(bool); !repairable {
		t.Errorf("shape drift is repairable: %v", failure)
	}
	if retryable, _ := failure["retryable"].(bool); retryable {
		t.Error("retrying an unchanged shape mismatch would fail identically")
	}
}

// Without the failing run's evidence there is nothing to replay against, so the
// repair is neither proven nor adopted — and the response says which.
// Without a node_trace there is nothing to replay against, so the user-approved
// repair is applied on its structural check alone and SAID to be unproven.
// Refusing outright — the behaviour this replaces — was worse than the problem
// it guarded: every client that did not post a trace (which is all of them
// today) silently got no repair at all. Faking a replay against synthetic stubs
// would be worse still: a shape-drift fix reads a field the stub lacks, the walk
// errors, and a correct repair is rolled back for an unrelated reason.
func TestStudioApplyRepair_AppliesUnprovenWhenNoEvidence(t *testing.T) {
	s, _ := studioFake(t)
	body := `{
	  "workflow": {"name":"News","trigger":{"type":"channel"},"flow":{
	    "nodes":[
	      {"id":"search","kind":"tool","tool":"web_search","output":"search"},
	      {"id":"fmt","kind":"agent","agent":"writer","input":"{{ toJson .search.results }}","output":"reply"}],
	    "edges":[{"from":"search","to":"fmt"},{"from":"fmt","to":"end"}],"entry":"search"}},
	  "proposal": {"node_id":"fmt","field":"input","new":"{{ toJson .search.items }}"}
	}`
	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/apply-repair", "k", body)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	// The user's approved fix must actually reach them.
	if applied, _ := out["applied"].(bool); !applied {
		t.Fatalf("a validated, user-approved repair must be applied: %v", out["attempt"])
	}
	if rolled, _ := out["rolled_back"].(bool); rolled {
		t.Error("nothing disproved this repair, so it must not be reported as rolled back")
	}
	att, _ := out["attempt"].(map[string]any)
	if unproven, _ := att["unproven"].(bool); !unproven {
		t.Error("the attempt must record that it was applied without proof")
	}
	if promoted, _ := att["promoted"].(bool); promoted {
		t.Error("promotion still requires a replay; validation alone must never promote")
	}
	// And the weaker standard must be disclosed, not hidden behind "applied".
	ver, _ := out["verification"].(map[string]any)
	if replayed, _ := ver["replayed"].(bool); replayed {
		t.Error("no evidence was supplied, so no replay happened")
	}
	if seeded, _ := ver["evidence_seeded"].(bool); seeded {
		t.Error("without a node_trace the replay cannot claim seeded evidence")
	}
	if note, _ := ver["note"].(string); !strings.Contains(note, "node_trace") {
		t.Errorf("the note should say what the client must send for a real proof: %q", note)
	}
}

// A security-class failure is refused before anything is applied: every code
// change available here would weaken a control rather than fix the cause.
func TestStudioApplyRepair_RefusesSecurityClassRepair(t *testing.T) {
	s, _ := studioFake(t)
	body := `{
	  "workflow": {"name":"News","trigger":{"type":"channel"},"flow":{
	    "nodes":[{"id":"fmt","kind":"agent","agent":"writer","input":"x","output":"reply"}],
	    "edges":[{"from":"fmt","to":"end"}],"entry":"fmt"}},
	  "node_trace":[{"node_id":"fmt","kind":"agent","error":"401 unauthorized: invalid api key"}],
	  "proposal": {"node_id":"fmt","field":"input","class":"auth","old":"x","new":"y"}
	}`
	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/apply-repair", "k", body)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	if applied, _ := out["applied"].(bool); applied {
		t.Fatal("an auth failure must never be 'repaired' by changing the workflow")
	}
	attempt, _ := out["attempt"].(map[string]any)
	if validated, _ := attempt["validated"].(bool); validated {
		t.Error("the refusal must come BEFORE anything is applied or validated")
	}
	if reason, _ := attempt["reason"].(string); !strings.Contains(reason, "refused") {
		t.Errorf("the refusal should be explicit: %q", reason)
	}
	failure, _ := out["failure"].(map[string]any)
	if failure["class"] != "auth" {
		t.Errorf("a 401 is an auth class, got %v", failure["class"])
	}
	if sec, _ := failure["security"].(bool); !sec {
		t.Errorf("auth is a security class: %v", failure)
	}
	wf, _ := out["workflow"].(map[string]any)
	flow, _ := wf["flow"].(map[string]any)
	nodes, _ := flow["nodes"].([]any)
	if in, _ := nodes[0].(map[string]any)["input"].(string); in != "x" {
		t.Errorf("the draft must be untouched, got %q", in)
	}
}

func TestStudioRepairDraftFromRuntimeTraceUsesProducerShape(t *testing.T) {
	draft := studio.Draft{Flow: studio.Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "search", Kind: sdkr.FlowNodeTool, Tool: "web_search", Output: "search"},
			{ID: "format", Kind: sdkr.FlowNodeTool, Tool: "publish", Input: `{{ toJson .search.results }}`, Output: "reply"},
		},
	}}
	entries := []reasoningpkg.FlowNodeRun{
		{NodeID: "search", Kind: sdkr.FlowNodeTool, Output: json.RawMessage(`{"items":[{"title":"a"}]}`)},
		{NodeID: "format", Kind: sdkr.FlowNodeTool, Input: `{{ toJson .search.results }}`, Error: `template: t:1: can't evaluate field results in type interface {}`},
	}

	repaired, applied := studioRepairDraftFromTrace(t.Context(), nil, draft, entries)
	if len(applied) != 1 {
		t.Fatalf("applied = %+v, want one deterministic repair", applied)
	}
	if got := repaired.Flow.Nodes[1].Input; !strings.Contains(got, ".search.items") {
		t.Fatalf("input = %q, want observed items key", got)
	}
}
