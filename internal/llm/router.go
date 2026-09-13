// Package llm implements the LLM routing layer.
// All inference calls go through the Router, which selects the appropriate
// provider based on the agent's LLMConfig and falls back to the global default.
// Adding a new provider requires implementing the Provider interface and
// registering it with Register().
package llm

import (
	"context"
	"fmt"
	"strings"
	"sync"

	sdkllm "github.com/soulacy/soulacy/sdk/llm"
)

// Canonical LLM contract types live in the versioned SDK (Story E9);
// these aliases keep every existing import path working unchanged.
type (
	// CompletionRequest is the provider-agnostic input to an LLM call.
	CompletionRequest = sdkllm.CompletionRequest
	// ChatMessage mirrors the role/content structure used by all major LLM APIs.
	ChatMessage = sdkllm.ChatMessage
	// ToolSchema is the JSON Schema description of a callable tool.
	ToolSchema = sdkllm.ToolSchema
	// CompletionResponse carries the LLM's answer back to the runtime.
	CompletionResponse = sdkllm.CompletionResponse
	// Provider is the interface every LLM inference backend implements.
	Provider = sdkllm.Provider
)

// Router dispatches LLM calls to registered providers.
type Router struct {
	mu                sync.RWMutex
	providers         map[string]Provider
	defaultID         string
	controller        Controller
	profileGeneration uint64
	modelProfiles     profileCache
}

// SetController installs the process-wide LLM admission and accounting
// controller. All calls through this router, including Studio and workflow
// calls, are governed after this point.
func (r *Router) SupportsRunCostBudgets() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.controller.(RunCostController)
	return ok && c.SupportsRunCostBudgets()
}

func (r *Router) SetController(controller Controller) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.controller = controller
}

// BeginGovernedCall and EndGovernedCall let non-chat inference surfaces such
// as embeddings share the router's admission and accounting controller.
func (r *Router) BeginGovernedCall(ctx context.Context, provider string, req *CompletionRequest) (context.Context, Reservation, error) {
	r.mu.RLock()
	controller := r.controller
	r.mu.RUnlock()
	if HasRunCostBudget(ctx) {
		capable, ok := controller.(RunCostController)
		if !ok || !capable.SupportsRunCostBudgets() {
			return ctx, Reservation{}, fmt.Errorf("mission cost limit requires an enabled cost controller")
		}
	}
	if controller == nil {
		return ctx, Reservation{}, nil
	}
	return controller.Before(ctx, provider, req)
}

func (r *Router) EndGovernedCall(ctx context.Context, reservation Reservation, provider string, req CompletionRequest, resp *CompletionResponse, err error) {
	r.mu.RLock()
	controller := r.controller
	r.mu.RUnlock()
	if controller != nil {
		controller.After(context.WithoutCancel(ctx), reservation, provider, req, resp, err)
	}
}

// NewRouter creates a Router with the given default provider ID.
func NewRouter(defaultProviderID string) *Router {
	return &Router{
		providers: make(map[string]Provider),
		defaultID: defaultProviderID,
	}
}

// Register adds or replaces a provider and invalidates cached model metadata.
func (r *Router) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.ID()] = p
	r.profileGeneration++
}

