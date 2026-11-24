package llm

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/soulacy/soulacy/internal/privacy"
)

// PrivacyPolicy keeps private requests on a provider that runs on this machine.
//
// The router is the last point every model call passes before data leaves the
// process, so the rule is enforced here rather than trusted to each caller:
// once a run is labelled private, none of its calls — workflow steps and
// delegated agents included — can reach a provider that is not local.
//
// It is inert when no local provider is registered: with nowhere private to
// send a request, refusing it would only break installs that never opted in.
type PrivacyPolicy struct {
	Enabled bool
	// Provider and Model optionally pin the local route. Empty means the first
	// registered local provider (by ID) and that provider's own default model.
	Provider string
	Model    string
}

type privacyState struct {
	mu     sync.RWMutex
	policy PrivacyPolicy
}

// SetPrivacyPolicy installs the private-data routing rule.
func (r *Router) SetPrivacyPolicy(policy PrivacyPolicy) {
	state := &r.privacy
	state.mu.Lock()
	defer state.mu.Unlock()
	policy.Provider, policy.Model = strings.TrimSpace(policy.Provider), strings.TrimSpace(policy.Model)
	state.policy = policy
}

// PrivateRoute reports where a private request would be sent. ok is false when
// the rule is off or no local provider is registered. A current provider that
// is already local is kept, with model "" meaning "leave the model as it is".
func (r *Router) PrivateRoute(current string) (provider, model string, ok bool) {
	state := &r.privacy
	state.mu.RLock()
	policy := state.policy
	state.mu.RUnlock()
	if !policy.Enabled {
		return "", "", false
	}
	locals := r.RoutePolicy().LocalProviders
	registered := func(id string) bool { return id != "" && locals[id] && r.Provider(id) != nil }
	if current == "" {
		current = r.DefaultProvider()
	}
	if registered(current) {
		return current, "", true
	}
	if registered(policy.Provider) {
		return policy.Provider, policy.Model, true
	}
	ids := make([]string, 0, len(locals))
	for id := range locals {
		if registered(id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return "", "", false
	}
	sort.Strings(ids)
	return ids[0], "", true
}

// IsPrivateRequest reports whether a call belongs to a run that was labelled
// private.
//
// The router enforces the label; it does not judge content. The judgment is
// made once, when a run starts, from the person's own words. Re-judging every
// request moved a run to the local model halfway through — after its context
// had been sized for a larger one — the moment a tool returned a web page with
// a card-shaped product code in it (#401).
func IsPrivateRequest(ctx context.Context, _ CompletionRequest) bool {
	return strings.EqualFold(strings.TrimSpace(CallMetadataFromContext(ctx).DataClassification), privacy.ClassPrivate)
}

// applyPrivacy rewrites a private call onto the local route. private is true
// when the caller must not fail over to any other provider.
func (r *Router) applyPrivacy(ctx context.Context, providerID string, req CompletionRequest) (context.Context, string, CompletionRequest, bool, error) {
	if req.Operation == "embedding" {
		// Changing the embedding model would silently break stored vectors.
		return ctx, providerID, req, false, nil
	}
	provider, model, ok := r.PrivateRoute(providerID)
	if !ok || !IsPrivateRequest(ctx, req) {
		return ctx, providerID, req, false, nil
	}
	effective := providerID
	if effective == "" {
		effective = r.DefaultProvider()
	}
	if provider != effective {
		// The requested model belongs to the provider we are not using.
		req.Model = model
	}
	metadata := CallMetadataFromContext(ctx)
	metadata.DataClassification = privacy.ClassPrivate
	return WithCallMetadata(ctx, metadata), provider, req, true, nil
}
