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
	runs map[string]context.CancelFunc
}

func newRunRegistry() *runRegistry {
	return &runRegistry{runs: make(map[string]context.CancelFunc)}
}

// Register stores cancel under id (overwriting any prior run with that id).
func (r *runRegistry) Register(id string, cancel context.CancelFunc) {
	if id == "" || cancel == nil {
		return
	}
	r.mu.Lock()
	r.runs[id] = cancel
	r.mu.Unlock()
}

// Done removes id from the registry (call on run completion).
func (r *runRegistry) Done(id string) {
	r.mu.Lock()
	delete(r.runs, id)
	r.mu.Unlock()
}

// Cancel cancels the run with id and removes it. Returns true if found.
func (r *runRegistry) Cancel(id string) bool {
	r.mu.Lock()
	cancel, ok := r.runs[id]
	if ok {
		delete(r.runs, id)
	}
	r.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}
