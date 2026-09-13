package autopilot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

func definitionJSON(t *testing.T, id, version, description string) []byte {
	t.Helper()
	raw, err := json.Marshal(&agent.Definition{ID: id, Version: version, Description: description})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func promoteToStable(t *testing.T, store *Store, subject string, version DeploymentVersion) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := store.PromoteDeployment(ctx, PromotionRequest{
		Subject: subject, AgentID: version.AgentID, VersionID: version.ID, ToChannel: ChannelSimulation,
	}); err != nil {
		t.Fatalf("promote simulation: %v", err)
	}
	if _, _, err := store.PromoteDeployment(ctx, PromotionRequest{
		Subject: subject, AgentID: version.AgentID, VersionID: version.ID, ToChannel: ChannelCanary, TrafficPercent: 10,
	}); err != nil {
		t.Fatalf("promote canary: %v", err)
	}
	if _, _, err := store.PromoteDeployment(ctx, PromotionRequest{
		Subject: subject, AgentID: version.AgentID, VersionID: version.ID, ToChannel: ChannelStable,
	}); err != nil {
		t.Fatalf("promote stable: %v", err)
	}
}

func TestDeploymentImmutableVersionsCanaryGatesFreezeAndRollback(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	rawV1 := definitionJSON(t, "writer", "1", "first")
	v1, err := store.CreateDeploymentVersion(ctx, DeploymentVersionInput{
		Subject: "alice", AgentID: "writer", Version: "1", DefinitionJSON: rawV1,
	})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(rawV1)
	if v1.RevisionHash != "sha256:"+hex.EncodeToString(hash[:]) {
		t.Fatalf("revision = %s, does not match runtime json.Marshal hash", v1.RevisionHash)
	}
	promoteToStable(t, store, "alice", v1)

	rawV2 := definitionJSON(t, "writer", "2", "second")
	v2, err := store.CreateDeploymentVersion(ctx, DeploymentVersionInput{
		Subject: "alice", AgentID: "writer", Version: "2", DefinitionJSON: rawV2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PromoteDeployment(ctx, PromotionRequest{
		Subject: "alice", AgentID: "writer", VersionID: v2.ID, ToChannel: ChannelSimulation,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PromoteDeployment(ctx, PromotionRequest{
		Subject: "alice", AgentID: "writer", VersionID: v2.ID, ToChannel: ChannelCanary, TrafficPercent: 10,
	}); err != nil {
		t.Fatal(err)
	}
	below, above := runForBucket(true), runForBucket(false)
	canary, err := store.ResolveDefinition(ctx, "alice", "writer", below)
	if err != nil || canary.VersionID != v2.ID || canary.Channel != ChannelCanary {
		t.Fatalf("canary resolution = %+v, err=%v", canary, err)
	}
	stable, err := store.ResolveDefinition(ctx, "alice", "writer", above)
	if err != nil || stable.VersionID != v1.ID || stable.Channel != ChannelStable {
		t.Fatalf("stable resolution = %+v, err=%v", stable, err)
	}
	again, err := store.ResolveDefinition(ctx, "alice", "writer", below)
	if err != nil || again.VersionID != canary.VersionID {
		t.Fatalf("routing was not deterministic: first=%+v again=%+v err=%v", canary, again, err)
	}

	// A successful simulation is visible but cannot satisfy a production gate.
	if _, err := store.SaveProof(ctx, ProofInput{
		Subject: "alice", RunID: "v2-sim", AgentID: "writer", Outcome: ProofSucceeded,
		RevisionHash: v2.RevisionHash, Simulation: true, Checks: []CheckResult{passedCheck("c")},
	}); err != nil {
		t.Fatal(err)
	}
	one := 1.0
	_, gate, err := store.PromoteDeployment(ctx, PromotionRequest{
		Subject: "alice", AgentID: "writer", VersionID: v2.ID, ToChannel: ChannelStable,
		Gates: PromotionGates{MinSamples: 1, MinSuccessRate: &one, MinVerificationRate: &one},
	})
	if !errors.Is(err, ErrConflict) || gate.Passed {
		t.Fatalf("simulation passed production gate: gate=%+v err=%v", gate, err)
	}
	if _, err := store.SaveProof(ctx, ProofInput{
		Subject: "alice", RunID: "v2-real", AgentID: "writer", Outcome: ProofSucceeded,
		RevisionHash: v2.RevisionHash, Checks: []CheckResult{passedCheck("c")},
	}); err != nil {
		t.Fatal(err)
	}
	state, gate, err := store.PromoteDeployment(ctx, PromotionRequest{
		Subject: "alice", AgentID: "writer", VersionID: v2.ID, ToChannel: ChannelStable,
		Gates: PromotionGates{MinSamples: 1, MinSuccessRate: &one, MinVerificationRate: &one},
	})
	if err != nil || !gate.Passed || state.CurrentVersionID != v2.ID || state.PreviousVersionID != v1.ID {
		t.Fatalf("real promotion state=%+v gate=%+v err=%v", state, gate, err)
	}
	rolled, err := store.RollbackDeployment(ctx, RollbackRequest{
		Subject: "alice", AgentID: "writer", Channel: ChannelStable, Reason: "regression", ExpectedVersionID: v2.ID,
	})
	if err != nil || rolled.CurrentVersionID != v1.ID || rolled.PreviousVersionID != v2.ID {
		t.Fatalf("rollback = %+v, err=%v", rolled, err)
	}
	if _, err := store.RollbackDeployment(ctx, RollbackRequest{Subject: "alice", AgentID: "writer", Channel: ChannelStable, ExpectedVersionID: v2.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale rollback undid a newer decision: %v", err)
	}
	status, err := store.SetDeploymentFrozen(ctx, "alice", "writer", true, "operator stop")
	if err != nil || !status.Frozen {
		t.Fatalf("freeze status=%+v err=%v", status, err)
	}
	if _, err := store.ResolveDefinition(ctx, "alice", "writer", "any"); !errors.Is(err, ErrFrozen) {
		t.Fatalf("frozen resolution = %v", err)
	}
	// Emergency rollback remains possible while execution is frozen.
	if _, err := store.RollbackDeployment(ctx, RollbackRequest{
		Subject: "alice", AgentID: "writer", Channel: ChannelStable,
	}); err != nil {
		t.Fatalf("rollback while frozen: %v", err)
	}
}

func TestDeploymentSubjectScopingAndImmutableRevision(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	raw := definitionJSON(t, "a", "1", "owned")
	version, err := store.CreateDeploymentVersion(ctx, DeploymentVersionInput{
		Subject: "alice", AgentID: "a", Version: "1", DefinitionJSON: raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetDeploymentVersion(ctx, "bob", version.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-subject version = %v", err)
	}
	if _, err := store.CreateDeploymentVersion(ctx, DeploymentVersionInput{
		Subject: "alice", AgentID: "a", Version: "duplicate", DefinitionJSON: append([]byte(" \n"), raw...),
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate semantic revision = %v, want conflict", err)
	}
	copy(version.DefinitionJSON[:1], []byte("x"))
	stored, err := store.GetDeploymentVersion(ctx, "alice", version.ID)
	if err != nil || !json.Valid(stored.DefinitionJSON) {
		t.Fatalf("caller mutated stored definition: %s, err=%v", stored.DefinitionJSON, err)
	}
	statuses, err := store.ListDeploymentStatuses(ctx, "alice")
	if err != nil || len(statuses) != 1 || statuses[0].AgentID != "a" {
		t.Fatalf("statuses=%+v err=%v", statuses, err)
	}
}

func runForBucket(canary bool) string {
	for i := 0; i < 10000; i++ {
		runID := fmt.Sprintf("run-%d", i)
		selected := deterministicBucket("alice", "writer", runID) < 10
		if selected == canary {
			return runID
		}
	}
	panic("unable to find deterministic bucket")
}
