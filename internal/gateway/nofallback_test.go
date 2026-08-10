package gateway

// When there is nothing to fall back to, the model's graph is the answer.
//
// studioDesignGraph is "model first, contract-checked, deterministic planner as
// the floor". The floor is not always there: the curated templates are all
// straight lines, so they decline an intent that spells out a fan-out (see
// studio.StructureNamedButUnbuildable). With no floor beneath it, a model graph
// that still carried blockers after repair fell out of every branch and the
// handler answered:
//
//	could not build this workflow; describe the source, transform, and
//	delivery steps more explicitly
//
// Untrue — a graph had been built — and unactionable, since it names nothing to
// change. Meanwhile the blockers it was hiding are the specific, listed,
// fixable things the UI renders next to a Save button they already gate.
//
// Observed live on v0.1.4-29-g4f6c217, on the first fan-out request after the
// templates started declining them.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/soulacy/soulacy/internal/llm"
)

// blockedGraphProvider always answers with a fan-out graph whose delivery step
// is inline Python with no run() entry point — a real blocker, of the kind a
// weak builder model actually produces, and one structural repair cannot clear
// because only the author knows what the code was meant to do.
type blockedGraphProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *blockedGraphProvider) ID() string { return "openai" }
func (p *blockedGraphProvider) Models(context.Context) ([]string, error) {
	return []string{"fake-model"}, nil
}
func (p *blockedGraphProvider) Complete(_ context.Context, _ llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return &llm.CompletionResponse{Content: blockedFanOutJSON}, nil
}

const blockedFanOutJSON = `{"name":"Incident Digest","trigger":{"type":"schedule","config":{"cron":"0 8 * * 1-5"}},
 "llm":{"provider":"openai","model":"gpt-4o-mini"},
 "new_agents":[
   {"id":"severity_reviewer","name":"Severity Reviewer","description":"Ranks impact","system_prompt":"You are the Severity Reviewer. Rank incident impact. If the input is empty, say so plainly."},
   {"id":"root_cause_reviewer","name":"Root Cause Reviewer","description":"Groups by cause","system_prompt":"You are the Root Cause Reviewer. Group incidents by likely cause. If the input is empty, say so plainly."},
   {"id":"impact_reviewer","name":"Customer Impact Reviewer","description":"Counts accounts","system_prompt":"You are the Customer Impact Reviewer. Count affected accounts. If the input is empty, say so plainly."},
   {"id":"digest_editor","name":"Digest Editor","description":"Writes the digest","system_prompt":"You are the Digest Editor. Combine the three reviews into one incident digest. If the input is empty, say so plainly."}],
 "flow":{"entry":"pull","nodes":[
   {"id":"pull","kind":"llm","input":"Pull the latest incident reports.","output":"incidents"},
   {"id":"fan","kind":"parallel","join":"all","join_node":"write","output":"reviews"},
   {"id":"severity","kind":"agent","agent":"severity_reviewer","input":"Rank {{ .incidents }}","output":"severity"},
   {"id":"cause","kind":"agent","agent":"root_cause_reviewer","input":"Group {{ .incidents }}","output":"cause"},
   {"id":"impact","kind":"agent","agent":"impact_reviewer","input":"Count {{ .incidents }}","output":"impact"},
   {"id":"write","kind":"agent","agent":"digest_editor","input":"Combine {{ .severity }} {{ .cause }} {{ .impact }}","output":"content"},
   {"id":"deliver","kind":"python","code":"print('sending')","input":"{{ .content }}","output":"sent"}],
  "edges":[
   {"from":"pull","to":"fan"},
   {"from":"fan","to":"severity"},{"from":"fan","to":"cause"},{"from":"fan","to":"impact"},
   {"from":"severity","to":"write"},{"from":"cause","to":"write"},{"from":"impact","to":"write"},
   {"from":"write","to":"deliver"}]}}`

