package autopilot

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeDraftModel struct {
	reply  string
	err    error
	prompt string
}

func (f *fakeDraftModel) Complete(_ context.Context, prompt string) (string, error) {
	f.prompt = prompt
	return f.reply, f.err
}

var draftAgents = []DraftAgent{{ID: "research", Name: "Researcher"}, {ID: "review", Name: "Reviewer"}}

func TestDraftGoalPlanReturnsOrderedValidatedSteps(t *testing.T) {
	model := &fakeDraftModel{reply: "```json\n" + `{"title":" Trip plan ","objective":"Plan the trip","steps":[
		{"title":"Research","agent_id":"research","prompt":"Collect options"},
		{"title":"Review","agent_id":"review","prompt":"Check the options"}]}` + "\n```"}
	draft, err := DraftGoalPlan(context.Background(), model, "Help me plan a trip", draftAgents)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Title != "Trip plan" || draft.Objective != "Plan the trip" {
		t.Fatalf("title/objective not trimmed: %+v", draft)
	}
	if len(draft.Steps) != 2 || draft.Steps[0].ID != "step-1" || draft.Steps[1].ID != "step-2" || draft.Steps[1].AgentID != "review" {
		t.Fatalf("unexpected steps: %+v", draft.Steps)
	}
	if !strings.Contains(model.prompt, `id="research"`) || !strings.Contains(model.prompt, "Help me plan a trip") {
		t.Fatalf("prompt missing agents or request: %s", model.prompt)
	}
}

func TestDraftGoalPlanRejectsAgentsTheOwnerCannotAssign(t *testing.T) {
	model := &fakeDraftModel{reply: `{"title":"T","objective":"O","steps":[{"title":"x","agent_id":"system","prompt":"p"}]}`}
	_, err := DraftGoalPlan(context.Background(), model, "goal", draftAgents)
	if !errors.Is(err, ErrDraftUnusable) {
		t.Fatalf("want ErrDraftUnusable, got %v", err)
	}
}

func TestDraftGoalPlanRejectsUnusableReplies(t *testing.T) {
	cases := map[string]string{
		"not json":       "I cannot help",
		"no steps":       `{"title":"T","objective":"O","steps":[]}`,
		"empty prompt":   `{"title":"T","objective":"O","steps":[{"title":"x","agent_id":"research","prompt":" "}]}`,
		"missing title":  `{"objective":"O","steps":[{"title":"x","agent_id":"research","prompt":"p"}]}`,
		"too many steps": `{"title":"T","objective":"O","steps":[` + strings.Repeat(`{"title":"x","agent_id":"research","prompt":"p"},`, 12) + `{"title":"x","agent_id":"research","prompt":"p"}]}`,
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := DraftGoalPlan(context.Background(), &fakeDraftModel{reply: reply}, "goal", draftAgents)
			if !errors.Is(err, ErrDraftUnusable) {
				t.Fatalf("want ErrDraftUnusable, got %v", err)
			}
		})
	}
}

func TestDraftGoalPlanRejectsBadRequestsBeforeCallingModel(t *testing.T) {
	model := &fakeDraftModel{reply: "{}"}
	if _, err := DraftGoalPlan(context.Background(), model, "   ", draftAgents); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty request: want ErrInvalid, got %v", err)
	}
	if _, err := DraftGoalPlan(context.Background(), model, strings.Repeat("x", maxDraftRequestBytes+1), draftAgents); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize request: want ErrInvalid, got %v", err)
	}
	if _, err := DraftGoalPlan(context.Background(), model, "goal", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no agents: want ErrInvalid, got %v", err)
	}
	if model.prompt != "" {
		t.Fatal("model must not be called for invalid requests")
	}
}

func TestDraftGoalPlanPropagatesModelFailure(t *testing.T) {
	boom := errors.New("provider down")
	_, err := DraftGoalPlan(context.Background(), &fakeDraftModel{err: boom}, "goal", draftAgents)
	if !errors.Is(err, boom) {
		t.Fatalf("want wrapped provider error, got %v", err)
	}
}
