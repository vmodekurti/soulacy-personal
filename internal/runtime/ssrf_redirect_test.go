package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// The SSRF check was pre-flight only, and that made it decorative.
//
// checkSSRF resolved the hostname the model supplied, found a public IP, and
// returned nil. The tool then handed the URL to a bare &http.Client{}, whose
// default policy follows up to 10 redirects — with nothing re-validating the
// target. A hostname the attacker controls only has to answer
// `302 Location: http://169.254.169.254/latest/meta-data/iam/security-credentials/`
// and the instance role credentials come back as the tool result, straight into
// the model's context.
//
// 127.0.0.1 (the httptest server) is deliberately in the always-allowed set, so
// the first hop passing proves the pre-flight check is not what stops this.
func TestFetchURL_RedirectToMetadataEndpointIsBlocked(t *testing.T) {
	for _, tool := range []string{"fetch_url", "http_request", "download_file"} {
		t.Run(tool, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/iam/security-credentials/", http.StatusFound)
			}))
			defer srv.Close()

			e := newMinimalEngine(t)
			e.SetSSRF(false, nil) // the metadata endpoint is blocked either way
			th := systemTool(t, e, tool)

			args := map[string]any{"url": srv.URL}
			switch tool {
			case "http_request":
				args["method"] = "GET"
			case "download_file":
				args["dest_path"] = t.TempDir() + "/out.bin"
			}
			out, err := th.Handler(context.Background(), args)
			if err == nil {
				t.Fatalf("the redirect to the metadata endpoint was followed; tool returned: %s", out)
			}
			if !strings.Contains(strings.ToLower(err.Error()), "ssrf") {
				t.Fatalf("request failed, but not because of the SSRF guard — that may be luck rather than a rule: %v", err)
			}
		})
	}
}

// The counterpart: an ordinary redirect between two allowed hosts must still
// work, or the fix has simply broken redirects.
func TestFetchURL_OrdinaryRedirectStillFollowed(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("arrived"))
	}))
	defer final.Close()
	hop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusFound)
	}))
	defer hop.Close()

	e := newMinimalEngine(t)
	e.SetSSRF(false, nil)
	th := systemTool(t, e, "fetch_url")
	out, err := th.Handler(context.Background(), map[string]any{"url": hop.URL})
	if err != nil {
		t.Fatalf("a benign redirect between two loopback hosts was refused: %v", err)
	}
	if !strings.Contains(out, "arrived") {
		t.Fatalf("redirect was not followed to the end: %q", out)
	}
}

// download_file streamed straight into io.Copy with no bound. The only limit was
// the client's 5-minute timeout, which on a fast link is tens of gigabytes onto
// the host disk — and the URL comes from model output, so a prompt-injected page
// picks the target. fetch_url and http_request in the same file both cap.
func TestDownloadFile_StopsAtTheSizeCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 64*1024)
		for i := 0; i < 64; i++ { // 4 MB, well past the cap set below
			_, _ = w.Write(chunk)
		}
	}))
	defer srv.Close()

	restore := maxDownloadBytes
	maxDownloadBytes = 256 * 1024
	defer func() { maxDownloadBytes = restore }()

	e := newMinimalEngine(t)
	e.SetSSRF(false, nil)
	dest := t.TempDir() + "/out.bin"
	_, err := systemTool(t, e, "download_file").Handler(context.Background(), map[string]any{
		"url": srv.URL, "dest_path": dest,
	})
	if err == nil {
		t.Fatal("a response far past the cap was written to disk without complaint")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Fatalf("download failed, but not because of the cap: %v", err)
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Error("the partial file was left on disk after the cap was hit")
	}
}
