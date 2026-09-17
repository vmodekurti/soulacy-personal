package gateway

import (
	"context"
	"strings"
	"testing"
)

// genieBuildGateway is the shared harness: a gateway whose configured default
// provider is the fake one, which is what an install looks like when its
// provider actually works.
func genieBuildGateway(t *testing.T, reply string) *Server {
	t.Helper()
	s, provider := newTestGatewayWithLLM(t, "secret")
	s.cfg.LLM.DefaultProvider = "test"
	provider.content = reply
	return s
}

// The builder asked a question. Genie relays it; it does not invent an answer,
// and it does not report having built anything.
func TestGenieBuildRelaysTheBuildersQuestion(t *testing.T) {
	s := genieBuildGateway(t, `{"reply":"Which inbox should I watch?","understanding":{"name":"inbox-watch","confidence":0.4,"missing":["which inbox"]}}`)

	out, err := s.BuildAgentForGenie(context.Background(), "", "keep an eye on my email")
	if err != nil {
		t.Fatalf("a question is not a failure: %v", err)
	}
	if out["status"] != "needs_detail" {
		t.Fatalf("status = %v, want needs_detail: %v", out["status"], out)
	}
	if q, _ := out["question"].(string); !strings.Contains(q, "inbox") {
		t.Errorf("the builder's own question should come back, got %q", q)
	}
	if sess, _ := out["session"].(string); sess == "" {
		t.Error("without a session the next turn starts over and asks again")
	}
	if _, built := out["agent_id"]; built {
		t.Error("nothing was built yet; saying otherwise is the lie that matters here")
	}
}

// A second turn continues the same build rather than starting a new one.
func TestGenieBuildKeepsTheSessionAcrossTurns(t *testing.T) {
	s := genieBuildGateway(t, `{"reply":"Which inbox?","understanding":{"name":"inbox-watch","confidence":0.4,"missing":["which inbox"]}}`)

	first, err := s.BuildAgentForGenie(context.Background(), "", "watch my email")
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	session, _ := first["session"].(string)
	second, err := s.BuildAgentForGenie(context.Background(), session, "the work one")
	if err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if got, _ := second["session"].(string); got != session {
		t.Errorf("session = %q, want the one handed back (%q)", got, session)
	}
}

// The whole point: a complete understanding becomes a real, saved agent —
// through the same gate and tool wiring the Studio screen uses.
func TestGenieBuildSavesARealAgent(t *testing.T) {
	s := genieBuildGateway(t, `{"reply":"That's everything I need.","understanding":{
		"name":"Morning Brief","description":"A daily summary",
		"confidence":0.95,"purpose":"summarise the morning",
		"system_prompt":"Summarise the overnight news in five bullets.",
		"trigger":{"type":"cron","schedule":"0 7 * * *"},
		"missing":[]}}`)

	out, err := s.BuildAgentForGenie(context.Background(), "", "give me a news summary every morning at 7")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if out["status"] != "built" {
		t.Fatalf("status = %v, want built: %v", out["status"], out)
	}
	id, _ := out["agent_id"].(string)
	if id == "" {
		t.Fatal("a built agent needs an id to be found again")
	}
	def := s.loader.Get(id)
	if def == nil {
		t.Fatalf("agent %q was reported built but is not in the loader", id)
	}
	if !def.Enabled {
		t.Error("an agent built in conversation should be ready to run")
	}
	// Findable and stoppable through the same conversation that made it.
	if def.Labels["soulacy.owner"] != "genie" {
		t.Errorf("owner label = %q, want genie", def.Labels["soulacy.owner"])
	}
	if out["cron"] != "0 7 * * *" {
		t.Errorf("the schedule the user asked for should come back, got %v", out["cron"])
	}
}

// An empty request is the one case that is the caller's mistake rather than
// the user's, so it is an error rather than a question to relay.
func TestGenieBuildRefusesAnEmptyRequest(t *testing.T) {
	s := genieBuildGateway(t, "{}")
	if _, err := s.BuildAgentForGenie(context.Background(), "", "   "); err == nil {
		t.Fatal("an empty request should be an error, not a build")
	}
}
