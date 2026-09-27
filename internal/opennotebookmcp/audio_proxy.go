package opennotebookmcp

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

const podcastAudioPathPrefix = "/api/podcasts/episodes/"

var audioRequestHeaders = []string{"Range", "If-Range", "If-None-Match", "If-Modified-Since"}
var audioResponseHeaders = []string{
	"Accept-Ranges", "Cache-Control", "Content-Disposition", "Content-Length",
	"Content-Range", "Content-Type", "ETag", "Last-Modified",
}

// ValidateAudioListenAddress ensures the media proxy cannot accidentally bind
// to a LAN or public interface. Tailscale Serve should forward to this listener.
func ValidateAudioListenAddress(address string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil || port == "" {
		return fmt.Errorf("audio listener must be a loopback host:port address")
	}
	if !isLoopbackHost(host) {
		return fmt.Errorf("audio listener must bind to loopback")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// AudioHandler exposes only completed podcast audio. It does not proxy health,
// notebooks, sources, generation, or any other Open Notebook API operation.
func (s *Server) AudioHandler() http.Handler {
	return http.HandlerFunc(s.servePodcastAudio)
}

func (s *Server) servePodcastAudio(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, ok := podcastEpisodeID(r.URL.Path)
	if !ok || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	target := *s.baseURL
	target.Path = joinURLPath(target.Path, podcastAudioPathPrefix+url.PathEscape(id)+"/audio")
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), nil)
	if err != nil {
		http.Error(w, "could not build podcast audio request", http.StatusBadGateway)
		return
	}
	for _, name := range audioRequestHeaders {
		if value := r.Header.Get(name); value != "" {
			req.Header.Set(name, value)
		}
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.audioClient.Do(req)
	if err != nil {
		http.Error(w, "Open Notebook podcast audio is unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for _, name := range audioResponseHeaders {
		if values := resp.Header.Values(name); len(values) > 0 {
			w.Header()[name] = append([]string(nil), values...)
		}
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodGet {
		_, _ = io.Copy(w, resp.Body)
	}
}

func podcastEpisodeID(requestPath string) (string, bool) {
	if !strings.HasPrefix(requestPath, podcastAudioPathPrefix) || !strings.HasSuffix(requestPath, "/audio") {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(requestPath, podcastAudioPathPrefix), "/audio")
	if id == "" || strings.Contains(id, "/") || strings.ContainsAny(id, "\\\x00") {
		return "", false
	}
	return id, true
}
