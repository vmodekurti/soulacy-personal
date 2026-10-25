package llm

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/soulacy/soulacy/internal/metrics"
	sdkllm "github.com/soulacy/soulacy/sdk/llm"
)

type RoutingHint = sdkllm.RoutingHint
type RouteTrace = sdkllm.RouteTrace
type RouteAttempt = sdkllm.RouteAttempt

// RouteCandidate is one provider/model in an ordered policy chain. Model may
// be empty to use the provider's configured default.
type RouteCandidate struct {
	Provider string `json:"provider" mapstructure:"provider" yaml:"provider"`
	Model    string `json:"model" mapstructure:"model" yaml:"model"`
}

// RoutePolicy controls smart selection. A disabled policy preserves the
// historical fixed provider path. Routing never creates token or spend limits.
type RoutePolicy struct {
	Enabled          bool
	DefaultMode      string
	Preset           string
	MaxFailovers     int
	FailureThreshold int
	Cooldown         time.Duration
	Routes           map[string][]RouteCandidate
	Fallbacks        []RouteCandidate
	LocalProviders   map[string]bool
	AllowedProviders []string
	AllowedModels    []string
	AllowedRegions   []string
	ProviderRegions  map[string]string
}

type routeKey struct{ provider, model string }
type routeHealth struct {
	consecutiveFailures int
	openUntil           time.Time
	successes           uint64
	failures            uint64
	ewmaLatency         time.Duration
}

type candidatePreparation struct {
	candidate RouteCandidate
	reason    string
}

type providerAttemptError struct {
	err        error
	attempts   int
	requestIDs []string
}

func (e *providerAttemptError) Error() string { return e.err.Error() }
func (e *providerAttemptError) Unwrap() error { return e.err }

type routeObserverKey struct{}
type routeHintKey struct{}
type completionObserverKey struct{}

// RouteEvent is a prompt-free routing lifecycle event suitable for durable
// run ledgers. Kind is decision, fallback, or completed.
type RouteEvent struct {
	Kind  string
	Trace RouteTrace
}

type RouteObserver func(RouteEvent)

// CompletionEvent describes one logical completion without deciding how much
// prompt/response content is persisted. The runtime applies the operator's
// capture policy before writing it to the durable ledger.
type CompletionEvent struct {
	Kind              string
	ID                string
	RequestedProvider string
	RequestedModel    string
	Request           CompletionRequest
	Response          *CompletionResponse
	Err               error
	Duration          time.Duration
}

type CompletionObserver func(CompletionEvent)

func WithRouteObserver(ctx context.Context, observer RouteObserver) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, routeObserverKey{}, observer)
}

func WithCompletionObserver(ctx context.Context, observer CompletionObserver) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, completionObserverKey{}, observer)
}

// WithRoutingHint supplies run-wide constraints. Request-local non-zero fields
// override these values, while allowlists are intersected by the router.
func WithRoutingHint(ctx context.Context, hint RoutingHint) context.Context {
	return context.WithValue(ctx, routeHintKey{}, hint)
}

func (r *Router) SetRoutePolicy(policy RoutePolicy) {
	policy.DefaultMode = normalizeRoutingMode(policy.DefaultMode)
	if policy.DefaultMode == "" {
		policy.DefaultMode = "auto"
	}
	policy.Preset = strings.ToLower(strings.TrimSpace(policy.Preset))
	if policy.Preset == "" {
		policy.Preset = "balanced"
	}
	if policy.MaxFailovers < 0 {
		policy.MaxFailovers = 2
	}
	if policy.MaxFailovers > 5 {
		policy.MaxFailovers = 5
	}
	if policy.FailureThreshold <= 0 {
		policy.FailureThreshold = 3
	}
	if policy.Cooldown <= 0 {
		policy.Cooldown = 30 * time.Second
	}
	policy = cloneRoutePolicy(policy)
	r.routeMu.Lock()
	r.routing = policy
	// Configuration changes are an explicit operator action. Clear stale
	// circuits so a corrected provider can be used immediately.
	r.health = make(map[routeKey]*routeHealth)
	r.routeMu.Unlock()
}

func (r *Router) RoutePolicy() RoutePolicy {
	r.routeMu.Lock()
	defer r.routeMu.Unlock()
	return cloneRoutePolicy(r.routing)
}

