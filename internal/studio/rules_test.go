package studio

import (
	"strings"
	"testing"
)

func TestDefaultSOULRulesAlignArchitectureModes(t *testing.T) {
	for _, want := range []string{
		"Use a Macro-Workflow graph for fixed, predictable pipelines",
		"use an 'auto' tool-calling agent for conversational assistants",
		"use 'plan_execute' for long multi-phase jobs",
		"Use 'react' only when the user explicitly asks",
		"channel.send uses the exact JSON arguments",
		"The field is text, not message",
	} {
		if !strings.Contains(DefaultSOULRules, want) {
			t.Fatalf("DefaultSOULRules missing %q", want)
		}
	}
	if strings.Contains(DefaultSOULRules, "Only use 'react' execution mode if a rigid workflow is totally impossible") {
		t.Fatal("DefaultSOULRules still contains stale workflow-first ReAct guidance")
	}
}
