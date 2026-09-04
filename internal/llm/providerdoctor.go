// providerdoctor.go — plain-language diagnosis of LLM provider failures.
//
// When a chat completion, model list, or key-test call fails against a hosted
// provider (OpenAI, Anthropic, Groq, OpenRouter, Together, Mistral, DeepSeek,
// Google Gemini, Grok), the raw error string surfaced to the operator today
// looks like `openai: /models returned 401: {"error":{...}}`. That is accurate
// but opaque — a first-time operator has no idea whether it means the key is
// wrong, the region is blocked, the account has no billing, or the model was
// misspelt.
//
// ClassifyProviderError turns those raw strings into a stable category, a
// plain-English reason, and a concrete fix — the "provider doctor" analog to
// internal/channels/deliverydoctor.go used by the Channels page. Both the
// Providers page's Test Connection button and every gateway response that
// bubbles a provider error MUST route through this classifier so the operator
// sees the same friendly text everywhere.
//
// This file is deliberately pure (no I/O, no gateway types) so it is trivially
// unit-tested and reused by both the API handler and the CLI doctor.
package llm

import (
	"errors"
	"strings"
)

// ProviderCategory is a stable machine-readable classification of a provider
// call failure. UIs can branch on it; humans read Reason/Fix.
type ProviderCategory string

const (
	ProviderOK               ProviderCategory = "ok"
	ProviderMissingKey       ProviderCategory = "missing_key"
	ProviderBadKey           ProviderCategory = "bad_key"
	ProviderForbidden        ProviderCategory = "forbidden"
	ProviderRateLimited      ProviderCategory = "rate_limited"
	ProviderOverloaded       ProviderCategory = "overloaded"
	ProviderModelNotFound    ProviderCategory = "model_not_found"
	ProviderContextTooLarge  ProviderCategory = "context_too_large"
	ProviderQuotaExceeded    ProviderCategory = "quota_exceeded"
	ProviderRegionBlocked    ProviderCategory = "region_blocked"
	ProviderNetwork          ProviderCategory = "network"
	ProviderBadEndpoint      ProviderCategory = "bad_endpoint"
	ProviderProviderDown     ProviderCategory = "provider_down"
	ProviderLocalUnreachable ProviderCategory = "local_unreachable"
	ProviderUnknown          ProviderCategory = "unknown"
)

// ProviderDiagnosis is the classifier output shape. Category is machine-stable,
// Reason and Fix are shown to the operator; Detail carries the raw error text
// so the "Show raw error" toggle still has something to display.
type ProviderDiagnosis struct {
	OK       bool             `json:"ok"`
	Category ProviderCategory `json:"category"`
	Reason   string           `json:"reason"`
	Fix      string           `json:"fix,omitempty"`
	Detail   string           `json:"detail,omitempty"`
}

// ClassifyProviderError maps a raw provider error to a plain-language diagnosis.
// providerID is the configured id ("openai", "anthropic", "ollama", ...) and is
// used to tune the fix wording per provider (e.g. Ollama says "start ollama",
// OpenAI says "check the key at platform.openai.com/api-keys").
//
// When err is nil, returns {OK:true, Category:ProviderOK}. Callers should use
// the returned Category to branch on; the Reason/Fix strings are stable but not
// contract-frozen (they can be improved without breaking clients).
func ClassifyProviderError(providerID string, err error) ProviderDiagnosis {
	if err == nil {
		return ProviderDiagnosis{OK: true, Category: ProviderOK, Reason: "Provider is reachable."}
	}
	raw := err.Error()
	d := ClassifyProviderErrorString(providerID, raw)
	d.Detail = raw
	return d
}

