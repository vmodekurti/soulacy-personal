// observers_workspace_test.go — the cross-tenant isolation contract for the two
// process-wide learning observers.
//
// The workflow distiller and the strategy-fit collector watch one event hub
// that carries every tenant's runs. Before events carried a workspace they were
// pinned to the personal workspace, which was safe but meant no other tenant
// learned anything. These tests pin the replacement: routing is by the event's
// own workspace, and a workspace never reads or writes another's learning.
package studio

import (
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/internal/wsroot"
	"github.com/soulacy/soulacy/pkg/message"
)

// perWorkspaceMacros builds a resolver backed by one file per workspace, which
// is what the gateway's resolver does with wsroot.File.
func perWorkspaceMacros(t *testing.T) (MacroStores, map[string]*MacroStore) {
	t.Helper()
	dir := t.TempDir()
	stores := map[string]*MacroStore{}
	return func(workspaceID string) *MacroStore {
		if existing, ok := stores[workspaceID]; ok {
			return existing
		}
		store := NewMacroStore(filepath.Join(dir, workspaceID+".json"))
		stores[workspaceID] = store
		return store
	}, stores
}

func distilledRun(d *WorkflowDistiller, workspaceID, agentID, sessionID, intent string, tools ...string) {
	d.Observe(message.Event{
		Type: "message.in", WorkspaceID: workspaceID, AgentID: agentID, SessionID: sessionID,
		Payload: message.Message{Parts: message.Text(intent)},
	})
	for _, name := range tools {
		d.Observe(message.Event{
			Type: "tool.call", WorkspaceID: workspaceID, AgentID: agentID, SessionID: sessionID,
			Payload: message.ToolCall{Name: name},
		})
	}
	d.Observe(message.Event{
		Type: "run.completed", WorkspaceID: workspaceID, AgentID: agentID, SessionID: sessionID,
		Payload: map[string]any{"run_id": workspaceID + "-" + sessionID, "success": true},
	})
}

// The core contract: what one tenant's runs teach Studio is not visible to
// another. A macro names the tools an internal workflow uses, so leaking one is
// leaking the shape of another company's automation.
func TestDistilledMacrosStayInTheWorkspaceThatProducedThem(t *testing.T) {
	resolver, stores := perWorkspaceMacros(t)
	d := NewWorkflowDistiller(resolver)

	distilledRun(d, "ws_a", "bot", "s1", "reconcile the alpha ledger", "alpha.fetch", "alpha.post")
	distilledRun(d, "ws_b", "bot", "s1", "publish the beta changelog", "beta.render", "beta.publish")
	d.Wait()

	for _, tc := range []struct{ workspace, ownTool, foreignTool string }{
		{"ws_a", "alpha.fetch", "beta.render"},
		{"ws_b", "beta.render", "alpha.fetch"},
	} {
		store := stores[tc.workspace]
		if store == nil {
			t.Fatalf("%s never got a store", tc.workspace)
		}
		patterns := store.All()
		if len(patterns) != 1 {
			t.Fatalf("%s holds %d patterns, want its own 1: %+v", tc.workspace, len(patterns), patterns)
		}
		var own, foreign bool
		for _, tool := range patterns[0].Tools {
			own = own || tool == tc.ownTool
			foreign = foreign || tool == tc.foreignTool
		}
		if !own {
			t.Errorf("%s lost its own macro: %+v", tc.workspace, patterns[0].Tools)
		}
		if foreign {
			t.Errorf("%s learned another workspace's workflow: %+v", tc.workspace, patterns[0].Tools)
		}
	}
}

// The same agent and session ID in two workspaces are two runs. Keying by agent
// and session alone would interleave their tool calls into one macro that
// belongs to neither.
func TestConcurrentRunsSharingIDsAcrossWorkspacesDoNotInterleave(t *testing.T) {
	resolver, stores := perWorkspaceMacros(t)
	d := NewWorkflowDistiller(resolver)

	open := func(workspaceID string) {
		d.Observe(message.Event{
			Type: "message.in", WorkspaceID: workspaceID, AgentID: "bot", SessionID: "shared",
			Payload: message.Message{Parts: message.Text("do the useful thing")},
		})
	}
	call := func(workspaceID, tool string) {
		d.Observe(message.Event{
			Type: "tool.call", WorkspaceID: workspaceID, AgentID: "bot", SessionID: "shared",
			Payload: message.ToolCall{Name: tool},
		})
	}
	// Interleaved on purpose: this is what a shared hub actually delivers.
	open("ws_a")
	open("ws_b")
	call("ws_a", "alpha.one")
	call("ws_b", "beta.one")
	call("ws_a", "alpha.two")
	call("ws_b", "beta.two")
	for _, workspaceID := range []string{"ws_a", "ws_b"} {
		d.Observe(message.Event{
			Type: "run.completed", WorkspaceID: workspaceID, AgentID: "bot", SessionID: "shared",
			Payload: map[string]any{"run_id": workspaceID + "-r1", "success": true},
		})
	}
	d.Wait()

	for workspaceID, prefix := range map[string]string{"ws_a": "alpha.", "ws_b": "beta."} {
		patterns := stores[workspaceID].All()
		if len(patterns) != 1 {
			t.Fatalf("%s holds %d patterns: %+v", workspaceID, len(patterns), patterns)
		}
		if len(patterns[0].Tools) != 2 {
			t.Fatalf("%s recorded %v, want exactly its own two tools", workspaceID, patterns[0].Tools)
		}
		for _, tool := range patterns[0].Tools {
			if len(tool) < len(prefix) || tool[:len(prefix)] != prefix {
				t.Errorf("%s macro contains another workspace's tool %q", workspaceID, tool)
			}
		}
	}
}

