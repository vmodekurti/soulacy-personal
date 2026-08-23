// Package remote moves untrusted Python execution out of the gateway process.
// Jobs and results travel through the configured durable queue; workers are
// stateless and disposable and acknowledge a job only after publishing its
// result.
package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/soulacy/soulacy/internal/executor"
	"github.com/soulacy/soulacy/internal/executor/process"
	"github.com/soulacy/soulacy/internal/queue"
	"github.com/soulacy/soulacy/internal/runtime"
)

const JobsSubject = "soulacy.execution.jobs"

type job struct {
	ID, Kind, Script string
	Args             []byte
	Command          *runtime.PrivilegedCommand
}
type result struct{ ID, Output, Error string }

type Executor struct{ queue queue.Backend }

func New(q queue.Backend) *Executor { return &Executor{queue: q} }

var _ executor.Backend = (*Executor)(nil)

func (e *Executor) Run(ctx context.Context, pyFile, funcName, inline string, args []byte) (string, error) {
	script, err := process.BuildScript(pyFile, funcName, inline)
	if err != nil {
		return "", err
	}
	id := uuid.NewString()
	subject := "soulacy.execution.results." + id
	ch := make(chan result, 1)
	sub, err := e.queue.Subscribe(ctx, subject, "", func(m *queue.Message) {
		var r result
		if json.Unmarshal(m.Data, &r) == nil {
			select {
			case ch <- r:
			default:
			}
		}
		_ = m.Ack()
	})
	if err != nil {
		return "", fmt.Errorf("remote executor subscribe: %w", err)
	}
	defer sub.Unsubscribe()
	payload, _ := json.Marshal(job{ID: id, Kind: "python", Script: script, Args: args})
	if err := e.queue.Publish(ctx, JobsSubject, payload); err != nil {
		return "", fmt.Errorf("remote executor publish: %w", err)
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-ch:
		if r.Error != "" {
			return r.Output, fmt.Errorf("remote worker: %s", r.Error)
		}
		return r.Output, nil
	}
}
func (e *Executor) Close() error { return nil }

type Worker struct {
	queue      queue.Backend
	backend    executor.Backend
	privileged runtime.PrivilegedCommandRunner
	group      string
	sem        chan struct{}
	sub        queue.Subscription
	once       sync.Once
}

func (w *Worker) SetPrivilegedRunner(r runtime.PrivilegedCommandRunner) { w.privileged = r }

func NewWorker(q queue.Backend, backend executor.Backend, group string, concurrency int) *Worker {
	if group == "" {
		group = "execution-workers"
	}
	if concurrency < 1 {
		concurrency = 4
	}
	return &Worker{queue: q, backend: backend, group: group, sem: make(chan struct{}, concurrency)}
}
func (w *Worker) Start(ctx context.Context) error {
	sub, err := w.queue.Subscribe(ctx, JobsSubject, w.group, func(m *queue.Message) {
		select {
		case w.sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		go func() {
			defer func() { <-w.sem }()
			var j job
			r := result{}
			if err := json.Unmarshal(m.Data, &j); err != nil {
				return
			}
			r.ID = j.ID
			runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			defer cancel()
			var err error
			if j.Kind == "command" && j.Command != nil {
				if w.privileged == nil {
					err = fmt.Errorf("privileged execution worker is unavailable")
				} else {
					r.Output, err = w.privileged.Run(runCtx, *j.Command)
				}
			} else {
				r.Output, err = w.backend.Run(runCtx, "", "", j.Script, j.Args)
			}
			if err != nil {
				r.Error = err.Error()
			}
			data, _ := json.Marshal(r)
			if w.queue.Publish(context.WithoutCancel(ctx), "soulacy.execution.results."+j.ID, data) == nil {
				_ = m.Ack()
			}
		}()
	})
	if err != nil {
		return err
	}
	w.sub = sub
	return nil
}

type PrivilegedRunner struct{ queue queue.Backend }

func NewPrivilegedRunner(q queue.Backend) *PrivilegedRunner { return &PrivilegedRunner{queue: q} }
func (*PrivilegedRunner) Mode() string                      { return "worker" }
func (r *PrivilegedRunner) Run(ctx context.Context, command runtime.PrivilegedCommand) (string, error) {
	id := uuid.NewString()
	subject := "soulacy.execution.results." + id
	ch := make(chan result, 1)
	sub, err := r.queue.Subscribe(ctx, subject, "", func(m *queue.Message) {
		var out result
		if json.Unmarshal(m.Data, &out) == nil {
			select {
			case ch <- out:
			default:
			}
		}
		_ = m.Ack()
	})
	if err != nil {
		return "", err
	}
	defer sub.Unsubscribe()
	payload, _ := json.Marshal(job{ID: id, Kind: "command", Command: &command})
	if err := r.queue.Publish(ctx, JobsSubject, payload); err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case out := <-ch:
		if out.Error != "" {
			return out.Output, fmt.Errorf("remote worker: %s", out.Error)
		}
		return out.Output, nil
	}
}
func (w *Worker) Close() error {
	var err error
	w.once.Do(func() {
		if w.sub != nil {
			err = w.sub.Unsubscribe()
		}
		_ = w.backend.Close()
	})
	return err
}
