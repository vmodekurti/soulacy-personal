// Package voice implements the host side of realtime voice (Story 11,
// design in docs/VOICE_SPIKE.md).
//
// The browser talks to the realtime provider DIRECTLY (WebRTC for OpenAI):
// audio never transits the gateway. The host's only job is the control
// plane — minting short-lived ephemeral client keys so the user's real API
// key never reaches the browser. Minting is a plain HTTPS call (no vendor
// SDKs in the binary, per the spike's sidecar/no-SDK constraint).
package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// EphemeralKey is a short-lived client credential for a realtime session.
type EphemeralKey struct {
	Key       string    `json:"key"`
	ExpiresAt time.Time `json:"expires_at"`
	Model     string    `json:"model"`
	Provider  string    `json:"provider"`
}

const (
	defaultModel   = "gpt-realtime-mini"
	defaultBaseURL = "https://api.openai.com"
)

// OpenAIMinter mints ephemeral Realtime client secrets via
// POST /v1/realtime/client_secrets (tolerating the legacy
// /v1/realtime/sessions response shape).
type OpenAIMinter struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

// NewOpenAIMinter builds a minter. Empty model/baseURL get defaults.
func NewOpenAIMinter(apiKey, model, baseURL string) *OpenAIMinter {
	if model == "" {
		model = defaultModel
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &OpenAIMinter{
		apiKey:  apiKey,
		model:   model,
		baseURL: baseURL,
		client:  &http.Client{Timeout: 15 * time.Second},
	}
}

// SetClient injects an HTTP client (tests use a fake RoundTripper).
func (m *OpenAIMinter) SetClient(c *http.Client) { m.client = c }

// Provider names the realtime provider.
func (m *OpenAIMinter) Provider() string { return "openai" }

// Model returns the configured realtime model.
func (m *OpenAIMinter) Model() string { return m.model }

// Ready reports whether minting can work, with a human-readable reason
// when it can't.
func (m *OpenAIMinter) Ready() (bool, string) {
	if m.apiKey == "" {
		return false, "no OpenAI API key configured (set llm.providers.openai.api_key or OPENAI_API_KEY)"
	}
	return true, ""
}

// Mint requests a fresh ephemeral client key.
func (m *OpenAIMinter) Mint(ctx context.Context) (EphemeralKey, error) {
	if ok, detail := m.Ready(); !ok {
		return EphemeralKey{}, errors.New(detail)
	}
	payload, _ := json.Marshal(map[string]any{
		"session": map[string]any{
			"type":  "realtime",
			"model": m.model,
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		m.baseURL+"/v1/realtime/client_secrets", bytes.NewReader(payload))
	if err != nil {
		return EphemeralKey{}, fmt.Errorf("voice: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+m.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return EphemeralKey{}, fmt.Errorf("voice: mint ephemeral key: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Never echo the request (which carries the API key); the response
		// body is provider diagnostics and safe to surface truncated.
		return EphemeralKey{}, fmt.Errorf("voice: provider returned %d: %s",
			resp.StatusCode, truncate(string(body), 200))
	}

	// Current shape: {"value":"ek_…","expires_at":unix}
	// Legacy shape:  {"client_secret":{"value":"ek_…","expires_at":unix}}
	var parsed struct {
		Value        string `json:"value"`
		ExpiresAt    int64  `json:"expires_at"`
		ClientSecret struct {
			Value     string `json:"value"`
			ExpiresAt int64  `json:"expires_at"`
		} `json:"client_secret"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return EphemeralKey{}, fmt.Errorf("voice: decode response: %w", err)
	}
	key, exp := parsed.Value, parsed.ExpiresAt
	if key == "" {
		key, exp = parsed.ClientSecret.Value, parsed.ClientSecret.ExpiresAt
	}
	if key == "" {
		return EphemeralKey{}, errors.New("voice: provider response carried no client secret")
	}
	return EphemeralKey{
		Key:       key,
		ExpiresAt: time.Unix(exp, 0).UTC(),
		Model:     m.model,
		Provider:  m.Provider(),
	}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
