package gateway

// The structure retry, end to end through the path the Workflow button uses.
//
// The UI's Workflow switch calls /studio/compile with force_workflow, not the
// streamed pipeline — so this is where a collapsed fan-out actually has to be
// caught and rebuilt.
//
// This test exists because the live demonstration could not be staged. Once the
// fix shipped, the builder model produced the fan-out on every attempt: twice on
// glm-5.2 and once on a deliberately weakened local model (ollama/gemma4). There
// was no bad roll left to watch the retry rescue. A scripted provider removes
// the luck — its first graph is the exact two-node draft observed live, and it
// only produces the fan-out once the correction names what it collapsed.

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/soulacy/soulacy/internal/llm"
)

// collapsingProvider answers with a two-node graph until it is told what it got
// wrong. Keyed on the correction text rather than call order, so it cannot pass
// by accident if the pipeline happens to call it a different number of times.
type collapsingProvider struct {
	mu        sync.Mutex
	prompts   []string
	corrected bool
}

func (p *collapsingProvider) ID() string { return "openai" }
func (p *collapsingProvider) Models(context.Context) ([]string, error) {
	return []string{"fake-model"}, nil
}

func (p *collapsingProvider) Complete(_ context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	prompt := ""
	if n := len(req.Messages); n > 0 {
		prompt = req.Messages[n-1].Content
	}
	p.mu.Lock()
	p.prompts = append(p.prompts, prompt)
	corrected := strings.Contains(prompt, "COLLAPSED THE STRUCTURE")
	if corrected {
		p.corrected = true
	}
	p.mu.Unlock()

	if corrected {
		return &llm.CompletionResponse{Content: fannedGraphJSON}, nil
	}
	return &llm.CompletionResponse{Content: collapsedGraphJSON}, nil
}

const collapsedGraphJSON = `{"name":"Supplier Scorecard Workflow","trigger":{"type":"manual"},
 "llm":{"provider":"openai","model":"gpt-4o-mini"},
 "new_agents":[{"id":"scorecard_writer","name":"Scorecard Writer","description":"Writes the scorecard","system_prompt":"You are the Scorecard Writer. Turn supplier data into a ranked scorecard. If the input is empty, say so plainly."}],
 "flow":{"entry":"retrieve_data","nodes":[
   {"id":"retrieve_data","kind":"llm","input":"Summarise the supplier delivery records.","output":"supplier_data"},
   {"id":"produce","kind":"agent","agent":"scorecard_writer","input":"Write the scorecard from {{ .supplier_data }}","output":"content"}],
  "edges":[{"from":"retrieve_data","to":"produce"}]}}`

const fannedGraphJSON = `{"name":"Supplier Scorecard Workflow","trigger":{"type":"manual"},
 "llm":{"provider":"openai","model":"gpt-4o-mini"},
 "new_agents":[
   {"id":"quality_reviewer","name":"Quality Reviewer","description":"Judges defect rates","system_prompt":"You are the Quality Reviewer. Judge defect rates in the supplier data. If the input is empty, say so plainly."},
   {"id":"cost_reviewer","name":"Cost Reviewer","description":"Checks invoiced price","system_prompt":"You are the Cost Reviewer. Check invoiced price against contract. If the input is empty, say so plainly."},
   {"id":"reliability_reviewer","name":"Reliability Reviewer","description":"Scores on-time delivery","system_prompt":"You are the Reliability Reviewer. Score on-time delivery. If the input is empty, say so plainly."},
   {"id":"scorecard_writer","name":"Scorecard Writer","description":"Writes the scorecard","system_prompt":"You are the Scorecard Writer. Combine the three reviews into one ranked scorecard. If the input is empty, say so plainly."}],
 "flow":{"entry":"retrieve_data","nodes":[
   {"id":"retrieve_data","kind":"llm","input":"Summarise the supplier delivery records.","output":"supplier_data"},
   {"id":"fan","kind":"parallel","output":"reviews"},
   {"id":"quality","kind":"agent","agent":"quality_reviewer","input":"Review {{ .supplier_data }}","output":"quality"},
   {"id":"cost","kind":"agent","agent":"cost_reviewer","input":"Review {{ .supplier_data }}","output":"cost"},
   {"id":"reliability","kind":"agent","agent":"reliability_reviewer","input":"Review {{ .supplier_data }}","output":"reliability"},
   {"id":"write","kind":"agent","agent":"scorecard_writer","input":"Combine {{ .quality }} {{ .cost }} {{ .reliability }}","output":"content"}],
  "edges":[
   {"from":"retrieve_data","to":"fan"},
   {"from":"fan","to":"quality"},{"from":"fan","to":"cost"},{"from":"fan","to":"reliability"},
   {"from":"quality","to":"write"},{"from":"cost","to":"write"},{"from":"reliability","to":"write"}]}}`

