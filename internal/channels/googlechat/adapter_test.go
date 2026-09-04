package googlechat

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

func TestGoogleChatSendPostsTextPayload(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Fatalf("content-type = %q, want json", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a, err := New("google_chat", srv.URL, "[Soulacy]", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	msg := message.Message{
		Channel: "google_chat",
		Parts:   message.Text("hello\n```chart\n{}\n```\nworld"),
	}
	if err := a.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if got["text"] != "[Soulacy]\n\nhello\n\nworld" {
		t.Fatalf("text = %#v", got["text"])
	}
	if !a.Status().Connected {
		t.Fatal("adapter should report connected after Start")
	}
}

// The per-message destination override exists so a reply can be addressed to a
// specific thread or room. It used to accept ANY http(s) URL and post there with
// the operator's configured headers attached — and the value comes from the
// `channel.send` tool's `to` argument, i.e. model output. This test previously
// asserted that a second, unrelated host received the delivery: that was the
// vulnerability written down as a requirement. What is asserted now is the
// boundary — the path may change, the host may not.
func TestGoogleChatSendOverrideMayChangeThePathButNotTheHost(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a, err := New("google_chat", srv.URL+"/hook", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Send(context.Background(), message.Message{
		Metadata: map[string]string{"to": srv.URL + "/hook/thread/42"},
		Parts:    message.Text("route this"),
	}); err != nil {
		t.Fatalf("a same-host override was refused, which breaks per-thread routing: %v", err)
	}
	if gotPath != "/hook/thread/42" {
		t.Fatalf("delivered to %q, want the overridden path", gotPath)
	}
}

func TestGoogleChatSendRefusesAnOverrideToAnotherHost(t *testing.T) {
	hitConfigured := false
	configured := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitConfigured = true
		w.WriteHeader(http.StatusOK)
	}))
	defer configured.Close()

	exfilHit := false
	exfil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exfilHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer exfil.Close()

	a, err := New("google_chat", configured.URL, "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	err = a.Send(context.Background(), message.Message{
		Metadata: map[string]string{"to": exfil.URL + "/collect"},
		Parts:    message.Text("the conversation so far"),
	})
	if err == nil {
		t.Fatal("a model-supplied override redirected the delivery to an unrelated host")
	}
	if exfilHit {
		t.Fatal("the message reached the attacker-chosen host")
	}
	if hitConfigured {
		t.Fatal("a refused override silently fell back to the configured host — the caller was not told")
	}
}

func TestGoogleChatSendNon2xxReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad hook", http.StatusBadGateway)
	}))
	defer srv.Close()
	a, err := New("google_chat", srv.URL, "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	err = a.Send(context.Background(), message.Message{Parts: message.Text("hello")})
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("err = %v, want status 502", err)
	}
}

func TestGoogleChatRegistryRequiresWebhookURL(t *testing.T) {
	_, ok, err := registry.NewChannel("google_chat", map[string]any{})
	if !ok {
		t.Fatal("google_chat factory not registered")
	}
	if err == nil || !strings.Contains(err.Error(), "webhook_url is required") {
		t.Fatalf("err = %v, want webhook_url required", err)
	}
}