func (r *Router) SmartRoutingEnabled(mode string) bool {
	policy := r.RoutePolicy()
	resolved := normalizeRoutingMode(mode)
	if resolved == "" {
		resolved = policy.DefaultMode
	}
	return policy.Enabled && resolved != "pinned"
}

func cloneRoutePolicy(p RoutePolicy) RoutePolicy {
	p.Routes = cloneRoutes(p.Routes)
	p.Fallbacks = append([]RouteCandidate(nil), p.Fallbacks...)
	p.AllowedProviders = copyRouteValues(p.AllowedProviders)
	p.AllowedModels = copyRouteValues(p.AllowedModels)
	p.AllowedRegions = copyRouteValues(p.AllowedRegions)
	if p.LocalProviders != nil {
		localProviders := make(map[string]bool, len(p.LocalProviders))
		for provider, local := range p.LocalProviders {
			localProviders[provider] = local
		}
		p.LocalProviders = localProviders
	}
	if p.ProviderRegions != nil {
		regions := make(map[string]string, len(p.ProviderRegions))
		for provider, region := range p.ProviderRegions {
			regions[provider] = region
		}
		p.ProviderRegions = regions
	}
	return p
}

func cloneRoutes(in map[string][]RouteCandidate) map[string][]RouteCandidate {
	if in == nil {
		return nil
	}
	out := make(map[string][]RouteCandidate, len(in))
	for k, v := range in {
		out[k] = append([]RouteCandidate(nil), v...)
	}
	return out
}

var routeSequence atomic.Uint64

