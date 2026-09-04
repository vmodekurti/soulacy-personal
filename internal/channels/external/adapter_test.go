package external

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/pkg/message"
)

// TestHelperSidecar is not a real test: it's re-executed as the sidecar
// subprocess by the adapter tests (standard os/exec helper-process pattern).
// The mode env var selects its behaviour.
func TestHelperSidecar(t *testing.T) {
	mode := os.Getenv("GO_EXTERNAL_SIDECAR")
	if mode == "" {
		t.Skip("helper process only")
	}
	defer os.Exit(0)

	out := os.Stdout
	in := bufio.NewScanner(os.Stdin)

	switch mode {
	case "nohello":
		time.Sleep(10 * time.Second)
		return
	case "badversion":
		fmt.Fprintln(out, `{"type":"hello","protocol":0,"name":"bad"}`)
		time.Sleep(5 * time.Second)
		return
	case "crashloop":
		// Valid handshake, then die fast — exercises supervised restarts.
		fmt.Fprintln(out, `{"type":"hello","protocol":1,"name":"crashy"}`)
		time.Sleep(50 * time.Millisecond)
		os.Exit(1)
	case "crashafter":
		// Stays "healthy" long enough to trip the healthy-reset window.
		fmt.Fprintln(out, `{"type":"hello","protocol":1,"name":"flaky"}`)
		time.Sleep(300 * time.Millisecond)
		os.Exit(1)
	case "shareddir":
		// Echoes the hello_ack shared_dir into the status detail (Story
		// E24 shared mounts: contract proof for the ECP side).
		fmt.Fprintln(out, `{"type":"hello","protocol":1,"name":"shareddir"}`)
		for in.Scan() {
			f, err := ParseFrame(in.Bytes())
			if err != nil {
				continue
			}
			switch f.Type {
			case "hello_ack":
				fmt.Fprintf(out, `{"type":"status","connected":true,"detail":"shared=%s"}`+"\n", f.SharedDir)
			case "shutdown":
				return
			}
		}
		return
	case "envecho":
		// Reports selected env vars in the status detail so tests can prove
		// which variables the host injected (E6 credential delegation).
		fmt.Fprintln(out, `{"type":"hello","protocol":1,"name":"envecho"}`)
		for in.Scan() {
			f, err := ParseFrame(in.Bytes())
			if err != nil {
				continue
			}
			switch f.Type {
			case "hello_ack":
				tok := os.Getenv("SIDE_TOKEN")
				undecl, ok := os.LookupEnv("SIDE_UNDECLARED")
				if !ok {
					undecl = "<unset>"
				}
				fmt.Fprintf(out, `{"type":"status","connected":true,"detail":"token=%s undeclared=%s"}`+"\n", tok, undecl)
			case "shutdown":
				return
			}
		}
		return
	}

	// happy mode: noise frame first (must be ignored), then hello.
	fmt.Fprintln(out, `{"type":"future.metric","x":1}`)
	fmt.Fprintln(out, `{"type":"hello","protocol":1,"name":"fake-platform","capabilities":["send"]}`)

	for in.Scan() {
		f, err := ParseFrame(in.Bytes())
		if err != nil {
			continue
		}
		switch f.Type {
		case "hello_ack":
			fmt.Fprintln(out, `{"type":"status","connected":true,"detail":"linked to fake platform"}`)
			fmt.Fprintln(out, `{"type":"message","id":"m1","chat_id":"c1","sender_id":"u1","sender_name":"Ada","text":"hello from platform","timestamp":1765000000}`)
		case "send":
			// Echo outbound back as a new inbound message (proves the
			// send path end-to-end).
			fmt.Fprintf(out, `{"type":"message","id":"m2","chat_id":%q,"sender_id":"u1","text":"echo:%s"}`+"\n", f.To, f.Text)
		case "shutdown":
			return
		}
	}
}

func helperAdapter(t *testing.T, mode string, activation channels.ActivationPolicy) (*Adapter, chan message.Message) {
	t.Helper()
	t.Setenv("GO_EXTERNAL_SIDECAR", mode)
	a := New("fake", os.Args[0], []string{"-test.run=TestHelperSidecar"}, "agent-1", activation, zap.NewNop())
	a.handshakeTimeout = 2 * time.Second
	inbox := make(chan message.Message, 16)
	if err := a.Start(context.Background(), inbox); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop() })
	return a, inbox
}

