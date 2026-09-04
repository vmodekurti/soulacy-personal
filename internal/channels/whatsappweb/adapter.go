// Package whatsappweb implements an experimental WhatsApp Web channel adapter.
//
// It is intentionally separate from internal/channels/whatsapp, which uses the
// official Meta WhatsApp Business Cloud API. This adapter starts a Node sidecar
// that uses Baileys to link a WhatsApp Web session via QR code. It is useful for
// personal/local automation, but it is not an official WhatsApp Business API
// integration and should not be treated as production-safe for customer traffic.
package whatsappweb

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/pkg/message"
)

// Adapter bridges Soulacy to a WhatsApp Web sidecar process.
type Adapter struct {
	id         string
	command    string
	args       []string
	sessionDir string
	agentID    string
	accountID  string
	activation channels.ActivationPolicy
	log        *zap.Logger

	mu        sync.Mutex
	inbox     chan<- message.Message
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	connected bool
	detail    string
	qrCode    string
	lastError string
	stopOnce  sync.Once
}

// New creates an experimental WhatsApp Web adapter.
func New(id, command string, args []string, sessionDir, agentID, accountID string, activation channels.ActivationPolicy, log *zap.Logger) *Adapter {
	if id == "" {
		id = "whatsapp_web"
	}
	if command == "" {
		command = "node"
	}
	if log == nil {
		log = zap.NewNop()
	}
	return &Adapter{
		id:         id,
		command:    command,
		args:       args,
		sessionDir: sessionDir,
		agentID:    agentID,
		accountID:  accountID,
		activation: activation,
		log:        log.Named("whatsapp_web"),
		detail:     "not started",
	}
}

func (a *Adapter) ID() string   { return a.id }
func (a *Adapter) Name() string { return "WhatsApp Web (experimental)" }

func (a *Adapter) Start(ctx context.Context, inbox chan<- message.Message) error {
	if a.agentID == "" {
		return errors.New("whatsapp_web: agent_id is required")
	}
	if len(a.args) == 0 {
		return errors.New("whatsapp_web: args must include the sidecar script path")
	}
	a.inbox = inbox

	args := append([]string{}, a.args...)
	if a.sessionDir != "" {
		args = append(args, "--session-dir", a.sessionDir)
	}
	if a.accountID != "" {
		args = append(args, "--account-id", a.accountID)
	}
	args = append(args, "--channel-id", a.id)

	cmd := exec.CommandContext(ctx, a.command, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("whatsapp_web: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("whatsapp_web: stderr pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("whatsapp_web: stdin pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("whatsapp_web: start sidecar: %w", err)
	}

	a.mu.Lock()
	a.cmd = cmd
	a.stdin = stdin
	a.connected = false
	a.detail = "starting sidecar"
	a.qrCode = ""
	a.lastError = ""
	a.mu.Unlock()

	go a.readEvents(ctx, stdout)
	go a.readStderr(stderr)
	go a.waitSidecar(cmd)
	return nil
}

func (a *Adapter) readEvents(ctx context.Context, r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 1024*1024)
	for sc.Scan() {
		var ev sidecarEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			a.log.Warn("invalid sidecar event", zap.String("line", sc.Text()), zap.Error(err))
			continue
		}
		switch ev.Type {
		case "status":
			a.setStatus(ev.Connected, ev.Detail, "")
		case "qr":
			detail := "scan QR from Channels page or gateway logs"
			if ev.Detail != "" {
				detail = ev.Detail
			}
			a.setStatus(false, detail, ev.Value)
			a.log.Info("whatsapp_web QR received; scan it with WhatsApp Linked Devices")
		case "message":
			a.handleMessage(ctx, ev)
		case "error":
			detail := firstNonEmpty(ev.Detail, ev.Error)
			a.mu.Lock()
			a.lastError = detail
			a.mu.Unlock()
			a.setStatus(false, detail, "")
			a.log.Error("sidecar error", zap.String("detail", detail))
		}
	}
	if err := sc.Err(); err != nil {
		a.setStatus(false, "sidecar read error: "+err.Error(), "")
	}
}

func (a *Adapter) readStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		a.log.Warn("sidecar stderr", zap.String("line", sc.Text()))
	}
}

func (a *Adapter) waitSidecar(cmd *exec.Cmd) {
	err := cmd.Wait()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.connected = false
	a.qrCode = ""
	switch {
	case a.lastError != "":
		// Keep the sidecar's own explanation — "exit status 1" tells the
		// user nothing; "Missing @whiskeysockets/baileys…" tells them
		// everything.
		a.detail = a.lastError
	case err != nil:
		a.detail = "sidecar exited: " + err.Error()
	default:
		a.detail = "sidecar stopped"
	}
}