// Complete selects an ordered, compatible route and fails over only for
// provider availability failures. Each candidate is independently governed by
// completeOne, so retries and fallbacks cannot escape accounting or budgets.
func (r *Router) Complete(ctx context.Context, providerID string, req CompletionRequest) (result *CompletionResponse, resultErr error) {
	callID := fmt.Sprintf("llm-%d-%d", time.Now().UTC().UnixMilli(), routeSequence.Add(1))
	started := time.Now()
	r.emitCompletion(ctx, CompletionEvent{Kind: "prompt", ID: callID, RequestedProvider: providerID, RequestedModel: req.Model, Request: req})
	defer func() {
		r.emitCompletion(ctx, CompletionEvent{Kind: "completion", ID: callID, RequestedProvider: providerID, RequestedModel: req.Model, Request: req, Response: result, Err: resultErr, Duration: time.Since(started)})
	}()
	policy := r.RoutePolicy()
	hint := mergeRoutingHints(ctx, req.Routing)
	mode := normalizeRoutingMode(hint.Mode)
	if mode == "" {
		mode = policy.DefaultMode
	}
	if !policy.Enabled || mode == "pinned" || req.Operation == "embedding" {
		return r.completeFixed(ctx, providerID, req, callID, hint)
	}

	taskClass := effectiveTaskClass(req, hint, mode, policy.Preset)
	trace := RouteTrace{
		ID:   callID,
		Mode: mode, TaskClass: taskClass, Phase: cleanLabel(hint.Phase),
		Decision: "ordered policy route selected from request requirements and provider health",
		Rejected: make(map[string]string),
	}

	candidates := r.routeCandidates(policy, taskClass, providerID, req.Model)
	if policy.Preset == "local" {
		sort.SliceStable(candidates, func(i, j int) bool {
			return policy.LocalProviders[candidates[i].Provider] && !policy.LocalProviders[candidates[j].Provider]
		})
	}
	ready := make([]RouteCandidate, 0, len(candidates))
	for i, prepared := range r.prepareCandidates(ctx, candidates, req, hint, policy) {
		label := candidateLabel(prepared.candidate)
		if label == "/" {
			label = candidateLabel(candidates[i])
		}
		trace.Candidates = append(trace.Candidates, label)
		if prepared.reason != "" {
			trace.Rejected[label] = prepared.reason
			continue
		}
		ready = append(ready, prepared.candidate)
	}
	if len(trace.Rejected) == 0 {
		trace.Rejected = nil
	}
	if len(ready) == 0 {
		trace.Decision = "no configured route satisfied the request constraints"
		r.emitRoute(ctx, "completed", trace)
		return nil, &RouteError{Trace: trace, Err: fmt.Errorf("llm routing: no compatible candidate for class %q", taskClass)}
	}
	r.emitRoute(ctx, "decision", trace)

	maxAttempts := 1 + policy.MaxFailovers
	if maxAttempts > len(ready) {
		maxAttempts = len(ready)
	}
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		candidate := ready[i]
		attemptReq := req
		attemptReq.Model = candidate.Model
		started := time.Now().UTC()
		resp, err := r.completeOne(ctx, candidate.Provider, attemptReq)
		attempt := RouteAttempt{Provider: candidate.Provider, Model: candidate.Model, StartedAt: started, Duration: time.Since(started)}
		if resp != nil {
			attempt.HTTPAttempts = resp.AttemptCount
			attempt.RequestIDs = append([]string(nil), resp.ProviderRequestIDs...)
		}
		var attempted *providerAttemptError
		if errors.As(err, &attempted) {
			attempt.HTTPAttempts = attempted.attempts
			attempt.RequestIDs = append([]string(nil), attempted.requestIDs...)
		}
		if err == nil {
			attempt.Outcome = "success"
			trace.Attempts = append(trace.Attempts, attempt)
			trace.SelectedProvider, trace.SelectedModel = candidate.Provider, candidate.Model
			trace.Fallbacks = i
			trace.Degraded = i > 0
			r.recordRouteHealth(candidate, true, attempt.Duration, policy)
			metrics.LLMRouteDecisionsTotal.WithLabelValues(taskClass, candidate.Provider, candidate.Model, "success").Inc()
			if resp != nil {
				resp.Route = trace
			}
			r.emitRoute(ctx, "completed", trace)
			return resp, nil
		}

		lastErr = err
		diagnosis := ClassifyProviderError(candidate.Provider, err)
		attempt.Outcome = "error"
		attempt.ErrorCategory = string(diagnosis.Category)
		attempt.Error = boundedRouteError(err)
		trace.Attempts = append(trace.Attempts, attempt)
		metrics.LLMRouteDecisionsTotal.WithLabelValues(taskClass, candidate.Provider, candidate.Model, "error").Inc()
		fallbackAllowed := routeFallbackAllowed(diagnosis.Category)
		if fallbackAllowed {
			r.recordRouteHealth(candidate, false, attempt.Duration, policy)
		}
		if ctx.Err() != nil || !fallbackAllowed || i+1 >= maxAttempts {
			break
		}
		trace.Fallbacks++
		trace.Degraded = true
		next := ready[i+1]
		trace.FallbackFrom = candidateLabel(candidate)
		trace.FallbackTo = candidateLabel(next)
		trace.FallbackReason = string(diagnosis.Category)
		metrics.LLMRouteFallbacksTotal.WithLabelValues(candidate.Provider, next.Provider, string(diagnosis.Category)).Inc()
		r.emitRoute(ctx, "fallback", trace)
	}

	trace.Fallbacks = max(0, len(trace.Attempts)-1)
	trace.Degraded = trace.Fallbacks > 0
	r.emitRoute(ctx, "completed", trace)
	return nil, &RouteError{Trace: trace, Err: fmt.Errorf("llm routing exhausted %d candidate(s): %w", len(trace.Attempts), lastErr)}
}

// completeFixed keeps the historical provider call unchanged while recording
// the fixed selection and outcome in the same trace shape as smart routing.
func (r *Router) completeFixed(ctx context.Context, providerID string, req CompletionRequest, callID string, hint RoutingHint) (*CompletionResponse, error) {
	provider, model := providerID, req.Model
	if provider == "" {
		provider = r.DefaultProvider()
	}
	if resolvedProvider, resolvedModel, err := r.ResolveModel(providerID, req.Model); err == nil {
		provider, model = resolvedProvider, resolvedModel
	}
	trace := RouteTrace{
		ID: callID, Mode: "pinned", TaskClass: classifyRequest(req, hint), Phase: cleanLabel(hint.Phase),
		Decision: "fixed provider/model assignment", Candidates: []string{candidateLabel(RouteCandidate{Provider: provider, Model: model})},
		SelectedProvider: provider, SelectedModel: model,
	}
	r.emitRoute(ctx, "decision", trace)
	started := time.Now().UTC()
	resp, err := r.completeOne(ctx, providerID, req)
	attempt := RouteAttempt{Provider: provider, Model: model, StartedAt: started, Duration: time.Since(started)}
	if resp != nil {
		attempt.HTTPAttempts = resp.AttemptCount
		attempt.RequestIDs = append([]string(nil), resp.ProviderRequestIDs...)
	}
	if err != nil {
		attempt.Outcome = "error"
		diagnosis := ClassifyProviderError(provider, err)
		attempt.ErrorCategory = string(diagnosis.Category)
		attempt.Error = boundedRouteError(err)
	} else {
		attempt.Outcome = "success"
	}
	trace.Attempts = []RouteAttempt{attempt}
	if resp != nil {
		resp.Route = trace
	}
	r.emitRoute(ctx, "completed", trace)
	return resp, err
}

