package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/agent"
)

type preparationProvider struct {
	*fakeHandleProvider
	profile llm.ModelProfile
	lookups atomic.Int32
}

type delayedPreparationStream struct {
	fakeHandleProvider
	cancelledEarly atomic.Bool
}

func (p *delayedPreparationStream) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	if !req.Stream {
		return &llm.CompletionResponse{Content: "unexpected synthesis"}, nil
	}
	ch := make(chan string, 1)
	go func() {
		defer close(ch)
		select {
		case <-ctx.Done():
			p.cancelledEarly.Store(true)
		case <-time.After(30 * time.Millisecond):
			ch <- "STREAM_READY"
		}
	}()
	return &llm.CompletionResponse{Stream: ch}, nil
}

func TestModelPreparationStreamingKeepsDeadlineUntilDrain(t *testing.T) {
	for _, timeout := range []time.Duration{time.Second, 5 * time.Millisecond} {
		t.Run(timeout.String(), func(t *testing.T) {
			e, d, _ := modelPreparationEngine(t)
			d.StreamReply = true
			e.loader.Register(d)
			p := &delayedPreparationStream{}
			e.llmRouter.Register(p)
			e.SetTimeoutHierarchy(time.Second, timeout, time.Second, time.Second)
			reply, err := e.Handle(context.Background(), testUserMessage(d.ID, "stream-deadline", "Return the supplied token."))
			if timeout == time.Second {
				if err != nil || p.cancelledEarly.Load() || !strings.Contains(flattenParts(reply.Parts), "STREAM_READY") {
					t.Fatal("stream cancelled before drain", reply, err)
				}
			} else if err == nil || !p.cancelledEarly.Load() || !strings.Contains(err.Error(), "streaming model response interrupted") {
				t.Fatal("interrupted stream claimed success", reply, err)
			}
		})
	}
}

func (p *preparationProvider) DefaultModel() string { return "selected-model" }
func (p *preparationProvider) ProfileModel(_ context.Context, model string) (llm.ModelProfile, error) {
	p.lookups.Add(1)
	result := p.profile
	result.Provider, result.Model = "test", model
	return result, nil
}
func modelPreparationEngine(t *testing.T) (*Engine, *agent.Definition, *preparationProvider) {
	t.Helper()
	d := &agent.Definition{ID: "model-aware", Name: "Model aware", Enabled: true, SystemPrompt: "Keep the release requirements exact.", Builtins: strListPtr(), MaxTurns: 2, LLM: agent.LLMConfig{Provider: "test", MaxTokens: 2048}, Budget: &agent.BudgetConfig{MaxTokens: 12000}}
	e, base := newHandleTestEngine(t, d)
	p := &preparationProvider{fakeHandleProvider: base, profile: llm.UnknownModelProfile("test", "selected-model")}
	p.profile.Source = "provider_metadata"
	p.profile.NativeTools = llm.SupportYes
	p.profile.Chat = llm.SupportYes
	p.profile.ContextTokens = 16384
	e.llmRouter.Register(p)
	return e, d, p
}

func TestModelPreparationPreservesStoredGoalPermissionsAndBudget(t *testing.T) {
	e, d, p := modelPreparationEngine(t)
	before := d.Clone()
	preview, err := e.PrepareAgentModel(context.Background(), d)
	if err != nil || !preview.GoalPreserved || preview.Profile.Model != "selected-model" || preview.Strategy != "native_tools" || len(preview.Approach) < 3 {
		t.Fatalf("%+v %v", preview, err)
	}
	if !reflect.DeepEqual(d, before) || !reflect.DeepEqual(e.loader.Get(d.ID).Budget, d.Budget) {
		t.Fatal("preview changed definition")
	}
	sink := &reasoningSink{}
	e.sink = sink
	goal := "Release v2 only if all 12 checks pass. Never change production."
	_, err = e.Handle(context.Background(), testUserMessage(d.ID, "prepare-native", goal))
	if err != nil {
		t.Fatal(err)
	}
	requests := p.requestsSnapshot()
	if len(requests) != 1 || requests[0].Model != "selected-model" || !chatMessagesContain(requests[0].Messages, "user", goal) || !chatMessagesContain(requests[0].Messages, "system", "concise milestones") {
		t.Fatalf("bad execution: %+v", requests)
	}
	if !reflect.DeepEqual(d, before) || strings.Contains(e.loader.Get(d.ID).SystemPrompt, "Model-aware") {
		t.Fatal("saved prompt modified")
	}
	events := sink.byType("agent.prepared")
	if len(events) != 1 {
		t.Fatal("missing preflight trace")
	}
	payload, _ := json.Marshal(events[0].Payload)
	if strings.Contains(string(payload), goal) || !strings.Contains(string(payload), `"goal_preserved":true`) {
		t.Fatal("trace leaked goal or lost preservation marker")
	}
	if p.lookups.Load() != 1 {
		t.Fatal("preflight repeated per inference")
	}
}

