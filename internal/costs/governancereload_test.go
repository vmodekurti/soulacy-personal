package costs

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/llm"
)

func governorForReload(t *testing.T, cfg GovernanceConfig) (*Governor, *governedProvider, *llm.Router) {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "costs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	provider := &governedProvider{response: &llm.CompletionResponse{Content: "ok"}}
	router := llm.NewRouter("paid")
	router.Register(provider)
	g := NewGovernor(store, PriceTable{"paid/model": {InputPerMTok: 1, OutputPerMTok: 2}}, cfg)
	router.SetController(g)
	return g, provider, router
}

// The bug: allowed_providers is an access control that the operator can edit,
// that the settings page confirms, and that the running process ignored until
// the next restart.
func TestRevokingAProviderTakesEffectWithoutARestart(t *testing.T) {
	g, _, router := governorForReload(t, GovernanceConfig{AllowedProviders: []string{"paid"}})
	if _, err := router.Complete(context.Background(), "paid", llm.CompletionRequest{Model: "model"}); err != nil {
		t.Fatalf("provider should be allowed before the change: %v", err)
	}

	g.SetGovernance(GovernanceConfig{AllowedProviders: []string{"other"}})

	if _, err := router.Complete(context.Background(), "paid", llm.CompletionRequest{Model: "model"}); err == nil {
		t.Fatal("a provider removed from allowed_providers is still callable; the allow-list was a " +
			"boot snapshot and the operator was told the section was live")
	}
}

// The per-provider half of the same bug: region, retention, data classes and
// tokens-per-minute all lived in the same boot snapshot.
func TestTighteningAProviderPolicyTakesEffectWithoutARestart(t *testing.T) {
	g, _, router := governorForReload(t, GovernanceConfig{
		AllowedRegions:   []string{"us"},
		ProviderPolicies: map[string]ProviderPolicy{"paid": {Region: "us"}},
	})
	if _, err := router.Complete(context.Background(), "paid", llm.CompletionRequest{Model: "model"}); err != nil {
		t.Fatalf("us region should be allowed before the change: %v", err)
	}

	g.SetGovernance(GovernanceConfig{
		AllowedRegions:   []string{"us"},
		ProviderPolicies: map[string]ProviderPolicy{"paid": {Region: "eu"}},
	})

	if _, err := router.Complete(context.Background(), "paid", llm.CompletionRequest{Model: "model"}); err == nil {
		t.Fatal("a provider moved to a disallowed region is still callable")
	}
}

// A reload arrives on the file watcher's goroutine while requests are in
// flight. Under -race this fails on any build that reads the settings without
// publishing them atomically.
func TestGovernanceCanBeRepublishedWhileCallsAreInFlight(t *testing.T) {
	g, _, router := governorForReload(t, GovernanceConfig{AllowedProviders: []string{"paid"}})
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			g.SetGovernance(GovernanceConfig{AllowedProviders: []string{"paid"}, MaxOutputCeiling: 128})
		}
	}()
	for i := 0; i < 200; i++ {
		if _, err := router.Complete(context.Background(), "paid", llm.CompletionRequest{Model: "model"}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	close(stop)
	wg.Wait()
}

// Boot and reload must produce the same settings from the same file, which is
// only guaranteed while they share one builder.
func TestBootAndReloadDeriveGovernanceIdentically(t *testing.T) {
	cfg := &config.Config{}
	cfg.Costs.DailyBudgetUSD = 12
	cfg.Costs.EnforcementMode = "hard"
	cfg.Costs.ReservationTTL = "30m"
	cfg.LLM.AllowedProviders = []string{"anthropic"}
	cfg.LLM.Providers = map[string]config.ProviderConfig{
		"anthropic": {Region: "eu", Retention: "none", MaxTokensPerMinute: 100},
	}

	got := GovernanceFrom(cfg)
	if got.DailyBudgetUSD != 12 || got.EnforcementMode != "hard" {
		t.Fatalf("budget/mode not carried: %+v", got)
	}
	if len(got.AllowedProviders) != 1 || got.AllowedProviders[0] != "anthropic" {
		t.Fatalf("allowed providers not carried: %+v", got.AllowedProviders)
	}
	policy, ok := got.ProviderPolicies["anthropic"]
	if !ok || policy.Region != "eu" || policy.Retention != "none" || policy.MaxTokensPerMinute != 100 {
		t.Fatalf("per-provider policy not carried: %+v", got.ProviderPolicies)
	}
	if got.ReservationTTL.String() != "30m0s" {
		t.Fatalf("reservation ttl = %s", got.ReservationTTL)
	}
}

// releaseProvider used to consult the CURRENT MaxConcurrentPerProvider to
// decide whether a slot taken under the OLD one should be handed back. Turning
// the limit off while calls were in flight therefore leaked every outstanding
// slot: turn it back on and the provider is permanently at capacity, with no
// error anywhere and no way to recover short of a restart.
func TestSlotsTakenUnderAnOldLimitAreStillReleased(t *testing.T) {
	g, _, _ := governorForReload(t, GovernanceConfig{MaxConcurrentPerProvider: 1})
	ctx := context.Background()

	if err := g.acquireProvider(ctx, "paid", "ws_a"); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	// The operator turns concurrency limiting off while that call is running.
	g.SetGovernance(GovernanceConfig{MaxConcurrentPerProvider: 0})
	g.releaseProvider("paid")

	g.mu.Lock()
	pending := len(g.providerReleases["paid"])
	g.mu.Unlock()
	if pending != 0 {
		t.Fatalf("%d slot(s) left held after release; the provider stays at capacity forever once "+
			"the limit is turned back on", pending)
	}

	// And the capacity is genuinely usable again.
	g.SetGovernance(GovernanceConfig{MaxConcurrentPerProvider: 1})
	if err := g.acquireProvider(ctx, "paid", "ws_b"); err != nil {
		t.Fatalf("acquire after the round trip: %v", err)
	}
	g.releaseProvider("paid")
}
