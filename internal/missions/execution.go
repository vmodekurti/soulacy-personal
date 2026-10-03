package missions

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	PlanReady       = "ready_for_execution"
	PlanNeedsInput  = "needs_input"
	PlanNeedsSetup  = "needs_setup"
	PlanUnsupported = "unsupported"
)

// ExecutionPlan is the durable, user-visible explanation of how Genie intends
// to accomplish a goal. It contains no credential values or payment details.
type ExecutionPlan struct {
	Goal                string                 `json:"goal"`
	Category            string                 `json:"category"`
	Status              string                 `json:"status"`
	Summary             string                 `json:"summary"`
	Route               string                 `json:"route"`
	Routes              []ExecutionRoute       `json:"routes"`
	Capabilities        []ExecutionCapability  `json:"capabilities"`
	RequiredInputs      []ExecutionRequirement `json:"required_inputs"`
	Steps               []ExecutionStep        `json:"steps"`
	CapabilityGaps      []string               `json:"capability_gaps"`
	ApprovalCheckpoints []string               `json:"approval_checkpoints"`
	CompletionEvidence  []string               `json:"completion_evidence"`
	GeneratedAt         time.Time              `json:"generated_at"`
}

// ExecutionRoute records the ordered ways Genie can try to complete an
// action. A missing preferred route does not make the goal impossible when a
// provider website or another official channel remains available.
type ExecutionRoute struct {
	Priority     int    `json:"priority"`
	ID           string `json:"id"`
	Label        string `json:"label"`
	Kind         string `json:"kind"`
	Status       string `json:"status"`
	Detail       string `json:"detail"`
	CapabilityID string `json:"capability_id,omitempty"`
}

type ExecutionCapability struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Kind      string `json:"kind"`
	Available bool   `json:"available"`
	Detail    string `json:"detail"`
	SetupHref string `json:"setup_href,omitempty"`
}

type ExecutionRequirement struct {
	Key          string `json:"key"`
	Label        string `json:"label"`
	Why          string `json:"why"`
	Status       string `json:"status"`
	Sensitive    bool   `json:"sensitive,omitempty"`
	SetupHref    string `json:"setup_href,omitempty"`
	ConnectionID string `json:"connection_id,omitempty"`
}

type ExecutionStep struct {
	ID               string `json:"id"`
	Kind             string `json:"kind"`
	Label            string `json:"label"`
	Detail           string `json:"detail"`
	Status           string `json:"status"`
	CapabilityID     string `json:"capability_id,omitempty"`
	RequiresApproval bool   `json:"requires_approval,omitempty"`
}

type InventoryConnector struct {
	Name     string   `json:"name"`
	Category string   `json:"category"`
	Domains  []string `json:"domains"`
}

type InventoryWebsiteAccess struct {
	ID      string   `json:"id,omitempty"`
	Name    string   `json:"name"`
	Domains []string `json:"domains"`
	Ready   bool     `json:"ready"`
}

type CapabilityInventory struct {
	PublicWeb         bool                     `json:"public_web"`
	BrowserAutomation bool                     `json:"browser_automation"`
	BrowserDetail     string                   `json:"browser_detail,omitempty"`
	Connectors        []InventoryConnector     `json:"connectors"`
	WebsiteAccess     []InventoryWebsiteAccess `json:"website_access"`
	Tools             []string                 `json:"tools"`
	Skills            []string                 `json:"skills"`
}

type planProfile struct {
	category  string
	label     string
	domains   []string
	inputs    []inputSpec
	toolTerms []string
	action    bool
	auth      string // required, optional, or none
	payment   bool
	physical  bool
	evidence  []string
}

type inputSpec struct{ key, label, why string }

// LooksLikeActionGoal reports whether the goal needs an external action rather
// than a research-only answer. The runtime uses this to ensure Genie consults
// the action planner even when a smaller model ignores the prompt instruction.
func LooksLikeActionGoal(goal string) bool {
	goal = strings.ToLower(strings.TrimSpace(goal))
	return containsAny(goal,
		"book ", "reserve ", "buy ", "purchase ", "order ", "schedule ",
		"make an appointment", "send a ", "send an ", "text ", "notify ",
		"get me an uber", "want an uber", "need an uber", "call a taxi")
}

