package external

import (
	"bufio"
	"context"
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

const (
	defaultHandshakeTimeout = 5 * time.Second
	shutdownGrace           = 3 * time.Second
)

// Adapter bridges Soulacy to an External Channel Protocol sidecar — a
// subprocess in any language speaking NDJSON frames on stdio. Satisfies
// channels.Adapter; supervision (restart/backoff) is layered on in E4.
type Adapter struct {
	id         string
	command    string
	args       []string
	agentID    string
	activation channels.ActivationPolicy
	log        *zap.Logger

	// handshakeTimeout is how long the sidecar has to send hello.
	// Overridable in tests.
	handshakeTimeout time.Duration

	// env, when non-nil, is the COMPLETE environment for the sidecar process
	// (the parent environment is NOT inherited). Used for credential
	// delegation (E6): a minimal base env plus only the declared secrets.
	env []string

	// sharedDir, when set, is advertised in hello_ack (Story E24 shared
	// mounts): the absolute per-run scratch directory where large
	// attachments move as files instead of inline frame payloads.
	sharedDir string

	mu          sync.Mutex
	inbox       chan<- message.Message
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	connected   bool
	detail      string
	qrCode      string
	sidecarName string
	negotiated  int
	stopOnce    sync.Once
	exited      chan struct{}
	// terminal marks the detail as a final failure verdict (handshake
	// timeout, protocol rejection) that waitExit must not overwrite.
	terminal bool
}

// New creates an external channel adapter. command+args spawn the sidecar;
// agentID receives inbound messages; activation filters them.
func New(id, command string, args []string, agentID string, activation channels.ActivationPolicy, log *zap.Logger) *Adapter {
	if id == "" {
		id = "external"
	}
	if log == nil {
		log = zap.NewNop()
	}
	return &Adapter{
		id:               id,
		command:          command,
		args:             args,
		agentID:          agentID,
		activation:       activation,
		log:              log.Named("external." + id),
		handshakeTimeout: defaultHandshakeTimeout,
		detail:           "not started",
	}
}

func (a *Adapter) ID() string { return a.id }

// SetEnv fixes the sidecar's complete environment (no parent inheritance).
// Must be called before Start. nil (the default) inherits the parent env,
// preserving pre-E6 behaviour. Values are passed to the process only —
// never logged or written to disk.
func (a *Adapter) SetEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.env = env
}

// SetSharedDir advertises a per-run shared scratch directory to the
// sidecar in hello_ack (Story E24). Must be called before Start.
func (a *Adapter) SetSharedDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sharedDir = dir
}

// Name reports the sidecar-announced name once the handshake completed.
func (a *Adapter) Name() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sidecarName != "" {
		return fmt.Sprintf("External: %s", a.sidecarName)
	}
	return "External channel (" + a.id + ")"
}

// Start spawns the sidecar and begins the handshake. Non-blocking: progress
// is reported through Status(), matching other adapters.
func (a *Adapter) Start(ctx context.Context, inbox chan<- message.Message) error {
	if a.agentID == "" {
		return errors.New("external: agent_id is required")
	}
	if a.command == "" {
		return errors.New("external: sidecar command is required")
	}
	a.mu.Lock()
	a.inbox = inbox
	a.mu.Unlock()

	cmd := exec.CommandContext(ctx, a.command, a.args...)
	a.mu.Lock()
	if a.env != nil {
		cmd.Env = a.env
	}
	a.mu.Unlock()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("external: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("external: stderr pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("external: stdin pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("external: start sidecar: %w", err)
	}

	a.mu.Lock()
	a.cmd = cmd
	a.stdin = stdin
	a.connected = false
	a.detail = "waiting for sidecar hello"
	a.terminal = false
	a.exited = make(chan struct{})
	a.mu.Unlock()

	go a.readLoop(ctx, stdout)
	go a.drainStderr(stderr)
	go a.waitExit(cmd)
	return nil
}

// readLoop performs the handshake, then dispatches frames until EOF.
func (a *Adapter) readLoop(ctx context.Context, r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 1024*1024)

	// ── handshake phase ──────────────────────────────────────────────
	// The sidecar must send hello within handshakeTimeout. The timer kills
	// the process so the scanner unblocks; unknown frames are skipped.
	handshook := make(chan struct{})
	timer := time.AfterFunc(a.handshakeTimeout, func() {
		select {
		case <-handshook:
		default:
			a.setFailure("handshake timeout: sidecar never sent hello")
			a.killProcess()
		}
	})
	defer timer.Stop()

	helloDone := false
	for !helloDone && sc.Scan() {
		f, err := ParseFrame(sc.Bytes())
		if err != nil {
			continue
		}
		if f.Type != "hello" {
			continue // ignore pre-hello noise (forward compatibility)
		}
		v, err := Negotiate(f)
		if err != nil {
			a.setFailure("protocol negotiation failed: " + err.Error())
			a.killProcess()
			close(handshook)
			return
		}
		a.mu.Lock()
		a.sidecarName = f.Name
		a.negotiated = v
		stdin := a.stdin
		sharedDir := a.sharedDir
		a.mu.Unlock()
		if stdin != nil {
			if err := WriteFrame(stdin, Frame{Type: "hello_ack", Protocol: v, SharedDir: sharedDir}); err != nil {
				a.setStatus(false, "hello_ack write failed: "+err.Error(), "")
				close(handshook)
				return
			}
		}
		a.setStatus(false, fmt.Sprintf("handshake complete (protocol %d), waiting for platform", v), "")
		helloDone = true
	}
	close(handshook)
	if !helloDone {
		return // EOF before hello; waitExit/timeout set the status
	}

	// ── frame dispatch phase ─────────────────────────────────────────
	for sc.Scan() {
		f, err := ParseFrame(sc.Bytes())
		if err != nil {
			a.log.Warn("invalid frame", zap.String("line", sc.Text()), zap.Error(err))
			continue
		}
		switch f.Type {
		case "status":
			a.setStatus(f.Connected, f.Detail, f.QR)
		case "message":
			a.handleMessage(ctx, f)
		case "error":
			a.setStatus(false, firstNonEmpty(f.Detail, f.Error, "sidecar error"), "")
			a.log.Error("sidecar error", zap.String("detail", firstNonEmpty(f.Detail, f.Error)))
		default:
			// Unknown frame types are ignored (forward compatibility).
		}
	}
	if err := sc.Err(); err != nil {
		a.setStatus(false, "sidecar read error: "+err.Error(), "")
	}
}

