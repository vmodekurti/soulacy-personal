package llm

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	sdkllm "github.com/soulacy/soulacy/sdk/llm"
)

type ModelProfile = sdkllm.ModelProfile
type ModelProfiler = sdkllm.ModelProfiler
type Support = sdkllm.Support

const (
	SupportUnknown = sdkllm.SupportUnknown
	SupportYes     = sdkllm.SupportYes
	SupportNo      = sdkllm.SupportNo
)

const modelProfileTimeout = 2 * time.Second
const modelProfileTTL = 5 * time.Minute
const maxModelProfiles = 128

type profileKey struct {
	provider, model string
	generation      uint64
}
type profileEntry struct {
	profile ModelProfile
	done    chan struct{}
}
type profileCache struct {
	mu      sync.Mutex
	entries map[profileKey]*profileEntry
}

func UnknownModelProfile(provider, model string) ModelProfile {
	return ModelProfile{Provider: provider, Model: model, Source: "unknown", ContextSource: "unknown", Chat: SupportUnknown, NativeTools: SupportUnknown, JSONMode: SupportUnknown, Reasoning: SupportUnknown, Vision: SupportUnknown}
}

func validModelIdentity(s string) bool {
	if len(s) > 256 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

// ResolveModel freezes the concrete provider/default model without listing
// models or selecting a different one. Dynamic third-party defaults stay empty.
func (r *Router) ResolveModel(provider, model string) (string, string, error) {
	_, key, _, err := r.modelSnapshot(provider, model)
	return key.provider, key.model, err
}

// The adapter, default and generation form one snapshot. Hot replacement must
// not pair metadata from one adapter with an inference call to another.
func (r *Router) modelSnapshot(provider, model string) (Provider, profileKey, Controller, error) {
	r.mu.RLock()
	if provider == "" {
		provider = r.defaultID
	}
	p, generation, controller := r.providers[provider], r.profileGeneration, r.controller
	r.mu.RUnlock()
	key := profileKey{provider, model, generation}
	if !validModelIdentity(provider) || !validModelIdentity(model) {
		return nil, key, controller, fmt.Errorf("invalid provider/model identity")
	}
	if p == nil {
		return nil, key, controller, fmt.Errorf("llm: unknown provider %q", provider)
	}
	if model == "" {
		if defaults, ok := p.(sdkllm.DefaultModelProvider); ok {
			model = defaults.DefaultModel()
		}
	}
	if !validModelIdentity(model) {
		return nil, key, controller, fmt.Errorf("invalid default model identity")
	}
	key.model = model
	return p, key, controller, nil
}

// DescribeModel caches bounded metadata, including short negative caching. A
// provider re-registration uses a new generation; an old in-flight response
// cannot overwrite the replacement's profile. Waiters honor cancellation and
// no detached background request outlives the caller's bounded lookup.
func (r *Router) DescribeModel(ctx context.Context, provider, model string) (ModelProfile, error) {
	p, key, _, err := r.modelSnapshot(provider, model)
	if err != nil {
		return UnknownModelProfile(provider, model), err
	}
	return r.describeSnapshot(ctx, p, key)
}

func (r *Router) describeSnapshot(ctx context.Context, p Provider, key profileKey) (ModelProfile, error) {
	if err := ctx.Err(); err != nil {
		return ModelProfile{}, err
	}
	provider, model := key.provider, key.model
	cache := &r.modelProfiles
	for {
		cache.mu.Lock()
		if cache.entries == nil {
			cache.entries = make(map[profileKey]*profileEntry)
		}
		if entry := cache.entries[key]; entry != nil {
			if entry.done != nil {
				done := entry.done
				cache.mu.Unlock()
				select {
				case <-done:
					continue
				case <-ctx.Done():
					return ModelProfile{}, ctx.Err()
				}
			}
			if time.Now().Before(entry.profile.ExpiresAt) {
				result := entry.profile
				cache.mu.Unlock()
				return result, nil
			}
			delete(cache.entries, key)
		}
		// Evict completed entries only. If every slot is in flight, return an
		// explicit unknown profile instead of unbounded fan-out or cache growth.
		if len(cache.entries) >= maxModelProfiles {
			var oldest profileKey
			var expiry time.Time
			for k, v := range cache.entries {
				if v.done == nil && (expiry.IsZero() || v.profile.ExpiresAt.Before(expiry)) {
					oldest, expiry = k, v.profile.ExpiresAt
				}
			}
			if expiry.IsZero() {
				cache.mu.Unlock()
				result := UnknownModelProfile(provider, model)
				result.Warning = "Model metadata lookup is busy; capabilities remain unknown."
				return result, nil
			}
			delete(cache.entries, oldest)
		}
		entry := &profileEntry{done: make(chan struct{})}
		cache.entries[key] = entry
		cache.mu.Unlock()
		result := UnknownModelProfile(provider, model)
		var lookupErr error
		if profiler, ok := p.(ModelProfiler); ok && model != "" {
			lookupCtx, cancel := context.WithTimeout(ctx, modelProfileTimeout)
			result, lookupErr = readModelProfile(lookupCtx, profiler, model)
			cancel()
		}
		if result.Provider != provider || result.Model != model {
			result = UnknownModelProfile(provider, model)
			lookupErr = fmt.Errorf("mismatched model metadata")
		}
		result = normalizeModelProfile(result)
		result.CheckedAt = time.Now().UTC()
		result.ExpiresAt = result.CheckedAt.Add(modelProfileTTL)
		if lookupErr != nil {
			result.Warning = "Model metadata is unavailable; unreported capabilities remain unknown."
			result.ExpiresAt = result.CheckedAt.Add(30 * time.Second)
		}
		if result.Source == "unknown" && result.Warning == "" {
			result.Warning = "This provider does not report model capabilities; using guarded execution without assuming model strength."
		}
		cache.mu.Lock()
		if ctx.Err() != nil {
			delete(cache.entries, key)
		} else {
			entry.profile = result
		}
		close(entry.done)
		entry.done = nil
		cache.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return ModelProfile{}, err
		}
		return result, nil
	}
}

