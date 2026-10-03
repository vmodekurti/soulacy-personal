package gateway

import (
	"context"
	"testing"

	"github.com/soulacy/soulacy/internal/managedbrowser"
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