// ClassifyProviderErrorString is the string form used by tests and callers that
// only have the raw error text (not an error value). Keeps ClassifyProviderError
// a one-liner for the common path.
func ClassifyProviderErrorString(providerID, raw string) ProviderDiagnosis {
	provider := strings.ToLower(strings.TrimSpace(providerID))
	label := providerLabel(provider)
	s := strings.ToLower(raw)

	contains := func(subs ...string) bool {
		for _, sub := range subs {
			if strings.Contains(s, sub) {
				return true
			}
		}
		return false
	}

	// Local providers (Ollama, LM Studio, vLLM, llama.cpp servers) have their
	// own failure vocabulary — a connection refused there means "start the
	// daemon", not "check the API key". Match these first so the fix wording
	// stays local-first.
	if isLocalProviderID(provider) {
		switch {
		case contains("connection refused", "no such host", "dial tcp", "connect: connection", "econnrefused"):
			return ProviderDiagnosis{
				Category: ProviderLocalUnreachable,
				Reason:   "The local model runtime is not reachable at the configured base URL.",
				Fix:      "Start the local runtime (`ollama serve` for Ollama, or your equivalent) and confirm the base URL matches (default `http://localhost:11434` for Ollama). Then click Test again.",
			}
		case contains("model not found", "model \"", "pull the model", "no such file or directory", "not found"):
			return ProviderDiagnosis{
				Category: ProviderModelNotFound,
				Reason:   "The local runtime is reachable but does not have the selected model installed.",
				Fix:      "Pull the model first (`ollama pull <name>`) and reload; the model will then appear in List models.",
			}
		case contains("timeout", "deadline exceeded", "context deadline"):
			return ProviderDiagnosis{
				Category: ProviderNetwork,
				Reason:   "The local runtime accepted the connection but did not respond in time.",
				Fix:      "The model may still be loading into memory (large models can take 30-90s on first call). Retry; if it persists, check the runtime logs.",
			}
		}
	}

	// Ordering matters — more specific signals first, coarse status buckets last.
	switch {

	// Auth: API key missing at the transport layer (401 with hints, or bare
	// "missing api key" text some SDKs surface).
	case contains("api key not provided", "missing api key", "no api key", "provide an api key", "authentication required"):
		return ProviderDiagnosis{
			Category: ProviderMissingKey,
			Reason:   label + " requires an API key, but none was sent with the request.",
			Fix:      "Add the API key on the Providers page (or run `sy setup`), save it to the vault, and restart the gateway.",
		}

	// Auth: bad or revoked key. 401 is the strong signal; some providers use
	// specific error codes.
	case contains("401", "invalid api key", "incorrect api key", "invalid_api_key", "authentication_error", "unauthenticated", "invalid_authentication", "invalid_credentials", "key not valid", "api key expired", "api_key_expired", "invalid access token", "invalid authentication", "expired token"):
		return ProviderDiagnosis{
			Category: ProviderBadKey,
			Reason:   label + " rejected the API key — it is missing, wrong, expired, or has been revoked.",
			Fix:      fixWordingForBadKey(provider),
		}

	// Model 404 — most providers return the model id in the message so we can
	// call it out. Checked BEFORE the generic 404 bucket because a bare 404
	// against /models is different from "model xyz not found".
	case contains("model not found", "model_not_found", "model does not exist", "no such model", "invalid model", "unknown model", "the model", "was not found"):
		return ProviderDiagnosis{
			Category: ProviderModelNotFound,
			Reason:   label + " does not have the model you asked for (either it was renamed, deprecated, or your account has no access to it).",
			Fix:      "Click List models to see what's available on this key, then Save one of the returned models as default.",
		}

	// Quota / billing.
	case contains("insufficient_quota", "quota_exceeded", "exceeded your current quota", "you exceeded your quota", "billing hard limit", "no billing", "billing_required", "requires a paid plan", "account balance", "credit balance is too low", "no active subscription", "payment required", "402"):
		return ProviderDiagnosis{
			Category: ProviderQuotaExceeded,
			Reason:   label + " reports the account has no available credit or has exceeded its billing quota.",
			Fix:      "Top up credit / enable billing on the " + label + " dashboard and try again.",
		}

	// Rate limit — surface distinctly from quota because the fix is "wait", not
	// "pay". Anthropic uses `overloaded_error` which is closer to "provider is
	// swamped" than a per-account rate limit; classify that separately.
	case contains("overloaded_error", "overloaded", "server_overloaded", "capacity", "service is at capacity"):
		return ProviderDiagnosis{
			Category: ProviderOverloaded,
			Reason:   label + " is currently overloaded and asked us to retry later.",
			Fix:      "Wait a few seconds and try again. If it persists during business hours, temporarily route to a different provider (e.g. OpenAI ↔ Anthropic) from the Studio model picker.",
		}
	case contains("429", "rate_limit", "rate limit", "too many requests", "rate-limited", "requests_per_minute", "tpm", "rpm", "retry-after", "retry_after"):
		return ProviderDiagnosis{
			Category: ProviderRateLimited,
			Reason:   label + " is throttling requests from this key.",
			Fix:      "Slow the call rate, increase your rate-limit tier on the " + label + " dashboard, or spread agents across multiple keys/providers.",
		}

	// Region blocks — providers like OpenAI/Anthropic surface a friendly reason
	// when the caller's IP is in an unsupported country. Classify before the
	// generic 403 bucket so the fix wording is meaningful.
	case contains("country, region, or territory not supported", "not supported in your country", "region_not_supported", "country_not_supported", "not available in your region"):
		return ProviderDiagnosis{
			Category: ProviderRegionBlocked,
			Reason:   label + " is not available from the network this gateway is calling from.",
			Fix:      "Use " + label + " through a supported egress (VPN, cloud instance in a supported region), or configure a different provider.",
		}

	// Forbidden / permission / project mismatch. Distinguished from bad-key by
	// message content (the key IS valid but this endpoint/model is off-limits).
	case contains("403", "forbidden", "permission_denied", "not authorized", "not_permitted", "insufficient permissions", "missing scope", "scope_insufficient", "project does not have access", "no access to model", "project_not_authorized", "org restriction"):
		return ProviderDiagnosis{
			Category: ProviderForbidden,
			Reason:   label + " accepted the key but this project/organization is not allowed to use the requested model or endpoint.",
			Fix:      "On the " + label + " dashboard, enable access to the model or add the project/organization scope that owns it. Then re-test.",
		}

	// Context / prompt too large.
	case contains("context_length_exceeded", "context length", "maximum context length", "too many tokens", "prompt is too long", "input is too long", "reduce your prompt", "413"):
		return ProviderDiagnosis{
			Category: ProviderContextTooLarge,
			Reason:   "The request exceeds " + label + "'s maximum context length for this model.",
			Fix:      "Trim the system prompt / conversation history, switch to a longer-context model, or summarise older turns via the Memory settings.",
		}

	// Provider outages / 5xx buckets. `503` typically means try again; `500`
	// could be a bad request the provider is masking. Both go into the same
	// bucket with "retry, then check status page".
	case contains("503", "service_unavailable", "temporarily unavailable", "provider_down"):
		return ProviderDiagnosis{
			Category: ProviderProviderDown,
			Reason:   label + " is temporarily unavailable.",
			Fix:      "Retry in a minute. If it persists, check " + label + "'s status page and fail over to another provider from the Studio model picker.",
		}
	case contains("500", "internal server error", "internal_error", "server_error"):
		return ProviderDiagnosis{
			Category: ProviderProviderDown,
			Reason:   label + " returned a server error.",
			Fix:      "Retry; if the error persists across attempts, check " + label + "'s status page or open a support ticket with the request id from the raw error.",
		}
	case contains("502", "504", "bad gateway", "gateway timeout"):
		return ProviderDiagnosis{
			Category: ProviderProviderDown,
			Reason:   label + " is intermittently unreachable through a load balancer.",
			Fix:      "Retry in a few seconds. If failures continue, check " + label + "'s status page.",
		}

	// Bad endpoint / URL: usually a typo in base_url, or someone pointed a
	// Together/OpenRouter config at the wrong host.
	case contains("no such host", "dns lookup", "name resolution", "getaddrinfo", "certificate signed", "x509", "tls handshake", "protocol error", "malformed"):
		return ProviderDiagnosis{
			Category: ProviderBadEndpoint,
			Reason:   "The configured base URL for " + label + " could not be reached or the TLS handshake failed.",
			Fix:      "Verify the base_url on the Providers page (typos in the host are the usual cause), and confirm outbound HTTPS to the host is permitted.",
		}

	// Transport failures — timeout, connection reset, EOF.
	case contains("timeout", "deadline exceeded", "context deadline", "i/o timeout", "connection reset", "connection refused", "eof", "broken pipe", "network is unreachable"):
		return ProviderDiagnosis{
			Category: ProviderNetwork,
			Reason:   label + " could not be reached over the network.",
			Fix:      "Check outbound network access from this host; retry after confirming DNS + HTTPS to the provider is unblocked.",
		}

	// Bare 404 — usually a wrong path when a self-hosted gateway is configured.
	case contains("404", "not found"):
		return ProviderDiagnosis{
			Category: ProviderBadEndpoint,
			Reason:   "The endpoint URL exists but the requested resource is not there.",
			Fix:      "Confirm the base_url is complete (some proxies want `/v1` on the end and some do not) and that the model / path is correct for " + label + ".",
		}

	// Bare 400 fallback.
	case contains("400", "bad request", "invalid_request"):
		return ProviderDiagnosis{
			Category: ProviderUnknown,
			Reason:   label + " rejected the request as malformed.",
			Fix:      "This usually means an incompatible parameter (e.g. `parallel_tool_calls` against a provider that doesn't support it). Check the raw error for the offending field.",
		}

	default:
		return ProviderDiagnosis{
			Category: ProviderUnknown,
			Reason:   "The provider call failed for a reason the doctor didn't recognise. The raw error is included below.",
			Fix:      "Check the raw error and the setup guide for " + label + "; if this looks like a common failure, please report it so we can add it to the doctor.",
		}
	}
}

