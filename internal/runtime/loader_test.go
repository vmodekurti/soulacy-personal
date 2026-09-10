// loader_test.go — round-trip and edge-case tests for the agent loader.
// Verifies Upsert/Delete behaviour and the legacy-flat-file migration so the
// agent registry doesn't regress on the fixed-issues list (phantom agents,
// missing IDs, double-loading).
package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/agent"
)

func timeFromSec(s int64) time.Duration { return time.Duration(s) * time.Second }

func TestLoader_UpsertCreatesFolderLayout(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader([]string{dir})

	def := &agent.Definition{
		ID:           "test-agent",
		Name:         "Test Agent",
		Description:  "for tests",
		Trigger:      agent.TriggerChannel,
		SystemPrompt: "you are a tester",
		Enabled:      true,
	}
	if err := l.Upsert(dir, def); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	expected := filepath.Join(dir, "test-agent", "SOUL.yaml")
	if _, err := os.Stat(expected); err != nil {
		t.Errorf("expected agent file at %s, got error %v", expected, err)
	}
	if got := l.Get("test-agent"); got == nil {
		t.Error("Get after Upsert returned nil")
	} else if got.Name != "Test Agent" {
		t.Errorf("agent name: got %q, want %q", got.Name, "Test Agent")
	}
}