func TestModelPreparationDefaultsAreAllowlistedBeforeDiscovery(t *testing.T) {
	for _, kind := range []string{"provider", "model", "reasoner"} {
		t.Run(kind, func(t *testing.T) {
			e, d, p := modelPreparationEngine(t)
			d.LLM.Provider, d.LLM.Model = "", ""
			switch kind {
			case "provider":
				d.LLM.AllowedProviders = []string{"local-only"}
			case "model":
				d.LLM.AllowedModels = []string{"permitted-only"}
			case "reasoner":
				d.Reasoning.Strategy = "react"
				d.LLM.AllowedProviders = []string{"test"}
				e.SetReasonerOverride("forbidden", "remote")
			}
			if _, err := e.PrepareAgentModel(context.Background(), d); err == nil {
				t.Fatal("allowlist bypass")
			}
			if p.lookups.Load() != 0 || len(p.requestsSnapshot()) != 0 {
				t.Fatal("discovery or inference preceded allowlist")
			}
		})
	}
}

func TestModelPreparationAutoUsesGuardedFallbackWithoutChangingGoal(t *testing.T) {
	e, d, p := modelPreparationEngine(t)
	p.profile.NativeTools = llm.SupportNo
	p.profile.JSONMode = llm.SupportYes
	p.responses = []llm.CompletionResponse{{Content: `{"is_done":true,"final_answer":"Verified result"}`}, {Content: `{"output":"Verified result"}`}}
	preview, err := e.PrepareAgentModel(context.Background(), d)
	if err != nil || preview.Strategy != "react" {
		t.Fatalf("%+v %v", preview, err)
	}
	_, err = e.Handle(context.Background(), testUserMessage(d.ID, "prompt-protocol", "Keep all requirements and check the result."))
	if err != nil {
		t.Fatal(err)
	}
	requests := p.requestsSnapshot()
	if len(requests) == 0 {
		t.Fatal("fallback did not execute")
	}
	for _, req := range requests {
		if len(req.Tools) > 0 || !chatMessagesContain(req.Messages, "user", "Keep all requirements") || req.Model != "selected-model" {
			t.Fatalf("unsafe fallback: %+v", req)
		}
	}
	if d.Reasoning.Strategy != "" || d.LLM.Model != "" {
		t.Fatal("saved strategy/default changed")
	}
}

func TestModelPreparationExplicitStrategyAndReasonerStayAuthoritative(t *testing.T) {
	for _, strategy := range []string{"auto", "react", "plan_execute"} {
		t.Run(strategy, func(t *testing.T) {
			e, d, p := modelPreparationEngine(t)
			d.Reasoning.Strategy = strategy
			d.LLM.Provider = ""
			p.profile.NativeTools = llm.SupportNo
			e.SetReasonerOverride("test", "reasoner-model")
			preview, err := e.PrepareAgentModel(context.Background(), d)
			if err != nil {
				t.Fatal(err)
			}
			wantModel := "reasoner-model"
			if strategy == "auto" {
				wantModel = "selected-model"
			}
			if preview.Profile.Model != wantModel || (strategy == "plan_execute" && preview.Strategy != strategy) {
				t.Fatalf("explicit choice lost: %+v", preview)
			}
		})
	}
}