func readModelProfile(ctx context.Context, p ModelProfiler, model string) (profile ModelProfile, err error) {
	// An optional extension must not poison a shared cache slot on panic.
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("model metadata reader failed")
		}
	}()
	return p.ProfileModel(ctx, model)
}

func normalizeModelProfile(p ModelProfile) ModelProfile {
	normalize := func(s Support) Support {
		if s != SupportYes && s != SupportNo {
			return SupportUnknown
		}
		return s
	}
	p.Chat, p.NativeTools, p.JSONMode, p.Reasoning, p.Vision = normalize(p.Chat), normalize(p.NativeTools), normalize(p.JSONMode), normalize(p.Reasoning), normalize(p.Vision)
	for _, limit := range []*int{&p.ContextTokens, &p.InputTokens, &p.OutputTokens} {
		if *limit < 0 || *limit > 16*1024*1024 {
			*limit = 0
		}
	}
	if p.Source != "provider_metadata" && p.Source != "adapter" {
		p.Source = "unknown"
	}
	if p.ContextSource != "provider_metadata" && p.ContextSource != "configured" && p.ContextSource != "provider_and_configured" {
		p.ContextSource = "unknown"
	}
	p.Warning = "" // No arbitrary provider text is promoted into agent instructions.
	return p
}

// ProfileInputBudget returns only an evidenced limit; 0 means unknown.
func ProfileInputBudget(p ModelProfile, output int) int {
	limit := p.InputTokens
	if p.ContextTokens > 0 {
		combined := max(1, p.ContextTokens-max(0, output))
		if limit == 0 || combined < limit {
			limit = combined
		}
	}
	return limit
}

// applyProfileLimits never weakens an output contract or raises a requested
// budget. Oversized immutable prompts fail before inference rather than losing
// the user's goal, evidence or system instructions inside a provider adapter.
func applyProfileLimits(p ModelProfile, req *CompletionRequest) error {
	if req.Operation == "embedding" {
		return nil
	}
	if p.Chat == SupportNo {
		return fmt.Errorf("model preflight: selected model does not support chat completion")
	}
	if p.NativeTools == SupportNo && len(req.Tools) > 0 {
		return fmt.Errorf("model preflight: native tools unsupported; use the guarded prompt-tool strategy")
	}
	if p.JSONMode == SupportNo && strings.TrimSpace(req.ResponseFormat) != "" {
		return fmt.Errorf("model preflight: requested structured output is unsupported; output contract was not changed")
	}
	// With an evidenced ceiling, an unbounded provider default could consume
	// the entire shared window. Use the runtime's conservative default reserve.
	if req.MaxTokens <= 0 && (p.ContextTokens > 0 || p.OutputTokens > 0) {
		req.MaxTokens = 1024
	}
	if p.OutputTokens > 0 && req.MaxTokens > p.OutputTokens {
		req.MaxTokens = p.OutputTokens
	}
	if p.ContextTokens > 0 && req.MaxTokens >= p.ContextTokens {
		req.MaxTokens = max(1, p.ContextTokens/4)
	}
	if budget := ProfileInputBudget(p, req.MaxTokens); budget > 0 && EstimateRequestTokens(*req) > budget {
		return fmt.Errorf("model preflight: prompt exceeds the evidenced input budget (%d tokens); split the task or reduce context without removing its requirements", budget)
	}
	return nil
}
