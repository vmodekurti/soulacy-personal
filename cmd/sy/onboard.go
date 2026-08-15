// onboard.go — `sy onboard` first-run wizard.
//
// Distinct from `sy setup` (legacy from-scratch config writer): onboard
// layers ON TOP of the silent first-run bootstrap that `soulacy serve`
// performs via internal/config.EnsureBootstrap. The wizard's job is to
// FILL fields the bootstrap would have left at sensible defaults — pick
// a provider, paste a key, install a daemon, etc. — not to recreate the
// config from scratch.
//
// Design principles:
//
//  1. Idempotent. Re-running onboard on a configured workspace must not
//     destroy state. We read what's there, present the current value,
//     and only mutate when the operator confirms.
//
//  2. Skippable. Every step has a "skip" option. The wizard's value is
//     in walking through the choices, not in coercing answers.
//
//  3. No new file format. Everything written goes through the same path
//     EnsureBootstrap uses — config.yaml mutations via textual patch so
//     operator comments and edits are preserved.
//
//  4. Reuses helpers from setup.go (prompt, confirm, promptChoices,
//     color funcs). No new TUI dependencies. Stays a single Go binary.
//
// Why we keep `sy setup` AND `sy onboard`: setup is the "I have nothing,
// generate me a fresh config.yaml" path. onboard is the "I just ran
// install.sh and soulacy serve once — what now?" path. The distinction
// matters because onboard knows about EnsureBootstrap's output and
// works WITH it; setup assumes a blank slate.

package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/soulacy/soulacy/internal/agentprompt"
	"github.com/soulacy/soulacy/internal/config"
	// Imported for its init(): registers the built-in LLM provider factories
	// (ollama/openai/anthropic/gemini/google) into the global registry so
	// onboard can build a client and query live models.
	_ "github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/sdk/registry"
)

func buildOnboardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "onboard",
		Short: "Guided first-run wizard — pick a provider, install a daemon, see your API key",
		Long: `Walk through the choices most operators want to make right after install:

  1. Confirm the workspace path (~/.soulacy/soulspace by default).
  2. Choose Personal, Team, or Scale deployment mode.
  3. Choose loopback or expose (loopback = local-only; expose = LAN/remote).
  4. Pick an LLM provider (Ollama auto-detect / OpenAI / Anthropic / skip).
  5. Set up web search (Ollama / Tavily / Serper) for the web_search tool.
  6. Optionally adopt a starter agent so the GUI isn't empty on first open.
  7. Optionally configure the release manifest used by update checks.
  8. Optionally install a daemon so soulacy starts on login.

Re-running ` + "`sy onboard`" + ` is safe: each step shows the current value and lets
you skip without changing anything. The wizard never deletes data.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOnboardWizard()
		},
	}
}

func runOnboardWizard() error {
	printOnboardBanner()

	// Step 0: workspace resolution + bootstrap. This is the gateway's
	// own first-run logic — calling it here means the wizard works on a
	// truly virgin install (no config.yaml, no workspace dirs).
	ws, err := config.ResolveWorkspace()
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}
	if err := config.EnsureDirs(loadOrFreshConfig(ws)); err != nil {
		return fmt.Errorf("ensure workspace dirs: %w", err)
	}

	cfg, cfgPath, _ := config.Load("")
	if cfg == nil {
		cfg = loadOrFreshConfig(ws)
		cfgPath = ws.ConfigFile
	}
	bootstrap, berr := config.EnsureBootstrap(cfg, cfgPath)
	if berr != nil {
		return fmt.Errorf("first-run bootstrap: %w", berr)
	}
	if bootstrap.Action != config.BootstrapNoop {
		fmt.Printf("%s Bootstrapped %s\n", green("✓"), bootstrap.ConfigPath)
		if bootstrap.APIKey != "" {
			fmt.Printf("  %s API key: %s\n", cyan("→"), bold(bootstrap.APIKey))
			fmt.Printf("  %s Save it — the GUI will ask for it.\n\n", gray("(this banner appears once)"))
		}
	} else {
		fmt.Printf("%s Existing config detected: %s\n\n", green("✓"), cfgPath)
	}

	// ── Step 1: Workspace path ─────────────────────────────────────────────
	printStep(1, "Workspace location")
	fmt.Printf("  %s %s\n", dim("Current:"), gray(ws.Root))
	if ws.Legacy {
		fmt.Printf("  %s You're on the legacy flat layout. Migrate with: %s\n",
			yellow("⚠"), bold("sy workspace migrate"))
	}
	fmt.Println()

	// ── Step 2: Deployment mode ────────────────────────────────────────────
	printStep(2, "Deployment mode")
	currentMode := cfg.DeploymentMode()
	fmt.Printf("  %s %s\n", dim("Current:"), cyan(currentMode))
	fmt.Printf("  %s Personal keeps the zero-dependency local setup. Team and Scale require JWT, PostgreSQL, and Docker isolation.\n", gray("→"))
	if confirm("  Change deployment mode?", false) {
		choices := []string{
			"Personal (single operator, local stores)",
			"Team (multi-user, PostgreSQL, Docker isolation)",
			"Scale (multi-instance, distributed queue and shared artifacts)",
		}
		mode := []string{config.DeploymentModePersonal, config.DeploymentModeTeam, config.DeploymentModeScale}[promptChoices("Deployment mode:", choices)]
		settings := deploymentSettings{Mode: mode}
		if config.IsMultiUserMode(mode) {
			settings.PostgresDSN = prompt("  PostgreSQL DSN", cfg.Storage.PostgresDSN)
			settings.JWTSecret = strings.TrimSpace(cfg.Auth.JWTSecret)
			if len(settings.JWTSecret) < 32 {
				settings.JWTSecret = generateJWTSecret()
				fmt.Printf("  %s Generated a stable JWT signing secret.\n", green("✓"))
			}
			settings.APIKey = strings.TrimSpace(cfg.Server.APIKey)
			if settings.APIKey == "" {
				settings.APIKey = generateAPIKey()
				fmt.Printf("  %s Generated bootstrap administration key: %s\n", green("✓"), bold(settings.APIKey))
			}
		}
		if mode == config.DeploymentModeScale {
			settings.NATSURL = prompt("  NATS URL", defaultString(cfg.Queue.NATSUrl, "nats://127.0.0.1:4222"))
			settings.SharedArtifactStore = prompt("  Shared artifact store URL", cfg.Deployment.SharedArtifactStore)
		}
		if err := patchDeploymentSettings(cfgPath, settings); err != nil {
			fmt.Printf("  %s Couldn't patch deployment settings: %v\n", red("✗"), err)
		} else {
			fmt.Printf("  %s Deployment mode set to %s. Run %s to verify prerequisites.\n", green("✓"), cyan(mode), bold("sy doctor"))
		}
	}
	fmt.Println()

	// ── Step 3: Loopback vs expose ─────────────────────────────────────────
	printStep(3, "Bind address")
	currentHost := cfg.Server.Host
	if currentHost == "" {
		currentHost = "127.0.0.1"
	}
	fmt.Printf("  %s %s:%d %s\n", dim("Current:"), currentHost, cfg.Server.Port,
		gray("(127.0.0.1 = local-only; 0.0.0.0 = expose on LAN)"))
	if confirm("  Change bind address?", false) {
		choice := promptChoices("Bind to:", []string{
			"127.0.0.1 (local-only, recommended)",
			"0.0.0.0 (expose on LAN — make sure auth is on)",
			"Custom IP",
		})
		switch choice {
		case 0:
			cfg.Server.Host = "127.0.0.1"
		case 1:
			cfg.Server.Host = "0.0.0.0"
			fmt.Printf("  %s Exposing means anyone on your network can hit the gateway.\n", yellow("⚠"))
			fmt.Printf("  %s Make sure server.api_key is set. (It is: %s)\n",
				gray("Note:"), maskKey(cfg.Server.APIKey))
		case 2:
			cfg.Server.Host = prompt("  Custom IP", "")
		}
		if err := patchServerHost(cfgPath, cfg.Server.Host); err != nil {
			fmt.Printf("  %s Couldn't patch config: %v\n", red("✗"), err)
		} else {
			fmt.Printf("  %s Updated bind address to %s\n", green("✓"), cfg.Server.Host)
		}
	}
	fmt.Println()

	// ── Step 4: LLM provider ───────────────────────────────────────────────
	printStep(4, "LLM provider")
	fmt.Printf("  %s %s\n", dim("Current default:"), cyan(cfg.LLM.DefaultProvider))
	fmt.Printf("  %s Detecting Ollama on localhost:11434...\n", gray("→"))
	if ollamaUp() {
		fmt.Printf("  %s Ollama is running locally. You're good — `sy chat` should work right now.\n", green("✓"))
	} else {
		fmt.Printf("  %s Ollama not reachable. You'll want OpenAI or Anthropic.\n", yellow("⚠"))
	}
	fmt.Println()
	if confirm("  Add or change an LLM provider key?", false) {
		choice := promptChoices("Provider:", []string{
			"OpenAI",
			"Anthropic",
			"Google (Gemini)",
			"Groq",
			"OpenRouter",
			"NVIDIA NIM",
			"Custom (OpenAI-compatible)",
			"Skip",
		})
		switch choice {
		case 0:
			runProviderSetup(cfgPath, "openai", "https://api.openai.com/v1")
		case 1:
			// No /v1 suffix: the Anthropic client appends /v1/... itself.
			runProviderSetup(cfgPath, "anthropic", "https://api.anthropic.com")
		case 2:
			// No /v1beta suffix: the Gemini client appends /v1beta/... itself.
			runProviderSetup(cfgPath, "google", "https://generativelanguage.googleapis.com")
		case 3:
			runProviderSetup(cfgPath, "groq", "https://api.groq.com/openai/v1")
		case 4:
			runProviderSetup(cfgPath, "openrouter", "https://openrouter.ai/api/v1")
		case 5:
			// NVIDIA NIM / API catalog — OpenAI-compatible.
			runProviderSetup(cfgPath, "nvidia", "https://integrate.api.nvidia.com/v1")
		case 6:
			runProviderSetup(cfgPath, "custom", "")
		}
	}
	fmt.Println()

	// ── Step 5: Web search ─────────────────────────────────────────────────
	printStep(5, "Web search")
	currentSearch := cfg.Search.Provider
	if currentSearch == "" {
		currentSearch = "ollama"
	}
	fmt.Printf("  %s %s\n", dim("Current provider:"), cyan(currentSearch))
	fmt.Printf("  %s The built-in web_search tool lets agents fetch live, up-to-date information.\n", gray("→"))
	fmt.Println()
	if confirm("  Set up or change web search?", false) {
		choice := promptChoices("Web search provider:", []string{
			"Ollama (hosted web search API — needs an ollama.com key)",
			"Tavily (search API built for LLMs)",
			"Serper (Google results via serper.dev)",
			"Skip",
		})
		switch choice {
		case 0:
			runWebSearchSetup(cfgPath, "ollama")
		case 1:
			runWebSearchSetup(cfgPath, "tavily")
		case 2:
			runWebSearchSetup(cfgPath, "serper")
		}
	}
	fmt.Println()

	// ── Step 6: Starter agent ──────────────────────────────────────────────
	printStep(6, "Starter agent")
	agentCount := countAgentsOnDisk(ws)
	if agentCount > 0 {
		fmt.Printf("  %s %d agent(s) on disk. Skipping.\n", green("✓"), agentCount)
	} else {
		fmt.Printf("  %s No agents yet.\n", yellow("⚠"))
		if confirm("  Drop in the basic-chat starter?", true) {
			if err := writeStarterAgent(ws); err != nil {
				fmt.Printf("  %s Couldn't write starter: %v\n", red("✗"), err)
			} else {
				fmt.Printf("  %s Wrote %s\n", green("✓"),
					filepath.Join(ws.Agents, "basic-chat", "SOUL.yaml"))
			}
		}
	}
	fmt.Println()

	// ── Step 7: Production updates ─────────────────────────────────────────
	printStep(7, "Production updates")
	currentManifest := strings.TrimSpace(cfg.Updates.ManifestURL)
	if currentManifest == "" {
		fmt.Printf("  %s No update manifest configured yet.\n", yellow("⚠"))
		fmt.Printf("  %s This is fine for development, but production installs should know where to check for verified release bundles.\n", gray("→"))
	} else {
		fmt.Printf("  %s %s\n", dim("Current manifest:"), cyan(currentManifest))
	}
	if confirm("  Configure update manifest?", currentManifest == "") {
		defaultManifest := currentManifest
		if defaultManifest == "" {
			defaultManifest = "https://github.com/vmodekurti/soulacy/releases/latest/download/release-manifest.json"
		}
		manifest := prompt("  Manifest URL or local path", defaultManifest)
		if strings.TrimSpace(manifest) == "" {
			fmt.Printf("  %s No manifest entered. Nothing changed.\n", yellow("→"))
		} else if err := patchUpdateManifestURL(cfgPath, manifest); err != nil {
			fmt.Printf("  %s Couldn't write update manifest: %v\n", red("✗"), err)
		} else {
			cfg.Updates.ManifestURL = manifest
			fmt.Printf("  %s Update manifest: %s\n", green("✓"), manifest)
			fmt.Printf("  %s Verify later with: %s\n", gray("→"), bold("sy update check"))
		}
	}
	fmt.Println()

	// ── Step 8: Daemon install ─────────────────────────────────────────────
	printStep(8, "Auto-start on login")
	switch runtime.GOOS {
	case "darwin":
		fmt.Printf("  %s On macOS this installs a LaunchAgent.\n", gray("→"))
	case "linux":
		fmt.Printf("  %s On Linux this installs a systemd --user unit.\n", gray("→"))
	default:
		fmt.Printf("  %s Auto-start not supported on %s. Skipping.\n", yellow("⚠"), runtime.GOOS)
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		if confirm("  Install daemon so soulacy starts on login?", false) {
			if err := daemonInstall(); err != nil {
				fmt.Printf("  %s Daemon install failed: %v\n", red("✗"), err)
				fmt.Printf("  %s You can try again later: %s\n", gray("Hint:"), bold("sy daemon install"))
			}
		} else {
			fmt.Printf("  %s Skipped. Start manually with: %s\n", gray("→"), bold("soulacy serve"))
		}
	}
	fmt.Println()

	// ── Done ────────────────────────────────────────────────────────────────
	printOnboardDone(cfg, cfgPath)
	return nil
}

// ── Step helpers ────────────────────────────────────────────────────────────

// loadOrFreshConfig returns a usable Config for EnsureDirs/EnsureBootstrap
// even when config.Load fails (no config file yet on a virgin install).
// The returned config has just enough defaults for the bootstrap path
// to take over.
func loadOrFreshConfig(ws config.Paths) *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Host: "127.0.0.1",
			Port: 18789,
		},
		AgentDirs: []string{ws.Agents},
	}
}

// runProviderSetup prompts for an API key and patches config.yaml under
// llm.providers.<id>.api_key. Doesn't change default_provider unless the
// operator confirms.
func runProviderSetup(cfgPath, providerID, baseURL string) {
	if providerID == "custom" {
		providerID = prompt("  Provider ID (e.g. vllm, together)", "")
		if providerID == "" {
			fmt.Printf("  %s No provider ID entered. Nothing changed.\n", yellow("→"))
			return
		}
		baseURL = prompt("  Base URL (e.g. https://api.together.xyz/v1)", "")
	}

	if baseURL != "" {
		fmt.Printf("  %s Provider base URL: %s\n", dim("Endpoint:"), gray(baseURL))
	}
	key := prompt("  Paste API key (or blank to skip)", "")
	if key == "" {
		fmt.Printf("  %s No key entered. Nothing changed.\n", yellow("→"))
		return
	}
	if err := patchProviderKey(cfgPath, providerID, key); err != nil {
		fmt.Printf("  %s Patch failed: %v\n", red("✗"), err)
		return
	}
	if baseURL != "" {
		if err := patchProviderBaseURL(cfgPath, providerID, baseURL); err != nil {
			fmt.Printf("  %s Base URL patch failed: %v\n", red("✗"), err)
		}
	}
	fmt.Printf("  %s Wrote llm.providers.%s.api_key (masked: %s)\n",
		green("✓"), providerID, maskKey(key))

	// Live model listing: use the key we just stored to query the provider's
	// real catalogue and let the operator pick the model to use.
	selectProviderModel(cfgPath, providerID, baseURL, key)

	if confirm(fmt.Sprintf("  Make %s the default provider?", providerID), false) {
		if err := patchDefaultProvider(cfgPath, providerID); err != nil {
			fmt.Printf("  %s Patch failed: %v\n", red("✗"), err)
		} else {
			fmt.Printf("  %s Default provider: %s\n", green("✓"), providerID)
		}
	}
}

// selectProviderModel queries the provider for its available models and lets
// the operator pick one, persisting the choice to llm.providers.<id>.model.
// On any failure (network, auth, empty list) it degrades gracefully to a
// free-text prompt so onboarding never dead-ends.
func selectProviderModel(cfgPath, providerID, baseURL, apiKey string) {
	fmt.Printf("  %s Fetching available models…\n", gray("→"))
	models, err := fetchProviderModels(providerID, baseURL, apiKey)
	if err != nil || len(models) == 0 {
		if err != nil {
			fmt.Printf("  %s Couldn't list models automatically: %v\n", yellow("⚠"), err)
		} else {
			fmt.Printf("  %s Provider returned no models.\n", yellow("⚠"))
		}
		if m := prompt("  Model to use (blank to skip)", ""); m != "" {
			applyProviderModel(cfgPath, providerID, m)
		}
		return
	}

	sort.Strings(models)
	options := append(append([]string{}, models...), "Enter manually", "Skip")
	idx := promptChoices("  Select a model:", options)
	switch idx {
	case len(options) - 1: // Skip
		fmt.Printf("  %s No model selected.\n", yellow("→"))
	case len(options) - 2: // Enter manually
		if m := prompt("  Model to use", ""); m != "" {
			applyProviderModel(cfgPath, providerID, m)
		}
	default:
		applyProviderModel(cfgPath, providerID, models[idx])
	}
}

// applyProviderModel writes the chosen model and reports the outcome.
func applyProviderModel(cfgPath, providerID, model string) {
	if err := patchProviderModel(cfgPath, providerID, model); err != nil {
		fmt.Printf("  %s Couldn't write model: %v\n", red("✗"), err)
		return
	}
	fmt.Printf("  %s Model: %s\n", green("✓"), model)
}

// fetchProviderModels builds a transient provider client from the given
// credentials and returns its live model list. Known providers use their own
// factory; any other id is treated as an OpenAI-compatible endpoint.
func fetchProviderModels(providerID, baseURL, apiKey string) ([]string, error) {
	cfg := map[string]any{"api_key": apiKey}
	if baseURL != "" {
		cfg["base_url"] = baseURL
	}
	factory := providerID
	switch providerID {
	case "ollama", "openai", "anthropic", "gemini", "google":
		// use the provider's own factory
	default:
		// groq, openrouter, vllm, together, custom, … speak the OpenAI API
		factory = "openai"
		cfg["id"] = providerID
	}

	p, ok, err := registry.NewProvider(factory, cfg)
	if err != nil {
		return nil, err
	}
	if !ok || p == nil {
		if p, ok, err = registry.NewProvider("openai", cfg); err != nil {
			return nil, err
		} else if !ok || p == nil {
			return nil, fmt.Errorf("no client available for provider %q", providerID)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return p.Models(ctx)
}

// runWebSearchSetup sets search.provider and (optionally) search.api_key under
// the top-level `search:` block in config.yaml. The built-in web_search tool
// reads these at boot (see internal/app/wire.go). A blank key is fine: the
// gateway falls back to the provider's env var (OLLAMA_API_KEY / TAVILY_API_KEY
// / SERPER_API_KEY) at runtime, and the Ollama backend additionally reuses the
// key from llm.providers.ollama if one is already configured.
func runWebSearchSetup(cfgPath, provider string) {
	if err := patchSearchProvider(cfgPath, provider); err != nil {
		fmt.Printf("  %s Couldn't set search provider: %v\n", red("✗"), err)
		return
	}
	fmt.Printf("  %s Web search provider: %s\n", green("✓"), provider)

	var keyEnv, keyHint string
	switch provider {
	case "ollama":
		keyEnv, keyHint = "OLLAMA_API_KEY", "Create a key at https://ollama.com/settings/keys (or reuse your Ollama provider key)"
	case "tavily":
		keyEnv, keyHint = "TAVILY_API_KEY", "Create a key at https://app.tavily.com"
	case "serper":
		keyEnv, keyHint = "SERPER_API_KEY", "Create a key at https://serper.dev"
	default:
		keyEnv = "the provider's API key env var"
	}
	if keyHint != "" {
		fmt.Printf("  %s %s\n", dim("Key:"), gray(keyHint))
	}
	key := prompt("  Paste API key (blank to use the "+keyEnv+" env var instead)", "")
	if key == "" {
		fmt.Printf("  %s No key written. web_search will read $%s at runtime if set.\n", yellow("→"), keyEnv)
		return
	}
	if err := patchSearchAPIKey(cfgPath, key); err != nil {
		fmt.Printf("  %s Couldn't write search key: %v\n", red("✗"), err)
		return
	}
	fmt.Printf("  %s Wrote search.api_key (masked: %s)\n", green("✓"), maskKey(key))
}

// patchSearchProvider upserts search.provider (unquoted scalar).
func patchSearchProvider(path, provider string) error {
	doc, root, err := loadConfigDoc(path)
	if err != nil {
		return err
	}
	setScalar(ensureMapping(root, "search"), "provider", provider, 0)
	return saveConfigDoc(path, doc)
}

// patchSearchAPIKey upserts search.api_key (double-quoted).
func patchSearchAPIKey(path, apiKey string) error {
	doc, root, err := loadConfigDoc(path)
	if err != nil {
		return err
	}
	setScalar(ensureMapping(root, "search"), "api_key", apiKey, yaml.DoubleQuotedStyle)
	return saveConfigDoc(path, doc)
}

// patchUpdateManifestURL upserts updates.manifest_url. It is intentionally
// separate from provider/search setup because production update readiness is an
// operator concern, not an agent/provider credential.
func patchUpdateManifestURL(path, manifestURL string) error {
	doc, root, err := loadConfigDoc(path)
	if err != nil {
		return err
	}
	setScalar(ensureMapping(root, "updates"), "manifest_url", strings.TrimSpace(manifestURL), yaml.DoubleQuotedStyle)
	return saveConfigDoc(path, doc)
}

// ollamaUp returns true when a HEAD/GET to localhost:11434 succeeds in
// under 1 second. We use this for the "Ollama is running locally" hint
// in step 3 — never to make a decision for the operator.
func ollamaUp() bool {
	client := &http.Client{Timeout: 1 * time.Second}
	resp, err := client.Get("http://localhost:11434/api/tags")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 500
}

// countAgentsOnDisk walks the agents dir looking for SOUL.yaml files.
// Doesn't recurse beyond the immediate child dirs — agents live at
// <agents>/<id>/SOUL.yaml in the soulspace layout.
func countAgentsOnDisk(ws config.Paths) int {
	entries, err := os.ReadDir(ws.Agents)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(ws.Agents, e.Name(), "SOUL.yaml")); err == nil {
			n++
		}
	}
	return n
}

// writeStarterAgent writes a minimal but functional SOUL.yaml so the
// GUI isn't empty on first open. The agent uses the configured default
// provider with no special tools — just chat.
func writeStarterAgent(ws config.Paths) error {
	dir := filepath.Join(ws.Agents, "basic-chat")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "SOUL.yaml")
	if _, err := os.Stat(path); err == nil {
		return nil // don't overwrite
	}
	prompt := agentprompt.EnsureShared("You are Basic chat, a friendly starter assistant for getting comfortable with Soulacy. Answer questions accurately and concisely. Use available tools only when they materially help, and say when you do not know.")
	prompt = "  " + strings.ReplaceAll(prompt, "\n", "\n  ")
	content := `# Starter agent written by ` + "`sy onboard`" + `.
