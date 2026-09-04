package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A JSON response must come back as the RAW JSON body (no "URL:/Status:/
// Content-Type:" preamble), so a downstream python json.loads / template parses
// it directly. Regression for the stock-data flow where the header preamble made
// json.loads fail at char 0.
func TestFetchURL_JSONReturnedClean(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"chart":{"result":[{"meta":{"regularMarketPrice":314.48}}]}}`))
	}))
	defer srv.Close()

	e := newMinimalEngine(t)
	e.SetSSRF(false, nil) // allow loopback test server
	tool := systemTool(t, e, "fetch_url")
	out, err := tool.Handler(context.Background(), map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatalf("fetch_url: %v", err)
	}
	s := strings.TrimSpace(out)
	if !strings.HasPrefix(s, "{") {
		t.Fatalf("JSON body should be returned clean, got: %q", s)
	}
	if strings.Contains(s, "Status:") || strings.Contains(s, "Content-Type:") {
		t.Fatalf("header preamble must not be prepended to JSON, got: %q", s)
	}
}

func TestFetchURL_AcceptsURLAliases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("alias-ok"))
	}))
	defer srv.Close()

	e := newMinimalEngine(t)
	e.SetSSRF(false, nil)
	tool := systemTool(t, e, "fetch_url")
	out, err := tool.Handler(context.Background(), map[string]any{"link": srv.URL})
	if err != nil {
		t.Fatalf("fetch_url: %v", err)
	}
	if !strings.Contains(out, "alias-ok") {
		t.Fatalf("expected fetched body via link alias, got: %q", out)
	}
}

// A non-JSON (HTML/text) response keeps the informative header block.
func TestFetchURL_NonJSONKeepsHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>hello</html>"))
	}))
	defer srv.Close()

	e := newMinimalEngine(t)
	e.SetSSRF(false, nil)
	tool := systemTool(t, e, "fetch_url")
	out, err := tool.Handler(context.Background(), map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatalf("fetch_url: %v", err)
	}
	if !strings.Contains(out, "Status:") {
		t.Fatalf("non-JSON should keep header context, got: %q", out)
	}
}