// BuildExecutionPlan combines intent with the capabilities installed right
// now. Genie supplies knownInputs as labels and non-secret values; only their
// presence is recorded in the resulting plan.
func BuildExecutionPlan(goal string, knownInputs map[string]string, inventory CapabilityInventory) (ExecutionPlan, error) {
	goal = strings.TrimSpace(goal)
	if len(goal) < 5 {
		return ExecutionPlan{}, errors.New("describe the outcome in a little more detail")
	}
	if len(goal) > 2000 {
		return ExecutionPlan{}, errors.New("goal must be 2000 characters or fewer")
	}
	known, err := normalizeKnownInputs(knownInputs)
	if err != nil {
		return ExecutionPlan{}, err
	}
	profile := executionProfile(strings.ToLower(goal))
	plan := ExecutionPlan{
		Goal: goal, Category: profile.category, GeneratedAt: time.Now().UTC(),
		Capabilities: []ExecutionCapability{}, RequiredInputs: []ExecutionRequirement{},
		Steps: []ExecutionStep{}, Routes: []ExecutionRoute{}, CapabilityGaps: []string{}, ApprovalCheckpoints: []string{},
		CompletionEvidence: append([]string{}, profile.evidence...),
	}

	plan.Capabilities = append(plan.Capabilities, ExecutionCapability{
		ID: "public_web", Label: "Public web research", Kind: "public_web", Available: inventory.PublicWeb,
		Detail: availableDetail(inventory.PublicWeb, "Can research public options and visible details without credentials.", "Public web access is unavailable."),
	})

	connector := matchingConnector(profile, inventory.Connectors)
	if connector != nil {
		plan.Capabilities = append(plan.Capabilities, ExecutionCapability{ID: "connector", Label: connector.Name, Kind: "connector", Available: true,
			Detail: "A user-owned connector can guide discovery across " + strings.Join(connector.Domains, ", ") + ". An action still uses a compatible tool or the provider website."})
	}

	actionTool := matchingActionTool(profile, inventory.Tools)
	browserAvailable := inventory.BrowserAutomation
	if actionTool != "" {
		plan.Capabilities = append(plan.Capabilities, ExecutionCapability{ID: "action_tool", Label: "Direct action tool", Kind: "mcp", Available: true,
			Detail: "Soulacy can use " + actionTool + " for this type of action."})
	}
	if profile.action {
		detail := inventory.BrowserDetail
		if detail == "" {
			detail = availableDetail(browserAvailable, "A browser automation route is installed.", "The provider website route will be prepared after the required details are collected.")
		}
		plan.Capabilities = append(plan.Capabilities, ExecutionCapability{ID: "browser", Label: "Browser automation", Kind: "browser", Available: browserAvailable,
			Detail: detail, SetupHref: "#mcp"})
		plan.Routes = executionRoutes(profile, actionTool, browserAvailable)
	}

	for _, spec := range profile.inputs {
		status := "needed"
		if strings.TrimSpace(known[spec.key]) != "" {
			status = "provided"
		}
		plan.RequiredInputs = append(plan.RequiredInputs, ExecutionRequirement{Key: spec.key, Label: spec.label, Why: spec.why, Status: status})
	}

	authReady, authName, authConnectionID := matchingWebsiteAccess(profile.domains, inventory.WebsiteAccess)
	if profile.auth != "none" {
		status := "optional"
		if authReady {
			status = "ready"
		} else if profile.auth == "required" {
			status = "secure_setup"
		}
		detail := "Sign in through Website Access if the provider requires an account. Credentials stay out of chat."
		if authReady {
			detail = "Encrypted Website Access is ready through " + authName + "."
		}
		plan.RequiredInputs = append(plan.RequiredInputs, ExecutionRequirement{Key: "website_access", Label: "Provider sign-in", Why: detail,
			Status: status, Sensitive: true, SetupHref: "#websites", ConnectionID: authConnectionID})
	}
	if profile.payment {
		status := "secure_setup"
		if value := strings.ToLower(strings.TrimSpace(known["payment_method"])); value == "saved" || value == "available" || value == "provider" {
			status = "provided"
		}
		plan.RequiredInputs = append(plan.RequiredInputs, ExecutionRequirement{Key: "payment_method", Label: "Payment method available with the provider",
			Why:    "Confirm only that a payment method is available. Enter card details directly in the provider's secure checkout, never in Genie chat.",
			Status: status, Sensitive: true, SetupHref: "#websites"})
	}

	if profile.physical {
		plan.Status = PlanUnsupported
		plan.Route = "human_handoff"
		plan.Summary = "This goal requires physical presence. Genie can research, coordinate, schedule, and prepare instructions, but cannot perform the physical act."
		plan.CapabilityGaps = append(plan.CapabilityGaps, "No digital tool can perform the required physical action.")
		plan.Steps = append(plan.Steps,
			ExecutionStep{ID: "research", Kind: "discover", Label: "Prepare the handoff", Detail: "Research options and organize the information a person or service needs.", Status: "ready", CapabilityID: "public_web"},
			ExecutionStep{ID: "handoff", Kind: "handoff", Label: "Hand off the physical step", Detail: "Ask the user to choose a person or service to perform the physical work.", Status: "waiting"})
		return plan, nil
	}

	plan.Steps = append(plan.Steps, ExecutionStep{ID: "discover", Kind: "discover", Label: "Find and compare options",
		Detail: "Use public information first and narrow the choices against the user's constraints.", Status: statusFor(inventory.PublicWeb), CapabilityID: "public_web"})
	if len(profile.inputs) > 0 {
		plan.Steps = append(plan.Steps, ExecutionStep{ID: "inputs", Kind: "input", Label: "Collect only missing details",
			Detail: "Ask for the minimum information needed for the next step. Do not ask for credentials or payment numbers in chat.", Status: requirementStepStatus(plan.RequiredInputs)})
	}
	if profile.auth != "none" {
		plan.Steps = append(plan.Steps, ExecutionStep{ID: "access", Kind: "authentication", Label: "Use secure provider access",
			Detail: "Use a domain-restricted Website Access session when account access is required.", Status: authStepStatus(profile.auth, authReady), CapabilityID: "website_access"})
	}

	if profile.action {
		executionStatus := "preparation_needed"
		capabilityID := "browser"
		detail := "Use the provider's official website to prepare the action and stop before the final commitment. Soulacy should prepare browser execution after the required details are known."
		if actionTool != "" {
			executionStatus, capabilityID = "ready", "action_tool"
			detail = "Use the installed direct action tool to prepare the request."
		} else if browserAvailable {
			executionStatus = "ready"
		}
		plan.Steps = append(plan.Steps, ExecutionStep{ID: "execute", Kind: "execute", Label: "Prepare the action", Detail: detail,
			Status: executionStatus, CapabilityID: capabilityID})
		approval := "Approve the final " + profile.label + " after Genie shows the exact provider, time, terms, and total cost."
		plan.ApprovalCheckpoints = append(plan.ApprovalCheckpoints, approval)
		plan.Steps = append(plan.Steps, ExecutionStep{ID: "approve", Kind: "approval", Label: "Ask for final approval", Detail: approval,
			Status: "waiting", RequiresApproval: true})
		plan.Steps = append(plan.Steps, ExecutionStep{ID: "verify", Kind: "verify", Label: "Verify completion",
			Detail: "Capture the provider's confirmation and report any unresolved condition.", Status: "waiting"})
	} else {
		plan.Steps = append(plan.Steps, ExecutionStep{ID: "deliver", Kind: "deliver", Label: "Deliver the result",
			Detail: "Provide the requested result with sources and clearly state any uncertainty.", Status: "ready"})
	}

	missingInputs, secureSetup := requirementCounts(plan.RequiredInputs)
	runtimeSetup := profile.action && actionTool == "" && !browserAvailable
	switch {
	case missingInputs > 0:
		plan.Status = PlanNeedsInput
	case secureSetup > 0 || runtimeSetup:
		plan.Status = PlanNeedsSetup
	default:
		plan.Status = PlanReady
	}
	plan.Route = executionRoute(profile.action, actionTool)
	plan.Summary = executionSummary(profile, plan, missingInputs, secureSetup, runtimeSetup)
	return plan, nil
}

