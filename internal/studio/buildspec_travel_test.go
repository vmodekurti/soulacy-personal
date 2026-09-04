package studio

import (
	"strings"
	"testing"
)

// The exact prompt from the browser session, asserted end to end. Every field
// here was wrong at once — capabilities blank, a false blocker, and a
// conversational agent described as manually triggered — so pinning the whole
// rendered spec is what proves the panel now agrees with the sentence it read.
func TestTravelAdvisorSpecMatchesWhatThePromptSays(t *testing.T) {
	const intent = "A conversational travel advisor agent that answers user travel " +
		"questions about flight and hotel options using the trvl MCP travel tool"

	spec := ExtractBuildSpecFrom(intent, specCatalog())

	if got := strings.Join(spec.Integrations, ", "); got != "trvl" {
		t.Errorf("capabilities = %q, want the server named in the prompt: trvl", got)
	}
	if spec.Trigger != "channel" {
		t.Errorf("trigger = %q, want channel: an agent that answers user questions "+
			"is not run by hand", spec.Trigger)
	}
	if !spec.Ready() {
		t.Errorf("not ready, blockers: %+v", spec.Blockers())
	}
	// The capability label reaches the user through the security note too, so a
	// raw mcp__server__tool identifier leaks into a sentence meant to be read.
	for _, s := range spec.Security {
		if strings.Contains(s, "mcp__") {
			t.Errorf("security note exposes a raw tool identifier: %q", s)
		}
	}
}
