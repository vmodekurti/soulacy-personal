package studio

import (
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// baseDraft is a minimal, otherwise-clean draft so each test's assertion is
// about the check under test and not about unrelated blockers.
func baseDraft() Draft {
	return Draft{
		Name:     "Daily digest",
		Channels: []string{"telegram"},
		Flow: Flow{Nodes: []sdkr.FlowNode{
			{ID: "search", Kind: "tool", Tool: "web_search", Input: `{"query":"ai news"}`},
		}},
		LLM: agent.LLMConfig{Provider: "openai", Model: "gpt-4o"},
	}
}

func issuesOfKind(issues []PreflightIssue, kind string) []PreflightIssue {
	var out []PreflightIssue
	for _, i := range issues {
		if i.Kind == kind {
			out = append(out, i)
		}
	}
	return out
}

// ST-07: SecretsSet was collected and never read, so a workspace with no
// credentials reported green. A missing credential must block AND name itself.
func TestPreflight_MissingSecretBlocksAndNamesIt(t *testing.T) {
	in := PreflightInput{
		Catalog:            Catalog{Tools: []string{"web_search"}},
		ChannelsConfigured: map[string]bool{"telegram": true},
		SecretsSet: map[string]bool{
			"llm.providers.openai.api_key": false,
			"channels.telegram.bot_token":  true,
		},
	}
	r := Preflight(baseDraft(), in)
	if r.OK {
		t.Fatal("expected a blocker for the missing provider credential")
	}
	secrets := issuesOfKind(r.Blockers, "secret")
	if len(secrets) != 1 {
		t.Fatalf("expected exactly 1 secret blocker, got %+v", r.Blockers)
	}
	b := secrets[0]
	if !strings.Contains(b.Fix, "llm.providers.openai.api_key") {
		t.Errorf("Fix must name the exact secret, got %q", b.Fix)
	}
	if b.Action != "open_providers" {
		t.Errorf("expected action open_providers, got %q", b.Action)
	}
	if b.ActionParams["secret"] != "llm.providers.openai.api_key" {
		t.Errorf("expected the secret name in ActionParams, got %+v", b.ActionParams)
	}
}

func TestPreflight_SecretPresentPasses(t *testing.T) {
	in := PreflightInput{
		Catalog:            Catalog{Tools: []string{"web_search"}},
		ChannelsConfigured: map[string]bool{"telegram": true},
		SecretsSet: map[string]bool{
			"llm.providers.openai.api_key": true,
			"channels.telegram.bot_token":  true,
		},
	}
	r := Preflight(baseDraft(), in)
	if !r.OK {
		t.Fatalf("expected OK with every credential stored, got %+v", r.Blockers)
	}
	if len(issuesOfKind(r.Passes, "secret")) != 1 {
		t.Fatalf("expected a passing credential item, got passes %+v", r.Passes)
	}
}

// Backward compatibility: a caller that never supplied credential state must
// not start seeing blockers it cannot act on.
func TestPreflight_NilSecretsSetDoesNotBlock(t *testing.T) {
	in := PreflightInput{
		Catalog:            Catalog{Tools: []string{"web_search"}},
		ChannelsConfigured: map[string]bool{"telegram": true},
		// SecretsSet deliberately nil.
	}
	r := Preflight(baseDraft(), in)
	if !r.OK {
		t.Fatalf("nil SecretsSet must not block, got %+v", r.Blockers)
	}
	if len(issuesOfKind(r.Blockers, "secret")) != 0 || len(issuesOfKind(r.Passes, "secret")) != 0 {
		t.Fatal("nil SecretsSet must produce no credential verdict at all")
	}
}

// An explicitly declared requirement is authoritative even when the caller's
// inventory has never heard of the name (the certify.go contract).
func TestPreflight_ExplicitRequiredSecretBlocks(t *testing.T) {
	in := PreflightInput{
		Catalog:         Catalog{Tools: []string{"web_search"}},
		SecretsSet:      map[string]bool{"something.else": true},
		RequiredSecrets: []SecretRequirement{{Name: "tools.notion.api_key", Kind: "tool", Owner: "notion"}},
	}
	r := Preflight(baseDraft(), in)
	secrets := issuesOfKind(r.Blockers, "secret")
	if len(secrets) != 1 || !strings.Contains(secrets[0].Fix, "tools.notion.api_key") {
		t.Fatalf("expected a named blocker for the declared credential, got %+v", r.Blockers)
	}
}

// ST-08: nothing verified the AGENT's runtime provider/model.
func TestPreflight_UnavailableProviderBlocks(t *testing.T) {
	in := PreflightInput{
		Catalog:            Catalog{Tools: []string{"web_search"}},
		ChannelsConfigured: map[string]bool{"telegram": true},
		ProvidersAvailable: map[string]bool{"ollama": true}, // openai NOT configured
	}
	r := Preflight(baseDraft(), in)
	blocks := issuesOfKind(r.Blockers, "provider")
	if len(blocks) != 1 {
		t.Fatalf("expected a provider blocker, got %+v", r.Blockers)
	}
	if !strings.Contains(blocks[0].Message, "openai") || blocks[0].Action != "open_providers" {
		t.Errorf("provider blocker must name the provider and carry open_providers: %+v", blocks[0])
	}
}

func TestPreflight_NoProviderConfiguredAtAllBlocks(t *testing.T) {
	d := baseDraft()
	d.LLM.Provider = ""
	in := PreflightInput{
		Catalog:            Catalog{Tools: []string{"web_search"}},
		ChannelsConfigured: map[string]bool{"telegram": true},
		ProvidersAvailable: map[string]bool{"openai": false},
	}
	r := Preflight(d, in)
	if len(issuesOfKind(r.Blockers, "provider")) != 1 {
		t.Fatalf("expected a blocker when no provider is available, got %+v", r.Blockers)
	}
}

func TestPreflight_ConfiguredProviderAndModelPass(t *testing.T) {
	in := PreflightInput{
		Catalog:            Catalog{Tools: []string{"web_search"}},
		ChannelsConfigured: map[string]bool{"telegram": true},
		ProvidersAvailable: map[string]bool{"openai": true},
		ModelsAvailable:    map[string]bool{"gpt-4o": true},
	}
	r := Preflight(baseDraft(), in)
	if !r.OK {
		t.Fatalf("expected OK with provider+model available, got %+v", r.Blockers)
	}
	if len(issuesOfKind(r.Passes, "provider")) != 1 || len(issuesOfKind(r.Passes, "model")) != 1 {
		t.Fatalf("expected provider and model pass items, got %+v", r.Passes)
	}
}

func TestPreflight_UnavailableModelBlocks(t *testing.T) {
	in := PreflightInput{
		Catalog:            Catalog{Tools: []string{"web_search"}},
		ChannelsConfigured: map[string]bool{"telegram": true},
		ProvidersAvailable: map[string]bool{"openai": true},
		ModelsAvailable:    map[string]bool{"gpt-4o-mini": true},
	}
	r := Preflight(baseDraft(), in)
	blocks := issuesOfKind(r.Blockers, "model")
	if len(blocks) != 1 || blocks[0].Action != "choose_model" {
		t.Fatalf("expected a model blocker with choose_model, got %+v", r.Blockers)
	}
	if blocks[0].ActionParams["model"] != "gpt-4o" {
		t.Errorf("model blocker must carry the model in ActionParams: %+v", blocks[0].ActionParams)
	}
}

func TestPreflight_MissingModelBlocksWhenInventorySupplied(t *testing.T) {
	d := baseDraft()
	d.LLM.Model = ""
	in := PreflightInput{
		Catalog:            Catalog{Tools: []string{"web_search"}},
		ChannelsConfigured: map[string]bool{"telegram": true},
		ProvidersAvailable: map[string]bool{"openai": true},
		ModelsAvailable:    map[string]bool{"gpt-4o": true},
	}
	r := Preflight(d, in)
	if len(issuesOfKind(r.Blockers, "model")) != 1 {
		t.Fatalf("expected a blocker for an unspecified model, got %+v", r.Blockers)
	}
}

func TestPreflight_NilProviderAndModelMapsStaySilent(t *testing.T) {
	r := Preflight(baseDraft(), PreflightInput{Catalog: Catalog{Tools: []string{"web_search"}}})
	for _, i := range append(append([]PreflightIssue{}, r.Blockers...), r.Warnings...) {
		if i.Kind == "provider" || i.Kind == "model" {
			t.Fatalf("unsupplied provider/model state must produce no verdict, got %+v", i)
		}
	}
}

// The Ready tier must reach the caller — "checked and fine" and "never checked"
// must not look the same.
func TestPreflight_ReturnsPassItems(t *testing.T) {
	d := baseDraft()
	d.Trigger = Trigger{Type: "schedule", Config: map[string]any{"cron": "0 7 * * *"}}
	in := PreflightInput{
		Catalog:            Catalog{Tools: []string{"web_search"}},
		ChannelsConfigured: map[string]bool{"telegram": true},
		ProvidersAvailable: map[string]bool{"openai": true},
		ModelsAvailable:    map[string]bool{"gpt-4o": true},
		SecretsSet:         map[string]bool{"llm.providers.openai.api_key": true},
	}
	r := Preflight(d, in)
	if len(r.Passes) == 0 {
		t.Fatal("expected pass items in the result")
	}
	want := map[string]bool{"schedule": true, "channel": true, "secret": true, "provider": true, "model": true}
	got := map[string]bool{}
	for _, p := range r.Passes {
		if p.Severity != "pass" {
			t.Errorf("pass item has severity %q", p.Severity)
		}
		got[p.Kind] = true
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing pass item of kind %q (got %v)", k, got)
		}
	}
}

