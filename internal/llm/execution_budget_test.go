package llm

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestExecutionBudgetParallelCallsShareParentCeiling(t *testing.T) {
	root := WithExecutionBudget(context.Background(), 0, 4)
	r := NewRouter("test")
	r.Register(&fakeProvider{id: "test"})
	var calls atomic.Int32
	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() {
			child := WithExecutionBudget(root, 0, 2)
			if _, err := r.Complete(child, "", CompletionRequest{}); err == nil {
				calls.Add(1)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 4 {
		t.Fatal("parallel budget exceeded", calls.Load())
	}
}

func TestExecutionBudgetReservesAndClampsBeforeInference(t *testing.T) {
	ctx := WithExecutionBudget(context.Background(), 100, 0)
	req := CompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "Keep the exact request"}}, MaxTokens: 200}
	input := EstimateRequestTokens(req)
	r, err := reserveExecution(ctx, &req)
	if err != nil || req.MaxTokens != 100-input {
		t.Fatal(req, err)
	}
	if _, err := reserveExecution(ctx, &CompletionRequest{}); err == nil {
		t.Fatal("in-flight reservation oversubscribed")
	}
	r.settle(&CompletionResponse{InputTokens: input, OutputTokens: 10}, nil)
	next := CompletionRequest{MaxTokens: 200}
	second, err := reserveExecution(ctx, &next)
	if err != nil || next.MaxTokens != 90-input {
		t.Fatal(next, err)
	}
	second.cancel()
	r.settle(nil, errors.New("duplicate")) // must not double-charge
}

func TestExecutionBudgetUnknownProviderFailureRemainsCharged(t *testing.T) {
	ctx := WithExecutionBudget(context.Background(), 100, 0)
	req := CompletionRequest{MaxTokens: 100}
	r, err := reserveExecution(ctx, &req)
	if err != nil {
		t.Fatal(err)
	}
	r.settle(nil, errors.New("lost acknowledgement"))
	if _, err := reserveExecution(ctx, &CompletionRequest{}); err == nil {
		t.Fatal("uncertain call was free")
	}
}

func TestExecutionBudgetStreamsStayReservedWithoutCostController(t *testing.T) {
	ctx := WithExecutionBudget(context.Background(), 100, 0)
	stream := make(chan string)
	r := NewRouter("test")
	r.Register(&fakeProvider{id: "test", response: &CompletionResponse{Stream: stream}})
	resp, err := r.Complete(ctx, "", CompletionRequest{MaxTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Complete(ctx, "", CompletionRequest{}); err == nil {
		t.Fatal("stream reservation released early")
	}
	go func() { stream <- "hello"; close(stream) }()
	for range resp.Stream {
	}
	// Draining the proxy synchronizes accounting even without a cost governor.
	req := CompletionRequest{MaxTokens: 10}
	reservation, err := reserveExecution(ctx, &req)
	if err != nil {
		t.Fatal(err)
	}
	reservation.cancel()
}

type deniedController struct{}

func (deniedController) Before(ctx context.Context, _ string, _ *CompletionRequest) (context.Context, Reservation, error) {
	return ctx, Reservation{}, errors.New("policy denied")
}
func (deniedController) After(context.Context, Reservation, string, CompletionRequest, *CompletionResponse, error) {
}

func TestExecutionBudgetAdmissionRejectionReleasesUnusedCapacity(t *testing.T) {
	ctx := WithExecutionBudget(context.Background(), 100, 1)
	r := NewRouter("test")
	r.Register(&fakeProvider{id: "test"})
	r.SetController(deniedController{})
	if _, err := r.Complete(ctx, "", CompletionRequest{}); err == nil {
		t.Fatal("policy bypassed")
	}
	r.SetController(nil)
	if _, err := r.Complete(ctx, "", CompletionRequest{}); err != nil {
		t.Fatal("unused reservation leaked", err)
	}
}