func (a *Adapter) handleMessage(ctx context.Context, ev sidecarEvent) {
	if strings.TrimSpace(ev.Text) == "" || strings.TrimSpace(ev.ChatID) == "" {
		return
	}
	userID := firstNonEmpty(ev.SenderID, ev.ChatID)
	text, ok := a.activation.Apply(ev.Text, ev.ChatID, userID, ev.IsGroup)
	if !ok {
		return
	}
	// The message will run the agent — show "typing…" in the chat while
	// the reply is generated (refreshed sidecar-side; cleared on send).
	a.sendTyping(ev.ChatID, "composing")
	username := firstNonEmpty(ev.SenderName, userID)
	created := time.Now().UTC()
	if ev.Timestamp > 0 {
		created = time.Unix(ev.Timestamp, 0).UTC()
	}
	msg := message.Message{
		ID:        firstNonEmpty(ev.ID, uuid.New().String()),
		SessionID: fmt.Sprintf("%s-%s", a.id, ev.ChatID),
		AgentID:   a.agentID,
		Channel:   a.id,
		ThreadID:  ev.ChatID,
		UserID:    userID,
		Username:  username,
		Role:      message.RoleUser,
		Parts:     message.Text(text),
		CreatedAt: created,
	}
	select {
	case a.inbox <- msg:
	case <-ctx.Done():
	default:
		a.log.Warn("inbox full, dropping WhatsApp Web message", zap.String("msg_id", msg.ID))
	}
}

func (a *Adapter) Send(ctx context.Context, msg message.Message) error {
	text := ""
	for _, p := range msg.Parts {
		if p.Text != "" {
			text = p.Text
			break
		}
	}
	if strings.TrimSpace(text) == "" || strings.TrimSpace(msg.ThreadID) == "" {
		return nil
	}
	a.mu.Lock()
	stdin := a.stdin
	a.mu.Unlock()
	if stdin == nil {
		return errors.New("whatsapp_web: sidecar stdin unavailable")
	}
	cmd := sidecarCommand{Type: "send", To: msg.ThreadID, Text: text}
	data, _ := json.Marshal(cmd)
	data = append(data, '\n')
	done := make(chan error, 1)
	go func() {
		_, err := stdin.Write(data)
		done <- err
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		if err != nil {
			return fmt.Errorf("whatsapp_web: send command: %w", err)
		}
		return nil
	}
}

func (a *Adapter) Stop() error {
	var err error
	a.stopOnce.Do(func() {
		a.mu.Lock()
		cmd := a.cmd
		stdin := a.stdin
		a.connected = false
		a.detail = "stopping"
		a.qrCode = ""
		a.mu.Unlock()
		if stdin != nil {
			_ = stdin.Close()
		}
		if cmd != nil && cmd.Process != nil {
			err = cmd.Process.Kill()
		}
	})
	return err
}

func (a *Adapter) Status() channels.AdapterStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return channels.AdapterStatus{Connected: a.connected, Detail: a.detail, QRCode: a.qrCode}
}

func (a *Adapter) setStatus(connected bool, detail, qr string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.connected = connected
	if detail != "" {
		a.detail = detail
	}
	if qr != "" || connected {
		a.qrCode = qr
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

type sidecarEvent struct {
	Type       string `json:"type"`
	Connected  bool   `json:"connected,omitempty"`
	Detail     string `json:"detail,omitempty"`
	Value      string `json:"value,omitempty"`
	Error      string `json:"error,omitempty"`
	ID         string `json:"id,omitempty"`
	ChatID     string `json:"chat_id,omitempty"`
	SenderID   string `json:"sender_id,omitempty"`
	SenderName string `json:"sender_name,omitempty"`
	Text       string `json:"text,omitempty"`
	Timestamp  int64  `json:"timestamp,omitempty"`
	IsGroup    bool   `json:"is_group,omitempty"`
}

type sidecarCommand struct {
	Type  string `json:"type"`
	To    string `json:"to"`
	Text  string `json:"text,omitempty"`
	State string `json:"state,omitempty"` // typing: "composing" | "paused"
}

// sendTyping best-effort signals a presence state ("composing" while the
// agent works, "paused" handled sidecar-side on reply). Failures are
// ignored — typing indicators are cosmetic.
func (a *Adapter) sendTyping(chatID, state string) {
	a.mu.Lock()
	stdin := a.stdin
	a.mu.Unlock()
	if stdin == nil || chatID == "" {
		return
	}
	data, _ := json.Marshal(sidecarCommand{Type: "typing", To: chatID, State: state})
	_, _ = stdin.Write(append(data, '\n'))
}
