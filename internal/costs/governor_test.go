package costs

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/llm"
)

type governedProvider struct {
	response *llm.CompletionResponse
	err      error
	request  llm.CompletionRequest
	calls    int
	delay    time.Duration
}

type cancellingStreamProvider struct{}

func (cancellingStreamProvider) ID() string { return "paid" }
func (cancellingStreamProvider) Models(context.Context) ([]string, error) {
	return []string{"model"}, nil
}
func (cancellingStreamProvider) Complete(ctx context.Context, _ llm.CompletionRequest) (*llm.CompletionResponse, error) {
	stream := make(chan string, 1)
	stream <- "partial output"
	go func() {
		<-ctx.Done()
		close(stream)
	}()
	return &llm.CompletionResponse{Stream: stream}, nil
}

func (p *governedProvider) ID() string { return "paid" }
func (p *governedProvider) Models(context.Context) ([]string, error) {
	return []string{"model"}, nil
}
func (p *governedProvider) Complete(_ context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.request = req
	p.calls++
	if p.delay > 0 {
		time.Sleep(p.delay)
	}
	return p.response, p.err
}

func TestGovernorClampsOutputAndEnforcesModelPolicy(t *testing.T) {
	provider := &governedProvider{response: &llm.CompletionResponse{Content: "ok"}}
	router, _ := newGovernedRouter(t, GovernanceConfig{
		MaxOutputCeiling: 64, AllowedModels: []string{"model"},
	}, provider)
	if _, err := router.Complete(context.Background(), "paid", llm.CompletionRequest{Model: "other", MaxTokens: 1000}); err == nil {
		t.Fatal("expected model policy rejection")
	}
	if _, err := router.Complete(context.Background(), "paid", llm.CompletionRequest{Model: "model", MaxTokens: 1000}); err != nil {
		t.Fatal(err)
	}
	if provider.request.MaxTokens != 64 {
		t.Fatalf("max tokens = %d, want ceiling 64", provider.request.MaxTokens)
	}
}

func TestGovernorEnforcesRegionAndClassificationAwareCaching(t *testing.T) {
	provider := &governedProvider{response: &llm.CompletionResponse{Content: "ok"}}
	router, _ := newGovernedRouter(t, GovernanceConfig{
		AllowedRegions: []string{"us"}, ProviderPolicies: map[string]ProviderPolicy{
			"paid": {Region: "us", PromptCaching: true, CacheAllowedDataClasses: []string{"public"}},
		},
	}, provider)
	ctx := llm.WithCallMetadata(context.Background(), llm.CallMetadata{DataClassification: "confidential"})
	if _, err := router.Complete(ctx, "paid", llm.CompletionRequest{Model: "model"}); err != nil {
		t.Fatal(err)
	}
	if !provider.request.DisablePromptCaching {
		t.Fatal("confidential request should suppress explicit provider caching")
	}

	blocked := &governedProvider{response: &llm.CompletionResponse{Content: "no"}}
	blockedRouter, _ := newGovernedRouter(t, GovernanceConfig{
		AllowedRegions: []string{"us"}, ProviderPolicies: map[string]ProviderPolicy{"paid": {Region: "eu"}},
	}, blocked)
	if _, err := blockedRouter.Complete(context.Background(), "paid", llm.CompletionRequest{Model: "model"}); err == nil {
		t.Fatal("expected regional routing rejection")
	}
	if blocked.calls != 0 {
		t.Fatal("region-blocked request reached provider")
	}
}

func TestInteractiveHighCostCallRequiresExplicitConfirmation(t *testing.T) {
	provider := &governedProvider{response: &llm.CompletionResponse{Content: "ok"}}
	router, store := newGovernedRouter(t, GovernanceConfig{ConfirmationThresholdUSD: 0.001}, provider)
	request := llm.CompletionRequest{Model: "model", MaxTokens: 1000}
	ctx := llm.WithCallMetadata(context.Background(), llm.CallMetadata{Source: "http"})
	_, err := router.Complete(ctx, "paid", request)
	var confirmation *ConfirmationRequiredError
	if !errors.As(err, &confirmation) || provider.calls != 0 {
		t.Fatalf("error=%v provider_calls=%d", err, provider.calls)
	}
	var rejected, code string
	if err := store.db.QueryRow(`SELECT status, error_code FROM token_usage`).Scan(&rejected, &code); err != nil {
		t.Fatal(err)
	}
	if rejected != "rejected" || code != "confirmation_required" {
		t.Fatalf("status/code=%s/%s", rejected, code)
	}
	ctx = llm.WithCallMetadata(context.Background(), llm.CallMetadata{Source: "http", CostConfirmed: true})
	if _, err := router.Complete(ctx, "paid", request); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 {
		t.Fatalf("provider_calls=%d, want 1", provider.calls)
	}
	stats, err := store.StatsSince(context.Background(), time.Time{})
	if err != nil || stats.Calls != 2 || stats.AttributedCalls != 2 || stats.RejectedCalls != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}

