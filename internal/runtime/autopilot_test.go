package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/soulacy/soulacy/internal/autopilot"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/taskcontract"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

func managedTestEngine(t *testing.T, def *agent.Definition) (*Engine, *fakeHandleProvider, *autopilot.Store) {
	t.Helper()
	e, p := newHandleTestEngine(t, def)
	store, err := autopilot.NewStore(filepath.Join(t.TempDir(), "autopilot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	e.SetAutopilot(store, nil)
	return e, p, store
}

func TestManagedRunIncludesUniversalTaskContract(t *testing.T) {
	def := checkedDefinition()
	e, p, _ := managedTestEngine(t, def)
	sink := &captureSink6{}
	e.sink = sink
	p.responses = []llm.CompletionResponse{{Content: "VERIFIED"}}

	if _, err := e.Handle(context.Background(), testUserMessage(def.ID, "managed-contract", "Return evidence")); err != nil {
		t.Fatal(err)
	}
	runs := eventsByType(sink.events, "run.completed")
	if len(runs) != 1 {
		t.Fatalf("run.completed events = %d, want 1", len(runs))
	}
	payload, ok := runs[0].Payload.(map[string]any)
	if !ok || payload["task_outcome"] != taskcontract.OutcomeDirectAnswer {
		t.Fatalf("managed run lacks task contract: %#v", runs[0].Payload)
	}
	if _, ok := payload["task_contract"].(taskcontract.Snapshot); !ok {
		t.Fatalf("managed task contract type = %T", payload["task_contract"])
	}
}

func TestManagedExternalActionRequiresVerifiedRuntimeEvidence(t *testing.T) {
	def := checkedDefinition()
	e, p, store := managedTestEngine(t, def)
	p.responses = []llm.CompletionResponse{{Content: "VERIFIED"}}
	msg := testUserMessage(def.ID, "managed-action", "Book the ride")

	if _, err := e.Handle(context.Background(), msg); err == nil || !strings.Contains(err.Error(), "without verified external-action evidence") {
		t.Fatalf("unsupported external action error = %v", err)
	}
	proof, err := store.GetProof(context.Background(), "admin", msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if proof.Outcome != autopilot.ProofFailed {
		t.Fatalf("unsupported action proof outcome = %q", proof.Outcome)
	}
}

func TestExternalActionContractErrorPreservesAttemptBlocker(t *testing.T) {
	snapshot := taskcontract.Snapshot{
		Mode: "external_action", Outcome: taskcontract.OutcomeBlocked, Attempts: 2,
		Blocker: "installer failed: tsc: not found",
	}
	if got := externalActionContractError(snapshot); got == nil || got.Error() != snapshot.Blocker {
		t.Fatalf("external action error = %v", got)
	}
}

func checkedDefinition() *agent.Definition {
	return &agent.Definition{ID: "verified", Name: "Verified", Enabled: true, LLM: agent.LLMConfig{Provider: "test", Model: "fake-model"}, MaxTurns: 3, Builtins: strListPtr(), Mission: &agent.MissionContract{ID: "mission", Goal: "Return evidence", Acceptance: []agent.MissionCheck{{ID: "receipt", Type: agent.MissionCheckOutputContains, Value: "VERIFIED"}}}}
}

func TestManagedRunCreatesFailureProposalAndBlocksReplay(t *testing.T) {
	def := checkedDefinition()
	e, p, store := managedTestEngine(t, def)
	p.responses = []llm.CompletionResponse{{Content: "missing proof"}}
	ctx := WithPrincipal(context.Background(), Principal{Subject: "alice", Role: "operator"})
	msg := testUserMessage(def.ID, "session", "work")
	if _, err := e.Handle(ctx, msg); err == nil {
		t.Fatal("failed acceptance was reported as success")
	}
	proof, err := store.GetProof(ctx, "alice", msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if proof.Verification != autopilot.CheckFail || proof.Outcome != autopilot.ProofFailed {
		t.Fatalf("proof=%+v", proof)
	}
	if err := autopilot.VerifyProof(proof); err != nil {
		t.Fatal(err)
	}
	proposals, err := store.ListProposals(ctx, "alice", autopilot.ProposalFilter{})
	if err != nil || len(proposals) != 1 {
		t.Fatalf("proposals=%v %v", proposals, err)
	}
	if _, err := e.Handle(ctx, msg); !errors.Is(err, autopilot.ErrConflict) {
		t.Fatalf("duplicate=%v", err)
	}
	if len(p.requestsSnapshot()) != 1 {
		t.Fatal("duplicate reached provider")
	}
	if _, err := store.GetProof(ctx, "bob", msg.ID); !errors.Is(err, autopilot.ErrNotFound) {
		t.Fatalf("cross-owner proof=%v", err)
	}
}

func TestManagedSimulationCannotCallUnknownEffectfulTools(t *testing.T) {
	def := checkedDefinition()
	def.Builtins = strListPtr("custom_write")
	e, p, store := managedTestEngine(t, def)
	var effects atomic.Int32
	e.builtins = []BuiltinTool{{Name: "custom_write", Parameters: map[string]any{"type": "object"}, Handler: func(context.Context, map[string]any) (string, error) { effects.Add(1); return "changed", nil }}}
	p.responses = []llm.CompletionResponse{{ToolCalls: []message.ToolCall{{ID: "effect", Name: "custom_write", Arguments: map[string]any{}}}}, {Content: "VERIFIED"}}
	msg := testUserMessage(def.ID, "session", "work")
	_, err := e.Handle(WithDryRun(context.Background(), true), msg)
	if err != nil {
		t.Fatal(err)
	}
	if effects.Load() != 0 {
		t.Fatal("simulation performed an effect")
	}
	proof, err := store.GetProof(context.Background(), "admin", msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !proof.Simulation || len(proof.Tools) != 1 || proof.Tools[0].Status != "simulated" {
		t.Fatalf("simulation proof=%+v", proof)
	}
	r, err := store.Reliability(context.Background(), "admin", def.ID, "")
	if err != nil || r.SampleCount != 0 {
		t.Fatalf("simulation affected reliability=%+v %v", r, err)
	}
}

func TestManagedToolAllowlistCannotBeBypassedByWorkflowAdapter(t *testing.T) {
	def := checkedDefinition()
	allowed := []string{}
	def.Mission.Limits.AllowedTools = &allowed
	e, _, _ := managedTestEngine(t, def)
	var calls atomic.Int32
	e.builtins = []BuiltinTool{{Name: "custom_write", Handler: func(context.Context, map[string]any) (string, error) { calls.Add(1); return "changed", nil }}}
	ctx := context.WithValue(context.Background(), autopilotRunKey{}, &managedRunContext{definition: def})
	if _, err := e.RunTool(ctx, "custom_write", `{}`); err == nil {
		t.Fatal("workflow adapter bypassed mission tools")
	}
	if _, err := e.RunInlinePython(ctx, "print('blocked')", nil); err == nil {
		t.Fatal("inline python bypassed mission tools")
	}
	if calls.Load() != 0 {
		t.Fatal("forbidden effect executed")
	}
}

func TestManagedFreezeAndUnknownCostFailBeforeProvider(t *testing.T) {
	t.Run("freeze", func(t *testing.T) {
		def := checkedDefinition()
		e, p, store := managedTestEngine(t, def)
		_, err := store.SetDeploymentFrozen(context.Background(), "admin", def.ID, true, "operator freeze")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Handle(context.Background(), testUserMessage(def.ID, "s", "work")); !errors.Is(err, autopilot.ErrFrozen) {
			t.Fatalf("freeze=%v", err)
		}
		if len(p.requestsSnapshot()) != 0 {
			t.Fatal("frozen provider call")
		}
	})
	t.Run("cost control unavailable", func(t *testing.T) {
		def := checkedDefinition()
		zero := 0.0
		def.Mission.Limits.MaxCostUSD = &zero
		e, p, _ := managedTestEngine(t, def)
		if _, err := e.Handle(context.Background(), testUserMessage(def.ID, "s", "work")); err == nil {
			t.Fatal("unmetered run succeeded")
		}
		if len(p.requestsSnapshot()) != 0 {
			t.Fatal("unmetered provider call")
		}
	})
}

func TestApprovedRegressionRemainsEnforcedWithoutChangingSnapshot(t *testing.T) {
	def := checkedDefinition()
	e, p, store := managedTestEngine(t, def)
	ctx := context.Background()
	base, err := store.SaveProof(ctx, autopilot.ProofInput{ID: "baseline", Subject: "admin", AgentID: def.ID, RunID: "baseline", Outcome: autopilot.ProofFailed})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := store.CreateProposal(ctx, autopilot.ProposalDraft{Subject: "admin", AgentID: def.ID, SourceProofID: base.ID, FailureSummary: "No sources", CandidateCheck: agent.MissionCheck{Type: agent.MissionCheckOutputContains, Value: "Sources:"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.SetProposalVerification(ctx, "admin", proposal.ID, autopilot.ProposalVerification{Status: autopilot.CheckFail, RunID: "baseline"}, autopilot.ProposalVerification{Status: autopilot.CheckPass, RunID: "fixed"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.DecideProposal(ctx, "admin", proposal.ID, autopilot.ProposalAccepted, "reviewed")
	if err != nil {
		t.Fatal(err)
	}
	p.responses = []llm.CompletionResponse{{Content: "VERIFIED but no citation"}}
	if _, err := e.Handle(ctx, testUserMessage(def.ID, "s", "work")); err == nil || !strings.Contains(err.Error(), "acceptance") {
		t.Fatalf("regression=%v", err)
	}
	if len(e.loader.Get(def.ID).Mission.Acceptance) != 1 {
		t.Fatal("runtime changed the source definition")
	}
}
