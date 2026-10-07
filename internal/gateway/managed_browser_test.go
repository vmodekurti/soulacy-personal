package gateway

import (
	"context"
	"testing"

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

type recordingConnectionResolver struct{ agentID string }

func (r *recordingConnectionResolver) Resolve(_ context.Context, _, _, agentID, _ string) (authconnections.Lease, error) {
	r.agentID = agentID
	return authconnections.Lease{Kind: authconnections.KindBrowser, AllowedDomains: []string{"example.com"}, BrowserState: []byte(`{"cookies":[]}`)}, nil
}

type staticManagedBrowser struct{}

func (staticManagedBrowser) Observe(context.Context) (managedbrowser.Observation, error) {
	return managedbrowser.Observation{URL: "https://example.com/", Elements: []managedbrowser.Element{}}, nil
}
func (staticManagedBrowser) Act(context.Context, managedbrowser.Action) (managedbrowser.Observation, error) {
	return managedbrowser.Observation{URL: "https://example.com/", Elements: []managedbrowser.Element{}}, nil
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
