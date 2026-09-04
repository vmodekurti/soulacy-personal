package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
	"github.com/soulacy/soulacy/sdk/registry"
)

func TestWebhookSendPostsJSONPayload(t *testing.T) {
	var got map[string]any
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Test")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	a, err := New("webhook", srv.URL, "POST", map[string]string{"X-Test": "yes"}, "", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	msg := message.Message{
		ID:        "msg-1",
		SessionID: "sess-1",
		AgentID:   "agent-1",
		Channel:   "webhook",
		ThreadID:  "destination-label",
		UserID:    "user-1",
		Username:  "user",
		Role:      message.RoleAssistant,
		Parts:     message.Text("hello\n```chart\n{}\n```\nworld"),
		CreatedAt: time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC),
	}
	if err := a.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if gotHeader != "yes" {
		t.Fatalf("header = %q, want yes", gotHeader)
	}
	if got["text"] != "hello\n\nworld" {
		t.Fatalf("text = %#v", got["text"])
	}
	if got["agent_id"] != "agent-1" {
		t.Fatalf("agent_id = %#v", got["agent_id"])
	}
}

func TestWebhookSendNon2xxReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer srv.Close()
	a, err := New("webhook", srv.URL, "POST", nil, "", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	err = a.Send(context.Background(), message.Message{Parts: message.Text("hello")})
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("err = %v, want status 502", err)
	}
}

func TestWebhookRegistryRequiresURL(t *testing.T) {
	_, ok, err := registry.NewChannel("webhook", map[string]any{})
	if !ok {
		t.Fatal("webhook factory not registered")
	}
	if err == nil || !strings.Contains(err.Error(), "url is required") {
		t.Fatalf("err = %v, want url required", err)
	}
}
