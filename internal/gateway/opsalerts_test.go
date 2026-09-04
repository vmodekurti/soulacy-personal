package gateway

import (
	"context"
	"net/http"
	"testing"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/pkg/message"
)

type fakeOpsAlertAdapter struct {
	id      string
	sent    []message.Message
	live    bool
	sendErr error
}

func (a *fakeOpsAlertAdapter) ID() string { return a.id }
func (a *fakeOpsAlertAdapter) Name() string {
	return "Fake Ops Alert"
}
func (a *fakeOpsAlertAdapter) Start(context.Context, chan<- message.Message) error { return nil }
func (a *fakeOpsAlertAdapter) Send(_ context.Context, msg message.Message) error {
	if a.sendErr != nil {
		return a.sendErr
	}
	a.sent = append(a.sent, msg)
	return nil
}
func (a *fakeOpsAlertAdapter) Stop() error { return nil }
func (a *fakeOpsAlertAdapter) Status() channels.AdapterStatus {
	return channels.AdapterStatus{Connected: a.live, Detail: "test"}
}

func TestOpsAlertStatusUsesConfiguredChannel(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.cfg.Ops = config.OpsConfig{AlertChannel: "telegram", AlertTo: "-10042", AlertMinStatus: "warn"}
	adapter := &fakeOpsAlertAdapter{id: "telegram", live: true}
	s.channels.Register(adapter)

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/ops/alerts/status", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("ops alert status = %d body=%v", status, body)
	}
	if body["status"] != "ok" || body["channel"] != "telegram" || body["to"] != "-10042" || body["min_status"] != "warn" {
		t.Fatalf("unexpected alert readiness: %v", body)
	}
}

func TestOpsAlertTestSendsThroughRegistry(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.cfg.Ops = config.OpsConfig{AlertChannel: "telegram", AlertTo: "-10042", AlertMinStatus: "fail"}
	adapter := &fakeOpsAlertAdapter{id: "telegram", live: true}
	s.channels.Register(adapter)

	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/ops/alerts/test", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("ops alert test = %d body=%v", status, body)
	}
	if len(adapter.sent) != 1 {
		t.Fatalf("expected one sent alert, got %d", len(adapter.sent))
	}
	if adapter.sent[0].Channel != "telegram" || adapter.sent[0].ThreadID != "-10042" {
		t.Fatalf("unexpected sent message: %#v", adapter.sent[0])
	}
}