// PreviewRoute performs the same capability, allowlist, and health checks as
// Complete without invoking a model or updating success/failure counters.
func (r *Router) PreviewRoute(ctx context.Context, providerID string, req CompletionRequest) (RouteTrace, error) {
	policy := r.RoutePolicy()
	hint := mergeRoutingHints(ctx, req.Routing)
	mode := normalizeRoutingMode(hint.Mode)
	if mode == "" {
		mode = policy.DefaultMode
	}
	if !policy.Enabled || mode == "pinned" {
		provider, model, err := r.ResolveModel(providerID, req.Model)
		trace := RouteTrace{Mode: "pinned", TaskClass: classifyRequest(req, hint), Phase: cleanLabel(hint.Phase), Decision: "fixed provider/model assignment", SelectedProvider: provider, SelectedModel: model, Candidates: []string{provider + "/" + model}}
		return trace, err
	}
	class := effectiveTaskClass(req, hint, mode, policy.Preset)
	trace := RouteTrace{Mode: mode, TaskClass: class, Phase: cleanLabel(hint.Phase), Decision: "preview of compatible candidates in routing order", Rejected: make(map[string]string)}
	candidates := r.routeCandidates(policy, class, providerID, req.Model)
	if policy.Preset == "local" {
		sort.SliceStable(candidates, func(i, j int) bool {
			return policy.LocalProviders[candidates[i].Provider] && !policy.LocalProviders[candidates[j].Provider]
		})
	}
	for _, prepared := range r.prepareCandidates(ctx, candidates, req, hint, policy) {
		label := candidateLabel(prepared.candidate)
		trace.Candidates = append(trace.Candidates, label)
		if prepared.reason != "" {
			trace.Rejected[label] = prepared.reason
			continue
		}
		if trace.SelectedProvider == "" {
			trace.SelectedProvider, trace.SelectedModel = prepared.candidate.Provider, prepared.candidate.Model
		}
	}
	if len(trace.Rejected) == 0 {
		trace.Rejected = nil
	}
	if trace.SelectedProvider == "" {
		return trace, fmt.Errorf("llm routing: no compatible candidate for class %q", class)
	}
	return trace, nil
}

func (r *Router) prepareCandidates(ctx context.Context, candidates []RouteCandidate, req CompletionRequest, hint RoutingHint, policy RoutePolicy) []candidatePreparation {
	prepared := make([]candidatePreparation, len(candidates))
	parallelism := min(4, max(1, len(candidates)))
	semaphore := make(chan struct{}, parallelism)
	var wg sync.WaitGroup
	for i, candidate := range candidates {
		wg.Add(1)
		go func(index int, value RouteCandidate) {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				prepared[index] = candidatePreparation{candidate: value, reason: ctx.Err().Error()}
				return
			}
			defer func() { <-semaphore }()
			resolved, reason := r.prepareCandidate(ctx, value, req, hint, policy)
			prepared[index] = candidatePreparation{candidate: resolved, reason: reason}
		}(i, candidate)
	}
	wg.Wait()
	return prepared
}

func effectiveTaskClass(req CompletionRequest, hint RoutingHint, mode, preset string) string {
	taskClass := classifyRequest(req, hint)
	if mode == "fast" {
		return "simple"
	}
	if mode == "strong" {
		return "complex"
	}
	if preset == "fast" && taskClass == "standard" {
		return "simple"
	}
	if preset == "quality" && taskClass == "standard" {
		return "complex"
	}
	if preset == "quality" && taskClass == "simple" {
		return "standard"
	}
	return taskClass
}

