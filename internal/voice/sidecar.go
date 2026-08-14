package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const (
	defaultSidecarURL = "http://127.0.0.1:8081"
	maxAudioBytes     = 16 << 20
	maxSpeechBytes    = 32 << 20
)

// SidecarCapabilities is the provider-neutral contract advertised by a voice
// process. A sidecar may omit /capabilities; Soulacy then assumes both STT and
// TTS are available and lets the individual calls report a useful error.
type SidecarCapabilities struct {
	STT            bool     `json:"stt"`
	TTS            bool     `json:"tts"`
	Streaming      bool     `json:"streaming,omitempty"`
	Languages      []string `json:"languages,omitempty"`
	Voices         []string `json:"voices,omitempty"`
	STTBackend     string   `json:"stt_backend,omitempty"`
	STTAccelerator string   `json:"stt_accelerator,omitempty"`
	TTSBackend     string   `json:"tts_backend,omitempty"`
	TTSDevice      string   `json:"tts_device,omitempty"`
}

// Sidecar connects Soulacy to local or explicitly-approved remote speech
// services without coupling speech to the agent's LLM provider.
type Sidecar struct {
	baseURL     *url.URL
	voice       string
	client      *http.Client
	allowRemote bool
}

func NewSidecar(rawURL, voice, timeout string, allowRemote bool) (*Sidecar, error) {
	if strings.TrimSpace(rawURL) == "" {
		rawURL = defaultSidecarURL
	}
	u, err := url.Parse(strings.TrimRight(rawURL, "/"))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("voice: sidecar_url must be an http(s) URL")
	}
	if !allowRemote && !isLoopbackHost(u.Hostname()) {
		return nil, fmt.Errorf("voice: remote sidecar %q requires voice.allow_remote: true", u.Hostname())
	}
	d := 60 * time.Second
	if strings.TrimSpace(timeout) != "" {
		parsed, parseErr := time.ParseDuration(timeout)
		if parseErr != nil || parsed <= 0 {
			return nil, fmt.Errorf("voice: invalid timeout %q", timeout)
		}
		d = parsed
	}
	client := &http.Client{Timeout: d, CheckRedirect: func(req *http.Request, _ []*http.Request) error {
		if !allowRemote && !isLoopbackHost(req.URL.Hostname()) {
			return errors.New("voice: sidecar redirect to a remote host was blocked")
		}
		return nil
	}}
	return &Sidecar{baseURL: u, voice: voice, allowRemote: allowRemote, client: client}, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Sidecar) SetClient(client *http.Client) { s.client = client }
func (s *Sidecar) Provider() string              { return "sidecar" }
func (s *Sidecar) Model() string                 { return "local speech" }
func (s *Sidecar) URL() string                   { return s.baseURL.String() }
func (s *Sidecar) Voice() string                 { return s.voice }

func (s *Sidecar) endpoint(p string) string {
	u := *s.baseURL
	u.Path = path.Join(strings.TrimSuffix(u.Path, "/"), p)
	return u.String()
}

func (s *Sidecar) Ready() (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.endpoint("health"), nil)
	resp, err := s.client.Do(req)
	if err != nil {
		return false, "voice sidecar is not reachable at " + s.URL() + ": " + err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Sprintf("voice sidecar health check returned %d", resp.StatusCode)
	}
	return true, ""
}

func (s *Sidecar) Capabilities(ctx context.Context) (SidecarCapabilities, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.endpoint("capabilities"), nil)
	resp, err := s.client.Do(req)
	if err != nil {
		return SidecarCapabilities{}, fmt.Errorf("voice: capabilities: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return SidecarCapabilities{STT: true, TTS: true}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SidecarCapabilities{}, fmt.Errorf("voice: capabilities returned %d", resp.StatusCode)
	}
	var out SidecarCapabilities
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return SidecarCapabilities{}, fmt.Errorf("voice: decode capabilities: %w", err)
	}
	return out, nil
}

func (s *Sidecar) Transcribe(ctx context.Context, contentType string, audio io.Reader) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("audio", "speech.webm")
	if err != nil {
		return "", err
	}
	n, err := io.Copy(part, io.LimitReader(audio, maxAudioBytes+1))
	if err != nil {
		return "", err
	}
	if n > maxAudioBytes {
		return "", errors.New("voice: audio exceeds 16 MiB limit")
	}
	_ = w.WriteField("content_type", contentType)
	if err = w.Close(); err != nil {
		return "", err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint("transcribe"), &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("voice: transcribe: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("voice: sidecar transcribe returned %d: %s", resp.StatusCode, truncate(string(data), 200))
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &out); err != nil || strings.TrimSpace(out.Text) == "" {
		return "", errors.New("voice: sidecar returned no transcript")
	}
	return strings.TrimSpace(out.Text), nil
}

func (s *Sidecar) Synthesize(ctx context.Context, text, requestedVoice string) ([]byte, string, error) {
	if len(text) > 32<<10 {
		return nil, "", errors.New("voice: synthesis text exceeds 32 KiB limit")
	}
	if requestedVoice == "" {
		requestedVoice = s.voice
	}
	payload, _ := json.Marshal(map[string]string{"text": text, "voice": requestedVoice})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint("synthesize"), bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("voice: synthesize: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, "", fmt.Errorf("voice: sidecar synthesize returned %d: %s", resp.StatusCode, truncate(string(data), 200))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSpeechBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxSpeechBytes {
		return nil, "", errors.New("voice: synthesized audio exceeds 32 MiB limit")
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "audio/wav"
	}
	return data, ct, nil
}
