// Package webhook provides a generic outbound webhook channel adapter.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/internal/netguard"
	"github.com/soulacy/soulacy/pkg/message"
)

const defaultTimeout = 10 * time.Second

// Adapter sends Soulacy messages to an HTTP endpoint.
type Adapter struct {
	id       string
	name     string
	url      string
	method   string
	headers  map[string]string
	template string
	secret   string // HMAC signing secret; empty = unsigned (back-compat)
	client   *http.Client

	mu        sync.RWMutex
	connected bool
	detail    string
}

// New creates an outbound webhook adapter.
func New(id, endpoint, method string, headers map[string]string, template, secret string, timeout time.Duration) (*Adapter, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		id = "webhook"
	}
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("webhook: url is required")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("webhook: url must be an absolute http(s) URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("webhook: url scheme must be http or https")
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		method = http.MethodPost
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Adapter{
		id:       id,
		name:     "Webhook",
		url:      endpoint,
		method:   method,
		headers:  headers,
		template: template,
		secret:   strings.TrimSpace(secret),
		client:   netguard.NewHTTPClient(timeout, false, nil),
	}, nil
}

func (a *Adapter) ID() string   { return a.id }
func (a *Adapter) Name() string { return a.name }

func (a *Adapter) Start(ctx context.Context, inbox chan<- message.Message) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.connected = true
	a.detail = "outbound webhook ready"
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
		return fmt.Errorf("webhook: send: %w", err)
	}
	text := messageText(msg)
	if text == "" {
		return nil
	}
	// The per-message destination override is model output (channel.send's `to`),
	// not operator config — see netguard.ResolveWebhookTarget. It may vary the
	// path on the configured host and nothing else.
	override := strings.TrimSpace(msg.ThreadID)
	if !netguard.IsHTTPURL(override) {
		override = strings.TrimSpace(msg.Metadata["to"])
	}
	target, err := netguard.ResolveWebhookTarget("webhook", a.url, override)
	if err != nil {
		return err
	}
	body, contentType, err := a.payload(msg, text)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, a.method, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook: send: %w", err)
	}
	req.Header.Set("Content-Type", contentType)

	// Sign the payload so the receiver can prove it came from us and hasn't been
	// tampered with. Opt-in: no secret → no signature headers (unchanged behavior).
	if a.secret != "" {
		ts := time.Now().Unix()
		req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
		req.Header.Set(HeaderSignature, Sign(a.secret, ts, body))
	}

	for k, v := range a.headers {
		k = strings.TrimSpace(k)
		if k != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook: send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		msg := strings.TrimSpace(string(snippet))
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("webhook: send: API returned status %d: %s", resp.StatusCode, msg)
	}
	return nil
}

func (a *Adapter) payload(msg message.Message, text string) ([]byte, string, error) {
	if strings.TrimSpace(a.template) != "" {
		rendered := renderTemplate(a.template, msg, text)
		return []byte(rendered), "text/plain; charset=utf-8", nil
	}
	payload := map[string]any{
		"id":         msg.ID,
		"session_id": msg.SessionID,
		"agent_id":   msg.AgentID,
		"channel":    msg.Channel,
		"thread_id":  msg.ThreadID,
		"user_id":    msg.UserID,
		"username":   msg.Username,
		"role":       msg.Role,
		"text":       text,
		"parts":      msg.Parts,
		"metadata":   msg.Metadata,
		"created_at": msg.CreatedAt,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("webhook: payload: %w", err)
	}
	return b, "application/json", nil
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

func renderTemplate(tmpl string, msg message.Message, text string) string {
	repl := map[string]string{
		"{text}":       text,
		"{agent_id}":   msg.AgentID,
		"{session_id}": msg.SessionID,
		"{channel}":    msg.Channel,
		"{thread_id}":  msg.ThreadID,
		"{to}":         msg.ThreadID,
		"{user_id}":    msg.UserID,
		"{username}":   msg.Username,
		"{timestamp}":  msg.CreatedAt.Format(time.RFC3339),
	}
	out := tmpl
	for k, v := range repl {
		out = strings.ReplaceAll(out, k, v)
	}
	return out
}