// An event with no workspace is a single-tenant event, not an unowned one. It
// belongs to the personal workspace — the same answer every other store in the
// codebase gives — rather than being dropped or written somewhere shared.
func TestEventsWithoutAWorkspaceLandInPersonal(t *testing.T) {
	resolver, stores := perWorkspaceMacros(t)
	d := NewWorkflowDistiller(resolver)
	distilledRun(d, "", "bot", "s1", "do the single-tenant thing", "one.a", "one.b")
	d.Wait()

	store := stores[wsroot.PersonalWorkspaceID]
	if store == nil || len(store.All()) != 1 {
		t.Fatalf("an unworkspaced event did not land in the personal workspace: %+v", stores)
	}
}

// A workspace whose store cannot be built — Studio learning off, or an
// unwritable path — drops the observation. It must never fall back to another
// tenant's file.
func TestAnUnresolvableWorkspaceDropsRatherThanFallsBack(t *testing.T) {
	dir := t.TempDir()
	personal := NewMacroStore(filepath.Join(dir, "personal.json"))
	d := NewWorkflowDistiller(func(workspaceID string) *MacroStore {
		if workspaceID == wsroot.PersonalWorkspaceID {
			return personal
		}
		return nil
	})
	distilledRun(d, "ws_a", "bot", "s1", "reconcile the alpha ledger", "alpha.fetch", "alpha.post")
	d.Wait()
	if got := personal.All(); len(got) != 0 {
		t.Fatalf("an unresolvable workspace's macro fell back to another store: %+v", got)
	}
}

// Strategy-fit observations are reliability history. One tenant's failures
// must not suppress a strategy for another, and the run-ID dedup must not let
// one tenant's run swallow another's.
func TestStrategyFitObservationsAreWorkspaceScoped(t *testing.T) {
	dir := t.TempDir()
	stores := map[string]*StrategyFitStore{}
	resolver := func(workspaceID string) *StrategyFitStore {
		if existing, ok := stores[workspaceID]; ok {
			return existing
		}
		store := NewStrategyFitStore(filepath.Join(dir, workspaceID+".json"))
		stores[workspaceID] = store
		return store
	}
	// The resolver is handed the event's workspace, so an agent ID shared by
	// two tenants resolves to each tenant's own model.
	var resolvedFor []string
	collector := NewStrategyFitCollector(resolver, func(workspaceID, agentID string) (string, string, bool) {
		resolvedFor = append(resolvedFor, workspaceID+"/"+agentID)
		return "model-" + workspaceID, "react", true
	})

	// Same agent ID, same run ID, different tenants.
	for _, workspaceID := range []string{"ws_a", "ws_b"} {
		collector.Observe(message.Event{
			Type: "run.completed", WorkspaceID: workspaceID, AgentID: "bot", SessionID: "s1",
			Payload: map[string]any{"run_id": "shared-run", "provider": "p", "success": workspaceID == "ws_a"},
		})
	}
	collector.Wait()

	if len(resolvedFor) != 2 || resolvedFor[0] != "ws_a/bot" || resolvedFor[1] != "ws_b/bot" {
		t.Fatalf("the resolver was not called per workspace: %v", resolvedFor)
	}
	for _, tc := range []struct {
		workspace       string
		passes, failure int
	}{
		{"ws_a", 1, 0},
		{"ws_b", 0, 1},
	} {
		rows := stores[tc.workspace].ForModel("model-" + tc.workspace)
		if len(rows) != 1 {
			t.Fatalf("%s recorded %d rows for its own model, want 1: %+v", tc.workspace, len(rows), rows)
		}
		if rows[0].Passes != tc.passes || rows[0].Failures != tc.failure {
			t.Errorf("%s outcome = %+v, want %d/%d", tc.workspace, rows[0], tc.passes, tc.failure)
		}
		// The other tenant's model must be entirely absent from this file.
		other := "ws_a"
		if tc.workspace == "ws_a" {
			other = "ws_b"
		}
		if got := stores[tc.workspace].ForModel("model-" + other); len(got) != 0 {
			t.Errorf("%s holds another workspace's reliability history: %+v", tc.workspace, got)
		}
	}
}
