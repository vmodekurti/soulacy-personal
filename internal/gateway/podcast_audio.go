package gateway

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/opennotebookmcp"
)

// Media elements cannot attach the browser's Authorization header. Mint a
// scoped, expiring capability after normal authentication; never put the
// gateway credential in a media URL. Restarting the gateway revokes tickets.
type podcastTickets struct {
	once sync.Once
	key  [32]byte
	err  error
}

type podcastTicket struct {
	URL     string `json:"url"`
	Expires int64  `json:"expires"`
}

func (t *podcastTickets) sign(payload string) (string, error) {
	t.once.Do(func() { _, t.err = rand.Read(t.key[:]) })
	if t.err != nil {
		return "", t.err
	}
	mac := hmac.New(sha256.New, t.key[:])
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// Only the configured adapter's audio-only loopback listener is reachable.
// Neither a chat link nor a ticket can choose an upstream host or API route.
func (s *Server) podcastAudioTarget(raw string) (*url.URL, error) {
	if len(raw) > 2048 || s.mcp == nil {
		return nil, fmt.Errorf("podcast audio is not configured")
	}
	source, err := url.Parse(raw)
	if err != nil || source.User != nil || source.RawQuery != "" || source.Fragment != "" {
		return nil, fmt.Errorf("invalid podcast link")
	}
	for _, srv := range s.mcp.ServersSnapshot() {
		if srv.ID != openNotebookMCPID {
			continue
		}
		setting := func(flag, env string) string {
			if value := openNotebookArgFromArgs(srv.Args, flag); value != "" {
				return value
			}
			return srv.Env[env]
		}
		base, err := opennotebookmcp.ValidateAudioBaseURL(setting("--audio-base-url", "OPEN_NOTEBOOK_AUDIO_URL"))
		if err != nil || base == nil {
			break
		}
		listen := setting("--audio-listen", "OPEN_NOTEBOOK_AUDIO_LISTEN")
		if opennotebookmcp.ValidateAudioListenAddress(listen) != nil {
			break
		}
		prefix := strings.TrimRight(base.Path, "/") + "/api/podcasts/episodes/"
		if source.Scheme != base.Scheme || !strings.EqualFold(source.Host, base.Host) || !strings.HasPrefix(source.Path, prefix) || !strings.HasSuffix(source.Path, "/audio") {
			break
		}
		id := strings.TrimSuffix(strings.TrimPrefix(source.Path, prefix), "/audio")
		// Decode once above and reject separators, percent escapes and dot segments.
		if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\%\x00\r\n") {
			break
		}
		return &url.URL{Scheme: "http", Host: listen, Path: "/api/podcasts/episodes/" + id + "/audio"}, nil
	}
	return nil, fmt.Errorf("link does not match the configured podcast audio service")
}

func (s *Server) handlePodcastTicket(c *fiber.Ctx) error {
	var body struct {
		URL string `json:"url"`
	}
	if err := c.BodyParser(&body); err != nil {
		return s.errMsg(c, 400, "invalid podcast link")
	}
	if _, err := s.podcastAudioTarget(body.URL); err != nil {
		return s.errMsg(c, 404, err.Error())
	}
	expires := time.Now().Add(4 * time.Hour).Unix()
	data, _ := json.Marshal(podcastTicket{URL: body.URL, Expires: expires})
	payload := base64.RawURLEncoding.EncodeToString(data)
	signature, err := s.podcastTickets.sign(payload)
	if err != nil {
		return s.errMsg(c, 503, "audio playback is unavailable")
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(fiber.Map{"url": "/api/v1/media/podcast?ticket=" + payload + "." + signature, "expires_at": expires})
}

var podcastAudioClient = func() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // This transport only contacts the configured loopback listener.
	transport.ResponseHeaderTimeout = 15 * time.Second
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}()

func (s *Server) handlePodcastAudio(c *fiber.Ctx) error {
	c.Set("Cache-Control", "private, no-store")
	c.Set("Referrer-Policy", "no-referrer")
	token := c.Query("ticket")
	payload, signature, ok := strings.Cut(token, ".")
	if !ok || len(token) > 4096 {
		return s.errMsg(c, 401, "invalid audio ticket")
	}
	expected, err := s.podcastTickets.sign(payload)
	if err != nil || !hmac.Equal([]byte(signature), []byte(expected)) {
		return s.errMsg(c, 401, "invalid audio ticket")
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	var ticket podcastTicket
	if err != nil || json.Unmarshal(data, &ticket) != nil || ticket.Expires <= time.Now().Unix() {
		return s.errMsg(c, 401, "audio ticket expired")
	}
	target, err := s.podcastAudioTarget(ticket.URL)
	if err != nil {
		return s.errMsg(c, 404, "podcast audio is not configured")
	}
	// Open Notebook's audio API supports GET but not HEAD. Fetch its headers
	// with GET, then close immediately for a downstream HEAD request.
	req, err := http.NewRequestWithContext(c.UserContext(), http.MethodGet, target.String(), nil)
	if err != nil {
		return s.errMsg(c, 502, "audio playback is unavailable")
	}
	for _, name := range []string{"Range", "If-Range", "If-None-Match", "If-Modified-Since"} {
		if value := c.Get(name); value != "" {
			req.Header.Set(name, value)
		}
	}
	resp, err := podcastAudioClient.Do(req)
	if err != nil {
		return s.errMsg(c, 502, "podcast audio is temporarily unavailable")
	}
	if resp.StatusCode >= 300 && resp.StatusCode != 304 && resp.StatusCode != 416 {
		resp.Body.Close()
		status := 502
		if resp.StatusCode == 404 {
			status = 404
		}
		return s.errMsg(c, status, "podcast audio is unavailable")
	}
	for _, name := range []string{"Accept-Ranges", "Content-Length", "Content-Range", "Content-Type", "ETag", "Last-Modified"} {
		if value := resp.Header.Get(name); value != "" {
			c.Set(name, value)
		}
	}
	c.Set("X-Content-Type-Options", "nosniff")
	c.Status(resp.StatusCode)
	if c.Method() == http.MethodHead || resp.StatusCode == 304 {
		resp.Body.Close()
		return nil
	}
	// fasthttp owns and closes this body, including on downstream disconnect.
	// Streaming preserves seeking and avoids buffering a whole episode in RAM.
	return c.SendStream(resp.Body, int(resp.ContentLength))
}