func normalizeKnownInputs(in map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for key, value := range in {
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		if len(key) > 80 || len(value) > 500 {
			return nil, errors.New("known input is too long")
		}
		for _, forbidden := range []string{"password", "passcode", "card_number", "cvv", "cookie", "token", "secret", "storage_state"} {
			if strings.Contains(key, forbidden) {
				return nil, fmt.Errorf("do not send %s in chat; use Website Access or the provider's secure checkout", key)
			}
		}
		out[key] = value
	}
	return out, nil
}

func executionProfile(goal string) planProfile {
	physicalTerms := []string{"perform surgery", "physically clean", "move the furniture", "drive me there", "hand deliver", "repair the plumbing"}
	if containsAny(goal, physicalTerms...) {
		return planProfile{category: "physical", label: "physical task", physical: true, evidence: []string{"A named person or service accepts the handoff."}}
	}
	if containsAny(goal, "uber", "lyft", "book a ride", "order a ride", "call a taxi", "ride to ") {
		return planProfile{category: "ride", label: "ride booking", domains: []string{"uber.com", "lyft.com"}, action: true, auth: "required", payment: true,
			inputs:    []inputSpec{{"pickup", "Pickup location", "The provider needs a precise pickup point."}, {"destination", "Destination", "The provider needs a destination."}, {"ride_time", "Pickup time", "Choose now or a scheduled time."}},
			toolTerms: []string{"uber", "lyft", "ride", "taxi"}, evidence: []string{"Provider confirmation or trip ID", "Final quoted price", "Pickup time and location"}}
	}
	if containsAny(goal, "reserve a table", "book a table", "restaurant reservation", "opentable", "resy", "dinner reservation") {
		return planProfile{category: "restaurant", label: "restaurant reservation", domains: []string{"opentable.com", "resy.com"}, action: true, auth: "optional",
			inputs:    []inputSpec{{"location", "Location or restaurant", "Genie needs the search area or preferred restaurant."}, {"date", "Date", "The reservation date is required."}, {"time", "Time or acceptable range", "A time or range is required to check availability."}, {"party_size", "Party size", "Availability depends on the number of guests."}, {"preferences", "Preferences", "Cuisine, budget, seating, and accessibility preferences improve the result."}},
			toolTerms: []string{"restaurant", "reservation", "opentable", "resy"}, evidence: []string{"Reservation confirmation", "Restaurant, date, time, and party size", "Cancellation terms"}}
	}
	if containsAny(goal, "buy ", "purchase ", "order ", "shopping", "shop for") {
		return planProfile{category: "shopping", label: "purchase", action: true, auth: "required", payment: true,
			inputs:    []inputSpec{{"item", "Item", "Describe what to buy."}, {"constraints", "Product constraints", "Brand, size, color, quality, or delivery constraints."}, {"spend_limit", "Maximum total price", "Genie needs a firm limit before checkout."}, {"delivery", "Delivery destination or timing", "Confirm delivery requirements without sharing payment details."}},
			toolTerms: []string{"cart", "checkout", "purchase", "order"}, evidence: []string{"Order confirmation", "Final total and delivery estimate", "Cancellation or return terms"}}
	}
	if containsAny(goal, "book a flight", "book a hotel", "reserve a hotel", "travel booking", "airbnb") {
		return planProfile{category: "travel", label: "travel booking", action: true, auth: "required", payment: true,
			inputs:    []inputSpec{{"destination", "Destination", "The destination is required."}, {"dates", "Dates", "Travel dates or a flexible range are required."}, {"travelers", "Travelers", "Traveler count and needs affect availability."}, {"constraints", "Travel preferences", "Budget, schedule, room, baggage, and accessibility constraints."}, {"spend_limit", "Maximum total price", "Genie needs a firm limit before booking."}},
			toolTerms: []string{"flight", "hotel", "booking", "travel"}, evidence: []string{"Booking confirmation", "Final itinerary and total", "Change and cancellation terms"}}
	}
	if containsAny(goal, "schedule an appointment", "book an appointment", "make an appointment") {
		return planProfile{category: "appointment", label: "appointment", action: true, auth: "optional",
			inputs:    []inputSpec{{"provider", "Provider or service", "Identify the person, business, or service."}, {"date_range", "Acceptable dates and times", "Genie needs a scheduling window."}, {"location", "Location or format", "Choose in-person, phone, or video when relevant."}, {"constraints", "Other requirements", "Include duration, insurance, accessibility, or service details."}},
			toolTerms: []string{"appointment", "calendar", "schedule"}, evidence: []string{"Appointment confirmation", "Date, time, provider, and location", "Rescheduling instructions"}}
	}
	if containsAny(goal, "send a message", "send an email", "text ", "notify ") {
		return planProfile{category: "communication", label: "message", action: true, auth: "none",
			inputs:    []inputSpec{{"recipient", "Recipient", "Identify who should receive it."}, {"content", "Message content", "Confirm the message or the points Genie should draft."}, {"channel", "Channel", "Choose email, SMS, WhatsApp, Telegram, or another available channel."}},
			toolTerms: []string{"send", "email", "message", "whatsapp", "telegram"}, evidence: []string{"Delivery result from the selected channel"}}
	}
	return planProfile{category: "research", label: "research result", auth: "none",
		inputs:   []inputSpec{{"constraints", "Success criteria or constraints", "Tell Genie what makes the result useful."}},
		evidence: []string{"Cited result", "Any uncertainty or unavailable source is stated"}}
}

