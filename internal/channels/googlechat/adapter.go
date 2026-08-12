// Package googlechat provides an outbound Google Chat webhook channel adapter.
package googlechat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/internal/netguard"
	"github.com/soulacy/soulacy/pkg/message"
)

const defaultTimeout = 10 * time.Second

// Adapter sends Soulacy messages to a Google Chat incoming webhook URL.
type Adapter struct {
	id         string
	webhookURL string
	prefix     string
	client     *http.Client

	mu        sync.RWMutex
	connected bool
	detail    string
}

// New creates an outbound Google Chat adapter.
func New(id, webhookURL, prefix string, timeout time.Duration) (*Adapter, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		id = "google_chat"
	}
	webhookURL = strings.TrimSpace(webhookURL)
	if webhookURL == "" {
		return nil, fmt.Errorf("google_chat: webhook_url is required")
	}
	u, err := url.Parse(webhookURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("google_chat: webhook_url must be an absolute http(s) URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("google_chat: webhook_url scheme must be http or https")
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Adapter{
		id:         id,
		webhookURL: webhookURL,
		prefix:     strings.TrimSpace(prefix),
		client:     &http.Client{Timeout: timeout},
	}, nil
}

func (a *Adapter) ID() string   { return a.id }
func (a *Adapter) Name() string { return "Google Chat" }

func (a *Adapter) Start(ctx context.Context, inbox chan<- message.Message) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.connected = true
	a.detail = "outbound Google Chat webhook ready"
	return nil
}

func (a *Adapter) Stop() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.connected = false
	a.detail = "stopped"
	return nil
}

func (a *Adapter) Status() channels.AdapterStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return channels.AdapterStatus{Connected: a.connected, Detail: a.detail}
}

func (a *Adapter) Send(ctx context.Context, msg message.Message) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("google_chat: send: %w", err)
	}
	text := messageText(msg)
	if text == "" {
		return nil
	}
	if a.prefix != "" {
		text = a.prefix + "\n\n" + text
	}
	// The per-message destination override is model output (channel.send's `to`),
	// not operator config — see netguard.ResolveWebhookTarget. It may vary the
	// path on the configured host and nothing else.
	override := strings.TrimSpace(msg.ThreadID)
	if !netguard.IsHTTPURL(override) {
		override = strings.TrimSpace(msg.Metadata["to"])
	}
	target, err := netguard.ResolveWebhookTarget("google_chat", a.webhookURL, override)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return fmt.Errorf("google_chat: payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("google_chat: send: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("google_chat: send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		detail := strings.TrimSpace(string(snippet))
		if detail == "" {
			detail = resp.Status
		}
		return fmt.Errorf("google_chat: send: API returned status %d: %s", resp.StatusCode, detail)
	}
	return nil
}

func messageText(msg message.Message) string {
	var parts []string
	for _, part := range msg.Parts {
		if part.Type == message.ContentText && strings.TrimSpace(part.Text) != "" {
			parts = append(parts, part.Text)
		}
	}
	return channels.PlainTextForMessaging(strings.Join(parts, "\n\n"))
}
