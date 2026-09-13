package llm

import (
	"context"
	"fmt"
	"math"
	"sync"
)

type executionBudgetKey struct{}
type executionBucket struct{ tokenLimit, callLimit, spent, held, calls int64 }
type executionScope struct {
	mu      *sync.Mutex
	buckets []*executionBucket
}
type executionReservation struct {
	scope   *executionScope
	tokens  int64
	settled bool
}

// WithExecutionBudget keeps configured token/call limits independent of the
// selected strategy. Parallel and nested agents share ancestor reservations;
// a child may impose a tighter budget but cannot reset its parent's budget.
// Token admission uses conservative estimates, not a provider billing promise.
func WithExecutionBudget(ctx context.Context, tokens, calls int) context.Context {
	if tokens <= 0 && calls <= 0 {
		return ctx
	}
	s := &executionScope{mu: &sync.Mutex{}}
	if parent, ok := ctx.Value(executionBudgetKey{}).(*executionScope); ok {
		s.mu = parent.mu
		s.buckets = append([]*executionBucket(nil), parent.buckets...)
	}
	s.buckets = append(s.buckets, &executionBucket{tokenLimit: int64(max(0, tokens)), callLimit: int64(max(0, calls))})
	return context.WithValue(ctx, executionBudgetKey{}, s)
}

func reserveExecution(ctx context.Context, req *CompletionRequest) (*executionReservation, error) {
	s, _ := ctx.Value(executionBudgetKey{}).(*executionScope)
	if s == nil {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	input, output := int64(EstimateRequestTokens(*req)), int64(req.MaxTokens)
	if output <= 0 {
		output = 1024
	}
	for _, b := range s.buckets {
		if b.callLimit > 0 && b.calls >= b.callLimit {
			return nil, fmt.Errorf("agent model-call budget exhausted")
		}
		if b.tokenLimit > 0 {
			if b.spent >= b.tokenLimit || b.held >= b.tokenLimit-b.spent || input >= b.tokenLimit-b.spent-b.held {
				return nil, fmt.Errorf("agent token budget cannot fit the next prompt")
			}
			remaining := b.tokenLimit - b.spent - b.held - input
			if remaining <= 0 {
				return nil, fmt.Errorf("agent token budget cannot fit the next prompt")
			}
			output = min(output, remaining)
		}
	}
	if input < 0 || output > math.MaxInt64-input {
		return nil, fmt.Errorf("invalid token reservation")
	}
	req.MaxTokens = int(output)
	reserved := input + output
	for _, b := range s.buckets {
		if b.held > math.MaxInt64-reserved || b.calls == math.MaxInt64 {
			return nil, fmt.Errorf("token reservation overflow")
		}
	}
	for _, b := range s.buckets {
		b.calls++
		b.held += reserved
	}
	return &executionReservation{scope: s, tokens: reserved}, nil
}

// cancel releases admission which never reached a provider.
func (r *executionReservation) cancel() {
	if r == nil {
		return
	}
	r.scope.mu.Lock()
	defer r.scope.mu.Unlock()
	if r.settled {
		return
	}
	r.settled = true
	for _, b := range r.scope.buckets {
		b.held -= r.tokens
		b.calls--
	}
}

// settle is safe to call twice (including a panic guard). Uncertain calls keep
// their reservation charged. Streaming reservations remain held until drained.
func (r *executionReservation) settle(resp *CompletionResponse, err error) {
	if r == nil {
		return
	}
	r.scope.mu.Lock()
	defer r.scope.mu.Unlock()
	if r.settled {
		return
	}
	r.settled = true
	actual := r.tokens
	if resp != nil && err == nil {
		actual = 0
		for _, v := range []int{resp.InputTokens, resp.OutputTokens, resp.ReasoningTokens, resp.ToolUsePromptTokens} {
			n := int64(max(0, v))
			if n > math.MaxInt64-actual {
				actual = math.MaxInt64
				break
			}
			actual += n
		}
		actual = max(actual, int64(max(0, resp.TotalTokens)))
		if actual <= 0 {
			actual = r.tokens
		}
	}
	for _, b := range r.scope.buckets {
		b.held -= r.tokens
		if actual > math.MaxInt64-b.spent {
			b.spent = math.MaxInt64
		} else {
			b.spent += actual
		}
	}
}
