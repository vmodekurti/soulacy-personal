package gateway

import (
	"context"
	"sync"
	"time"

	"github.com/soulacy/soulacy/internal/runcontrol"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// runRegistry tracks in-flight agent runs so they can be cancelled (Story #22).
// A chat/stream handler registers its run's cancel func under a run id and emits
// that id to the client; a POST /chat/cancel then cancels it. Concurrency-safe.
type runRegistry struct {
	mu     sync.Mutex
	runs   map[string]context.CancelFunc
	shared *runcontrol.Store
}

func (r *runRegistry) SetShared(store *runcontrol.Store) { r.shared = store }

func runKey(workspaceID, id string) string { return wsroot.Normalize(workspaceID) + "\x00" + id }

func (r *runRegistry) RegisterScoped(ctx context.Context, workspaceID, id string, cancel context.CancelFunc, ttl time.Duration) error {
	if r.shared == nil {
		r.Register(id, cancel)
		return nil
	}
	key := runKey(workspaceID, id)
	r.Register(key, cancel)
	if err := r.shared.Register(ctx, workspaceID, id, ttl); err != nil {
		r.Done(key)
		return err
	}
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				requested, err := r.shared.CancelRequested(ctx, workspaceID, id)
				if err == nil && requested {
					r.Cancel(key)
					return
				}
			}
		}
	}()
	return nil
}

func (r *runRegistry) DoneScoped(workspaceID, id string) {
	if r.shared == nil {
		r.Done(id)
		return
	}
	r.Done(runKey(workspaceID, id))
	_ = r.shared.Done(context.Background(), workspaceID, id)
}

func (r *runRegistry) CancelScoped(ctx context.Context, workspaceID, id string) bool {
	if r.shared != nil {
		requested, err := r.shared.RequestCancel(ctx, workspaceID, id)
		if err != nil || !requested {
			return false
		}
		r.Cancel(runKey(workspaceID, id))
		return true
	}
	return r.Cancel(id)
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
