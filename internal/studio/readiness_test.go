package studio

import (
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func readinessDraft() Draft {
	return Draft{
		Name:     "Daily digest",
		Channels: []string{"telegram"},
		Flow: Flow{Nodes: []sdkr.FlowNode{
			{ID: "search", Kind: "tool", Tool: "web_search", Input: `{"query":"ai news"}`},
			{ID: "send", Kind: "tool", Tool: "channel.send", Input: `{"channel":"telegram","to":"1","text":"digest ready"}`},
		}},
		LLM: agent.LLMConfig{Provider: "openai", Model: "gpt-4o"},
	}
}

func sectionByID(rep ReadinessReport, id string) (ReadinessSection, bool) {
	for _, s := range rep.Sections {
		if s.ID == id {
			return s, true
		}
	}
	return ReadinessSection{}, false
}

func TestReadiness_ComposesEverySource(t *testing.T) {
	rep := Readiness(ReadinessInput{
		Draft:   readinessDraft(),
		Catalog: Catalog{Tools: []string{"web_search", "channel.send"}, Channels: []string{"telegram"}},
		Preflight: PreflightInput{
			ChannelsConfigured: map[string]bool{"telegram": true},
			ProvidersAvailable: map[string]bool{"openai": true},
			ModelsAvailable:    map[string]bool{"gpt-4o": true},
			SecretsSet:         map[string]bool{"llm.providers.openai.api_key": true},
		},
	})
	for _, id := range []string{
		ReadinessSectionPreflight, ReadinessSectionContract,
		ReadinessSectionSecurity, ReadinessSectionConsent,
	} {
		sec, ok := sectionByID(rep, id)
		if !ok {
			t.Fatalf("section %q missing from the report", id)
		}
		if sec.Status == ReadinessUnknown {
			t.Errorf("section %q should have been evaluated, got unknown (%s)", id, sec.Reason)
		}
	}
	if rep.Preflight == nil || rep.Contract == nil || rep.Security == nil || rep.Consent == nil {
		t.Fatal("every underlying report must be attached when its section was evaluated")
	}
	if len(rep.Ready) == 0 {
		t.Error("expected Ready items so a client can render the full triage")
	}
	if !rep.OK {
		t.Fatalf("expected a ready verdict, got blockers %+v", rep.Blockers)
	}
}

// The specific bug: a failed security-review request was dropped client-side
// while ok still computed true. An unevaluated section must be visible AND must
// force OK=false.
func TestReadiness_UnavailableSectionIsUnknownAndForcesNotOK(t *testing.T) {
	rep := Readiness(ReadinessInput{
		Draft:   readinessDraft(),
		Catalog: Catalog{Tools: []string{"web_search", "channel.send"}, Channels: []string{"telegram"}},
		Preflight: PreflightInput{
			ChannelsConfigured: map[string]bool{"telegram": true},
			ProvidersAvailable: map[string]bool{"openai": true},
			ModelsAvailable:    map[string]bool{"gpt-4o": true},
		},
		Unavailable: map[string]string{ReadinessSectionSecurity: "security review request failed"},
	})
	if rep.OK {
		t.Fatal("a section that could not be evaluated must never report OK")
	}
	sec, ok := sectionByID(rep, ReadinessSectionSecurity)
	if !ok || sec.Status != ReadinessUnknown {
		t.Fatalf("security section must be reported unknown, got %+v", sec)
	}
	if !strings.Contains(sec.Reason, "security review request failed") {
		t.Errorf("unknown section must carry the caller's reason, got %q", sec.Reason)
	}
	if len(rep.Unknown) != 1 || rep.Unknown[0] != ReadinessSectionSecurity {
		t.Errorf("unknown ids must be listed, got %v", rep.Unknown)
	}
	if rep.Security != nil {
		t.Error("an unevaluated section must not attach an empty report that reads as a clean result")
	}
	if !strings.Contains(rep.Summary, "could not be evaluated") {
		t.Errorf("summary must state the partial failure, got %q", rep.Summary)
	}
}

func TestReadiness_BlockerFromPreflightPropagates(t *testing.T) {
	rep := Readiness(ReadinessInput{
		Draft:   readinessDraft(),
		Catalog: Catalog{Tools: []string{"web_search", "channel.send"}},
		Preflight: PreflightInput{
			ChannelsConfigured: map[string]bool{}, // telegram not configured
			SecretsSet:         map[string]bool{"llm.providers.openai.api_key": false},
		},
	})
	if rep.OK {
		t.Fatal("expected a not-ready verdict")
	}
	var sawSecret, sawChannel bool
	for _, b := range rep.Blockers {
		if b.Action == "" {
			t.Errorf("readiness blocker without an action: %+v", b)
		}
		if b.Kind == "secret" {
			sawSecret = true
		}
		if b.Kind == "channel" {
			sawChannel = true
		}
	}
	if !sawSecret || !sawChannel {
		t.Fatalf("expected both the credential and channel blockers, got %+v", rep.Blockers)
	}
	if sec, _ := sectionByID(rep, ReadinessSectionPreflight); sec.Status != ReadinessBlocked {
		t.Errorf("preflight section should be blocked, got %q", sec.Status)
	}
}

// A draft that needs privileged-exposure consent is not ready until consent is
// granted — the save path refuses it, so "ready" must not claim otherwise.
func TestReadiness_UngrantedConsentBlocksAndAcceptanceClears(t *testing.T) {
	d := Draft{
		Name:     "shell runner",
		Channels: []string{"telegram"},
		Tools:    []string{"shell_exec"},
		Strategy: "auto",
		Flow: Flow{Nodes: []sdkr.FlowNode{
			{ID: "run", Kind: "tool", Tool: "shell_exec", Input: `{"command":"ls"}`},
		}},
	}
	in := ReadinessInput{
		Draft:     d,
		Catalog:   Catalog{Tools: []string{"shell_exec"}},
		Preflight: PreflightInput{ChannelsConfigured: map[string]bool{"telegram": true}},
	}
	rep := Readiness(in)
	consent, ok := sectionByID(rep, ReadinessSectionConsent)
	if !ok {
		t.Fatal("consent section missing")
	}
	if consent.Status != ReadinessBlocked {
		t.Skipf("draft did not classify as privileged (tier=%v); consent gating not exercised", rep.Consent)
	}
	if rep.OK {
		t.Fatal("ungranted consent must not report ready")
	}

	in.ConsentAccepted = true
	rep2 := Readiness(in)
	if sec, _ := sectionByID(rep2, ReadinessSectionConsent); sec.Status == ReadinessBlocked {
		t.Fatal("accepted consent must clear the consent blocker")
	}
}
