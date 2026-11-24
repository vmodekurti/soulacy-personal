package runtime

import (
	"context"
	"testing"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/agent"
)

type localTestProvider struct{ *fakeHandleProvider }

func (localTestProvider) ID() string { return "ollama" }
func (localTestProvider) Models(context.Context) ([]string, error) {
	return []string{"local-model"}, nil
}
func (localTestProvider) DefaultModel() string { return "local-model" }

func newPrivacyTestEngine(t *testing.T) (*Engine, *fakeHandleProvider, *fakeHandleProvider) {
	t.Helper()
	e, cloud := newHandleTestEngine(t, &agent.Definition{
		ID: "assistant", Name: "Assistant", Enabled: true, SystemPrompt: "Be helpful.",
		LLM: agent.LLMConfig{Provider: "test", Model: "fake-model"}, MaxTurns: 3, Builtins: strListPtr(),
	})
	local := &fakeHandleProvider{responses: []llm.CompletionResponse{{Content: "local answer"}, {Content: "local again"}}}
	e.llmRouter.Register(localTestProvider{local})
	e.llmRouter.SetRoutePolicy(llm.RoutePolicy{LocalProviders: map[string]bool{"ollama": true}})
	e.llmRouter.SetPrivacyPolicy(llm.PrivacyPolicy{Enabled: true})
	cloud.responses = []llm.CompletionResponse{{Content: "cloud answer"}}
	return e, cloud, local
}

func TestPrivateConversationIsAnsweredLocallyAndStaysLocal(t *testing.T) {
	e, cloud, local := newPrivacyTestEngine(t)

	reply, err := e.Handle(context.Background(), testUserMessage("assistant", "s-private", "My SSN is 123-45-6789, what should I put on the form?"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := flattenParts(reply.Parts); got != "local answer" {
		t.Fatalf("reply = %q, want the local model's answer", got)
	}
	if reply.Metadata["answered_locally"] != "true" {
		t.Fatalf("reply metadata = %v, want answered_locally", reply.Metadata)
	}
	if n := len(cloud.requestsSnapshot()); n != 0 {
		t.Fatalf("private message reached the cloud provider %d time(s)", n)
	}

	// The subject changes; the conversation still carries the earlier turn.
	reply, err = e.Handle(context.Background(), testUserMessage("assistant", "s-private", "Now tell me a joke"))
	if err != nil {
		t.Fatalf("Handle follow-up: %v", err)
	}
	if got := flattenParts(reply.Parts); got != "local again" || reply.Metadata["answered_locally"] != "true" {
		t.Fatalf("follow-up = %q %v, want it kept local", got, reply.Metadata)
	}
	if n := len(cloud.requestsSnapshot()); n != 0 {
		t.Fatalf("follow-up in a private conversation reached the cloud provider")
	}
	if n := len(local.requestsSnapshot()); n != 2 {
		t.Fatalf("local provider calls = %d, want 2", n)
	}
}

func TestOrdinaryConversationIsUnchanged(t *testing.T) {
	e, cloud, local := newPrivacyTestEngine(t)
	reply, err := e.Handle(context.Background(), testUserMessage("assistant", "s-open", "What is the capital of France?"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := flattenParts(reply.Parts); got != "cloud answer" {
		t.Fatalf("reply = %q, want the configured provider's answer", got)
	}
	if _, marked := reply.Metadata["answered_locally"]; marked {
		t.Fatal("an ordinary reply was marked as answered locally")
	}
	if len(cloud.requestsSnapshot()) != 1 || len(local.requestsSnapshot()) != 0 {
		t.Fatal("an ordinary message was rerouted")
	}
}

// A delegated run's text comes from another agent and can quote a web page;
// it must not be judged as if the person had typed it (#401).
func TestDelegatedRunTextIsNotJudged(t *testing.T) {
	e, cloud, local := newPrivacyTestEngine(t)
	msg := testUserMessage("assistant", "s-delegated", "Compare these: SKU 4111111111111111 and SKU 4539578763621486")
	msg.Channel = "internal"
	if _, err := e.Handle(context.Background(), msg); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(cloud.requestsSnapshot()) != 1 || len(local.requestsSnapshot()) != 0 {
		t.Fatal("a delegated run quoting product codes was moved to the local model")
	}
}
