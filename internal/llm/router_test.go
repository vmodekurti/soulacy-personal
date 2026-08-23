// router_test.go — tests for Router, retry helpers, httpclient helpers,
// and EmbedderRegistry. Uses fake http.RoundTripper — no httptest.NewServer.
package llm

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Minimal fake Provider used by Router tests
// ---------------------------------------------------------------------------

type fakeProvider struct {
	id       string
	response *CompletionResponse
	err      error
	models   []string
}

func (f *fakeProvider) ID() string { return f.id }

func (f *fakeProvider) Complete(_ context.Context, _ CompletionRequest) (*CompletionResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.response != nil {
		return f.response, nil
	}
	return &CompletionResponse{Content: "ok from " + f.id}, nil
}

func (f *fakeProvider) Models(_ context.Context) ([]string, error) {
	return f.models, nil
}

// ---------------------------------------------------------------------------
// Router — basic construction
// ---------------------------------------------------------------------------

func TestNewRouterStoresDefaultID(t *testing.T) {
	r := NewRouter("ollama")
	if r.DefaultProvider() != "ollama" {
		t.Errorf("DefaultProvider = %q, want ollama", r.DefaultProvider())
	}
}

func TestNewRouterEmptyDefaultID(t *testing.T) {
	r := NewRouter("")
	if r.DefaultProvider() != "" {
		t.Errorf("DefaultProvider = %q, want empty", r.DefaultProvider())
	}
}

// ---------------------------------------------------------------------------
// Router — Register / ProviderIDs
// ---------------------------------------------------------------------------

func TestRouterRegisterSingleProvider(t *testing.T) {
	r := NewRouter("a")
	r.Register(&fakeProvider{id: "a"})
	ids := r.ProviderIDs()
	if len(ids) != 1 || ids[0] != "a" {
		t.Errorf("ProviderIDs = %v, want [a]", ids)
	}
}

func TestRouterRegisterMultipleProviders(t *testing.T) {
	r := NewRouter("a")
	r.Register(&fakeProvider{id: "a"})
	r.Register(&fakeProvider{id: "b"})
	r.Register(&fakeProvider{id: "c"})

	ids := r.ProviderIDs()
	if len(ids) != 3 {
		t.Errorf("ProviderIDs count = %d, want 3", len(ids))
	}
}

