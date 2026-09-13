package costs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/soulacy/soulacy/internal/llm"
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
	store            *Store
	prices           PriceTable
	cfg              GovernanceConfig
	mu               sync.Mutex
	providerSlots    map[string]chan struct{}
	providerFailures map[string]int
	circuitUntil     map[string]time.Time
}

func (g *Governor) SupportsRunCostBudgets() bool { return g != nil && g.store != nil }

func NewGovernor(store *Store, prices PriceTable, cfg GovernanceConfig) *Governor {
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
	return &Governor{store: store, prices: prices, cfg: cfg,
		providerSlots:    make(map[string]chan struct{}),
		providerFailures: make(map[string]int), circuitUntil: make(map[string]time.Time)}
}

func (g *Governor) Before(ctx context.Context, provider string, req *llm.CompletionRequest) (context.Context, llm.Reservation, error) {
	if g == nil || g.store == nil {
		return ctx, llm.Reservation{}, nil
	}
	if req.MaxTokens <= 0 && req.Operation != "embedding" {
		req.MaxTokens = g.cfg.DefaultMaxOutput
	}
	if req.MaxTokens > g.cfg.MaxOutputCeiling {
		req.MaxTokens = g.cfg.MaxOutputCeiling
	}
	if !allowedValue(g.cfg.AllowedProviders, provider) {
		return ctx, llm.Reservation{}, fmt.Errorf("llm cost control: provider %q is not globally allowed", provider)
	}
	if !allowedValue(g.cfg.AllowedModels, req.Model) {
		return ctx, llm.Reservation{}, fmt.Errorf("llm cost control: model %q is not globally allowed", req.Model)
	}
	metadata := llm.CallMetadataFromContext(ctx)
	classification := strings.TrimSpace(metadata.DataClassification)
	if classification == "" {
		classification = "unclassified"
	}
	providerTokenLimit := 0
	if policy, ok := g.cfg.ProviderPolicies[provider]; ok {
		providerTokenLimit = policy.MaxTokensPerMinute
		if policy.AllowedDataClasses != nil && !allowedValue(policy.AllowedDataClasses, classification) {
			return ctx, llm.Reservation{}, fmt.Errorf("llm data policy: provider %q does not allow %q data", provider, classification)
		}
		if g.cfg.AllowedRegions != nil && !allowedValue(g.cfg.AllowedRegions, policy.Region) {
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
	if pricingStatus == "unknown" && (llm.HasRunCostBudget(ctx) || strings.EqualFold(g.cfg.UnknownPricing, "block")) {
		return ctx, llm.Reservation{}, fmt.Errorf("llm cost control: pricing is unknown for %s/%s", provider, req.Model)
	}
	if g.cfg.ConfirmationThresholdUSD > 0 && estimatedUSD > g.cfg.ConfirmationThresholdUSD &&
		isInteractiveSource(metadata.Source) && !metadata.CostConfirmed {
		return ctx, llm.Reservation{}, &ConfirmationRequiredError{
			Provider: provider, Model: req.Model, EstimatedUSD: estimatedUSD,
			EstimatedTokens: estimatedTokens, ThresholdUSD: g.cfg.ConfirmationThresholdUSD,
		}
	}
	if err := g.acquireProvider(ctx, provider); err != nil {
		return ctx, llm.Reservation{}, err
	}
	admitted := false
	defer func() {
		if !admitted {
			g.releaseProvider(provider)
		}
	}()

	now := time.Now().UTC()
	hard := strings.EqualFold(g.cfg.EnforcementMode, "hard")
	id := newReservationID()
	if !hard && providerTokenLimit <= 0 {
		if err := g.store.Reserve(ctx, id, metadata.Subject, metadata.AgentID, provider, estimatedMicros, estimatedTokens, now.Add(g.cfg.ReservationTTL)); err != nil {
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
			policy.GlobalDailyMicros = dollarsToMicros(g.cfg.DailyBudgetUSD)
			policy.GlobalMonthlyMicros = dollarsToMicros(g.cfg.MonthlyBudgetUSD)
			policy.UserDailyMicros = dollarsToMicros(g.cfg.PerUserDailyBudgetUSD)
			policy.AgentDailyMicros = dollarsToMicros(g.cfg.PerAgentDailyBudgetUSD)
			policy.UserTokenLimit = int64(g.cfg.PerUserTokensDay)
			policy.AgentTokenLimit = int64(g.cfg.PerAgentTokensDay)
		}
		for attempt := 0; attempt < 2; attempt++ {
			err := g.store.TryReserve(ctx, id, metadata.Subject, metadata.AgentID, provider, estimatedMicros, estimatedTokens, now.Add(g.cfg.ReservationTTL), policy)
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
	if err := llm.ReserveRunCost(ctx, id, estimatedMicros); err != nil {
		_ = g.store.Release(context.WithoutCancel(ctx), id)
		return ctx, llm.Reservation{}, err
	}
	admitted = true
	return ctx, llm.Reservation{ID: id}, nil
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
	g.mu.Lock()
	defer g.mu.Unlock()
	if callErr == nil {
		g.providerFailures[provider] = 0
		delete(g.circuitUntil, provider)
	} else {
		g.providerFailures[provider]++
		if g.providerFailures[provider] >= g.cfg.CircuitFailureThreshold {
			g.circuitUntil[provider] = time.Now().UTC().Add(g.cfg.CircuitCooldown)
		}
	}
	if reservation.ID != "" {
		_ = g.store.Release(ctx, reservation.ID)
	}
	metadata := llm.CallMetadataFromContext(ctx)
	record := UsageRecord{
		Subject: metadata.Subject, Workspace: metadata.Workspace,
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
	llm.SettleRunCost(ctx, reservation.ID, record.CostMicros, callErr == nil && resp != nil && record.PricingStatus != "unknown")
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
		Subject: metadata.Subject, Workspace: metadata.Workspace, AgentID: metadata.AgentID,
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

func (g *Governor) acquireProvider(ctx context.Context, provider string) error {
	if g.cfg.MaxConcurrentPerProvider <= 0 {
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
	slot := g.providerSlots[provider]
	if slot == nil {
		slot = make(chan struct{}, g.cfg.MaxConcurrentPerProvider)
		g.providerSlots[provider] = slot
	}
	g.mu.Unlock()
	select {
	case slot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("llm cost control: waiting for provider %q capacity: %w", provider, ctx.Err())
	}
}

func (g *Governor) releaseProvider(provider string) {
	if g.cfg.MaxConcurrentPerProvider <= 0 {
		return
	}
	g.mu.Lock()
	slot := g.providerSlots[provider]
	g.mu.Unlock()
	if slot != nil {
		select {
		case <-slot:
		default:
		}
	}
}

var _ llm.Controller = (*Governor)(nil)
var _ llm.RejectionRecorder = (*Governor)(nil)
