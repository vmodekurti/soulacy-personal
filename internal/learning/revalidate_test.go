// revalidate_test.go — MU-025 criterion 5: background jobs revalidate
// workspace status and policy before committing, not at admission.
package learning

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// mutableAgents lets a test change what the sweeper will re-read at commit
// time, which is the whole point: a definition captured before the tail is not
// the one a later reader loads.
type mutableAgents struct {
	byWorkspace map[string][]*agent.Definition
}

func (m *mutableAgents) All() []*agent.Definition { return m.byWorkspace["ws_personal"] }
func (m *mutableAgents) AllWorkspaces() []string {
	out := make([]string, 0, len(m.byWorkspace))
	for id := range m.byWorkspace {
		out = append(out, id)
	}
	return out
}
func (m *mutableAgents) AllInWorkspace(workspaceID string) []*agent.Definition {
	return m.byWorkspace[workspaceID]
}

func learningAgent(id string, enabled bool) *agent.Definition {
	return &agent.Definition{
		ID: id, Name: id,
		Learning: agent.LearningConfig{Enabled: enabled, AutoPropose: enabled, MinChars: 20, MaxProposals: 2},
	}
}

func sweeperFor(t *testing.T, agents *mutableAgents, events map[string]map[string][]message.Event) (*Sweeper, *Stores) {
	t.Helper()
	return sweeperWithTailHook(t, agents, events, nil)
}

// mutatingTailer changes the world in the window that matters: AFTER the loop
// has captured its agent definition and BEFORE anything commits. Mutating from
// the workspace-status hook instead would fire before the agent list is even
// read, so the loop would simply never see the agent — which proves the loop
// skips absent agents, not that the commit re-reads them.
type mutatingTailer struct {
	inner  workspaceTailer
	onTail func()
}

func (m mutatingTailer) Tail(agentID string, n int) ([]message.Event, error) {
	events, err := m.inner.Tail(agentID, n)
	if m.onTail != nil {
		m.onTail()
	}
	return events, err
}

func (m mutatingTailer) TailInWorkspace(workspaceID, agentID string, n int) ([]message.Event, error) {
	events, err := m.inner.TailInWorkspace(workspaceID, agentID, n)
	if m.onTail != nil {
		m.onTail()
	}
	return events, err
}

func sweeperWithTailHook(t *testing.T, agents *mutableAgents, events map[string]map[string][]message.Event, onTail func()) (*Sweeper, *Stores) {
	t.Helper()
	stores, _ := newStores(t)
	return NewSweeper(SweeperConfig{
		Stores: stores, Agents: agents,
		Actions: mutatingTailer{inner: workspaceTailer{byWorkspace: events}, onTail: onTail},
		Logger:  zap.NewNop(),
	}), stores
}