func TestRouterRegisterOverwritesSameID(t *testing.T) {
	r := NewRouter("a")
	r.Register(&fakeProvider{id: "a", response: &CompletionResponse{Content: "first"}})
	r.Register(&fakeProvider{id: "a", response: &CompletionResponse{Content: "second"}})

	resp, err := r.Complete(context.Background(), "a", CompletionRequest{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Content != "second" {
		t.Errorf("content = %q, want second (last registration wins)", resp.Content)
	}
}

// ---------------------------------------------------------------------------
// Router — Provider() lookup
// ---------------------------------------------------------------------------

func TestRouterProviderByID(t *testing.T) {
	r := NewRouter("a")
	fp := &fakeProvider{id: "a"}
	r.Register(fp)

	p := r.Provider("a")
	if p == nil {
		t.Fatal("Provider('a') returned nil")
	}
	if p.ID() != "a" {
		t.Errorf("p.ID() = %q, want a", p.ID())
	}
}

func TestRouterProviderEmptyIDReturnsDefault(t *testing.T) {
	r := NewRouter("default-p")
	fp := &fakeProvider{id: "default-p"}
	r.Register(fp)

	p := r.Provider("") // empty → default
	if p == nil {
		t.Fatal("Provider('') returned nil")
	}
	if p.ID() != "default-p" {
		t.Errorf("p.ID() = %q, want default-p", p.ID())
	}
}

func TestRouterProviderUnknownIDReturnsNil(t *testing.T) {
	r := NewRouter("a")
	r.Register(&fakeProvider{id: "a"})

	if r.Provider("unknown") != nil {
		t.Error("Provider('unknown') should return nil")
	}
}

// ---------------------------------------------------------------------------
// Router — Complete dispatch
// ---------------------------------------------------------------------------

func TestRouterCompleteDispatchesToNamedProvider(t *testing.T) {
	r := NewRouter("a")
	r.Register(&fakeProvider{id: "a", response: &CompletionResponse{Content: "from-a"}})
	r.Register(&fakeProvider{id: "b", response: &CompletionResponse{Content: "from-b"}})

	resp, err := r.Complete(context.Background(), "b", CompletionRequest{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Content != "from-b" {
		t.Errorf("content = %q, want from-b", resp.Content)
	}
}

func TestRouterCompletePrefersContextProviderResolver(t *testing.T) {
	r := NewRouter("shared")
	r.Register(&fakeProvider{id: "shared", response: &CompletionResponse{Content: "deployment"}})
	r.SetProviderResolver(func(_ context.Context, providerID string) (Provider, bool) {
		if providerID != "shared" {
			return nil, false
		}
		return &fakeProvider{id: "shared", response: &CompletionResponse{Content: "workspace"}}, true
	})

	resp, err := r.Complete(context.Background(), "shared", CompletionRequest{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Content != "workspace" {
		t.Fatalf("content = %q, want workspace resolver provider", resp.Content)
	}
}

func TestRouterCompleteEmptyProviderUsesDefault(t *testing.T) {
	r := NewRouter("default-p")
	r.Register(&fakeProvider{id: "default-p", response: &CompletionResponse{Content: "default-resp"}})

	resp, err := r.Complete(context.Background(), "", CompletionRequest{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Content != "default-resp" {
		t.Errorf("content = %q, want default-resp", resp.Content)
	}
}

func TestRouterCompleteUnknownProviderErrors(t *testing.T) {
	r := NewRouter("a")
	r.Register(&fakeProvider{id: "a"})

	_, err := r.Complete(context.Background(), "bogus", CompletionRequest{})
	if err == nil {
		t.Fatal("expected error for unknown provider, got nil")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error should mention the unknown provider name, got: %v", err)
	}
}

func TestRouterCompleteNoProvidersNoDefaultErrors(t *testing.T) {
	r := NewRouter("") // no default, no providers
	_, err := r.Complete(context.Background(), "", CompletionRequest{})
	if err == nil {
		t.Fatal("expected error when no providers registered")
	}
}

func TestRouterCompletePropagatesproviderError(t *testing.T) {
	provErr := errors.New("provider exploded")
	r := NewRouter("p")
	r.Register(&fakeProvider{id: "p", err: provErr})

	_, err := r.Complete(context.Background(), "p", CompletionRequest{})
	if !errors.Is(err, provErr) {
		t.Errorf("expected provErr, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Router — ProviderIDs on empty router
// ---------------------------------------------------------------------------

func TestRouterProviderIDsEmpty(t *testing.T) {
	r := NewRouter("x")
	if ids := r.ProviderIDs(); len(ids) != 0 {
		t.Errorf("ProviderIDs on empty router = %v, want []", ids)
	}
}

// ---------------------------------------------------------------------------
// shouldRetryStatus
// ---------------------------------------------------------------------------

func TestShouldRetryStatus(t *testing.T) {
	retryable := []int{408, 429, 500, 502, 503, 504}
	for _, code := range retryable {
		if !shouldRetryStatus(code) {
			t.Errorf("shouldRetryStatus(%d) = false, want true", code)
		}
	}
	notRetryable := []int{200, 201, 400, 401, 403, 404, 422}
	for _, code := range notRetryable {
		if shouldRetryStatus(code) {
			t.Errorf("shouldRetryStatus(%d) = true, want false", code)
		}
	}
}

// ---------------------------------------------------------------------------
// shouldRetryError
// ---------------------------------------------------------------------------

type timeoutError struct{ msg string }

func (e *timeoutError) Error() string   { return e.msg }
func (e *timeoutError) Timeout() bool   { return true }
func (e *timeoutError) Temporary() bool { return true }

func TestShouldRetryErrorNil(t *testing.T) {
	if shouldRetryError(nil) {
		t.Error("shouldRetryError(nil) = true, want false")
	}
}

func TestShouldRetryErrorEOF(t *testing.T) {
	if !shouldRetryError(io.EOF) {
		t.Error("shouldRetryError(io.EOF) = false, want true")
	}
}

func TestShouldRetryErrorUnexpectedEOF(t *testing.T) {
	if !shouldRetryError(io.ErrUnexpectedEOF) {
		t.Error("shouldRetryError(io.ErrUnexpectedEOF) = false, want true")
	}
}

func TestShouldRetryErrorNetTimeout(t *testing.T) {
	var netErr net.Error = &timeoutError{"timeout"}
	if !shouldRetryError(netErr) {
		t.Error("shouldRetryError(timeout net.Error) = false, want true")
	}
}

func TestShouldRetryErrorURLErrWrapsEOF(t *testing.T) {
	urlErr := &url.Error{Op: "Post", URL: "http://x", Err: io.EOF}
	if !shouldRetryError(urlErr) {
		t.Error("shouldRetryError(*url.Error wrapping EOF) = false, want true")
	}
}

func TestShouldRetryErrorNonTransient(t *testing.T) {
	if shouldRetryError(errors.New("application error")) {
		t.Error("shouldRetryError(generic error) = true, want false")
	}
}

// ---------------------------------------------------------------------------
// parseRetryAfter
// ---------------------------------------------------------------------------

func TestParseRetryAfterEmpty(t *testing.T) {
	if d := parseRetryAfter(""); d != 0 {
		t.Errorf("parseRetryAfter('') = %v, want 0", d)
	}
}

func TestParseRetryAfterSeconds(t *testing.T) {
	d := parseRetryAfter("30")
	if d != 30*time.Second {
		t.Errorf("parseRetryAfter('30') = %v, want 30s", d)
	}
}

func TestParseRetryAfterInvalidString(t *testing.T) {
	d := parseRetryAfter("not-a-number-or-date")
	if d != 0 {
		t.Errorf("parseRetryAfter(invalid) = %v, want 0", d)
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	// Use a date 60 seconds in the future.
	future := time.Now().Add(60 * time.Second).UTC()
	httpDate := future.Format(http.TimeFormat)
	d := parseRetryAfter(httpDate)
	if d <= 0 || d > 70*time.Second {
		t.Errorf("parseRetryAfter(future HTTP date) = %v, expected ~60s", d)
	}
}

func TestParseRetryAfterPastHTTPDate(t *testing.T) {
	past := time.Now().Add(-60 * time.Second).UTC()
	httpDate := past.Format(http.TimeFormat)
	d := parseRetryAfter(httpDate)
	if d != 0 {
		t.Errorf("parseRetryAfter(past HTTP date) = %v, want 0", d)
	}
}

// ---------------------------------------------------------------------------
// jitter
// ---------------------------------------------------------------------------

func TestJitterIsNonNegative(t *testing.T) {
	for i := 0; i < 50; i++ {
		if j := jitter(time.Second); j < 0 {
			t.Fatalf("jitter returned negative value: %v", j)
		}
	}
}

func TestJitterZeroBaseReturnsZero(t *testing.T) {
	if j := jitter(0); j != 0 {
		t.Errorf("jitter(0) = %v, want 0", j)
	}
}

func TestJitterNegativeBaseReturnsZero(t *testing.T) {
	if j := jitter(-time.Second); j != 0 {
		t.Errorf("jitter(-1s) = %v, want 0", j)
	}
}

func TestJitterIsLessThanHalfBase(t *testing.T) {
	base := 2 * time.Second
	for i := 0; i < 100; i++ {
		if j := jitter(base); j >= base/2 {
			t.Errorf("jitter(%v) = %v, want < %v", base, j, base/2)
		}
	}
}

// ---------------------------------------------------------------------------
// RetryConfig.normalize
// ---------------------------------------------------------------------------

func TestRetryConfigNormalizeDefaults(t *testing.T) {
	cfg := RetryConfig{}.normalize()
	if cfg.MaxAttempts != 3 {
		t.Errorf("MaxAttempts = %d, want 3", cfg.MaxAttempts)
	}
	if cfg.InitialDelay != 250*time.Millisecond {
		t.Errorf("InitialDelay = %v, want 250ms", cfg.InitialDelay)
	}
	if cfg.MaxDelay != 5*time.Second {
		t.Errorf("MaxDelay = %v, want 5s", cfg.MaxDelay)
	}
}

func TestRetryConfigNormalizePreservesExplicitValues(t *testing.T) {
	cfg := RetryConfig{MaxAttempts: 7, InitialDelay: time.Second, MaxDelay: 10 * time.Second}.normalize()
	if cfg.MaxAttempts != 7 || cfg.InitialDelay != time.Second || cfg.MaxDelay != 10*time.Second {
		t.Errorf("normalize changed explicit values: %+v", cfg)
	}
}

// ---------------------------------------------------------------------------
// DoWithRetry — success on first attempt
// ---------------------------------------------------------------------------

func TestDoWithRetrySuccessFirstAttempt(t *testing.T) {
	calls := 0
	client := clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(200, `{}`), nil
	})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://test/", nil)
	resp, err := DoWithRetry(context.Background(), client, req, RetryConfig{MaxAttempts: 3})
	if err != nil {
		t.Fatalf("DoWithRetry: %v", err)
	}
	defer resp.Body.Close()
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// TestDoWithRetryRetriesOn503 ensures that a 503 triggers a retry and the
// second call succeeds.
func TestDoWithRetryRetriesOn503(t *testing.T) {
	calls := 0
	client := clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls < 2 {
			resp := jsonResponse(503, `{}`)
			resp.Header.Set("x-request-id", "attempt-1")
			return resp, nil
		}
		resp := jsonResponse(200, `{}`)
		resp.Header.Set("x-request-id", "attempt-2")
		return resp, nil
	})
	ctx, stats := withRetryStats(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://test/", nil)
	resp, err := DoWithRetry(ctx, client, req, RetryConfig{
		MaxAttempts:  3,
		InitialDelay: time.Microsecond, // near-zero to keep test fast
		MaxDelay:     time.Millisecond,
	})
	if err != nil {
		t.Fatalf("DoWithRetry: %v", err)
	}
	defer resp.Body.Close()
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	if resp.StatusCode != 200 {
		t.Errorf("final status = %d, want 200", resp.StatusCode)
	}
	attempts, ids := stats.snapshot()
	if attempts != 2 || strings.Join(ids, ",") != "attempt-1,attempt-2" {
		t.Fatalf("retry stats attempts=%d ids=%v", attempts, ids)
	}
}

// TestDoWithRetryExhaustsAllAttempts returns the last response when all attempts fail.
func TestDoWithRetryExhaustsAllAttempts(t *testing.T) {
	calls := 0
	client := clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(503, `{}`), nil
	})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://test/", nil)
	resp, err := DoWithRetry(context.Background(), client, req, RetryConfig{
		MaxAttempts:  3,
		InitialDelay: time.Microsecond,
		MaxDelay:     time.Millisecond,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if calls != 3 {
		t.Errorf("calls = %d, want 3 (MaxAttempts)", calls)
	}
	if resp.StatusCode != 503 {
		t.Errorf("final status = %d, want 503", resp.StatusCode)
	}
}

// TestDoWithRetryDoesNotRetryOn400 confirms that 4xx (non-429/408) are not retried.
func TestDoWithRetryDoesNotRetryOn400(t *testing.T) {
	calls := 0
	client := clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(400, `{}`), nil
	})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://test/", nil)
	resp, err := DoWithRetry(context.Background(), client, req, RetryConfig{MaxAttempts: 3})
	if err != nil {
		t.Fatalf("DoWithRetry: %v", err)
	}
	defer resp.Body.Close()
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry on 400)", calls)
	}
}

// TestDoWithRetryRetriesOn429 confirms that 429 triggers a retry.
func TestDoWithRetryRetriesOn429(t *testing.T) {
	calls := 0
	client := clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return jsonResponse(429, `{}`), nil
		}
		return jsonResponse(200, `{}`), nil
	})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://test/", nil)
	resp, err := DoWithRetry(context.Background(), client, req, RetryConfig{
		MaxAttempts:  3,
		InitialDelay: time.Microsecond,
		MaxDelay:     time.Millisecond,
	})
	if err != nil {
		t.Fatalf("DoWithRetry: %v", err)
	}
	defer resp.Body.Close()
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestDoWithRetryCapsProviderRetryAfter(t *testing.T) {
	calls := 0
	client := clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		resp := jsonResponse(200, `{}`)
		if calls == 1 {
			resp = jsonResponse(429, `{}`)
			resp.Header.Set("Retry-After", "3600")
		}
		return resp, nil
	})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://test/", nil)
	started := time.Now()
	resp, err := DoWithRetry(context.Background(), client, req, RetryConfig{
		MaxAttempts: 2, InitialDelay: time.Microsecond, MaxDelay: time.Millisecond, MaxRetryAfter: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if calls != 2 || time.Since(started) > 250*time.Millisecond {
		t.Fatalf("calls=%d elapsed=%s", calls, time.Since(started))
	}
}

// TestDoWithRetryContextCancelled confirms that a cancelled context stops
// retries immediately.
func TestDoWithRetryContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	client := clientWithRoundTripper(func(r *http.Request) (*http.Response, error) {
		return nil, context.Canceled
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://test/", nil)
	_, err := DoWithRetry(ctx, client, req, RetryConfig{MaxAttempts: 3})
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}

// ---------------------------------------------------------------------------
// SharedTransport / SharedHTTPClient
// ---------------------------------------------------------------------------

func TestSharedTransportNonNil(t *testing.T) {
	tr := SharedTransport()
	if tr == nil {
		t.Fatal("SharedTransport returned nil")
	}
}

func TestSharedTransportIsSingleton(t *testing.T) {
	t1 := SharedTransport()
	t2 := SharedTransport()
	if t1 != t2 {
		t.Error("SharedTransport should return the same instance every call")
	}
}

func TestSharedTransportTuning(t *testing.T) {
	tr := SharedTransport()
	if tr.MaxIdleConnsPerHost != 64 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 64", tr.MaxIdleConnsPerHost)
	}
	if tr.MaxConnsPerHost != 0 {
		t.Errorf("MaxConnsPerHost = %d, want 0 (unlimited)", tr.MaxConnsPerHost)
	}
}

func TestSharedHTTPClientWithTimeout(t *testing.T) {
	c := SharedHTTPClient(30 * time.Second)
	if c == nil {
		t.Fatal("SharedHTTPClient returned nil")
	}
	if c.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want 30s", c.Timeout)
	}
}

func TestSharedHTTPClientZeroTimeoutIsAllowed(t *testing.T) {
	c := SharedHTTPClient(0)
	if c == nil {
		t.Fatal("SharedHTTPClient(0) returned nil")
	}
	if c.Timeout != 0 {
		t.Errorf("Timeout = %v, want 0", c.Timeout)
	}
}

// ---------------------------------------------------------------------------
// EmbedderRegistry
// ---------------------------------------------------------------------------

type fakeEmbedder struct{ id string }

func (f *fakeEmbedder) ID() string { return f.id }
func (f *fakeEmbedder) Embed(_ context.Context, _ string, _ []string) ([][]float32, error) {
	return nil, nil
}
func (f *fakeEmbedder) Dim(_ context.Context, _ string) (int, error) { return 0, nil }

func TestEmbedderRegistryGetReturnsNilForUnknown(t *testing.T) {
	reg := NewEmbedderRegistry()
	if reg.Get("nobody") != nil {
		t.Error("Get('nobody') should return nil")
	}
}

func TestEmbedderRegistryRegisterAndGet(t *testing.T) {
	reg := NewEmbedderRegistry()
	fe := &fakeEmbedder{id: "emb-a"}
	reg.Register(fe)

	got := reg.Get("emb-a")
	if got == nil {
		t.Fatal("Get('emb-a') returned nil after Register")
	}
	if got.ID() != "emb-a" {
		t.Errorf("ID = %q, want emb-a", got.ID())
	}
}

func TestEmbedderRegistryIDsEmpty(t *testing.T) {
	reg := NewEmbedderRegistry()
	if ids := reg.IDs(); len(ids) != 0 {
		t.Errorf("IDs on empty registry = %v, want []", ids)
	}
}

func TestEmbedderRegistryIDsAfterRegister(t *testing.T) {
	reg := NewEmbedderRegistry()
	reg.Register(&fakeEmbedder{id: "x"})
	reg.Register(&fakeEmbedder{id: "y"})

	ids := reg.IDs()
	if len(ids) != 2 {
		t.Errorf("IDs count = %d, want 2", len(ids))
	}
}

func TestEmbedderRegistryRegisterOverwrites(t *testing.T) {
	reg := NewEmbedderRegistry()
	reg.Register(&fakeEmbedder{id: "same"})
	reg.Register(&fakeEmbedder{id: "same"}) // overwrite

	ids := reg.IDs()
	if len(ids) != 1 {
		t.Errorf("IDs count after overwrite = %d, want 1", len(ids))
	}
}

// ---------------------------------------------------------------------------
// CompletionRequest / CompletionResponse zero-value sanity
// ---------------------------------------------------------------------------

func TestCompletionRequestZeroValueIsValid(t *testing.T) {
	var req CompletionRequest
	// Registering a provider and calling Complete with a zero-value request
	// should not panic — providers must handle empty messages gracefully.
	r := NewRouter("fp")
	r.Register(&fakeProvider{id: "fp"})
	resp, err := r.Complete(context.Background(), "fp", req)
	if err != nil {
		t.Fatalf("Complete with zero request: %v", err)
	}
	if resp == nil {
		t.Fatal("Complete returned nil response")
	}
}

func TestCompletionResponseZeroValueFields(t *testing.T) {
	var resp CompletionResponse
	if resp.Content != "" {
		t.Errorf("Content should be empty string by default")
	}
	if resp.Stream != nil {
		t.Errorf("Stream channel should be nil by default")
	}
	if resp.InputTokens != 0 || resp.OutputTokens != 0 {
		t.Errorf("token counts should default to 0")
	}
}

// ---------------------------------------------------------------------------
// ChatMessage / ToolSchema zero-value sanity
// ---------------------------------------------------------------------------

func TestChatMessageFields(t *testing.T) {
	m := ChatMessage{Role: "user", Content: "hello"}
	if m.Role != "user" || m.Content != "hello" {
		t.Error("ChatMessage fields not set correctly")
	}
	if m.ToolCalls != nil {
		t.Error("ToolCalls should be nil by default")
	}
}

func TestToolSchemaFields(t *testing.T) {
	ts := ToolSchema{Name: "add", Description: "adds numbers", Parameters: map[string]any{"type": "object"}}
	if ts.Name != "add" {
		t.Errorf("Name = %q, want add", ts.Name)
	}
}
