// workspace_test.go — the cross-tenant isolation contract for reviewable
// learning proposals and the background reflection sweep.
//
// internal/ownership/catalog.go names this file as the isolation evidence for
// the learning store.
package learning

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/wsroot"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

func newStores(t *testing.T) (*Stores, string) {
	t.Helper()
	base := filepath.Join(t.TempDir(), "learning.jsonl")
	stores, err := NewStores(base)
	if err != nil {
		t.Fatalf("NewStores: %v", err)
	}
	return stores, base
}

func addProposal(t *testing.T, store *Store, agentID, content string) Proposal {
	t.Helper()
	p, err := store.Add(Proposal{AgentID: agentID, Content: content, Kind: "procedure"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	return p
}

// A proposal is a candidate rule that changes how an agent behaves once
// accepted. One tenant's review queue must not be visible from another.
func TestProposalsAreInvisibleAcrossWorkspaces(t *testing.T) {
	stores, _ := newStores(t)
	a, b := stores.For("ws_a"), stores.For("ws_b")
	addProposal(t, a, "assistant", "always cite the alpha ledger before posting")
	addProposal(t, b, "assistant", "always tag the beta changelog")

	for _, tc := range []struct {
		store        *Store
		own, foreign string
	}{
		{a, "alpha ledger", "beta changelog"},
		{b, "beta changelog", "alpha ledger"},
	} {
		listed, err := tc.store.List("assistant", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(listed) != 1 {
			t.Fatalf("a workspace listed %d proposals, want its own 1: %+v", len(listed), listed)
		}
		if !contains(listed[0].Content, tc.own) {
			t.Errorf("a workspace lost its own proposal: %q", listed[0].Content)
		}
		if contains(listed[0].Content, tc.foreign) {
			t.Errorf("a workspace read another's proposal: %q", listed[0].Content)
		}
	}
}

// The dangerous operation is accepting, not reading: accepting a proposal turns
// it into a rule the agent follows. A proposal ID from another workspace must
// be indistinguishable from one that does not exist, so IDs cannot be probed
// and cannot be accepted across the boundary.
func TestAProposalCannotBeAcceptedFromAnotherWorkspace(t *testing.T) {
	stores, _ := newStores(t)
	a, b := stores.For("ws_a"), stores.For("ws_b")
	owned := addProposal(t, a, "assistant", "always cite the alpha ledger before posting")

	if _, err := b.UpdateStatus(owned.ID, StatusAccepted); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("another workspace accepted a proposal it does not own: %v", err)
	}
	if _, err := b.UpdateDraft(owned.ID, "rewritten", "content someone else wrote", nil); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("another workspace edited a proposal it does not own: %v", err)
	}
	if _, err := b.SetDisabled(owned.ID, true); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("another workspace disabled a proposal it does not own: %v", err)
	}
	// An unknown ID gives exactly the same answer, so IDs cannot be probed.
	if _, err := b.UpdateStatus("does-not-exist", StatusAccepted); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an unknown ID gave a different answer from a foreign one: %v", err)
	}
	// And the owner's proposal is untouched by any of it.
	still, err := a.List("assistant", StatusPending, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(still) != 1 || still[0].Status != StatusPending {
		t.Fatalf("the owner's proposal was changed from another workspace: %+v", still)
	}
}

// Product invariant 7: an existing single-user installation's review queue does
// not move, and a tenant's queue never lands in it.
func TestPersonalProposalPathIsUnchanged(t *testing.T) {
	stores, base := newStores(t)
	addProposal(t, stores.For(wsroot.PersonalWorkspaceID), "bot", "a personal lesson worth keeping")
	addProposal(t, stores.For("ws_a"), "bot", "a tenant lesson worth keeping")

	if _, err := os.Stat(base); err != nil {
		t.Fatalf("the personal queue is not at its historical path %s: %v", base, err)
	}
	namespaced := wsroot.File(base, "ws_a")
	if namespaced == base {
		t.Fatal("a tenant's queue resolved to the personal path")
	}
	if _, err := os.Stat(namespaced); err != nil {
		t.Fatalf("a tenant's queue was not namespaced: %v", err)
	}
	// An absent workspace is the personal one, which is what a pre-tenant
	// installation was — not an unowned queue.
	if stores.For("") != stores.For(wsroot.PersonalWorkspaceID) {
		t.Fatal("an absent workspace resolved to a store other than the personal one")
	}
}

