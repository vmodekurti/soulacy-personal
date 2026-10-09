package gateway

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/authconnections"
	"github.com/soulacy/soulacy/internal/managedbrowser"
	"github.com/soulacy/soulacy/internal/runtime"
)

type readyBrowserFactory struct{}

func (readyBrowserFactory) Available() (bool, string) { return true, "test managed Chromium" }
func (readyBrowserFactory) Open(context.Context, managedbrowser.OpenRequest) (managedbrowser.Browser, error) {
	return nil, nil
}

func TestExecutionPlanUsesManagedBrowserWithoutMCP(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	s.SetManagedBrowser(managedbrowser.New(nil, readyBrowserFactory{}))

	plan, err := s.buildExecutionPlan(t.Context(), "personal", "admin", "Book an Uber from home to the airport tomorrow at 8 AM", map[string]string{
		"pickup": "home", "destination": "airport", "ride_time": "tomorrow at 8 AM", "payment_method": "available",
	})
	if err != nil {
		t.Fatal(err)
	}
	var browserReady, executeReady bool
	for _, capability := range plan.Capabilities {
		if capability.ID == "browser" && capability.Available {
			browserReady = true
		}
	}
	for _, step := range plan.Steps {
		if step.ID == "execute" && step.Status == "ready" && step.CapabilityID == "browser" {
			executeReady = true
		}
	}
	if !browserReady || !executeReady {
		t.Fatalf("managed browser was not selected without MCP: %+v", plan)
	}
}

func TestBrowserStatusReportsManagedRuntime(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	s.SetManagedBrowser(managedbrowser.New(nil, readyBrowserFactory{}))
	status := s.browserAutomationReadiness()
	if ready, _ := status.Managed["available"].(bool); !ready {
		t.Fatalf("managed status = %#v", status.Managed)
	}
	if status.Score < 60 || status.Checks[0].Status != "ok" || status.Checks[1].Status != "ok" || status.Checks[2].Status != "ok" {
		t.Fatalf("browser readiness = %+v", status)
	}
}

type recordingConnectionResolver struct {
	agentID      string
	connectionID string
}

func (r *recordingConnectionResolver) Resolve(_ context.Context, _, _, agentID, connectionID string) (authconnections.Lease, error) {
	r.agentID = agentID
	r.connectionID = connectionID
	return authconnections.Lease{Kind: authconnections.KindBrowser, AllowedDomains: []string{"example.com"}, BrowserState: []byte(`{"cookies":[]}`)}, nil
}

func TestManagedBrowserAutomaticallySelectsReadyGrantedWebsiteAccess(t *testing.T) {
	store, err := authconnections.Open(filepath.Join(t.TempDir(), "connections.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	connection, err := store.Create(t.Context(), authconnections.CreateInput{
		WorkspaceID: runtime.PersonalWorkspaceID, OwnerSubject: "admin", Scope: authconnections.ScopeUser,
		Kind: authconnections.KindBrowser, Name: "Example", BaseURL: "https://example.com", AllowedDomains: []string{"example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSecret(t.Context(), runtime.PersonalWorkspaceID, connection.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceAgentGrants(t.Context(), runtime.PersonalWorkspaceID, connection.ID, []string{"morning-podcast-digest"}); err != nil {
		t.Fatal(err)
	}
	resolver := &recordingConnectionResolver{}
	s := &Server{authConnections: store, managedBrowser: managedbrowser.New(resolver, staticManagedBrowserFactory{})}
	ctx := runtime.WithActiveAgent(t.Context(), "morning-podcast-digest")

	result, err := s.StartWebsiteActionForGenie(ctx, "https://example.com/account", "")
	if err != nil {
		t.Fatal(err)
	}
	if resolver.connectionID != connection.ID || result["route"] != "website_access" || result["connection_id"] != connection.ID {
		t.Fatalf("automatic route = %#v, resolved connection = %q", result, resolver.connectionID)
	}
}

func TestMatchingReadyWebsiteConnectionHonorsGrantExpiryAndSpecificity(t *testing.T) {
	now := time.Now()
	expired := now.Add(-time.Minute)
	connections := []authconnections.Connection{
		{ID: "broad", Kind: authconnections.KindBrowser, Status: authconnections.StatusReady, HasSecret: true, AllowedDomains: []string{"google.com"}, AgentIDs: []string{"agent"}},
		{ID: "specific", Kind: authconnections.KindBrowser, Status: authconnections.StatusReady, HasSecret: true, AllowedDomains: []string{"notebooklm.google.com"}, AgentIDs: []string{"agent"}},
		{ID: "ungranted", Kind: authconnections.KindBrowser, Status: authconnections.StatusReady, HasSecret: true, AllowedDomains: []string{"notebooklm.google.com"}},
		{ID: "expired", Kind: authconnections.KindBrowser, Status: authconnections.StatusReady, HasSecret: true, ExpiresAt: &expired, AllowedDomains: []string{"notebooklm.google.com"}, AgentIDs: []string{"agent"}},
	}
	selected, ok := matchingReadyWebsiteConnection(connections, "agent", "notebooklm.google.com", now)
	if !ok || selected.ID != "specific" {
		t.Fatalf("selected = %+v, ok=%v", selected, ok)
	}
}
func (r *recordingConnectionResolver) UpdateBrowserState(_ context.Context, _, _ string, update func([]byte) ([]byte, bool, error)) error {
	_, _, err := update([]byte(`{"cookies":[]}`))
	return err
}
func (*recordingConnectionResolver) MarkNeedsAuthentication(context.Context, string, string) {}

type staticManagedBrowser struct{}

func (staticManagedBrowser) Observe(context.Context) (managedbrowser.Observation, error) {
	return managedbrowser.Observation{URL: "https://example.com/", Elements: []managedbrowser.Element{}}, nil
}
func (staticManagedBrowser) Act(context.Context, managedbrowser.Action) (managedbrowser.Observation, error) {
	return managedbrowser.Observation{URL: "https://example.com/", Elements: []managedbrowser.Element{}}, nil
}
func (staticManagedBrowser) StorageState(context.Context) ([]byte, error) {
	return []byte(`{"cookies":[]}`), nil
}
func (staticManagedBrowser) Close() error { return nil }

type staticManagedBrowserFactory struct{}

func (staticManagedBrowserFactory) Available() (bool, string) { return true, "test browser" }
func (staticManagedBrowserFactory) Open(context.Context, managedbrowser.OpenRequest) (managedbrowser.Browser, error) {
	return staticManagedBrowser{}, nil
}

func TestManagedBrowserUsesCallingAgentGrant(t *testing.T) {
	resolver := &recordingConnectionResolver{}
	s := &Server{managedBrowser: managedbrowser.New(resolver, staticManagedBrowserFactory{})}
	ctx := runtime.WithActiveAgent(t.Context(), "morning-podcast-digest")

	if _, err := s.StartWebsiteActionForGenie(ctx, "https://example.com/", "conn_notebook"); err != nil {
		t.Fatal(err)
	}
	if resolver.agentID != "morning-podcast-digest" {
		t.Fatalf("connection resolved for %q, want calling agent", resolver.agentID)
	}
}
