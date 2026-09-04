package studio

import (
	"strings"
	"testing"
)

// The generator is allowed to invent helper agents — that is a feature, and
// every invented peer becomes a real agent in the user's workspace on save.
// What it must not do is invent one the workspace already has. Listing the
// available agents is not enough on its own; the prompt has to say to use them.
func TestBuildPrompt_AsksTheModelToReuseExistingAgents(t *testing.T) {
	cat := Catalog{Agents: []string{"summarizer", "notifier"}}
	p := BuildPrompt("Search for travel options and summarise them", cat, nil)

	if !strings.Contains(p, "Available agents: summarizer, notifier") {
		t.Fatal("prompt should list the agents the workspace already has")
	}
	if !strings.Contains(p, "PREFER AN EXISTING AGENT") {
		t.Fatal("prompt lists existing agents but never tells the model to reuse them")
	}
	// The instruction is worthless if it doesn't say how to reference one.
	if !strings.Contains(p, `"agent"`) {
		t.Fatal("reuse instruction should name the node field to set")
	}
}

// With no agents in the catalog there is nothing to reuse, so the instruction
// must not appear — a rule about an empty list is just noise in the prompt.
func TestBuildPrompt_NoReuseInstructionWithoutAgents(t *testing.T) {
	p := BuildPrompt("Do a thing", Catalog{}, nil)
	if strings.Contains(p, "PREFER AN EXISTING AGENT") {
		t.Fatal("reuse instruction should be omitted when the catalog has no agents")
	}
}
