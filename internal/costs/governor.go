package costs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/quota"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// GovernanceConfig controls process-wide inference admission. Budget periods
// use UTC calendar boundaries so resets are deterministic across instances.
type GovernanceConfig struct {
	DailyBudgetUSD           float64
	MonthlyBudgetUSD         float64
	PerUserDailyBudgetUSD    float64
	PerAgentDailyBudgetUSD   float64
	EnforcementMode          string
	UnknownPricing           string
	DefaultMaxOutput         int
	MaxOutputCeiling         int
	ConfirmationThresholdUSD float64
	ReservationTTL           time.Duration
	PerUserTokensDay         int
	// PerUserTokensWorkspaceID scopes the flat per-user token allowance to one
	// workspace. Public demo deployments use it so the demo safety ceiling does
	// not throttle authenticated customers in ordinary workspaces.
	PerUserTokensWorkspaceID string
	PerAgentTokensDay        int
	AllowedProviders         []string
	AllowedModels            []string
	AllowedRegions           []string
	MaxConcurrentPerProvider int
	CircuitFailureThreshold  int
	CircuitCooldown          time.Duration
	ProviderPolicies         map[string]ProviderPolicy
}

type ProviderPolicy struct {
	AllowedDataClasses      []string
	CacheAllowedDataClasses []string
	MaxTokensPerMinute      int
	Region                  string
	Retention               string
	PromptCaching           bool
}

// ConfirmationRequiredError is returned before provider access when an
// interactive call exceeds the configured estimate threshold.
type ConfirmationRequiredError struct {
	Provider        string  `json:"provider"`
	Model           string  `json:"model"`
	EstimatedUSD    float64 `json:"estimated_usd"`
	EstimatedTokens int     `json:"estimated_tokens"`
	ThresholdUSD    float64 `json:"threshold_usd"`
}

func (e *ConfirmationRequiredError) Error() string {
	return fmt.Sprintf("llm cost confirmation required: estimated $%.6f for %s/%s exceeds $%.6f threshold", e.EstimatedUSD, e.Provider, e.Model, e.ThresholdUSD)
}

// Governor implements llm.Controller with durable usage and in-flight
// reservations. Its mutex makes check+reserve atomic in this process; the
// durable reservation rows retain in-flight visibility across restarts.
type Governor struct {
	store  *Store
	prices PriceTable
	// cfg is an atomic snapshot, not a plain struct, because config reload
	// republishes it while calls are in flight (SetGovernance). Every method
	// takes ONE snapshot and uses it throughout: reading g.cfg twice inside
	// Before could otherwise apply the old ceiling and the new allow-list to
	// the same request, which is a decision that was never configured.
	cfg atomic.Pointer[GovernanceConfig]
	mu  sync.Mutex
	// providerShares admits in-flight calls per provider under max-min
	// fairness by workspace (MU-024 criterion 4); providerReleases holds the
	// release functions handed back, so releaseProvider returns exactly as
	// many slots as acquireProvider took.
	// quotaPolicy, when set, supplies per-subject limit VALUES resolved across
	// six levels (MU-024 criterion 1). nil keeps the flat-config behaviour.
	quotaMu     sync.RWMutex
	quotaPolicy *quota.Policy
	// workspaceUserTokenLimits are administrator-managed rolling 24-hour
	// allowances for individual workspaces.
	workspaceUserTokenLimits map[string]int64

	providerShares   map[string]*quota.FairShare
	providerReleases map[string][]func()
	providerFailures map[string]int
	circuitUntil     map[string]time.Time
}

