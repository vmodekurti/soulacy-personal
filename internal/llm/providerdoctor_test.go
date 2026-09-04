package llm

import (
	"errors"
	"strings"
	"testing"
)

// TestClassifyProviderErrorCoverage pins the classifier's coverage of the
// error shapes the drivers produce. Failure here is a signal that either the
// classifier regressed or a driver's raw string changed shape — in both cases
// the operator would silently drop back to the "unknown" bucket, which is
// exactly what E4 is trying to avoid.
func TestClassifyProviderErrorCoverage(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		raw      string
		want     ProviderCategory
		reason   string // substring that must appear in Reason (case-insensitive)
	}{
		// --- Bad key / 401 shapes across providers ---
		{
			name:     "openai_401_models",
			provider: "openai",
			raw:      `openai: /models returned 401: {"error":{"message":"Incorrect API key provided: sk-****","type":"invalid_request_error","code":"invalid_api_key"}}`,
			want:     ProviderBadKey,
			reason:   "openai",
		},
		{
			name:     "anthropic_401_chat",
			provider: "anthropic",
			raw:      `anthropic: http 401: {"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`,
			want:     ProviderBadKey,
			reason:   "anthropic",
		},
		{
			name:     "groq_invalid_api_key",
			provider: "groq",
			raw:      `groq: http 401: {"error":{"code":"invalid_api_key","message":"Invalid API Key"}}`,
			want:     ProviderBadKey,
			reason:   "groq",
		},

		// --- Missing key (before request even goes out) ---
		{
			name:     "missing_key_hint",
			provider: "openai",
			raw:      "openai: /models request: no api key provided",
			want:     ProviderMissingKey,
		},

		// --- Model not found (called before generic 404) ---
		{
			name:     "openai_model_not_found",
			provider: "openai",
			raw:      `openai: http 404: {"error":{"message":"The model 'gpt-9-mega' does not exist","code":"model_not_found"}}`,
			want:     ProviderModelNotFound,
		},
		{
			name:     "anthropic_unknown_model",
			provider: "anthropic",
			raw:      `anthropic: http 404: {"error":{"type":"not_found_error","message":"The requested model does not exist"}}`,
			want:     ProviderModelNotFound,
		},

		// --- Quota / billing vs. rate limit ---
		{
			name:     "openai_insufficient_quota",
			provider: "openai",
			raw:      `openai: http 429: {"error":{"code":"insufficient_quota","message":"You exceeded your current quota"}}`,
			want:     ProviderQuotaExceeded,
		},
		{
			name:     "openai_rate_limit",
			provider: "openai",
			raw:      `openai: http 429: {"error":{"code":"rate_limit_exceeded","message":"Rate limit reached, please retry"}}`,
			want:     ProviderRateLimited,
		},
		{
			name:     "anthropic_overloaded",
			provider: "anthropic",
			raw:      `anthropic: http 529: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
			want:     ProviderOverloaded,
		},

		// --- Region / permission ---
		{
			name:     "openai_region_block",
			provider: "openai",
			raw:      `openai: http 403: Country, region, or territory not supported`,
			want:     ProviderRegionBlocked,
		},
		{
			name:     "generic_forbidden",
			provider: "openai",
			raw:      `openai: http 403: {"error":{"message":"Project does not have access to model gpt-5-turbo"}}`,
			want:     ProviderForbidden,
		},

		// --- Context / prompt too large ---
		{
			name:     "context_length_exceeded",
			provider: "openai",
			raw:      `openai: http 400: {"error":{"code":"context_length_exceeded","message":"This model's maximum context length is 8192 tokens"}}`,
			want:     ProviderContextTooLarge,
		},

		// --- Provider outages (5xx) ---
		{
			name:     "openai_503",
			provider: "openai",
			raw:      `openai: http 503: Service temporarily unavailable`,
			want:     ProviderProviderDown,
		},

		// --- Bad endpoint / URL ---
		{
			name:     "no_such_host",
			provider: "openai",
			raw:      `openai: /models request: Get "https://api.openaii.com/v1/models": dial tcp: lookup api.openaii.com: no such host`,
			want:     ProviderBadEndpoint,
		},

		// --- Local providers ---
		{
			name:     "ollama_connection_refused",
			provider: "ollama",
			raw:      `ollama: connection refused`,
			want:     ProviderLocalUnreachable,
		},
		{
			name:     "ollama_model_not_pulled",
			provider: "ollama",
			raw:      `ollama: unexpected status 404: model "llama3.2" not found, try pulling it first`,
			want:     ProviderModelNotFound,
		},

		// --- Fallback ---
		{
			name:     "totally_unknown",
			provider: "openai",
			raw:      `openai: something entirely new`,
			want:     ProviderUnknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := ClassifyProviderErrorString(tc.provider, tc.raw)
			if d.Category != tc.want {
				t.Fatalf("category mismatch: got %q, want %q\nreason: %s\nfix: %s", d.Category, tc.want, d.Reason, d.Fix)
			}
			if tc.reason != "" && !strings.Contains(strings.ToLower(d.Reason), strings.ToLower(tc.reason)) {
				t.Fatalf("reason %q missing substring %q", d.Reason, tc.reason)
			}
			if d.Reason == "" {
				t.Fatal("Reason must not be empty for classified errors")
			}
			// Every non-Unknown/non-OK category should have an actionable fix.
			if d.Category != ProviderUnknown && d.Category != ProviderOK && d.Fix == "" {
				t.Fatal("Fix must not be empty for known categories")
			}
		})
	}
}

func TestClassifyProviderErrorNil(t *testing.T) {
	d := ClassifyProviderError("openai", nil)
	if !d.OK || d.Category != ProviderOK {
		t.Fatalf("nil error should classify as OK, got %+v", d)
	}
}

func TestClassifyProviderErrorPreservesDetail(t *testing.T) {
	raw := `openai: /models returned 401: bad key`
	d := ClassifyProviderError("openai", errors.New(raw))
	if d.Detail != raw {
		t.Fatalf("Detail should carry the raw error, got %q", d.Detail)
	}
	if d.Category != ProviderBadKey {
		t.Fatalf("category should still classify through the error interface, got %q", d.Category)
	}
}

func TestClassifyProviderErrorAsUnwrap(t *testing.T) {
	inner := errors.New(`openai: http 429: {"error":{"code":"insufficient_quota"}}`)
	wrapped := &wrappedError{inner: inner}
	d := ClassifyProviderErrorAs("openai", wrapped)
	if d.Category != ProviderQuotaExceeded {
		t.Fatalf("wrapped classification failed: got %q, want %q", d.Category, ProviderQuotaExceeded)
	}
}

type wrappedError struct{ inner error }

func (w *wrappedError) Error() string { return "wrapped: " + w.inner.Error() }
func (w *wrappedError) Unwrap() error { return w.inner }
