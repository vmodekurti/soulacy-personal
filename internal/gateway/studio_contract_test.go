package gateway

import (
	"net/http"
	"testing"

	"github.com/soulacy/soulacy/internal/studio"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestStudioContractEndpointReportsAuthoringRules(t *testing.T) {
	s, _ := studioFake(t)
	body := `{"workflow":{"name":"Brittle Flow","trigger":{"type":"manual"},"flow":{
	  "entry":"summarize",
	  "nodes":[
	    {"id":"summarize","kind":"agent","agent":"summarizer","output":"reply"},
	    {"id":"store","kind":"tool","tool":"kb_write","input":"please store it","output":"stored"}],
	  "edges":[{"from":"summarize","to":"store"}]}}}`

	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/contract", "k", body)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	if _, ok := out["score"].(float64); !ok {
		t.Fatalf("contract response should include score: %v", out)
	}
	checks, _ := out["checks"].([]any)
	if len(checks) == 0 {
		t.Fatalf("contract response should include checks: %v", out)
	}
	found := false
	for _, raw := range checks {
		check, _ := raw.(map[string]any)
		if check["id"] == "data.contracts" && check["status"] == "warn" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected data.contracts warning in %v", checks)
	}
}

func TestStudioSaveRejectsContractBlockersBeforeCreatingAgent(t *testing.T) {
	s, _ := studioFake(t)
	before := len(s.loader.All())
	body := `{"workflow":{"name":"Born Broken","trigger":{"type":"manual"},"flow":{
	  "entry":"missing",
	  "nodes":[]}}}`

	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/save", "k", body)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%v", status, out)
	}
	if _, ok := out["contract"].(map[string]any); !ok {
		t.Fatalf("save failure should include contract details: %v", out)
	}
	if _, ok := out["preflight"].(map[string]any); !ok {
		t.Fatalf("save failure should include preflight details: %v", out)
	}
	if after := len(s.loader.All()); after != before {
		t.Fatalf("invalid Studio save created an agent: before=%d after=%d", before, after)
	}
}

func TestStudioSaveRepairsParallelJoinBeforePersisting(t *testing.T) {
	s := saveGateway(t)
	body := `{"workflow":{"id":"weekday-market-digest","name":"Weekday Market Digest","trigger":{"type":"manual"},"llm":{"provider":"openai","model":"gpt-4o-mini"},"flow":{
	  "entry":"gather_data",
	  "nodes":[
	    {"id":"gather_data","kind":"agent","agent":"data_gatherer","output":"data"},
	    {"id":"fan_out_specialists","kind":"parallel","join":"all"},
	    {"id":"fundamentals_analyst","kind":"agent","agent":"fundamentals_analyst","output":"fundamentals_analysis"},
	    {"id":"risk_analyst","kind":"agent","agent":"risk_analyst","output":"risk_analysis"},
	    {"id":"sentiment_analyst","kind":"agent","agent":"sentiment_analyst","output":"sentiment_analysis"},
	    {"id":"editor","kind":"agent","agent":"editor","output":"briefing"}],
	  "edges":[
	    {"from":"gather_data","to":"fan_out_specialists"},
	    {"from":"fan_out_specialists","to":"fundamentals_analyst"},
	    {"from":"fan_out_specialists","to":"risk_analyst"},
	    {"from":"fan_out_specialists","to":"sentiment_analyst"},
	    {"from":"fundamentals_analyst","to":"editor"},
	    {"from":"risk_analyst","to":"editor"},
	    {"from":"sentiment_analyst","to":"editor"}]}}}`

	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/save", "k", body)
	if status != http.StatusCreated {
		t.Fatalf("status=%d body=%v", status, out)
	}
	got := s.loader.Get("weekday-market-digest")
	if got == nil || got.Workflow == nil {
		t.Fatal("saved workflow is missing")
	}
	for _, node := range got.Workflow.Nodes {
		if node.ID == "fan_out_specialists" {
			if node.JoinNode != "editor" {
				t.Fatalf("persisted join_node=%q, want editor", node.JoinNode)
			}
			return
		}
	}
	t.Fatal("saved workflow is missing fan_out_specialists")
}

func TestFinalizeStudioResultRepairsParallelJoin(t *testing.T) {
	s := &Server{}
	res := &studio.Result{Workflow: studio.Draft{
		Name: "Weekday Market Digest",
		Flow: studio.Flow{
			Entry: "fan",
			Nodes: []sdkr.FlowNode{
				{ID: "fan", Kind: sdkr.FlowNodeParallel, Join: "all"},
				{ID: "left", Kind: sdkr.FlowNodeLLM},
				{ID: "right", Kind: sdkr.FlowNodeLLM},
				{ID: "editor", Kind: sdkr.FlowNodeLLM},
			},
			Edges: []sdkr.FlowEdge{
				{From: "fan", To: "left"}, {From: "fan", To: "right"},
				{From: "left", To: "editor"}, {From: "right", To: "editor"},
			},
		},
	}}

	s.finalizeStudioResult(res, studio.Catalog{}, studio.PreflightInput{})
	if got := res.Workflow.Flow.Nodes[0].JoinNode; got != "editor" {
		t.Fatalf("finalized join_node=%q, want editor", got)
	}
}
