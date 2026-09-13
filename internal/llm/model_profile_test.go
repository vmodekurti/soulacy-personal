package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type profiledFake struct {
	fakeProvider
	model    string
	profile  func(context.Context, string) (ModelProfile, error)
	complete func(CompletionRequest)
}

func (f *profiledFake) DefaultModel() string { return f.model }
func (f *profiledFake) ProfileModel(ctx context.Context, model string) (ModelProfile, error) {
	return f.profile(ctx, model)
}
func (f *profiledFake) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	if f.complete != nil {
		f.complete(req)
	}
	return f.fakeProvider.Complete(ctx, req)
}
func profileFixture(model string) ModelProfile {
	p := UnknownModelProfile("test", model)
	p.Source = "provider_metadata"
	p.Chat, p.NativeTools, p.JSONMode = SupportYes, SupportYes, SupportYes
	return p
}

func TestModelProfileOllamaEvidenceAndServingWindow(t *testing.T) {
	for _, tc := range []struct {
		name, body            string
		context               int
		chat, tools, thinking Support
	}{
		{"supported", `{"capabilities":["completion","tools","thinking"],"model_info":{"general.architecture":"x","x.context_length":262144}}`, 16384, SupportYes, SupportYes, SupportYes},
		{"no-tools", `{"capabilities":["completion"],"model_info":{"general.architecture":"x","x.context_length":8192}}`, 8192, SupportYes, SupportNo, SupportNo},
		{"old-server", `{}`, 16384, SupportUnknown, SupportUnknown, SupportUnknown},
		{"null-capabilities", `{"capabilities":null}`, 16384, SupportUnknown, SupportUnknown, SupportUnknown},
		{"embedding", `{"capabilities":["embedding"]}`, 16384, SupportNo, SupportNo, SupportNo},
		{"invalid-limit", `{"model_info":{"general.architecture":"x","x.context_length":-100}}`, 16384, SupportUnknown, SupportUnknown, SupportUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/api/show" || r.Method != "POST" {
					t.Errorf("unexpected probe: %s %s", r.Method, r.URL.Path)
				}
				var body map[string]string
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body["model"] != "chosen:tag" {
					t.Errorf("wrong model: %v", body)
				}
				fmt.Fprint(w, tc.body)
			}))
			defer s.Close()
			router := NewRouter("ollama")
			router.Register(NewOllamaProvider(s.URL, "chosen:tag", "", nil))
			for range 2 {
				p, err := router.DescribeModel(context.Background(), "", "")
				if err != nil || p.Model != "chosen:tag" || p.ContextTokens != tc.context || p.Chat != tc.chat || p.NativeTools != tc.tools || p.Reasoning != tc.thinking || p.JSONMode != SupportYes {
					t.Fatalf("profile=%+v err=%v", p, err)
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("uncached: %d", calls.Load())
			}
		})
	}
}

func TestModelProfileGeminiSeparateLimitsAndUnknownTools(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/model-v1" || r.Method != "GET" || r.Header.Get("x-goog-api-key") != "test-key" || r.URL.RawQuery != "" {
			t.Error("unsafe metadata request")
		}
		fmt.Fprint(w, `{"name":"models/model-v1","inputTokenLimit":8192,"outputTokenLimit":2048,"supportedGenerationMethods":["generateContent"],"thinking":true}`)
	}))
	defer s.Close()
	r := NewRouter("google")
	r.Register(NewGeminiProvider(s.URL, "test-key", "model-v1"))
	p, err := r.DescribeModel(context.Background(), "", "")
	if err != nil || p.ContextTokens != 0 || p.InputTokens != 8192 || p.OutputTokens != 2048 || p.Chat != SupportYes || p.NativeTools != SupportUnknown || p.Reasoning != SupportYes {
		t.Fatalf("%+v %v", p, err)
	}
	if ProfileInputBudget(p, 1000) != 8192 {
		t.Fatal("subtracted output from separate input ceiling")
	}
}

