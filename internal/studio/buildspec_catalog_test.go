package studio

import (
	"strings"
	"testing"
)

// specCatalog is travelCatalog (conversational_intent_test.go) plus decoys: a
// second MCP server and two skills the intent never mentions, so a passing test
// cannot be passing merely because everything in the catalogue matched.
func specCatalog() Catalog {
	cat := travelCatalog()
	cat.MCP = append(cat.MCP, CatalogMCPServer{
		Server: "jenkins",
		Tools:  []CatalogMCPTool{{Name: "mcp__jenkins__build", Description: "Trigger a CI build."}},
	})
	cat.Skills = []CatalogSkill{{Name: "itinerary-writer"}, {Name: "payroll-export"}}
	return cat
}

func TestExtractBuildSpecFromReportsNamedMCPServer(t *testing.T) {
	const intent = "A conversational travel advisor agent that answers user travel " +
		"questions about flight and hotel options using the trvl MCP travel tool"

	spec := ExtractBuildSpecFrom(intent, specCatalog())

	if len(spec.Integrations) == 0 {
		t.Fatal("capabilities empty for a prompt that names an installed MCP server; " +
			"this is the 'not specified' bug the panel showed")
	}
	joined := strings.ToLower(strings.Join(spec.Integrations, " "))
	if !strings.Contains(joined, "trvl") {
		t.Errorf("expected the trvl server to be reported, got %v", spec.Integrations)
	}
	if strings.Contains(joined, "jenkins") {
		t.Errorf("reported an MCP server the intent never mentioned: %v", spec.Integrations)
	}
	if strings.Contains(joined, "payroll") {
		t.Errorf("reported an unrelated skill: %v", spec.Integrations)
	}
}

// The blocker is the part the user actually hit: the panel claimed it could not
// tell what the agent should do, for a prompt that said so in its first clause.
func TestNamingACapabilityIsNotAMissingProcessingStep(t *testing.T) {
	const intent = "A conversational travel advisor agent that answers user travel " +
		"questions about flight and hotel options using the trvl MCP travel tool"

	spec := ExtractBuildSpecFrom(intent, specCatalog())

	for _, q := range spec.Questions {
		if q.ID == "stages" && q.Blocker {
			t.Fatalf("blocked with %q — but the intent names both the job and the tool", q.Why)
		}
	}
	if !spec.Ready() {
		t.Errorf("spec not ready; blockers: %+v", spec.Blockers())
	}
}

// The blocker must still fire when it is genuinely right to. Loosening a
// validation is only safe if the case it was built for still trips it.
func TestVagueIntentWithNoCapabilityStillBlocks(t *testing.T) {
	spec := ExtractBuildSpecFrom("something helpful for the team", specCatalog())

	var found bool
	for _, q := range spec.Questions {
		if q.ID == "stages" && q.Blocker {
			found = true
		}
	}
	if !found {
		t.Error("a vague intent naming no capability and no stage should still block")
	}
}

// A spec that is not ready must always say WHY. An unexplained "not ready" is
// what produced the disabled Generate button with an empty blocker list — the
// user fixed everything Studio asked for and the button stayed dead.
func TestNotReadyAlwaysHasAtLeastOneBlocker(t *testing.T) {
	for _, intent := range []string{
		"",
		"something helpful for the team",
		"do the thing",
		"answer travel questions using the trvl MCP travel tool",
		"Every weekday at 7am summarise the news and send it to Telegram",
		"be useful",
	} {
		spec := ExtractBuildSpecFrom(intent, specCatalog())
		if !spec.Ready() && len(spec.Blockers()) == 0 {
			t.Errorf("intent %q is not ready but lists no blocker: "+
				"the UI would disable Generate with nothing for the user to fix", intent)
		}
		if spec.Ready() && len(spec.Blockers()) > 0 {
			t.Errorf("intent %q is ready but still lists blockers %+v", intent, spec.Blockers())
		}
	}
}

// An empty catalogue must degrade to the old brand-list behaviour rather than
// erroring — ExtractBuildSpec still has callers that have no catalogue to give.
func TestExtractBuildSpecWithoutCatalogKeepsBrandMatching(t *testing.T) {
	spec := ExtractBuildSpec("Summarise my Notion pages every morning and post to Slack")
	joined := strings.Join(spec.Integrations, " ")
	if !strings.Contains(joined, "Notion") {
		t.Errorf("brand matching regressed, got %v", spec.Integrations)
	}
}

// A server the user names but has NOT installed must not be reported as a
// capability. The panel's job is to say what Studio can actually build with.
func TestUninstalledServerIsNotReportedAsCapability(t *testing.T) {
	spec := ExtractBuildSpecFrom(
		"answer travel questions using the trvl MCP travel tool", Catalog{})
	for _, in := range spec.Integrations {
		if strings.Contains(strings.ToLower(in), "trvl") {
			t.Errorf("reported trvl as available with an empty catalogue: %v", spec.Integrations)
		}
	}
}
