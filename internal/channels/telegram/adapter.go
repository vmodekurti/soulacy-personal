// Package telegram provides the Telegram Bot channel adapter.
// It uses long-polling (getUpdates) to receive messages and the sendMessage
// API to reply. No third-party library required — plain HTTP calls.
//
// Configuration in config.yaml:
//
//	channels:
//	  telegram:
//	    enabled: true
//	    token: "YOUR_BOT_TOKEN"
//	    agent_id: "my-agent"
//	    allowed_user_ids: [123456789, 987654321]  # whitelist; empty = allow all
//	    trigger_phrase: "!soulacy"                # empty = all messages
//	    ignore_groups: true
//	    outbound_only: false                      # true = send-only, no polling
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/pkg/message"
)

const apiBase = "https://api.telegram.org/bot"

// Adapter polls the Telegram Bot API.
type Adapter struct {
	id             string // adapter ID — "telegram" for the primary bot, "telegram-<agentID>" for extras
	token          string
	agentID        string
	allowedUserIDs map[int64]bool // nil/empty = allow everyone
	activation     channels.ActivationPolicy
	outboundOnly   bool
	client         *http.Client
	inbox          chan<- message.Message
	offset         int64
	connected      bool
	stopCh         chan struct{}
	stopOnce       sync.Once
}

// New creates a Telegram adapter for the primary (single-bot) configuration.
// The adapter's channel ID is "telegram". allowedUserIDs restricts which
// Telegram user IDs may interact with the bot — pass nil or empty to allow all.
func New(token, agentID string, allowedUserIDs []int64) *Adapter {
	return NewWithID("telegram", token, agentID, allowedUserIDs)
}

// NewWithID creates a Telegram adapter with a custom channel ID. Use this when
// running multiple bots — each bot should have a unique ID such as
// "telegram-system" or "telegram-financial-agent". The ID is embedded in every
// message's Channel field, which is how the registry routes outbound replies
// back to the correct bot.
func NewWithID(id, token, agentID string, allowedUserIDs []int64) *Adapter {
	userIDs := make([]string, 0, len(allowedUserIDs))
	for _, uid := range allowedUserIDs {
		userIDs = append(userIDs, strconv.FormatInt(uid, 10))
	}
	return NewWithIDAndActivation(id, token, agentID, allowedUserIDs, channels.ActivationPolicy{
		AllowedUserIDs: userIDs,
	})
}

// NewWithIDAndActivation creates a Telegram adapter with shared channel
// activation guardrails.
func NewWithIDAndActivation(id, token, agentID string, allowedUserIDs []int64, activation channels.ActivationPolicy) *Adapter {
	allowed := make(map[int64]bool, len(allowedUserIDs))
	for _, uid := range allowedUserIDs {
		allowed[uid] = true
	}
	if len(activation.AllowedUserIDs) == 0 {
		for _, uid := range allowedUserIDs {
			activation.AllowedUserIDs = append(activation.AllowedUserIDs, strconv.FormatInt(uid, 10))
		}
	}
	return &Adapter{
		id:             id,
		token:          token,
		agentID:        agentID,
		allowedUserIDs: allowed,
		activation:     activation,
		client:         &http.Client{Timeout: 35 * time.Second},
		stopCh:         make(chan struct{}),
	}
}

// SetOutboundOnly makes the adapter send-only. Start still marks the adapter
// connected so registry sends work, but it does not poll Telegram updates or
// route inbound messages to an agent.
func (a *Adapter) SetOutboundOnly(outboundOnly bool) {
	a.outboundOnly = outboundOnly
}

func (a *Adapter) ID() string   { return a.id }
func (a *Adapter) Name() string { return "Telegram" }

func (a *Adapter) Start(ctx context.Context, inbox chan<- message.Message) error {
	a.inbox = inbox
	a.connected = true
	if a.outboundOnly || a.agentID == "" {
		return nil
	}
	go a.poll(ctx)
	return nil
}

