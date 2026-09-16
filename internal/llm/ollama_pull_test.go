package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newPullServer(t *testing.T, body string, code int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/pull" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
}

func TestPullModelReportsProgressThroughEveryPhase(t *testing.T) {
	// A real pull: manifest first with no byte counts, then layer transfer
	// with counts, then verification and success.
	stream := strings.Join([]string{
		`{"status":"pulling manifest"}`,
		`{"status":"pulling 8eeb52dfb3bb","completed":1024,"total":4096}`,
		`{"status":"pulling 8eeb52dfb3bb","completed":4096,"total":4096}`,
		`{"status":"verifying sha256 digest"}`,
		`{"status":"success"}`,
	}, "\n")
	srv := newPullServer(t, stream, http.StatusOK)
	defer srv.Close()

	p := &OllamaProvider{baseURL: srv.URL, client: srv.Client()}
	var seen []PullProgress
	if err := p.PullModel(context.Background(), "gemma4:latest", func(pr PullProgress) {
		seen = append(seen, pr)
	}); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(seen) != 5 {
		t.Fatalf("want 5 progress steps, got %d: %+v", len(seen), seen)
	}
	if seen[0].Total != 0 {
		t.Error("the manifest phase has no byte total; a caller must not read that as 0%")
	}
	if seen[2].Completed != 4096 || seen[2].Total != 4096 {
		t.Errorf("byte counts should pass through unchanged; got %+v", seen[2])
	}
	if seen[len(seen)-1].Status != "success" {
		t.Errorf("last status = %q, want success", seen[len(seen)-1].Status)
	}
}

// Ollama reports a bad model name in-band with HTTP 200. Missing that would
// leave the dashboard showing a pull that appears to be running forever.
func TestPullModelSurfacesAnInBandError(t *testing.T) {
	stream := `{"status":"pulling manifest"}` + "\n" + `{"error":"model \"nope:9000b\" not found"}`
	srv := newPullServer(t, stream, http.StatusOK)
	defer srv.Close()

	p := &OllamaProvider{baseURL: srv.URL, client: srv.Client()}
	err := p.PullModel(context.Background(), "nope:9000b", nil)
	if err == nil {
		t.Fatal("an in-band error must fail the pull")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should carry Ollama's reason; got %v", err)
	}
}

func TestPullModelFailsOnHTTPError(t *testing.T) {
	srv := newPullServer(t, "upstream exploded", http.StatusInternalServerError)
	defer srv.Close()

	p := &OllamaProvider{baseURL: srv.URL, client: srv.Client()}
	if err := p.PullModel(context.Background(), "gemma4:latest", nil); err == nil {
		t.Fatal("a non-200 must fail")
	}
}

func TestPullModelRejectsAnEmptyName(t *testing.T) {
	p := &OllamaProvider{baseURL: "http://127.0.0.1:1", client: http.DefaultClient}
	if err := p.PullModel(context.Background(), "   ", nil); err == nil {
		t.Fatal("an empty model name should fail before any request")
	}
}

// A pull runs for minutes; the caller's context is the only cancellation.
func TestPullModelStopsWhenTheContextIsCancelled(t *testing.T) {
	srv := newPullServer(t, `{"status":"pulling manifest"}`, http.StatusOK)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &OllamaProvider{baseURL: srv.URL, client: srv.Client()}
	if err := p.PullModel(ctx, "gemma4:latest", nil); err == nil {
		t.Fatal("a cancelled context should abort the pull")
	}
}

// Garbage in the stream is not a reason to abandon a download that is working.
func TestPullModelIgnoresUnparseableLines(t *testing.T) {
	stream := strings.Join([]string{
		`{"status":"pulling manifest"}`,
		`not json at all`,
		`{"status":"success"}`,
	}, "\n")
	srv := newPullServer(t, stream, http.StatusOK)
	defer srv.Close()

	p := &OllamaProvider{baseURL: srv.URL, client: srv.Client()}
	var n int
	if err := p.PullModel(context.Background(), "gemma4:latest", func(PullProgress) { n++ }); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if n != 2 {
		t.Errorf("want 2 understood steps, got %d", n)
	}
}
