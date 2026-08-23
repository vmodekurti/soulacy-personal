// Package whatsapp implements a Soulacy channel adapter for WhatsApp
// via the Meta Business Cloud API.
//
// Setup (one-time):
//  1. Create an app at https://developers.facebook.com
//  2. Add the WhatsApp product, note your Phone Number ID and access token.
//  3. In Webhooks, point Meta to: https://your-domain/channels/whatsapp/webhook
//     and enter the verify_token you set in config.yaml.
//  4. Subscribe to the "messages" webhook field.
//
// Message flow:
//
//	Inbound: Meta POST → /channels/whatsapp/webhook → inbox → Engine.Handle → reply
//	Outbound: Engine reply → Meta Graph API POST → user's WhatsApp
package whatsapp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/pkg/message"
)

// _ ensures io is used (io.LimitReader in Send).
var _ = io.Discard

const (
	graphAPIVersion = "v20.0"
	graphAPIBase    = "https://graph.facebook.com/" + graphAPIVersion
)

// Adapter implements channels.Adapter for WhatsApp Business Cloud.
type Adapter struct {
	phoneNumberID string
	accessToken   string
	verifyToken   string
	// appSecret is Meta's "App secret" from the developer portal — used to
	// verify the X-Hub-Signature-256 HMAC on inbound webhooks. When empty
	// the adapter logs a loud warning but still accepts requests (for
	// backward compatibility with existing deployments). Once we're past
	// the migration window the empty case should hard-fail.
	// (PRODUCTION_AUDIT → CRITICAL/Security)
	appSecret  string
	agentID    string
	activation channels.ActivationPolicy
	log        *zap.Logger
	inbox      chan<- message.Message
	ctx        context.Context
	client     *http.Client
}

// New creates a new WhatsApp adapter.
//   - phoneNumberID: from Meta developer portal ("Phone Number ID")
//   - accessToken:   permanent or temporary access token for the app
//   - verifyToken:   arbitrary string you set in Meta Webhooks config
//   - appSecret:     Meta App secret used to verify X-Hub-Signature-256.
//     Empty = unverified (logged loudly).
//   - agentID:       Soulacy agent that handles WhatsApp messages
func New(phoneNumberID, accessToken, verifyToken, appSecret, agentID string, log *zap.Logger) *Adapter {
	return NewWithActivation(phoneNumberID, accessToken, verifyToken, appSecret, agentID, channels.ActivationPolicy{}, log)
}

// NewWithActivation creates a WhatsApp adapter with shared channel activation
// guardrails.
func NewWithActivation(phoneNumberID, accessToken, verifyToken, appSecret, agentID string, activation channels.ActivationPolicy, log *zap.Logger) *Adapter {
	return &Adapter{
		phoneNumberID: phoneNumberID,
		accessToken:   accessToken,
		verifyToken:   verifyToken,
		appSecret:     appSecret,
		agentID:       agentID,
		activation:    activation,
		log:           log,
		client:        &http.Client{Timeout: 15 * time.Second},
	}
}

