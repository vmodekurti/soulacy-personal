package agentsave

import (
	"context"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/pkg/agent"
)

func ok(t *testing.T, def *agent.Definition, opts Options) Decision {
	t.Helper()
	return Gate(context.Background(), def, opts)
}

// The two built-ins are never overwritten, whatever produced the definition.
func TestProtectedBuiltinsAreRefusedOutright(t *testing.T) {
	for _, id := range []string{runtime.SystemAgentID, runtime.GenieAgentID} {
		d := ok(t, &agent.Definition{ID: id, Name: id, SystemPrompt: "x"}, Options{})
		if d.Allowed {
			t.Errorf("%s must not be overwritable", id)
		}
		if d.Refused == "" {
			t.Errorf("%s should be refused with a reason, not merely blocked", id)
		}
	}
}

// Steward is core but ordinary: undeletable, still writable.
func TestAnOrdinaryCoreAgentCanStillBeSaved(t *testing.T) {
	d := ok(t, &agent.Definition{
		ID: runtime.StewardAgentID, Name: "Steward", SystemPrompt: "Look after the day.",
	}, Options{})
	if d.Refused != "" {
		t.Errorf("Steward is editable; got refusal %q", d.Refused)
	}
}

func TestAnAgentWithoutAnIdCannotBeSaved(t *testing.T) {
	d := ok(t, &agent.Definition{Name: "nameless"}, Options{})
	if d.Allowed || len(d.Blockers) == 0 {
		t.Fatalf("an agent with no id is not saveable: %+v", d)
	}
}

func TestNilDefinitionIsRefusedRatherThanPanicking(t *testing.T) {
	if d := ok(t, nil, Options{}); d.Allowed || d.Refused == "" {
		t.Fatalf("nil should be refused: %+v", d)
	}
}

// The rule Studio applies, now applied wherever an agent is written: a
// privileged agent reachable on a channel is a decision for a person.
func TestPrivilegedAgentOnAChannelNeedsConsent(t *testing.T) {
	star := []string{"*"}
	def := &agent.Definition{
		ID: "wide", Name: "Wide", SystemPrompt: "x",
		MCPServers: &star,                // wildcard MCP is what makes it privileged
		Channels:   []string{"telegram"}, // and a channel is what exposes it
	}
	d := ok(t, def, Options{})
	if !d.RequiresConsent {
		t.Fatalf("a privileged agent on a channel should need consent: %+v", d)
	}
	if d.Allowed {
		t.Error("it must not be saveable while consent is outstanding")
	}
	if len(d.ConsentItems) == 0 || d.ConsentItems[0].Name != "telegram" {
		t.Errorf("the consent item should name the channel: %+v", d.ConsentItems)
	}
	if d.ConsentItems[0].Reason == "" {
		t.Error("a consent request with no reason is not a request, it is a dialog")
	}

	// Once a person has said yes, it saves.
	accepted := ok(t, def, Options{AcceptPrivilegedExposure: true})
	if accepted.RequiresConsent || !accepted.Allowed {
		t.Errorf("accepted exposure should be saveable: %+v", accepted)
	}
}

// The same wide agent with no channel is not exposed, so nothing to consent to.
func TestPrivilegedWithoutAChannelIsFine(t *testing.T) {
	star := []string{"*"}
	d := ok(t, &agent.Definition{ID: "wide", Name: "Wide", SystemPrompt: "x", MCPServers: &star}, Options{})
	if d.RequiresConsent {
		t.Errorf("no channel means no exposure: %+v", d.ConsentItems)
	}
}

// An ordinary scheduled agent — the shape the front door and Genie's monitors
// both produce — passes without ceremony. A gate that blocks the common case
// gets routed around.
func TestAnOrdinaryScheduledAgentPasses(t *testing.T) {
	builtins := []string{"web_search"}
	d := ok(t, &agent.Definition{
		ID: "morning-brief", Name: "Morning Brief", SystemPrompt: "Summarise the news.",
		Enabled: true, Surfaces: []string{"schedule"}, Builtins: &builtins,
		Trigger: agent.TriggerCron, Schedule: &agent.Schedule{Cron: "0 7 * * *"},
	}, Options{})
	if !d.Allowed {
		t.Fatalf("an ordinary scheduled agent should save: refused=%q blockers=%+v consent=%v",
			d.Refused, d.Blockers, d.RequiresConsent)
	}
}

// Consent is never inferred from the caller's intent: a model asking to save
// something does not constitute the user accepting it.
func TestConsentIsNotAssumedFromTheCaller(t *testing.T) {
	star := []string{"*"}
	def := &agent.Definition{ID: "wide", Name: "Wide", SystemPrompt: "x", MCPServers: &star, Channels: []string{"slack"}}
	if d := ok(t, def, Options{}); !d.RequiresConsent {
		t.Fatal("the default must be to ask")
	}
}

func TestBlockersCarryEnoughToActOn(t *testing.T) {
	d := ok(t, &agent.Definition{Name: "no id"}, Options{})
	for _, b := range d.Blockers {
		if strings.TrimSpace(b.Problem) == "" {
			t.Error("a blocker with no problem statement cannot be fixed")
		}
	}
}
