package gateway

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/learning"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

func TestProposeLearningFromRunCreatesReviewableProposals(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("SOULACY_WORKSPACE", workspace)

	srv := newTestGateway(t, "secret")
	store, err := learning.NewStore(filepath.Join(workspace, "data", "learning.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	srv.engine.SetLearningStore(store)
	srv.loader.Register(&agent.Definition{
		ID:      "learn-agent",
		Name:    "Learning Agent",
		Enabled: true,
		Learning: agent.LearningConfig{
			MinChars:     20,
			MaxProposals: 3,
		},
	})

	in := message.Message{
		ID:        "msg-in",
		AgentID:   "learn-agent",
		SessionID: "sess-learn",
		Channel:   "http",
		Role:      message.RoleUser,
		Parts:     message.Text("Build a reusable stock research checklist"),
		CreatedAt: time.Now().UTC(),
	}
	out := message.Message{
		ID:        "msg-out",
		AgentID:   "learn-agent",
		SessionID: "sess-learn",
		Channel:   "http",
		Role:      message.RoleAssistant,
		Parts:     message.Text("1. Pull prices.\n2. Check earnings.\n3. Summarize risk and catalysts."),
		CreatedAt: time.Now().UTC(),
	}
	srv.actions = &fakeTailBackend{events: []message.Event{
		{Type: "message.in", AgentID: "learn-agent", SessionID: "sess-learn", Payload: in, Timestamp: time.Now().UTC()},
		{Type: "tool.call", AgentID: "learn-agent", SessionID: "sess-learn", Payload: message.ToolCall{Name: "yfinance_quote"}, Timestamp: time.Now().UTC()},
		{Type: "message.out", AgentID: "learn-agent", SessionID: "sess-learn", Payload: out, Timestamp: time.Now().UTC()},
	}}

	status, body := gatewayJSON(t, srv, http.MethodPost, "/api/v1/learning/propose-from-run", "secret", `{"agent_id":"learn-agent","session_id":"sess-learn"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if int(body["created"].(float64)) != 3 {
		t.Fatalf("created = %v, want 3; body=%v", body["created"], body)
	}
	props, err := store.List("learn-agent", learning.StatusPending, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(props) != 3 {
		t.Fatalf("stored proposals = %d, want 3", len(props))
	}
	if props[0].Source != "manual_run_review" {
		t.Fatalf("source = %q", props[0].Source)
	}
	var skillProp learning.Proposal
	for _, p := range props {
		if p.Kind == "skill" {
			skillProp = p
			break
		}
	}
	if skillProp.ID == "" || !strings.Contains(skillProp.Content, "yfinance_quote") {
		t.Fatalf("skill proposal missing tool provenance: %#v", skillProp)
	}
}

func TestLearningSummaryEndpoint(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("SOULACY_WORKSPACE", workspace)

	srv := newTestGateway(t, "secret")
	store, err := learning.NewStore(filepath.Join(workspace, "data", "learning.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	srv.engine.SetLearningStore(store)
	pending, err := store.Add(learning.Proposal{AgentID: "agent-a", Kind: "memory", Content: "draft", Source: "manual_run_review"})
	if err != nil {
		t.Fatalf("Add pending: %v", err)
	}
	accepted, err := store.Add(learning.Proposal{
		AgentID: "agent-a",
		Kind:    "skill",
		Content: "skill draft",
		Source:  "background_reflection",
		Meta:    map[string]string{"background_reflection": "true"},
	})
	if err != nil {
		t.Fatalf("Add accepted: %v", err)
	}
	if _, err := store.UpdateStatusMeta(accepted.ID, learning.StatusAccepted, map[string]string{"installed_path": "/tmp/SKILL.md"}); err != nil {
		t.Fatalf("accept: %v", err)
	}

	status, body := gatewayJSON(t, srv, http.MethodGet, "/api/v1/learning/summary?agent_id=agent-a", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	summary, ok := body["summary"].(map[string]any)
	if !ok {
		t.Fatalf("summary missing: %v", body)
	}
	if summary["total"].(float64) != 2 || summary["pending"].(float64) != 1 || summary["accepted"].(float64) != 1 {
		t.Fatalf("summary counts = %v", summary)
	}
	if summary["installed_skills"].(float64) != 1 {
		t.Fatalf("installed_skills = %v", summary["installed_skills"])
	}
	if summary["manual_reviews"].(float64) != 1 || summary["background_runs"].(float64) != 1 {
		t.Fatalf("learning provenance counts = %v", summary)
	}
	if pending.ID == "" {
		t.Fatal("pending proposal was not created")
	}
}

func TestReflectLearningFromRecentRunsCreatesReviewableProposals(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("SOULACY_WORKSPACE", workspace)

	srv := newTestGateway(t, "secret")
	store, err := learning.NewStore(filepath.Join(workspace, "data", "learning.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	srv.engine.SetLearningStore(store)
	srv.loader.Register(&agent.Definition{
		ID:      "reflect-agent",
		Name:    "Reflect Agent",
		Enabled: true,
		Learning: agent.LearningConfig{
			MinChars:     20,
			MaxProposals: 2,
		},
	})

	in := message.Message{
		ID:        "msg-in",
		AgentID:   "reflect-agent",
		SessionID: "sess-reflect",
		Channel:   "slack",
		Role:      message.RoleUser,
		Parts:     message.Text("Create a reusable process for notebook ingestion"),
		CreatedAt: time.Now().UTC(),
	}
	out := message.Message{
		ID:        "msg-out",
		AgentID:   "reflect-agent",
		SessionID: "sess-reflect",
		Channel:   "slack",
		Role:      message.RoleAssistant,
		Parts:     message.Text("1. Capture the URL.\n2. Queue it.\n3. Process the queue during the scheduled run."),
		CreatedAt: time.Now().UTC(),
	}
	srv.actions = &fakeTailBackend{events: []message.Event{
		{Type: "message.in", AgentID: "reflect-agent", SessionID: "sess-reflect", Payload: in, Timestamp: time.Now().UTC()},
		{Type: "tool.call", AgentID: "reflect-agent", SessionID: "sess-reflect", Payload: message.ToolCall{Name: "queue_put"}, Timestamp: time.Now().UTC()},
		{Type: "message.out", AgentID: "reflect-agent", SessionID: "sess-reflect", Payload: out, Timestamp: time.Now().UTC()},
	}}

	status, body := gatewayJSON(t, srv, http.MethodPost, "/api/v1/learning/reflect-recent-runs", "secret", `{"agent_id":"reflect-agent","max_runs":5}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if int(body["reviewed"].(float64)) != 1 {
		t.Fatalf("reviewed = %v, want 1; body=%v", body["reviewed"], body)
	}
	if int(body["created"].(float64)) != 2 {
		t.Fatalf("created = %v, want 2; body=%v", body["created"], body)
	}
	props, err := store.List("reflect-agent", learning.StatusPending, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(props) != 2 {
		t.Fatalf("stored proposals = %d, want 2", len(props))
	}
	for _, p := range props {
		if p.Source != "reflection_sweep" {
			t.Fatalf("source = %q, want reflection_sweep", p.Source)
		}
		if p.Meta["reflection_sweep"] != "true" {
			t.Fatalf("reflection metadata missing: %#v", p.Meta)
		}
	}
}
