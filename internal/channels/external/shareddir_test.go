package external

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/pkg/message"
)

// Story E24 (shared mounts on the ECP side): the adapter advertises the
// per-run scratch directory in hello_ack; sidecars without one get an
// empty/absent field (backwards compatible).
func TestAdapter_HelloAckCarriesSharedDir(t *testing.T) {
	t.Setenv("GO_EXTERNAL_SIDECAR", "shareddir")
	a := New("fake", os.Args[0], []string{"-test.run=TestHelperSidecar"},
		"agent-1", channels.ActivationPolicy{}, zap.NewNop())
	a.handshakeTimeout = 2 * time.Second
	a.SetSharedDir("/scratch/run-42")
	inbox := make(chan message.Message, 1)
	if err := a.Start(context.Background(), inbox); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop() })

	st := waitStatus(t, a, func(s channels.AdapterStatus) bool {
		return strings.Contains(s.Detail, "shared=")
	}, "shared-dir echo")
	if !strings.Contains(st.Detail, "shared=/scratch/run-42") {
		t.Errorf("sidecar saw %q, want shared=/scratch/run-42", st.Detail)
	}
}

func TestAdapter_NoSharedDirByDefault(t *testing.T) {
	t.Setenv("GO_EXTERNAL_SIDECAR", "shareddir")
	a := New("fake", os.Args[0], []string{"-test.run=TestHelperSidecar"},
		"agent-1", channels.ActivationPolicy{}, zap.NewNop())
	a.handshakeTimeout = 2 * time.Second
	inbox := make(chan message.Message, 1)
	if err := a.Start(context.Background(), inbox); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop() })

	st := waitStatus(t, a, func(s channels.AdapterStatus) bool {
		return strings.Contains(s.Detail, "shared=")
	}, "shared-dir echo")
	if !strings.HasSuffix(st.Detail, "shared=") {
		t.Errorf("expected empty shared dir, got %q", st.Detail)
	}
}

// The supervisor re-advertises the SAME shared dir to respawned sidecars.
func TestSupervisor_PassesSharedDirThrough(t *testing.T) {
	t.Setenv("GO_EXTERNAL_SIDECAR", "shareddir")
	sup := NewSupervisor("fake", os.Args[0],
		[]string{"-test.run=TestHelperSidecar"}, "agent-1",
		channels.ActivationPolicy{}, zap.NewNop(),
		SupervisorConfig{SharedDir: "/scratch/sup-1"})
	sup.handshakeTimeout = 2 * time.Second
	inbox := make(chan message.Message, 1)
	if err := sup.Start(context.Background(), inbox); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = sup.Stop() })

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(sup.Status().Detail, "shared=/scratch/sup-1") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("supervised sidecar never saw the shared dir; status %+v", sup.Status())
}
