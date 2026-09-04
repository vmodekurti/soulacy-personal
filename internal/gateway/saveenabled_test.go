package gateway

// Saving an edit must not switch off an agent the operator turned on.
//
// The Studio panel says, in its own words: "New agents are always saved
// disabled so you review and deploy them explicitly." The handler applied that
// to every save. So fixing a typo in a running daily digest ended the digest,
// and the only evidence was a briefing that stopped arriving — the save
// returned 201, the canvas looked right, and the agent list showed "disabled"
// for a reason nobody had chosen.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

const saveWorkflowBody = `{"workflow":{
  "id":"%ID%","name":"Nightly Digest",
  "trigger":{"type":"schedule","config":{"cron":"0 7 * * 1-5"}},
  "llm":{"provider":"openai","model":"gpt-4o-mini"},
  "flow":{"entry":"step","nodes":[
    {"id":"step","kind":"llm","input":"Summarise the day.","output":"content"}],
   "edges":[]}}}`

// saveGateway is a gateway whose provider actually resolves, so a save is
// judged on the thing under test rather than on an unconfigured model.
func saveGateway(t *testing.T) *Server {
	t.Helper()
	s, _ := studioFake(t)
	s.cfg.LLM.DefaultProvider = "openai"
	return s
}

func saveDraftAs(t *testing.T, s *Server, id string) (int, map[string]any) {
	t.Helper()
	return gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/save", "k",
		strings.ReplaceAll(saveWorkflowBody, "%ID%", id))
}

// seedAgent puts an already-deployed agent in the loader, as if the operator
// had reviewed and enabled it earlier.
func seedDeployedAgent(t *testing.T, s *Server, id string, enabled bool) {
	t.Helper()
	def := &agent.Definition{
		ID: id, Name: "Nightly Digest", Enabled: enabled,
		Trigger:  agent.TriggerCron,
		Schedule: &agent.Schedule{Cron: "0 7 * * 1-5"},
		LLM:      agent.LLMConfig{Provider: "openai", Model: "gpt-4o-mini"},
		Workflow: &agent.WorkflowSpec{
			Entry: "step",
			Nodes: []sdkr.FlowNode{{ID: "step", Kind: sdkr.FlowNodeLLM, Input: "x", Output: "content"}},
		},
	}
	dir := ""
	if len(s.cfg.AgentDirs) > 0 {
		dir = s.cfg.AgentDirs[0]
	}
	if err := s.loader.Upsert(dir, def); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestStudioSave_LeavesARunningAgentRunning(t *testing.T) {
	s := saveGateway(t)
	seedDeployedAgent(t, s, "nightly-digest", true)

	status, out := saveDraftAs(t, s, "nightly-digest")
	if status != http.StatusCreated {
		t.Fatalf("save status=%d error=%v", status, out["error"])
	}
	if got := s.loader.Get("nightly-digest"); got == nil || !got.Enabled {
		t.Fatal("editing a live scheduled agent switched it off with no announcement")
	}
	if enabled, ok := out["enabled"].(bool); ok && !enabled {
		t.Error("the response told the caller the agent is disabled while it is running")
	}
}

// A brand-new agent is still staged for review. That half of the rule was
// right, and it is the half the UI copy actually describes.
func TestStudioSave_StagesANewAgentDisabled(t *testing.T) {
	s := saveGateway(t)

	status, out := saveDraftAs(t, s, "brand-new-digest")
	if status != http.StatusCreated {
		t.Fatalf("save status=%d error=%v", status, out["error"])
	}
	if got := s.loader.Get("brand-new-digest"); got == nil || got.Enabled {
		t.Fatal("a never-reviewed agent was saved ready to run")
	}
}

// "Preserve what it was" has to work in the inconvenient direction too: an
// agent the operator paused must not be switched back on by an unrelated edit.
func TestStudioSave_LeavesAPausedAgentPaused(t *testing.T) {
	s := saveGateway(t)
	seedDeployedAgent(t, s, "paused-digest", false)

	if status, out := saveDraftAs(t, s, "paused-digest"); status != http.StatusCreated {
		t.Fatalf("save status=%d error=%v", status, out["error"])
	}
	if got := s.loader.Get("paused-digest"); got == nil || got.Enabled {
		t.Fatal("a paused agent was switched back on by an unrelated edit")
	}
}
