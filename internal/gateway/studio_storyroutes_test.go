package gateway

// studio_storyroutes_test.go — coverage for the four Studio modules that were
// fully implemented and completely unreachable: the build spec (ST-01), the
// plan view (ST-03/ST-06), the model capability registry (ST-09), and the
// capability warning ST-02's "informed override" depends on.

import (
	"net/http"
	"strings"
	"testing"
)

// ── ST-01: build spec ────────────────────────────────────────────────────────

// An underspecified intent must come back as a populated spec PLUS the blocking
// questions, not as a validation error: at this point nothing is wrong, the user
// simply hasn't said yet.
func TestStudioBuildSpec_PopulatesSectionsAndBlockers(t *testing.T) {
	s, _ := studioFake(t)
	body := `{"intent":"Every weekday summarize the top AI stories from hbr.org and technologyreview.com and send a briefing on Telegram"}`
	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/build-spec", "k", body)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	if out["trigger"] != "schedule" {
		t.Errorf("a weekday cadence is a schedule trigger, got %v", out["trigger"])
	}
	for _, section := range []string{"inputs", "stages", "outputs", "delivery", "security"} {
		if list, _ := out[section].([]any); len(list) == 0 {
			t.Errorf("section %q should be populated: %v", section, out[section])
		}
	}
	blockers, _ := out["blockers"].([]any)
	if len(blockers) == 0 {
		t.Fatalf("no time of day and no destination are both blockers: %v", out)
	}
	ids := map[string]bool{}
	for _, b := range blockers {
		m, _ := b.(map[string]any)
		id, _ := m["id"].(string)
		ids[id] = true
		if why, _ := m["why"].(string); strings.TrimSpace(why) == "" {
			t.Errorf("every question must say why it is asked: %v", m)
		}
		if blocker, _ := m["blocker"].(bool); !blocker {
			t.Errorf("blockers[] must only contain blocking questions: %v", m)
		}
	}
	if !ids["schedule_time"] {
		t.Errorf("a schedule with no time must block, got %v", ids)
	}
	if !ids["destination"] {
		t.Errorf("naming a channel is not naming a destination; that must block: %v", ids)
	}
	if ready, _ := out["ready"].(bool); ready {
		t.Error("a spec with blockers is not ready to build")
	}
	if compared, _ := out["compared"].(bool); compared {
		t.Error("no previous_intent was sent, so nothing was compared")
	}
}

// The visible change summary ST-01 requires: with previous_intent the response
// must say what changed and whether the change was material.
func TestStudioBuildSpec_DiffsAgainstPreviousIntent(t *testing.T) {
	s, _ := studioFake(t)
	body := `{
	  "previous_intent":"Every weekday summarize the top AI stories and send a briefing on Telegram to @aidesk",
	  "intent":"Every weekday at 7:00 summarize the top AI stories from hbr.org and turn them into a podcast, then send it on Telegram to @aidesk"
	}`
	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/build-spec", "k", body)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	if compared, _ := out["compared"].(bool); !compared {
		t.Fatalf("previous_intent was supplied, so the response must report a comparison: %v", out)
	}
	diff, _ := out["diff"].([]any)
	if len(diff) == 0 {
		t.Fatalf("adding a source, a time and an audio output must show up as a diff: %v", out)
	}
	fields := map[string]bool{}
	for _, d := range diff {
		m, _ := d.(map[string]any)
		fields[m["field"].(string)] = true
		if kind, _ := m["kind"].(string); kind != "added" && kind != "removed" && kind != "changed" {
			t.Errorf("every change needs a kind: %v", m)
		}
	}
	if !fields["schedule"] || !fields["inputs"] {
		t.Errorf("a new time and a new source should both appear: %v", fields)
	}
	if md, _ := out["materially_different"].(bool); !md {
		t.Error("a refinement that adds a source, a time and an output changed the BUILD")
	}
}

