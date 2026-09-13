package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/learning"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

func learningEngine(t *testing.T) (*Engine, *fakeHandleProvider, *agent.Definition, *learning.Notebook, context.Context) {
	t.Helper()
	def := &agent.Definition{ID: "learner", Name: "Learner", Enabled: true, LLM: agent.LLMConfig{Provider: "test", Model: "fake-model"}, MaxTurns: 4, Learning: agent.LearningConfig{Enabled: true, AutoPropose: true, MaxProposals: 2}}
	e, p := newHandleTestEngine(t, def)
	n, err := learning.OpenNotebook(filepath.Join(t.TempDir(), "notebook.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	e.SetLearningNotebook(n, true)
	ctx := WithPrincipal(context.Background(), Principal{Subject: "alice", Role: "operator"})
	return e, p, def, n, ctx
}
func runtimeDraft() learning.Draft {
	return learning.Draft{Key: "units", Kind: "preference", Title: "Measurement units", Trigger: "Responding with measurements", Content: "Prefer SI units by default.", Verification: "Check whether the current user explicitly requests other units.", Citations: []learning.Citation{{SourceID: "user", Quote: "Please always use metric measurements."}}}
}
func draftArgs(t *testing.T, d learning.Draft) map[string]any {
	t.Helper()
	b, _ := json.Marshal(d)
	var args map[string]any
	if err := json.Unmarshal(b, &args); err != nil {
		t.Fatal(err)
	}
	return args
}
func TestLearningActualRunProposesThenReusesApprovedPrivateLesson(t *testing.T) {
	e, p, def, n, ctx := learningEngine(t)
	p.responses = []llm.CompletionResponse{{ToolCalls: []message.ToolCall{{ID: "c1", Name: "learning.propose", Arguments: draftArgs(t, runtimeDraft())}}}, {Content: "I prepared a lesson for your review."}}
	msg := testUserMessage(def.ID, "session-one", "Please always use metric measurements.")
	if _, err := e.Handle(ctx, msg); err != nil {
		t.Fatal(err)
	}
	scope := learning.Scope{Owner: "alice", AgentID: def.ID}
	pending, err := n.List(ctx, scope, "pending")
	if err != nil || len(pending) != 1 {
		t.Fatal(pending, err)
	}
	if len(p.requestsSnapshot()) != 2 {
		t.Fatal("learning bypassed the task's model-call budget")
	}
	requests := p.requestsSnapshot()
	if !strings.Contains(requests[0].Messages[0].Content, "Private learning notebook") {
		t.Fatal("missing learning guide")
	}
	_, err = n.Review(ctx, scope, pending[0].ID, "approve")
	if err != nil {
		t.Fatal(err)
	}
	p.responses = []llm.CompletionResponse{{Content: "A second answer"}}
	msg.ID = "msg-two"
	msg.SessionID = "session-two"
	msg.Parts = message.Text("Give me measurements for the project")
	if _, err := e.Handle(ctx, msg); err != nil {
		t.Fatal(err)
	}
	last := p.requestsSnapshot()[len(p.requestsSnapshot())-1]
	if !strings.Contains(last.Messages[0].Content, "Prefer SI units by default.") {
		t.Fatal("approved preference not reused")
	}
	got, _ := n.Get(ctx, scope, pending[0].ID)
	if got.Uses != 1 {
		t.Fatal(got.Uses)
	}
	_, _ = n.Review(ctx, scope, pending[0].ID, "archive")
	msg.ID = "msg-three"
	msg.SessionID = "session-three"
	if _, err := e.Handle(ctx, msg); err != nil {
		t.Fatal(err)
	}
	last = p.requestsSnapshot()[len(p.requestsSnapshot())-1]
	if strings.Contains(last.Messages[0].Content, "Prefer SI units by default.") {
		t.Fatal("disabled guidance still injected")
	}
}

func TestLearningToolBoundariesAndObservedSources(t *testing.T) {
	e, _, def, n, ctx := learningEngine(t)
	msg := testUserMessage(def.ID, "s", "Please always use metric measurements.")
	ctx = llm.WithCallMetadata(ctx, llm.CallMetadata{RunID: "r"})
	ctx = context.WithValue(ctx, inboundMsgKey{}, msg)
	ctx = e.startLearningRun(ctx, def.Clone(), msg)
	e.observeLearningTool(ctx, def, message.ToolCall{Name: "fetch_url"}, "A real tool observation for reference.", nil)
	e.observeLearningTool(ctx, def, message.ToolCall{Name: "safe_undo.prepare"}, "private before values", nil)
	s, err := e.runTool(ctx, def, "s", message.ToolCall{Name: "learning.search", Arguments: map[string]any{"query": "metric"}})
	if err != nil || !strings.Contains(s, "tool-1") || strings.Contains(s, "private before") {
		t.Fatal(s, err)
	}
	call := message.ToolCall{Name: "learning.propose", Arguments: draftArgs(t, runtimeDraft())}
	for _, principal := range []Principal{{Subject: "alice", Role: "viewer"}, {Subject: "bob", Role: "operator"}, {Subject: "alice", Role: "operator", Scopes: []string{"chat"}}, {Role: "admin"}} {
		if _, err := e.runTool(WithPrincipal(ctx, principal), def, "s", call); err == nil {
			t.Fatal("principal bypass", principal)
		}
	}
	for _, channel := range []string{"telegram", "slack", "webhook"} {
		m := msg
		m.Channel = channel
		blocked := e.startLearningRun(ctx, def.Clone(), m)
		if _, err := e.runTool(blocked, def, "s", call); err == nil {
			t.Fatal(channel)
		}
	}
	disabled := def.Clone()
	disabled.Learning.Enabled = false
	blocked := e.startLearningRun(ctx, disabled, msg)
	if _, err := e.runTool(blocked, disabled, "s", call); err == nil {
		t.Fatal("disabled nested agent inherited collector")
	}
	if _, err := e.runTool(ctx, def, "s", call); err != nil {
		t.Fatal(err)
	}
	all, _ := n.List(ctx, learning.Scope{Owner: "alice", AgentID: def.ID}, "")
	if len(all) != 1 {
		t.Fatal(all)
	}
	if _, err := e.sessionSearch(ctx, map[string]any{"query": "metric", "agent_id": "other"}); err == nil {
		t.Fatal("cross-agent recall")
	}
	if _, err := e.runTool(ctx, def, "s", message.ToolCall{Name: "learning.read", Arguments: map[string]any{"id": all[0].ID}}); !errors.Is(err, learning.ErrLessonNotFound) {
		t.Fatal("pending read", err)
	}
	if !isSideEffectingTool("learning.propose") {
		t.Fatal("proposal omitted from side effects")
	}
}

func TestTeachLearningUsesGovernedToolFreeBoundedModelCall(t *testing.T) {
	e, p, def, n, ctx := learningEngine(t)
	d := runtimeDraft()
	raw, _ := json.Marshal(map[string]any{"lesson": d})
	p.responses = []llm.CompletionResponse{{Content: string(raw)}}
	scope := learning.Scope{Owner: "alice", AgentID: def.ID}
	l, created, err := e.TeachLearning(ctx, scope, "Please always use metric measurements.")
	if err != nil || !created || l.Status != "pending" {
		t.Fatal(l, created, err)
	}
	req := p.requestsSnapshot()[0]
	if len(req.Tools) != 0 || req.Stream || req.MaxTokens > 2200 || req.ResponseFormat != "json" || len(p.requestsSnapshot()) != 1 {
		t.Fatal(req)
	}
	if all, _ := n.List(ctx, scope, "active"); len(all) != 0 {
		t.Fatal(all)
	}
	for _, response := range []llm.CompletionResponse{{Content: "not JSON"}, {Content: `{"lesson":null}`, ToolCalls: []message.ToolCall{{Name: "shell_exec"}}}, {Content: `{"lesson":{"key":"invented"}}`}} {
		p.responses = []llm.CompletionResponse{response}
		p.requests = nil
		if _, _, err := e.TeachLearning(ctx, scope, "Please always use metric measurements."); err == nil {
			t.Fatal(response)
		}
	}
	p.responses = []llm.CompletionResponse{{Content: `{"lesson":null}`}}
	p.requests = nil
	none, created, err := e.TeachLearning(ctx, scope, "There is no reusable procedure here.")
	if err != nil || none != nil || created {
		t.Fatal(none, created, err)
	}
}

func TestLearningSourceCollectionIsBoundedAndKeepsArguments(t *testing.T) {
	e, _, def, _, ctx := learningEngine(t)
	ctx = e.startLearningRun(ctx, def.Clone(), testUserMessage(def.ID, "s", "A short user request for a workflow."))
	for range 20 {
		e.observeLearningTool(ctx, def, message.ToolCall{Name: "fetch_url", Arguments: map[string]any{"url": "https://example.com/status"}}, strings.Repeat("z", 5000), nil)
	}
	r := ctx.Value(learningRunKey{}).(*learningRun)
	if len(r.sources) != 9 || len(r.sources[1].Text) > 2030 || !strings.Contains(r.sources[1].Text, "https://example.com/status") {
		t.Fatal("unbounded or untraceable tool observations", r.sources)
	}
}

func TestTeachLearningRefusesUntrustedScopesAndDisabledAuthentication(t *testing.T) {
	e, p, def, n, ctx := learningEngine(t)
	scope := learning.Scope{Owner: "alice", AgentID: def.ID}
	for _, principal := range []Principal{{}, {Subject: "bob", Role: "admin"}, {Subject: "alice", Role: "viewer"}, {Subject: "alice", Role: "operator", Scopes: []string{"memory:read", "agents:read"}}} {
		if _, _, err := e.TeachLearning(WithPrincipal(ctx, principal), scope, "Please always use metric measurements."); err == nil {
			t.Fatal("unexpected authorization", principal)
		}
	}
	e.SetLearningNotebook(n, false)
	if _, _, err := e.TeachLearning(ctx, scope, "Please always use metric measurements."); err == nil {
		t.Fatal("authentication disabled")
	}
	if len(p.requestsSnapshot()) != 0 {
		t.Fatal("unauthorized model spending")
	}
}