// Complete routes a request to the named provider (or the default if providerID is "").
func (r *Router) Complete(ctx context.Context, providerID string, req CompletionRequest) (*CompletionResponse, error) {
	p, key, controller, snapshotErr := r.modelSnapshot(providerID, req.Model)
	if snapshotErr != nil {
		return nil, snapshotErr
	}
	providerID, req.Model = key.provider, key.model

	if HasRunCostBudget(ctx) {
		capable, ok := controller.(RunCostController)
		if !ok || !capable.SupportsRunCostBudgets() {
			return nil, fmt.Errorf("mission cost limit requires an enabled cost controller")
		}
	}
	profile := UnknownModelProfile(providerID, req.Model)
	if req.Operation != "embedding" {
		var err error
		profile, err = r.describeSnapshot(ctx, p, key)
		if err != nil {
			return nil, err
		}
		if err := applyProfileLimits(profile, &req); err != nil {
			return nil, err
		}
	}
	var reservation Reservation
	var err error
	execution, reserveErr := reserveExecution(ctx, &req)
	if reserveErr != nil {
		return nil, reserveErr
	}
	reservedInput, reservedOutput := EstimateRequestTokens(req), req.MaxTokens
	providerStarted, streamOwnsReservation := false, false
	defer func() {
		if !providerStarted {
			execution.cancel()
		} else if !streamOwnsReservation {
			execution.settle(nil, fmt.Errorf("incomplete provider call"))
		}
	}()
	if controller != nil {
		ctx, reservation, err = controller.Before(ctx, providerID, &req)
		if err != nil {
			if recorder, ok := controller.(RejectionRecorder); ok {
				recorder.Rejected(context.WithoutCancel(ctx), providerID, req, err)
			}
			return nil, err
		}
	}
	// Admission may supply a default output ceiling. Recheck it before the
	// actual call and release any reservation if a controller broke the model
	// contract. Never infer against an unprofiled model after admission.
	if req.Model != key.model {
		err = fmt.Errorf("model preflight: admission changed the selected model")
	} else if execution != nil && (req.MaxTokens > reservedOutput || EstimateRequestTokens(req) > reservedInput) {
		err = fmt.Errorf("model preflight: admission exceeded the reserved token limit")
	} else {
		err = applyProfileLimits(profile, &req)
	}
	if err != nil {
		if controller != nil {
			controller.After(context.WithoutCancel(ctx), reservation, providerID, req, nil, err)
		}
		return nil, err
	}

	providerStarted = true
	providerCtx, retryStats := withRetryStats(ctx)
	resp, callErr := p.Complete(providerCtx, req)
	attemptCount, requestIDs := retryStats.snapshot()
	if resp != nil {
		// Exact provider usage wins. Only missing dimensions use the shared,
		// conservative fallback so admission, context trimming and accounting
		// all speak the same unit.
		if resp.InputTokens == 0 {
			resp.InputTokens = EstimateRequestTokens(req)
		}
		if resp.OutputTokens == 0 && resp.Stream == nil {
			resp.OutputTokens = EstimateTextTokens(resp.Content)
		}
		if resp.TotalTokens == 0 && resp.Stream == nil {
			resp.TotalTokens = resp.InputTokens + resp.OutputTokens + resp.ReasoningTokens + resp.ToolUsePromptTokens
		}
		resp.AttemptCount, resp.ProviderRequestIDs = attemptCount, requestIDs
		if resp.AttemptCount == 0 {
			resp.AttemptCount = 1
		}
		if resp.ProviderRequestID != "" && len(resp.ProviderRequestIDs) == 0 {
			resp.ProviderRequestIDs = []string{resp.ProviderRequestID}
		}
	}
	if callErr != nil || resp == nil || resp.Stream == nil {
		execution.settle(resp, callErr)
	}
	if controller == nil && (resp == nil || resp.Stream == nil || execution == nil || callErr != nil) {
		return resp, callErr
	}
	if callErr != nil || resp == nil || resp.Stream == nil {
		accountingResp := resp
		if accountingResp == nil {
			accountingResp = &CompletionResponse{AttemptCount: attemptCount, ProviderRequestIDs: requestIDs}
			if accountingResp.AttemptCount == 0 {
				accountingResp.AttemptCount = 1
			}
		}
		controller.After(context.WithoutCancel(ctx), reservation, providerID, req, accountingResp, callErr)
		return resp, callErr
	}

	// A stream is not billable-accounting complete until its provider channel
	// closes. Proxy it so After runs exactly once and partial/cancelled output is
	// still measured. Providers may populate exact usage before closing.
	inner := resp.Stream
	proxied := make(chan string, 64)
	resp.Stream = proxied
	streamOwnsReservation = true
	go func() {
		defer close(proxied)
		var output strings.Builder
		forward := true
		for token := range inner {
			output.WriteString(token)
			if forward {
				select {
				case proxied <- token:
				case <-ctx.Done():
					forward = false
				}
			}
		}
		if resp.InputTokens == 0 {
			resp.InputTokens = EstimateRequestTokens(req)
		}
		if resp.OutputTokens == 0 {
			resp.OutputTokens = estimateTextTokens(output.String())
		}
		if resp.TotalTokens == 0 {
			resp.TotalTokens = resp.InputTokens + resp.OutputTokens + resp.ReasoningTokens
		}
		execution.settle(resp, ctx.Err())
		if controller != nil {
			controller.After(context.WithoutCancel(ctx), reservation, providerID, req, resp, ctx.Err())
		}
	}()
	return resp, nil
}

// Provider returns the registered provider with the given ID, or nil if none.
// If id is empty, the default provider is returned.
func (r *Router) Provider(id string) Provider {
	if id == "" {
		id = r.defaultID
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.providers[id]
}

// DefaultProvider returns the configured default provider ID.
func (r *Router) DefaultProvider() string {
	return r.defaultID
}

// ProviderIDs returns the IDs of all registered providers.
func (r *Router) ProviderIDs() []string {
	return r.providerIDs()
}

func (r *Router) providerIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.providers))
	for id := range r.providers {
		ids = append(ids, id)
	}
	return ids
}
