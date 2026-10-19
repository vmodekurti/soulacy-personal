package gateway

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/httptestutil"
	"github.com/soulacy/soulacy/internal/mcp"
	"go.uber.org/zap"
)

const testPodcastURL = "https://phone.example:8443/media/api/podcasts/episodes/episode:abc/audio"

func podcastGateway(t *testing.T, handler http.HandlerFunc) *Server {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	s := newTestGateway(t, "secret")
	// Keep the configured registration without launching a subprocess.
	s.mcp = mcp.New(mcp.Config{Servers: map[string]mcp.ServerConfig{
		openNotebookMCPID: {Transport: "test-disabled", Args: []string{
			"--audio-base-url", "https://phone.example:8443/media",
			"--audio-listen", strings.TrimPrefix(upstream.URL, "http://"),
		}},
	}}, zap.NewNop())
	return s
}

func mintPodcast(t *testing.T, s *Server) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"url": testPodcastURL})
	status, result := gatewayJSON(t, s, "POST", "/api/v1/media/podcast/ticket", "secret", string(body))
	if status != 200 {
		t.Fatalf("mint: %d %v", status, result)
	}
	return result["url"].(string)
}

func TestPodcastAudioStreamsRangesAndHead(t *testing.T) {
	s := podcastGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/podcasts/episodes/episode:abc/audio" || r.URL.RawQuery != "" {
			t.Errorf("unexpected target %s", r.URL)
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("forwarded browser credentials")
		}
		if r.Method != "GET" {
			t.Error("upstream does not support HEAD")
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Set-Cookie", "must-not-forward=1")
		http.ServeContent(w, r, "episode.mp3", time.Time{}, strings.NewReader("0123456789"))
	})
	playback := mintPodcast(t, s)
	for _, method := range []string{"GET", "HEAD"} {
		req := httptest.NewRequest(method, playback, nil)
		req.Header.Set("Range", "bytes=2-5")
		req.Header.Set("Cookie", "private=1")
		resp, err := s.app.Test(httptestutil.WithHost(req), -1)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 206 || resp.Header.Get("Content-Range") != "bytes 2-5/10" || resp.ContentLength != 4 {
			t.Fatalf("bad range response: %#v", resp)
		}
		if method == "GET" && string(data) != "2345" {
			t.Fatalf("body: %q", data)
		}
		if method == "HEAD" && len(data) != 0 {
			t.Fatal("HEAD body")
		}
		if resp.Header.Get("Set-Cookie") != "" || resp.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatal("unsafe response headers")
		}
	}
	req := httptest.NewRequest("GET", playback, nil)
	req.Header.Set("Range", "bytes=200-300")
	resp, err := s.app.Test(httptestutil.WithHost(req), -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 416 {
		t.Fatalf("range status %d", resp.StatusCode)
	}
}

func TestPodcastAudioRequiresScopedUnexpiredTicket(t *testing.T) {
	var hits atomic.Int32
	s := podcastGateway(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(200) })
	body, _ := json.Marshal(map[string]string{"url": testPodcastURL})
	status, _ := gatewayJSON(t, s, "POST", "/api/v1/media/podcast/ticket", "", string(body))
	if status != 401 {
		t.Fatalf("unauthenticated mint: %d", status)
	}
	for _, raw := range []string{
		"http://127.0.0.1:5055/api/podcasts/episodes/a/audio",
		"https://evil.example/media/api/podcasts/episodes/a/audio",
		"https://phone.example:8443/media/api/podcasts/episodes/../audio",
		"https://phone.example:8443/media/api/podcasts/episodes/a%2fb/audio",
		"https://phone.example:8443/media/api/podcasts/episodes/%252e%252e/audio",
		"https://user@phone.example:8443/media/api/podcasts/episodes/a/audio",
		testPodcastURL + "?redirect=elsewhere", testPodcastURL + "#fragment",
	} {
		body, _ := json.Marshal(map[string]string{"url": raw})
		status, _ := gatewayJSON(t, s, "POST", "/api/v1/media/podcast/ticket", "secret", string(body))
		if status != 404 {
			t.Errorf("accepted %s: %d", raw, status)
		}
	}
	valid := mintPodcast(t, s)
	data, _ := json.Marshal(podcastTicket{URL: testPodcastURL, Expires: time.Now().Add(-time.Minute).Unix()})
	payload := base64.RawURLEncoding.EncodeToString(data)
	sig, _ := s.podcastTickets.sign(payload)
	for _, path := range []string{"/api/v1/media/podcast", valid + "x", "/api/v1/media/podcast?ticket=" + payload + "." + sig} {
		status, _ := gatewayJSON(t, s, "GET", path, "", "")
		if status != 401 {
			t.Errorf("invalid ticket accepted: %d", status)
		}
	}
	if hits.Load() != 0 {
		t.Fatal("invalid requests contacted upstream")
	}
}

func TestPodcastAudioNeverFollowsRedirects(t *testing.T) {
	var redirected atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer destination.Close()
	s := podcastGateway(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) })
	status, _ := gatewayJSON(t, s, "GET", mintPodcast(t, s), "", "")
	if status != 502 || redirected.Load() != 0 {
		t.Fatal("redirect was followed or exposed")
	}
}
