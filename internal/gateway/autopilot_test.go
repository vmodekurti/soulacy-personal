package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/autopilot"
	"github.com/soulacy/soulacy/internal/costs"
	"github.com/soulacy/soulacy/pkg/agent"
)

func TestAutopilotEncodedProofAndProposalIDs(t *testing.T) {
	s, _ := newAutopilotGateway(t)
	status, reply := gatewayJSONWithHeader(t, s, "POST", "/api/v1/chat", "secret", `{"agent_id":"verified","session_id":"encoding","text":"work"}`, "Idempotency-Key", "encoded-proof")
	if status != 200 {
		t.Fatalf("run=%d %v", status, reply)
	}
	proofID := reply["run_id"].(string)
	status, proof := gatewayJSON(t, s, "GET", "/api/v1/autopilot/proofs/"+strings.ReplaceAll(url.PathEscape(proofID), ":", "%3A"), "secret", "")
	if status != 200 {
		t.Fatalf("encoded proof=%d %v", status, proof)
	}
	for _, id := range []string{"proposal:encoded", "proposal%3Aliteral"} {
		_, err := s.autopilotStore.CreateProposal(context.Background(), autopilot.ProposalDraft{
			ID: id, Subject: proof["subject"].(string), AgentID: "verified", SourceProofID: proofID,
			FailureSummary: "Encoding regression", CandidateCheck: agent.MissionCheck{ID: "content", Type: agent.MissionCheckOutputContains, Value: "sync reply"},
		})
		if err != nil {
			t.Fatal(err)
		}
		path := "/api/v1/autopilot/proposals/" + strings.ReplaceAll(url.PathEscape(id), ":", "%3A")
		body, _ := json.Marshal(map[string]string{"candidate_proof_id": proofID})
		status, result := gatewayJSON(t, s, "POST", path+"/verify", "secret", string(body))
		if status != 200 || result["id"] != id {
			t.Fatalf("encoded verification=%d %v", status, result)
		}
		status, result = gatewayJSON(t, s, "POST", path+"/reject", "secret", `{}`)
		if status != 200 || result["status"] != "rejected" {
			t.Fatalf("encoded decision=%d %v", status, result)
		}
	}
}

func newAutopilotGateway(t *testing.T) (*Server, *fakeLLMProvider) {
	t.Helper()
	s, p := newTestGatewayWithLLM(t, "secret")
	store, err := autopilot.NewStore(filepath.Join(t.TempDir(), "autopilot.db"))
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := costs.NewStore(filepath.Join(t.TempDir(), "costs.db"))
	if err != nil {
		t.Fatal(err)
	}
	s.llmRouter.SetController(costs.NewGovernor(ledger, costs.PriceTable{"test/fake-model": {InputPerMTok: 1, OutputPerMTok: 2}}, costs.GovernanceConfig{}))
	s.SetAutopilotStore(store)
	s.engine.SetAutopilot(store, nil)
	t.Cleanup(func() { s.closeAutopilot(); _ = store.Close(); _ = ledger.Close() })
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", `{"id":"verified","name":"Verified","enabled":true,"trigger":"channel","channels":["http"],"llm":{"provider":"test","model":"fake-model"},"system_prompt":"Return sync reply.","builtins":[],"mission":{"id":"test-mission","goal":"Return the expected text","acceptance":[{"id":"content","type":"output_contains","value":"sync reply"}],"limits":{"max_cost_usd":1,"max_duration":"30s"}}}`)
	if status != 201 {
		t.Fatalf("create agent=%d %v", status, body)
	}
	return s, p
}

