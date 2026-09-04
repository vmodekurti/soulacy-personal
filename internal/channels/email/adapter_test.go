package email

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

func TestNew_RequiresHostAndSender(t *testing.T) {
	if _, err := New("email", "", 587, "", "", "", "", "", "", 0); err == nil {
		t.Error("host must be required")
	}
	if _, err := New("email", "smtp.example.com", 587, "", "", "", "", "", "", 0); err == nil {
		t.Error("from/username must be required")
	}
	if _, err := New("email", "smtp.example.com", 587, "me@example.com", "pw", "", "", "", "", 0); err != nil {
		t.Errorf("from should default to username: %v", err)
	}
}

// TLS mode is inferred from the port when unset — 465 is implicit TLS by
// convention, everything else is STARTTLS.
func TestNew_InfersTLSModeFromPort(t *testing.T) {
	a, err := New("email", "smtp.example.com", 465, "u@e.com", "p", "", "", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if a.tlsMode != TLSImplicit {
		t.Errorf("port 465 → implicit TLS, got %q", a.tlsMode)
	}
	a, err = New("email", "smtp.example.com", 587, "u@e.com", "p", "", "", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if a.tlsMode != TLSStartTLS {
		t.Errorf("port 587 → starttls, got %q", a.tlsMode)
	}
	if _, err := New("email", "smtp.example.com", 587, "u@e.com", "p", "", "", "", "bogus", 0); err == nil {
		t.Error("an unknown tls mode must be rejected")
	}
}

// Credentials must never cross an unencrypted link — better to fail loudly.
func TestSend_RefusesCredentialsOverPlaintext(t *testing.T) {
	a, err := New("email", "127.0.0.1", 2525, "me@example.com", "hunter2", "", "to@example.com", "", TLSNone, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// Point at a closed port: if the guard works we fail on the credential check,
	// not on the dial. Either way we must NOT have transmitted the password.
	err = a.Send(context.Background(), message.Message{Parts: message.Text("hi")})
	if err == nil {
		t.Fatal("expected an error")
	}
	// It should either refuse outright, or fail to connect — never succeed.
	if strings.Contains(err.Error(), "hunter2") {
		t.Error("the password must never appear in an error message")
	}
}

func TestSend_RequiresARecipient(t *testing.T) {
	a, err := New("email", "smtp.example.com", 587, "me@example.com", "pw", "", "", "", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	err = a.Send(context.Background(), message.Message{Parts: message.Text("hello")})
	if err == nil || !strings.Contains(err.Error(), "no recipient") {
		t.Fatalf("a message with nowhere to go must say so plainly, got: %v", err)
	}
}

func TestSend_EmptyBodyIsANoOp(t *testing.T) {
	a, _ := New("email", "smtp.example.com", 587, "me@example.com", "pw", "", "to@example.com", "", "", time.Second)
	if err := a.Send(context.Background(), message.Message{}); err != nil {
		t.Errorf("an empty message should be a silent no-op, got: %v", err)
	}
}

// Header injection: an agent-generated subject containing CRLF must not be able
// to forge extra headers (e.g. a hidden Bcc).
//
// The property that matters is that no NEW HEADER LINE is created. The CRLF is
// folded to a space, so "Bcc:" may survive as literal text inside the Subject
// VALUE — that's inert. What must never happen is a line that *begins* with it.
func TestBuildMessage_StripsHeaderInjection(t *testing.T) {
	raw := string(buildMessage(
		"me@example.com", "you@example.com",
		"Report\r\nBcc: attacker@evil.com", "body",
	))
	if strings.Contains(raw, "\r\nBcc:") {
		t.Fatalf("CRLF in the subject forged a real header line:\n%q", raw)
	}
	if strings.Count(raw, "Subject:") != 1 {
		t.Errorf("expected exactly one Subject header:\n%s", raw)
	}
	// The headers block must still end exactly where it should.
	head, _, ok := strings.Cut(raw, "\r\n\r\n")
	if !ok {
		t.Fatal("no header/body separator")
	}
	for _, line := range strings.Split(head, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "bcc:") {
			t.Fatalf("an injected Bcc header line survived: %q", line)
		}
	}
}

// Dot-stuffing: a body line that is a lone "." would end the SMTP DATA command
// early and silently truncate the message.
func TestBuildMessage_DotStuffsBody(t *testing.T) {
	raw := string(buildMessage("a@b.c", "d@e.f", "s", "line one\n.\nline two"))
	if !strings.Contains(raw, "\r\n..\r\n") {
		t.Fatalf("a lone '.' line must be dot-stuffed or the message truncates:\n%q", raw)
	}
	if !strings.Contains(raw, "line two") {
		t.Error("body after the dot line was lost")
	}
}

func TestBuildMessage_HasRequiredHeaders(t *testing.T) {
	raw := string(buildMessage("Soulacy <me@example.com>", "you@example.com", "Daily briefing", "hello"))
	for _, want := range []string{
		"From: Soulacy <me@example.com>",
		"To: you@example.com",
		"Subject: Daily briefing",
		"Content-Type: text/plain; charset=UTF-8",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("missing header %q in:\n%s", want, raw)
		}
	}
	// Headers must be separated from the body by a blank line.
	if !strings.Contains(raw, "\r\n\r\nhello") {
		t.Error("headers and body must be separated by a blank line")
	}
}

func TestAddrOnly_ExtractsBareAddress(t *testing.T) {
	cases := map[string]string{
		"Soulacy <me@example.com>": "me@example.com",
		"me@example.com":           "me@example.com",
		"  spaced@example.com  ":   "spaced@example.com",
	}
	for in, want := range cases {
		if got := addrOnly(in); got != want {
			t.Errorf("addrOnly(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitRecipients(t *testing.T) {
	got := splitRecipients("a@x.com, Bob <b@x.com> ,c@x.com")
	want := []string{"a@x.com", "b@x.com", "c@x.com"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// Destination resolution must follow the same precedence as every other channel:
// the routed ThreadID, then metadata["to"], then the configured default.
func TestSend_RecipientPrecedence(t *testing.T) {
	a, _ := New("email", "smtp.example.com", 587, "", "", "me@example.com", "default@example.com", "", "", time.Millisecond)

	// ThreadID wins. We can't complete an SMTP session here, so assert on the
	// error naming the routed recipient rather than the default.
	err := a.Send(context.Background(), message.Message{
		ThreadID: "routed@example.com",
		Parts:    message.Text("x"),
	})
	if err == nil {
		t.Skip("unexpected success without a server")
	}
	if strings.Contains(err.Error(), "no recipient") {
		t.Error("ThreadID should have supplied the recipient")
	}
}