func TestLoader_DeleteRemovesFileAndEntry(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader([]string{dir})

	def := &agent.Definition{ID: "deletable", Trigger: agent.TriggerChannel, Enabled: true}
	if err := l.Upsert(dir, def); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := l.Delete("deletable"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := l.Get("deletable"); got != nil {
		t.Error("Delete should remove from registry; Get returned non-nil")
	}
	// Folder should also be gone (empty folder cleanup happens in Delete).
	if _, err := os.Stat(filepath.Join(dir, "deletable")); !os.IsNotExist(err) {
		t.Errorf("Delete should remove the agent folder, stat err: %v", err)
	}
}

func TestLoader_UpsertCapturesVersionAndRestore(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader([]string{dir})

	def := &agent.Definition{
		ID:           "versioned",
		Name:         "First",
		Trigger:      agent.TriggerChannel,
		SystemPrompt: "one",
		Enabled:      true,
	}
	if err := l.Upsert(dir, def); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	def.Name = "Second"
	def.SystemPrompt = "two"
	if err := l.Upsert(dir, def); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	versions, err := l.AgentVersions("versioned")
	if err != nil {
		t.Fatalf("AgentVersions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("versions len = %d, want 1", len(versions))
	}
	data, _, err := l.ReadAgentVersion("versioned", versions[0].ID)
	if err != nil {
		t.Fatalf("ReadAgentVersion: %v", err)
	}
	if got := string(data); !strings.Contains(got, "name: First") || strings.Contains(got, "name: Second") {
		t.Fatalf("snapshot did not contain previous YAML:\n%s", got)
	}

	restored, _, err := l.RestoreAgentVersion(dir, "versioned", versions[0].ID)
	if err != nil {
		t.Fatalf("RestoreAgentVersion: %v", err)
	}
	if restored.Name != "First" || restored.SystemPrompt != "one" {
		t.Fatalf("restored = name %q prompt %q, want First/one", restored.Name, restored.SystemPrompt)
	}
	if got := l.Get("versioned"); got == nil || got.Name != "First" {
		t.Fatalf("loader registry not restored: %#v", got)
	}
}

func TestLoader_LoadAllSkipsAgentHistory(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader([]string{dir})

	if err := l.Upsert(dir, &agent.Definition{ID: "real", Name: "Real", Trigger: agent.TriggerChannel}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	hdir := filepath.Join(dir, ".agent-history", "real")
	if err := os.MkdirAll(hdir, 0755); err != nil {
		t.Fatalf("mkdir history: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hdir, "20260101T000000.000000000Z.yaml"), []byte("id: stale\nname: Stale\n"), 0644); err != nil {
		t.Fatalf("write history: %v", err)
	}
	if errs := l.LoadAll(); len(errs) > 0 {
		t.Fatalf("LoadAll errs: %v", errs)
	}
	if got := l.Get("stale"); got != nil {
		t.Fatalf("history snapshot should not load as agent: %#v", got)
	}
}

func TestLoader_DeleteIsIdempotent(t *testing.T) {
	l := NewLoader([]string{t.TempDir()})
	if err := l.Delete("ghost"); err != nil {
		t.Errorf("Delete on absent agent should be nil, got %v", err)
	}
}

func TestLoader_ProtectedSystemCannotBeModifiedOrDeleted(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader([]string{dir})

	// Upsert should succeed and allow modifying LLM settings / Prompt,
	// but safety features (Channels = [http], Enabled = true, SystemTools = true) are enforced.
	if err := l.Upsert(dir, &agent.Definition{
		ID:           SystemAgentID,
		Name:         "Modified System",
		Channels:     []string{"telegram"}, // should be overridden to [http]
		Enabled:      false,                // should be overridden to true
		SystemTools:  false,                // should be overridden to true
		ConfirmTools: []string{"shell_exec"},
	}); err != nil {
		t.Fatalf("Upsert system agent should succeed, got error: %v", err)
	}

	if err := l.Delete(SystemAgentID); err == nil {
		t.Fatal("Delete should reject the protected system agent")
	}

	sys := l.Get(SystemAgentID)
	if sys == nil {
		t.Fatal("system agent should still be present")
	}
	if sys.Name != "Modified System" {
		t.Fatalf("system Name = %q, want modified name", sys.Name)
	}
	if !sys.Enabled {
		t.Fatal("system agent should remain enabled")
	}
	if !sys.SystemTools {
		t.Fatal("system agent should remain system tools enabled")
	}
	if len(sys.Channels) != 1 || sys.Channels[0] != "http" {
		t.Fatalf("system channels = %v, want [http] enforced", sys.Channels)
	}
	if !containsExactString(sys.ConfirmTools, "package_install") {
		t.Fatalf("system confirm_tools = %v, want package_install enforced", sys.ConfirmTools)
	}
}

func TestLoader_SeedsProtectedNonPrivilegedGenie(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader([]string{dir})
	g := l.Get(GenieAgentID)
	if g == nil || !g.Enabled || g.SourcePath != builtinSourcePath {
		t.Fatalf("Genie not seeded as an enabled builtin: %#v", g)
	}
	if g.SystemTools || g.AllowShell || g.HasCapability("system") {
		t.Fatal("Genie received system capability")
	}
	if g.LLM.ReasoningEffort != "high" || len(g.Agents) != 1 || g.Agents[0] != "*" {
		t.Fatalf("Genie orchestration defaults are incomplete: %#v", g)
	}
	if err := l.Delete(GenieAgentID); err == nil {
		t.Fatal("Delete should reject protected Genie")
	}

	// Custom model/prompt may be saved, but the security boundary is restored.
	if err := l.Upsert(dir, &agent.Definition{ID: GenieAgentID, Name: "My Genie", Enabled: false, SystemTools: true, AllowShell: true, Capabilities: []string{"system"}}); err != nil {
		t.Fatalf("Upsert Genie: %v", err)
	}
	g = l.Get(GenieAgentID)
	if !g.Enabled || g.SystemTools || g.AllowShell || g.HasCapability("system") {
		t.Fatalf("Genie hardening was not enforced: %#v", g)
	}
}

func TestLoader_LoadAllAppliesOnDiskSystemOverride(t *testing.T) {
	dir := t.TempDir()
	sysDir := filepath.Join(dir, SystemAgentID)
	if err := os.MkdirAll(sysDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "SOUL.yaml"), []byte(`id: system
name: Custom System
channels: [telegram]
enabled: false
system_prompt: customized prompt
`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	l := NewLoader([]string{dir})
	if errs := l.LoadAll(); len(errs) > 0 {
		t.Fatalf("LoadAll errors: %v", errs)
	}

	sys := l.Get(SystemAgentID)
	if sys == nil {
		t.Fatal("system agent should still be present")
	}
	if sys.Name != "Custom System" {
		t.Fatalf("system name = %q, want customized name", sys.Name)
	}
	if !sys.Enabled {
		t.Fatal("system agent should remain enabled")
	}
	if sys.SystemPrompt != "customized prompt" {
		t.Fatalf("system prompt = %q, want customized prompt", sys.SystemPrompt)
	}
	if sys.SourcePath == builtinSourcePath {
		t.Fatal("system SourcePath should point to on-disk file, not builtin sentinel")
	}
	if len(sys.Channels) != 1 || sys.Channels[0] != "http" {
		t.Fatalf("system channels = %v, want [http]", sys.Channels)
	}
}

func TestLoader_LoadAllParsesValidSOUL(t *testing.T) {
	dir := t.TempDir()
	soul := []byte(`id: parse-test
name: Parse Test
trigger: channel
system_prompt: test
llm:
  provider: ollama
  model: llama3
enabled: true
agents:
  - peer-one
  - peer-two
knowledge:
  - kb-x
mcp_servers:
  - rocketmoney
mcp_tools:
  - mcp__filesystem__read_file
`)
	agentDir := filepath.Join(dir, "parse-test")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "SOUL.yaml"), soul, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	l := NewLoader([]string{dir})
	if errs := l.LoadAll(); len(errs) > 0 {
		t.Fatalf("LoadAll errors: %v", errs)
	}
	def := l.Get("parse-test")
	if def == nil {
		t.Fatal("Get returned nil for loaded agent")
	}
	if len(def.Agents) != 2 || def.Agents[0] != "peer-one" {
		t.Errorf("agents list: got %v", def.Agents)
	}
	if len(def.Knowledge) != 1 || def.Knowledge[0] != "kb-x" {
		t.Errorf("knowledge list: got %v", def.Knowledge)
	}
	if def.MCPServers == nil || len(*def.MCPServers) != 1 || (*def.MCPServers)[0] != "rocketmoney" {
		t.Errorf("mcp_servers list: got %v", def.MCPServers)
	}
	if def.MCPTools == nil || len(*def.MCPTools) != 1 || (*def.MCPTools)[0] != "mcp__filesystem__read_file" {
		t.Errorf("mcp_tools list: got %v", def.MCPTools)
	}
}

func TestLoader_RejectsMissingID(t *testing.T) {
	dir := t.TempDir()
	soul := []byte(`name: NoID Agent
trigger: channel
`)
	path := filepath.Join(dir, "noid.yaml")
	if err := os.WriteFile(path, soul, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	l := NewLoader([]string{dir})
	errs := l.LoadAll()
	if len(errs) == 0 {
		t.Error("expected LoadAll to surface missing-id error, got none")
	}
}

func TestLoader_UpsertRequiresID(t *testing.T) {
	l := NewLoader([]string{t.TempDir()})
	if err := l.Upsert(t.TempDir(), &agent.Definition{Name: "no-id"}); err == nil {
		t.Error("Upsert without ID should fail")
	}
}

func TestDefinition_ResolvedRunTimeout(t *testing.T) {
	tests := []struct {
		name     string
		def      *agent.Definition
		fallback int64
		want     int64
	}{
		{"nil def returns fallback", nil, 60, 60},
		{"empty run_timeout returns fallback", &agent.Definition{}, 60, 60},
		{"valid duration parsed", &agent.Definition{RunTimeout: "30s"}, 60, 30},
		{"invalid duration returns fallback", &agent.Definition{RunTimeout: "garbage"}, 60, 60},
		{"zero duration returns fallback", &agent.Definition{RunTimeout: "0s"}, 60, 60},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.def.ResolvedRunTimeout(timeFromSec(tc.fallback))
			if got != timeFromSec(tc.want) {
				t.Errorf("got %v, want %v", got, timeFromSec(tc.want))
			}
		})
	}
}

func TestDefinitionCloneCopiesMCPAllowlists(t *testing.T) {
	servers := []string{"rocketmoney"}
	tools := []string{"mcp__rocketmoney__get_transactions"}
	def := &agent.Definition{
		ID:         "finance",
		MCPServers: &servers,
		MCPTools:   &tools,
	}

	cp := def.Clone()
	(*cp.MCPServers)[0] = "filesystem"
	(*cp.MCPTools)[0] = "mcp__filesystem__read_file"

	if (*def.MCPServers)[0] != "rocketmoney" {
		t.Fatalf("Clone aliased MCPServers: got %q", (*def.MCPServers)[0])
	}
	if (*def.MCPTools)[0] != "mcp__rocketmoney__get_transactions" {
		t.Fatalf("Clone aliased MCPTools: got %q", (*def.MCPTools)[0])
	}
}

// ---------------------------------------------------------------------------
// Phase 7: YAML round-trip tests for advanced schema fields.
//
// These tests exercise the full Upsert→disk→LoadAll→Get pipeline so that
// YAML serialisation regressions (omitempty on nil *[]string, nested structs
// being dropped, etc.) are caught automatically rather than at runtime.
// ---------------------------------------------------------------------------

// upsertThenReload writes def to disk via Upsert, re-scans via LoadAll, and
// returns the freshly-parsed definition. Failures are fatal so callers don't
// have to check errors on every assertion.
func upsertThenReload(t *testing.T, def *agent.Definition) *agent.Definition {
	t.Helper()
	dir := t.TempDir()
	l := NewLoader([]string{dir})
	if err := l.Upsert(dir, def); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if errs := l.LoadAll(); len(errs) > 0 {
		t.Fatalf("LoadAll: %v", errs)
	}
	got := l.Get(def.ID)
	if got == nil {
		t.Fatalf("Get(%q) returned nil after reload", def.ID)
	}
	return got
}

// TestLoaderRoundTripBuiltinsNil verifies that a nil Builtins pointer (legacy
// default — all gated builtins offered) survives the YAML round-trip as nil.
func TestLoaderRoundTripBuiltinsNil(t *testing.T) {
	def := &agent.Definition{
		ID: "rt-builtins-nil", Enabled: true,
		Builtins: nil, // absent from YAML → must stay nil after reload
	}
	got := upsertThenReload(t, def)
	if got.Builtins != nil {
		t.Errorf("Builtins: got %v, want nil", got.Builtins)
	}
}

// TestLoaderRoundTripBuiltinsEmpty verifies that an explicit empty Builtins
// list (opt-out of all builtins) round-trips as a non-nil pointer to an
// empty slice — distinct from nil.
func TestLoaderRoundTripBuiltinsEmpty(t *testing.T) {
	empty := []string{}
	def := &agent.Definition{
		ID: "rt-builtins-empty", Enabled: true,
		Builtins: &empty,
	}
	got := upsertThenReload(t, def)
	if got.Builtins == nil {
		t.Fatal("Builtins: got nil, want non-nil empty list")
	}
	if len(*got.Builtins) != 0 {
		t.Errorf("Builtins: got %v, want []", *got.Builtins)
	}
}

// TestLoaderRoundTripBuiltinsExplicit verifies that a specific builtins list
// survives the round-trip with every entry preserved.
func TestLoaderRoundTripBuiltinsExplicit(t *testing.T) {
	list := []string{"web_search", "kb_search"}
	def := &agent.Definition{
		ID: "rt-builtins-list", Enabled: true,
		Builtins: &list,
	}
	got := upsertThenReload(t, def)
	if got.Builtins == nil || len(*got.Builtins) != 2 {
		t.Fatalf("Builtins: got %v, want [web_search kb_search]", got.Builtins)
	}
	if (*got.Builtins)[0] != "web_search" || (*got.Builtins)[1] != "kb_search" {
		t.Errorf("Builtins entries: got %v", *got.Builtins)
	}
}

// TestLoaderRoundTripBuiltinsWildcard verifies the ["*"] wildcard value.
func TestLoaderRoundTripBuiltinsWildcard(t *testing.T) {
	wc := []string{"*"}
	def := &agent.Definition{
		ID: "rt-builtins-wc", Enabled: true,
		Builtins: &wc,
	}
	got := upsertThenReload(t, def)
	if got.Builtins == nil || len(*got.Builtins) != 1 || (*got.Builtins)[0] != "*" {
		t.Errorf("Builtins wildcard: got %v, want [*]", got.Builtins)
	}
}

// TestLoaderRoundTripConfirmTools verifies that confirm_tools list is preserved.
func TestLoaderRoundTripConfirmTools(t *testing.T) {
	def := &agent.Definition{
		ID: "rt-confirm", Enabled: true,
		ConfirmTools: []string{"shell_exec", "write_file", "http_request"},
	}
	got := upsertThenReload(t, def)
	if len(got.ConfirmTools) != 3 {
		t.Fatalf("ConfirmTools count = %d, want 3", len(got.ConfirmTools))
	}
	if got.ConfirmTools[0] != "shell_exec" {
		t.Errorf("ConfirmTools[0] = %q, want shell_exec", got.ConfirmTools[0])
	}
}

// TestLoaderRoundTripSystemTools verifies that system_tools:true survives.
func TestLoaderRoundTripSystemTools(t *testing.T) {
	def := &agent.Definition{
		ID: "rt-systemtools", Enabled: true,
		SystemTools: true,
	}
	got := upsertThenReload(t, def)
	if !got.SystemTools {
		t.Error("SystemTools: got false, want true")
	}
}

// TestLoaderRoundTripSecurity verifies that the security block (passphrase
// and prompt) round-trips without losing any fields.
func TestLoaderRoundTripSecurity(t *testing.T) {
	def := &agent.Definition{
		ID: "rt-security", Enabled: true,
		Security: &agent.SecurityConfig{
			Passphrase:       "hunter2",
			PassphrasePrompt: "Enter the magic word:",
		},
	}
	got := upsertThenReload(t, def)
	if got.Security == nil {
		t.Fatal("Security: got nil, want non-nil")
	}
	if got.Security.Passphrase != "hunter2" {
		t.Errorf("Passphrase = %q, want hunter2", got.Security.Passphrase)
	}
	if got.Security.PassphrasePrompt != "Enter the magic word:" {
		t.Errorf("PassphrasePrompt = %q", got.Security.PassphrasePrompt)
	}
}

// TestLoaderRoundTripSecurityNil verifies that omitting the security block
// leaves Security as nil after reload (no ghost empty struct injected).
func TestLoaderRoundTripSecurityNil(t *testing.T) {
	def := &agent.Definition{
		ID: "rt-security-nil", Enabled: true,
		Security: nil,
	}
	got := upsertThenReload(t, def)
	if got.Security != nil {
		t.Errorf("Security: got %+v, want nil", got.Security)
	}
}

// TestLoaderRoundTripWorkflow verifies that a multi-step WorkflowSpec survives
// the YAML round-trip with all step fields intact.
func TestLoaderRoundTripWorkflow(t *testing.T) {
	def := &agent.Definition{
		ID: "rt-workflow", Enabled: true,
		Workflow: &agent.WorkflowSpec{
			Steps: []agent.StepSpec{
				{ID: "gather", Tool: "web_search", Input: "{{user_message}}", Output: "search_result"},
				{ID: "summarise", Prompt: "Summarise: {{search_result}}", OnError: "skip"},
			},
		},
	}
	got := upsertThenReload(t, def)
	if got.Workflow == nil {
		t.Fatal("Workflow: got nil, want non-nil")
	}
	if len(got.Workflow.Steps) != 2 {
		t.Fatalf("Workflow steps = %d, want 2", len(got.Workflow.Steps))
	}
	s0 := got.Workflow.Steps[0]
	if s0.ID != "gather" || s0.Tool != "web_search" || s0.Output != "search_result" {
		t.Errorf("step 0: %+v", s0)
	}
	s1 := got.Workflow.Steps[1]
	if s1.ID != "summarise" || s1.OnError != "skip" {
		t.Errorf("step 1: %+v", s1)
	}
}

// TestLoaderRoundTripWorkflowNil verifies that omitting workflow leaves
// Workflow as nil (no empty WorkflowSpec injected).
func TestLoaderRoundTripWorkflowNil(t *testing.T) {
	def := &agent.Definition{
		ID: "rt-workflow-nil", Enabled: true,
		Workflow: nil,
	}
	got := upsertThenReload(t, def)
	if got.Workflow != nil {
		t.Errorf("Workflow: got %+v, want nil", got.Workflow)
	}
}

// TestLoaderRoundTripScheduleOutput verifies that the schedule.output block
// (channel routing for cron agents) round-trips completely.
func TestLoaderRoundTripScheduleOutput(t *testing.T) {
	def := &agent.Definition{
		ID: "rt-sched-output", Enabled: true,
		Schedule: &agent.Schedule{
			Cron: "0 9 * * 1-5",
			Output: &agent.ScheduleOutput{
				Channel:  "telegram-newsbot",
				To:       "123456789",
				BotName:  "News Bot",
				Template: "📰 Morning briefing:\n{{reply}}",
			},
		},
	}
	got := upsertThenReload(t, def)
	if got.Schedule == nil {
		t.Fatal("Schedule: got nil")
	}
	if got.Schedule.Cron != "0 9 * * 1-5" {
		t.Errorf("Schedule.Cron = %q", got.Schedule.Cron)
	}
	if got.Schedule.Output == nil {
		t.Fatal("Schedule.Output: got nil")
	}
	o := got.Schedule.Output
	if o.Channel != "telegram-newsbot" {
		t.Errorf("Output.Channel = %q, want telegram-newsbot", o.Channel)
	}
	if o.To != "123456789" {
		t.Errorf("Output.To = %q, want 123456789", o.To)
	}
	if o.BotName != "News Bot" {
		t.Errorf("Output.BotName = %q, want 'News Bot'", o.BotName)
	}
	if o.Template != "📰 Morning briefing:\n{{reply}}" {
		t.Errorf("Output.Template = %q", o.Template)
	}
}

// TestLoaderRoundTripAllowedProviders verifies the provider guard list.
func TestLoaderRoundTripAllowedProviders(t *testing.T) {
	def := &agent.Definition{
		ID: "rt-providers", Enabled: true,
		LLM: agent.LLMConfig{
			Provider:         "ollama",
			Model:            "llama3",
			AllowedProviders: []string{"ollama"},
		},
	}
	got := upsertThenReload(t, def)
	if len(got.LLM.AllowedProviders) != 1 || got.LLM.AllowedProviders[0] != "ollama" {
		t.Errorf("AllowedProviders: got %v, want [ollama]", got.LLM.AllowedProviders)
	}
	if got.LLM.Provider != "ollama" || got.LLM.Model != "llama3" {
		t.Errorf("LLM config: provider=%q model=%q", got.LLM.Provider, got.LLM.Model)
	}
}