// normalizeGovernance fills in the defaults a zero-valued section implies.
// Shared by construction and by reload, so a hot-applied config cannot end up
// with a zero output ceiling that boot would have filled in.
func normalizeGovernance(cfg GovernanceConfig) GovernanceConfig {
	if cfg.DefaultMaxOutput <= 0 {
		cfg.DefaultMaxOutput = 4096
	}
	if cfg.MaxOutputCeiling <= 0 {
		cfg.MaxOutputCeiling = 32768
	}
	if cfg.ReservationTTL <= 0 {
		cfg.ReservationTTL = 15 * time.Minute
	}
	if strings.TrimSpace(cfg.EnforcementMode) == "" {
		cfg.EnforcementMode = "soft"
	}
	if strings.TrimSpace(cfg.UnknownPricing) == "" {
		cfg.UnknownPricing = "allow"
	}
	if cfg.CircuitFailureThreshold <= 0 {
		cfg.CircuitFailureThreshold = 5
	}
	if cfg.CircuitCooldown <= 0 {
		cfg.CircuitCooldown = 30 * time.Second
	}
	return cfg
}

func NewGovernor(store *Store, prices PriceTable, cfg GovernanceConfig) *Governor {
	g := &Governor{store: store, prices: prices,
		providerShares:   make(map[string]*quota.FairShare),
		providerReleases: make(map[string][]func()),
		providerFailures: make(map[string]int), circuitUntil: make(map[string]time.Time),
		workspaceUserTokenLimits: make(map[string]int64)}
	g.SetGovernance(cfg)
	return g
}

// governance returns the current admission settings. Callers take one snapshot
// and use it for the whole operation.
func (g *Governor) governance() GovernanceConfig {
	if g == nil {
		return GovernanceConfig{}
	}
	if c := g.cfg.Load(); c != nil {
		return *c
	}
	return GovernanceConfig{}
}

// SetGovernance republishes the admission settings from a config reload.
//
// WHY THIS EXISTS. costs and llm are both classified hot-reloadable, and the
// budgets, the enforcement mode and the quota policy genuinely were. The
// PROVIDER-level half was not: allowed_providers, allowed_models,
// allowed_regions and every per-provider data-class, retention and rate policy
// were copied into this struct once at boot and never read again. An operator
// who removed a provider from allowed_providers, or tightened a data class,
// watched the file save and the setting take no effect — with no restart
// notice, because the section was advertised as live. A half-applied security
// control is worse than one that says it needs a restart.
func (g *Governor) SetGovernance(next GovernanceConfig) {
	if g == nil {
		return
	}
	next = normalizeGovernance(next)
	previous := g.governance()
	g.cfg.Store(&next)
	if previous.MaxConcurrentPerProvider == next.MaxConcurrentPerProvider {
		return
	}
	// A changed concurrency ceiling needs new schedulers: quota.FairShare
	// fixes its limit at construction. Calls already holding a slot keep the
	// release closure for the OLD scheduler, so they release correctly and
	// simply stop counting against the new one — a brief over-admission
	// bounded by the number of calls in flight at the moment of the change.
	// The alternative, blocking the reload until every in-flight call drains,
	// makes a config save hang for as long as the slowest model takes.
	g.mu.Lock()
	g.providerShares = make(map[string]*quota.FairShare, len(g.providerShares))
	g.mu.Unlock()
}