func (r *Router) routeCandidates(policy RoutePolicy, class, provider, model string) []RouteCandidate {
	chain := append([]RouteCandidate(nil), policy.Routes[class]...)
	if len(chain) == 0 && class != "standard" {
		chain = append(chain, policy.Routes["standard"]...)
	}
	if len(chain) == 0 && len(policy.Fallbacks) == 0 && !hasConfiguredRoutes(policy.Routes) {
		chain = r.smartDefaultCandidates(class, provider)
	}
	chain = append(chain, policy.Fallbacks...)
	// The original assignment remains a final safety candidate and is the only
	// candidate when no smart route was configured.
	chain = append(chain, RouteCandidate{Provider: provider, Model: model})
	seen := make(map[string]bool)
	out := make([]RouteCandidate, 0, len(chain))
	for _, candidate := range chain {
		if candidate.Provider == "" {
			candidate.Provider = r.DefaultProvider()
		}
		key := candidate.Provider + "\x00" + candidate.Model
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, candidate)
	}
	return out
}

func hasConfiguredRoutes(routes map[string][]RouteCandidate) bool {
	for _, candidates := range routes {
		if len(candidates) > 0 {
			return true
		}
	}
	return false
}

func (r *Router) smartDefaultCandidates(class, requestedProvider string) []RouteCandidate {
	r.mu.RLock()
	candidates := make([]RouteCandidate, 0, len(r.providers))
	for id, provider := range r.providers {
		model := ""
		if defaults, ok := provider.(sdkllm.DefaultModelProvider); ok {
			model = defaults.DefaultModel()
		}
		candidates = append(candidates, RouteCandidate{Provider: id, Model: model})
	}
	r.mu.RUnlock()
	sort.SliceStable(candidates, func(i, j int) bool {
		if class == "standard" && (candidates[i].Provider == requestedProvider || candidates[j].Provider == requestedProvider) {
			return candidates[i].Provider == requestedProvider
		}
		left, right := modelStrength(candidates[i].Model), modelStrength(candidates[j].Model)
		if left == right {
			return candidates[i].Provider < candidates[j].Provider
		}
		if class == "complex" {
			return left > right
		}
		return left < right
	})
	return candidates
}

func modelStrength(model string) int {
	model = strings.ToLower(model)
	for _, marker := range []string{"nano", "tiny", "mini", "flash", "haiku", "small", "1b", "3b", "7b", "8b"} {
		if strings.Contains(model, marker) {
			return 1
		}
	}
	for _, marker := range []string{"opus", "sonnet", "gpt-5", "large", "pro", "32b", "34b", "70b", "72b", "405b", "max"} {
		if strings.Contains(model, marker) {
			return 3
		}
	}
	return 2
}

func (r *Router) prepareCandidate(ctx context.Context, candidate RouteCandidate, req CompletionRequest, hint RoutingHint, policy RoutePolicy) (RouteCandidate, string) {
	if !allowedRouteValue(hint.AllowedProviders, candidate.Provider) {
		return candidate, "provider is outside the agent allowlist"
	}
	if !allowedRouteValue(policy.AllowedProviders, candidate.Provider) {
		return candidate, "provider is outside the workspace allowlist"
	}
	provider, model, err := r.ResolveModel(candidate.Provider, candidate.Model)
	if err != nil {
		return candidate, boundedRouteError(err)
	}
	candidate.Provider, candidate.Model = provider, model
	if policy.Preset == "local" && !policy.LocalProviders[provider] {
		return candidate, "provider is not marked local under the local/private preset"
	}
	if !allowedRouteValue(hint.AllowedModels, model) {
		return candidate, "model is outside the agent allowlist"
	}
	if !allowedRouteValue(policy.AllowedModels, model) {
		return candidate, "model is outside the workspace allowlist"
	}
	if !allowedRouteValue(policy.AllowedRegions, policy.ProviderRegions[provider]) {
		return candidate, "provider region is outside the workspace allowlist"
	}
	if open, until := r.routeCircuitOpen(candidate); open {
		return candidate, "provider/model circuit is open until " + until.UTC().Format(time.RFC3339)
	}
	profile, err := r.DescribeModel(ctx, provider, model)
	if err != nil {
		return candidate, "model capability check failed: " + boundedRouteError(err)
	}
	if profile.Chat == SupportNo {
		return candidate, "model does not support chat completion"
	}
	if len(req.Tools) > 0 && profile.NativeTools == SupportNo {
		return candidate, "model does not support required native tools"
	}
	input := EstimateRequestTokens(req)
	if profile.InputTokens > 0 && input > profile.InputTokens {
		return candidate, fmt.Sprintf("request needs about %d input tokens; model limit is %d", input, profile.InputTokens)
	}
	if profile.ContextTokens > 0 && input+max(1, req.MaxTokens) > profile.ContextTokens {
		return candidate, fmt.Sprintf("request and output need about %d tokens; context limit is %d", input+max(1, req.MaxTokens), profile.ContextTokens)
	}
	return candidate, ""
}