// ClassifyProviderErrorAs mirrors errors.As style — the classifier collapses
// any wrapper chain and works from the outermost .Error() string. This helper
// exists so future callers with a typed error path can plug in without
// changing the public signature.
func ClassifyProviderErrorAs(providerID string, target error) ProviderDiagnosis {
	if target == nil {
		return ProviderDiagnosis{OK: true, Category: ProviderOK}
	}
	// Unwrap once to also match wrapped provider errors — errors.Unwrap returns
	// nil when there is no wrapped error, so this is safe.
	if inner := errors.Unwrap(target); inner != nil {
		return ClassifyProviderError(providerID, inner)
	}
	return ClassifyProviderError(providerID, target)
}

// providerLabel returns a human-friendly display name for the provider id.
// The switch stays short — anything we don't recognise falls back to the raw id.
func providerLabel(id string) string {
	switch id {
	case "openai":
		return "OpenAI"
	case "anthropic":
		return "Anthropic"
	case "google", "gemini":
		return "Google Gemini"
	case "groq":
		return "Groq"
	case "openrouter":
		return "OpenRouter"
	case "together":
		return "Together AI"
	case "mistral":
		return "Mistral"
	case "deepseek":
		return "DeepSeek"
	case "grok":
		return "xAI Grok"
	case "ollama":
		return "Ollama"
	case "lmstudio", "lm_studio", "lm-studio":
		return "LM Studio"
	case "vllm":
		return "vLLM"
	case "":
		return "the provider"
	default:
		return id
	}
}