func proposalCount(t *testing.T, stores *Stores, workspaceID, agentID string) int {
	t.Helper()
	listed, err := stores.For(workspaceID).List(agentID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	return len(listed)
}

// A suspended workspace must not receive derived learning, and finding that
// out must not stop the sweep for everybody else.
func TestASuspendedWorkspaceIsSkippedWithoutStoppingTheSweep(t *testing.T) {
	agents := &mutableAgents{byWorkspace: map[string][]*agent.Definition{
		"ws_suspended": {learningAgent("researcher", true)},
		"ws_active":    {learningAgent("researcher", true)},
	}}
	events := map[string]map[string][]message.Event{
		"ws_suspended": {"researcher": sweepRun("researcher", "review the suspended ledger carefully")},
		"ws_active":    {"researcher": sweepRun("researcher", "review the active ledger carefully")},
	}
	sweeper, stores := sweeperFor(t, agents, events)
	sweeper.SetWorkspaceStatus(func(_ context.Context, workspaceID string) error {
		if workspaceID == "ws_suspended" {
			return &ErrWorkspaceNotCommittable{WorkspaceID: workspaceID, Reason: "workspace is suspended"}
		}
		return nil
	})

	if _, err := sweeper.SweepOnce(context.Background()); err != nil {
		t.Fatalf("one uncommittable workspace aborted the whole sweep: %v", err)
	}
	if got := proposalCount(t, stores, "ws_suspended", "researcher"); got != 0 {
		t.Fatalf("a suspended workspace received %d proposals", got)
	}
	if got := proposalCount(t, stores, "ws_active", "researcher"); got == 0 {
		t.Fatal("an active workspace was starved because another was suspended")
	}
}

// A status source that cannot be reached is not "everything is fine". A
// skipped sweep is recoverable; a write into a suspended tenant is not.
func TestAnUnreachableStatusSourceFailsClosed(t *testing.T) {
	agents := &mutableAgents{byWorkspace: map[string][]*agent.Definition{
		"ws_a": {learningAgent("researcher", true)},
	}}
	events := map[string]map[string][]message.Event{
		"ws_a": {"researcher": sweepRun("researcher", "review the ledger carefully before posting")},
	}
	sweeper, stores := sweeperFor(t, agents, events)
	sweeper.SetWorkspaceStatus(func(context.Context, string) error {
		return errors.New("tenancy database unreachable")
	})

	if _, err := sweeper.SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := proposalCount(t, stores, "ws_a", "researcher"); got != 0 {
		t.Fatalf("proposals were committed while the status source was unreachable: %d", got)
	}
}

// THE property revalidation is about. The sweep reads a definition, tails
// thousands of events, and only then writes. An operator turning learning off
// in between must be honoured — a job that checked once at the start is
// enforcing a policy nobody is looking at.
func TestLearningTurnedOffDuringASweepIsHonouredAtCommitTime(t *testing.T) {
	live := learningAgent("researcher", true)
	agents := &mutableAgents{byWorkspace: map[string][]*agent.Definition{
		"ws_a": {live},
	}}
	events := map[string]map[string][]message.Event{
		"ws_a": {"researcher": sweepRun("researcher", "review the ledger carefully before posting")},
	}
	// Turned off AFTER the loop captured `live` and before anything commits.
	sweeper, stores := sweeperWithTailHook(t, agents, events, func() {
		agents.byWorkspace["ws_a"] = []*agent.Definition{learningAgent("researcher", false)}
	})

	if _, err := sweeper.SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := proposalCount(t, stores, "ws_a", "researcher"); got != 0 {
		t.Fatalf("%d proposals were committed for an agent whose learning was turned off mid-sweep", got)
	}
	// The captured pointer still says enabled — which is exactly why
	// re-checking it rather than re-reading would have proved nothing.
	if !live.Learning.Enabled {
		t.Fatal("the test did not exercise the re-read path")
	}
}

// An agent deleted between the tail and the write has real evidence and
// nothing left for a proposal to be about.
func TestAnAgentDeletedMidSweepGetsNoProposals(t *testing.T) {
	agents := &mutableAgents{byWorkspace: map[string][]*agent.Definition{
		"ws_a": {learningAgent("researcher", true)},
	}}
	events := map[string]map[string][]message.Event{
		"ws_a": {"researcher": sweepRun("researcher", "review the ledger carefully before posting")},
	}
	// Deleted AFTER the loop captured it and before anything commits.
	sweeper, stores := sweeperWithTailHook(t, agents, events, func() {
		agents.byWorkspace["ws_a"] = nil
	})

	if _, err := sweeper.SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := proposalCount(t, stores, "ws_a", "researcher"); got != 0 {
		t.Fatalf("%d proposals were committed for a deleted agent", got)
	}
}

// No status hook is the personal-deployment answer: one workspace, running the
// sweep, which cannot be suspended from under itself.
func TestWithNoStatusHookTheSweepProceeds(t *testing.T) {
	agents := &mutableAgents{byWorkspace: map[string][]*agent.Definition{
		"ws_a": {learningAgent("researcher", true)},
	}}
	events := map[string]map[string][]message.Event{
		"ws_a": {"researcher": sweepRun("researcher", "review the ledger carefully before posting")},
	}
	sweeper, stores := sweeperFor(t, agents, events)
	if _, err := sweeper.SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := proposalCount(t, stores, "ws_a", "researcher"); got == 0 {
		t.Fatal("an unconfigured sweeper stopped proposing")
	}
}

// Revalidation must read the agent from ITS OWN workspace. Reading by ID
// across tenants would let one workspace's disabled agent block — or one
// workspace's enabled agent authorise — another's.
func TestRevalidationReadsTheAgentFromItsOwnWorkspace(t *testing.T) {
	agents := &mutableAgents{byWorkspace: map[string][]*agent.Definition{
		"ws_off": {learningAgent("shared-bot", false)},
		"ws_on":  {learningAgent("shared-bot", true)},
	}}
	sweeper, _ := sweeperFor(t, agents, nil)

	if _, err := sweeper.revalidateAgent("ws_on", learningAgent("shared-bot", true)); err != nil {
		t.Fatalf("ws_on was refused because another workspace's agent is off: %v", err)
	}
	if _, err := sweeper.revalidateAgent("ws_off", learningAgent("shared-bot", true)); err == nil {
		t.Fatal("ws_off was permitted because another workspace's agent is on")
	}
}

// TestNoCrossWorkspaceSharingSurfaceExists is MU-025 criteria 2 and 3 as a
// structural guard.
//
// "Cross-workspace learning is disabled by default and cannot occur through
// aggregate caches or vector similarity queries" is currently true because
// there is NO WAY TO EXPRESS IT: every read and write goes through
// Stores.For(workspaceID), which returns one tenant's store. That is a
// stronger guarantee than a default, and it is worth keeping structurally
// rather than by memory.
//
// Criterion 3 — "organization-wide sharing requires an explicit policy, source
// attribution, redaction, and opt-in destination" — is therefore vacuous
// today, and deliberately so: see docs/LEARNING_LINEAGE.md for why building a
// policy framework for an unspecified feature would be speculative design. If
// somebody adds a sharing surface, this test fails and they have to come read
// that decision before shipping it.
func TestNoCrossWorkspaceSharingSurfaceExists(t *testing.T) {
	fileSet := token.NewFileSet()
	packageFiles, err := parser.ParseDir(fileSet, ".", func(info os.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range packageFiles {
		for name, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Type.Params == nil || !fn.Name.IsExported() {
					continue
				}
				workspaceParams := 0
				for _, field := range fn.Type.Params.List {
					ident, ok := field.Type.(*ast.Ident)
					if !ok || ident.Name != "string" {
						continue
					}
					for _, paramName := range field.Names {
						if strings.Contains(strings.ToLower(paramName.Name), "workspace") {
							workspaceParams++
						}
					}
				}
				// Two workspaces in one signature is the shape a sharing
				// surface has: "copy from here to there". One is ordinary
				// scoping; zero is scoped by the receiver.
				if workspaceParams > 1 {
					t.Errorf("%s: %s takes %d workspace parameters — a cross-workspace surface needs the policy, attribution, redaction and opt-in destination MU-025 criterion 3 requires",
						name, fn.Name.Name, workspaceParams)
				}
			}
		}
	}
}
