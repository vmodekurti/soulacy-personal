package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Gateway-level safety gates for the Studio run/build surfaces:
//
//   - the STREAMED generate path must attach the same contract the sync path
//     does, and must say `blocked` out loud rather than emitting a bare success
//     for a draft that cannot run (ST-05);
//   - Run Live must refuse a blocked workflow (422) and unacknowledged side
//     effects (409), and must not silently escalate to unattended/privileged
//     (ST-07 / ST-08 / ST-11);
//   - a build defaults to MOCKED side effects and only goes real on an explicit
//     opt-in (ST-12).

// sseDoneFrame runs an SSE endpoint through the test app and returns the parsed
// payload of the terminating `event: done` frame.
func sseDoneFrame(t *testing.T, s *Server, path, body string) map[string]any {
	t.Helper()
	status, raw := gatewayRaw(t, s, http.MethodPost, path, "k", body)
	if status != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", path, status, raw)
	}
	var done string
	for _, block := range strings.Split(raw, "\n\n") {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		isDone := false
		data := ""
		for _, ln := range lines {
			switch {
			case strings.HasPrefix(ln, "event: "):
				isDone = strings.TrimSpace(strings.TrimPrefix(ln, "event: ")) == "done"
			case strings.HasPrefix(ln, "data: "):
				data = strings.TrimPrefix(ln, "data: ")
			}
		}
		if isDone && data != "" {
			done = data
		}
	}
	if done == "" {
		t.Fatalf("%s emitted no done frame; raw stream:\n%s", path, raw)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(done), &out); err != nil {
		t.Fatalf("decode done frame %q: %v", done, err)
	}
	return out
}

// ── ST-05: the streamed generate path must carry the contract gate ──────────

// The GUI's post-generation blocker gate keys off result.compile.contract. When
// the streamed path left it nil, a draft with execution blockers landed on the
// canvas dressed up as a clean success. The done frame must now carry a
// populated contract, an explicit `blocked` flag, and still preserve the draft.
func TestStudioGenerateStream_CarriesContractAndBlockedFlag(t *testing.T) {
	s, fake := studioFake(t)
	fake.content = `{"refined_intent":"Every morning read the local sales file and send the summary to telegram","summary":"daily sales digest","assumptions":[],"questions":[]}`

	body := `{"intent":"every morning read /tmp/sales.csv, summarize it and send it to telegram","auto_repair":true}`
	done := sseDoneFrame(t, s, "/api/v1/studio/generate/stream", body)

	result, _ := done["result"].(map[string]any)
	if result == nil {
		t.Fatalf("done frame must preserve the (possibly partial) pipeline result: %v", done)
	}
	compile, _ := result["compile"].(map[string]any)
	if compile == nil {
		t.Fatalf("done frame must preserve the partial draft under result.compile: %v", result)
	}
	// THE regression: the sync path populated Result.Contract, the streamed path
	// did not, so the GUI's gate never fired.
	contract, ok := compile["contract"].(map[string]any)
	if !ok {
		t.Fatalf("streamed result.compile must carry the same contract the sync path attaches: %v", compile)
	}
	if _, ok := contract["score"].(float64); !ok {
		t.Fatalf("contract must be a real assessment (score present): %v", contract)
	}
	blockedRaw, ok := done["blocked"].(bool)
	if !ok {
		t.Fatalf("done frame must carry an explicit `blocked` flag: %v", done)
	}
	blockers, _ := contract["blockers"].(float64)
	if blockedRaw != (blockers > 0) {
		t.Fatalf("blocked=%v disagrees with contract.blockers=%v", blockedRaw, blockers)
	}
	if blockedRaw {
		if reason, _ := done["blocked_reason"].(string); strings.TrimSpace(reason) == "" {
			t.Errorf("a blocked draft must explain why: %v", done)
		}
		// The partial draft must survive the failure — the user needs to see and
		// fix it, not lose it.
		if _, ok := compile["workflow"]; !ok {
			t.Errorf("a blocked result must still carry the partial draft: %v", compile)
		}
	}
}

// ── ST-07 / ST-08 / ST-11: Run Live must be gated ───────────────────────────

const blockedFlowDraft = `{"name":"Broken Sender","trigger":{"type":"manual"},"flow":{
  "entry":"nope",
  "nodes":[{"id":"send","kind":"tool","tool":"channel.send","input":"{}","output":"sent"}]}}`

