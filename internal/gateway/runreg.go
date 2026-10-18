package gateway

import (
	"context"
	"sync"
)

// runRegistry tracks in-flight agent runs so they can be cancelled (Story #22).
// A chat/stream handler registers its run's cancel func under a run id and emits
// that id to the client; a POST /chat/cancel then cancels it. Concurrency-safe.
type runRegistry struct {
	mu   sync.Mutex
	next uint64
	runs map[string]map[uint64]context.CancelFunc
}

func newRunRegistry() *runRegistry {
	return &runRegistry{runs: make(map[string]map[uint64]context.CancelFunc)}
}

// Register returns cleanup for this specific run. Synchronous requests share
// a session alias; finishing one must not deregister another queued request.
func (r *runRegistry) Register(id string, cancel context.CancelFunc) func() {
	if id == "" || cancel == nil {
		return func() {}
	}
	r.mu.Lock()
	r.next++
	ticket := r.next
	if r.runs[id] == nil {
		r.runs[id] = make(map[uint64]context.CancelFunc)
	}
	r.runs[id][ticket] = cancel
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.runs[id], ticket)
		if len(r.runs[id]) == 0 {
			delete(r.runs, id)
		}
	}
}

// Done removes id from the registry (call on run completion).
func (r *runRegistry) Done(id string) {
	r.mu.Lock()
	delete(r.runs, id)
	r.mu.Unlock()
}

// Active reports how many runs are in flight. The auto-updater uses it to
// restart only when nobody is mid-conversation.
func (r *runRegistry) Active() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, runs := range r.runs {
		count += len(runs)
	}
	return count
}

// Cancel stops the run, or every request sharing a legacy session alias.
func (r *runRegistry) Cancel(id string) bool {
	r.mu.Lock()
	runs, ok := r.runs[id]
	if ok {
		delete(r.runs, id)
	}
	r.mu.Unlock()
	if ok {
		for _, cancel := range runs {
			cancel()
		}
	}
	return ok
}
