package gateway

import (
	"net/http"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/soulacy/soulacy/internal/authconnections"
	"github.com/soulacy/soulacy/internal/missions"
	"github.com/soulacy/soulacy/internal/runtime"
)

type fakeMissionMonitor struct {
	items map[string]map[string]any
	next  int
}

func newFakeMissionMonitor() *fakeMissionMonitor {
	return &fakeMissionMonitor{items: map[string]map[string]any{}}
}

func (m *fakeMissionMonitor) CreateGenieMonitor(prompt, cron, at, channel, to string) (map[string]any, error) {
	m.next++
	id := "genie-monitor-mission-" + strconv.Itoa(m.next)
	item := map[string]any{"id": id, "prompt": prompt, "cron": cron, "at": at, "enabled": true}
	m.items[id] = item
	return item, nil
}

func (m *fakeMissionMonitor) ListGenieMonitors() []map[string]any {
	out := []map[string]any{}
	for _, item := range m.items {
		out = append(out, item)
	}
	return out
}

func (m *fakeMissionMonitor) PauseGenieMonitor(id string) error {
	m.items[id]["enabled"] = false
	return nil
}

func (m *fakeMissionMonitor) ResumeGenieMonitor(id string) error {
	m.items[id]["enabled"] = true
	return nil
}

func (m *fakeMissionMonitor) CancelGenieMonitor(id string) error {
	delete(m.items, id)
	return nil
}

func TestMissionLifecycleAPIAndGenieShareOneStore(t *testing.T) {
	s := newTestGateway(t, "secret")
	store, err := missions.OpenStore(filepath.Join(t.TempDir(), "missions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	monitor := newFakeMissionMonitor()
	s.SetMissionStore(store)
	s.SetMissionMonitor(monitor)
	s.engine.SetGenieMissionManager(s)

	status, planned := gatewayJSON(t, s, http.MethodPost, "/api/v1/missions/plan", "secret", `{"objective":"Prepare a useful leadership briefing every weekday"}`)
	if status != http.StatusOK || planned["plan"] == nil {
		t.Fatalf("plan status=%d body=%v", status, planned)
	}

	status, created := gatewayJSON(t, s, http.MethodPost, "/api/v1/missions", "secret", `{
		"title":"Leadership briefing","objective":"Prepare a useful leadership briefing every weekday",
		"finish_line":"Deliver five cited items","cron":"0 8 * * 1-5"
	}`)
	if status != http.StatusCreated {
		t.Fatalf("create status=%d body=%v", status, created)
	}
	mission := created["mission"].(map[string]any)
	id := mission["id"].(string)
	monitorID := mission["monitor_id"].(string)
	if monitor.items[monitorID] == nil {
		t.Fatalf("runner %q was not created", monitorID)
	}
	if mission["execution_plan"] == nil {
		t.Fatal("new mission did not receive an execution plan")
	}

	status, paused := gatewayJSON(t, s, http.MethodPost, "/api/v1/missions/"+id+"/pause", "secret", `{}`)
	if status != http.StatusOK || paused["mission"].(map[string]any)["status"] != missions.StatusPaused {
		t.Fatalf("pause=%d %v", status, paused)
	}
	status, resumed := gatewayJSON(t, s, http.MethodPost, "/api/v1/missions/"+id+"/resume", "secret", `{}`)
	if status != http.StatusOK || resumed["mission"].(map[string]any)["status"] != missions.StatusActive {
		t.Fatalf("resume=%d %v", status, resumed)
	}

	status, updated := gatewayJSON(t, s, http.MethodPatch, "/api/v1/missions/"+id, "secret", `{"status":"blocked","progress":"Three sources checked","next_action":"Refresh access","blocker":"Subscription expired"}`)
	if status != http.StatusOK || updated["mission"].(map[string]any)["blocker"] != "Subscription expired" {
		t.Fatalf("update=%d %v", status, updated)
	}

	listed, err := s.ListMissionsForGenie(t.Context())
	if err != nil || listed["count"] != 1 {
		t.Fatalf("Genie list=%v err=%v", listed, err)
	}
	status, completed := gatewayJSON(t, s, http.MethodPost, "/api/v1/missions/"+id+"/complete", "secret", `{"progress":"Briefing delivered"}`)
	if status != http.StatusOK || completed["mission"].(map[string]any)["status"] != missions.StatusCompleted {
		t.Fatalf("complete=%d %v", status, completed)
	}
	if monitor.items[monitorID] != nil {
		t.Fatal("completed mission runner still exists")
	}
}

func TestMissionExecutionPlannerAndSecureWebsiteAccess(t *testing.T) {
	s := newTestGateway(t, "secret")
	store, err := missions.OpenStore(filepath.Join(t.TempDir(), "missions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s.SetMissionStore(store)
	connections, err := authconnections.Open(filepath.Join(t.TempDir(), "connections.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connections.Close() })
	s.SetAuthenticatedConnectionStore(connections)
	s.SetCredentialVault(newMemVault())

	status, planned := gatewayJSON(t, s, http.MethodPost, "/api/v1/missions/execution-plan", "secret", `{
		"goal":"Book me an Uber to the airport tomorrow morning",
		"known_inputs":{"pickup":"Home","destination":"Airport","ride_time":"7 AM"}
	}`)
	if status != http.StatusOK {
		t.Fatalf("plan status=%d body=%v", status, planned)
	}
	plan := planned["execution_plan"].(map[string]any)
	if plan["category"] != "ride" || plan["status"] != missions.PlanNeedsSetup {
		t.Fatalf("execution plan=%v", plan)
	}

	prepared, err := s.PrepareWebsiteAccessForGenie(t.Context(), "Uber", "https://www.uber.com")
	if err != nil {
		t.Fatal(err)
	}
	connection := prepared["connection"].(authconnections.Connection)
	if connection.Status != authconnections.StatusPending || len(connection.AgentIDs) != 1 || connection.AgentIDs[0] != runtime.GenieAgentID {
		t.Fatalf("connection=%+v", connection)
	}
	if prepared["setup_href"] != "#websites" {
		t.Fatalf("prepared=%v", prepared)
	}
}