func mergeRoutingHints(ctx context.Context, local RoutingHint) RoutingHint {
	base, _ := ctx.Value(routeHintKey{}).(RoutingHint)
	if local.Mode != "" {
		base.Mode = local.Mode
	}
	if local.TaskClass != "" {
		base.TaskClass = local.TaskClass
	}
	if local.Phase != "" {
		base.Phase = local.Phase
	}
	base.AllowedProviders = intersectRouteValues(base.AllowedProviders, local.AllowedProviders)
	base.AllowedModels = intersectRouteValues(base.AllowedModels, local.AllowedModels)
	return base
}

func intersectRouteValues(a, b []string) []string {
	if a == nil {
		return copyRouteValues(b)
	}
	if b == nil {
		return copyRouteValues(a)
	}
	set := make(map[string]bool, len(b))
	for _, value := range b {
		set[value] = true
	}
	out := make([]string, 0, len(a))
	for _, value := range a {
		if set[value] {
			out = append(out, value)
		}
	}
	return out
}

func copyRouteValues(values []string) []string {
	if values == nil {
		return nil
	}
	copyValues := make([]string, len(values))
	copy(copyValues, values)
	return copyValues
}

func allowedRouteValue(allowed []string, value string) bool {
	if allowed == nil {
		return true
	}
	for _, candidate := range allowed {
		if candidate == value {
			return true
		}
	}
	return false
}

func classifyRequest(req CompletionRequest, hint RoutingHint) string {
	if class := strings.ToLower(strings.TrimSpace(hint.TaskClass)); class == "simple" || class == "standard" || class == "complex" {
		return class
	}
	phase := strings.ToLower(strings.TrimSpace(hint.Phase))
	if strings.Contains(phase, "final") || strings.Contains(phase, "plan") || strings.Contains(phase, "reflect") || strings.Contains(phase, "recover") || strings.Contains(phase, "repair") {
		return "complex"
	}
	score := 0
	input := EstimateRequestTokens(req)
	if input > 12000 {
		score += 2
	} else if input > 4000 {
		score++
	}
	if len(req.Tools) > 8 {
		score += 2
	} else if len(req.Tools) > 0 {
		score++
	}
	if req.ResponseFormat == "json_schema" {
		score++
	}
	switch strings.ToLower(strings.TrimSpace(req.ReasoningEffort)) {
	case "xhigh", "max", "ultra":
		score++
	}
	if score >= 3 {
		return "complex"
	}
	if score == 0 && input <= 4000 && len(req.Tools) == 0 {
		return "simple"
	}
	return "standard"
}

func normalizeRoutingMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "auto", "fast", "strong", "pinned":
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return ""
	}
}

func routeFallbackAllowed(category ProviderCategory) bool {
	switch category {
	case ProviderNetwork, ProviderLocalUnreachable, ProviderRateLimited, ProviderOverloaded,
		ProviderProviderDown, ProviderQuotaExceeded, ProviderBadKey, ProviderForbidden,
		ProviderModelNotFound, ProviderBadEndpoint, ProviderContextTooLarge:
		return true
	default:
		return false
	}
}

func (r *Router) recordRouteHealth(candidate RouteCandidate, success bool, latency time.Duration, policy RoutePolicy) {
	key := routeKey{candidate.Provider, candidate.Model}
	r.routeMu.Lock()
	defer r.routeMu.Unlock()
	h := r.health[key]
	if h == nil {
		h = &routeHealth{}
		r.health[key] = h
	}
	if h.ewmaLatency == 0 {
		h.ewmaLatency = latency
	} else {
		h.ewmaLatency = (h.ewmaLatency*4 + latency) / 5
	}
	if success {
		h.successes++
		h.consecutiveFailures = 0
		h.openUntil = time.Time{}
		return
	}
	h.failures++
	h.consecutiveFailures++
	if h.consecutiveFailures >= policy.FailureThreshold {
		h.openUntil = time.Now().Add(policy.Cooldown)
		metrics.LLMRouteCircuitOpensTotal.WithLabelValues(candidate.Provider, candidate.Model).Inc()
	}
}

