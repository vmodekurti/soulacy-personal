package quota

import (
	"container/list"
	"context"
	"sync"
)

// fairshare.go — MU-024 criterion 4: "concurrency uses fair scheduling rather
// than global first-come starvation."
//
// A plain semaphore is first-come, and first-come is not a scheduling policy
// so much as the absence of one. A tenant that submits a hundred runs takes
// every slot and holds them, and everyone behind them waits — without
// exceeding a single budget, because they are not spending faster than
// allowed, they are simply spending FIRST. Budgets bound how much; they say
// nothing about who goes next.
//
// THE POLICY. Max-min fairness by workspace: no workspace may hold more than
// its equal share of the capacity while another workspace is waiting, and any
// capacity nobody wants is freely available to whoever does. Concretely, with
// N workspaces actively competing for C slots, each may hold up to
// ceil(C / N) — so one tenant alone gets everything, two get half each, and a
// tenant that arrives late claws back to its share as slots are released
// rather than waiting for the incumbent to finish everything.
//
// WHY ceil AND NOT floor. With C=4 and N=3, floor gives each 1 and leaves a
// slot permanently idle while three tenants queue for it. Fairness that
// wastes capacity is not the goal; the goal is that nobody is starved.
//
// WHY WORKSPACE AND NOT PRINCIPAL. The unit of fairness is the tenant, because
// the tenant is what a deployment sells and what it isolates. Making it
// per-principal would let a workspace with fifty members take fifty shares
// from a workspace with two — the same starvation, one level down.

// FairShare admits work so that no workspace starves another.
type FairShare struct {
	mu       sync.Mutex
	capacity int
	held     map[string]int
	waiting  map[string]*list.List // workspace → queue of waiters
	order    *list.List            // round-robin order of workspaces with waiters
	inOrder  map[string]*list.Element
}

// NewFairShare builds a scheduler admitting at most `capacity` concurrent
// holders. A capacity of zero or less means unlimited, which is the
// single-tenant answer and keeps a personal deployment free of queueing it
// does not need.
func NewFairShare(capacity int) *FairShare {
	return &FairShare{
		capacity: capacity,
		held:     make(map[string]int),
		waiting:  make(map[string]*list.List),
		order:    list.New(),
		inOrder:  make(map[string]*list.Element),
	}
}

// Acquire blocks until this workspace may proceed, or ctx is done.
//
// Returns a release function. It is safe to call more than once; the second
// call is a no-op, because a caller that defers release AND releases on an
// error path should not corrupt the accounting.
func (f *FairShare) Acquire(ctx context.Context, workspaceID string) (func(), error) {
	if f == nil || f.capacity <= 0 {
		return func() {}, nil
	}
	f.mu.Lock()
	if f.admissible(workspaceID) {
		f.held[workspaceID]++
		f.mu.Unlock()
		return f.releaser(workspaceID), nil
	}
	ready := make(chan struct{})
	queue, ok := f.waiting[workspaceID]
	if !ok {
		queue = list.New()
		f.waiting[workspaceID] = queue
	}
	element := queue.PushBack(ready)
	if _, queued := f.inOrder[workspaceID]; !queued {
		f.inOrder[workspaceID] = f.order.PushBack(workspaceID)
	}
	f.mu.Unlock()

	select {
	case <-ready:
		return f.releaser(workspaceID), nil
	case <-ctx.Done():
		// Remove ourselves. A waiter that gives up must not leave a slot
		// promised to a channel nobody is reading — that slot would be lost
		// for the lifetime of the process.
		f.mu.Lock()
		if queue, ok := f.waiting[workspaceID]; ok {
			queue.Remove(element)
			if queue.Len() == 0 {
				delete(f.waiting, workspaceID)
				if el, ok := f.inOrder[workspaceID]; ok {
					f.order.Remove(el)
					delete(f.inOrder, workspaceID)
				}
			}
		}
		select {
		case <-ready:
			// We were admitted between the ctx firing and taking the lock.
			// The slot is ours and nobody else is going to release it, so
			// hand it back rather than leaking it.
			f.held[workspaceID]++
			f.mu.Unlock()
			f.releaser(workspaceID)()
			return nil, ctx.Err()
		default:
		}
		f.mu.Unlock()
		return nil, ctx.Err()
	}
}

// admissible reports whether one more holder for this workspace stays within
// its fair share. Caller holds the lock.
func (f *FairShare) admissible(workspaceID string) bool {
	total := 0
	for _, n := range f.held {
		total += n
	}
	if total >= f.capacity {
		return false
	}
	// Competitors are workspaces currently holding or waiting, plus this one.
	competitors := map[string]struct{}{workspaceID: {}}
	for ws, n := range f.held {
		if n > 0 {
			competitors[ws] = struct{}{}
		}
	}
	for ws, queue := range f.waiting {
		if queue.Len() > 0 {
			competitors[ws] = struct{}{}
		}
	}
	share := (f.capacity + len(competitors) - 1) / len(competitors) // ceil
	return f.held[workspaceID] < share
}

func (f *FairShare) releaser(workspaceID string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			if f.held[workspaceID] > 0 {
				f.held[workspaceID]--
				if f.held[workspaceID] == 0 {
					delete(f.held, workspaceID)
				}
			}
			f.promote()
			f.mu.Unlock()
		})
	}
}

// promote wakes waiters in round-robin workspace order. Caller holds the lock.
//
// Round-robin over WORKSPACES, not over waiters: draining one workspace's
// whole queue before looking at the next is first-come again, wearing a
// queue. The loop stops when a full pass admits nobody, so a workspace already
// at its share does not spin.
func (f *FairShare) promote() {
	for f.order.Len() > 0 {
		admitted := false
		for i, element := 0, f.order.Front(); element != nil && i < f.order.Len(); i++ {
			next := element.Next()
			workspaceID, _ := element.Value.(string)
			queue := f.waiting[workspaceID]
			if queue == nil || queue.Len() == 0 {
				f.order.Remove(element)
				delete(f.inOrder, workspaceID)
				delete(f.waiting, workspaceID)
				element = next
				continue
			}
			if !f.admissible(workspaceID) {
				element = next
				continue
			}
			front := queue.Front()
			queue.Remove(front)
			if ready, ok := front.Value.(chan struct{}); ok {
				close(ready)
			}
			f.held[workspaceID]++
			admitted = true
			// Move this workspace to the back so the next promotion starts
			// with somebody else. This is the round-robin.
			f.order.MoveToBack(element)
			if queue.Len() == 0 {
				f.order.Remove(element)
				delete(f.inOrder, workspaceID)
				delete(f.waiting, workspaceID)
			}
			element = next
		}
		if !admitted {
			return
		}
	}
}

// Held returns a snapshot of in-flight holders per workspace, for diagnostics
// and for tests that must assert the share rather than infer it from timing.
func (f *FairShare) Held() map[string]int {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]int, len(f.held))
	for ws, n := range f.held {
		out[ws] = n
	}
	return out
}
