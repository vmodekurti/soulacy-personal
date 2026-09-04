package learning

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

type fakeAgents struct {
	defs []*agent.Definition
}

func (f fakeAgents) All() []*agent.Definition { return f.defs }

type fakeTailer struct {
	events map[string][]message.Event
}

func (f fakeTailer) Tail(agentID string, n int) ([]message.Event, error) {
	return f.events[agentID], nil
}

func TestSweeperCreatesReviewableProposalsForAutoProposeAgents(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "learning.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	agentID := "researcher"
	defs := fakeAgents{defs: []*agent.Definition{{
		ID:   agentID,
		Name: "Researcher",
		Learning: agent.LearningConfig{
			Enabled:      true,
			AutoPropose:  true,
			MinChars:     20,
			MaxProposals: 2,
		},
	}}}
	events := []message.Event{
		{AgentID: agentID, SessionID: "s1", Type: "message.in", Payload: map[string]any{"text": "Find the best repeatable way to brief earnings.", "channel": "http"}, Timestamp: time.Now()},
		{AgentID: agentID, SessionID: "s1", Type: "tool.call", Payload: map[string]any{"name": "web_search"}, Timestamp: time.Now()},
		{AgentID: agentID, SessionID: "s1", Type: "message.out", Payload: map[string]any{"text": "Use these steps:\n1. Search official release.\n2. Compare estimates.\n3. Summarize risks."}, Timestamp: time.Now()},
	}
	sweeper := NewSweeper(SweeperConfig{
		Store:   store,
		Actions: fakeTailer{events: map[string][]message.Event{agentID: events}},
		Agents:  defs,
	})

	result, err := sweeper.SweepOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.AgentsReviewed != 1 || result.RunsReviewed != 1 || result.Created == 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	proposals, err := store.List(agentID, StatusPending, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(proposals) != result.Created {
		t.Fatalf("stored proposals = %d, created = %d", len(proposals), result.Created)
	}
	for _, p := range proposals {
		if p.Source != "background_reflection" {
			t.Fatalf("proposal source = %q", p.Source)
		}
		if p.Meta["background_reflection"] != "true" {
			t.Fatalf("missing background metadata: %+v", p.Meta)
		}
	}

	again, err := sweeper.SweepOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if again.Created != 0 {
		t.Fatalf("duplicate sweep created %d proposals", again.Created)
	}
}

func TestSweeperSkipsAgentsWithoutAutoPropose(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "learning.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	agentID := "quiet"
	sweeper := NewSweeper(SweeperConfig{
		Store: store,
		Actions: fakeTailer{events: map[string][]message.Event{agentID: {
			{AgentID: agentID, SessionID: "s1", Type: "message.in", Payload: map[string]any{"text": "hello"}},
			{AgentID: agentID, SessionID: "s1", Type: "message.out", Payload: map[string]any{"text": "hello back with enough detail"}},
		}}},
		Agents: fakeAgents{defs: []*agent.Definition{{
			ID:       agentID,
			Learning: agent.LearningConfig{Enabled: true, AutoPropose: false},
		}}},
	})
	result, err := sweeper.SweepOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.AgentsReviewed != 0 || result.Created != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
}