func (g *Governor) Before(ctx context.Context, provider string, req *llm.CompletionRequest) (context.Context, llm.Reservation, error) {
	if g == nil || g.store == nil {
		return ctx, llm.Reservation{}, nil
	}
	// One snapshot for the whole admission decision.
	gcfg := g.governance()
	if req.MaxTokens <= 0 && req.Operation != "embedding" {
		req.MaxTokens = gcfg.DefaultMaxOutput
	}
	if req.MaxTokens > gcfg.MaxOutputCeiling {
		req.MaxTokens = gcfg.MaxOutputCeiling
	}
	if !allowedValue(gcfg.AllowedProviders, provider) {
		return ctx, llm.Reservation{}, fmt.Errorf("llm cost control: provider %q is not globally allowed", provider)
	}
	if !allowedValue(gcfg.AllowedModels, req.Model) {
		return ctx, llm.Reservation{}, fmt.Errorf("llm cost control: model %q is not globally allowed", req.Model)
	}
	metadata := llm.CallMetadataFromContext(ctx)
	classification := strings.TrimSpace(metadata.DataClassification)
	if classification == "" {
		classification = "unclassified"
	}
	providerTokenLimit := 0
	if policy, ok := gcfg.ProviderPolicies[provider]; ok {
		providerTokenLimit = policy.MaxTokensPerMinute
		if policy.AllowedDataClasses != nil && !allowedValue(policy.AllowedDataClasses, classification) {
			return ctx, llm.Reservation{}, fmt.Errorf("llm data policy: provider %q does not allow %q data", provider, classification)
		}
		if gcfg.AllowedRegions != nil && !allowedValue(gcfg.AllowedRegions, policy.Region) {
			return ctx, llm.Reservation{}, fmt.Errorf("llm data policy: provider %q region %q is not allowed", provider, policy.Region)
		}
		if policy.PromptCaching && !allowedValueNonNil(policy.CacheAllowedDataClasses, classification) {
			req.DisablePromptCaching = true
		}
	}
	inputTokens := llm.EstimateRequestTokens(*req)
	estimatedTokens := inputTokens + req.MaxTokens
	estimatedUSD, estimatedMicros, pricingStatus := EstimateDetailed(g.prices, provider, req.Model, UsageDimensions{
		InputTokens: inputTokens, OutputTokens: req.MaxTokens,
	})
	if pricingStatus == "unknown" && strings.EqualFold(gcfg.UnknownPricing, "block") {
		return ctx, llm.Reservation{}, fmt.Errorf("llm cost control: pricing is unknown for %s/%s", provider, req.Model)
	}
	if gcfg.ConfirmationThresholdUSD > 0 && estimatedUSD > gcfg.ConfirmationThresholdUSD &&
		isInteractiveSource(metadata.Source) && !metadata.CostConfirmed {
		return ctx, llm.Reservation{}, &ConfirmationRequiredError{
			Provider: provider, Model: req.Model, EstimatedUSD: estimatedUSD,
			EstimatedTokens: estimatedTokens, ThresholdUSD: gcfg.ConfirmationThresholdUSD,
		}
	}
	if err := g.acquireProvider(ctx, provider, wsroot.Normalize(metadata.Workspace)); err != nil {
		return ctx, llm.Reservation{}, err
	}
	admitted := false
	defer func() {
		if !admitted {
			g.releaseProvider(provider)
		}
	}()

	now := time.Now().UTC()
	hard := strings.EqualFold(gcfg.EnforcementMode, "hard")
	id := newReservationID()
	// A call arriving without a workspace is a single-tenant call — the
	// scheduler, a channel, or Personal itself — so it resolves to the
	// personal workspace, the same rule every other store applies. Failing
	// closed here would block every LLM call on any path that has not yet
	// established a principal, which is a much worse outcome than charging
	// single-tenant spend to the single tenant that exists.
	workspace := wsroot.Normalize(metadata.Workspace)
	if !hard && providerTokenLimit <= 0 {
		if err := g.store.Reserve(ctx, workspace, id, metadata.Subject, metadata.AgentID, provider, estimatedMicros, estimatedTokens, now.Add(gcfg.ReservationTTL)); err != nil {
			return ctx, llm.Reservation{}, fmt.Errorf("llm cost control: reserve: %w", err)
		}
	} else {
		policy := ReservationPolicy{
			Now: now, DailyStart: time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC),
			MonthlyStart:             time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC),
			TokenWindowStart:         now.Add(-24 * time.Hour),
			ProviderTokenWindowStart: now.Add(-time.Minute),
			ProviderTokenLimit:       int64(providerTokenLimit),
		}
		if hard {
			policy.GlobalDailyMicros = dollarsToMicros(gcfg.DailyBudgetUSD)
			policy.GlobalMonthlyMicros = dollarsToMicros(gcfg.MonthlyBudgetUSD)
			policy.UserDailyMicros = dollarsToMicros(gcfg.PerUserDailyBudgetUSD)
			policy.AgentDailyMicros = dollarsToMicros(gcfg.PerAgentDailyBudgetUSD)
			policy.UserTokenLimit = configuredPerUserTokenLimit(gcfg, workspace)
			policy.AgentTokenLimit = int64(gcfg.PerAgentTokensDay)
		}
		policy.UserTokenLimit = tighten(policy.UserTokenLimit, g.workspaceUserTokenLimit(workspace))
		// Multi-level limits tighten the flat config; they never loosen it.
		// See quotapolicy.go for why replacing would invert the precedence.
		policy = g.applyQuotaPolicy(policy, quotaSubject{
			OrganizationID: metadata.Organization, WorkspaceID: workspace,
			Subject: metadata.Subject, AgentID: metadata.AgentID,
			Provider: provider, Model: req.Model,
		})
		for attempt := 0; attempt < 2; attempt++ {
			err := g.store.TryReserve(ctx, workspace, id, metadata.Subject, metadata.AgentID, provider, estimatedMicros, estimatedTokens, now.Add(gcfg.ReservationTTL), policy)
			if err == nil {
				break
			}
			var rejected *ReservationRejectedError
			if !errors.As(err, &rejected) || attempt > 0 || req.Operation == "embedding" {
				return ctx, llm.Reservation{}, fmt.Errorf("llm cost control: reserve: %w", err)
			}
			maxOutput := req.MaxTokens
			if rejected.Capacity.AvailableTokens != int64(^uint64(0)>>1) {
				maxOutput = min(maxOutput, int(rejected.Capacity.AvailableTokens)-inputTokens)
			}
			if rejected.Capacity.AvailableMicros != int64(^uint64(0)>>1) {
				_, inputMicros, _ := EstimateDetailed(g.prices, provider, req.Model, UsageDimensions{InputTokens: inputTokens})
				if affordable, ok := MaxAffordableOutput(g.prices, provider, req.Model, rejected.Capacity.AvailableMicros-inputMicros); ok {
					maxOutput = min(maxOutput, affordable)
				} else {
					maxOutput = 0
				}
			}
			if maxOutput <= 0 || maxOutput >= req.MaxTokens {
				return ctx, llm.Reservation{}, fmt.Errorf("llm cost control: %w", err)
			}
			req.MaxTokens = maxOutput
			estimatedTokens = inputTokens + maxOutput
			_, estimatedMicros, _ = EstimateDetailed(g.prices, provider, req.Model, UsageDimensions{InputTokens: inputTokens, OutputTokens: maxOutput})
		}
	}
	admitted = true
	return ctx, llm.Reservation{ID: id}, nil
}

