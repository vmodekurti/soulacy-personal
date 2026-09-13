package llm

import (
	"context"
	"fmt"
	"math"
	"sync"
)

type runCostKey struct{}
type costBucket struct {
	limit, spent, held  int64
	enforced, uncertain bool
}
type runCostState struct {
	mu           sync.Mutex
	reservations map[string]runCostReservation
}
type runCostReservation struct {
	estimate int64
	buckets  []*costBucket
}
type runCostScope struct {
	state   *runCostState
	buckets []*costBucket
}

// WithRunCostBudget adds a dollar-denominated inference budget to this run.
// Descendants share ancestor reservations, so parallel peers cannot each spend
// the parent's full budget. This bounds estimated inference charges, not third
// party purchases or a provider's final invoice.
func WithRunCostBudget(ctx context.Context, micros int64) context.Context {
	if micros < 0 {
		return ctx
	}
	return withRunCostScope(ctx, micros, true)
}

// WithRunCostTracking meters a run (including descendants) without imposing
// pricing policy on ordinary calls. A budget adds an enforced nested scope.
func WithRunCostTracking(ctx context.Context) context.Context {
	return withRunCostScope(ctx, math.MaxInt64, false)
}

func withRunCostScope(ctx context.Context, micros int64, enforced bool) context.Context {
	previous, _ := ctx.Value(runCostKey{}).(*runCostScope)
	next := &runCostScope{}
	if previous != nil {
		next.state = previous.state
		next.buckets = append([]*costBucket(nil), previous.buckets...)
	} else {
		next.state = &runCostState{reservations: map[string]runCostReservation{}}
	}
	next.buckets = append(next.buckets, &costBucket{limit: micros, enforced: enforced})
	return context.WithValue(ctx, runCostKey{}, next)
}

func HasRunCostBudget(ctx context.Context) bool {
	s, _ := ctx.Value(runCostKey{}).(*runCostScope)
	if s != nil {
		for _, b := range s.buckets {
			if b.enforced {
				return true
			}
		}
	}
	return false
}

// RunCostObservation returns estimated inference charges, never an invoice.
// Missing/uncertain usage or an outstanding provider call remains unknown.
func RunCostObservation(ctx context.Context) *float64 {
	s, _ := ctx.Value(runCostKey{}).(*runCostScope)
	if s == nil || len(s.buckets) == 0 {
		return nil
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	b := s.buckets[len(s.buckets)-1]
	if b.uncertain || b.held != 0 {
		return nil
	}
	v := float64(b.spent) / 1e6
	return &v
}

func ReserveRunCost(ctx context.Context, id string, estimate int64) error {
	s, _ := ctx.Value(runCostKey{}).(*runCostScope)
	if s == nil {
		return nil
	}
	if estimate < 0 || id == "" {
		return fmt.Errorf("invalid run cost reservation")
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if _, exists := s.state.reservations[id]; exists {
		return fmt.Errorf("duplicate run cost reservation")
	}
	for _, b := range s.buckets {
		if estimate > b.limit-b.spent-b.held {
			return fmt.Errorf("mission inference budget exhausted (limit $%.6f)", float64(b.limit)/1e6)
		}
	}
	for _, b := range s.buckets {
		b.held += estimate
	}
	s.state.reservations[id] = runCostReservation{estimate: estimate, buckets: s.buckets}
	return nil
}

// SettleRunCost keeps the reservation charged when the provider's actual usage
// is unknown. A failed/uncertain request is not treated as free capacity.
func SettleRunCost(ctx context.Context, id string, actual int64, known bool) {
	s, _ := ctx.Value(runCostKey{}).(*runCostScope)
	if s == nil {
		return
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	r, exists := s.state.reservations[id]
	if !exists {
		return
	}
	delete(s.state.reservations, id)
	if !known || actual < 0 {
		actual = r.estimate
	}
	for _, b := range r.buckets {
		b.held -= r.estimate
		b.spent += actual
		b.uncertain = b.uncertain || !known
	}
}

// RunCostController marks controllers that enforce context-scoped budgets.
type RunCostController interface{ SupportsRunCostBudgets() bool }