// Run Live must not start a workflow the contract already says cannot execute.
// Previously the run started anyway and turned a precise blocker list into an
// opaque runtime error.
func TestStudioTryAgent_RejectsContractBlockers(t *testing.T) {
	s, _ := studioFake(t)
	body := `{"workflow":` + blockedFlowDraft + `,"question":"go"}`

	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/try-agent", "k", body)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%v", status, out)
	}
	contract, _ := out["contract"].(map[string]any)
	if contract == nil {
		t.Fatalf("422 must carry the blocker list: %v", out)
	}
	if blockers, _ := contract["blockers"].(float64); blockers <= 0 {
		t.Fatalf("422 must be justified by real blockers: %v", contract)
	}
	// Nothing may have been registered — the run never started.
	for _, d := range s.loader.All() {
		if d != nil && strings.HasPrefix(d.ID, "studio-try-") {
			t.Fatalf("blocked draft still registered an ephemeral agent: %s", d.ID)
		}
	}
}

const sendingAgentDraft = `{"name":"Notifier","strategy":"react","system_prompt":"deliver short notifications to the operator's channel",` +
	`"trigger":{"type":"channel"},"channels":["http"],"tools":["channel.send"]}`

// A draft that can send on a channel causes REAL, externally visible effects.
// Run Live must refuse until the caller acknowledges them, and the refusal must
// carry the structured preview the GUI renders as a confirmation dialog.
func TestStudioTryAgent_RequiresSideEffectAcknowledgement(t *testing.T) {
	s, fake := studioFake(t)
	fake.content = `{"thought":"done","is_done":true,"final_answer":"sent"}`

	body := `{"workflow":` + sendingAgentDraft + `,"question":"notify me"}`
	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/try-agent", "k", body)
	if status != http.StatusConflict {
		t.Fatalf("status=%d body=%v", status, out)
	}
	if req, _ := out["requires_confirmation"].(bool); !req {
		t.Errorf("409 must flag that confirmation is required: %v", out)
	}
	unack, _ := out["unacknowledged_tools"].([]any)
	if len(unack) == 0 || unack[0] != "channel.send" {
		t.Errorf("409 must name the unacknowledged tools: %v", out["unacknowledged_tools"])
	}
	preview, _ := out["preview"].(map[string]any)
	if preview == nil {
		t.Fatalf("409 must carry a structured preview: %v", out)
	}
	summary, _ := preview["summary"].(map[string]any)
	if summary == nil {
		t.Fatalf("preview must include the SecuritySummary: %v", preview)
	}
	for _, key := range []string{"network_tools", "file_tools", "channel_tools", "privileged_tools", "confirm_tools", "untrusted_content_sources"} {
		if _, ok := summary[key]; !ok {
			t.Errorf("preview summary missing %q: %v", key, summary)
		}
	}
	for _, d := range s.loader.All() {
		if d != nil && strings.HasPrefix(d.ID, "studio-try-") {
			t.Fatalf("unacknowledged run still registered an ephemeral agent: %s", d.ID)
		}
	}
}

// A per-tool acknowledgement that covers the whole preview proceeds — and the
// resulting throwaway definition must NOT be unattended: guardrail
// confirmations are enforced (and fail fast), not auto-approved.
func TestStudioTryAgent_ProceedsWhenAcknowledgedAndIsNotUnattended(t *testing.T) {
	s, fake := studioFake(t)
	fake.content = `{"thought":"answer directly","is_done":true,"final_answer":"notification sent"}`

	body := `{"workflow":` + sendingAgentDraft + `,"question":"notify me","acknowledged_tools":["channel.send"]}`
	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/try-agent", "k", body)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	unattended, ok := out["unattended"].(bool)
	if !ok {
		t.Fatalf("response must report the run's unattended posture: %v", out)
	}
	if unattended {
		t.Errorf("Run Live must not force Unattended — that auto-approves guardrail confirmations the operator never saw")
	}
	if _, ok := out["reply"].(string); !ok {
		t.Errorf("an acknowledged run must actually run: %v", out)
	}
}

// The blanket acknowledgement also works, and is what grants privileged
// exposure to ToAgentDefinition.
func TestStudioTryAgent_BlanketConfirmProceeds(t *testing.T) {
	s, fake := studioFake(t)
	fake.content = `{"thought":"answer directly","is_done":true,"final_answer":"ok"}`

	body := `{"workflow":` + sendingAgentDraft + `,"question":"notify me","confirm_side_effects":true}`
	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/try-agent", "k", body)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
}

