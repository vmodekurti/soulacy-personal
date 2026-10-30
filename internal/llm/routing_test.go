package llm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type routingProvider struct {
	id        string
	mu        sync.Mutex
	calls     int
	requests  []CompletionRequest
	errs      []error
	alwaysErr error
	content   string
	profile   ModelProfile
}

func (p *routingProvider) ID() string { return p.id }
func (p *routingProvider) Complete(_ context.Context, req CompletionRequest) (*CompletionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.requests = append(p.requests, req)
	if p.alwaysErr != nil {
		return nil, p.alwaysErr
	}
	if len(p.errs) > 0 {
		err := p.errs[0]
		p.errs = p.errs[1:]
		if err != nil {
			return nil, err
		}
	}
	return &CompletionResponse{Content: p.content}, nil
}
func (p *routingProvider) Models(context.Context) ([]string, error) {
	return []string{p.profile.Model}, nil
}
func (p *routingProvider) DefaultModel() string { return p.profile.Model }
func (p *routingProvider) ProfileModel(_ context.Context, model string) (ModelProfile, error) {
	profile := p.profile
	profile.Provider, profile.Model = p.id, model
	profile.Source, profile.Chat = "test", SupportYes
	profile.ExpiresAt = time.Now().Add(time.Hour)
	return profile, nil
}

type routeAccountingController struct {
	mu            sync.Mutex
	before, after int
	providers     []string
}

func (c *routeAccountingController) Before(ctx context.Context, provider string, _ *CompletionRequest) (context.Context, Reservation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.before++
	c.providers = append(c.providers, provider)
	return ctx, Reservation{ID: provider}, nil
}
func (c *routeAccountingController) After(_ context.Context, _ Reservation, _ string, _ CompletionRequest, _ *CompletionResponse, _ error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.after++
}

func routedTestRouter(t *testing.T) (*Router, *routingProvider, *routingProvider) {
	t.Helper()
	fast := &routingProvider{id: "fast", content: "fast", profile: UnknownModelProfile("fast", "small")}
	strong := &routingProvider{id: "strong", content: "strong", profile: UnknownModelProfile("strong", "large")}
	router := NewRouter("fast")
	router.Register(fast)
	router.Register(strong)
	router.SetRoutePolicy(RoutePolicy{Enabled: true, DefaultMode: "auto", MaxFailovers: 1, FailureThreshold: 2, Cooldown: time.Minute, Routes: map[string][]RouteCandidate{
		"simple":   {{Provider: "fast", Model: "small"}},
		"standard": {{Provider: "fast", Model: "small"}, {Provider: "strong", Model: "large"}},
		"complex":  {{Provider: "strong", Model: "large"}},
	}})
	return router, fast, strong
}