# Edit freely — the gateway watches this file and reloads on change.
id: basic-chat
name: Basic chat
description: A friendly assistant for getting started.
system_prompt: |
` + prompt + `
llm:
  # default_provider from config.yaml wins unless you override here:
  # provider: openai
  # model: gpt-4o-mini
  temperature: 0.7
`
	return os.WriteFile(path, []byte(content), 0o644)
}

// ── config.yaml edits via the yaml.v3 Node API ──────────────────────────────
//
// We parse config.yaml into a yaml.Node tree, mutate the specific keys, and
// re-marshal. yaml.v3 preserves comments attached to surviving nodes, so the
// file stays "lived in" while edits are structurally correct — no fragile
// string offsets that could write a value into the wrong provider block.

// loadConfigDoc reads path and returns the document node plus its root mapping.
// A missing-or-empty file yields a fresh empty mapping so callers can populate it.
func loadConfigDoc(path string) (*yaml.Node, *yaml.Node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
		return &doc, root, nil
	}
	return &doc, doc.Content[0], nil
}

// saveConfigDoc marshals doc with 2-space indentation (matching the template)
// and writes it back at 0600 (the file contains api keys).
func saveConfigDoc(path string, doc *yaml.Node) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o600)
}

// yamlMapValue returns the value node paired with key in mapping m, or nil.
func yamlMapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// setScalar sets m[key] to a scalar string with the given style (use 0 for a
// plain/unquoted scalar, yaml.DoubleQuotedStyle to force quotes). The key/value
// pair is created if absent and updated in place otherwise — so re-running with
// an unchanged value can never produce a duplicate key.
func setScalar(m *yaml.Node, key, value string, style yaml.Style) {
	if v := yamlMapValue(m, key); v != nil {
		v.Kind = yaml.ScalarNode
		v.Tag = "!!str"
		v.Value = value
		v.Style = style
		return
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: style},
	)
}

func setBool(m *yaml.Node, key string, value bool) {
	text := "false"
	if value {
		text = "true"
	}
	if v := yamlMapValue(m, key); v != nil {
		v.Kind = yaml.ScalarNode
		v.Tag = "!!bool"
		v.Value = text
		v.Style = 0
		return
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: text},
	)
}

// ensureMapping returns m[key] as a mapping node, creating it if absent.
func ensureMapping(m *yaml.Node, key string) *yaml.Node {
	if v := yamlMapValue(m, key); v != nil {
		if v.Kind != yaml.MappingNode {
			v.Kind = yaml.MappingNode
			v.Tag = "!!map"
			v.Value = ""
			v.Content = nil
		}
		return v
	}
	child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		child,
	)
	return child
}

type deploymentSettings struct {
	Mode                string
	PostgresDSN         string
	JWTSecret           string
	APIKey              string
	NATSURL             string
	SharedArtifactStore string
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// patchDeploymentSettings changes only the operating-mode fields. It is safe
// to run repeatedly and deliberately preserves unrelated operator settings.
func patchDeploymentSettings(path string, settings deploymentSettings) error {
	doc, root, err := loadConfigDoc(path)
	if err != nil {
		return err
	}
	deployment := ensureMapping(root, "deployment")
	setScalar(deployment, "mode", settings.Mode, 0)

	if config.IsMultiUserMode(settings.Mode) {
		setScalar(ensureMapping(root, "auth"), "mode", "jwt", 0)
		setScalar(ensureMapping(root, "auth"), "jwt_secret", settings.JWTSecret, yaml.DoubleQuotedStyle)
		setScalar(ensureMapping(root, "storage"), "backend", "postgres", 0)
		setScalar(ensureMapping(root, "storage"), "postgres_dsn", settings.PostgresDSN, yaml.DoubleQuotedStyle)
		setScalar(ensureMapping(root, "executor"), "backend", "docker", 0)
		sandbox := ensureMapping(ensureMapping(root, "runtime"), "sandbox")
		setBool(sandbox, "enabled", true)
		setScalar(sandbox, "mode", "docker", 0)
		server := ensureMapping(root, "server")
		setBool(server, "allow_unauthenticated", false)
		setScalar(server, "api_key", settings.APIKey, yaml.DoubleQuotedStyle)
	}
	if settings.Mode == config.DeploymentModeScale {
		queue := ensureMapping(root, "queue")
		setScalar(queue, "backend", "nats", 0)
		setScalar(queue, "nats_url", settings.NATSURL, yaml.DoubleQuotedStyle)
		setScalar(deployment, "shared_artifact_store", settings.SharedArtifactStore, yaml.DoubleQuotedStyle)
	}
	return saveConfigDoc(path, doc)
}

func patchServerHost(path, host string) error {
	doc, root, err := loadConfigDoc(path)
	if err != nil {
		return err
	}
	setScalar(ensureMapping(root, "server"), "host", host, yaml.DoubleQuotedStyle)
	return saveConfigDoc(path, doc)
}

// patchProviderKey sets llm.providers.<id>.api_key, creating the llm,
// providers, and provider blocks as needed.
func patchProviderKey(path, providerID, apiKey string) error {
	doc, root, err := loadConfigDoc(path)
	if err != nil {
		return err
	}
	providers := ensureMapping(ensureMapping(root, "llm"), "providers")
	setScalar(ensureMapping(providers, providerID), "api_key", apiKey, yaml.DoubleQuotedStyle)
	return saveConfigDoc(path, doc)
}

// patchProviderBaseURL sets llm.providers.<id>.base_url on the named provider
// only — sibling providers are never touched.
func patchProviderBaseURL(path, providerID, baseURL string) error {
	doc, root, err := loadConfigDoc(path)
	if err != nil {
		return err
	}
	providers := ensureMapping(ensureMapping(root, "llm"), "providers")
	setScalar(ensureMapping(providers, providerID), "base_url", baseURL, yaml.DoubleQuotedStyle)
	return saveConfigDoc(path, doc)
}

// patchProviderModel sets llm.providers.<id>.model on the named provider only.
func patchProviderModel(path, providerID, model string) error {
	doc, root, err := loadConfigDoc(path)
	if err != nil {
		return err
	}
	providers := ensureMapping(ensureMapping(root, "llm"), "providers")
	setScalar(ensureMapping(providers, providerID), "model", model, yaml.DoubleQuotedStyle)
	return saveConfigDoc(path, doc)
}

func patchDefaultProvider(path, providerID string) error {
	doc, root, err := loadConfigDoc(path)
	if err != nil {
		return err
	}
	setScalar(ensureMapping(root, "llm"), "default_provider", providerID, 0)
	return saveConfigDoc(path, doc)
}

// ── Cosmetic helpers ────────────────────────────────────────────────────────

func maskKey(k string) string {
	if len(k) <= 8 {
		return strings.Repeat("•", len(k))
	}
	return k[:3] + strings.Repeat("•", len(k)-7) + k[len(k)-4:]
}

func printOnboardBanner() {
	fmt.Println()
	fmt.Println(bold(cyan("  ╔═══════════════════════════════════════════════╗")))
	fmt.Println(bold(cyan("  ║   Soulacy — onboarding                        ║")))
	fmt.Println(bold(cyan("  ║   We'll only ask about things that matter.    ║")))
	fmt.Println(bold(cyan("  ╚═══════════════════════════════════════════════╝")))
	fmt.Println()
}

func printOnboardDone(cfg *config.Config, cfgPath string) {
	host := cfg.Server.Host
	if host == "" {
		host = "127.0.0.1"
	}
	url := fmt.Sprintf("http://%s:%d", host, cfg.Server.Port)
	fmt.Println(bold(green("  ╔═══════════════════════════════════════════════╗")))
	fmt.Println(bold(green("  ║   You're set.                                 ║")))
	fmt.Println(bold(green("  ╚═══════════════════════════════════════════════╝")))
	fmt.Println()
	fmt.Printf("  %s %s\n", dim("Config:"), gray(cfgPath))
	fmt.Printf("  %s %s\n", dim("URL:"), cyan(url))
	if cfg.Server.APIKey != "" {
		fmt.Printf("  %s %s\n", dim("API key:"), gray(maskKey(cfg.Server.APIKey)))
	}
	fmt.Println()
	fmt.Printf("  Next:\n")
	fmt.Printf("    %s %s     %s\n", green("→"), bold("soulacy serve"), gray("(start the gateway)"))
	fmt.Printf("    %s %s          %s\n", green("→"), bold("sy doctor"), gray("(verify everything's healthy)"))
	fmt.Printf("    %s %s         %s\n", green("→"), bold("sy agent ls"), gray("(see what's installed)"))
	fmt.Println()
}