// fixWordingForBadKey picks a per-provider "where to rotate the key" hint. The
// generic form still works for unrecognised ids.
func fixWordingForBadKey(provider string) string {
	switch provider {
	case "openai":
		return "Rotate the key at platform.openai.com/api-keys, save it via Providers → OpenAI, and restart the gateway."
	case "anthropic":
		return "Rotate the key at console.anthropic.com/settings/keys, save it via Providers → Anthropic, and restart the gateway."
	case "google", "gemini":
		return "Rotate the key at aistudio.google.com/apikey, save it via Providers → Google, and restart the gateway."
	case "groq":
		return "Rotate the key at console.groq.com/keys, save it via Providers → Groq, and restart the gateway."
	case "openrouter":
		return "Rotate the key at openrouter.ai/keys, save it via Providers → OpenRouter, and restart the gateway."
	case "together":
		return "Rotate the key at api.together.xyz/settings/api-keys, save it via Providers → Together, and restart the gateway."
	case "mistral":
		return "Rotate the key at console.mistral.ai, save it via Providers → Mistral, and restart the gateway."
	case "deepseek":
		return "Rotate the key at platform.deepseek.com/api_keys, save it via Providers → DeepSeek, and restart the gateway."
	case "grok":
		return "Rotate the key at console.x.ai, save it via Providers → Grok, and restart the gateway."
	default:
		return "Rotate the API key with the provider, save it via Providers, and restart the gateway."
	}
}

// isLocalProviderID keeps a local copy of the local-provider heuristic so this
// file has no dependency on internal/studio (which imports internal/llm — the
// dependency would be circular). Kept intentionally narrow: only the providers
// that speak the local-runtime vocabulary go through the local branch above.
func isLocalProviderID(id string) bool {
	switch id {
	case "ollama", "lmstudio", "lm_studio", "lm-studio", "vllm", "llamacpp", "llama_cpp", "llama-cpp":
		return true
	}
	return false
}
