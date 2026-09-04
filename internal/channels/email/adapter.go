// Package email delivers agent output over SMTP.
//
// Email is the most-requested missing channel: it's how an agent reaches a
// person who isn't in Slack/Telegram, and how a scheduled briefing lands
// somewhere durable. This adapter is outbound-only by design — inbound mail
// (IMAP polling) is a separate concern with its own failure modes, and is better
// served by the external sidecar protocol than by pulling an IMAP client into
// the core binary.
//
// It uses only the standard library (net/smtp), so it adds no dependencies.
//
// Config (channels.email):
//
//	host:          smtp.gmail.com          (required)
//	port:          587                     (default 587 = STARTTLS; 465 = implicit TLS)
//	username:      me@example.com
//	password:      <app password>          (stored in the vault, never inline)
//	from:          "Soulacy <me@example.com>"   (defaults to username)
//	default_output_to: someone@example.com  (fallback recipient)
//	subject:       "Soulacy update"        (default subject)
//	tls:           starttls | implicit | none

package email

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"time"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/pkg/message"
)

// TLS modes.
const (
	TLSStartTLS = "starttls" // plaintext connect, upgrade with STARTTLS (port 587)
	TLSImplicit = "implicit" // TLS from the first byte (port 465)
	TLSNone     = "none"     // no TLS — only sane for a local relay
)

const defaultTimeout = 20 * time.Second

// Adapter sends agent messages as email over SMTP.
type Adapter struct {
	id        string
	host      string
	port      int
	username  string
	password  string
	from      string
	defaultTo string
	subject   string
	tlsMode   string
	timeout   time.Duration

	mu        sync.RWMutex
	connected bool
	detail    string
}

// New builds an SMTP adapter.
func New(id, host string, port int, username, password, from, defaultTo, subject, tlsMode string, timeout time.Duration) (*Adapter, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		id = "email"
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, fmt.Errorf("email: host is required (e.g. smtp.gmail.com)")
	}
	if port <= 0 {
		port = 587
	}
	tlsMode = strings.ToLower(strings.TrimSpace(tlsMode))
	switch tlsMode {
	case "":
		// Infer from the port: 465 is implicit TLS by convention, else STARTTLS.
		if port == 465 {
			tlsMode = TLSImplicit
		} else {
			tlsMode = TLSStartTLS
		}
	case TLSStartTLS, TLSImplicit, TLSNone:
	default:
		return nil, fmt.Errorf("email: tls must be one of starttls|implicit|none, got %q", tlsMode)
	}
	from = strings.TrimSpace(from)
	if from == "" {
		from = strings.TrimSpace(username)
	}
	if from == "" {
		return nil, fmt.Errorf("email: from (or username) is required")
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if strings.TrimSpace(subject) == "" {
		subject = "Soulacy update"
	}

	return &Adapter{
		id: id, host: host, port: port,
		username: strings.TrimSpace(username), password: password,
		from: from, defaultTo: strings.TrimSpace(defaultTo),
		subject: subject, tlsMode: tlsMode, timeout: timeout,
	}, nil
}

func (a *Adapter) ID() string   { return a.id }
func (a *Adapter) Name() string { return "Email" }

// Start marks the adapter ready. Outbound-only: there is no inbound loop, so
// nothing is ever posted to inbox.
func (a *Adapter) Start(_ context.Context, _ chan<- message.Message) error {
	a.mu.Lock()
	a.connected = true
	a.detail = fmt.Sprintf("outbound via %s:%d (%s)", a.host, a.port, a.tlsMode)
	a.mu.Unlock()
	return nil
}

func (a *Adapter) Stop() error {
	a.mu.Lock()
	a.connected = false
	a.mu.Unlock()
	return nil
}

func (a *Adapter) Status() channels.AdapterStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return channels.AdapterStatus{Connected: a.connected, Detail: a.detail}
}

// Send delivers the message as an email. The recipient comes from the message's
// ThreadID (the routed destination), then metadata["to"], then the configured
// default — matching how every other channel resolves its destination.
func (a *Adapter) Send(ctx context.Context, msg message.Message) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("email: send: %w", err)
	}
	body := messageText(msg)
	if strings.TrimSpace(body) == "" {
		return nil // nothing to say
	}

	to := firstNonBlank(msg.ThreadID, msg.Metadata["to"], a.defaultTo)
	if to == "" {
		return fmt.Errorf("email: send: no recipient — set a destination on the mapping or a default_output_to")
	}
	subject := firstNonBlank(msg.Metadata["subject"], a.subject)

	raw := buildMessage(a.from, to, subject, body)
	return a.deliver(ctx, to, raw)
}

// deliver opens an SMTP conversation and hands over the message.
func (a *Adapter) deliver(ctx context.Context, to string, raw []byte) error {
	addr := net.JoinHostPort(a.host, fmt.Sprint(a.port))

	dialer := &net.Dialer{Timeout: a.timeout}
	var conn net.Conn
	var err error
	if a.tlsMode == TLSImplicit {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: a.host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("email: send: connect %s: %w", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(a.timeout))

	c, err := smtp.NewClient(conn, a.host)
	if err != nil {
		return fmt.Errorf("email: send: smtp handshake: %w", err)
	}
	defer c.Close()

	if a.tlsMode == TLSStartTLS {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: a.host, MinVersion: tls.VersionTLS12}); err != nil {
				return fmt.Errorf("email: send: starttls: %w", err)
			}
		}
	}

	// Only authenticate when credentials are supplied AND the link is encrypted —
	// sending a password over a plaintext link would be worse than failing.
	if a.username != "" && a.password != "" {
		if a.tlsMode == TLSNone {
			return fmt.Errorf("email: send: refusing to send credentials over an unencrypted connection (set tls: starttls or implicit)")
		}
		auth := smtp.PlainAuth("", a.username, a.password, a.host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("email: send: authentication failed (check the username/app password): %w", err)
		}
	}

	if err := c.Mail(addrOnly(a.from)); err != nil {
		return fmt.Errorf("email: send: sender rejected: %w", err)
	}
	for _, rcpt := range splitRecipients(to) {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("email: send: recipient %q rejected: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("email: send: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("email: send: write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("email: send: %w", err)
	}
	return c.Quit()
}

// buildMessage renders RFC-5322 headers + a plain-text body.
func buildMessage(from, to, subject, body string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + sanitizeHeader(subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	// Dot-stuffing: a line that is a lone "." would otherwise terminate the DATA
	// command early and truncate the message.
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, ".") {
			line = "." + line
		}
		b.WriteString(line + "\r\n")
	}
	return []byte(b.String())
}

// sanitizeHeader strips CR/LF so a message can't inject extra headers
// (a classic header-injection hole when the subject is agent-generated).
func sanitizeHeader(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

// addrOnly extracts the bare address from a "Name <addr>" form.
func addrOnly(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "<"); i >= 0 {
		if j := strings.Index(s[i:], ">"); j > 0 {
			return strings.TrimSpace(s[i+1 : i+j])
		}
	}
	return s
}

// splitRecipients allows a comma-separated destination list.
func splitRecipients(to string) []string {
	var out []string
	for _, part := range strings.Split(to, ",") {
		if a := addrOnly(part); a != "" {
			out = append(out, a)
		}
	}
	return out
}

func firstNonBlank(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// messageText flattens a message's text parts.
func messageText(msg message.Message) string {
	var b strings.Builder
	for _, p := range msg.Parts {
		if p.Type == message.ContentText && p.Text != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(p.Text)
		}
	}
	return b.String()
}
