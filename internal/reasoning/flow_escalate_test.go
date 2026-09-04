package reasoning

import (
	"context"
	"errors"
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestCompileFlow_EscalateValidation(t *testing.T) {
	// escalate without a declared escalation node is refused at compile time.
	if _, err := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{{ID: "a", Tool: "t", OnError: "escalate"}},
	}); err == nil {
		t.Fatal("expected error: escalate with no escalation node")
	}
	// an escalation target that does not exist is refused.
	if _, err := CompileFlow(sdkr.FlowSpec{
		Nodes:      []sdkr.FlowNode{{ID: "a", Tool: "t"}},
		Escalation: "ghost",
	}); err == nil {
		t.Fatal("expected error: unknown escalation node")
	}
	// the valid shape compiles.
	if _, err := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "a", Tool: "t", OnError: "escalate"},
			{ID: "notify", Tool: "t2"},
		},
		Escalation: "notify",
	}); err != nil {
		t.Fatalf("valid escalate spec refused: %v", err)
	}
}

func TestRunFlow_EscalateRoutesToEscalationNode(t *testing.T) {
	g, err := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "work", Tool: "t", Output: "out", OnError: "escalate"},
			{ID: "notify", Tool: "t2", Input: `{"reason": {{ toJson .failure.error }}, "from": {{ toJson .failure.node }}}`},
		},
		Edges:      []sdkr.FlowEdge{{From: "work", To: "notify"}, {From: "notify", To: "end"}},
		Escalation: "notify",
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &recRunner{
		errs:    map[string]error{"work": errors.New("boom: upstream shape drift")},
		results: map[string]string{"notify": `{"escalated":true}`},
	}
	out, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{})
	if err != nil {
		t.Fatalf("escalate should not abort the flow: %v", err)
	}
	ids := nodeIDs(r.calls)
	if len(ids) != 2 || ids[0] != "work" || ids[1] != "notify" {
		t.Fatalf("expected work→notify, got %v", ids)
	}
	// The escalation node's templates can address the recorded failure.
	if !strings.Contains(r.calls[1], "boom: upstream shape drift") || !strings.Contains(r.calls[1], `"work"`) {
		t.Fatalf("failure var not rendered into escalation input: %q", r.calls[1])
	}
	if string(out) != `{"escalated":true}` {
		t.Fatalf("final output should be the escalation node's result, got %s", out)
	}
}

func TestRunFlow_EscalationNodeFailureAborts(t *testing.T) {
	// The escalation node failing must abort (never re-escalate into a loop),
	// even when it also declares on_error: escalate.
	g, err := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "work", Tool: "t", OnError: "escalate"},
			{ID: "notify", Tool: "t2", OnError: "escalate"},
		},
		Edges:      []sdkr.FlowEdge{{From: "work", To: "end"}},
		Escalation: "notify",
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &recRunner{errs: map[string]error{
		"work":   errors.New("boom"),
		"notify": errors.New("notify also broken"),
	}}
	_, err = RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{})
	if err == nil || !strings.Contains(err.Error(), "notify") {
		t.Fatalf("expected abort at escalation node, got %v", err)
	}
	if got := nodeIDs(r.calls); len(got) != 2 {
		t.Fatalf("expected exactly work,notify — no loop — got %v", got)
	}
}

func TestRunFlow_RepairPredicateHook(t *testing.T) {
	spec := sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "a", Tool: "t", Output: "out"},
			{ID: "b", Tool: "t"},
			{ID: "c", Tool: "t"},
		},
		Edges: []sdkr.FlowEdge{
			// .out is a plain string, so .out.field fails to render.
			{From: "a", To: "b", If: "{{.out.field}}"},
			{From: "a", To: "c"},
		},
	}
	results := map[string]string{"a": `"plain string"`}

	// take=true: the repaired decision takes the broken edge.
	g, err := CompileFlow(spec)
	if err != nil {
		t.Fatal(err)
	}
	r := &recRunner{results: results}
	var sawEdge string
	hooks := FlowHooks{RepairPredicate: func(_ context.Context, e sdkr.FlowEdge, renderErr error, _ map[string]any) (bool, bool) {
		sawEdge = e.From + "→" + e.To
		if renderErr == nil {
			t.Error("expected a render error")
		}
		return true, true
	}}
	if _, err := RunFlow(context.Background(), g, map[string]any{}, r.run, hooks); err != nil {
		t.Fatalf("repaired predicate should not abort: %v", err)
	}
	if got := nodeIDs(r.calls); len(got) != 2 || got[1] != "b" {
		t.Fatalf("take=true should route a→b, got %v", got)
	}
	if sawEdge != "a→b" {
		t.Fatalf("hook saw wrong edge: %q", sawEdge)
	}

	// take=false: the broken edge is skipped and the fallback edge routes.
	r = &recRunner{results: results}
	hooks.RepairPredicate = func(_ context.Context, _ sdkr.FlowEdge, _ error, _ map[string]any) (bool, bool) {
		return false, true
	}
	if _, err := RunFlow(context.Background(), g, map[string]any{}, r.run, hooks); err != nil {
		t.Fatal(err)
	}
	if got := nodeIDs(r.calls); len(got) != 2 || got[1] != "c" {
		t.Fatalf("take=false should fall through a→c, got %v", got)
	}

	// no hook (or ok=false): today's behavior — the render error aborts.
	r = &recRunner{results: results}
	if _, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{}); err == nil {
		t.Fatal("expected predicate render error to abort without a repair hook")
	}
}
