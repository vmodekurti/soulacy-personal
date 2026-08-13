// Package teams provides an outbound Microsoft Teams webhook channel adapter.
package teams

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

// Adapter sends Soulacy messages to a Microsoft Teams Incoming Webhook or
// Teams Workflow URL. It is intentionally outbound-only; interactive Teams bot
// support is a separate adapter surface.
type Adapter struct {
	id         string
	webhookURL string
	title      string
	client     *http.Client

	mu        sync.RWMutex
	connected bool
	detail    string
}

// New creates an outbound Teams adapter.
func New(id, webhookURL, title string, timeout time.Duration) (*Adapter, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		id = "teams"
	}
	webhookURL = strings.TrimSpace(webhookURL)
	if webhookURL == "" {
		return nil, fmt.Errorf("teams: webhook_url is required")
	}
	u, err := url.Parse(webhookURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("teams: webhook_url must be an absolute http(s) URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("teams: webhook_url scheme must be http or https")
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Adapter{
		id:         id,
		webhookURL: webhookURL,
		title:      strings.TrimSpace(title),
		client:     netguard.NewHTTPClient(timeout, false, nil),
	}, nil
}

func (a *Adapter) ID() string   { return a.id }
func (a *Adapter) Name() string { return "Microsoft Teams" }

func (a *Adapter) Start(ctx context.Context, inbox chan<- message.Message) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.connected = true
	a.detail = "outbound Teams webhook ready"
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
		return fmt.Errorf("teams: send: %w", err)
	}
	text := messageText(msg)
	if text == "" {
		return nil
	}
	if a.title != "" {
		text = "**" + a.title + "**\n\n" + text
	}
	// The per-message destination override is model output (channel.send's `to`),
	// not operator config — see netguard.ResolveWebhookTarget. It may vary the
	// path on the configured host and nothing else.
	override := strings.TrimSpace(msg.ThreadID)
	if !netguard.IsHTTPURL(override) {
		override = strings.TrimSpace(msg.Metadata["to"])
	}
	target, err := netguard.ResolveWebhookTarget("teams", a.webhookURL, override)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return fmt.Errorf("teams: payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("teams: send: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("teams: send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		detail := strings.TrimSpace(string(snippet))
		if detail == "" {
			detail = resp.Status
		}
		return fmt.Errorf("teams: send: API returned status %d: %s", resp.StatusCode, detail)
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
