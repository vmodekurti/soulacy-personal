package runtime

import (
	"fmt"
	"strings"

	"github.com/soulacy/soulacy/pkg/agent"
)

// builderMissionContract translates concrete deliverables into deterministic
// run checks. These checks stop a fluent progress summary from being recorded
// as success when the agent never used the route or produced the artifact the
// user asked for.
func builderMissionContract(u *BuilderUnderstanding, agentID string) *agent.MissionContract {
	if u == nil {
		return nil
	}
	goal := strings.TrimSpace(u.Purpose)
	if goal == "" {
		goal = strings.TrimSpace(u.Description)
	}
	text := strings.ToLower(strings.Join([]string{u.Purpose, u.Description, u.SystemPrompt}, "\n"))
	checks := make([]agent.MissionCheck, 0, 5)
	usesWebsiteAccess := len(u.Connections) > 0 || containsAnyBuilderPhrase(text,
		"website access", "saved website", "authenticated session", "authenticated content")

	if usesWebsiteAccess && containsAnyBuilderPhrase(text,
		"article", "research", "read ", "source", "trending", "subscription") {
		checks = append(checks, agent.MissionCheck{
			ID:          "website-content-read",
			Type:        agent.MissionCheckRequiredTool,
			Description: "Read authenticated website content through Website Access",
			Tool:        "authenticated_fetch",
		})
	}

	if builderNeedsInteractiveWebsite(u, text) {
		checks = append(checks,
			agent.MissionCheck{ID: "website-action-started", Type: agent.MissionCheckRequiredTool, Description: "Open the approved interactive website session", Tool: "start_website_action"},
			agent.MissionCheck{ID: "website-action-inspected", Type: agent.MissionCheckRequiredTool, Description: "Inspect the live website before acting", Tool: "inspect_website_action"},
			agent.MissionCheck{ID: "website-action-completed", Type: agent.MissionCheckRequiredTool, Description: "Perform the requested website action", Tool: "act_on_website"},
		)
	}

	if containsAnyBuilderPhrase(text, "podcast", "audio overview", "audio episode") {
		checks = append(checks, agent.MissionCheck{
			ID:          "audio-link-produced",
			Type:        agent.MissionCheckOutputRegex,
			Description: "Return a playable podcast or audio link",
			Value:       `(?is)((podcast|audio).{0,800}https?://|https?://.{0,800}(podcast|audio))`,
		})
	}

	if len(checks) == 0 {
		return nil
	}
	return &agent.MissionContract{
		ID:         "builder-" + agentID,
		Goal:       goal,
		Acceptance: checks,
	}
}

// effectiveBuilderMissionContract gives agents created by older Genie builds
// the same completion checks after an upgrade. Their saved definitions already
// carry Genie's ownership label and Website Access connection IDs, but predate
// the mission field added by this release.
func effectiveBuilderMissionContract(def *agent.Definition) *agent.MissionContract {
	if def == nil || def.Mission != nil {
		if def == nil {
			return nil
		}
		return def.Mission
	}
	ownedByGenie := def.Labels["soulacy.owner"] == GenieAgentID
	legacyScheduledWebsiteAgent := (def.Trigger == agent.TriggerCron || def.Trigger == agent.TriggerOneShot) &&
		len(def.Connections) > 0 &&
		containsAnyBuilderPhrase(strings.ToLower(def.Description+"\n"+def.SystemPrompt),
			"website access", "saved website", "authenticated session", "authenticated content")
	if !ownedByGenie && !legacyScheduledWebsiteAgent {
		return nil
	}
	connections := make([]BuilderConnection, 0, len(def.Connections))
	for _, id := range def.Connections {
		connections = append(connections, BuilderConnection{ID: id, Name: id, Ready: true})
	}
	return builderMissionContract(&BuilderUnderstanding{
		Name: def.ID, Description: def.Description, Purpose: def.Description,
		SystemPrompt: def.SystemPrompt, Connections: connections,
	}, def.ID)
}

func builderNeedsInteractiveWebsite(u *BuilderUnderstanding, text string) bool {
	for _, connection := range u.Connections {
		identity := strings.ToLower(connection.Name + " " + strings.Join(connection.Domains, " "))
		if strings.Contains(identity, "notebooklm") || strings.Contains(identity, "notebook.google") {
			return true
		}
	}
	if containsAnyBuilderPhrase(text, "notebooklm", "notebook.google", "website access") &&
		containsAnyBuilderPhrase(text, "create a notebook", "add them to", "generate podcast", "generate audio") {
		return true
	}
	return containsAnyBuilderPhrase(text,
		"create a notebook", "add them to", "generate podcast", "generate audio",
		"book ", "reserve ", "purchase ", "submit ", "fill out")
}

func containsAnyBuilderPhrase(text string, phrases ...string) bool {
	for _, phrase := range phrases {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

func missionYAML(contract *agent.MissionContract) string {
	if contract == nil || !contract.HasChecks() {
		return ""
	}
	var out strings.Builder
	out.WriteString("\nmission:\n")
	fmt.Fprintf(&out, "  id: %q\n", contract.ID)
	fmt.Fprintf(&out, "  goal: %q\n", contract.Goal)
	out.WriteString("  acceptance:\n")
	for _, check := range contract.Acceptance {
		fmt.Fprintf(&out, "    - id: %q\n", check.ID)
		fmt.Fprintf(&out, "      type: %q\n", check.Type)
		if check.Description != "" {
			fmt.Fprintf(&out, "      description: %q\n", check.Description)
		}
		if check.Tool != "" {
			fmt.Fprintf(&out, "      tool: %q\n", check.Tool)
		}
		if check.Value != "" {
			fmt.Fprintf(&out, "      value: %q\n", check.Value)
		}
	}
	return out.String()
}
