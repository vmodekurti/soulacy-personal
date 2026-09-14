package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	mobilechan "github.com/soulacy/soulacy/internal/channels/mobile"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/pkg/agent"
)

type pushCapture struct {
	mu   sync.Mutex
	seen []map[string]any
}

func (p *pushCapture) all() []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]map[string]any(nil), p.seen...)
}

// mobileFixture wires a real mobile store + adapter (relay transport captured
// by an httptest server) and one paired, push-enabled phone for the owner.
func mobileFixture(t *testing.T, s *Server) (*mobilechan.Store, *pushCapture) {
	t.Helper()
	cap := &pushCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n map[string]any
		_ = json.NewDecoder(r.Body).Decode(&n)
		if n == nil {
			n = map[string]any{}
		}
		n["_path"] = r.URL.Path
		cap.mu.Lock()
		cap.seen = append(cap.seen, n)
		cap.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("SOULACY_MOBILE_PUSH_RELAY_URL", srv.URL)
	t.Setenv("SOULACY_MOBILE_APNS_KEY_ID", "")
	store, err := mobilechan.Open(filepath.Join(t.TempDir(), "mobile.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close(); mobilechan.SetDefaultStore(nil); mobilechan.SetDefaultAdapter(nil) })
	mobilechan.SetDefaultStore(store)
	_ = mobilechan.New(store, zap.NewNop())
	tok := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := store.UpsertDevice(context.Background(), "personal", "admin", mobilechan.Device{ID: "phone-1", UserID: "admin", Name: "Ada's iPhone", PushToken: tok, PushEnvironment: "production", BundleID: "dev.soulacy.ios", NotificationsEnabled: true}); err != nil {
		t.Fatal(err)
	}
	return store, cap
}

func TestMobileTriggersListAndFire(t *testing.T) {
	s, provider := newTestGatewayWithLLM(t, "secret")
	provider.content = "Welcome to the office. Three meetings today."
	store, pushes := mobileFixture(t, s)
	triggerCooldowns.Lock()
	triggerCooldowns.last = map[string]time.Time{}
	triggerCooldowns.Unlock()
	s.loader.Register(&agent.Definition{ID: "office", Name: "Office brief", Enabled: true, Trigger: agent.TriggerLocation, LLM: agent.LLMConfig{Provider: "test", Model: "fake-model"},
		Location: &agent.LocationTrigger{Name: "Office", Latitude: 37.77, Longitude: -122.41, RadiusM: 200, On: "enter", Cooldown: "10m"}})
	s.loader.Register(&agent.Definition{ID: "home", Name: "Leaving home", Enabled: true, Trigger: agent.TriggerLocation,
		Location: &agent.LocationTrigger{Latitude: 37.70, Longitude: -122.40, RadiusM: 300, On: "exit", Device: "phone-x"}})
	s.loader.Register(&agent.Definition{ID: "nightly", Name: "Nightly", Enabled: true, Trigger: agent.TriggerCron, Schedule: &agent.Schedule{Cron: "0 1 * * *"}})

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/mobile/triggers?device_id=phone-1", "secret", "")
	if status != 200 {
		t.Fatalf("list: %d %+v", status, body)
	}
	trs := body["triggers"].([]any)
	if len(trs) != 1 || trs[0].(map[string]any)["agent_id"] != "office" || trs[0].(map[string]any)["radius_m"].(float64) != 200 {
		t.Fatalf("phone-1 should see only the unbound office trigger: %+v", trs)
	}

	if status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/mobile/triggers/office/fire", "secret", `{"event":"exit","device_id":"phone-1"}`); status != 202 || body["ignored"] != true {
		t.Fatalf("exit on an enter trigger should be ignored: %d %+v", status, body)
	}
	if status, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/mobile/triggers/office/fire", "secret", `{"event":"enter"}`); status != 400 {
		t.Fatalf("missing device_id should be 400, got %d", status)
	}
	if status, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/mobile/triggers/home/fire", "secret", `{"event":"exit","device_id":"phone-1"}`); status != 403 {
		t.Fatalf("device-bound trigger must refuse another phone, got %d", status)
	}
	if status, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/mobile/triggers/nightly/fire", "secret", `{"event":"enter","device_id":"phone-1"}`); status != 409 {
		t.Fatalf("non-location agent must be refused, got %d", status)
	}

	status, body = gatewayJSON(t, s, http.MethodPost, "/api/v1/mobile/triggers/office/fire", "secret", `{"event":"enter","device_id":"phone-1","latitude":37.7701,"longitude":-122.4102}`)
	if status != 202 || body["ok"] != true || body["ignored"] == true {
		t.Fatalf("fire: %d %+v", status, body)
	}
	deadline := time.Now().Add(5 * time.Second)
	var deliveries []mobilechan.Delivery
	for time.Now().Before(deadline) {
		deliveries, _ = store.List(context.Background(), "personal", "phone-1", "admin", 10)
		if len(deliveries) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(deliveries) != 1 {
		t.Logf("pushes so far: %+v", pushes.all())
	}
	if len(deliveries) != 1 || deliveries[0].AgentID != "office" || deliveries[0].Metadata["trigger"] != "location" || deliveries[0].Metadata["location.event"] != "enter" {
		t.Fatalf("result should be delivered to the firing phone: %+v", deliveries)
	}
	var sent []map[string]any
	for time.Now().Before(deadline) {
		if sent = pushes.all(); len(sent) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(sent) != 1 || sent[0]["category"] != mobilechan.CategoryDelivery {
		t.Fatalf("delivery push missing or uncategorised: %+v", sent)
	}
	if last := provider.lastRequest(); len(last.Messages) == 0 {
		t.Fatal("agent was not run")
	}
	if status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/mobile/triggers/office/fire", "secret", `{"event":"enter","device_id":"phone-1"}`); status != 202 || body["reason"] != "cooldown" {
		t.Fatalf("immediate refire should hit the cooldown: %d %+v", status, body)
	}
}