// Rewording alone must NOT read as progress — the case the story cares about
// most, and the reason materially_different is not omitempty.
func TestStudioBuildSpec_RewordingIsNotMateriallyDifferent(t *testing.T) {
	s, _ := studioFake(t)
	body := `{
	  "previous_intent":"Every weekday at 7:00 summarize AI stories from hbr.org and send them on Telegram to @aidesk",
	  "intent":"Each weekday at 7:00 please summarize AI stories from hbr.org and send them on Telegram to @aidesk"
	}`
	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/build-spec", "k", body)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	if md, _ := out["materially_different"].(bool); md {
		t.Errorf("only the prose changed, so nothing structural did: diff=%v", out["diff"])
	}
	if _, present := out["materially_different"]; !present {
		t.Error("materially_different must always be present, or false reads as 'not compared'")
	}
}

// ── ST-03 / ST-06: plan view ─────────────────────────────────────────────────

const planViewDraftJSON = `{"workflow":{
  "name":"AI Podcast",
  "trigger":{"type":"schedule","config":{"cron":"0 7 * * 1-5"}},
  "flow":{"entry":"fan","nodes":[
    {"id":"fan","kind":"parallel","join":"quorum","join_quorum":2,"join_node":"curate"},
    {"id":"search_hbr","kind":"tool","tool":"web_search","description":"search HBR"},
    {"id":"search_mit","kind":"tool","tool":"web_search","description":"search MIT Tech Review"},
    {"id":"search_gartner","kind":"tool","tool":"web_search","description":"search Gartner"},
    {"id":"curate","kind":"python","description":"curate the source pack"},
    {"id":"deliver","kind":"tool","tool":"channel.send","description":"send to Telegram"}],
  "edges":[
    {"from":"fan","to":"search_hbr"},{"from":"fan","to":"search_mit"},{"from":"fan","to":"search_gartner"},
    {"from":"search_hbr","to":"curate"},{"from":"search_mit","to":"curate"},{"from":"search_gartner","to":"curate"},
    {"from":"curate","to":"deliver"},{"from":"deliver","to":"end"}]}}}`

func TestStudioPlanView_TriggerWorkDelivery(t *testing.T) {
	s, _ := studioFake(t)
	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/plan-view", "k", planViewDraftJSON)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	trigger, _ := out["trigger"].(map[string]any)
	if trigger["kind"] != "schedule" || !strings.Contains(trigger["detail"].(string), "0 7 * * 1-5") {
		t.Errorf("trigger not projected: %v", trigger)
	}
	work, _ := out["work"].([]any)
	if len(work) == 0 {
		t.Fatalf("no work stages: %v", out)
	}
	delivery, _ := out["delivery"].([]any)
	if len(delivery) != 1 {
		t.Fatalf("the send step belongs in delivery: %v", out["delivery"])
	}
	if _, present := out["warnings"]; !present {
		t.Error("warnings must always be present; an absent key reads as 'not checked'")
	}
	// Every stage must carry the operational facts a reviewer needs.
	for _, w := range work {
		m, _ := w.(map[string]any)
		if r, _ := m["retry"].(string); r == "" {
			t.Errorf("stage %v has no retry policy", m["id"])
		}
		if cp, _ := m["complete"].(string); cp == "" {
			t.Errorf("stage %v has no completion condition", m["id"])
		}
	}
}

