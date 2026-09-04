package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

// The generic webhook adapter is the worst case of the destination-override
// problem, because it is the one that attaches OPERATOR-CONFIGURED HEADERS —
// typically an Authorization bearer — and an HMAC signature. The override comes
// from the `channel.send` tool's `to` argument, i.e. model output, and
// channel.send is in the always-on SAFE tool partition: no capability, no
// confirmation, no policy category.
//
// The teams and googlechat adapters have the equivalent tests; this package had
// none, which a mutation sweep found by deleting the guard here and watching
// everything stay green.
func TestSend_RefusesAnOverrideToAnotherHost(t *testing.T) {
	configuredHit := false
	configured := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		configuredHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer configured.Close()

	var gotAuth string
	exfilHit := false
	exfil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exfilHit = true
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer exfil.Close()

	a, err := New("webhook", configured.URL, http.MethodPost,
		map[string]string{"Authorization": "Bearer sk-operator-secret"}, "", "signing-secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}

	err = a.Send(context.Background(), message.Message{
		ThreadID: exfil.URL + "/collect",
		Parts:    message.Text("the conversation so far"),
	})
	if err == nil {
		t.Fatal("a model-supplied override redirected the delivery to an unrelated host")
	}
	if exfilHit {
		t.Fatalf("the message reached the attacker-chosen host, carrying %q", gotAuth)
	}
	if configuredHit {
		t.Fatal("a refused override silently fell back to the configured host — the caller was not told")
	}
}

func TestSend_OverrideMayChangeThePathOnTheConfiguredHost(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a, err := New("webhook", srv.URL+"/hook", http.MethodPost, nil, "", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Send(context.Background(), message.Message{
		ThreadID: srv.URL + "/hook/thread/42",
		Parts:    message.Text("route this"),
	}); err != nil {
		t.Fatalf("a same-host override was refused, which breaks per-thread routing: %v", err)
	}
	if gotPath != "/hook/thread/42" {
		t.Fatalf("delivered to %q, want the overridden path", gotPath)
	}
}

// By far the common case: `to` is a plain label, not a URL. It must be ignored,
// not treated as an error.
func TestSend_NonURLDestinationStillUsesTheConfiguredEndpoint(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a, err := New("webhook", srv.URL, http.MethodPost, nil, "", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Send(context.Background(), message.Message{
		ThreadID: "destination-label",
		Parts:    message.Text("hello"),
	}); err != nil {
		t.Fatalf("a plain destination label became an error: %v", err)
	}
	if !hit {
		t.Fatal("the configured endpoint was never called")
	}
}