func TestApprovalRegistrationPushesActionableNotificationToPhones(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	_, pushes := mobileFixture(t, s)
	s.wirePushNotifications()
	_ = s.engine.Broker().RegisterRequestForPrincipal(runtime.ConfirmRequest{CallID: "call-1", Tool: "shell_exec", Reason: "wants to run rm"}, "planner", "sess-1", "admin")
	deadline := time.Now().Add(3 * time.Second)
	var sent []map[string]any
	for time.Now().Before(deadline) {
		if sent = pushes.all(); len(sent) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(sent) != 1 {
		t.Fatalf("expected one approval push, got %d", len(sent))
	}
	n := sent[0]
	data, _ := n["data"].(map[string]any)
	if n["category"] != mobilechan.CategoryApproval || n["time_sensitive"] != true || data["call_id"] != "call-1" || data["agent_id"] != "planner" || n["deep_link"] != "soulacy://approval/call-1" {
		t.Fatalf("approval push malformed: %+v", n)
	}
	if approvalDestination("operator:ada") != "user:ada" || approvalDestination("admin") != "user:admin" || approvalDestination("") != "" {
		t.Fatal("principal to destination mapping wrong")
	}
}

// TestApprovalPushIsScopedToTheApprovingUsersPhones pins the identity
// mapping the lock-screen flow relies on: the mobile-companion credential's
// subject is the web owner's subject, so an approval raised by one principal
// reaches only phones paired under that same subject. A phone paired by
// anyone else must stay silent even in a single-owner workspace.
func TestApprovalPushIsScopedToTheApprovingUsersPhones(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	store, pushes := mobileFixture(t, s)
	other := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	if err := store.UpsertDevice(context.Background(), "personal", "guest", mobilechan.Device{ID: "phone-2", UserID: "guest", Name: "Guest iPhone", PushToken: other, PushEnvironment: "production", BundleID: "dev.soulacy.ios", NotificationsEnabled: true}); err != nil {
		t.Fatal(err)
	}
	s.wirePushNotifications()
	_ = s.engine.Broker().RegisterRequestForPrincipal(runtime.ConfirmRequest{CallID: "call-2", Tool: "shell_exec", Reason: "wants to run rm"}, "planner", "sess-2", "operator:admin")
	deadline := time.Now().Add(3 * time.Second)
	var sent []map[string]any
	for time.Now().Before(deadline) {
		if sent = pushes.all(); len(sent) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Give a stray second push a moment to show up before asserting.
	time.Sleep(150 * time.Millisecond)
	sent = pushes.all()
	if len(sent) != 1 {
		t.Fatalf("expected exactly one approval push (admin's phone), got %d: %+v", len(sent), sent)
	}
	if tok, _ := sent[0]["device_token"].(string); tok == other || tok == "" {
		t.Fatalf("approval push reached the wrong phone: token %q", tok)
	}
}