// ST-06: once the graph DECLARES a join policy, the plan must report the
// declaration. Inferring a different one from the branches' on_error would put
// the plan and the runtime into direct contradiction.
func TestStudioPlanView_ParallelGroupReportsDeclaredJoin(t *testing.T) {
	s, _ := studioFake(t)
	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/plan-view", "k", planViewDraftJSON)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	work, _ := out["work"].([]any)
	var group map[string]any
	for _, w := range work {
		m, _ := w.(map[string]any)
		if m["kind"] == "parallel" {
			group = m
			break
		}
	}
	if group == nil {
		t.Fatalf("three branches leaving a kind=parallel node must form a group: %v", work)
	}
	if branches, _ := group["branches"].([]any); len(branches) != 3 {
		t.Errorf("expected 3 branches, got %v", group["branches"])
	}
	// Branches abort on error, so the OLD inference would have said "all".
	if group["join"] != "quorum" {
		t.Fatalf("the declared join must win over the inferred one, got %v", group["join"])
	}
	detail, _ := group["join_detail"].(string)
	if !strings.Contains(detail, "at least 2 of 3") {
		t.Errorf("the declared quorum must be stated in consequences: %q", detail)
	}
	if cp, _ := group["complete"].(string); !strings.Contains(cp, "curate") {
		t.Errorf("the barrier the branches converge at should be named: %q", cp)
	}
}

// ── ST-09: model capabilities ────────────────────────────────────────────────

func TestStudioModelCapabilities_ListsRegistry(t *testing.T) {
	s, _ := studioFake(t)
	status, out := gatewayJSON(t, s, http.MethodGet, "/api/v1/studio/model-capabilities", "k", "")
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	models, _ := out["models"].([]any)
	if len(models) < 5 {
		t.Fatalf("the registry should list every shipped profile, got %d", len(models))
	}
	seenWeak := false
	for _, m := range models {
		card, _ := m.(map[string]any)
		if known, _ := card["known"].(bool); !known {
			t.Errorf("a shipped registry row is known by definition: %v", card)
		}
		if card["recommended_mode"] == nil || card["recommended_mode"] == "" {
			t.Errorf("every card needs a recommended mode: %v", card)
		}
		// A "no" must come with a reason, or the operator cannot argue with it.
		if ok, _ := card["supports_react"].(bool); !ok {
			seenWeak = true
			if why, _ := card["react_why_not"].(string); strings.TrimSpace(why) == "" {
				t.Errorf("%v cannot do ReAct but says nothing about why", card["model"])
			}
		}
	}
	if !seenWeak {
		t.Error("the registry ships models that cannot sustain ReAct; none was reported")
	}
	th, _ := out["thresholds"].(map[string]any)
	if th["react_min_arg_accuracy"] == nil {
		t.Errorf("the bars the verdicts were taken against should be echoed: %v", out["thresholds"])
	}
}

func TestStudioModelCapabilities_SingleModel(t *testing.T) {
	s, _ := studioFake(t)
	status, out := gatewayJSON(t, s, http.MethodGet,
		"/api/v1/studio/model-capabilities?provider=anthropic&model=claude-sonnet-4-6-20260101", "k", "")
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	if known, _ := out["known"].(bool); !known {
		t.Fatalf("a dated build should resolve to its family entry: %v", out)
	}
	if out["source"] != "registry" {
		t.Errorf("source should say where the profile came from, got %v", out["source"])
	}
	if ok, _ := out["supports_react"].(bool); !ok {
		t.Errorf("a frontier model clears the ReAct bar: %v", out)
	}

	// "provider/model" in one field is how ids are usually pasted.
	_, out = gatewayJSON(t, s, http.MethodGet, "/api/v1/studio/model-capabilities?model=ollama/mistral", "k", "")
	if nt, _ := out["native_tools"].(bool); nt {
		t.Errorf("the combined form should resolve to the same row: %v", out)
	}
}