func matchingConnector(profile planProfile, connectors []InventoryConnector) *InventoryConnector {
	for i := range connectors {
		if connectors[i].Category == profile.category || domainsOverlap(profile.domains, connectors[i].Domains) {
			return &connectors[i]
		}
	}
	return nil
}

func matchingWebsiteAccess(domains []string, connections []InventoryWebsiteAccess) (bool, string, string) {
	for _, connection := range connections {
		if connection.Ready && (len(domains) == 0 || domainsOverlap(domains, connection.Domains)) {
			return true, connection.Name, connection.ID
		}
	}
	return false, "", ""
}

func matchingActionTool(profile planProfile, tools []string) string {
	for _, tool := range tools {
		lower := strings.ToLower(tool)
		if containsAny(lower, profile.toolTerms...) && containsAny(lower, "book", "create", "reserve", "order", "purchase", "send", "schedule", "request") {
			return tool
		}
	}
	return ""
}

func DetectBrowserAutomation(tools []string) (bool, string) {
	var matches []string
	for _, tool := range tools {
		lower := strings.ToLower(tool)
		if strings.Contains(lower, "playwright") || strings.Contains(lower, "browser_navigate") || strings.Contains(lower, "browser_click") || strings.Contains(lower, "browser_type") {
			matches = append(matches, tool)
		}
	}
	if len(matches) == 0 {
		return false, "No connected MCP server exposes browser navigation and interaction tools."
	}
	sort.Strings(matches)
	if len(matches) > 3 {
		matches = matches[:3]
	}
	return true, "Available through " + strings.Join(matches, ", ") + "."
}