func TestRoutingClassSelectsSimpleAndComplexModels(t *testing.T) {
	router, fast, strong := routedTestRouter(t)
	resp, err := router.Complete(context.Background(), "fast", CompletionRequest{Model: "small", Routing: RoutingHint{TaskClass: "simple"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "fast" || resp.Route.SelectedProvider != "fast" || resp.Route.TaskClass != "simple" {
		t.Fatalf("simple route = %#v", resp.Route)
	}
	resp, err = router.Complete(context.Background(), "fast", CompletionRequest{Model: "small", Routing: RoutingHint{TaskClass: "complex"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "strong" || resp.Route.SelectedProvider != "strong" {
		t.Fatalf("complex route = %#v", resp.Route)
	}
	if fast.calls != 1 || strong.calls != 1 {
		t.Fatalf("calls fast/strong = %d/%d", fast.calls, strong.calls)
	}
}

func TestRoutingFallbackIsGovernedObservedAndAudited(t *testing.T) {
	router, fast, strong := routedTestRouter(t)
	fast.errs = []error{errors.New("fast: http 503: overloaded")}
	controller := &routeAccountingController{}
	router.SetController(controller)
	var events []RouteEvent
	ctx := WithRouteObserver(context.Background(), func(event RouteEvent) { events = append(events, event) })
	resp, err := router.Complete(ctx, "fast", CompletionRequest{Model: "small", Routing: RoutingHint{TaskClass: "standard"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "strong" || !resp.Route.Degraded || resp.Route.Fallbacks != 1 || len(resp.Route.Attempts) != 2 {
		t.Fatalf("fallback trace = %#v", resp.Route)
	}
	if resp.Route.Attempts[0].HTTPAttempts != 1 {
		t.Fatalf("provider attempt count = %d", resp.Route.Attempts[0].HTTPAttempts)
	}
	if controller.before != 2 || controller.after != 2 {
		t.Fatalf("governance before/after = %d/%d", controller.before, controller.after)
	}
	if len(events) != 3 || events[0].Kind != "decision" || events[1].Kind != "fallback" || events[2].Kind != "completed" {
		t.Fatalf("events = %#v", events)
	}
	if strong.calls != 1 {
		t.Fatalf("strong calls = %d", strong.calls)
	}
}

func TestRoutingDoesNotFallbackForUnknownApplicationError(t *testing.T) {
	router, fast, strong := routedTestRouter(t)
	fast.errs = []error{errors.New("response did not match required schema")}
	_, err := router.Complete(context.Background(), "fast", CompletionRequest{Model: "small", Routing: RoutingHint{TaskClass: "standard"}})
	if err == nil {
		t.Fatal("expected route error")
	}
	trace, ok := RouteTraceFromError(err)
	if !ok || len(trace.Attempts) != 1 {
		t.Fatalf("trace = %#v, ok=%v", trace, ok)
	}
	if strong.calls != 0 {
		t.Fatalf("unsafe fallback reached strong provider %d time(s)", strong.calls)
	}
}

func TestRoutingAllowlistsAreHardFilters(t *testing.T) {
	router, fast, strong := routedTestRouter(t)
	ctx := WithRoutingHint(context.Background(), RoutingHint{AllowedProviders: []string{"strong"}, AllowedModels: []string{"large"}})
	resp, err := router.Complete(ctx, "fast", CompletionRequest{Model: "small", Routing: RoutingHint{TaskClass: "standard"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Route.SelectedProvider != "strong" || fast.calls != 0 || strong.calls != 1 {
		t.Fatalf("route=%#v calls=%d/%d", resp.Route, fast.calls, strong.calls)
	}
}

func TestRoutingCircuitSkipsRepeatedlyFailingCandidate(t *testing.T) {
	router, fast, _ := routedTestRouter(t)
	fast.errs = []error{errors.New("fast: http 503"), errors.New("fast: http 503")}
	for i := 0; i < 2; i++ {
		if _, err := router.Complete(context.Background(), "fast", CompletionRequest{Model: "small", Routing: RoutingHint{TaskClass: "standard"}}); err != nil {
			t.Fatal(err)
		}
	}
	resp, err := router.Complete(context.Background(), "fast", CompletionRequest{Model: "small", Routing: RoutingHint{TaskClass: "standard"}})
	if err != nil {
		t.Fatal(err)
	}
	if fast.calls != 2 || resp.Route.SelectedProvider != "strong" {
		t.Fatalf("circuit did not skip fast: calls=%d route=%#v", fast.calls, resp.Route)
	}
	if resp.Route.Rejected["fast/small"] == "" {
		t.Fatalf("open circuit rejection missing: %#v", resp.Route.Rejected)
	}
}

func TestRoutingDisabledPreservesFixedProvider(t *testing.T) {
	router, fast, strong := routedTestRouter(t)
	router.SetRoutePolicy(RoutePolicy{Enabled: false})
	var events []RouteEvent
	ctx := WithRouteObserver(context.Background(), func(event RouteEvent) { events = append(events, event) })
	resp, err := router.Complete(ctx, "fast", CompletionRequest{Model: "small", Routing: RoutingHint{TaskClass: "complex"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "fast" || fast.calls != 1 || strong.calls != 0 || resp.Route.SelectedProvider != "fast" || resp.Route.Mode != "pinned" {
		t.Fatalf("fixed path changed: resp=%#v calls=%d/%d", resp, fast.calls, strong.calls)
	}
	if len(events) != 2 || events[0].Kind != "decision" || events[1].Kind != "completed" || events[1].Trace.Attempts[0].Outcome != "success" {
		t.Fatalf("fixed route was not observable: %#v", events)
	}
}

func TestRoutingPreviewDoesNotInvokeProviders(t *testing.T) {
	router, fast, strong := routedTestRouter(t)
	trace, err := router.PreviewRoute(context.Background(), "fast", CompletionRequest{Model: "small", Routing: RoutingHint{TaskClass: "complex"}})
	if err != nil {
		t.Fatal(err)
	}
	if trace.SelectedProvider != "strong" || fast.calls != 0 || strong.calls != 0 {
		t.Fatalf("preview=%#v calls=%d/%d", trace, fast.calls, strong.calls)
	}
}

func TestRoutingErrorNamesEveryFailedCandidate(t *testing.T) {
	router, fast, strong := routedTestRouter(t)
	fast.errs = []error{errors.New("fast: http 503")}
	strong.errs = []error{errors.New("strong: http 429")}
	_, err := router.Complete(context.Background(), "fast", CompletionRequest{Model: "small", Routing: RoutingHint{TaskClass: "standard"}})
	if err == nil {
		t.Fatal("expected failure")
	}
	message := err.Error()
	if !strings.Contains(message, "fast/small") || !strings.Contains(message, "strong/large") || !strings.Contains(message, "provider_down") || !strings.Contains(message, "rate_limited") {
		t.Fatalf("incomplete route error: %s", message)
	}
}

func TestRoutingErrorExplainsRejectedCandidates(t *testing.T) {
	router, _, _ := routedTestRouter(t)
	ctx := WithRoutingHint(context.Background(), RoutingHint{AllowedProviders: []string{}})
	_, err := router.Complete(ctx, "fast", CompletionRequest{Model: "small", Routing: RoutingHint{TaskClass: "complex"}})
	if err == nil || !strings.Contains(err.Error(), "outside the agent allowlist") || !strings.Contains(err.Error(), "strong/large") {
		t.Fatalf("rejection details missing: %v", err)
	}
}

func TestRoutingConcurrentFailoverHasUniqueAuditableRoutes(t *testing.T) {
	router, fast, _ := routedTestRouter(t)
	fast.alwaysErr = errors.New("fast: http 503: unavailable")
	const calls = 100
	ids := make(chan string, calls)
	errs := make(chan error, calls)
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := router.Complete(context.Background(), "fast", CompletionRequest{Model: "small", Routing: RoutingHint{TaskClass: "standard"}})
			if err != nil {
				errs <- err
				return
			}
			ids <- resp.Route.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Errorf("concurrent route failed: %v", err)
	}
	seen := make(map[string]bool, calls)
	for id := range ids {
		if id == "" || seen[id] {
			t.Fatalf("route id is empty or duplicated: %q", id)
		}
		seen[id] = true
	}
	if len(seen) != calls {
		t.Fatalf("completed routes = %d, want %d", len(seen), calls)
	}
}

func TestCompletionObserverCoversFixedAndRoutedCalls(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		router, _, _ := routedTestRouter(t)
		if !enabled {
			router.SetRoutePolicy(RoutePolicy{Enabled: false})
		}
		var events []CompletionEvent
		ctx := WithCompletionObserver(context.Background(), func(event CompletionEvent) { events = append(events, event) })
		if _, err := router.Complete(ctx, "fast", CompletionRequest{Model: "small", Messages: []ChatMessage{{Role: "user", Content: "hello"}}}); err != nil {
			t.Fatal(err)
		}
		if len(events) != 2 || events[0].Kind != "prompt" || events[1].Kind != "completion" || events[0].ID == "" || events[0].ID != events[1].ID {
			t.Fatalf("enabled=%v events=%#v", enabled, events)
		}
	}
}

func TestSmartDefaultsRankSmallAndStrongConfiguredModels(t *testing.T) {
	router := NewRouter("fast")
	fast := &routingProvider{id: "fast", content: "fast", profile: UnknownModelProfile("fast", "vendor-mini")}
	strong := &routingProvider{id: "strong", content: "strong", profile: UnknownModelProfile("strong", "vendor-opus")}
	router.Register(fast)
	router.Register(strong)
	router.SetRoutePolicy(RoutePolicy{Enabled: true, DefaultMode: "auto", MaxFailovers: 1})
	resp, err := router.Complete(context.Background(), "fast", CompletionRequest{Model: "vendor-mini", Routing: RoutingHint{TaskClass: "simple"}})
	if err != nil || resp.Route.SelectedProvider != "fast" {
		t.Fatalf("simple smart default: resp=%#v err=%v", resp, err)
	}
	resp, err = router.Complete(context.Background(), "fast", CompletionRequest{Model: "vendor-mini", Routing: RoutingHint{TaskClass: "complex"}})
	if err != nil || resp.Route.SelectedProvider != "strong" {
		t.Fatalf("complex smart default: resp=%#v err=%v", resp, err)
	}
}

func TestLocalPresetNeverRoutesToRemoteProvider(t *testing.T) {
	router, fast, strong := routedTestRouter(t)
	router.SetRoutePolicy(RoutePolicy{Enabled: true, DefaultMode: "auto", Preset: "local", MaxFailovers: 1, LocalProviders: map[string]bool{"fast": true}, Routes: map[string][]RouteCandidate{"complex": {{Provider: "strong", Model: "large"}, {Provider: "fast", Model: "small"}}}})
	resp, err := router.Complete(context.Background(), "strong", CompletionRequest{Model: "large", Routing: RoutingHint{TaskClass: "complex"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Route.SelectedProvider != "fast" || strong.calls != 0 || fast.calls != 1 {
		t.Fatalf("local route escaped: %#v calls=%d/%d", resp.Route, fast.calls, strong.calls)
	}
}
