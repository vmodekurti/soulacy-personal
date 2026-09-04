package gateway

import (
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/studio"
	"github.com/soulacy/soulacy/pkg/agent"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// A Studio-generated workflow can reference helper agents that live only in the
// draft (draft.NewAgents) and have never been persisted. Run Live runs the
// UNSAVED draft, so those peers are not in the loader and an `agent` node would
// dispatch `agent__<peer>` and fail with "agent call: <id> not loaded". These
// tests cover registerEphemeralPeers, the fix that stubs those peers in memory
// for the duration of the try run.

func agentNodeWorkflow(peerIDs ...string) *agent.Definition {
	nodes := make([]sdkr.FlowNode, 0, len(peerIDs))
	for i, id := range peerIDs {
		nodes = append(nodes, sdkr.FlowNode{
			ID:          "n" + string(rune('1'+i)),
			Kind:        "agent",
			Agent:       id,
			Description: "delegate to " + id,
		})
	}
	return &agent.Definition{
		ID:       "parent-wf",
		Name:     "Parent Workflow",
		Workflow: &agent.WorkflowSpec{Nodes: nodes},
	}
}

// A referenced peer that is not in the loader must be registered (Enabled, in
// memory only) so the agent node can resolve it, and Unregistered on cleanup.
func TestRegisterEphemeralPeers_StubsMissingPeer(t *testing.T) {
	s := newTestGateway(t, "k")
	def := agentNodeWorkflow("notifier")

	// The draft carried a full profile for the peer — it must be used verbatim.
	newAgents := []studio.NewAgent{{
		ID:           "notifier",
		Name:         "Notifier",
		Description:  "Sends notifications",
		SystemPrompt: "You are Notifier. You deliver concise, reliable notifications.",
	}}

	cleanup := s.registerEphemeralPeers(def, newAgents)

	got := s.loader.Get("notifier")
	if got == nil {
		t.Fatalf("peer 'notifier' was not registered — an agent node would fail with \"not loaded\"")
	}
	if !got.Enabled {
		t.Errorf("stub must be Enabled so runAgentCall does not reject it as disabled")
	}
	if got.SourcePath != "" {
		t.Errorf("stub must be in-memory only (SourcePath empty), got %q", got.SourcePath)
	}
	if got.Name != "Notifier" || !strings.Contains(got.SystemPrompt, "Notifier") {
		t.Errorf("stub did not carry the draft profile: %+v", got)
	}

	cleanup()
	if s.loader.Get("notifier") != nil {
		t.Errorf("cleanup must Unregister the ephemeral peer")
	}
}

// When the draft has no profile for a referenced peer, a complete persona is
// synthesized (no blank stub) so the peer still runs.
func TestRegisterEphemeralPeers_SynthesizesWhenProfileMissing(t *testing.T) {
	s := newTestGateway(t, "k")
	def := agentNodeWorkflow("summarizer")

	cleanup := s.registerEphemeralPeers(def, nil)
	defer cleanup()

	got := s.loader.Get("summarizer")
	if got == nil {
		t.Fatalf("peer 'summarizer' was not registered")
	}
	if strings.TrimSpace(got.Name) == "" || strings.TrimSpace(got.SystemPrompt) == "" {
		t.Errorf("synthesized stub must have a name and a non-thin system prompt: %+v", got)
	}
}

// A referenced agent that already exists in the loader (a real, persisted peer)
// must not be overwritten by a stub.
func TestRegisterEphemeralPeers_LeavesExistingAgentAlone(t *testing.T) {
	s := newTestGateway(t, "k")
	real := &agent.Definition{
		ID:           "helper",
		Name:         "Real Helper",
		SystemPrompt: "the real persisted persona",
		Enabled:      true,
	}
	s.loader.Register(real)
	defer s.loader.Unregister("helper")

	def := agentNodeWorkflow("helper")
	cleanup := s.registerEphemeralPeers(def, []studio.NewAgent{{ID: "helper", Name: "Stub"}})
	defer cleanup()

	got := s.loader.Get("helper")
	if got == nil || got.Name != "Real Helper" {
		t.Errorf("existing peer must be left untouched, got %+v", got)
	}
}

// After cleanup, the existing (real) peer must survive — cleanup only removes
// stubs the call actually added.
func TestRegisterEphemeralPeers_CleanupPreservesRealPeer(t *testing.T) {
	s := newTestGateway(t, "k")
	real := &agent.Definition{ID: "helper", Name: "Real Helper", Enabled: true}
	s.loader.Register(real)
	defer s.loader.Unregister("helper")

	def := agentNodeWorkflow("helper")
	cleanup := s.registerEphemeralPeers(def, nil)
	cleanup()

	if s.loader.Get("helper") == nil {
		t.Errorf("cleanup must not remove a peer it did not register")
	}
}

// A def with no workflow (a reasoning agent) registers nothing and returns a
// safe no-op cleanup.
func TestRegisterEphemeralPeers_NoWorkflowIsNoop(t *testing.T) {
	s := newTestGateway(t, "k")
	def := &agent.Definition{ID: "react-agent", Name: "ReAct"}
	cleanup := s.registerEphemeralPeers(def, nil)
	cleanup() // must not panic
}