// An unknown model is answered conservatively, NOT with a 404 — a 404 would push
// the client into inventing its own default, and the optimistic default is
// exactly what this registry replaced.
func TestStudioModelCapabilities_UnknownModelIsConservative(t *testing.T) {
	s, _ := studioFake(t)
	status, out := gatewayJSON(t, s, http.MethodGet,
		"/api/v1/studio/model-capabilities?provider=acme&model=totally-new-model", "k", "")
	if status != http.StatusOK {
		t.Fatalf("an unprofiled model must still get an answer, got status=%d %v", status, out)
	}
	if known, _ := out["known"].(bool); known {
		t.Fatalf("this model is not in the registry: %v", out)
	}
	if out["recommended_mode"] != "auto" {
		t.Errorf("an unprofiled model should use the non-workflow default, got %v", out["recommended_mode"])
	}
	if ok, _ := out["supports_react"].(bool); ok {
		t.Error("an unprofiled model must not read as ReAct-capable")
	}
	if why, _ := out["react_why_not"].(string); !strings.Contains(why, "not been profiled") {
		t.Errorf("the reason should be 'we have never measured this', got %q", why)
	}
	if notes, _ := out["notes"].(string); !strings.Contains(notes, "capability probe") {
		t.Errorf("the card should say how to fix the gap: %q", notes)
	}

	// A provider with no model is a malformed query, not an empty registry.
	if status, _ := gatewayJSON(t, s, http.MethodGet, "/api/v1/studio/model-capabilities?provider=acme", "k", ""); status != http.StatusBadRequest {
		t.Errorf("provider without model should be rejected, got %d", status)
	}
}

// ── ST-02: the informed override ─────────────────────────────────────────────

// Forcing ReAct onto a model that cannot emit well-formed tool arguments used to
// produce a clean-looking draft and no warning, even though AdviseStrategy had
// already written one and the compile path threw it away.
func TestStudioCompile_CarriesCapabilityWarning(t *testing.T) {
	s, fake := studioFake(t)
	fake.content = `not json`
	// The builder model is authoritative server-side (the catalog's generation
	// profile is re-grounded from config), so the weak model has to be configured
	// rather than asserted by the client.
	s.cfg.LLM.Studio.Model = "mistral"
	body := `{
	  "intent":"Use a ReAct loop to answer questions about my stock portfolio interactively",
	  "catalog":{"tools":["web_search"]}
	}`
	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/compile", "k", body)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	warn, _ := out["capability_warning"].(string)
	if !strings.Contains(warn, "ReAct is a poor fit") {
		t.Fatalf("an explicit ReAct request on a weak model must warn, got %q (body=%v)", warn, out)
	}
	if out["confidence"] != "low" {
		t.Errorf("an unsupported forced mode is low confidence, got %v", out["confidence"])
	}
	caps, _ := out["capabilities"].(map[string]any)
	if caps == nil {
		t.Fatalf("the profile the decision was based on must ship with the advice: %v", out)
	}
	if caps["model"] != "mistral" {
		t.Errorf("the profile should name the model it resolved, got %v", caps["model"])
	}
	if nt, _ := caps["native_tools"].(bool); nt {
		t.Errorf("the profile should show WHY the warning fired: %v", caps)
	}
	if out["strategy_mode"] != "react" {
		t.Errorf("the override is still honoured, got mode=%v", out["strategy_mode"])
	}
	// Advisory, never blocking: the draft is still returned.
	if _, ok := out["workflow"].(map[string]any); !ok {
		t.Error("the warning must not block the compile")
	}
}

// A model that clears the bar produces no warning — otherwise the warning is
// noise and gets ignored when it matters.
func TestStudioCompile_NoCapabilityWarningForCapableModel(t *testing.T) {
	s, fake := studioFake(t)
	fake.content = `not json`
	// The configured builder model (gpt-4o-mini) clears both bars.
	body := `{
	  "intent":"An on-demand assistant that selects and calls the appropriate skill to answer stock questions",
	  "catalog":{"tools":["web_search"]}
	}`
	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/compile", "k", body)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	if warn, _ := out["capability_warning"].(string); warn != "" {
		t.Errorf("a capable model should produce no warning, got %q", warn)
	}
	if out["confidence"] == nil || out["confidence"] == "" {
		t.Errorf("confidence should still be reported: %v", out)
	}
	if _, ok := out["capabilities"].(map[string]any); !ok {
		t.Errorf("the profile should ship even when nothing is wrong: %v", out)
	}
}
