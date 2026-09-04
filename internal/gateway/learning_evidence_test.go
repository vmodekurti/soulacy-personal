package gateway

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/learning"
	"github.com/soulacy/soulacy/pkg/message"
)

func TestLearningEvidenceEndpoint(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("SOULACY_WORKSPACE", workspace)

	srv := newTestGateway(t, "secret")
	store, err := learning.NewStore(filepath.Join(workspace, "data", "learning.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	srv.engine.SetLearningStore(store)

	// An accepted learned skill for agent-a. The store stamps the acceptance
	// time as time.Now(), so anchor the synthetic events around now: reuses
	// must fall after acceptance, "before" errors before it.
	added, err := store.Add(learning.Proposal{
		AgentID: "agent-a", Kind: "skill", Content: "skill draft",
		Meta: map[string]string{"skill_name": "morning-brief"},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := store.UpdateStatusMeta(added.ID, learning.StatusAccepted, map[string]string{"installed_path": "/tmp/SKILL.md"}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	now := time.Now().UTC()

	srv.actions = &fakeTailBackend{events: []message.Event{
		// two post-acceptance reuses of the accepted skill in distinct sessions
		{Type: "tool.call", AgentID: "agent-a", SessionID: "s1", Timestamp: now.Add(2 * time.Hour),
			Payload: message.ToolCall{Name: "read_skill", Arguments: map[string]any{"skill_name": "morning-brief"}}},
		{Type: "tool.call", AgentID: "agent-a", SessionID: "s2", Timestamp: now.Add(3 * time.Hour),
			Payload: message.ToolCall{Name: "read_skill", Arguments: map[string]any{"skill_name": "morning-brief"}}},
		// same failure twice before learning, once after (ids normalize equally)
		{Type: "error", AgentID: "agent-a", SessionID: "s0", Timestamp: now.Add(-48 * time.Hour),
			Payload: map[string]any{"error": "provider foo returned 500 (attempt 1)"}},
		{Type: "error", AgentID: "agent-a", SessionID: "s0b", Timestamp: now.Add(-24 * time.Hour),
			Payload: map[string]any{"error": "provider foo returned 500 (attempt 2)"}},
		{Type: "error", AgentID: "agent-a", SessionID: "s3", Timestamp: now.Add(4 * time.Hour),
			Payload: map[string]any{"error": "provider foo returned 500 (attempt 3)"}},
	}}

	status, body := gatewayJSON(t, srv, http.MethodGet, "/api/v1/learning/evidence?agent_id=agent-a", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if body["enabled"] != true {
		t.Fatalf("enabled = %v", body["enabled"])
	}
	evidence, ok := body["evidence"].(map[string]any)
	if !ok {
		t.Fatalf("evidence missing: %v", body)
	}
	if evidence["reused_skills"].(float64) != 1 || evidence["total_skill_uses"].(float64) != 2 {
		t.Fatalf("reuse metrics = %v", evidence)
	}
	reuse, ok := evidence["skill_reuse"].([]any)
	if !ok || len(reuse) != 1 {
		t.Fatalf("skill_reuse = %v", evidence["skill_reuse"])
	}
	first := reuse[0].(map[string]any)
	if first["skill_name"] != "morning-brief" || first["uses"].(float64) != 2 || first["sessions"].(float64) != 2 {
		t.Fatalf("skill reuse entry = %v", first)
	}
	repeated, ok := evidence["repeated_errors"].([]any)
	if !ok || len(repeated) != 1 {
		t.Fatalf("repeated_errors = %v", evidence["repeated_errors"])
	}
	trend := repeated[0].(map[string]any)
	if trend["before"].(float64) != 2 || trend["after"].(float64) != 1 {
		t.Fatalf("error trend = %v", trend)
	}
}

func TestLearningEvidenceEndpointDisabled(t *testing.T) {
	srv := newTestGateway(t, "secret")
	// No learning store configured.
	status, body := gatewayJSON(t, srv, http.MethodGet, "/api/v1/learning/evidence?agent_id=x", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if body["enabled"] != false {
		t.Fatalf("expected enabled=false, got %v", body)
	}
}