const fanOutIntent = "Pull yesterday's supplier delivery records, then run three reviewers in parallel " +
	"over that material: a quality reviewer who judges defect rates, a cost reviewer who checks invoiced " +
	"price, and a reliability reviewer who scores on-time delivery. Finally a scorecard writer combines " +
	"all three reviews into one supplier scorecard."

func compileWithProvider(t *testing.T, p *collapsingProvider) map[string]any {
	t.Helper()
	s, _ := newTestGatewayWithLLM(t, "k")
	s.llmRouter.Register(p)
	// Without a resolvable provider the runtime.model/runtime.provider checks
	// block every draft, and /compile then errors before the graph is judged.
	s.config().LLM.DefaultProvider = "openai"

	body := `{"intent":` + jsonString(fanOutIntent) + `,"force_workflow":true,"catalog":{}}`
	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/compile", "k", body)
	if status != http.StatusOK {
		t.Fatalf("compile status=%d body=%v", status, out)
	}
	return out
}

func jsonString(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

// graphOf pulls the node kinds out of the compile response.
func graphOf(t *testing.T, out map[string]any) (agents int, hasParallel bool, total int) {
	t.Helper()
	wf, _ := out["workflow"].(map[string]any)
	if wf == nil {
		t.Fatalf("no workflow in response: %v", out)
	}
	flow, _ := wf["flow"].(map[string]any)
	nodes, _ := flow["nodes"].([]any)
	for _, n := range nodes {
		m, _ := n.(map[string]any)
		switch strings.ToLower(str(m["kind"])) {
		case "agent":
			agents++
		case "parallel":
			hasParallel = true
		}
	}
	return agents, hasParallel, len(nodes)
}

func str(v any) string { s, _ := v.(string); return s }

func TestStudioCompile_RebuildsACollapsedFanOut(t *testing.T) {
	p := &collapsingProvider{}
	out := compileWithProvider(t, p)

	if !p.corrected {
		t.Fatal("the model was never told it had collapsed the structure — the retry did not fire, " +
			"so a two-node graph would have been handed to the user for a three-specialist request")
	}

	agents, hasParallel, total := graphOf(t, out)
	if !hasParallel {
		t.Errorf("the rescued graph has no parallel step (%d nodes, %d agents)", total, agents)
	}
	if agents < 4 {
		t.Errorf("expected the four-agent fan-out to be kept, got %d agent nodes across %d", agents, total)
	}
}

// The correction has to name the omission. Asking again with the same prompt is
// the version of this that changes nothing — a model that already read the brief
// once and flattened it will flatten it again.
func TestStudioCompile_CorrectionNamesWhatWasCollapsed(t *testing.T) {
	p := &collapsingProvider{}
	compileWithProvider(t, p)

	var correction string
	for _, pr := range p.prompts {
		if strings.Contains(pr, "COLLAPSED THE STRUCTURE") {
			correction = pr
			break
		}
	}
	if correction == "" {
		t.Fatal("no corrective prompt was ever sent")
	}
	for _, want := range []string{"quality reviewer", `kind "parallel"`, "Do NOT merge"} {
		if !strings.Contains(correction, want) {
			t.Errorf("the correction never mentions %q", want)
		}
	}
}

// A graph that already fans out must not cost a second model call.
type fanningProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *fanningProvider) ID() string { return "openai" }
func (p *fanningProvider) Models(context.Context) ([]string, error) {
	return []string{"fake-model"}, nil
}
func (p *fanningProvider) Complete(_ context.Context, _ llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return &llm.CompletionResponse{Content: fannedGraphJSON}, nil
}

func TestStudioCompile_DoesNotRetryAGraphThatAlreadyFansOut(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "k")
	p := &fanningProvider{}
	s.llmRouter.Register(p)
	s.config().LLM.DefaultProvider = "openai"

	body := `{"intent":` + jsonString(fanOutIntent) + `,"force_workflow":true,"catalog":{}}`
	if status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/compile", "k", body); status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	if p.calls > 1 {
		t.Errorf("spent %d model calls on a graph that already matched the request", p.calls)
	}
}