func TestGovernorCircuitBreakerStopsSubsequentCalls(t *testing.T) {
	provider := &governedProvider{err: errors.New("upstream unavailable")}
	router, _ := newGovernedRouter(t, GovernanceConfig{
		CircuitFailureThreshold: 1, CircuitCooldown: time.Minute,
	}, provider)
	_, _ = router.Complete(context.Background(), "paid", llm.CompletionRequest{Model: "model"})
	if _, err := router.Complete(context.Background(), "paid", llm.CompletionRequest{Model: "model"}); err == nil {
		t.Fatal("expected open-circuit rejection")
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls)
	}
}

func newGovernedRouter(t *testing.T, cfg GovernanceConfig, provider *governedProvider) (*llm.Router, *Store) {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "costs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	router := llm.NewRouter("paid")
	router.Register(provider)
	router.SetController(NewGovernor(store, PriceTable{"paid/model": {
		InputPerMTok: 1, OutputPerMTok: 2,
	}}, cfg))
	return router, store
}

func TestGovernorMetersEveryRouterCallWithAttribution(t *testing.T) {
	provider := &governedProvider{response: &llm.CompletionResponse{
		Content: "ok", InputTokens: 100, OutputTokens: 25, TotalTokens: 125,
		ProviderRequestID: "provider-123",
	}}
	router, store := newGovernedRouter(t, GovernanceConfig{}, provider)
	ctx := llm.WithCallMetadata(context.Background(), llm.CallMetadata{
		Subject: "user-1", AgentID: "agent-1", SessionID: "session-1", Source: "studio",
	})
	if _, err := router.Complete(ctx, "paid", llm.CompletionRequest{Model: "model", MaxTokens: 50}); err != nil {
		t.Fatal(err)
	}
	var subject, agentID, source, pricingStatus, requestID string
	var total int
	if err := store.db.QueryRow(`SELECT subject, agent_id, source, pricing_status, provider_request_id, total_tokens FROM token_usage`).
		Scan(&subject, &agentID, &source, &pricingStatus, &requestID, &total); err != nil {
		t.Fatal(err)
	}
	if subject != "user-1" || agentID != "agent-1" || source != "studio" {
		t.Fatalf("attribution = %q/%q/%q", subject, agentID, source)
	}
	if pricingStatus != "priced" || requestID != "provider-123" || total != 125 {
		t.Fatalf("usage = pricing %q request %q total %d", pricingStatus, requestID, total)
	}
}

