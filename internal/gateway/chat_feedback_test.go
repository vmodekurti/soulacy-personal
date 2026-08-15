package gateway

import (
	"github.com/soulacy/soulacy/internal/wsroot"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/internal/learning"
	"github.com/soulacy/soulacy/pkg/message"
)

func TestFeedbackRunExistsUsesExactTerminalRun(t *testing.T) {
	events := []message.Event{{Type: "run.completed", AgentID: "a", SessionID: "s", Payload: map[string]any{"run_id": "r"}}}
	if !feedbackRunExists(events, "a", "s", "r") {
		t.Fatal("expected run match")
	}
	if feedbackRunExists(events, "a", "other", "r") {
		t.Fatal("cross-session match")
	}
	if feedbackRunExists(events, "a", "s", "other") {
		t.Fatal("wrong run match")
	}
}

func TestChatFeedbackEndpointPersistsRatingAndReviewableCorrection(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("SOULACY_WORKSPACE", workspace)
	srv := newTestGateway(t, "secret")
	stores, err := learning.NewStores(filepath.Join(workspace, "learning.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// The personal workspace is what a directly constructed test gateway is.
	store := stores.For(wsroot.PersonalWorkspaceID)
	srv.engine.SetLearningStores(stores)
	srv.actions = &fakeTailBackend{events: []message.Event{{
		Type: "run.completed", AgentID: "agent-a", SessionID: "session-a", Payload: map[string]any{"run_id": "run-a"},
	}}}
	status, body := gatewayJSON(t, srv, http.MethodPost, "/api/v1/chat/feedback", "secret",
		`{"agent_id":"agent-a","session_id":"session-a","run_id":"run-a","response_id":"response-a","rating":-1,"comment":"Always include the source date."}`)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, body)
	}
	feedback, err := store.ListFeedback("agent-a", 10)
	if err != nil || len(feedback) != 1 || feedback[0].Rating != -1 {
		t.Fatalf("feedback=%+v err=%v", feedback, err)
	}
	proposals, err := store.List("agent-a", learning.StatusPending, 10)
	if err != nil || len(proposals) != 1 || proposals[0].Source != "user_feedback" {
		t.Fatalf("proposals=%+v err=%v", proposals, err)
	}
}