// VerifySignature verifies Meta's X-Hub-Signature-256 HMAC on an inbound webhook.
//
// Security change: empty appSecret now REJECTS (previously ACCEPTED)
//
// The old behaviour silently accepted all webhook requests when app_secret was
// not configured, reasoning that the verify_token was sufficient protection.
// That reasoning is wrong: the verify_token is exchanged once at subscription
// time (GET request from Meta) and is not included on subsequent event POSTs.
// Without an HMAC check, any caller who discovers the webhook URL can inject
// arbitrary WhatsApp messages into the agent loop — a trivially exploitable
// vector for prompt injection.
//
// The new behaviour hard-rejects any request when appSecret is empty and logs
// an actionable error. Operators upgrading from an unconfigured deployment must
// add channels.whatsapp.app_secret (found in the Meta App Dashboard under
// App Settings → Basic → App Secret) before webhook delivery will resume.
func (a *Adapter) VerifySignature(headerValue string, rawBody []byte) bool {
	if a.appSecret == "" {
		a.log.Error("whatsapp: REJECTING webhook — app_secret not configured. " +
			"Set channels.whatsapp.app_secret in config.yaml. " +
			"Without it any caller can forge inbound WhatsApp messages.")
		return false
	}
	const prefix = "sha256="
	if !strings.HasPrefix(headerValue, prefix) {
		return false
	}
	want, err := hex.DecodeString(strings.TrimPrefix(headerValue, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(a.appSecret))
	mac.Write(rawBody)
	got := mac.Sum(nil)
	return subtle.ConstantTimeCompare(got, want) == 1
}

func (a *Adapter) ID() string   { return "whatsapp" }
func (a *Adapter) Name() string { return "WhatsApp (Meta Business Cloud)" }

// Start wires up the shared inbox. WhatsApp is webhook-driven so there is no
// polling goroutine — messages arrive via HandleWebhook.
func (a *Adapter) Start(ctx context.Context, inbox chan<- message.Message) error {
	a.inbox = inbox
	a.ctx = ctx
	a.log.Info("whatsapp adapter ready",
		zap.String("phone_number_id", a.phoneNumberID),
		zap.String("agent_id", a.agentID),
	)
	return nil
}

// Send delivers a reply back to the WhatsApp user via the Graph API.
func (a *Adapter) Send(ctx context.Context, msg message.Message) error {
	text := ""
	for _, p := range msg.Parts {
		if p.Text != "" {
			text = p.Text
			break
		}
	}
	if text == "" || msg.ThreadID == "" {
		return nil // nothing to send
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("whatsapp: send: %w", err)
	}

	payload := map[string]any{
		"messaging_product": "whatsapp",
		"to":                msg.ThreadID, // recipient phone number
		"type":              "text",
		"text":              map[string]string{"body": text},
	}
	body, _ := json.Marshal(payload)

	url := fmt.Sprintf("%s/%s/messages", graphAPIBase, a.phoneNumberID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("whatsapp: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.accessToken)

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("whatsapp: send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("whatsapp: send error %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func (a *Adapter) Stop() error { return nil }

func (a *Adapter) Status() channels.AdapterStatus {
	if a.phoneNumberID != "" && a.accessToken != "" {
		return channels.AdapterStatus{Connected: true, Detail: "webhook"}
	}
	return channels.AdapterStatus{Connected: false, Detail: "unconfigured"}
}

// ── Webhook helpers (called by gateway Fiber handlers) ───────────────────────

// Verify checks Meta's GET webhook challenge.
// Returns (challenge, true) on success, ("", false) on failure.
func (a *Adapter) Verify(mode, token, challenge string) (string, bool) {
	// subtle.ConstantTimeCompare, not ==: a byte-at-a-time comparison against a
	// shared secret on an unauthenticated endpoint is measurable, and this
	// endpoint can be probed at will. The HMAC check in this same file already
	// uses it.
	//
	// The failed attempt is no longer logged with the token in it either. That
	// line wrote the caller's guess — and, on a misconfiguration where the real
	// token reached the wrong adapter, the real secret — into a log file that is
	// collected in support bundles.
	ok := subtle.ConstantTimeCompare([]byte(token), []byte(a.verifyToken)) == 1
	if mode == "subscribe" && ok {
		a.log.Info("whatsapp: webhook verified by Meta")
		return challenge, true
	}
	a.log.Warn("whatsapp: webhook verification failed", zap.String("mode", mode))
	return "", false
}

// Dispatch parses a Meta webhook notification body and pushes any text
// messages onto the channel inbox.
func (a *Adapter) Dispatch(body []byte) {
	var notification waNotification
	if err := json.Unmarshal(body, &notification); err != nil {
		a.log.Warn("whatsapp: parse notification error", zap.Error(err))
		return
	}

	for _, entry := range notification.Entry {
		for _, change := range entry.Changes {
			if change.Field != "messages" {
				continue
			}
			v := change.Value
			for _, wamsg := range v.Messages {
				if wamsg.Type != "text" {
					continue // only text messages for now
				}
				// Look up the sender's display name from contacts
				username := wamsg.From
				for _, c := range v.Contacts {
					if c.WAID == wamsg.From {
						username = c.Profile.Name
						break
					}
				}
				text, ok := a.activation.Apply(wamsg.Text.Body, wamsg.From, wamsg.From, false)
				if !ok {
					continue
				}

				msg := message.Message{
					ID:        wamsg.ID,
					SessionID: fmt.Sprintf("wa-%s", wamsg.From),
					AgentID:   a.agentID,
					Channel:   "whatsapp",
					ThreadID:  wamsg.From, // used as the reply-to address
					UserID:    wamsg.From,
					Username:  username,
					Role:      message.RoleUser,
					Parts:     message.Text(text),
					CreatedAt: time.Now().UTC(),
				}

				if a.inbox != nil {
					ctx := a.ctx
					if ctx == nil {
						ctx = context.Background()
					}
					select {
					case a.inbox <- msg:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}
}

// ── Meta webhook payload types ────────────────────────────────────────────────

type waNotification struct {
	Object string    `json:"object"`
	Entry  []waEntry `json:"entry"`
}

type waEntry struct {
	ID      string     `json:"id"`
	Changes []waChange `json:"changes"`
}

type waChange struct {
	Field string  `json:"field"`
	Value waValue `json:"value"`
}

type waValue struct {
	Messages []waMessage `json:"messages"`
	Contacts []waContact `json:"contacts"`
}

type waMessage struct {
	From      string `json:"from"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Text      struct {
		Body string `json:"body"`
	} `json:"text"`
}

type waContact struct {
	WAID    string `json:"wa_id"`
	Profile struct {
		Name string `json:"name"`
	} `json:"profile"`
}