func (r *Router) routeCircuitOpen(candidate RouteCandidate) (bool, time.Time) {
	r.routeMu.Lock()
	defer r.routeMu.Unlock()
	h := r.health[routeKey{candidate.Provider, candidate.Model}]
	if h == nil || h.openUntil.IsZero() {
		return false, time.Time{}
	}
	if time.Now().Before(h.openUntil) {
		return true, h.openUntil
	}
	// Cooldown elapsed: re-admit the candidate and begin a fresh failure window.
	h.openUntil = time.Time{}
	h.consecutiveFailures = 0
	return false, time.Time{}
}

func (r *Router) emitRoute(ctx context.Context, kind string, trace RouteTrace) {
	observer, _ := ctx.Value(routeObserverKey{}).(RouteObserver)
	if observer == nil {
		return
	}
	copyTrace := trace
	copyTrace.Candidates = append([]string(nil), trace.Candidates...)
	copyTrace.Attempts = append([]RouteAttempt(nil), trace.Attempts...)
	defer func() { _ = recover() }()
	observer(RouteEvent{Kind: kind, Trace: copyTrace})
}

func (r *Router) emitCompletion(ctx context.Context, event CompletionEvent) {
	observer, _ := ctx.Value(completionObserverKey{}).(CompletionObserver)
	if observer == nil {
		return
	}
	defer func() { _ = recover() }()
	observer(event)
}

func candidateLabel(candidate RouteCandidate) string {
	return candidate.Provider + "/" + candidate.Model
}
func cleanLabel(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 64 {
		value = value[:64]
	}
	return value
}
func boundedRouteError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.TrimSpace(err.Error())
	if len(value) > 512 {
		value = value[:512] + "…"
	}
	return value
}

// RouteError preserves the complete decision chain when every safe candidate
// fails. errors.Is/errors.As still reach the final provider error.
type RouteError struct {
	Trace RouteTrace
	Err   error
}

func (e *RouteError) Error() string {
	if len(e.Trace.Attempts) == 0 {
		if len(e.Trace.Rejected) > 0 {
			labels := make([]string, 0, len(e.Trace.Rejected))
			for label := range e.Trace.Rejected {
				labels = append(labels, label)
			}
			sort.Strings(labels)
			const maxReported = 8
			parts := make([]string, 0, min(len(labels), maxReported)+1)
			for _, label := range labels[:min(len(labels), maxReported)] {
				parts = append(parts, label+" ("+e.Trace.Rejected[label]+")")
			}
			if len(labels) > maxReported {
				parts = append(parts, fmt.Sprintf("%d more rejected candidate(s)", len(labels)-maxReported))
			}
			return "llm routing found no compatible candidate: " + strings.Join(parts, "; ")
		}
		return e.Err.Error()
	}
	parts := make([]string, 0, len(e.Trace.Attempts))
	for _, attempt := range e.Trace.Attempts {
		detail := attempt.ErrorCategory
		if attempt.Error != "" {
			detail += ": " + attempt.Error
		}
		parts = append(parts, candidateLabel(RouteCandidate{Provider: attempt.Provider, Model: attempt.Model})+" ("+detail+")")
	}
	return fmt.Sprintf("llm routing failed after %d candidate(s): %s", len(parts), strings.Join(parts, "; "))
}
func (e *RouteError) Unwrap() error { return e.Err }

func RouteTraceFromError(err error) (RouteTrace, bool) {
	var routeErr *RouteError
	if !errors.As(err, &routeErr) {
		return RouteTrace{}, false
	}
	return routeErr.Trace, true
}

// RoutingHealth returns bounded, sorted operational state for diagnostics.
func (r *Router) RoutingHealth() []map[string]any {
	r.routeMu.Lock()
	defer r.routeMu.Unlock()
	keys := make([]routeKey, 0, len(r.health))
	for key := range r.health {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].provider == keys[j].provider {
			return keys[i].model < keys[j].model
		}
		return keys[i].provider < keys[j].provider
	})
	out := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		h := r.health[key]
		out = append(out, map[string]any{"provider": key.provider, "model": key.model, "successes": h.successes, "failures": h.failures, "consecutive_failures": h.consecutiveFailures, "circuit_open_until": h.openUntil, "ewma_latency_ms": h.ewmaLatency.Milliseconds()})
	}
	return out
}