func (a *Adapter) poll(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.stopCh:
			return
		default:
		}

		updates, err := a.getUpdates(ctx)
		if err != nil {
			log.Printf("telegram: getUpdates error: %s — retrying in 5s", a.redact(err))
			select {
			case <-ctx.Done():
				return
			case <-a.stopCh:
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}

		for _, u := range updates {
			if u.Message == nil {
				a.offset = u.UpdateID + 1
				continue
			}

			// ── Allowlist check ───────────────────────────────────────────────
			if len(a.allowedUserIDs) > 0 && !a.allowedUserIDs[u.Message.From.ID] {
				log.Printf("telegram: blocked user %d (@%s) — not in allowed_user_ids",
					u.Message.From.ID, u.Message.From.Username)
				a.offset = u.UpdateID + 1
				continue
			}
			threadID := strconv.FormatInt(u.Message.Chat.ID, 10)
			userID := strconv.FormatInt(u.Message.From.ID, 10)
			text, ok := a.activation.Apply(u.Message.Text, threadID, userID, u.Message.Chat.Type != "private")
			if !ok {
				a.offset = u.UpdateID + 1
				continue
			}

			// Show "typing…" in the chat while the agent works (best
			// effort — Telegram clears it after ~5s or when we reply).
			go a.sendTyping(context.Background(), u.Message.Chat.ID)

			msg := message.Message{
				ID:        uuid.New().String(),
				SessionID: fmt.Sprintf("%s-%d", a.id, u.Message.Chat.ID),
				AgentID:   a.agentID,
				Channel:   a.id, // uses adapter's own ID so multi-bot replies route correctly
				ThreadID:  threadID,
				UserID:    userID,
				Username:  u.Message.From.Username,
				Role:      message.RoleUser,
				Parts:     message.Text(text),
				CreatedAt: time.Now().UTC(),
			}

			select {
			case a.inbox <- msg:
				a.offset = u.UpdateID + 1
			case <-ctx.Done():
				return
			case <-a.stopCh:
				return
			default:
				log.Printf("telegram: inbox full, dropping message %s", msg.ID)
				a.offset = u.UpdateID + 1
			}
		}
	}
}

// Send honours the caller's context (Story 19a): cancellation/deadline
// propagate into the Telegram HTTP request instead of being swallowed by a
// fresh context.Background().
func (a *Adapter) Send(ctx context.Context, msg message.Message) error {
	if len(msg.Parts) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("telegram: send: %w", err)
	}
	// S2.8 — Telegram rejects messages over 4096 chars. Split long replies so
	// they arrive as several messages instead of failing the whole send.
	chatID := mustParseInt64(msg.ThreadID)
	text := channels.PlainTextForMessaging(msg.Parts[0].Text)
	if text == "" {
		return nil
	}
	for _, chunk := range channels.SplitForLimit(text, 4096) {
		if err := a.sendText(ctx, chatID, chunk); err != nil {
			return err
		}
	}
	return nil
}

// sendTyping fires a one-shot "typing" chat action. Telegram displays it
// for ~5s; cosmetic, so errors are ignored.
func (a *Adapter) sendTyping(ctx context.Context, chatID int64) {
	payload, _ := json.Marshal(map[string]any{"chat_id": chatID, "action": "typing"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		apiBase+a.token+"/sendChatAction", bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if resp, err := a.client.Do(req); err == nil {
		resp.Body.Close()
	}
}

func (a *Adapter) sendText(ctx context.Context, chatID int64, text string) error {
	body := map[string]any{"chat_id": chatID, "text": text}
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		apiBase+a.token+"/sendMessage", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: send: %s", a.redact(err))
	}
	defer resp.Body.Close()
	// S2.8 — surface API rejections (e.g. 400 message too long, 429 rate limit)
	// instead of silently swallowing them.
	if resp.StatusCode >= 400 {
		if detail := telegramErrorDetail(resp.Body); detail != "" {
			return fmt.Errorf("telegram: send: API returned status %d: %s", resp.StatusCode, detail)
		}
		return fmt.Errorf("telegram: send: API returned status %d", resp.StatusCode)
	}
	return nil
}

func telegramErrorDetail(r io.Reader) string {
	if r == nil {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(r, 4096))
	if err != nil {
		return ""
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return ""
	}
	var env struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(data, &env); err == nil && strings.TrimSpace(env.Description) != "" {
		return strings.TrimSpace(env.Description)
	}
	return raw
}

func (a *Adapter) Stop() error {
	a.connected = false
	a.stopOnce.Do(func() { close(a.stopCh) })
	return nil
}

func (a *Adapter) Status() channels.AdapterStatus {
	if a.outboundOnly || a.agentID == "" {
		return channels.AdapterStatus{Connected: a.connected, Detail: "outbound-only"}
	}
	return channels.AdapterStatus{Connected: a.connected, Detail: "long-polling"}
}

func mustParseInt64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// --- Telegram API types ---

type update struct {
	UpdateID int64  `json:"update_id"`
	Message  *tgMsg `json:"message"`
}

type tgMsg struct {
	Text string `json:"text"`
	From tgUser `json:"from"`
	Chat tgChat `json:"chat"`
}

type tgUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type tgChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

func (a *Adapter) getUpdates(ctx context.Context) ([]update, error) {
	url := fmt.Sprintf("%s%s/getUpdates?offset=%d&timeout=30", apiBase, a.token, a.offset)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s", a.redact(err))
	}
	defer resp.Body.Close()

	var result struct {
		OK     bool     `json:"ok"`
		Result []update `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if !result.OK {
		return nil, fmt.Errorf("telegram: API returned ok=false")
	}
	return result.Result, nil
}

func (a *Adapter) redact(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if a.token != "" {
		s = strings.ReplaceAll(s, a.token, "<telegram-bot-token>")
	}
	return s
}
