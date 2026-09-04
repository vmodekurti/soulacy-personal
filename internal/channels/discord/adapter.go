// Package discord provides the Discord Gateway channel adapter.
// Uses Discord's Gateway WebSocket API (with reconnection) and REST for replies.
//
// Configuration:
//
//	channels:
//	  discord:
//	    enabled: true
//	    token: "Bot YOUR_BOT_TOKEN"
//	    agent_id: "my-agent"
//	    guild_id: ""     # empty = all guilds
package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/pkg/message"
)

const (
	discordAPI     = "https://discord.com/api/v10"
	discordGateway = "wss://gateway.discord.gg/?v=10&encoding=json"
	opDispatch     = 0
	opHeartbeat    = 1
	opIdentify     = 2
	opHeartbeatACK = 11
)

// Adapter connects to the Discord Gateway WebSocket.
type Adapter struct {
	id         string // "discord" for the primary bot; "discord-<agentID>" for extras
	token      string
	agentID    string
	guildID    string
	activation channels.ActivationPolicy
	client     *http.Client
	inbox      chan<- message.Message
	connected  bool
	stopCh     chan struct{}
	stopOnce   sync.Once // guards close(stopCh) so Stop() is idempotent
}

// New creates a Discord adapter with the default channel ID "discord".
func New(token, agentID, guildID string) *Adapter {
	return NewWithID("discord", token, agentID, guildID)
}

// NewWithID creates a Discord adapter with a custom channel ID. Use when
// running multiple bots — each bot needs a unique ID (e.g. "discord-system")
// so the registry can route replies back to the correct bot.
func NewWithID(id, token, agentID, guildID string) *Adapter {
	return NewWithIDAndActivation(id, token, agentID, guildID, channels.ActivationPolicy{})
}

// NewWithIDAndActivation creates a Discord adapter with shared channel
// activation guardrails.
func NewWithIDAndActivation(id, token, agentID, guildID string, activation channels.ActivationPolicy) *Adapter {
	return &Adapter{
		id:         id,
		token:      token,
		agentID:    agentID,
		guildID:    guildID,
		activation: activation,
		client:     &http.Client{Timeout: 10 * time.Second},
		stopCh:     make(chan struct{}),
	}
}

func (a *Adapter) ID() string   { return a.id }
func (a *Adapter) Name() string { return "Discord" }

func (a *Adapter) Start(ctx context.Context, inbox chan<- message.Message) error {
	a.inbox = inbox
	go a.connect(ctx)
	return nil
}

func (a *Adapter) connect(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.stopCh:
			return
		default:
		}

		if err := a.run(ctx); err != nil {
			log.Printf("discord: connection error: %v — reconnecting in 10s", err)
			a.connected = false
			select {
			case <-time.After(10 * time.Second):
			case <-ctx.Done():
				return
			case <-a.stopCh:
				return
			}
		}
	}
}