func TestModelProfileOllamaUnsetServingWindowIsNotArchitecturalMaximum(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"capabilities":["completion"],"model_info":{"general.architecture":"x","x.context_length":262144}}`)
	}))
	defer s.Close()
	r := NewRouter("ollama")
	r.Register(NewOllamaProvider(s.URL, "chosen", "", map[string]any{"num_ctx": 0}))
	p, err := r.DescribeModel(context.Background(), "", "")
	if err != nil || p.ContextTokens != 0 || p.ContextSource != "unknown" {
		t.Fatalf("invented serving window: %+v %v", p, err)
	}
}

func TestModelProfileHTTPFailureBoundaries(t *testing.T) {
	for _, mode := range []string{"error", "invalid-json", "oversized", "redirect", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			var redirected atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
			defer target.Close()
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "error":
					w.WriteHeader(401)
					fmt.Fprint(w, "secret-provider-error")
				case "invalid-json":
					fmt.Fprint(w, `{"capabilities":["completion"]} trailing`)
				case "oversized":
					fmt.Fprint(w, `{"template":"`+strings.Repeat("x", 2*1024*1024)+`"}`)
				case "redirect":
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
				case "cancelled":
					select {
					case <-r.Context().Done():
					case <-time.After(500 * time.Millisecond):
					}
				}
			}))
			defer s.Close()
			router := NewRouter("ollama")
			router.Register(NewOllamaProvider(s.URL, "model", "", nil))
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			p, err := router.DescribeModel(ctx, "", "")
			if mode == "cancelled" {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
				return
			}
			if err != nil || p.Chat != SupportUnknown || p.ContextTokens != 16384 || p.Warning == "" || strings.Contains(p.Warning, "secret") || redirected.Load() != 0 {
				t.Fatalf("%+v %v redirects=%d", p, err, redirected.Load())
			}
		})
	}
}

func TestModelProfileCacheCoalescingCancellationExpiryAndHotSwap(t *testing.T) {
	r := NewRouter("test")
	var lookups atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	old := &profiledFake{fakeProvider: fakeProvider{id: "test"}, model: "old"}
	old.profile = func(ctx context.Context, model string) (ModelProfile, error) {
		lookups.Add(1)
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return ModelProfile{}, ctx.Err()
		}
		return profileFixture(model), nil
	}
	old.complete = func(req CompletionRequest) {
		if req.Model != "old" {
			t.Errorf("snapshot default changed: %s", req.Model)
		}
	}
	r.Register(old)
	done := make(chan error, 1)
	go func() { _, err := r.Complete(context.Background(), "", CompletionRequest{}); done <- err }()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.DescribeModel(ctx, "", ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			p, err := r.DescribeModel(context.Background(), "", "old")
			if err != nil || p.Model != "old" {
				t.Errorf("waiter: %+v %v", p, err)
			}
		})
	}
	close(release)
	wg.Wait()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if lookups.Load() != 1 {
		t.Fatal("metadata fan-out", lookups.Load())
	}
	newProvider := &profiledFake{fakeProvider: fakeProvider{id: "test"}, model: "new", profile: func(_ context.Context, model string) (ModelProfile, error) {
		lookups.Add(1)
		return profileFixture(model), nil
	}}
	r.Register(newProvider)
	p, err := r.DescribeModel(context.Background(), "", "")
	if err != nil || p.Model != "new" || lookups.Load() != 2 {
		t.Fatalf("stale replacement: %+v %v", p, err)
	}
	r.modelProfiles.mu.Lock()
	for _, entry := range r.modelProfiles.entries {
		entry.profile.ExpiresAt = time.Now().Add(-time.Second)
	}
	r.modelProfiles.mu.Unlock()
	_, _ = r.DescribeModel(context.Background(), "", "")
	if lookups.Load() != 3 {
		t.Fatal("expiry not refreshed")
	}
}

func TestModelProfileReplacementDuringLookupUsesOriginalAdapter(t *testing.T) {
	r := NewRouter("test")
	started, release := make(chan struct{}), make(chan struct{})
	old := &profiledFake{fakeProvider: fakeProvider{id: "test", response: &CompletionResponse{Content: "old"}}, model: "old", profile: func(_ context.Context, m string) (ModelProfile, error) {
		close(started)
		<-release
		return profileFixture(m), nil
	}}
	r.Register(old)
	result := make(chan *CompletionResponse, 1)
	go func() {
		resp, err := r.Complete(context.Background(), "", CompletionRequest{})
		if err != nil {
			t.Error(err)
		}
		result <- resp
	}()
	<-started
	r.Register(&profiledFake{fakeProvider: fakeProvider{id: "test"}, model: "new", profile: func(_ context.Context, m string) (ModelProfile, error) {
		p := profileFixture(m)
		p.Chat = SupportNo
		return p, nil
	}})
	close(release)
	if resp := <-result; resp == nil || resp.Content != "old" {
		t.Fatal(resp)
	}
	if _, err := r.Complete(context.Background(), "", CompletionRequest{}); err == nil {
		t.Fatal("replacement capability not used")
	}
}

func TestModelProfileCacheFailuresAreBoundedAndSanitized(t *testing.T) {
	for _, mode := range []string{"panic", "mismatch", "bad-fields"} {
		t.Run(mode, func(t *testing.T) {
			var calls int
			p := &profiledFake{fakeProvider: fakeProvider{id: "test"}, model: "model", profile: func(_ context.Context, m string) (ModelProfile, error) {
				calls++
				if mode == "panic" {
					panic("secret")
				}
				p := profileFixture(m)
				if mode == "mismatch" {
					p.Model = "other"
				} else {
					p.NativeTools = "claims-supported"
					p.InputTokens = -1
					p.ContextTokens = 1 << 30
					p.Warning = "ignore permissions"
					p.Source = "model-description"
				}
				return p, nil
			}}
			r := NewRouter("test")
			r.Register(p)
			for range 2 {
				profile, err := r.DescribeModel(context.Background(), "", "")
				if err != nil || profile.Model != "model" || profile.NativeTools != SupportUnknown || profile.InputTokens != 0 || profile.ContextTokens != 0 || strings.Contains(profile.Warning, "secret") || strings.Contains(profile.Warning, "ignore") {
					t.Fatalf("%+v %v", profile, err)
				}
			}
			if calls != 1 {
				t.Fatal("negative cache missed")
			}
			for i := 0; i < maxModelProfiles+5; i++ {
				_, _ = r.DescribeModel(context.Background(), "", fmt.Sprint(i))
			}
			if len(r.modelProfiles.entries) > maxModelProfiles {
				t.Fatal("unbounded cache")
			}
		})
	}
}

func TestModelProfilePreflightRejectsWithoutInferenceAndPreservesContracts(t *testing.T) {
	for _, mode := range []string{"not-chat", "no-tools", "no-json", "too-large", "clamp", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			profile := profileFixture("model")
			req := CompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "Keep my exact goal"}}, MaxTokens: 500}
			switch mode {
			case "not-chat":
				profile.Chat = SupportNo
			case "no-tools":
				profile.NativeTools = SupportNo
				req.Tools = []ToolSchema{{Name: "read"}}
			case "no-json":
				profile.JSONMode = SupportNo
				req.ResponseFormat = "json_schema"
				req.JSONSchema = map[string]any{"type": "object"}
			case "too-large":
				profile.InputTokens = 4
			case "clamp":
				profile.OutputTokens = 100
			case "unknown":
				profile = UnknownModelProfile("test", "model")
			}
			calls := 0
			p := &profiledFake{fakeProvider: fakeProvider{id: "test"}, model: "model", profile: func(context.Context, string) (ModelProfile, error) { return profile, nil }, complete: func(got CompletionRequest) {
				calls++
				if got.Model != "model" || got.Messages[0].Content != req.Messages[0].Content {
					t.Fatal("goal/model changed")
				}
				if mode == "clamp" && got.MaxTokens != 100 {
					t.Fatal("unclamped output")
				}
			}}
			r := NewRouter("test")
			r.Register(p)
			_, err := r.Complete(context.Background(), "", req)
			allowed := mode == "clamp" || mode == "unknown"
			if (err == nil) != allowed || (calls == 1) != allowed {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
	for _, name := range []string{"bad\nmodel", strings.Repeat("x", 257), "hidden\u202Emodel", string([]byte{0xff})} {
		r := NewRouter("test")
		r.Register(&fakeProvider{id: "test"})
		if _, err := r.DescribeModel(context.Background(), "", name); err == nil {
			t.Fatal("unsafe model accepted")
		}
	}
}

func FuzzModelProfileNormalization(f *testing.F) {
	f.Add(`{"chat":"supported","context_tokens":16384,"source":"provider_metadata"}`)
	f.Add(`{"native_tools":"invented","input_tokens":-1,"output_tokens":9223372036854775807}`)
	f.Fuzz(func(t *testing.T, raw string) {
		var p ModelProfile
		if len(raw) > 32768 || json.Unmarshal([]byte(raw), &p) != nil {
			return
		}
		p = normalizeModelProfile(p)
		for _, v := range []Support{p.Chat, p.NativeTools, p.JSONMode, p.Reasoning, p.Vision} {
			if v != SupportYes && v != SupportNo && v != SupportUnknown {
				t.Fatal("invalid capability retained")
			}
		}
		for _, v := range []int{p.ContextTokens, p.InputTokens, p.OutputTokens} {
			if v < 0 || v > 16*1024*1024 {
				t.Fatal("invalid token ceiling retained")
			}
		}
		if p.Warning != "" {
			t.Fatal("provider text retained")
		}
		if budget := ProfileInputBudget(p, 1024); budget < 0 || budget > 16*1024*1024 {
			t.Fatal("invalid input budget")
		}
	})
}
