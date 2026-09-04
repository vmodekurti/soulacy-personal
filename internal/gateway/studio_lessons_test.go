package gateway

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// lastPrompt returns the user content of the most recent completion request.
func (p *fakeLLMProvider) lastPrompt() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) == 0 {
		return ""
	}
	msgs := p.requests[len(p.requests)-1].Messages
	if len(msgs) == 0 {
		return ""
	}
	return msgs[len(msgs)-1].Content
}

// End-to-end learning loop: accepting a repair via /studio/apply-repair records
// a lesson, and a subsequent /studio/compile injects it into the generated
// agent prompt. Studio's default compiler is deterministic now, so the lesson
// no longer needs to pass through an architecture-builder LLM prompt.
func TestStudioLearningLoop_AcceptThenInject(t *testing.T) {
	// Point the lesson store at a temp file for this test.
	t.Setenv("SOULACY_STUDIO_LESSONS", filepath.Join(t.TempDir(), "lessons.json"))

	s, fake := studioFake(t)

	// 1) Accept a shape-drift repair on a web_search-fed node.
	applyBody := `{
	  "workflow": {"name":"News","intent":"news digest","trigger":{"type":"channel"},"flow":{
	    "nodes":[
	      {"id":"search","kind":"tool","tool":"web_search","output":"search"},
	      {"id":"fmt","kind":"agent","agent":"writer","input":"{{ toJson .search.items }}","output":"reply"}],
	    "edges":[{"from":"search","to":"fmt"},{"from":"fmt","to":"end"}],"entry":"search"}},
	  "proposal": {"node_id":"search","field":"input","class":"shape_drift",
	    "rationale":"The API response has no \"results\"; the list is under \"items\".",
	    "observed_keys":["items","meta"]}
	}`
	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/apply-repair", "k", applyBody)
	if status != http.StatusOK {
		t.Fatalf("apply status=%d body=%v", status, out)
	}

	// 2) Compile a new workflow that can use web_search → the lesson is injected
	//    into the prompt the builder model receives.
	fake.content = `{"name":"X","trigger":{"type":"channel"},"flow":{"nodes":[{"id":"a","kind":"agent","agent":"w","input":"{{ .trigger.text }}","output":"r"}],"edges":[{"from":"a","to":"end"}],"entry":"a"}}`
	compileBody := `{"intent":"summarize the news","catalog":{"tools":["web_search"]}}`
	status, out = gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/compile", "k", compileBody)
	if status != http.StatusOK {
		t.Fatalf("compile status=%d body=%v", status, out)
	}
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), "LESSONS FROM PAST RUNS") ||
		!strings.Contains(string(raw), "items") {
		t.Fatalf("compiled agent did not include the learned lesson:\n%s", string(raw))
	}
}