// Deduplication is per workspace. Two tenants independently learning the same
// lesson are two proposals, and neither suppresses the other's review.
func TestDeduplicationDoesNotReachAcrossWorkspaces(t *testing.T) {
	stores, _ := newStores(t)
	const lesson = "always summarise the risks before sending"
	first := addProposal(t, stores.For("ws_a"), "assistant", lesson)
	second := addProposal(t, stores.For("ws_b"), "assistant", lesson)
	if first.ID == second.ID {
		t.Fatal("one workspace's proposal was returned to another as a duplicate")
	}
	for _, workspaceID := range []string{"ws_a", "ws_b"} {
		listed, err := stores.For(workspaceID).List("assistant", StatusPending, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(listed) != 1 {
			t.Fatalf("%s has %d proposals, want its own 1", workspaceID, len(listed))
		}
	}
}

// The reflection sweep covers every tenant but touches one at a time: each
// workspace's agents are listed, tailed, and proposed into within that
// workspace. Reading one tenant's runs and proposing into another's queue would
// be a rule someone else could accept.
type workspaceAgents struct {
	byWorkspace map[string][]*agent.Definition
}

func (w workspaceAgents) All() []*agent.Definition {
	return w.byWorkspace[wsroot.PersonalWorkspaceID]
}

func (w workspaceAgents) AllWorkspaces() []string {
	out := make([]string, 0, len(w.byWorkspace))
	for id := range w.byWorkspace {
		out = append(out, id)
	}
	return out
}

func (w workspaceAgents) AllInWorkspace(workspaceID string) []*agent.Definition {
	return w.byWorkspace[workspaceID]
}

type workspaceTailer struct {
	byWorkspace map[string]map[string][]message.Event
}

func (w workspaceTailer) Tail(agentID string, n int) ([]message.Event, error) {
	return w.byWorkspace[wsroot.PersonalWorkspaceID][agentID], nil
}

func (w workspaceTailer) TailInWorkspace(workspaceID, agentID string, n int) ([]message.Event, error) {
	return w.byWorkspace[workspaceID][agentID], nil
}

func sweepRun(agentID, text string) []message.Event {
	now := time.Now()
	return []message.Event{
		{AgentID: agentID, SessionID: "s1", Type: "message.in", Payload: map[string]any{"text": text, "channel": "http"}, Timestamp: now},
		{AgentID: agentID, SessionID: "s1", Type: "tool.call", Payload: map[string]any{"name": "web_search"}, Timestamp: now},
		{AgentID: agentID, SessionID: "s1", Type: "message.out", Payload: map[string]any{"text": "Use these steps:\n1. " + text + "\n2. Compare estimates.\n3. Summarize risks."}, Timestamp: now},
	}
}

func TestTheReflectionSweepProposesIntoEachWorkspacesOwnQueue(t *testing.T) {
	stores, _ := newStores(t)
	def := func() *agent.Definition {
		return &agent.Definition{
			ID: "researcher", Name: "Researcher",
			Learning: agent.LearningConfig{Enabled: true, AutoPropose: true, MinChars: 20, MaxProposals: 2},
		}
	}
	sweeper := NewSweeper(SweeperConfig{
		Stores: stores,
		Agents: workspaceAgents{byWorkspace: map[string][]*agent.Definition{
			"ws_a": {def()}, "ws_b": {def()},
		}},
		Actions: workspaceTailer{byWorkspace: map[string]map[string][]message.Event{
			"ws_a": {"researcher": sweepRun("researcher", "Find the best repeatable way to brief alpha earnings")},
			"ws_b": {"researcher": sweepRun("researcher", "Find the best repeatable way to brief beta earnings")},
		}},
	})

	if _, err := sweeper.SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ workspace, own, foreign string }{
		{"ws_a", "alpha earnings", "beta earnings"},
		{"ws_b", "beta earnings", "alpha earnings"},
	} {
		listed, err := stores.For(tc.workspace).List("researcher", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(listed) == 0 {
			t.Fatalf("%s got no proposals from its own runs", tc.workspace)
		}
		for _, p := range listed {
			if contains(p.Content, tc.foreign) {
				t.Errorf("%s was proposed a lesson from another workspace's run: %q", tc.workspace, p.Content)
			}
		}
	}
	// The personal workspace ran nothing, so it learned nothing.
	personal, err := stores.For(wsroot.PersonalWorkspaceID).List("researcher", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(personal) != 0 {
		t.Fatalf("the personal queue collected tenants' proposals: %+v", personal)
	}
}

// A tailer with no tenant-aware surface is single-tenant. Falling back to the
// unscoped tail for a non-personal workspace would read the personal
// workspace's runs and propose them into that tenant's queue.
func TestASingleTenantTailerDoesNotFeedOtherWorkspaces(t *testing.T) {
	stores, _ := newStores(t)
	sweeper := NewSweeper(SweeperConfig{
		Stores: stores,
		Agents: workspaceAgents{byWorkspace: map[string][]*agent.Definition{
			"ws_a": {{
				ID: "researcher", Name: "Researcher",
				Learning: agent.LearningConfig{Enabled: true, AutoPropose: true, MinChars: 20, MaxProposals: 2},
			}},
		}},
		// fakeTailer has only the unscoped Tail.
		Actions: fakeTailer{events: map[string][]message.Event{
			"researcher": sweepRun("researcher", "Find the best repeatable way to brief personal earnings"),
		}},
	})
	if _, err := sweeper.SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	listed, err := stores.For("ws_a").List("researcher", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("a tenant was proposed the personal workspace's lessons: %+v", listed)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