func waitStatus(t *testing.T, a *Adapter, want func(channels.AdapterStatus) bool, what string) channels.AdapterStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last channels.AdapterStatus
	for time.Now().Before(deadline) {
		last = a.Status()
		if want(last) {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; last status: %+v", what, last)
	return last
}

func TestAdapter_HandshakeAndInboundMessage(t *testing.T) {
	a, inbox := helperAdapter(t, "happy", channels.ActivationPolicy{})

	st := waitStatus(t, a, func(s channels.AdapterStatus) bool { return s.Connected }, "connected status")
	if !strings.Contains(st.Detail, "linked") {
		t.Errorf("detail = %q", st.Detail)
	}

	select {
	case msg := <-inbox:
		if msg.AgentID != "agent-1" || msg.Channel != "fake" || msg.ThreadID != "c1" {
			t.Errorf("message identity = %+v", msg)
		}
		if msg.UserID != "u1" || msg.Username != "Ada" {
			t.Errorf("sender = %s/%s", msg.UserID, msg.Username)
		}
		text := ""
		for _, p := range msg.Parts {
			text += p.Text
		}
		if text != "hello from platform" {
			t.Errorf("text = %q", text)
		}
		if msg.SessionID != "fake-c1" {
			t.Errorf("session = %q", msg.SessionID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("inbound message never arrived")
	}
}

func TestAdapter_SendRoundTrip(t *testing.T) {
	a, inbox := helperAdapter(t, "happy", channels.ActivationPolicy{})
	waitStatus(t, a, func(s channels.AdapterStatus) bool { return s.Connected }, "connected")
	<-inbox // drain the greeting message

	err := a.Send(context.Background(), message.Message{
		ThreadID: "c1",
		Parts:    message.Text("reply text"),
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case msg := <-inbox:
		text := ""
		for _, p := range msg.Parts {
			text += p.Text
		}
		if text != "echo:reply text" {
			t.Errorf("echo = %q", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("send echo never arrived")
	}
}

func TestAdapter_ActivationPolicyFilters(t *testing.T) {
	// Trigger phrase required; the greeting lacks it → never reaches inbox.
	a, inbox := helperAdapter(t, "happy", channels.ActivationPolicy{TriggerPhrase: "!soulacy"})
	waitStatus(t, a, func(s channels.AdapterStatus) bool { return s.Connected }, "connected")

	select {
	case msg := <-inbox:
		t.Fatalf("filtered message leaked through: %+v", msg)
	case <-time.After(400 * time.Millisecond):
		// good — filtered
	}
}

func TestAdapter_HandshakeTimeout(t *testing.T) {
	a, _ := helperAdapter(t, "nohello", channels.ActivationPolicy{})
	st := waitStatus(t, a, func(s channels.AdapterStatus) bool {
		return !s.Connected && strings.Contains(s.Detail, "handshake")
	}, "handshake timeout status")
	if st.Connected {
		t.Error("must not be connected")
	}
}

func TestAdapter_BadProtocolVersion(t *testing.T) {
	a, _ := helperAdapter(t, "badversion", channels.ActivationPolicy{})
	waitStatus(t, a, func(s channels.AdapterStatus) bool {
		return !s.Connected && strings.Contains(s.Detail, "protocol")
	}, "protocol rejection status")
}

func TestAdapter_StopShutsDownSidecar(t *testing.T) {
	a, _ := helperAdapter(t, "happy", channels.ActivationPolicy{})
	waitStatus(t, a, func(s channels.AdapterStatus) bool { return s.Connected }, "connected")
	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitStatus(t, a, func(s channels.AdapterStatus) bool { return !s.Connected }, "disconnected after stop")
}

func TestAdapter_NameFromSidecar(t *testing.T) {
	a, _ := helperAdapter(t, "happy", channels.ActivationPolicy{})
	waitStatus(t, a, func(s channels.AdapterStatus) bool { return s.Connected }, "connected")
	if name := a.Name(); !strings.Contains(name, "fake-platform") {
		t.Errorf("Name() = %q, want sidecar-announced name", name)
	}
}