func configuredPerUserTokenLimit(cfg GovernanceConfig, workspaceID string) int64 {
	scope := strings.TrimSpace(cfg.PerUserTokensWorkspaceID)
	if scope != "" {
		scope = wsroot.Normalize(scope)
	}
	if scope != "" && scope != wsroot.Normalize(workspaceID) {
		return 0
	}
	return int64(cfg.PerUserTokensDay)
}

func isInteractiveSource(source string) bool {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "http", "studio", "builder", "playground":
		return true
	default:
		return false
	}
}

func dollarsToMicros(value float64) int64 {
	if value <= 0 {
		return 0
	}
	return int64(value*1_000_000 + 0.5)
}

func (g *Governor) After(ctx context.Context, reservation llm.Reservation, provider string, req llm.CompletionRequest, resp *llm.CompletionResponse, callErr error) {
	if g == nil || g.store == nil {
		return
	}
	defer g.releaseProvider(provider)
	gcfg := g.governance()
	g.mu.Lock()
	defer g.mu.Unlock()
	if callErr == nil {
		g.providerFailures[provider] = 0
		delete(g.circuitUntil, provider)
	} else {
		g.providerFailures[provider]++
		if g.providerFailures[provider] >= gcfg.CircuitFailureThreshold {
			g.circuitUntil[provider] = time.Now().UTC().Add(gcfg.CircuitCooldown)
		}
	}
	metadata := llm.CallMetadataFromContext(ctx)
	// Released in the same workspace it was reserved in — read from the call's
	// own metadata, so a release cannot free another tenant's headroom.
	if reservation.ID != "" {
		_ = g.store.Release(ctx, wsroot.Normalize(metadata.Workspace), reservation.ID)
	}
	record := UsageRecord{
		Subject: metadata.Subject, Workspace: wsroot.Normalize(metadata.Workspace),
		AgentID: metadata.AgentID, SessionID: metadata.SessionID,
		RunID: metadata.RunID, CallID: metadata.CallID,
		Source: metadata.Source, Trigger: metadata.Trigger,
		Provider: provider, Model: req.Model, Status: "success",
		CreatedAt: time.Now().UTC(),
	}
	if record.CallID == "" {
		record.CallID = reservation.ID
	}
	if callErr != nil {
		record.Status = "error"
		record.ErrorCode = classifyCallError(callErr)
	}
	if resp != nil {
		record.PromptTokens = resp.InputTokens
		record.CompTokens = resp.OutputTokens
		record.CacheCreationTokens = resp.CacheCreationTokens
		record.CacheReadTokens = resp.CacheReadTokens
		record.ReasoningTokens = resp.ReasoningTokens
		record.ToolUsePromptTokens = resp.ToolUsePromptTokens
		record.TotalTokens = resp.TotalTokens
		if record.TotalTokens == 0 {
			record.TotalTokens = resp.InputTokens + resp.OutputTokens + resp.ReasoningTokens + resp.ToolUsePromptTokens
		}
		record.ProviderRequestID = resp.ProviderRequestID
		record.ProviderRequestIDs = append([]string(nil), resp.ProviderRequestIDs...)
		record.AttemptCount = resp.AttemptCount
	}
	record.CostUSD, record.CostMicros, record.PricingStatus = EstimateDetailed(g.prices, provider, req.Model, UsageDimensions{
		InputTokens: record.PromptTokens, OutputTokens: record.CompTokens,
		CacheCreationTokens: record.CacheCreationTokens, CacheReadTokens: record.CacheReadTokens,
		ReasoningTokens: record.ReasoningTokens, ToolUsePromptTokens: record.ToolUsePromptTokens,
	})
	record.PricingVersion = PricingVersion(g.prices, provider, req.Model)
	_ = g.store.Record(ctx, record)
}

