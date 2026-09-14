package runtime

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// AdaptiveMemoryOptions are the runtime-side knobs for adaptive memory. The
// engine itself (local or Mem0) is supplied separately so a provider swap is
// one pointer store.
type AdaptiveMemoryOptions struct {
	Enabled bool
	// ModelProvider/Model select the lightweight model used for extraction
	// and arbitration. Empty falls back to the agent's own provider/model.
	ModelProvider string
	Model         string
	// MaxPromptFacts caps how many facts are recalled per turn (default 5).
	MaxPromptFacts int
	// PromptTokenBudget caps the injected block (default 50 tokens).
	PromptTokenBudget int
	// GraphEnabled reports whether entity relations are extracted and
	// recalled alongside facts.
	GraphEnabled bool
}

type adaptiveRuntime struct {
	engine memory.Adaptive
	opts   AdaptiveMemoryOptions
}

// adaptiveWorkers bounds concurrent background extractions so a burst of
// turns cannot fan out into unbounded model calls.
const adaptiveWorkers = 4

var adaptiveSem = make(chan struct{}, adaptiveWorkers)

// SetAdaptiveMemory installs (or replaces) the adaptive memory engine. Pass a
// nil engine to disable. Safe to call while runs are in flight: each turn
// loads the pointer once.
func (e *Engine) SetAdaptiveMemory(m memory.Adaptive, opts AdaptiveMemoryOptions) {
	if opts.MaxPromptFacts <= 0 {
		opts.MaxPromptFacts = 5
	}
	if opts.PromptTokenBudget <= 0 {
		opts.PromptTokenBudget = memory.DefaultPromptTokenBudget
	}
	if m == nil {
		e.adaptive.Store(nil)
		return
	}
	e.adaptive.Store(&adaptiveRuntime{engine: m, opts: opts})
}

// AdaptiveMemory returns the installed engine, or nil.
func (e *Engine) AdaptiveMemory() memory.Adaptive {
	r := e.adaptive.Load()
	if r == nil {
		return nil
	}
	return r.engine
}

// AdaptiveMemoryOptions returns the installed options (zero when disabled).
func (e *Engine) AdaptiveMemoryOptions() AdaptiveMemoryOptions {
	r := e.adaptive.Load()
	if r == nil {
		return AdaptiveMemoryOptions{}
	}
	return r.opts
}

type adaptiveModelKey struct{}

type adaptiveModel struct{ provider, model string }

// AdaptiveCompleter returns a memory.Completer that routes extraction and
// arbitration calls through the governed LLM router. Provider and model
// resolve, in order: the options on the context (set per turn from the agent
// definition), the explicit arguments, then the router default.
func (e *Engine) AdaptiveCompleter(provider, model string) memory.Completer {
	return func(ctx context.Context, system, user string, maxTokens int) (string, error) {
		p, m := strings.TrimSpace(provider), strings.TrimSpace(model)
		if hint, ok := ctx.Value(adaptiveModelKey{}).(adaptiveModel); ok {
			if p == "" {
				p, m = hint.provider, hint.model
			}
		}
		if p == "" && e.llmRouter != nil {
			p = e.llmRouter.DefaultProvider()
		}
		if maxTokens <= 0 {
			maxTokens = 320
		}
		meta := llm.CallMetadataFromContext(ctx)
		meta.Source = "memory.adaptive"
		meta.RunID = uuid.NewString()
		ctx = llm.WithCallMetadata(ctx, meta)
		ctx, cancel := context.WithTimeout(ctx, e.effectiveLLMTimeout())
		defer cancel()
		resp, err := e.llmRouter.Complete(ctx, p, llm.CompletionRequest{
			Model:          m,
			Messages:       []llm.ChatMessage{{Role: "system", Content: system}, {Role: "user", Content: user}},
			MaxTokens:      maxTokens,
			Temperature:    0,
			ResponseFormat: "json",
		})
		if err != nil {
			return "", err
		}
		if resp == nil {
			return "", nil
		}
		return resp.Content, nil
	}
}

// adaptiveScope decides whether this turn participates in adaptive memory and
// returns its tenancy scope. Turns without a stable authenticated identity,
// on shared external channels, in simulations, or in dry runs are excluded:
// facts must always be attributable to one person.
func (e *Engine) adaptiveScope(ctx context.Context, def *agent.Definition, msg message.Message) (*adaptiveRuntime, memory.FactScope, bool) {
	r := e.adaptive.Load()
	if r == nil || !r.opts.Enabled || def == nil {
		return nil, memory.FactScope{}, false
	}
	if def.Memory.Adaptive != nil && !*def.Memory.Adaptive {
		return nil, memory.FactScope{}, false
	}
	if isSharedExternalChannel(msg.Channel) || missionSimulation(ctx) || dryRunFrom(ctx) {
		return nil, memory.FactScope{}, false
	}
	p, ok := PrincipalFromContext(ctx)
	if !ok || strings.TrimSpace(p.Subject) == "" || strings.HasPrefix(p.Subject, "builtin:") {
		return nil, memory.FactScope{}, false
	}
	meta := llm.CallMetadataFromContext(ctx)
	scope := memory.FactScope{Workspace: meta.Workspace, Owner: p.Subject, AgentID: def.ID, SessionID: msg.SessionID}.Normalize()
	return r, scope, true
}

