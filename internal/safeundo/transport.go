package safeundo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/soulacy/soulacy/internal/netguard"
)

type snapshot struct {
	version string
	media   string
	body    []byte
	object  map[string]any
}

func strongETag(h http.Header) (string, bool) {
	v := h.Values("ETag")
	if len(v) != 1 || len(v[0]) < 2 || len(v[0]) > 512 || v[0][0] != '"' || v[0][len(v[0])-1] != '"' {
		return "", false
	}
	for _, c := range []byte(v[0][1 : len(v[0])-1]) {
		if c < 0x21 || c == '"' || c == 0x7f {
			return "", false
		}
	}
	return v[0], true
}

func resourceClient(r Resource) *http.Client {
	u, _ := url.Parse(r.URL)
	allowed := []string{}
	if r.AllowPrivateHost {
		allowed = append(allowed, u.Hostname())
	}
	return &http.Client{
		Timeout:       8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &netguard.GuardedTransport{BlockPrivate: true, AllowedHosts: allowed,
			Base: &http.Transport{Proxy: nil, DisableCompression: true, MaxResponseHeaderBytes: 16 << 10,
				TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second}},
	}
}

func (s *Store) request(ctx context.Context, r Resource, method, contentType, version string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, r.URL, bytes.NewReader(body))
	if err != nil {
		return nil, ErrInvalid
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-cache, no-store")
	if r.Kind == "json_record" {
		req.Header.Set("Accept", "application/json")
	} else {
		req.Header.Set("Accept", "text/plain, text/markdown")
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if version != "" {
		req.Header.Set("If-Match", version)
	}
	if r.TokenEnv != "" {
		token := s.token(r.TokenEnv)
		if token == "" || len(token) > 8192 || strings.ContainsAny(token, "\r\n\x00") {
			return nil, ErrPermission
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return s.clients[r.AgentID+"/"+r.ID].Do(req)
}

func (s *Store) read(ctx context.Context, r Resource) (snapshot, error) {
	resp, err := s.request(ctx, r, http.MethodGet, "", "", nil)
	if err != nil {
		if errors.Is(err, ErrPermission) {
			return snapshot{}, ErrPermission
		}
		return snapshot{}, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return snapshot{}, ErrPermission
	}
	if resp.StatusCode != 200 || (resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity") {
		return snapshot{}, ErrUnavailable
	}
	version, ok := strongETag(resp.Header)
	if !ok {
		return snapshot{}, fmt.Errorf("%w: resource must return a strong ETag", ErrUnavailable)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if err != nil || len(b) > MaxBody || !utf8.Valid(b) {
		return snapshot{}, ErrUnavailable
	}
	rawMedia := resp.Header.Get("Content-Type")
	media, params, err := mime.ParseMediaType(rawMedia)
	if err != nil || len(resp.Header.Values("Content-Type")) != 1 || len(rawMedia) > 256 || (params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8")) {
		return snapshot{}, ErrUnavailable
	}
	snap := snapshot{version: version, media: rawMedia, body: b}
	if r.Kind == "json_record" {
		if media != "application/json" && !strings.HasSuffix(media, "+json") {
			return snapshot{}, ErrUnavailable
		}
		value, err := parseJSON(b)
		if err != nil {
			return snapshot{}, ErrUnavailable
		}
		canonical, err := json.Marshal(value)
		if err != nil || len(canonical) > MaxBody {
			return snapshot{}, ErrUnavailable
		}
		var ok bool
		snap.object, ok = value.(map[string]any)
		if !ok {
			return snapshot{}, ErrUnavailable
		}
	} else if (media != "text/plain" && media != "text/markdown") || bytes.ContainsRune(b, 0) {
		return snapshot{}, ErrUnavailable
	}
	return snap, nil
}

// mutate never retries. Even a 500 or a malformed success may follow a write.
// Only the conditional-write contract's explicit refusal statuses establish
// that nothing happened. A successful response is subsequently read back.
func (s *Store) mutate(ctx context.Context, r Resource, a Action, direction, version string) error {
	method, media := http.MethodPut, a.Media
	body := []byte(*a.AfterTextOrEmpty())
	if direction == "undo" {
		body = []byte(*a.BeforeTextOrEmpty())
	}
	if r.Kind == "json_record" {
		method, media = http.MethodPatch, "application/json-patch+json"
		body = patchFor(a, direction)
	}
	resp, err := s.request(ctx, r, method, media, version, body)
	if err != nil {
		if errors.Is(err, ErrPermission) {
			return ErrPermission
		}
		return ErrUncertain
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, MaxBody+1))
	switch resp.StatusCode {
	case 200, 204:
		return nil
	case 412, 409, 404, 405, 415, 422:
		return ErrConflict
	case 401, 403:
		return ErrPermission
	default:
		return ErrUncertain
	}
}

func (a Action) AfterTextOrEmpty() *string {
	if a.AfterText != nil {
		return a.AfterText
	}
	v := ""
	return &v
}
func (a Action) BeforeTextOrEmpty() *string {
	if a.BeforeText != nil {
		return a.BeforeText
	}
	v := ""
	return &v
}

var environmentToken = os.Getenv