func (g *Governor) Rejected(ctx context.Context, provider string, req llm.CompletionRequest, rejection error) {
	if g == nil || g.store == nil {
		return
	}
	metadata := llm.CallMetadataFromContext(ctx)
	callID := metadata.CallID
	if callID == "" {
		callID = newReservationID()
	}
	_, _, pricingStatus := EstimateDetailed(g.prices, provider, req.Model, UsageDimensions{})
	_ = g.store.Record(ctx, UsageRecord{
		Subject: metadata.Subject, Workspace: wsroot.Normalize(metadata.Workspace), AgentID: metadata.AgentID,
		SessionID: metadata.SessionID, RunID: metadata.RunID, CallID: callID,
		Source: metadata.Source, Trigger: metadata.Trigger, Provider: provider, Model: req.Model,
		PricingStatus: pricingStatus, PricingVersion: PricingVersion(g.prices, provider, req.Model),
		Status: "rejected", ErrorCode: classifyAdmissionError(rejection), CreatedAt: time.Now().UTC(),
	})
}

func classifyAdmissionError(err error) string {
	var confirmation *ConfirmationRequiredError
	var budget *ReservationRejectedError
	switch {
	case errors.As(err, &confirmation):
		return "confirmation_required"
	case errors.As(err, &budget):
		if budget.Scope == "provider_tokens_1m" {
			return "provider_tpm_exceeded"
		}
		return "budget_rejected"
	case strings.Contains(err.Error(), "circuit is open"):
		return "circuit_open"
	case strings.Contains(err.Error(), "not globally allowed"), strings.Contains(err.Error(), "data policy"):
		return "policy_rejected"
	default:
		return "admission_rejected"
	}
}

