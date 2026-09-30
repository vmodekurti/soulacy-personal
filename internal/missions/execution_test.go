package missions

import "testing"

func TestBuildExecutionPlanRideExplainsMissingRouteAndSecureInputs(t *testing.T) {
	plan, err := BuildExecutionPlan("Book me an Uber to the airport tomorrow morning", map[string]string{
		"pickup": "Home", "destination": "Airport", "ride_time": "Tomorrow at 7 AM",
	}, CapabilityInventory{PublicWeb: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Category != "ride" || plan.Status != PlanNeedsSetup || plan.Route != "capability_required" {
		t.Fatalf("plan=%+v", plan)
	}
	if len(plan.CapabilityGaps) == 0 || len(plan.ApprovalCheckpoints) != 1 {
		t.Fatalf("ride plan must expose capability gap and approval: %+v", plan)
	}
	assertRequirementStatus(t, plan, "website_access", "secure_setup")
	assertRequirementStatus(t, plan, "payment_method", "secure_setup")
}

func TestBuildExecutionPlanRestaurantReadyThroughBrowser(t *testing.T) {
	plan, err := BuildExecutionPlan("Reserve a table at a restaurant", map[string]string{
		"location": "Chicago", "date": "Friday", "time": "7 PM", "party_size": "4", "preferences": "Italian",
	}, CapabilityInventory{PublicWeb: true, BrowserAutomation: true, BrowserDetail: "Playwright is connected"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Category != "restaurant" || plan.Status != PlanReady || plan.Route != "browser_automation" {
		t.Fatalf("plan=%+v", plan)
	}
	if len(plan.ApprovalCheckpoints) != 1 {
		t.Fatalf("reservation must stop for approval: %+v", plan)
	}
}

func TestBuildExecutionPlanResearchUsesPublicAccess(t *testing.T) {
	plan, err := BuildExecutionPlan("Research the best home energy rebates", map[string]string{"constraints": "Illinois programs"}, CapabilityInventory{PublicWeb: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Category != "research" || plan.Status != PlanReady || plan.Route != "public_web" || len(plan.ApprovalCheckpoints) != 0 {
		t.Fatalf("plan=%+v", plan)
	}
}

func TestBuildExecutionPlanUnsupportedPhysicalTaskProvidesHandoff(t *testing.T) {
	plan, err := BuildExecutionPlan("Physically clean my garage this afternoon", nil, CapabilityInventory{PublicWeb: true, BrowserAutomation: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != PlanUnsupported || plan.Route != "human_handoff" || len(plan.CapabilityGaps) == 0 {
		t.Fatalf("plan=%+v", plan)
	}
}

func TestBuildExecutionPlanRejectsSecretsInKnownInputs(t *testing.T) {
	_, err := BuildExecutionPlan("Book me a ride", map[string]string{"card_number": "4111111111111111"}, CapabilityInventory{PublicWeb: true})
	if err == nil {
		t.Fatal("accepted a payment number as chat input")
	}
}

func TestDetectBrowserAutomation(t *testing.T) {
	available, detail := DetectBrowserAutomation([]string{"mcp__playwright__browser_navigate", "mcp__playwright__browser_click"})
	if !available || detail == "" {
		t.Fatalf("available=%v detail=%q", available, detail)
	}
}

func assertRequirementStatus(t *testing.T, plan ExecutionPlan, key, want string) {
	t.Helper()
	for _, requirement := range plan.RequiredInputs {
		if requirement.Key == key {
			if requirement.Status != want {
				t.Fatalf("requirement %s status=%q want=%q", key, requirement.Status, want)
			}
			return
		}
	}
	t.Fatalf("requirement %s missing from %+v", key, plan.RequiredInputs)
}
