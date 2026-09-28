package opennotebookmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestValidateBaseURLLoopbackBoundary(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:5055", "http://localhost:5055", "https://[::1]:5055"} {
		if _, err := ValidateBaseURL(raw); err != nil {
			t.Errorf("ValidateBaseURL(%q): %v", raw, err)
		}
	}
	for _, raw := range []string{"https://notebooks.example.com", "http://192.168.1.8:5055", "http://user:pass@localhost:5055", "file:///tmp/notebook"} {
		if _, err := ValidateBaseURL(raw); err == nil {
			t.Errorf("ValidateBaseURL(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestValidateAudioConfiguration(t *testing.T) {
	for _, raw := range []string{"https://mac.tailnet.ts.net:8443", "http://192.168.1.8:18791/media"} {
		if _, err := ValidateAudioBaseURL(raw); err != nil {
			t.Errorf("ValidateAudioBaseURL(%q): %v", raw, err)
		}
	}
	for _, raw := range []string{"file:///tmp/audio", "https://user:pass@example.com", "https://example.com?token=secret"} {
		if _, err := ValidateAudioBaseURL(raw); err == nil {
			t.Errorf("ValidateAudioBaseURL(%q) unexpectedly succeeded", raw)
		}
	}
	for _, address := range []string{"127.0.0.1:18791", "localhost:18791", "[::1]:18791"} {
		if err := ValidateAudioListenAddress(address); err != nil {
			t.Errorf("ValidateAudioListenAddress(%q): %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:18791", ":18791", "192.168.1.8:18791", "bad"} {
		if err := ValidateAudioListenAddress(address); err == nil {
			t.Errorf("ValidateAudioListenAddress(%q) unexpectedly succeeded", address)
		}
	}
}

func TestPodcastAudioUsesClientFacingBaseURL(t *testing.T) {
	srv, err := NewWithAudioBaseURL(DefaultBaseURL, "https://mac.tailnet.ts.net:8443/media", "", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := srv.execute(context.Background(), "open_notebook_get_podcast_audio", map[string]any{"episode_id": "episode:abc"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(result, []byte(`"audio_url":"https://mac.tailnet.ts.net:8443/media/api/podcasts/episodes/episode:abc/audio"`)) {
		t.Fatalf("result = %s", result)
	}
}

func TestAudioHandlerOnlyProxiesPodcastAudio(t *testing.T) {
	var gotMethod, gotRange, gotAuthorization string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotRange = r.Header.Get("Range")
		gotAuthorization = r.Header.Get("Authorization")
		if r.URL.Path != "/api/podcasts/episodes/episode:abc/audio" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("Content-Range", "bytes 0-3/8")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("test"))
	}))
	defer api.Close()
	srv, err := NewWithAudioBaseURL(api.URL, "https://mac.tailnet.ts.net:8443", "secret", "test", api.Client())
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/podcasts/episodes/episode:abc/audio", nil)
	req.Header.Set("Range", "bytes=0-3")
	rec := httptest.NewRecorder()
	srv.AudioHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "test" {
		t.Fatalf("response = %d %q", rec.Code, rec.Body.String())
	}
	if gotMethod != http.MethodGet || gotRange != "bytes=0-3" || gotAuthorization != "Bearer secret" {
		t.Fatalf("upstream request = method %q range %q auth %q", gotMethod, gotRange, gotAuthorization)
	}
	if rec.Header().Get("Content-Type") != "audio/mpeg" || rec.Header().Get("Content-Range") != "bytes 0-3/8" {
		t.Fatalf("headers = %#v", rec.Header())
	}

	for _, requestPath := range []string{"/health", "/api/notebooks", "/api/podcasts/episodes/episode:abc", "/api/podcasts/episodes/x/y/audio"} {
		rec := httptest.NewRecorder()
		srv.AudioHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, requestPath, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", requestPath, rec.Code)
		}
	}
}

func TestMCPInitializeAndToolDiscovery(t *testing.T) {
	srv, err := New(DefaultBaseURL, "", "v-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	initData, ok := srv.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`))
	if !ok || !bytes.Contains(initData, []byte(`"protocolVersion":"2025-06-18"`)) {
		t.Fatalf("initialize = %s", initData)
	}
	listData, ok := srv.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	if !ok {
		t.Fatal("tools/list returned no response")
	}
	var response struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(listData, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Result.Tools) < 20 {
		t.Fatalf("tool count = %d, want at least 20", len(response.Result.Tools))
	}
	seen := map[string]bool{}
	for _, tool := range response.Result.Tools {
		seen[tool.Name] = true
		if strings.Contains(tool.Name, "delete") {
			t.Fatalf("destructive tool exposed: %s", tool.Name)
		}
	}
	for _, required := range []string{"open_notebook_add_text_source", "open_notebook_search", "open_notebook_ask", "open_notebook_generate_podcast"} {
		if !seen[required] {
			t.Errorf("missing tool %s", required)
		}
	}
}

func TestRepresentativeOpenNotebookCalls(t *testing.T) {
	var requests []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&data)
		typeName, _ := data["type"].(string)
		requests = append(requests, fmt.Sprintf("%s %s %s", r.Method, r.URL.RequestURI(), typeName))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/notebooks":
			_, _ = w.Write([]byte(`[{"id":"notebook:1","name":"Research"}]`))
		case "/api/sources/json":
			_, _ = w.Write([]byte(`{"id":"source:1","status":"processing"}`))
		case "/api/search":
			_, _ = w.Write([]byte(`[{"title":"Finding"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	srv, err := New(api.URL, "", "test", api.Client())
	if err != nil {
		t.Fatal(err)
	}

	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{"open_notebook_list_notebooks", nil},
		{"open_notebook_add_text_source", map[string]any{"content": "HBR article text", "notebooks": []any{"notebook:1"}}},
		{"open_notebook_search", map[string]any{"query": "strategy", "limit": float64(5)}},
	} {
		params, _ := json.Marshal(map[string]any{"name": call.name, "arguments": call.args})
		result := srv.callTool(context.Background(), params)
		if result["isError"] != false {
			t.Fatalf("%s failed: %#v", call.name, result)
		}
	}
	want := []string{"GET /api/notebooks ", "POST /api/sources/json text", "POST /api/search "}
	if fmt.Sprint(requests) != fmt.Sprint(want) {
		t.Fatalf("requests = %v, want %v", requests, want)
	}
}

func TestGeneratePodcastResolvesProfileIDsToNames(t *testing.T) {
	var submitted map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/episode-profiles":
			_, _ = w.Write([]byte(`[{"id":"episode_profile:episode-1","name":"Tech Discussion"}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/speaker-profiles":
			_, _ = w.Write([]byte(`[{"id":"speaker_profile:speaker-1","name":"Solo Expert"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/podcasts/generate":
			if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil {
				t.Errorf("decode podcast request: %v", err)
			}
			_, _ = w.Write([]byte(`{"job_id":"command:1","status":"submitted"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	srv, err := New(api.URL, "", "test", api.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := srv.execute(context.Background(), "open_notebook_generate_podcast", map[string]any{
		"episode_profile": "episode_profile:episode-1",
		"speaker_profile": "speaker_profile:speaker-1",
		"episode_name":    "MCP Test",
		"content":         "Source content",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(result, []byte(`"job_id":"command:1"`)) {
		t.Fatalf("result = %s", result)
	}
	if got := stringValue(submitted["episode_profile"]); got != "Tech Discussion" {
		t.Fatalf("episode_profile = %q, want %q", got, "Tech Discussion")
	}
	if got := stringValue(submitted["speaker_profile"]); got != "Solo Expert" {
		t.Fatalf("speaker_profile = %q, want %q", got, "Solo Expert")
	}
}

func TestListPodcastEpisodesReturnsCompactMetadata(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/podcasts/episodes" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{
			"id":"episode:1","name":"Daily Brief","created":"2026-09-28T12:00:00Z",
			"job_status":"completed","error_message":"","audio_file":"episodes/1/audio.mp3",
			"transcript":{"transcript":[{"speaker":"Host","dialogue":"large transcript"}]},
			"outline":{"segments":[{"name":"large outline"}]},
			"episode_profile":{"name":"Tech Discussion"},"speaker_profile":{"name":"Tech Discussion"}
		}]`))
	}))
	defer api.Close()

	srv, err := New(api.URL, "", "test", api.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := srv.execute(context.Background(), "open_notebook_list_podcast_episodes", nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(result, []byte("transcript")) || bytes.Contains(result, []byte("outline")) || bytes.Contains(result, []byte("episode_profile")) {
		t.Fatalf("compact list leaked expanded episode data: %s", result)
	}
	for _, want := range [][]byte{[]byte(`"id":"episode:1"`), []byte(`"job_status":"completed"`), []byte(`"audio_available":true`)} {
		if !bytes.Contains(result, want) {
			t.Fatalf("compact list %s does not contain %s", result, want)
		}
	}
}

func TestGetPodcastJobWaitsForTerminalStatus(t *testing.T) {
	statuses := []string{"running", "running", "completed"}
	requests := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/podcasts/jobs/command:1" {
			http.NotFound(w, r)
			return
		}
		status := statuses[requests]
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"job_id":"command:1","status":%q,"result":{"episode_id":"episode:1","transcript":{"transcript":[{"dialogue":"large transcript"}]},"outline":{"segments":[{"name":"large outline"}]}}}`, status)
	}))
	defer api.Close()

	srv, err := New(api.URL, "", "test", api.Client())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(0, 0)
	srv.now = func() time.Time { return now }
	srv.sleep = func(_ context.Context, delay time.Duration) error {
		now = now.Add(delay)
		return nil
	}
	result, err := srv.execute(context.Background(), "open_notebook_get_podcast_job", map[string]any{
		"job_id": "command:1", "wait_seconds": float64(10), "poll_interval_seconds": float64(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 3 || !bytes.Contains(result, []byte(`"status":"completed"`)) {
		t.Fatalf("requests = %d, result = %s", requests, result)
	}
	if bytes.Contains(result, []byte("transcript")) || bytes.Contains(result, []byte("outline")) {
		t.Fatalf("compact job leaked expanded episode data: %s", result)
	}
}

func TestGetPodcastJobReturnsFailedImmediately(t *testing.T) {
	requests := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"job_id":"command:1","status":"failed","error_message":"voice failed"}`))
	}))
	defer api.Close()
	srv, _ := New(api.URL, "", "test", api.Client())
	result, err := srv.execute(context.Background(), "open_notebook_get_podcast_job", map[string]any{"job_id": "command:1", "wait_seconds": float64(30)})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || !bytes.Contains(result, []byte(`"status":"failed"`)) {
		t.Fatalf("requests = %d, result = %s", requests, result)
	}
}

func TestGetPodcastJobMarksWaitTimeout(t *testing.T) {
	requests := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"job_id":"command:1","status":"running"}`))
	}))
	defer api.Close()
	srv, _ := New(api.URL, "", "test", api.Client())
	now := time.Unix(0, 0)
	srv.now = func() time.Time { return now }
	srv.sleep = func(_ context.Context, delay time.Duration) error {
		now = now.Add(delay)
		return nil
	}
	result, err := srv.execute(context.Background(), "open_notebook_get_podcast_job", map[string]any{
		"job_id": "command:1", "wait_seconds": float64(5), "poll_interval_seconds": float64(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 4 || !bytes.Contains(result, []byte(`"wait_timed_out":true`)) || !bytes.Contains(result, []byte(`"waited_seconds":5`)) {
		t.Fatalf("requests = %d, result = %s", requests, result)
	}
}

func TestGetPodcastJobWaitHonorsCancellation(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"job_id":"command:1","status":"running"}`))
	}))
	defer api.Close()
	srv, _ := New(api.URL, "", "test", api.Client())
	ctx, cancel := context.WithCancel(context.Background())
	srv.sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}
	_, err := srv.execute(ctx, "open_notebook_get_podcast_job", map[string]any{"job_id": "command:1", "wait_seconds": float64(30)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestAPIFailureIsBoundedAndSanitized(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"detail":"bad input","internal":"secret-value"}`))
	}))
	defer api.Close()
	srv, _ := New(api.URL, "super-secret-token", "test", api.Client())
	_, err := srv.execute(context.Background(), "open_notebook_create_notebook", map[string]any{"name": "x"})
	if err == nil {
		t.Fatal("expected API error")
	}
	if got := err.Error(); got != "the Open Notebook API returned HTTP 422: bad input" {
		t.Fatalf("error = %q", got)
	}
}

func TestURLSourceRejectsPrivateDestinations(t *testing.T) {
	srv, _ := New(DefaultBaseURL, "", "test", nil)
	_, err := srv.execute(context.Background(), "open_notebook_add_url_source", map[string]any{"url": "http://127.0.0.1/private"})
	if err == nil || !strings.Contains(err.Error(), "public HTTP(S) address") {
		t.Fatalf("error = %v", err)
	}
}

func TestServeUsesNewlineDelimitedJSONRPC(t *testing.T) {
	srv, _ := New(DefaultBaseURL, "", "test", nil)
	in := strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n")
	var out bytes.Buffer
	if err := srv.Serve(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n" {
		t.Fatalf("output = %q", got)
	}
}