func requirementCounts(requirements []ExecutionRequirement) (missing, setup int) {
	for _, requirement := range requirements {
		switch requirement.Status {
		case "needed":
			missing++
		case "secure_setup":
			setup++
		}
	}
	return missing, setup
}

func requirementStepStatus(requirements []ExecutionRequirement) string {
	missing, _ := requirementCounts(requirements)
	if missing > 0 {
		return "waiting"
	}
	return "ready"
}

func authStepStatus(mode string, ready bool) string {
	if ready || mode == "optional" {
		return "ready"
	}
	return "waiting"
}

func executionRoute(action bool, tool string) string {
	if !action {
		return "public_web"
	}
	if tool != "" {
		return "direct_tool"
	}
	return "provider_website"
}

func executionRoutes(profile planProfile, tool string, browser bool) []ExecutionRoute {
	directStatus := "not_selected"
	directDetail := "Use the provider website for this request."
	if tool != "" {
		directStatus = "selected"
		directDetail = "Use " + tool + " as the fastest supported route."
	}
	websiteStatus := "fallback"
	if tool == "" {
		websiteStatus = "selected"
	}
	websiteDetail := "Use the provider's official website in a secure managed browser."
	return []ExecutionRoute{
		{Priority: 1, ID: "direct_integration", Label: "Connector or native API", Kind: "direct_tool", Status: directStatus, Detail: directDetail, CapabilityID: "action_tool"},
		{Priority: 2, ID: "provider_website", Label: "Provider website", Kind: "browser", Status: websiteStatus, Detail: websiteDetail, CapabilityID: "browser"},
		{Priority: 3, ID: "official_alternative", Label: "Another official route", Kind: "alternative", Status: "fallback", Detail: alternativeRouteDetail(profile)},
	}
}

