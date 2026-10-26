// Package hooks defines Soulacy's stable in-process lifecycle extension API.
//
// Hooks observe agent execution and may deny operations at explicit "before"
// stages. They never bypass the runtime's policy, sandbox, tool allowlists, or
// provider accounting. A zero Decision allows execution.
package hooks

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Stage identifies a stable lifecycle boundary.
type Stage string

const (
	RunBefore    Stage = "run.before"
	RunAfter     Stage = "run.after"
	ContextAfter Stage = "context.after"
	LLMBefore    Stage = "llm.before"
	LLMAfter     Stage = "llm.after"
	ToolBefore   Stage = "tool.before"
	ToolAfter    Stage = "tool.after"
)

// Event contains bounded execution metadata. Data is stage-specific and must
// not contain provider secrets or encrypted website-session material.
type Event struct {
	Stage     Stage          `json:"stage"`
	RunID     string         `json:"run_id,omitempty"`
	AgentID   string         `json:"agent_id,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
	Data      map[string]any `json:"data,omitempty"`
}

// Decision is returned by a Handler. Deny is honored only at a before stage.
// Reason should explain what an operator can change to unblock the operation.
type Decision struct {
	Deny   bool   `json:"deny,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Handler handles one lifecycle event.
type Handler interface {
	Handle(context.Context, Event) (Decision, error)
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(context.Context, Event) (Decision, error)

func (f HandlerFunc) Handle(ctx context.Context, event Event) (Decision, error) {
	return f(ctx, event)
}

type registration struct {
	name    string
	stages  map[Stage]bool
	handler Handler
}

// Registry is a concurrency-safe ordered hook chain. Registration order is
// execution order, making behavior deterministic in tests and production.
type Registry struct {
	mu       sync.RWMutex
	handlers []registration
}

// Register adds a named handler. An empty stages list subscribes to all stages.
func (r *Registry) Register(name string, handler Handler, stages ...Stage) error {
	if r == nil {
		return fmt.Errorf("hooks: nil registry")
	}
	if name == "" {
		return fmt.Errorf("hooks: name is required")
	}
	if handler == nil {
		return fmt.Errorf("hooks: handler %q is nil", name)
	}
	set := make(map[Stage]bool, len(stages))
	for _, stage := range stages {
		set[stage] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.handlers {
		if existing.name == name {
			return fmt.Errorf("hooks: handler %q is already registered", name)
		}
	}
	r.handlers = append(r.handlers, registration{name: name, stages: set, handler: handler})
	return nil
}

// Names returns registered names in execution order.
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.handlers))
	for i, h := range r.handlers {
		out[i] = h.name
	}
	return out
}

// Run invokes subscribed handlers. The first error or denial stops the chain.
func (r *Registry) Run(ctx context.Context, event Event) (Decision, error) {
	if r == nil {
		return Decision{}, nil
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	r.mu.RLock()
	handlers := append([]registration(nil), r.handlers...)
	r.mu.RUnlock()
	for _, registered := range handlers {
		if len(registered.stages) > 0 && !registered.stages[event.Stage] {
			continue
		}
		decision, err := callHandler(ctx, registered, event)
		if err != nil {
			return Decision{}, fmt.Errorf("hook %q at %s: %w", registered.name, event.Stage, err)
		}
		if decision.Deny && (event.Stage == RunBefore || event.Stage == LLMBefore || event.Stage == ToolBefore) {
			if decision.Reason == "" {
				decision.Reason = "denied by lifecycle hook " + registered.name
			}
			return decision, nil
		}
	}
	return Decision{}, nil
}

func callHandler(ctx context.Context, registered registration, event Event) (decision Decision, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %v", recovered)
		}
	}()
	return registered.handler.Handle(ctx, event)
}
