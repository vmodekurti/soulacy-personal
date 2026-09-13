package llm

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestRunCostBudgetParallelDescendantsAndUnknownUsage(t *testing.T) {
	ctx := WithRunCostBudget(context.Background(), 100)
	var wg sync.WaitGroup
	var count atomic.Int32
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			child := WithRunCostBudget(ctx, 50)
			id := fmt.Sprint(i)
			if ReserveRunCost(child, id, 25) == nil {
				count.Add(1)
				SettleRunCost(child, id, 0, false)
			}
		}()
	}
	wg.Wait()
	if count.Load() != 4 {
		t.Fatalf("admitted %d requests, want 4", count.Load())
	}
	if ReserveRunCost(ctx, "extra", 1) == nil {
		t.Fatal("unknown usage refunded")
	}
}

func TestRunCostBudgetSettlementAndChildLimit(t *testing.T) {
	parent := WithRunCostBudget(context.Background(), 100)
	child := WithRunCostBudget(parent, 20)
	if ReserveRunCost(child, "too-big", 21) == nil {
		t.Fatal("child limit bypassed")
	}
	if err := ReserveRunCost(child, "first", 20); err != nil {
		t.Fatal(err)
	}
	SettleRunCost(child, "first", 5, true)
	SettleRunCost(child, "first", 5, true)
	if err := ReserveRunCost(child, "second", 15); err != nil {
		t.Fatal(err)
	}
	if err := ReserveRunCost(parent, "remaining", 80); err != nil {
		t.Fatal(err)
	}
	if ReserveRunCost(parent, "extra", 1) == nil {
		t.Fatal("parent limit bypassed")
	}
}
