package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/caps"
	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/internal/llm"
)

// ---------------------------------------------------------------------------
// Manifest v2 parsing
// ---------------------------------------------------------------------------

func TestLoader_V1Manifest_StringChannelsStillParse(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "legacy", `
id: legacy
channels: [telegram, slack]
providers: [ollama]
`)
	l := New([]string{root}, zap.NewNop())
	if l.Count() != 1 {
		t.Fatalf("Count = %d, want 1 (v1 manifests must keep loading)", l.Count())
	}
	m := l.All()[0].Manifest
	if len(m.Channels) != 2 || m.Channels[0].ID != "telegram" {
		t.Fatalf("Channels = %+v", m.Channels)
	}
	if len(m.Providers) != 1 || m.Providers[0].ID != "ollama" {
		t.Fatalf("Providers = %+v", m.Providers)
	}
}

func v2Manifest() string {
	return `
id: matrix-suite
name: Matrix Suite
version: 1.0.0
manifest_schema: 2
channels:
  - id: matrix
    agent_id: assistant
    sidecar:
      command: node
      args: ["sidecar/matrix.mjs"]
providers:
  - id: local-vllm
    openai_compatible:
      base_url: http://localhost:8000/v1
      api_key_env: VLLM_KEY
      model: llama-3.3-70b
skills:
  - skills/moderation
gui:
  nav: { label: "Matrix", icon: "💬" }
  static: ui
permissions:
  - cap: channel.send
    channels: [matrix]
credentials:
  - key: MATRIX_TOKEN
    from: matrix-suite/token
`
}

// writePluginWithDirs creates the plugin plus any subdirectories it declares.
func writePluginWithDirs(t *testing.T, root, id, manifest string, dirs ...string) string {
	t.Helper()
	writePlugin(t, root, id, manifest)
	pdir := filepath.Join(root, id)
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(pdir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return pdir
}

func TestLoader_V2Manifest_FullParse(t *testing.T) {
	root := t.TempDir()
	writePluginWithDirs(t, root, "matrix-suite", v2Manifest(), "skills/moderation", "ui")

	l := New([]string{root}, zap.NewNop())
	if l.Count() != 1 {
		t.Fatalf("Count = %d, want 1", l.Count())
	}
	m := l.All()[0].Manifest
	if m.ManifestSchema != 2 {
		t.Fatalf("ManifestSchema = %d", m.ManifestSchema)
	}
	if len(m.Channels) != 1 {
		t.Fatalf("Channels = %+v", m.Channels)
	}
	ch := m.Channels[0]
	if ch.ID != "matrix" || ch.AgentID != "assistant" || ch.Sidecar == nil ||
		ch.Sidecar.Command != "node" || len(ch.Sidecar.Args) != 1 {
		t.Fatalf("channel = %+v sidecar=%+v", ch, ch.Sidecar)
	}
	if len(m.Providers) != 1 || m.Providers[0].OpenAICompatible == nil ||
		m.Providers[0].OpenAICompatible.BaseURL != "http://localhost:8000/v1" {
		t.Fatalf("Providers = %+v", m.Providers)
	}
	if len(m.Skills) != 1 || m.Skills[0] != "skills/moderation" {
		t.Fatalf("Skills = %+v", m.Skills)
	}
	if m.GUI == nil || m.GUI.Nav.Label != "Matrix" || m.GUI.Static != "ui" {
		t.Fatalf("GUI = %+v", m.GUI)
	}
}

// ---------------------------------------------------------------------------
// Validation: clear refusals
// ---------------------------------------------------------------------------

func TestLoader_V2_ChannelMissingCommand_Refused(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "p", `
id: p
manifest_schema: 2
channels:
  - id: matrix
    agent_id: assistant
    sidecar: { args: ["x"] }
`)
	if l := New([]string{root}, zap.NewNop()); l.Count() != 0 {
		t.Fatal("channel without sidecar command must refuse the plugin")
	}
}

func TestLoader_V2_ChannelMissingAgent_Refused(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "p", `
id: p
manifest_schema: 2
channels:
  - id: matrix
    sidecar: { command: node }
`)
	if l := New([]string{root}, zap.NewNop()); l.Count() != 0 {
		t.Fatal("sidecar channel without agent_id must refuse the plugin")
	}
}

func TestLoader_V2_ProviderMissingBaseURL_Refused(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "p", `
id: p
manifest_schema: 2
providers:
  - id: vllm
    openai_compatible: { api_key_env: K }
`)
	if l := New([]string{root}, zap.NewNop()); l.Count() != 0 {
		t.Fatal("provider without base_url must refuse the plugin")
	}
}

func TestLoader_V2_GUIStaticMissing_Refused(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "p", `
id: p
manifest_schema: 2
gui:
  nav: { label: X }
  static: ui
`) // ui/ dir deliberately not created
	if l := New([]string{root}, zap.NewNop()); l.Count() != 0 {
		t.Fatal("gui with missing static dir must refuse the plugin")
	}
}

func TestLoader_FutureSchema_WarnAndSkip(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "p", `
id: p
manifest_schema: 3
`)
	if l := New([]string{root}, zap.NewNop()); l.Count() != 0 {
		t.Fatal("future manifest schema must be skipped, not guessed at")
	}
}

func TestLoader_V1WithSidecarDecl_ContributionSkippedPluginLoads(t *testing.T) {
	// A v1 manifest (no manifest_schema) that smuggles v2-only declarations:
	// the contributions are skipped with a warning, but the plugin (and its
	// Python tools) keeps working — no breakage.
	root := t.TempDir()
	writePlugin(t, root, "p", `
id: p
channels:
  - id: matrix
    agent_id: assistant
    sidecar: { command: node }
`)
	l := New([]string{root}, zap.NewNop())
	if l.Count() != 1 {
		t.Fatalf("Count = %d, want 1 (v1 plugin must still load)", l.Count())
	}
	if got := l.All()[0].SidecarChannels(); len(got) != 0 {
		t.Fatalf("v1 manifest produced sidecar channels: %+v", got)
	}
}