func TestRunTotalsRemainIsolatedAcrossConcurrentStudioBuilds(t *testing.T) {
	provider := &governedProvider{response: &llm.CompletionResponse{Content: "ok", InputTokens: 20, OutputTokens: 10}}
	router, store := newGovernedRouter(t, GovernanceConfig{}, provider)
	for _, runID := range []string{"build-a", "build-b", "build-b"} {
		ctx := llm.WithCallMetadata(context.Background(), llm.CallMetadata{Source: "studio", RunID: runID})
		if _, err := router.Complete(ctx, "paid", llm.CompletionRequest{Model: "model", MaxTokens: 10}); err != nil {
			t.Fatal(err)
		}
	}
	a, err := store.TotalsByRun(context.Background(), "build-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.TotalsByRun(context.Background(), "build-b")
	if err != nil {
		t.Fatal(err)
	}
	if a.TotalTokens != 30 || b.TotalTokens != 60 {
		t.Fatalf("isolated totals: a=%d b=%d", a.TotalTokens, b.TotalTokens)
	}
}

func TestGovernorStreamingFallbackIsRecordedAfterClose(t *testing.T) {
	stream := make(chan string, 2)
	stream <- "hello "
	stream <- "world"
	close(stream)
	provider := &governedProvider{response: &llm.CompletionResponse{Stream: stream}}
	router, store := newGovernedRouter(t, GovernanceConfig{}, provider)
	resp, err := router.Complete(context.Background(), "paid", llm.CompletionRequest{
		Model: "model", Messages: []llm.ChatMessage{{Role: "user", Content: "say hello"}}, Stream: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range resp.Stream {
	}
	deadline := time.Now().Add(time.Second)
	for {
		var calls, tokens int
		if err := store.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(total_tokens), 0) FROM token_usage`).Scan(&calls, &tokens); err != nil {
			t.Fatal(err)
		}
		if calls == 1 {
			if tokens <= 0 {
				t.Fatal("stream was recorded with zero tokens")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stream usage was not recorded")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCancelledPartialStreamIsRecorded(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "costs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	router := llm.NewRouter("paid")
	router.Register(cancellingStreamProvider{})
	router.SetController(NewGovernor(store, PriceTable{"paid/model": {InputPerMTok: 1, OutputPerMTok: 2}}, GovernanceConfig{}))
	ctx, cancel := context.WithCancel(context.Background())
	resp, err := router.Complete(ctx, "paid", llm.CompletionRequest{Model: "model", Stream: true,
		Messages: []llm.ChatMessage{{Role: "user", Content: "stream"}}})
	if err != nil {
		t.Fatal(err)
	}
	if token := <-resp.Stream; token == "" {
		t.Fatal("expected partial token")
	}
	cancel()
	for range resp.Stream {
	}
	deadline := time.Now().Add(time.Second)
	for {
		var calls, output int
		_ = store.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(comp_tokens), 0) FROM token_usage`).Scan(&calls, &output)
		if calls == 1 {
			if output <= 0 {
				t.Fatal("cancelled partial output was recorded as zero")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancelled stream usage not recorded")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestGovernorHardBudgetRejectsBeforeProvider(t *testing.T) {
	provider := &governedProvider{response: &llm.CompletionResponse{Content: "unexpected"}}
	router, _ := newGovernedRouter(t, GovernanceConfig{
		DailyBudgetUSD: 0.000001, EnforcementMode: "hard",
	}, provider)
	_, err := router.Complete(context.Background(), "paid", llm.CompletionRequest{
		Model: "model", Messages: []llm.ChatMessage{{Role: "user", Content: "expensive"}}, MaxTokens: 100,
	})
	if err == nil {
		t.Fatal("expected hard-budget rejection")
	}
	if provider.request.Model != "" {
		t.Fatal("provider was called after budget rejection")
	}
}

func TestGovernorEnforcesPerUserDollarBudget(t *testing.T) {
	provider := &governedProvider{response: &llm.CompletionResponse{Content: "unexpected"}}
	router, _ := newGovernedRouter(t, GovernanceConfig{
		// Leaves enough room for the conservatively counted input plus a
		// reduced output, but not the requested 100-token completion.
		PerUserDailyBudgetUSD: 0.00003, EnforcementMode: "hard",
	}, provider)
	ctx := llm.WithCallMetadata(context.Background(), llm.CallMetadata{Subject: "user-1"})
	_, err := router.Complete(ctx, "paid", llm.CompletionRequest{
		Model: "model", Messages: []llm.ChatMessage{{Role: "user", Content: "expensive"}}, MaxTokens: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.request.MaxTokens >= 100 {
		t.Fatalf("per-user budget did not clamp output: %d", provider.request.MaxTokens)
	}
}

func TestAtomicReservationAllowsOnlyOneConcurrentGovernor(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "costs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prices := PriceTable{"paid/model": {InputPerMTok: 1, OutputPerMTok: 2}}
	cfg := GovernanceConfig{DailyBudgetUSD: 0.00021, EnforcementMode: "hard", MaxConcurrentPerProvider: 10}
	providers := []*governedProvider{{response: &llm.CompletionResponse{Content: "ok"}, delay: 50 * time.Millisecond}, {response: &llm.CompletionResponse{Content: "ok"}, delay: 50 * time.Millisecond}}
	routers := []*llm.Router{llm.NewRouter("paid"), llm.NewRouter("paid")}
	for i := range routers {
		routers[i].Register(providers[i])
		routers[i].SetController(NewGovernor(store, prices, cfg))
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, router := range routers {
		wg.Add(1)
		go func(router *llm.Router) {
			defer wg.Done()
			<-start
			_, err := router.Complete(context.Background(), "paid", llm.CompletionRequest{
				Model: "model", Messages: []llm.ChatMessage{{Role: "user", Content: "x"}}, MaxTokens: 100,
			})
			results <- err
		}(router)
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent admissions = %d, want exactly 1", successes)
	}
}

func TestHierarchicalRejectionNamesScopeAndReset(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "costs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	err = store.TryReserve(context.Background(), "too-large", "user-1", "agent-1", "paid", 101, 1, now.Add(time.Minute), ReservationPolicy{
		Now: now, DailyStart: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC),
		MonthlyStart: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), TokenWindowStart: now.Add(-24 * time.Hour),
		UserDailyMicros: 100,
	})
	var rejected *ReservationRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("error = %v, want ReservationRejectedError", err)
	}
	if rejected.Scope != "user_daily" || rejected.ResetAt.IsZero() {
		t.Fatalf("scope/reset = %q/%v", rejected.Scope, rejected.ResetAt)
	}
}

func TestProviderTokenPerMinuteAdmissionIsAtomic(t *testing.T) {
	provider := &governedProvider{response: &llm.CompletionResponse{Content: "ok", InputTokens: 20, OutputTokens: 50, TotalTokens: 70}}
	router, _ := newGovernedRouter(t, GovernanceConfig{ProviderPolicies: map[string]ProviderPolicy{
		"paid": {MaxTokensPerMinute: 100},
	}}, provider)
	if _, err := router.Complete(context.Background(), "paid", llm.CompletionRequest{Model: "model", MaxTokens: 70}); err != nil {
		t.Fatal(err)
	}
	_, err := router.Complete(context.Background(), "paid", llm.CompletionRequest{Model: "model", MaxTokens: 70,
		Messages: []llm.ChatMessage{{Role: "user", Content: strings.Repeat("x", 200)}}})
	var rejected *ReservationRejectedError
	if !errors.As(err, &rejected) || rejected.Scope != "provider_tokens_1m" {
		t.Fatalf("error=%v rejected=%+v", err, rejected)
	}
}

func TestGovernorRecordsProviderErrorsAndReleasesReservation(t *testing.T) {
	provider := &governedProvider{err: errors.New("upstream unavailable")}
	router, store := newGovernedRouter(t, GovernanceConfig{}, provider)
	if _, err := router.Complete(context.Background(), "paid", llm.CompletionRequest{Model: "model"}); err == nil {
		t.Fatal("expected provider error")
	}
	var status, code string
	if err := store.db.QueryRow(`SELECT status, error_code FROM token_usage`).Scan(&status, &code); err != nil {
		t.Fatal(err)
	}
	if status != "error" || code != "provider_error" {
		t.Fatalf("status/code = %q/%q", status, code)
	}
	var reservations int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM cost_reservations`).Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if reservations != 0 {
		t.Fatalf("reservations = %d, want 0", reservations)
	}
}

func TestProviderReconciliationCalculatesVariance(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "costs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	if err := store.Record(context.Background(), UsageRecord{
		Provider: "paid", Model: "model", CostUSD: 1.25, CostMicros: 1_250_000,
		PricingStatus: "priced", Status: "success", CreatedAt: start.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	item, err := store.ReconcileProvider(context.Background(), "paid", start, start.AddDate(0, 1, 0), 1_500_000, "provider-cost-api")
	if err != nil {
		t.Fatal(err)
	}
	if item.EstimatedMicros != 1_250_000 || item.VarianceMicros != 250_000 {
		t.Fatalf("reconciliation = %+v", item)
	}
	items, err := store.ListReconciliations(context.Background(), 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("list reconciliations = %d, %v", len(items), err)
	}
}

func TestUsageCallIDIsIdempotentAndChargebackIsGrouped(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "costs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record := UsageRecord{CallID: "same-call", Subject: "user-1", Source: "studio", Provider: "paid", Model: "model",
		TotalTokens: 100, CostMicros: 500, CostUSD: 0.0005, AttemptCount: 2, ProviderRequestIDs: []string{"a", "b"}}
	if err := store.Record(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	rows, err := store.Chargeback(context.Background(), time.Time{}, []string{"user", "feature", "provider", "model"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if rows[0].Calls != 1 || rows[0].Attempts != 2 || rows[0].CostMicros != 500 {
		t.Fatalf("chargeback=%+v", rows[0])
	}
	usage, err := store.ListUsage(context.Background(), time.Time{}, 10)
	if err != nil || len(usage) != 1 || strings.Join(usage[0].ProviderRequestIDs, ",") != "a,b" {
		t.Fatalf("usage=%+v err=%v", usage, err)
	}
}
