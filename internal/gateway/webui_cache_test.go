package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gofiber/fiber/v2"
)

// guiApp mounts the real cache policy over an in-memory FS. Note the zero
// ModTime: that is not an artifact of the fake, it is exactly what embed.FS
// reports, and it is why Last-Modified is unavailable as a validator here.
func guiApp() *fiber.App {
	fsys := fstest.MapFS{
		"index.html":             {Data: []byte(`<!doctype html><script src="/assets/index-abc123.js"></script>`)},
		"assets/index-abc123.js": {Data: []byte("console.log(1)")},
	}
	app := fiber.New()
	mountStaticGUI(app, http.FS(fsys))
	return app
}

// After an upgrade the browser must pick up the new index.html without a hard
// refresh. "Cache-Control: no-cache" alone does not achieve that: it permits
// storing the response and only requires REVALIDATION, which the browser cannot
// perform without a validator to put in If-None-Match.
func TestIndexHTMLIsRevalidatable(t *testing.T) {
	app := guiApp()

	res, err := app.Test(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", res.StatusCode)
	}
	if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Errorf("Cache-Control = %q, want no-cache so the browser revalidates", cc)
	}

	etag := res.Header.Get("ETag")
	if etag == "" && res.Header.Get("Last-Modified") == "" {
		t.Fatal("index.html has neither ETag nor Last-Modified, so the browser cannot " +
			"make a conditional request and will serve a stale app after an upgrade")
	}
	if etag == "" {
		t.Fatal("no ETag: embed.FS reports a zero ModTime, so Last-Modified cannot be relied on")
	}

	// A validator that is not honoured is decoration.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)
	res2, err := app.Test(req)
	if err != nil {
		t.Fatalf("conditional GET /: %v", err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusNotModified {
		t.Errorf("conditional GET = %d, want 304 — the ETag is not being checked", res2.StatusCode)
	}
}

// The point of the validator is that CHANGED content is re-fetched. A constant
// ETag would pass the test above and still ship a stale GUI.
func TestChangedIndexHTMLGetsANewETag(t *testing.T) {
	first := guiApp()
	res, _ := first.Test(httptest.NewRequest(http.MethodGet, "/", nil))
	defer res.Body.Close()
	oldETag := res.Header.Get("ETag")

	// A rebuilt GUI: same path, different bundle hash inside.
	rebuilt := fiber.New()
	mountStaticGUI(rebuilt, http.FS(fstest.MapFS{
		"index.html": {Data: []byte(`<!doctype html><script src="/assets/index-NEWHASH.js"></script>`)},
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", oldETag)
	res2, err := rebuilt.Test(req)
	if err != nil {
		t.Fatalf("conditional GET after rebuild: %v", err)
	}
	defer res2.Body.Close()
	if res2.StatusCode == http.StatusNotModified {
		t.Fatal("rebuilt index.html still answered 304 — the upgrade would not reach the user")
	}
	if got := res2.Header.Get("ETag"); got == oldETag {
		t.Errorf("ETag unchanged (%s) despite different content", got)
	}
}

// Hashed assets are immutable and must stay cacheable forever; adding the
// validator must not make every asset revalidate on each page load.
func TestHashedAssetsStayImmutable(t *testing.T) {
	res, err := guiApp().Test(httptest.NewRequest(http.MethodGet, "/assets/index-abc123.js", nil))
	if err != nil {
		t.Fatalf("GET asset: %v", err)
	}
	defer res.Body.Close()
	cc := res.Header.Get("Cache-Control")
	if !strings.Contains(cc, "immutable") || !strings.Contains(cc, "max-age=31536000") {
		t.Errorf("asset Cache-Control = %q, want a long immutable policy", cc)
	}
	if strings.Contains(cc, "no-cache") {
		t.Errorf("asset marked no-cache (%q) — hashed assets should never revalidate", cc)
	}
}

// The SPA fallback must still work, and must not be cached as if it were the
// asset the user asked for.
func TestUnknownRouteFallsBackToIndexUncached(t *testing.T) {
	res, err := guiApp().Test(httptest.NewRequest(http.MethodGet, "/studio", nil))
	if err != nil {
		t.Fatalf("GET /studio: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("SPA fallback returned %d, want 200", res.StatusCode)
	}
	if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Errorf("SPA fallback Cache-Control = %q, want no-cache", cc)
	}
}
