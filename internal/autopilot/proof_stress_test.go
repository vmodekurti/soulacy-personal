package autopilot

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestProofRetainsLongRunReceipts(t *testing.T) {
	store, _ := openTestStore(t)
	input := ProofInput{Subject: "operator", AgentID: "long", RunID: "long", Outcome: ProofSucceeded}
	for i := 0; i < 2048; i++ {
		id := fmt.Sprintf("step-%d", i)
		input.Tools = append(input.Tools, ToolUse{Name: "step", CallID: id, Status: "succeeded"})
		input.Evidence = append(input.Evidence, Evidence{ID: id, Kind: "tool_receipt", ContentHash: id})
	}
	proof, err := store.SaveProof(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.GetProof(context.Background(), "operator", proof.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Tools) != 2048 || len(saved.Evidence) != 2048 || saved.Tools[2047].CallID != "step-2047" {
		t.Fatal("long-run receipts were lost")
	}
	if err := VerifyProof(saved); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentProofFinalizationPreservesEveryRun(t *testing.T) {
	store, _ := openTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const count = 128
	errs := make(chan error, count)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			run := fmt.Sprintf("concurrent-%d", i)
			if err := store.ClaimRun(ctx, "operator", run, "worker"); err != nil {
				errs <- err
				return
			}
			_, err := store.SaveProof(ctx, ProofInput{Subject: "operator", AgentID: "worker", RunID: run, Outcome: ProofSucceeded, RevisionHash: "revision"})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	for _, revision := range []string{"", "revision"} {
		reliability, err := store.Reliability(ctx, "operator", "worker", revision)
		if err != nil || reliability.SampleCount != count || reliability.SuccessCount != count {
			t.Fatalf("reliability = %+v, err = %v", reliability, err)
		}
	}
	claims, err := store.ListUnfinishedRunClaims(ctx, "operator", count)
	if err != nil || len(claims) != 0 {
		t.Fatalf("unfinished claims = %d, err = %v", len(claims), err)
	}
}
