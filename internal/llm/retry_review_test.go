package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRetryExhaustionPreservesProviderErrorBody(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		io.WriteString(w, `{"error":"capacity unavailable"}`)
	}))
	defer s.Close()
	r, _ := http.NewRequestWithContext(context.Background(), "GET", s.URL, nil)
	resp, err := DoWithRetry(context.Background(), s.Client(), r, RetryConfig{MaxAttempts: 2, InitialDelay: time.Microsecond, MaxDelay: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(body), "capacity unavailable") {
		t.Fatalf("provider error body lost: %s %v", body, err)
	}
}
