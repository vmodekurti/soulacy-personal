// Package slack provides the Slack Socket Mode channel adapter.
// Socket Mode lets Slack send events over a WebSocket so no public URL is needed.
//
// Configuration:
//
//	channels:
//	  slack:
//	    enabled: true
//	    bot_token: "xoxb-..."
//	    app_token: "xapp-..."   # Socket Mode app-level token
//	    agent_id: "my-agent"
package slack

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

var slackAPI = "https://slack.com/api"

// Adapter implements the Slack Socket Mode protocol.
type Adapter struct {
	id         string // "slack" for the primary bot; "slack-<agentID>" for extras
	botToken   string
	appToken   string
	agentID    string
	activation channels.ActivationPolicy
	client     *http.Client
	inbox      chan<- message.Message
	connected  bool
	stopCh     chan struct{}
	stopOnce   sync.Once // guards close(stopCh) so Stop() is idempotent
}

// New creates a Slack adapter with the default channel ID "slack".
func New(botToken, appToken, agentID string) *Adapter {
	return NewWithID("slack", botToken, appToken, agentID)
}

// NewWithID creates a Slack adapter with a custom channel ID. Use when
// running multiple bots — each bot needs a unique ID (e.g. "slack-system")
// so the registry can route replies back to the correct bot.
func NewWithID(id, botToken, appToken, agentID string) *Adapter {
	return NewWithIDAndActivation(id, botToken, appToken, agentID, channels.ActivationPolicy{})
}

// NewWithIDAndActivation creates a Slack adapter with shared channel
// activation guardrails.
func NewWithIDAndActivation(id, botToken, appToken, agentID string, activation channels.ActivationPolicy) *Adapter {
	return &Adapter{
		id:         id,
		botToken:   botToken,
		appToken:   appToken,
		agentID:    agentID,
		activation: activation,
		client:     &http.Client{Timeout: 10 * time.Second},
		stopCh:     make(chan struct{}),
	}
}

func (a *Adapter) ID() string   { return a.id }
func (a *Adapter) Name() string { return "Slack" }

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

		wsURL, err := a.openConnection(ctx)
		if err != nil {
			log.Printf("slack: open connection error: %v — retrying in 10s", err)
			select {
			case <-ctx.Done():
				return
			case <-a.stopCh:
				return
			case <-time.After(10 * time.Second):
			}
			continue
		}

		if err := a.run(ctx, wsURL); err != nil {
			log.Printf("slack: run error: %v — reconnecting in 5s", err)
			a.connected = false
			select {
			case <-ctx.Done():
				return
			case <-a.stopCh:
				return
			case <-time.After(5 * time.Second):
			}
		}
	}
}

