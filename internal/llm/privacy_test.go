package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func privacyRouter(t *testing.T, enabled bool, local *routingProvider) (*Router, *routingProvider) {
	t.Helper()
	cloud := &routingProvider{id: "cloud", content: "from cloud", profile: ModelProfile{Model: "big"}}
	r := NewRouter("cloud")
	r.Register(cloud)
	locals := map[string]bool{}
	if local != nil {
		r.Register(local)
		locals[local.id] = true
	}
	r.SetRoutePolicy(RoutePolicy{LocalProviders: locals})
	r.SetPrivacyPolicy(PrivacyPolicy{Enabled: enabled})
	return r, cloud
}

func userReq(text string) CompletionRequest {
	return CompletionRequest{Model: "big", Messages: []ChatMessage{{Role: "user", Content: text}}}
}

func privateCtx() context.Context {
	return WithCallMetadata(context.Background(), CallMetadata{DataClassification: "private"})
}

func TestPrivateRunStaysOnTheLocalProvider(t *testing.T) {
	local := &routingProvider{id: "ollama", content: "from local", profile: ModelProfile{Model: "small"}}
	r, cloud := privacyRouter(t, true, local)

	resp, err := r.Complete(privateCtx(), "cloud", userReq("Fill in the form"))
	if err != nil || resp.Content != "from local" {
		t.Fatalf("private run = %v, %v", resp, err)
	}
	if cloud.calls != 0 {
		t.Fatal("a private run reached the cloud provider")
	}
	if got := local.requests[0].Model; got != "small" {
		t.Fatalf("local call used model %q, want the local default", got)
	}

	resp, err = r.Complete(context.Background(), "cloud", userReq("What is the capital of France?"))
	if err != nil || resp.Content != "from cloud" || cloud.calls != 1 {
		t.Fatalf("ordinary run = %v, %v (cloud calls %d)", resp, err, cloud.calls)
	}
}

// Regression for #401: the router must not judge content. A web page returned
// by a tool carries card-shaped product codes; re-judging each request moved a
// run to the local model partway through.
func TestRouterDoesNotRejudgeContentMidRun(t *testing.T) {
	local := &routingProvider{id: "ollama", content: "from local", profile: ModelProfile{Model: "small"}}
	r, cloud := privacyRouter(t, true, local)
	req := CompletionRequest{Model: "big", Messages: []ChatMessage{
		{Role: "user", Content: "Show me laptop deals with 16GB RAM"},
		{Role: "tool", Content: "Laptop — SKU 4111111111111111 — EAN 4539578763621486 — PIN: 0000"},
	}}
	if _, err := r.Complete(context.Background(), "cloud", req); err != nil {
		t.Fatal(err)
	}
	if cloud.calls != 1 || local.calls != 0 {
		t.Fatalf("unlabelled run with web content: cloud %d, local %d", cloud.calls, local.calls)
	}
}

func TestPrivateRunNeverFailsOverToCloud(t *testing.T) {
	local := &routingProvider{id: "ollama", alwaysErr: errors.New("connection refused"), profile: ModelProfile{Model: "small"}}
	r, cloud := privacyRouter(t, true, local)
	r.SetRoutePolicy(RoutePolicy{Enabled: true, MaxFailovers: 2, LocalProviders: map[string]bool{"ollama": true},
		Fallbacks: []RouteCandidate{{Provider: "cloud", Model: "big"}}})
	_, err := r.Complete(privateCtx(), "cloud", userReq("Check the charge"))
	if err == nil || !strings.Contains(err.Error(), "nothing was sent elsewhere") {
		t.Fatalf("err = %v", err)
	}
	if cloud.calls != 0 {
		t.Fatal("a private run failed over to the cloud provider")
	}
}

func TestPrivateRunTooLongForTheLocalModelSaysSoPlainly(t *testing.T) {
	local := &routingProvider{id: "ollama", alwaysErr: errors.New("model preflight: prompt exceeds the evidenced input budget (24576 tokens)"), profile: ModelProfile{Model: "small"}}
	r, cloud := privacyRouter(t, true, local)
	_, err := r.Complete(privateCtx(), "cloud", userReq("Summarize everything"))
	if err == nil || !strings.Contains(err.Error(), "grown too long for the model on your machine") || !strings.Contains(err.Error(), "nothing was sent elsewhere") {
		t.Fatalf("err = %v", err)
	}
	if cloud.calls != 0 {
		t.Fatal("an oversized private run reached the cloud provider")
	}
}

func TestPrivacyIsInertWithoutALocalProviderOrWhenOff(t *testing.T) {
	r, cloud := privacyRouter(t, true, nil)
	if _, err := r.Complete(privateCtx(), "cloud", userReq("Fill in the form")); err != nil || cloud.calls != 1 {
		t.Fatalf("no local provider: err %v, cloud calls %d", err, cloud.calls)
	}
	if _, _, ok := r.PrivateRoute("cloud"); ok {
		t.Fatal("PrivateRoute reported a route with no local provider")
	}

	local := &routingProvider{id: "ollama", content: "from local", profile: ModelProfile{Model: "small"}}
	off, cloudOff := privacyRouter(t, false, local)
	if _, err := off.Complete(privateCtx(), "cloud", userReq("Fill in the form")); err != nil || cloudOff.calls != 1 || local.calls != 0 {
		t.Fatalf("policy off: err %v, cloud %d, local %d", err, cloudOff.calls, local.calls)
	}
}

func TestPrivateRouteKeepsAnAlreadyLocalProviderAndSkipsEmbeddings(t *testing.T) {
	local := &routingProvider{id: "ollama", content: "from local", profile: ModelProfile{Model: "small"}}
	r, cloud := privacyRouter(t, true, local)
	if provider, model, ok := r.PrivateRoute("ollama"); !ok || provider != "ollama" || model != "" {
		t.Fatalf("already local = %q %q %v", provider, model, ok)
	}
	req := userReq("Index this")
	req.Operation = "embedding"
	if _, err := r.Complete(privateCtx(), "cloud", req); err != nil || cloud.calls != 1 {
		t.Fatalf("embedding: err %v, cloud calls %d", err, cloud.calls)
	}
}