// startAdaptiveMemory recalls the facts most relevant to the incoming message
// and appends them to the system prompt under the memory heading. It is
// bounded by a short timeout so a slow embedder can never stall a turn.
func (e *Engine) startAdaptiveMemory(ctx context.Context, def *agent.Definition, msg message.Message) {
	r, scope, ok := e.adaptiveScope(ctx, def, msg)
	if !ok {
		return
	}
	query := strings.TrimSpace(flattenParts(msg.Parts))
	rctx, cancel := context.WithTimeout(ctx, e.adaptiveRecallTimeout())
	defer cancel()
	facts, err := r.engine.Recall(rctx, scope, query, r.opts.MaxPromptFacts)
	if err != nil {
		e.log.Debug("adaptive memory recall failed", zap.String("agent", def.ID), zap.Error(err))
		return
	}
	var rels []memory.Relation
	if r.opts.GraphEnabled {
		// Relations are a bonus: a failure here never blocks the fact block.
		rels, _ = r.engine.Relations(rctx, scope, query, 3)
	}
	block := memory.FormatPromptBlock(facts, r.opts.PromptTokenBudget, rels...)
	if block == "" {
		return
	}
	def.SystemPrompt += "\n\n" + block
}

// adaptiveRecallTimeout derives the per-turn recall deadline from the
// configured LLM timeout: recall is one embedding call, so it gets a small
// slice of that budget, clamped to a range that keeps turns snappy.
func (e *Engine) adaptiveRecallTimeout() time.Duration {
	d := e.effectiveLLMTimeout() / 20
	if d < time.Second {
		d = time.Second
	}
	if d > 3*time.Second {
		d = 3 * time.Second
	}
	return d
}

// adaptivePending lets tests and graceful shutdown wait for background
// extractions.
var adaptivePending sync.WaitGroup

// WaitAdaptiveMemory blocks until queued extractions finish or timeout
// elapses. Returns false on timeout.
func WaitAdaptiveMemory(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() { adaptivePending.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// queueAdaptiveMemory schedules extraction for a completed turn. It returns
// immediately; the reply is never delayed by memory work. When all workers
// are busy the turn is skipped rather than queued, and the skip is logged, so
// memory can lag under load but can never pile up unbounded.
func (e *Engine) queueAdaptiveMemory(ctx context.Context, def *agent.Definition, msg message.Message, userText, reply string) {
	r, scope, ok := e.adaptiveScope(ctx, def, msg)
	if !ok {
		return
	}
	turn := memory.Turn{SessionID: msg.SessionID, RunID: msg.ID, User: userText, Assistant: reply}
	if !memory.ShouldExtract(turn) {
		return
	}
	select {
	case adaptiveSem <- struct{}{}:
	default:
		e.log.Warn("adaptive memory: extraction skipped, workers busy", zap.String("agent", def.ID))
		return
	}
	// Detach from the request context: the HTTP response has already been
	// sent. Keep governance metadata and the principal so the model call is
	// attributed to the same user, and give the work its own deadline.
	meta := llm.CallMetadataFromContext(ctx)
	principal, _ := PrincipalFromContext(ctx)
	bg := llm.WithCallMetadata(context.Background(), meta)
	bg = WithPrincipal(bg, principal)
	bg = context.WithValue(bg, adaptiveModelKey{}, adaptiveModel{provider: def.LLM.Provider, model: def.LLM.Model})
	agentID, agentName := def.ID, def.Name
	adaptivePending.Add(1)
	go func() {
		defer adaptivePending.Done()
		defer func() { <-adaptiveSem }()
		// One extraction plus at most a few arbitration calls: the configured
		// LLM deadline is the right bound.
		bg, cancel := context.WithTimeout(bg, e.effectiveLLMTimeout())
		defer cancel()
		out, err := r.engine.Remember(bg, scope, turn)
		if err != nil {
			e.log.Warn("adaptive memory: remember failed", zap.String("agent", agentID), zap.String("provider", r.engine.Provider()), zap.Error(err))
			return
		}
		if out.Candidates == 0 && len(out.Added) == 0 {
			return
		}
		e.log.Info("adaptive memory updated",
			zap.String("agent", agentID), zap.String("provider", out.Provider),
			zap.Int("candidates", out.Candidates), zap.Int("added", len(out.Added)),
			zap.Int("superseded", len(out.Superseded)), zap.Int("retracted", len(out.Retracted)),
			zap.Int("relations", out.RelationsAdded), zap.Int("skipped", out.Skipped))
		if e.sink != nil && (len(out.Added) > 0 || len(out.Superseded) > 0 || len(out.Retracted) > 0 || out.RelationsAdded > 0) {
			e.sink.Emit(message.Event{
				Type: "memory.adaptive", AgentID: agentID, SessionID: msg.SessionID,
				Payload: map[string]any{
					"agent_name": agentName, "provider": out.Provider,
					"added": len(out.Added), "superseded": len(out.Superseded), "retracted": len(out.Retracted),
					"relations": out.RelationsAdded, "skipped": out.Skipped,
				},
				Timestamp: time.Now().UTC(),
			})
		}
	}()
}

// adaptiveField is embedded in Engine; declared here to keep the engine
// struct edit minimal.
type adaptiveField = atomic.Pointer[adaptiveRuntime]
