package external

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/internal/sandbox"
	"github.com/soulacy/soulacy/pkg/message"
)

func helperSupervisor(t *testing.T, mode string, cfg SupervisorConfig) (*Supervisor, chan message.Message) {
	t.Helper()
	t.Setenv("GO_EXTERNAL_SIDECAR", mode)
	s := NewSupervisor("fake", os.Args[0], []string{"-test.run=TestHelperSidecar"},
		"agent-1", channels.ActivationPolicy{}, zap.NewNop(), cfg)
	s.handshakeTimeout = 2 * time.Second
	inbox := make(chan message.Message, 32)
	if err := s.Start(context.Background(), inbox); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	return s, inbox
}

func waitSupervisor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSupervisor_RestartsCrashingSidecar(t *testing.T) {
	s, _ := helperSupervisor(t, "crashloop", SupervisorConfig{
		MinBackoff:   30 * time.Millisecond,
		MaxBackoff:   100 * time.Millisecond,
		HealthyReset: time.Hour, // never reset during this test
	})

	waitSupervisor(t, "two restarts", func() bool { return s.Restarts() >= 2 })

	// Backoff must grow: attempt counter feeds the delay.
	if s.Restarts() < 2 {
		t.Fatalf("restarts = %d", s.Restarts())
	}
}

func TestSupervisor_StatusShowsLifecycle(t *testing.T) {
	s, _ := helperSupervisor(t, "crashloop", SupervisorConfig{
		MinBackoff:   200 * time.Millisecond,
		MaxBackoff:   time.Second,
		HealthyReset: time.Hour,
	})

	// During the backoff window the status must say so (Channels GUI
	// surfaces Detail without any new UI work).
	waitSupervisor(t, "restart status", func() bool {
		st := s.Status()
		return !st.Connected && strings.Contains(st.Detail, "restart")
	})
}

func TestSupervisor_HealthyResetClearsAttempts(t *testing.T) {
	s, _ := helperSupervisor(t, "crashafter", SupervisorConfig{
		MinBackoff:   20 * time.Millisecond,
		MaxBackoff:   50 * time.Millisecond,
		HealthyReset: 100 * time.Millisecond, // crashafter stays up ~300ms
	})

	// Let it crash and restart at least twice.
	start := time.Now()
	crashes := 0
	last := 0
	for time.Now().Before(start.Add(4 * time.Second)) {
		if r := s.Restarts(); r != last {
			last = r
			crashes++
		}
		if crashes >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Uptime (~300ms) always exceeds HealthyReset (100ms), so the attempt
	// counter resets before each increment: it must never exceed 1.
	if r := s.Restarts(); r > 1 {
		t.Errorf("Restarts = %d, want <= 1 (healthy reset should clear attempts)", r)
	}
}

func TestSupervisor_StopHaltsRestarts(t *testing.T) {
	s, _ := helperSupervisor(t, "crashloop", SupervisorConfig{
		MinBackoff:   30 * time.Millisecond,
		MaxBackoff:   100 * time.Millisecond,
		HealthyReset: time.Hour,
	})
	waitSupervisor(t, "first restart", func() bool { return s.Restarts() >= 1 })

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	count := s.Restarts()
	time.Sleep(400 * time.Millisecond)
	if got := s.Restarts(); got != count {
		t.Errorf("restarts continued after Stop: %d → %d", count, got)
	}
	if st := s.Status(); st.Connected {
		t.Error("must not be connected after Stop")
	}
}

func TestSupervisor_DelegatesWhenHealthy(t *testing.T) {
	s, inbox := helperSupervisor(t, "happy", SupervisorConfig{
		MinBackoff: 50 * time.Millisecond, MaxBackoff: time.Second, HealthyReset: time.Hour,
	})

	waitSupervisor(t, "connected", func() bool { return s.Status().Connected })

	// Inbound flows through.
	select {
	case msg := <-inbox:
		if msg.Channel != "fake" {
			t.Errorf("channel = %q", msg.Channel)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no inbound message through supervisor")
	}

	// Send flows through (sidecar echoes).
	if err := s.Send(context.Background(), message.Message{ThreadID: "c1", Parts: message.Text("ping")}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case msg := <-inbox:
		text := ""
		for _, p := range msg.Parts {
			text += p.Text
		}
		if !strings.Contains(text, "echo:ping") {
			t.Errorf("echo = %q", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("send echo never arrived")
	}

	if name := s.Name(); !strings.Contains(name, "fake-platform") {
		t.Errorf("Name() = %q", name)
	}
	if s.ID() != "fake" {
		t.Errorf("ID() = %q", s.ID())
	}
}

func TestSupervisor_SandboxWrapsCommand(t *testing.T) {
	cfg := SupervisorConfig{
		SandboxSelf:   "/usr/local/bin/soulacy",
		SandboxLimits: sandbox.Limits{Enabled: true, CPUSeconds: 60},
	}
	s := NewSupervisor("x", "node", []string{"sidecar.mjs"}, "agent",
		channels.ActivationPolicy{}, zap.NewNop(), cfg)

	command, args := s.buildCommand()
	if command != "/usr/local/bin/soulacy" {
		t.Errorf("command = %q, want the sandbox self-exec wrapper", command)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "node") || !strings.Contains(joined, "sidecar.mjs") {
		t.Errorf("wrapped args lost the original command: %v", args)
	}
}

func TestSupervisor_NoSandboxPassthrough(t *testing.T) {
	s := NewSupervisor("x", "node", []string{"sidecar.mjs"}, "agent",
		channels.ActivationPolicy{}, zap.NewNop(), SupervisorConfig{})
	command, args := s.buildCommand()
	if command != "node" || len(args) != 1 || args[0] != "sidecar.mjs" {
		t.Errorf("passthrough = %q %v", command, args)
	}
}
