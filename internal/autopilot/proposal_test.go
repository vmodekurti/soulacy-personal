package autopilot

import (
	"context"
	"errors"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

func TestLearningProposalRequiresVerifiedRegressionBeforeAcceptance(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	proof, err := store.SaveProof(ctx, ProofInput{
		Subject: "alice", RunID: "failed-run", AgentID: "writer", Outcome: ProofFailed,
		Checks: []CheckResult{{ID: "citation", Type: agent.MissionCheckOutputContains, Status: CheckFail}},
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := store.CreateProposal(ctx, ProposalDraft{
		Subject: "alice", AgentID: "writer", SourceProofID: proof.ID,
		FailureSummary: "the response omitted citations",
		CandidateCheck: agent.MissionCheck{ID: "citation", Type: agent.MissionCheckOutputContains, Value: "Sources:"},
		RulePatch:      "+ Always include a Sources section",
	})
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Status != ProposalPending || proposal.Baseline.Status != CheckUnknown || proposal.Candidate.Status != CheckUnknown {
		t.Fatalf("new proposal = %+v", proposal)
	}
	if _, err := store.DecideProposal(ctx, "alice", proposal.ID, ProposalAccepted, "looks good"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unverified accept = %v, want ErrInvalid", err)
	}
	proposal, err = store.SetProposalVerification(ctx, "alice", proposal.ID,
		ProposalVerification{Status: CheckFail, RunID: "baseline"},
		ProposalVerification{Status: CheckPass, RunID: "candidate"})
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Baseline.VerifiedAt == nil || proposal.Candidate.VerifiedAt == nil {
		t.Fatal("verification timestamps were not recorded")
	}
	proposal, err = store.DecideProposal(ctx, "alice", proposal.ID, ProposalAccepted, "verified")
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Status != ProposalAccepted || proposal.ReviewedAt == nil || proposal.RulePatch == "" {
		t.Fatalf("accepted proposal = %+v", proposal)
	}
	if _, err := store.SetProposalVerification(ctx, "alice", proposal.ID,
		ProposalVerification{Status: CheckFail}, ProposalVerification{Status: CheckPass}); !errors.Is(err, ErrConflict) {
		t.Fatalf("mutate reviewed proposal = %v, want ErrConflict", err)
	}
	if _, err := store.GetProposal(ctx, "bob", proposal.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-subject proposal get = %v", err)
	}
}

func TestLearningProposalRejectDoesNotNeedCandidatePass(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	proof, err := store.SaveProof(ctx, ProofInput{
		Subject: "alice", RunID: "r", AgentID: "a", Outcome: ProofFailed,
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := store.CreateProposal(ctx, ProposalDraft{
		Subject: "alice", AgentID: "a", SourceProofID: proof.ID, FailureSummary: "bad output",
		CandidateCheck: agent.MissionCheck{Type: agent.MissionCheckOutputRegex, Value: `ok`},
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err = store.DecideProposal(ctx, "alice", proposal.ID, ProposalRejected, "too broad")
	if err != nil || proposal.Status != ProposalRejected || proposal.DecisionReason != "too broad" {
		t.Fatalf("reject = %+v, err=%v", proposal, err)
	}
	listed, err := store.ListProposals(ctx, "alice", ProposalFilter{Status: ProposalRejected})
	if err != nil || len(listed) != 1 || listed[0].ID != proposal.ID {
		t.Fatalf("rejected list = %+v, err=%v", listed, err)
	}
}

func TestLearningProposalCannotCrossAgentProofBoundary(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	proof, err := store.SaveProof(ctx, ProofInput{Subject: "alice", RunID: "r", AgentID: "a", Outcome: ProofFailed})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateProposal(ctx, ProposalDraft{
		Subject: "alice", AgentID: "b", SourceProofID: proof.ID, FailureSummary: "bad",
		CandidateCheck: agent.MissionCheck{Type: agent.MissionCheckOutputContains, Value: "x"},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-agent proposal = %v, want ErrInvalid", err)
	}
}