func (a *Adapter) run(ctx context.Context) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, discordGateway, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	var heartbeatInterval time.Duration
	var seq *int

	for {
		var payload struct {
			Op int             `json:"op"`
			D  json.RawMessage `json:"d"`
			S  *int            `json:"s"`
			T  string          `json:"t"`
		}
		if err := conn.ReadJSON(&payload); err != nil {
			return fmt.Errorf("read: %w", err)
		}
		if payload.S != nil {
			seq = payload.S
		}

		switch payload.Op {
		case 10: // Hello — start heartbeating and identify
			var hello struct {
				HeartbeatInterval int `json:"heartbeat_interval"`
			}
			_ = json.Unmarshal(payload.D, &hello)
			heartbeatInterval = time.Duration(hello.HeartbeatInterval) * time.Millisecond
			a.connected = true

			// Identify
			identify := map[string]any{
				"op": opIdentify,
				"d": map[string]any{
					"token":   discordGatewayToken(a.token),
					"intents": 1 << 9, // GUILD_MESSAGES + MESSAGE_CONTENT
					"properties": map[string]string{
						"os": "linux", "browser": "soulacy", "device": "soulacy",
					},
				},
			}
			_ = conn.WriteJSON(identify)

			// Start heartbeat loop
			go func() {
				ticker := time.NewTicker(heartbeatInterval)
				defer ticker.Stop()
				for range ticker.C {
					hb := map[string]any{"op": opHeartbeat, "d": seq}
					if err := conn.WriteJSON(hb); err != nil {
						return
					}
				}
			}()

		case opDispatch:
			if payload.T == "MESSAGE_CREATE" {
				var dm struct {
					ID        string `json:"id"`
					Content   string `json:"content"`
					ChannelID string `json:"channel_id"`
					GuildID   string `json:"guild_id"`
					Author    struct {
						ID       string `json:"id"`
						Username string `json:"username"`
						Bot      bool   `json:"bot"`
					} `json:"author"`
				}
				_ = json.Unmarshal(payload.D, &dm)
				if dm.Author.Bot || dm.Content == "" {
					continue
				}
				if a.guildID != "" && dm.GuildID != a.guildID {
					continue
				}
				text, ok := a.activation.Apply(dm.Content, dm.ChannelID, dm.Author.ID, dm.GuildID != "")
				if !ok {
					continue
				}
				msg := message.Message{
					ID:        uuid.New().String(),
					SessionID: fmt.Sprintf("%s-%s", a.id, dm.ChannelID),
					AgentID:   a.agentID,
					Channel:   a.id, // adapter's own ID for correct multi-bot reply routing
					ThreadID:  dm.ChannelID,
					UserID:    dm.Author.ID,
					Username:  dm.Author.Username,
					Role:      message.RoleUser,
					Parts:     message.Text(text),
					CreatedAt: time.Now().UTC(),
				}
				// Non-blocking send so a wedged engine doesn't wedge the
				// Discord gateway socket loop too. (PRODUCTION_AUDIT → HIGH)
				select {
				case a.inbox <- msg:
				case <-ctx.Done():
					return nil
				case <-a.stopCh:
					return nil
				default:
					log.Printf("discord: inbox full, dropping message %s", msg.ID)
				}
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-a.stopCh:
			return nil
		default:
		}
	}
}

// Send honours the caller's context (Story 19a follow-up): cancellation
// propagates into the Discord HTTP request and errors wrap the ctx error.
func (a *Adapter) Send(ctx context.Context, msg message.Message) error {
	if len(msg.Parts) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("discord: send: %w", err)
	}
	// S2.8 — Discord rejects messages over 2000 chars. Split long replies so
	// they arrive as several messages instead of failing the whole send.
	for _, chunk := range channels.SplitForLimit(msg.Parts[0].Text, 2000) {
		if err := a.sendChunk(ctx, msg.ThreadID, chunk); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) sendChunk(ctx context.Context, channelID, text string) error {
	body, _ := json.Marshal(map[string]string{"content": text})
	url := fmt.Sprintf("%s/channels/%s/messages", discordAPI, channelID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", discordRESTAuth(a.token))
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("discord: send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("discord: send: API returned status %d: %s", resp.StatusCode, discordErrorDetail(bodyBytes))
	}
	return nil
}

func discordGatewayToken(token string) string {
	token = strings.TrimSpace(token)
	if strings.HasPrefix(strings.ToLower(token), "bot ") {
		return strings.TrimSpace(token[4:])
	}
	return token
}

func discordRESTAuth(token string) string {
	token = strings.TrimSpace(token)
	if strings.HasPrefix(strings.ToLower(token), "bot ") {
		return "Bot " + strings.TrimSpace(token[4:])
	}
	return "Bot " + token
}

func discordErrorDetail(body []byte) string {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return "empty response"
	}
	var parsed struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Message != "" {
		if parsed.Code != 0 {
			return fmt.Sprintf("%s (code %d)", parsed.Message, parsed.Code)
		}
		return parsed.Message
	}
	return string(body)
}

func (a *Adapter) Stop() error {
	a.connected = false
	a.stopOnce.Do(func() { close(a.stopCh) })
	return nil
}

func (a *Adapter) Status() channels.AdapterStatus {
	return channels.AdapterStatus{Connected: a.connected, Detail: "gateway websocket"}
}
