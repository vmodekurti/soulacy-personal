package managedbrowser

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestBrowserProxyBlocksPrivateDestinations(t *testing.T) {
	var reached atomic.Bool
	private := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Store(true) }))
	defer private.Close()

	proxy, err := startBrowserProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	req := httptest.NewRequest(http.MethodGet, private.URL, nil)
	recorder := httptest.NewRecorder()
	proxy.serveHTTP(recorder, req)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", recorder.Code)
	}
	if reached.Load() {
		t.Fatal("guarded browser proxy reached a private destination")
	}
}