// Every blocker must be machine-actionable, whichever check produced it.
func TestPreflight_EveryBlockerCarriesAnAction(t *testing.T) {
	d := Draft{
		Name:     "broken",
		Trigger:  Trigger{Type: "schedule"}, // invalid cron -> schedule blocker
		Channels: []string{"telegram"},      // unconfigured -> channel blocker
		Flow: Flow{Nodes: []sdkr.FlowNode{
			{ID: "mcp", Kind: "tool", Tool: "mcp__notebooklm__audio", Input: `{"notebook_id":""}`},
			{ID: "empty", Kind: "tool", Tool: ""},                                         // field blocker
			{ID: "py", Kind: "python", Code: "x = 1"},                                     // python entrypoint blocker
			{ID: "tpl", Kind: "tool", Tool: "web_search", Input: `{"query":"{{ ..x }}"}`}, // template blocker
		}},
		LLM: agent.LLMConfig{Provider: "openai"},
	}
	in := PreflightInput{
		Catalog: Catalog{Tools: []string{"web_search"}, MCP: []CatalogMCPServer{{
			Server: "notebooklm",
			Tools:  []CatalogMCPTool{{Name: "mcp__notebooklm__audio", Params: "notebook_id*:string"}},
		}}},
		ConnectedMCP:       map[string]bool{},
		ChannelsConfigured: map[string]bool{},
		SecretsSet:         map[string]bool{"llm.providers.openai.api_key": false},
		ProvidersAvailable: map[string]bool{},
		ModelsAvailable:    map[string]bool{},
	}
	r := Preflight(d, in)
	if len(r.Blockers) == 0 {
		t.Fatal("expected blockers")
	}
	valid := map[string]bool{
		"open_preflight": true, "open_providers": true, "open_mcp": true,
		"open_delivery": true, "add_assertions": true, "run_live": true,
		"choose_model": true, "open_studio": true,
	}
	for _, b := range r.Blockers {
		if b.Action == "" {
			t.Errorf("blocker without an Action: %+v", b)
			continue
		}
		if !valid[b.Action] {
			t.Errorf("blocker carries action %q outside certify.go's vocabulary: %+v", b.Action, b)
		}
	}
}

func TestDeriveSecretRequirements_UsesCallerInventoryOnly(t *testing.T) {
	d := baseDraft()
	known := map[string]bool{
		"llm.providers.openai.api_key":     false,
		"channels.telegram.bot_token":      false,
		"channels.slack.bot_token":         true, // not used by this draft
		"llm.providers.anthropic.api_key":  true, // not this draft's provider
		"channels.telegram.signing_secret": false,
	}
	reqs := DeriveSecretRequirements(d, known)
	var names []string
	for _, r := range reqs {
		names = append(names, r.Name)
	}
	want := []string{"channels.telegram.bot_token", "channels.telegram.signing_secret", "llm.providers.openai.api_key"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("derived %v, want %v", names, want)
	}
	if len(DeriveSecretRequirements(d, nil)) != 0 {
		t.Fatal("no inventory means no derived requirement (never guess a credential name)")
	}
}
