package autopilot

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/sqlitex"
	"github.com/soulacy/soulacy/pkg/agent"
)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "autopilot.db")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func passedCheck(id string) CheckResult {
	return CheckResult{ID: id, Type: agent.MissionCheckOutputContains, Status: CheckPass}
}

func TestStoreMigrationsAreVersionedAndIdempotent(t *testing.T) {
	store, path := openTestStore(t)
	version, err := sqlitex.SchemaVersion(store.db, schemaComponent)
	if err != nil {
		t.Fatal(err)
	}
	if version != 5 {
		t.Fatalf("schema version = %d, want 5", version)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(path)
	if err != nil {
		t.Fatalf("reopen/migrate: %v", err)
	}
	defer reopened.Close()
	version, err = sqlitex.SchemaVersion(reopened.db, schemaComponent)
	if err != nil || version != 5 {
		t.Fatalf("reopened schema version = %d, err = %v", version, err)
	}
}

func TestStoreMigratesVersionFourGoalsWithEmptyFailureReason(t *testing.T) {
	path := filepath.Join(t.TempDir(), "autopilot-v4.db")
	db, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqlitex.MigrateSchema(db, schemaComponent, schemaMigrations[:4]); err != nil {
		t.Fatal(err)
	}
	now := timeString(time.Now().UTC())
	if _, err := db.Exec(`
		INSERT INTO autopilot_goals
		(id, subject, title, objective, status, max_cost_usd, max_duration_ms, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, "legacy-goal", "alice", "Legacy", "Migrate safely",
		GoalStatusDraft, 1, 1000, now, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	goal, err := store.GetGoal(context.Background(), "alice", "legacy-goal")
	if err != nil || goal.Error != "" {
		t.Fatalf("migrated goal = %+v, err = %v", goal, err)
	}
}

func TestClaimRunPreventsReplayAndFinalizesWithProof(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.ClaimRun(ctx, "alice", "run-1", "brief"); err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimRun(ctx, "alice", "run-1", "brief"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate claim error = %v, want ErrConflict", err)
	}
	proof, err := store.SaveProof(ctx, ProofInput{
		Subject: "alice", RunID: "run-1", AgentID: "brief", Outcome: ProofSucceeded,
		Checks: []CheckResult{passedCheck("output")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyProof(proof); err != nil {
		t.Fatalf("proof verification: %v", err)
	}
	var status RunClaimStatus
	var proofID string
	if err := store.db.QueryRow(`SELECT status, proof_id FROM autopilot_run_claims WHERE subject = ? AND run_id = ?`,
		"alice", "run-1").Scan(&status, &proofID); err != nil {
		t.Fatal(err)
	}
	if status != RunClaimFinalized || proofID != proof.ID {
		t.Fatalf("claim = %s/%s, want finalized/%s", status, proofID, proof.ID)
	}
	if _, err := store.SaveProof(ctx, ProofInput{
		Subject: "alice", RunID: "run-1", AgentID: "brief", Outcome: ProofSucceeded,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate proof error = %v, want ErrConflict", err)
	}
}

func TestRunAndProofIDsAreScopedPerSubject(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	for _, subject := range []string{"alice", "bob"} {
		if err := store.ClaimRun(ctx, subject, "same-run", "brief"); err != nil {
			t.Fatalf("claim for %s: %v", subject, err)
		}
		if _, err := store.SaveProof(ctx, ProofInput{
			ID: "same-proof", Subject: subject, RunID: "same-run", AgentID: "brief",
			Outcome: ProofSucceeded,
		}); err != nil {
			t.Fatalf("proof for %s: %v", subject, err)
		}
	}
}

func TestInterruptedClaimBecomesUncertainAndNeverReplays(t *testing.T) {
	store, path := openTestStore(t)
	ctx := context.Background()
	if err := store.ClaimRun(ctx, "alice", "run-crash", "writer"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	claims, err := reopened.ListUnfinishedRunClaims(ctx, "alice", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || claims[0].Status != RunClaimUncertain {
		t.Fatalf("claims = %+v, want one uncertain", claims)
	}
	if err := reopened.ClaimRun(ctx, "alice", "run-crash", "writer"); !errors.Is(err, ErrConflict) {
		t.Fatalf("reclaim uncertain run = %v, want ErrConflict", err)
	}
}

func TestProofIntegrityDetectsStoredPayloadTampering(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	proof, err := store.SaveProof(ctx, ProofInput{
		ID: "proof-1", Subject: "alice", RunID: "run-1", AgentID: "agent",
		Outcome: ProofSucceeded, Checks: []CheckResult{passedCheck("check")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetProof(ctx, "bob", proof.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-subject get = %v, want not found", err)
	}
	if _, err := store.db.Exec(`UPDATE autopilot_proofs SET payload_json = replace(payload_json, '"succeeded"', '"failed"') WHERE id = ?`, proof.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetProof(ctx, "alice", proof.ID); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("tampered proof error = %v, want ErrIntegrity", err)
	}
}

func TestReliabilityPreservesUnknownAndObservedZero(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	failed := CheckResult{ID: "c", Type: agent.MissionCheckRequiredTool, Status: CheckFail}
	if _, err := store.SaveProof(ctx, ProofInput{
		Subject: "alice", RunID: "r1", AgentID: "zero", Outcome: ProofFailed,
		RevisionHash: "rev", Checks: []CheckResult{failed},
	}); err != nil {
		t.Fatal(err)
	}
	zero, err := store.Reliability(ctx, "alice", "zero", "rev")
	if err != nil {
		t.Fatal(err)
	}
	if zero.SampleCount != 1 || zero.SuccessRate == nil || *zero.SuccessRate != 0 ||
		zero.VerificationRate == nil || *zero.VerificationRate != 0 || zero.Score == nil || *zero.Score != 0 {
		t.Fatalf("observed zero metrics = %+v", zero)
	}
	unknownCheck := CheckResult{ID: "c", Type: agent.MissionCheckMaxCost, Status: CheckUnknown}
	if _, err := store.SaveProof(ctx, ProofInput{
		Subject: "alice", RunID: "r2", AgentID: "unknown", Outcome: ProofSucceeded,
		Checks: []CheckResult{unknownCheck},
	}); err != nil {
		t.Fatal(err)
	}
	unknown, err := store.Reliability(ctx, "alice", "unknown", "")
	if err != nil {
		t.Fatal(err)
	}
	if unknown.SuccessRate == nil || *unknown.SuccessRate != 1 || unknown.VerificationRate != nil || unknown.Score != nil {
		t.Fatalf("unknown metrics collapsed to zero: %+v", unknown)
	}
	empty, err := store.Reliability(ctx, "alice", "never-ran", "")
	if err != nil || empty.SampleCount != 0 || empty.SuccessRate != nil || empty.VerificationRate != nil {
		t.Fatalf("empty reliability = %+v, err = %v", empty, err)
	}
}

func TestSimulationProofIsStoredButExcludedFromReliability(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	proof, err := store.SaveProof(ctx, ProofInput{
		Subject: "alice", RunID: "sim-1", AgentID: "agent", Outcome: ProofSucceeded,
		RevisionHash: "rev", Simulation: true, Checks: []CheckResult{passedCheck("c")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !proof.Simulation {
		t.Fatal("simulation flag was lost")
	}
	reliability, err := store.Reliability(ctx, "alice", "agent", "rev")
	if err != nil || reliability.SampleCount != 0 {
		t.Fatalf("simulation entered production reliability: %+v, err=%v", reliability, err)
	}
	sim := true
	proofs, err := store.ListProofs(ctx, "alice", ProofFilter{Simulation: &sim})
	if err != nil || len(proofs) != 1 || proofs[0].ID != proof.ID {
		t.Fatalf("simulation list = %+v, err=%v", proofs, err)
	}
}

func TestProofDoesNotDuplicateOutputAndUsesFixedTimestampOrdering(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 12, 1, 2, 3, 0, time.UTC)
	for i, ns := range []int{900_000_000, 10_000_000} {
		output := "credential-bearing-output-" + strings.Repeat("x", i+1)
		result := EvaluateMission(&agent.MissionContract{Acceptance: []agent.MissionCheck{{
			ID: "c", Type: agent.MissionCheckOutputContains, Value: "credential-bearing",
		}}}, Observation{Output: output})
		if _, err := store.SaveProof(ctx, ProofInput{
			ID: fmtID(i), Subject: "alice", RunID: fmtID(i), AgentID: "agent",
			Outcome: ProofSucceeded, Checks: result.Checks, CompletedAt: base.Add(time.Duration(ns)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	proofs, err := store.ListProofs(ctx, "alice", ProofFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(proofs) != 2 || proofs[0].CompletedAt.Before(proofs[1].CompletedAt) {
		t.Fatalf("proof ordering = %+v", proofs)
	}
	if strings.Contains(proofs[0].Checks[0].Actual, "credential-bearing-output") {
		t.Fatal("receipt duplicated full output")
	}
}

func fmtID(i int) string {
	if i == 0 {
		return "later"
	}
	return "earlier"
}