// openConnection calls apps.connections.open to get a fresh WebSocket URL.
func (a *Adapter) openConnection(ctx context.Context) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		slackAPI+"/apps.connections.open", nil)
	req.Header.Set("Authorization", "Bearer "+a.appToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := a.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		OK  bool   `json:"ok"`
		URL string `json:"url"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if !result.OK {
		return "", fmt.Errorf("slack: apps.connections.open failed")
	}
	return result.URL, nil
}

func (a *Adapter) run(ctx context.Context, wsURL string) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial %s: %w", wsURL, err)
	}
	defer conn.Close()
	a.connected = true

	for {
		var envelope struct {
			EnvelopeID string          `json:"envelope_id"`
			Type       string          `json:"type"`
			Payload    json.RawMessage `json:"payload"`
		}
		if err := conn.ReadJSON(&envelope); err != nil {
			return fmt.Errorf("read: %w", err)
		}

		// Acknowledge every envelope
		if envelope.EnvelopeID != "" {
			_ = conn.WriteJSON(map[string]string{"envelope_id": envelope.EnvelopeID})
		}

		if envelope.Type == "events_api" {
			var ep struct {
				Event struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					User        string `json:"user"`
					Channel     string `json:"channel"`
					ChannelType string `json:"channel_type"`
					TS          string `json:"ts"`
					BotID       string `json:"bot_id"`
				} `json:"event"`
			}
			_ = json.Unmarshal(envelope.Payload, &ep)

			if (ep.Event.Type == "message" || ep.Event.Type == "app_mention") && ep.Event.BotID == "" {
				text := ep.Event.Text
				isGroup := slackEventIsGroup(ep.Event.Type, ep.Event.ChannelType)
				if ep.Event.Type == "app_mention" {
					// Slack delivers explicit bot mentions as app_mention events
					// with text like "<@U123> weather?". Treat the mention itself
					// as the activation signal and strip it before handing text to
					// the agent, otherwise channel bots look connected but never run.
					text = stripLeadingSlackMention(text)
				}
				text, ok := a.activation.Apply(text, ep.Event.Channel, ep.Event.User, isGroup)
				if !ok {
					continue
				}
				msg := message.Message{
					ID:        uuid.New().String(),
					SessionID: fmt.Sprintf("%s-%s", a.id, ep.Event.Channel),
					AgentID:   a.agentID,
					Channel:   a.id, // adapter's own ID for correct multi-bot reply routing
					ThreadID:  ep.Event.Channel,
					UserID:    ep.Event.User,
					Username:  ep.Event.User,
					Role:      message.RoleUser,
					Parts:     message.Text(text),
					Metadata:  map[string]string{"ts": ep.Event.TS},
					CreatedAt: time.Now().UTC(),
				}
				// Non-blocking send so a wedged engine doesn't wedge the
				// Slack socket loop too. (PRODUCTION_AUDIT → HIGH)
				select {
				case a.inbox <- msg:
				case <-ctx.Done():
					return nil
				case <-a.stopCh:
					return nil
				default:
					log.Printf("slack: inbox full, dropping message %s", msg.ID)
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

// Send honours the caller's context (Story 19a): cancellation/deadline
// propagate into the Slack HTTP request.
func (a *Adapter) Send(ctx context.Context, msg message.Message) error {
	if len(msg.Parts) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("slack: send: %w", err)
	}
	body, _ := json.Marshal(map[string]string{
		"channel": msg.ThreadID,
		"text":    msg.Parts[0].Text,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, slackAPI+"/chat.postMessage", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+a.botToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("slack: send: API returned status %d: %s", resp.StatusCode, slackErrorDetail(bodyBytes))
	}
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if len(bodyBytes) > 0 && json.Unmarshal(bodyBytes, &result) == nil && !result.OK {
		if result.Error == "" {
			result.Error = "unknown_error"
		}
		return fmt.Errorf("slack: send: %s", result.Error)
	}
	return nil
}

func stripLeadingSlackMention(text string) string {
	text = strings.TrimSpace(text)
	for strings.HasPrefix(text, "<@") {
		end := strings.Index(text, ">")
		if end < 0 {
			break
		}
		text = strings.TrimSpace(text[end+1:])
	}
	return text
}

func slackEventIsGroup(eventType, channelType string) bool {
	if eventType == "app_mention" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(channelType)) {
	case "im":
		return false
	case "channel", "group", "mpim":
		return true
	default:
		// Be conservative for older/partial payloads: require the trigger phrase
		// unless Slack explicitly says this is a direct message.
		return true
	}
}

func slackErrorDetail(body []byte) string {
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if len(body) > 0 && json.Unmarshal(body, &result) == nil && result.Error != "" {
		return result.Error
	}
	if s := strings.TrimSpace(string(body)); s != "" {
		return s
	}
	return "unknown_error"
}

func (a *Adapter) Stop() error {
	a.connected = false
	a.stopOnce.Do(func() { close(a.stopCh) })
	return nil
}

func (a *Adapter) Status() channels.AdapterStatus {
	return channels.AdapterStatus{Connected: a.connected, Detail: "socket mode"}
}