func TestLoadedPlugin_SidecarChannels_V2(t *testing.T) {
	root := t.TempDir()
	writePluginWithDirs(t, root, "matrix-suite", v2Manifest(), "skills/moderation", "ui")
	l := New([]string{root}, zap.NewNop())
	if l.Count() != 1 {
		t.Fatal("plugin not loaded")
	}
	chs := l.All()[0].SidecarChannels()
	if len(chs) != 1 || chs[0].ID != "matrix" {
		t.Fatalf("SidecarChannels = %+v", chs)
	}
}

// ---------------------------------------------------------------------------
// Wire: registry integration
// ---------------------------------------------------------------------------

type fakeChanReg struct{ adapters []channels.Adapter }

func (f *fakeChanReg) Register(a channels.Adapter) { f.adapters = append(f.adapters, a) }

type fakeLLMReg struct{ providers []llm.Provider }

func (f *fakeLLMReg) Register(p llm.Provider) { f.providers = append(f.providers, p) }

func TestWire_SidecarChannelRegistered(t *testing.T) {
	root := t.TempDir()
	writePluginWithDirs(t, root, "matrix-suite", v2Manifest(), "skills/moderation", "ui")
	l := New([]string{root}, zap.NewNop())

	v := testVault(t)
	setSecret(t, v, "matrix-suite", "token", "tok")

	cr := &fakeChanReg{}
	pr := &fakeLLMReg{}
	enf := caps.NewEnforcer(nil, zap.NewNop())
	errs := Wire(context.Background(), l, WireDeps{
		Channels: cr, LLM: pr, Vault: v, Enforcer: enf, Log: zap.NewNop(),
	})
	if len(errs) != 0 {
		t.Fatalf("Wire errors: %v", errs)
	}
	if len(cr.adapters) != 1 || cr.adapters[0].ID() != "matrix" {
		t.Fatalf("adapters = %+v", cr.adapters)
	}
	if len(pr.providers) != 1 || pr.providers[0].ID() != "local-vllm" {
		t.Fatalf("providers = %+v", pr.providers)
	}
	// Capability set registered with the enforcer under the plugin principal.
	d := enf.Check(caps.PluginPrincipal("matrix-suite"), caps.CapChannelSend, "matrix")
	if !d.Allowed {
		t.Fatalf("enforcer did not receive the plugin's capability set: %s", d.Reason)
	}
}

func TestWire_MissingDeclaredSecret_ChannelSkippedWithError(t *testing.T) {
	root := t.TempDir()
	writePluginWithDirs(t, root, "matrix-suite", v2Manifest(), "skills/moderation", "ui")
	l := New([]string{root}, zap.NewNop())

	v := testVault(t) // declared secret NOT set

	cr := &fakeChanReg{}
	errs := Wire(context.Background(), l, WireDeps{
		Channels: cr, Vault: v, Log: zap.NewNop(),
	})
	// The channel is still registered (supervisor retries env resolution
	// through its crash loop once secrets arrive) OR an error is surfaced;
	// either way Wire must not panic and must report the issue.
	if len(errs) == 0 && len(cr.adapters) == 0 {
		t.Fatal("missing secret silently dropped the channel with no error")
	}
}

func TestWire_NoVault_CredentialChannelErrors(t *testing.T) {
	root := t.TempDir()
	writePluginWithDirs(t, root, "matrix-suite", v2Manifest(), "skills/moderation", "ui")
	l := New([]string{root}, zap.NewNop())

	cr := &fakeChanReg{}
	errs := Wire(context.Background(), l, WireDeps{Channels: cr, Log: zap.NewNop()})
	if len(errs) == 0 {
		t.Fatal("plugin declares credentials but no vault is available — Wire must report it")
	}
	for _, e := range errs {
		if strings.Contains(e.Error(), "tok") {
			t.Fatalf("error leaked a secret value: %v", e)
		}
	}
}

func TestWire_V1Plugin_NothingRegistered(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "legacy", `
id: legacy
channels: [telegram]
`)
	l := New([]string{root}, zap.NewNop())
	cr := &fakeChanReg{}
	pr := &fakeLLMReg{}
	if errs := Wire(context.Background(), l, WireDeps{Channels: cr, LLM: pr, Log: zap.NewNop()}); len(errs) != 0 {
		t.Fatalf("Wire errors on v1 plugin: %v", errs)
	}
	if len(cr.adapters) != 0 || len(pr.providers) != 0 {
		t.Fatal("v1 plugin must not contribute v2 registrations")
	}
}

func TestWire_ProviderAPIKeyFromEnv(t *testing.T) {
	t.Setenv("VLLM_KEY", "k-123")
	root := t.TempDir()
	writePluginWithDirs(t, root, "matrix-suite", v2Manifest(), "skills/moderation", "ui")
	l := New([]string{root}, zap.NewNop())
	v := testVault(t)
	setSecret(t, v, "matrix-suite", "token", "tok")
	pr := &fakeLLMReg{}
	if errs := Wire(context.Background(), l, WireDeps{LLM: pr, Vault: v, Log: zap.NewNop()}); len(errs) != 0 {
		t.Fatalf("Wire errors: %v", errs)
	}
	if len(pr.providers) != 1 {
		t.Fatalf("providers = %+v", pr.providers)
	}
}