// The prompt that produced the 422, verbatim.
const incidentFanOutIntent = "Every weekday morning pull the latest incident reports from our support queue, " +
	"then run three reviewers in parallel over that material: a severity reviewer who ranks impact, " +
	"a root-cause reviewer who groups them by likely cause, and a customer-impact reviewer who counts " +
	"affected accounts. Finally an editor combines all three reviews into one incident digest and posts " +
	"it to Telegram."

func compileIncidentFanOut(t *testing.T) (int, map[string]any) {
	t.Helper()
	s, _ := newTestGatewayWithLLM(t, "k")
	s.llmRouter.Register(&blockedGraphProvider{})
	s.cfg.LLM.DefaultProvider = "openai"

	body := `{"intent":` + jsonString(incidentFanOutIntent) + `,"force_workflow":true,"catalog":{}}`
	return gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/compile", "k", body)
}

func TestStudioCompile_ReturnsAGraphWithBlockersRatherThanNothing(t *testing.T) {
	status, out := compileIncidentFanOut(t)

	if status != http.StatusOK {
		t.Fatalf("a built graph was thrown away and the caller got %d: %v", status, out["error"])
	}
	wf, _ := out["workflow"].(map[string]any)
	flow, _ := wf["flow"].(map[string]any)
	nodes, _ := flow["nodes"].([]any)
	if len(nodes) < 5 {
		t.Fatalf("the returned graph is not the one the model built (%d nodes)", len(nodes))
	}
}

// The blockers are the point. Returning the graph while hiding what is wrong
// with it would be worse than the 422 — the user would try to save something
// that cannot run and be refused with no explanation.
func TestStudioCompile_StillReportsTheBlockersOnTheGraphItKept(t *testing.T) {
	_, out := compileIncidentFanOut(t)

	contract, _ := out["contract"].(map[string]any)
	if contract == nil {
		t.Fatal("no contract on the response — the UI has nothing to list")
	}
	blockers, _ := contract["blockers"].(float64)
	if blockers < 1 {
		t.Errorf("the graph was returned as clean; its run()-less python step should block (blockers=%v)", blockers)
	}
}

// And the user must be told why they are looking at a blocked graph instead of
// a clean one, or "kept as built" reads as "this is fine".
func TestStudioCompile_SaysWhyTheBlockedGraphWasKept(t *testing.T) {
	_, out := compileIncidentFanOut(t)

	notes, _ := out["notes"].([]any)
	joined := ""
	for _, n := range notes {
		joined += str(n) + "\n"
	}
	// Specific, not just the word "blocker": the sibling note ("kept because the
	// deterministic alternative carries N blockers of its own") also contains it,
	// and comparing against an EMPTY draft is not a reason to keep anything.
	if !strings.Contains(joined, "no curated alternative for this shape") {
		t.Errorf("the note does not say why this graph was kept: %q", joined)
	}
}

// The model returning nothing at all is a different case from the model
// returning something flawed, and it must not end in an error that names
// nothing to change.
func TestStudioCompile_FallsBackToATemplateWhenTheModelReturnsNothing(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "k")
	s.llmRouter.Register(&failingProvider{})
	s.cfg.LLM.DefaultProvider = "openai"

	body := `{"intent":` + jsonString(incidentFanOutIntent) + `,"force_workflow":true,"catalog":{}}`
	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/compile", "k", body)
	if status != http.StatusOK {
		t.Fatalf("the user was told to reword a perfectly explicit request: %d %v", status, out["error"])
	}

	notes, _ := out["notes"].([]any)
	joined := ""
	for _, n := range notes {
		joined += str(n) + "\n"
	}
	// A template that does the wrong shape, presented as if it were the right
	// one, is the failure this whole change is about.
	if !strings.Contains(joined, "does NOT contain the parallel specialists you described") {
		t.Errorf("a straight-line template was handed over without saying what it is missing: %q", joined)
	}
}

// failingProvider never returns a graph.
type failingProvider struct{}

func (p *failingProvider) ID() string { return "openai" }
func (p *failingProvider) Models(context.Context) ([]string, error) {
	return []string{"fake-model"}, nil
}
func (p *failingProvider) Complete(context.Context, llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return nil, errors.New("provider unavailable")
}