func newReservationID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err == nil {
		return hex.EncodeToString(buf[:])
	}
	return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
}

func classifyCallError(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	default:
		return "provider_error"
	}
}

func allowedValue(allowlist []string, value string) bool {
	if allowlist == nil {
		return true
	}
	for _, allowed := range allowlist {
		if strings.EqualFold(strings.TrimSpace(allowed), strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

func allowedValueNonNil(allowlist []string, value string) bool {
	return allowlist != nil && allowedValue(allowlist, value)
}

// acquireProvider admits one in-flight call to a provider.
//
// MU-024 criterion 4: the slot is taken through a fair-share scheduler keyed
// by workspace, not a plain semaphore. A semaphore is first-come, and
// first-come is not a scheduling policy so much as the absence of one: a
// tenant with a hundred queued runs takes every slot and holds it while
// everyone behind waits — without exceeding a single budget, because it is not
// spending faster than allowed, only first. Budgets bound how much; they say
// nothing about who goes next.
func (g *Governor) acquireProvider(ctx context.Context, provider, workspaceID string) error {
	maxConcurrent := g.governance().MaxConcurrentPerProvider
	if maxConcurrent <= 0 {
		g.mu.Lock()
		until := g.circuitUntil[provider]
		g.mu.Unlock()
		if time.Now().UTC().Before(until) {
			return fmt.Errorf("llm cost control: provider %q circuit is open until %s", provider, until.Format(time.RFC3339))
		}
		return nil
	}
	g.mu.Lock()
	until := g.circuitUntil[provider]
	if time.Now().UTC().Before(until) {
		g.mu.Unlock()
		return fmt.Errorf("llm cost control: provider %q circuit is open until %s", provider, until.Format(time.RFC3339))
	}
	share := g.providerShares[provider]
	if share == nil {
		share = quota.NewFairShare(maxConcurrent)
		g.providerShares[provider] = share
	}
	g.mu.Unlock()

	release, err := share.Acquire(ctx, wsroot.Normalize(workspaceID))
	if err != nil {
		return fmt.Errorf("llm cost control: waiting for provider %q capacity: %w", provider, err)
	}
	g.mu.Lock()
	g.providerReleases[provider] = append(g.providerReleases[provider], release)
	g.mu.Unlock()
	return nil
}

func (g *Governor) releaseProvider(provider string) {
	// Deliberately NOT gated on MaxConcurrentPerProvider. It used to be, and
	// that read the CURRENT setting to decide whether a slot taken under the
	// OLD one should be given back: turning concurrency limiting off while
	// calls were in flight leaked every outstanding slot, and turning it on
	// tried to release slots that were never taken. The pending-release list
	// is the only thing that knows, so it is the only thing consulted.
	// LIFO over the pending releases for this provider. Which specific release
	// runs does not matter — every holder of a slot released one — but the
	// count must match exactly, or the scheduler either leaks capacity or
	// hands out more than it has.
	g.mu.Lock()
	pending := g.providerReleases[provider]
	var release func()
	if n := len(pending); n > 0 {
		release = pending[n-1]
		g.providerReleases[provider] = pending[:n-1]
	}
	g.mu.Unlock()
	if release != nil {
		release()
	}
}

var _ llm.Controller = (*Governor)(nil)
var _ llm.RejectionRecorder = (*Governor)(nil)