func alternativeRouteDetail(profile planProfile) string {
	switch profile.category {
	case "ride":
		return "If the requested provider cannot complete the ride, find another licensed ride or taxi service and ask before switching providers."
	case "restaurant":
		return "If online booking fails, look for the restaurant's official phone or contact route and ask before using it."
	case "shopping", "travel", "appointment":
		return "If the first website cannot complete the request, find another official seller, provider, or contact route and ask before switching."
	default:
		return "Find another official channel that can complete the action and ask before changing providers or terms."
	}
}

func executionSummary(profile planProfile, plan ExecutionPlan, missing, setup int, runtimeSetup bool) string {
	if plan.Status == PlanReady {
		if profile.action {
			return "Genie has a workable route. It can prepare the action, show the final terms, ask for approval, and verify the confirmation."
		}
		return "Genie can complete this with the capabilities available now."
	}
	if profile.action && missing > 0 {
		return fmt.Sprintf("Genie has selected the best available route and needs %d detail(s) from the user. Ask for those details first, then prepare secure sign-in and the action route. Do not send the user away to complete the task.", missing)
	}
	if profile.action && (setup > 0 || runtimeSetup) {
		return "The provider website is the selected route. Prepare secure sign-in and browser execution, then show the final terms for approval. Do not tell the user that a connector is required."
	}
	parts := []string{}
	if missing > 0 {
		parts = append(parts, fmt.Sprintf("%d detail(s) still need the user", missing))
	}
	if setup > 0 {
		parts = append(parts, fmt.Sprintf("%d secure setup step(s) remain", setup))
	}
	if len(plan.CapabilityGaps) > 0 {
		parts = append(parts, fmt.Sprintf("%d capability gap(s) remain", len(plan.CapabilityGaps)))
	}
	return "Genie has formed an approach, but " + strings.Join(parts, ", ") + "."
}

func statusFor(available bool) string {
	if available {
		return "ready"
	}
	return "blocked"
}

func availableDetail(available bool, yes, no string) string {
	if available {
		return yes
	}
	return no
}

func domainsOverlap(a, b []string) bool {
	for _, left := range a {
		left = strings.TrimPrefix(strings.ToLower(left), "www.")
		for _, right := range b {
			right = strings.TrimPrefix(strings.ToLower(right), "www.")
			if left == right || strings.HasSuffix(left, "."+right) || strings.HasSuffix(right, "."+left) {
				return true
			}
		}
	}
	return false
}

func containsAny(value string, terms ...string) bool {
	for _, term := range terms {
		if term != "" && strings.Contains(value, term) {
			return true
		}
	}
	return false
}
