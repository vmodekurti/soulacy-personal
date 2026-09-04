package external

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/pkg/message"
)

// envEchoArgs builds the helper invocation for the envecho sidecar mode,
// which reports SIDE_TOKEN and SIDE_UNDECLARED in its status detail.
// The mode travels in the controlled env itself (the child no longer
// inherits the parent environment once SetEnv is used).
func envEchoEnv(extra ...string) []string {
	base := []string{
		"GO_EXTERNAL_SIDECAR=envecho",
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	return append(base, extra...)
}

func TestAdapter_ControlledEnv_OnlyDeclaredVisible(t *testing.T) {
	t.Setenv("SIDE_UNDECLARED", "host-leak") // in OUR env; must not reach child

	a := New("fake", os.Args[0], []string{"-test.run=TestHelperSidecar"},
		"agent-1", channels.ActivationPolicy{}, zap.NewNop())
	a.handshakeTimeout = 2 * time.Second
	a.SetEnv(envEchoEnv("SIDE_TOKEN=tok-v1"))

	inbox := make(chan message.Message, 4)
	if err := a.Start(context.Background(), inbox); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop() })

	st := waitStatus(t, a, func(s channels.AdapterStatus) bool { return s.Connected }, "connected")
	if !strings.Contains(st.Detail, "token=tok-v1") {
		t.Fatalf("declared env var not visible to sidecar: %q", st.Detail)
	}
	if !strings.Contains(st.Detail, "undeclared=<unset>") {
		t.Fatalf("undeclared host env var leaked to sidecar: %q", st.Detail)
	}
}

func TestAdapter_NilEnv_InheritsParent(t *testing.T) {
	// Backwards compatibility: without SetEnv the helper still finds the
	// mode in the inherited environment.
	a, _ := helperAdapter(t, "happy", channels.ActivationPolicy{})
	waitStatus(t, a, func(s channels.AdapterStatus) bool { return s.Connected }, "connected")
}

func TestSupervisor_EnvFn_FreshPerSpawn(t *testing.T) {
	// The env resolver runs once per spawn, so a restarted sidecar sees
	// rotated credentials.
	var gen atomic.Int32
	cfg := SupervisorConfig{
		MinBackoff:   10 * time.Millisecond,
		MaxBackoff:   20 * time.Millisecond,
		HealthyReset: time.Hour,
		Env: func() ([]string, error) {
			n := gen.Add(1)
			return envEchoEnv(fmt.Sprintf("SIDE_TOKEN=tok-v%d", n)), nil
		},
	}
	s := NewSupervisor("fake", os.Args[0], []string{"-test.run=TestHelperSidecar"},
		"agent-1", channels.ActivationPolicy{}, zap.NewNop(), cfg)
	s.handshakeTimeout = 2 * time.Second

	inbox := make(chan message.Message, 4)
	if err := s.Start(context.Background(), inbox); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	waitSupStatus(t, s, func(st channels.AdapterStatus) bool {
		return st.Connected && strings.Contains(st.Detail, "token=tok-v1")
	}, "first spawn with tok-v1")

	// Simulate a rotation: restart the sidecar; the resolver must be
	// consulted again and deliver the new value.
	s.Restart("credential rotated")

	waitSupStatus(t, s, func(st channels.AdapterStatus) bool {
		return st.Connected && strings.Contains(st.Detail, "token=tok-v2")
	}, "respawn with tok-v2")
}

func TestSupervisor_EnvFnError_RetriesAsCrash(t *testing.T) {
	var calls atomic.Int32
	cfg := SupervisorConfig{
		MinBackoff:   10 * time.Millisecond,
		MaxBackoff:   20 * time.Millisecond,
		HealthyReset: time.Hour,
		Env: func() ([]string, error) {
			if calls.Add(1) < 3 {
				return nil, fmt.Errorf("vault unavailable")
			}
			return envEchoEnv("SIDE_TOKEN=tok-late"), nil
		},
	}
	s := NewSupervisor("fake", os.Args[0], []string{"-test.run=TestHelperSidecar"},
		"agent-1", channels.ActivationPolicy{}, zap.NewNop(), cfg)
	s.handshakeTimeout = 2 * time.Second

	inbox := make(chan message.Message, 4)
	err := s.Start(context.Background(), inbox)
	if err == nil {
		// Start may surface the env failure either as an error or via the
		// supervised retry loop; both are acceptable as long as the sidecar
		// eventually comes up with the late credentials.
		t.Cleanup(func() { _ = s.Stop() })
		waitSupStatus(t, s, func(st channels.AdapterStatus) bool {
			return st.Connected && strings.Contains(st.Detail, "token=tok-late")
		}, "recovery after env failures")
		return
	}
	if !strings.Contains(err.Error(), "vault unavailable") {
		t.Fatalf("Start error = %v, want vault unavailable", err)
	}
}

func waitSupStatus(t *testing.T, s *Supervisor, want func(channels.AdapterStatus) bool, what string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var last channels.AdapterStatus
	for time.Now().Before(deadline) {
		last = s.Status()
		if want(last) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; last status: %+v", what, last)
}
