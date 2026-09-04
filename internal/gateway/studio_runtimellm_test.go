package gateway

// Which model a generated agent RUNS on.
//
// Two different models are in play in Studio and they are easy to conflate: the
// builder model (llm.studio) that writes the agent, and the runtime model the
// saved agent executes on. Putting the first into SOUL.yaml produces an agent
// that dies on a model the operator never chose, which is why stampDefaultLLM
// exists. It was wired into the two synchronous generate handlers and missed on
// the streamed one — the path the UI uses when "Streamed" is on.
//
// The save gate had the mirror-image problem: it judged the raw draft, so a
// draft that legitimately inherits the workspace default was refused with "does
// not specify a model to run on" — even though Run Live, which resolves the
// default first, called the same draft runnable.

import (
	"net/http"
	"strings"
	"testing"
)

// A draft with no llm block is legal: the runtime resolves the workspace
// default. The save gate must resolve it the same way instead of blocking.
func TestStudioSave_InheritsWorkspaceModelInsteadOfBlocking(t *testing.T) {
	s, _ := studioFake(t)
	s.cfg.LLM.DefaultProvider = "openai"

	// No "llm" key at all — exactly what a generated draft carries today.
	body := `{"workflow":{"name":"Inheritor","trigger":{"type":"manual"},
	  "new_agents":[{"id":"summarizer","name":"Summarizer","description":"Summarises","system_prompt":"You are Summarizer. Turn structured input into a short, friendly answer. If the input is empty, say so plainly."}],
	  "flow":{"entry":"step","nodes":[
	    {"id":"step","kind":"agent","agent":"summarizer","input":"Summarise: {{ .trigger.text }}","output":"response"}],
	  "edges":[]}}}`

	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/save", "k", body)
	if status == http.StatusUnprocessableEntity {
		t.Fatalf("save was blocked for a draft the runtime could have run: %v", out["error"])
	}
	if status != http.StatusCreated {
		t.Fatalf("save status=%d body=%v", status, out)
	}

	saved := s.loader.Get("inheritor")
	if saved == nil {
		t.Fatalf("agent was not saved; agents: %v", agentIDs(s))
	}
	// Resolved, not left blank: the YAML should say what it runs on rather than
	// silently depending on a workspace default that can change under it.
	if saved.LLM.Provider != "openai" {
		t.Fatalf("saved agent did not record the resolved provider, got %q", saved.LLM.Provider)
	}
}

// A model the workspace genuinely cannot serve is still a blocker — resolving
// the default must not turn the gate off.
func TestStudioSave_StillBlocksAnUnservableModel(t *testing.T) {
	s, _ := studioFake(t)
	s.cfg.LLM.DefaultProvider = "openai"

	body := `{"workflow":{"name":"Ghost Model","trigger":{"type":"manual"},
	  "llm":{"provider":"openai","model":"definitely-not-a-real-model"},
	  "new_agents":[{"id":"summarizer","name":"Summarizer","description":"Summarises","system_prompt":"You are Summarizer. Turn structured input into a short, friendly answer. If the input is empty, say so plainly."}],
	  "flow":{"entry":"step","nodes":[
	    {"id":"step","kind":"agent","agent":"summarizer","input":"Summarise: {{ .trigger.text }}","output":"response"}],
	  "edges":[]}}}`

	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/save", "k", body)
	if status != http.StatusUnprocessableEntity {
		t.Skipf("this workspace cannot enumerate models, so the check is silent by design (status=%d)", status)
	}
	if msg, _ := out["error"].(string); !strings.Contains(strings.ToLower(msg), "block") {
		t.Fatalf("expected a blocking contract summary, got %v", out["error"])
	}
}

// The streamed generate path must pin the RUNTIME provider/model, exactly as
// the two synchronous generate handlers do. It didn't, so a draft generated
// with "Streamed" on reached the canvas — and SOUL.yaml — naming whatever the
// generator happened to leave in `llm`.
func TestStudioGenerateStream_StampsRuntimeProviderNotBuilder(t *testing.T) {
	s, fake := studioFake(t)
	// The builder runs on a DIFFERENT provider/model than the workspace default.
	// If the builder's choice leaks, this is where it shows up.
	s.cfg.LLM.DefaultProvider = "openai"
	s.cfg.LLM.Studio.Provider = "ollama-cloud"
	s.cfg.LLM.Studio.Model = "glm-5.2"
	fake.content = `{"refined_intent":"Summarise the daily sales file","summary":"daily sales digest","assumptions":[],"questions":[]}`

	done := sseDoneFrame(t, s, "/api/v1/studio/generate/stream",
		`{"intent":"summarise /tmp/sales.csv every morning","auto_repair":true}`)

	result, _ := done["result"].(map[string]any)
	compile, _ := result["compile"].(map[string]any)
	workflow, _ := compile["workflow"].(map[string]any)
	if workflow == nil {
		t.Fatalf("streamed done frame carries no draft: %v", compile)
	}
	llm, _ := workflow["llm"].(map[string]any)
	if llm == nil {
		t.Fatalf("streamed draft has no llm block, so the saved YAML would name no model: %v", workflow)
	}
	if got, _ := llm["provider"].(string); got != "openai" {
		t.Fatalf("streamed draft should run on the workspace provider %q, got %q", "openai", got)
	}
	if got, _ := llm["model"].(string); got == "glm-5.2" {
		t.Fatal("the BUILDER model leaked into the draft — the agent would run on a model the operator never chose")
	}
}
