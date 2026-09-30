package missions

import "testing"

func TestLooksLikeActionGoal(t *testing.T) {
	for _, goal := range []string{"Book me an Uber", "Reserve a table for four", "Buy a birthday gift", "Schedule an appointment"} {
		if !LooksLikeActionGoal(goal) {
			t.Errorf("action goal was not detected: %q", goal)
		}
	}
	for _, goal := range []string{"Research airport transfer options", "Explain how Uber pricing works"} {
		if LooksLikeActionGoal(goal) {
			t.Errorf("research goal was classified as an action: %q", goal)
		}
	}
}

func TestBuildExecutionPlanRideUsesWebsiteFallbackAndAsksForDetailsFirst(t *testing.T) {
	plan, err := BuildExecutionPlan("Book me an Uber to the airport tomorrow morning", map[string]string{
		"pickup": "Home", "destination": "Airport", "ride_time": "Tomorrow at 7 AM",
	}, CapabilityInventory{PublicWeb: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Category != "ride" || plan.Status != PlanNeedsSetup || plan.Route != "provider_website" {
		t.Fatalf("plan=%+v", plan)
	}
	if len(plan.CapabilityGaps) != 0 || len(plan.ApprovalCheckpoints) != 1 {
		t.Fatalf("missing connector must not become a capability gap: %+v", plan)
	}
	assertRouteStatus(t, plan, "direct_integration", "not_selected")
	assertRouteStatus(t, plan, "provider_website", "selected")
	assertRouteStatus(t, plan, "official_alternative", "fallback")
	assertRequirementStatus(t, plan, "website_access", "secure_setup")
	assertRequirementStatus(t, plan, "payment_method", "secure_setup")
}

func TestBuildExecutionPlanRideRequestsMinimumDetailsBeforeSetup(t *testing.T) {
	plan, err := BuildExecutionPlan("Book an Uber for me", map[string]string{"pickup": "Home"}, CapabilityInventory{PublicWeb: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != PlanNeedsInput || plan.Route != "provider_website" {
		t.Fatalf("plan=%+v", plan)
	}
	assertRequirementStatus(t, plan, "destination", "needed")
	assertRequirementStatus(t, plan, "ride_time", "needed")
	for _, requirement := range plan.RequiredInputs {
		if requirement.Key == "ride_preferences" || requirement.Key == "spend_limit" {
			t.Fatalf("ride plan asked for a premature detail: %+v", plan.RequiredInputs)
		}
	}
}

func TestBuildExecutionPlanPrefersDirectActionTool(t *testing.T) {
	plan, err := BuildExecutionPlan("Book an Uber for me", map[string]string{
		"pickup": "Home", "destination": "Airport", "ride_time": "Now", "payment_method": "saved",
	}, CapabilityInventory{PublicWeb: true, Tools: []string{"mcp__uber__request_ride"}, WebsiteAccess: []InventoryWebsiteAccess{{Name: "Uber", Domains: []string{"uber.com"}, Ready: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != PlanReady || plan.Route != "direct_tool" {
		t.Fatalf("plan=%+v", plan)
	}
	assertRouteStatus(t, plan, "direct_integration", "selected")
	assertRouteStatus(t, plan, "provider_website", "fallback")
}

func TestBuildExecutionPlanRestaurantReadyThroughBrowser(t *testing.T) {
	plan, err := BuildExecutionPlan("Reserve a table at a restaurant", map[string]string{
		"location": "Chicago", "date": "Friday", "time": "7 PM", "party_size": "4", "preferences": "Italian",
	}, CapabilityInventory{PublicWeb: true, BrowserAutomation: true, BrowserDetail: "Playwright is connected"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Category != "restaurant" || plan.Status != PlanReady || plan.Route != "provider_website" {
		t.Fatalf("plan=%+v", plan)
	}
	if len(plan.ApprovalCheckpoints) != 1 {
		t.Fatalf("reservation must stop for approval: %+v", plan)
	}
}

func assertRouteStatus(t *testing.T, plan ExecutionPlan, id, want string) {
	t.Helper()
	for _, route := range plan.Routes {
		if route.ID == id {
			if route.Status != want {
				t.Fatalf("route %s status=%q want=%q", id, route.Status, want)
			}
			return
		}
	}
	t.Fatalf("route %s missing from %+v", id, plan.Routes)
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