func TestModelPreparationRejectsEmbeddingModelBeforeInference(t *testing.T) {
	e, d, p := modelPreparationEngine(t)
	p.profile.Chat = llm.SupportNo
	preview, err := e.PrepareAgentModel(context.Background(), d)
	if err != nil || preview.BlockedReason == "" {
		t.Fatal(preview, err)
	}
	if _, err = e.Handle(context.Background(), testUserMessage(d.ID, "embedding", "Run this")); err == nil || !strings.Contains(err.Error(), "does not support chat") {
		t.Fatal(err)
	}
	if len(p.requestsSnapshot()) != 0 {
		t.Fatal("embedding model invoked")
	}
}

func TestModelPreparationRouterDoesNotProbe(t *testing.T) {
	e, d, p := modelPreparationEngine(t)
	d.Kind = "router"
	preview, err := e.PrepareAgentModel(context.Background(), d)
	if err != nil || preview.Strategy != "router" || p.lookups.Load() != 0 {
		t.Fatal(preview, err)
	}
}

func TestModelPreparationWorkflowDoesNotAssumeEveryStepUsesRootModel(t *testing.T) {
	e, d, p := modelPreparationEngine(t)
	d.Workflow = &agent.WorkflowSpec{}
	d.LLM.Provider = "not-used-by-tool-steps"
	p.profile.Chat = llm.SupportNo
	before := d.Clone()
	preview, err := e.PrepareAgentModel(context.Background(), d)
	if err != nil || preview.Strategy != "workflow" || preview.Profile.Model != "" || preview.BlockedReason != "" || p.lookups.Load() != 0 || !reflect.DeepEqual(before, d) {
		t.Fatal(preview, err)
	}
}

func TestModelPreparationFallbackPreservesCallBudget(t *testing.T) {
	e, d, p := modelPreparationEngine(t)
	d.Budget = &agent.BudgetConfig{MaxLLMCalls: 1}
	e.loader.Register(d)
	p.profile.NativeTools = llm.SupportNo
	p.profile.JSONMode = llm.SupportYes
	p.responses = []llm.CompletionResponse{{Content: `{"is_done":false,"thought":"Need another step"}`}}
	_, _ = e.Handle(context.Background(), testUserMessage(d.ID, "bounded-fallback", "Verify every requirement."))
	if len(p.requestsSnapshot()) != 1 {
		t.Fatal("automatic fallback bypassed call budget", len(p.requestsSnapshot()))
	}
}

func TestModelPreparationFallbackPreservesOutputAndTurnLimits(t *testing.T) {
	e, d, p := modelPreparationEngine(t)
	p.profile.NativeTools = llm.SupportNo
	d.MaxTurns = 2
	d.LLM.MaxTokens = 128
	before := d.Clone()
	cp := d.Clone()
	if err := e.resolveExecutionModel(cp); err != nil {
		t.Fatal(err)
	}
	result, err := e.prepareModelDefinition(context.Background(), cp)
	if err != nil || result.BlockedReason != "" || cp.Reasoning.MaxSteps != 2 || cp.Reasoning.Think.MaxTokens != 128 || cp.Reasoning.Reflect.MaxTokens != 128 || !reflect.DeepEqual(d, before) {
		t.Fatal(result, cp.Reasoning, err)
	}
}

func TestModelPreparationFallbackNeverDropsProtocolContracts(t *testing.T) {
	for _, kind := range []string{"forced-tool", "no-tools", "json", "schema"} {
		t.Run(kind, func(t *testing.T) {
			e, d, p := modelPreparationEngine(t)
			p.profile.NativeTools = llm.SupportNo
			switch kind {
			case "forced-tool":
				d.LLM.ToolChoice = "agent__peer"
			case "no-tools":
				d.LLM.ToolChoice = "none"
			case "json":
				d.LLM.ResponseFormat = "json"
			case "schema":
				d.LLM.OutputSchema = map[string]any{"type": "object"}
			}
			before := d.Clone()
			preview, err := e.PrepareAgentModel(context.Background(), d)
			if err != nil || preview.BlockedReason == "" || !reflect.DeepEqual(d, before) || len(p.requestsSnapshot()) != 0 {
				t.Fatal(preview, err)
			}
		})
	}
}