func (a *Adapter) drainStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		a.log.Warn("sidecar stderr", zap.String("line", sc.Text()))
	}
}

func (a *Adapter) waitExit(cmd *exec.Cmd) {
	err := cmd.Wait()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.connected = false
	a.qrCode = ""
	if a.exited != nil {
		close(a.exited)
	}
	if a.terminal {
		return // keep the failure verdict (handshake timeout etc.)
	}
	if err != nil {
		a.detail = "sidecar exited: " + err.Error()
	} else {
		a.detail = "sidecar stopped"
	}
}

// setFailure records a terminal failure detail that survives process exit.
func (a *Adapter) setFailure(detail string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.connected = false
	a.detail = detail
	a.qrCode = ""
	a.terminal = true
}

func (a *Adapter) handleMessage(ctx context.Context, f Frame) {
	if strings.TrimSpace(f.Text) == "" || strings.TrimSpace(f.ChatID) == "" {
		return
	}
	userID := firstNonEmpty(f.SenderID, f.ChatID)
	text, ok := a.activation.Apply(f.Text, f.ChatID, userID, f.IsGroup)
	if !ok {
		return
	}
	created := time.Now().UTC()
	if f.Timestamp > 0 {
		created = time.Unix(f.Timestamp, 0).UTC()
	}
	msg := message.Message{
		ID:        firstNonEmpty(f.ID, uuid.New().String()),
		SessionID: fmt.Sprintf("%s-%s", a.id, f.ChatID),
		AgentID:   a.agentID,
		Channel:   a.id,
		ThreadID:  f.ChatID,
		UserID:    userID,
		Username:  firstNonEmpty(f.SenderName, userID),
		Role:      message.RoleUser,
		Parts:     message.Text(text),
		CreatedAt: created,
	}
	a.mu.Lock()
	inbox := a.inbox
	a.mu.Unlock()
	if inbox == nil {
		return
	}
	select {
	case inbox <- msg:
	case <-ctx.Done():
	default:
		a.log.Warn("inbox full, dropping external message", zap.String("msg_id", msg.ID))
	}
}

// Send writes a send frame to the sidecar.
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
		return errors.New("external: sidecar stdin unavailable")
	}
	done := make(chan error, 1)
	go func() { done <- WriteFrame(stdin, Frame{Type: "send", To: msg.ThreadID, Text: text}) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		if err != nil {
			return fmt.Errorf("external: send frame: %w", err)
		}
		return nil
	}
}

// Stop sends shutdown, waits briefly for a clean exit, then kills.
func (a *Adapter) Stop() error {
	var err error
	a.stopOnce.Do(func() {
		a.mu.Lock()
		stdin := a.stdin
		cmd := a.cmd
		a.connected = false
		a.detail = "stopping"
		a.qrCode = ""
		a.mu.Unlock()

		a.mu.Lock()
		exited := a.exited
		a.mu.Unlock()

		if stdin != nil {
			_ = WriteFrame(stdin, Frame{Type: "shutdown"})
			_ = stdin.Close()
		}
		if cmd != nil && cmd.Process != nil && exited != nil {
			// waitExit owns cmd.Wait(); we just watch for it to finish.
			select {
			case <-exited:
			case <-time.After(shutdownGrace):
				err = cmd.Process.Kill()
			}
		}
	})
	return err
}

// Done returns a channel closed when the sidecar process exits. Nil before
// Start. Used by the supervisor (E4) to drive restarts.
func (a *Adapter) Done() <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.exited
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

func (a *Adapter) killProcess() {
	a.mu.Lock()
	cmd := a.cmd
	a.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
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