// run-preview answers "what would this do?" without running anything: no
// ephemeral agent, no model call.
func TestStudioRunPreview_ReturnsSummaryWithoutRunning(t *testing.T) {
	s, fake := studioFake(t)
	fake.content = `should never be used`

	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/run-preview", "k", `{"workflow":`+sendingAgentDraft+`}`)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	if req, _ := out["requires_confirmation"].(bool); !req {
		t.Errorf("preview must report that this draft needs confirmation: %v", out)
	}
	tools, _ := out["side_effect_tools"].([]any)
	if len(tools) != 1 || tools[0] != "channel.send" {
		t.Errorf("preview must name the side-effecting tools, got %v", out["side_effect_tools"])
	}
	if _, ok := out["contract"].(map[string]any); !ok {
		t.Errorf("preview must carry the contract verdict: %v", out)
	}
	if got := fake.lastPrompt(); got != "" {
		t.Errorf("run-preview must not call the model, got prompt %.80q", got)
	}
	for _, d := range s.loader.All() {
		if d != nil && strings.HasPrefix(d.ID, "studio-try-") {
			t.Fatalf("run-preview executed something: %s", d.ID)
		}
	}
}

// ── ST-12: builds default to mocked side effects ────────────────────────────

const buildableDraft = `{"name":"Digest","trigger":{"type":"manual"},"flow":{
  "entry":"send",
  "nodes":[{"id":"send","kind":"tool","tool":"channel.send","input":"{\"channel\":\"http\",\"text\":\"hi\"}","output":"sent"}],
  "edges":[]}}`

// A build request that says nothing about side effects must run MOCKED. The old
// default was real execution, so a half-built agent could fire production tools
// once per repair attempt.
func TestStudioBuildStream_DefaultsToMockedSideEffects(t *testing.T) {
	s, fake := studioFake(t)
	fake.content = `{}`

	done := sseDoneFrame(t, s, "/api/v1/studio/build/stream", `{"workflow":`+buildableDraft+`,"intent":"send a digest"}`)
	if got := done["side_effects"]; got != "mocked" {
		t.Fatalf("build with no side-effect field must be mocked, got %v (done=%v)", got, done)
	}
	report, _ := done["report"].(map[string]any)
	if report == nil {
		t.Fatalf("done frame must carry the report: %v", done)
	}
	if got := report["side_effects"]; got != "mocked" {
		t.Errorf("report must record the policy it actually ran under, got %v", got)
	}
	// Budget/outcome fields the GUI needs to distinguish "out of time" from
	// "couldn't fix it".
	for _, key := range []string{"stopped_reason", "elapsed_ms", "tokens_used", "cost_usd"} {
		if _, ok := done[key]; !ok {
			t.Errorf("done frame missing %q: %v", key, done)
		}
	}
}

// verify:false stays mocked (unchanged meaning for existing clients).
func TestStudioBuildStream_LegacyVerifyFalseIsMocked(t *testing.T) {
	s, fake := studioFake(t)
	fake.content = `{}`

	done := sseDoneFrame(t, s, "/api/v1/studio/build/stream", `{"workflow":`+buildableDraft+`,"verify":false}`)
	if got := done["side_effects"]; got != "mocked" {
		t.Fatalf("verify:false must be mocked, got %v", got)
	}
}

// The explicit wire field selects real execution, and wins over verify.
func TestStudioBuildStream_ExplicitRealSelectsReal(t *testing.T) {
	s, fake := studioFake(t)
	fake.content = `{}`

	done := sseDoneFrame(t, s, "/api/v1/studio/build/stream", `{"workflow":`+buildableDraft+`,"side_effects":"real","verify":false}`)
	if got := done["side_effects"]; got != "real" {
		t.Fatalf(`side_effects:"real" must win over verify:false, got %v`, got)
	}
}

// The synchronous /studio/build endpoint shares the same default.
func TestStudioBuild_SyncDefaultsToMockedSideEffects(t *testing.T) {
	s, fake := studioFake(t)
	fake.content = `{}`

	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/build", "k", `{"workflow":`+buildableDraft+`}`)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	if got := out["side_effects"]; got != "mocked" {
		t.Fatalf("sync build must default to mocked, got %v", got)
	}
}
