// Package http provides the HTTP webhook channel adapter.
// This adapter is always enabled and requires no external accounts.
// It exposes POST /channels/http/message on the gateway.
// Any external system can send a message to an agent by POSTing:
//
//	{
//	  "agent_id": "my-agent",
//	  "user_id":  "user-123",
//	  "text":     "Hello!"
//	}
//
// Responses are returned synchronously in the HTTP response body.
package http

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/pkg/message"
)

// Adapter is the HTTP webhook channel adapter.
type Adapter struct {
	inbox     chan<- message.Message
	mu        sync.Mutex             // guards responses
	responses map[string]chan string // correlates request IDs to response channels
	connected bool
}

// New creates a new HTTP channel adapter.
func New() *Adapter {
	return &Adapter{
		responses: make(map[string]chan string),
	}
}

func (a *Adapter) ID() string   { return "http" }
func (a *Adapter) Name() string { return "HTTP Webhook" }

func (a *Adapter) Start(_ context.Context, inbox chan<- message.Message) error {
	a.inbox = inbox
	a.connected = true
	// The HTTP adapter doesn't poll — it receives via gateway route injection.
	// See gateway/api.go which calls a.Receive() directly.
	return nil
}

// Receive is called by the gateway HTTP handler when a POST arrives on /channels/http/message.
// It injects the message into the inbox and returns a channel that will receive the agent's reply.
//
// The returned msgID is the key the caller MUST pass to Release() once it's
// done waiting (either after a reply arrives, or on context cancellation /
// timeout). Without Release the responses map leaks an entry per request
// that the engine failed to complete (PRODUCTION_AUDIT.md → CRITICAL).
func (a *Adapter) Receive(agentID, userID, username, text string) (<-chan string, string) {
	msgID := uuid.New().String()
	replyCh := make(chan string, 1)

	a.mu.Lock()
	a.responses[msgID] = replyCh
	a.mu.Unlock()

	msg := message.Message{
		ID:        msgID,
		SessionID: fmt.Sprintf("http-%s", userID),
		AgentID:   agentID,
		Channel:   "http",
		ThreadID:  userID,
		UserID:    userID,
		Username:  username,
		Role:      message.RoleUser,
		Parts:     message.Text(text),
		CreatedAt: time.Now().UTC(),
	}
	a.inbox <- msg
	return replyCh, msgID
}

// Release clears the pending response slot for `msgID`. Safe to call multiple
// times — subsequent calls are no-ops. Callers should `defer adapter.Release(id)`
// immediately after Receive so timeouts and engine errors don't leak entries.
func (a *Adapter) Release(msgID string) {
	a.mu.Lock()
	delete(a.responses, msgID)
	a.mu.Unlock()
}

// Send delivers the agent's reply to the waiting HTTP handler.
func (a *Adapter) Send(ctx context.Context, msg message.Message) error {
	if len(msg.Parts) == 0 {
		return nil
	}
	// Story 19a — honour the caller's context: a cancelled caller must not
	// have its reply delivered as if nothing happened.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("http: send: %w", err)
	}
	// Extract text from first part
	text := msg.Parts[0].Text

	a.mu.Lock()
	ch, ok := a.responses[msg.ID]
	if ok {
		delete(a.responses, msg.ID)
	}
	a.mu.Unlock()

	if ok {
		// Non-blocking — if the caller already gave up (context cancelled,
		// handler returned), the buffered channel still accepts one value;
		// if not buffered, this would block. Channel is buffered=1 in Receive
		// precisely to make this send safe.
		ch <- text
	}
	return nil
}

func (a *Adapter) Stop() error {
	a.connected = false
	return nil
}

func (a *Adapter) Status() channels.AdapterStatus {
	return channels.AdapterStatus{Connected: a.connected, Detail: "HTTP webhook always-on"}
}