func TestAutopilotReleaseLifecycleAPI(t *testing.T) {
	s, _ := newAutopilotGateway(t)
	status, _ := gatewayJSON(t, s, http.MethodGet, "/api/v1/autopilot/summary", "", "")
	if status != 401 {
		t.Fatalf("unauthenticated summary=%d", status)
	}
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/autopilot/deployments", "secret", `{"agent_id":"verified","version":"v1"}`)
	if status != 201 {
		t.Fatalf("draft=%d %v", status, body)
	}
	id := body["id"].(string)
	path := "/api/v1/autopilot/deployments/" + id
	status, body = gatewayJSON(t, s, http.MethodPost, path+"/canary", "secret", `{}`)
	if status != 409 {
		t.Fatalf("ungated promotion=%d %v", status, body)
	}
	status, body = gatewayJSON(t, s, http.MethodPost, path+"/simulate", "secret", `{"input":"Representative test"}`)
	if status != 200 {
		t.Fatalf("simulation=%d %v", status, body)
	}
	proof := body["proof"].(map[string]any)
	if proof["simulation"] != true || body["state"] == nil {
		t.Fatalf("simulation contract=%v", body)
	}
	status, body = gatewayJSON(t, s, http.MethodPost, path+"/canary", "secret", `{}`)
	if status != 409 {
		t.Fatalf("simulation admitted live traffic=%d %v", status, body)
	}
	for range 5 {
		status, body = gatewayJSON(t, s, http.MethodPost, path+"/run", "secret", `{"input":"Representative test"}`)
		if status != 200 || body["run_error"] != nil {
			t.Fatalf("real qualification=%d %v", status, body)
		}
	}
	for _, step := range []string{"canary", "promote"} {
		status, body = gatewayJSON(t, s, http.MethodPost, path+"/"+step, "secret", `{}`)
		if status != 200 {
			t.Fatalf("%s=%d %v", step, status, body)
		}
	}
	status, body = gatewayJSON(t, s, http.MethodPost, "/api/v1/autopilot/agents/verified/freeze", "secret", `{"frozen":true,"reason":"test"}`)
	if status != 200 {
		t.Fatalf("freeze=%d %v", status, body)
	}
	status, body = gatewayJSON(t, s, http.MethodPost, "/api/v1/chat", "secret", `{"agent_id":"verified","session_id":"freeze-test","text":"work"}`)
	if status != 409 {
		t.Fatalf("frozen chat=%d %v", status, body)
	}
	status, body = gatewayJSON(t, s, http.MethodGet, "/api/v1/autopilot/reliability?agent_id=verified", "secret", "")
	if status != 200 {
		t.Fatal(body)
	}
	reliability := body["reliability"].([]any)[0].(map[string]any)
	if reliability["sample_count"] != float64(5) {
		t.Fatalf("live samples=%v", reliability)
	}
}

func TestAutopilotChatStableRequestIDNeverReplays(t *testing.T) {
	s, p := newAutopilotGateway(t)
	body := `{"agent_id":"verified","session_id":"idempotency-test","text":"work"}`
	status, reply := gatewayJSONWithHeader(t, s, "POST", "/api/v1/chat", "secret", body, "Idempotency-Key", "ios-outbox-one")
	if status != 200 {
		t.Fatalf("first send=%d %v", status, reply)
	}
	if reply["run_id"] != "chat:ios-outbox-one" {
		t.Fatalf("request id lost=%v", reply)
	}
	status, reply = gatewayJSONWithHeader(t, s, "POST", "/api/v1/chat", "secret", body, "Idempotency-Key", "ios-outbox-one")
	if status != 409 {
		t.Fatalf("replay=%d %v", status, reply)
	}
	p.mu.Lock()
	calls := len(p.requests)
	p.mu.Unlock()
	if calls != 1 {
		t.Fatalf("provider calls=%d", calls)
	}
}

func TestAutopilotGoalDAGExecutesVerifiedDependencyResults(t *testing.T) {
	s, p := newAutopilotGateway(t)
	body := `{"title":"Two step team","objective":"Verify dependency handoff","budget":{"max_cost_usd":2,"max_duration_ms":60000},"tasks":[{"id":"first","title":"First","agent_id":"verified","prompt":"first task","depends_on":[],"budget":{"max_cost_usd":1,"max_duration_ms":20000}},{"id":"second","title":"Second","agent_id":"verified","prompt":"second task","depends_on":["first"],"budget":{"max_cost_usd":1,"max_duration_ms":20000}}]}`
	status, result := gatewayJSON(t, s, "POST", "/api/v1/autopilot/goals", "secret", body)
	if status != 201 {
		t.Fatalf("create=%d %v", status, result)
	}
	id := result["id"].(string)
	status, result = gatewayJSON(t, s, "POST", "/api/v1/autopilot/goals/"+id+"/run", "secret", `{}`)
	if status != 202 {
		t.Fatalf("start=%d %v", status, result)
	}
	deadline := time.Now().Add(3 * time.Second)
	var goal autopilot.Goal
	for time.Now().Before(deadline) {
		var err error
		goal, err = s.autopilotStore.GetGoal(context.Background(), "admin", id)
		if err != nil {
			t.Fatal(err)
		}
		if goal.Status != autopilot.GoalRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if goal.Status != autopilot.GoalSucceeded {
		raw, _ := json.Marshal(goal)
		t.Fatalf("goal=%s", raw)
	}
	if len(goal.Tasks) != 2 || goal.Tasks[0].ProofID == "" || goal.Tasks[1].ProofID == "" {
		t.Fatalf("missing task proofs=%+v", goal.Tasks)
	}
	request := p.lastRequest()
	raw, _ := json.Marshal(request.Messages)
	if !containsJSON(string(raw), "Dependency results", "sync reply") {
		t.Fatalf("handoff=%s", raw)
	}
	status, result = gatewayJSON(t, s, "POST", "/api/v1/autopilot/goals/"+id+"/run", "secret", `{}`)
	if status != 409 {
		t.Fatalf("goal replay=%d %v", status, result)
	}
}
func containsJSON(raw string, values ...string) bool {
	for _, v := range values {
		if !strings.Contains(raw, v) {
			return false
		}
	}
	return true
}
