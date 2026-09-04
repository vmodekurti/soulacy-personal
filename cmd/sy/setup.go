// setup.go — interactive Soulacy setup wizard.
// Invoked with: sy setup
// No third-party dependencies — pure stdlib + ANSI escape codes.
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/soulacy/soulacy/internal/config"
)

// ── ANSI colour helpers ───────────────────────────────────────────────────────

const (
	clrReset  = "\033[0m"
	clrBold   = "\033[1m"
	clrDim    = "\033[2m"
	clrGreen  = "\033[32m"
	clrYellow = "\033[33m"
	clrBlue   = "\033[34m"
	clrCyan   = "\033[36m"
	clrRed    = "\033[31m"
	clrGray   = "\033[90m"
)

func bold(s string) string   { return clrBold + s + clrReset }
func green(s string) string  { return clrGreen + s + clrReset }
func yellow(s string) string { return clrYellow + s + clrReset }
func cyan(s string) string   { return clrCyan + s + clrReset }
func red(s string) string    { return clrRed + s + clrReset }
func dim(s string) string    { return clrDim + s + clrReset }
func gray(s string) string   { return clrGray + s + clrReset }

// ── Wizard state ─────────────────────────────────────────────────────────────

type setupConfig struct {
	// Server
	Host   string
	Port   int
	APIKey string

	// LLM
	LLMProvider  string
	LLMModel     string
	OllamaURL    string
	OllamaAPIKey string
	OpenAIKey    string
	AnthropicKey string

	// Web search (built-in web_search tool)
	SearchProvider string // "", "ollama", "tavily", "serper"
	SearchAPIKey   string

	// Channels
	TelegramToken   string
	TelegramAgent   string
	DiscordToken    string
	DiscordAgent    string
	SlackBotToken   string
	SlackAppToken   string
	SlackAgent      string
	WAPhoneNumberID string
	WAAccessToken   string
	WAVerifyToken   string
	WAAgent         string

	// Runtime
	PythonBin string
	LogLevel  string

	// Paths
	DataDir    string
	ConfigPath string
}

var reader = bufio.NewReader(os.Stdin)

// buildSetupCmd returns the `sy setup` cobra command.
func buildSetupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Interactive setup wizard — configure Soulacy from scratch",
		Long: `Run the Soulacy setup wizard.

Walks you through every setting, detects what's installed on your system,
writes ~/.soulacy/config.yaml, creates the data directory structure,
and optionally installs an example agent and starts the gateway.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSetupWizard()
		},
	}
}

func runSetupWizard() error {
	clearScreen()
	printBanner()

	home, _ := os.UserHomeDir()
	cfg := &setupConfig{
		Host:        "127.0.0.1",
		Port:        18789,
		LLMProvider: "ollama",
		LLMModel:    "llama3",
		OllamaURL:   "http://localhost:11434",
		PythonBin:   "python3",
		LogLevel:    "info",
		DataDir:     setupDataDir(home),
	}

	// ── Workspace location (default: ~/.soulacy/soulspace) ─────────────────
	chooseWorkspaceLocation(cfg, home)
	cfg.ConfigPath = filepath.Join(cfg.DataDir, "config.yaml")

	// ── Check for existing config ──────────────────────────────────────────
	if _, err := os.Stat(cfg.ConfigPath); err == nil {
		fmt.Printf("\n%s Existing config found at %s\n", yellow("!"), gray(cfg.ConfigPath))
		if !confirm("Overwrite it with new settings?", false) {
			fmt.Println(dim("\nSetup cancelled. Your existing config was not changed."))
			return nil
		}
	}

	fmt.Printf("\n%s Let's configure Soulacy step by step. Press Enter to accept defaults.\n\n",
		cyan("→"))

	// ── Step 1: Prerequisites ─────────────────────────────────────────────
	printStep(1, "Prerequisites")
	checkPrerequisites(cfg)

	// ── Step 2: Gateway settings ──────────────────────────────────────────
	printStep(2, "Gateway server")
	setupGateway(cfg)

	// ── Step 3: LLM provider ──────────────────────────────────────────────
	printStep(3, "LLM provider")
	setupLLM(cfg)

	// ── Step 4: Channels ──────────────────────────────────────────────────
	printStep(4, "Messaging channels")
	setupChannels(cfg)

	// ── Step 5: Runtime ───────────────────────────────────────────────────
	printStep(5, "Runtime & logging")
	setupRuntime(cfg)

	// ── Summary ───────────────────────────────────────────────────────────
	printSummary(cfg)
	if !confirm("Write this configuration?", true) {
		fmt.Println(dim("\nSetup cancelled. Nothing was written."))
		return nil
	}

	// ── Write ─────────────────────────────────────────────────────────────
	if err := writeConfig(cfg); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := createDataDirs(cfg); err != nil {
		return fmt.Errorf("create dirs: %w", err)
	}

	// Persist a non-default workspace location so the gateway resolves it on
	// every boot (env vars don't survive launchctl/systemd). The canonical
	// ~/.soulacy/soulspace needs no pointer.
	if cfg.DataDir == defaultWorkspace(home) {
		_ = config.ClearWorkspacePointer(home)
	} else if err := config.SaveWorkspacePointer(home, cfg.DataDir); err != nil {
		fmt.Printf("  %s Could not record workspace location: %v\n", yellow("!"), err)
		fmt.Printf("  %s Set %s so the gateway finds it.\n", gray("→"),
			cyan("SOULACY_WORKSPACE="+cfg.DataDir))
	}

	installExampleAgent(cfg)

	printSuccess(cfg)
	return nil
}

// ── Step implementations ──────────────────────────────────────────────────────

func checkPrerequisites(cfg *setupConfig) {
	// Go binary
	checkItem("Soulacy binaries", func() (string, bool) {
		if _, err := exec.LookPath("soulacy"); err == nil {
			return "soulacy found in PATH", true
		}
		return "not in PATH (run: make install)", false
	})

	// Ollama
	ollamaOK := checkItem("Ollama (local LLM)", func() (string, bool) {
		models := fetchOllamaModels("http://localhost:11434")
		if models != nil {
			return fmt.Sprintf("running · %d model(s) installed", len(models)), true
		}
		return "not running  →  https://ollama.com", false
	})
	if !ollamaOK {
		fmt.Printf("  %s Ollama not detected — you can still use OpenAI/Anthropic, or start Ollama first.\n",
			yellow("→"))
	}

	// Python
	checkItem("Python 3 (for tools/agents)", func() (string, bool) {
		out, err := exec.Command("python3", "--version").Output()
		if err == nil {
			cfg.PythonBin = "python3"
			return strings.TrimSpace(string(out)), true
		}
		out, err = exec.Command("python", "--version").Output()
		if err == nil && strings.Contains(string(out), "3.") {
			cfg.PythonBin = "python"
			return strings.TrimSpace(string(out)), true
		}
		return "not found (optional — required for Python tools)", false
	})

	// Port availability
	checkItem(fmt.Sprintf("Port %d available", cfg.Port), func() (string, bool) {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
		if err != nil {
			return fmt.Sprintf("in use — choose a different port below"), false
		}
		ln.Close()
		return "free", true
	})

	fmt.Println()
}

func setupGateway(cfg *setupConfig) {
	fmt.Printf("  %s Bind address %s\n", dim("Host"), gray("(127.0.0.1 = local only, 0.0.0.0 = all interfaces)"))
	cfg.Host = prompt("  Host", cfg.Host)

	portStr := prompt("  Port", strconv.Itoa(cfg.Port))
	if p, err := strconv.Atoi(portStr); err == nil && p > 0 && p < 65536 {
		cfg.Port = p
	}

	fmt.Printf("\n  %s An API key protects the gateway REST API with Bearer auth.\n",
		dim("Security"))
	fmt.Printf("  %s\n", gray("Leave blank for open access (dev mode only — never expose to a network without a key)"))

	choice := promptChoices("  API key", []string{
		"Generate a random key (recommended)",
		"Enter my own key",
		"Leave blank (dev/local only)",
	})
	switch choice {
	case 0:
		cfg.APIKey = generateAPIKey()
		fmt.Printf("  %s Generated: %s\n", green("✓"), bold(cfg.APIKey))
		fmt.Printf("  %s Save this — you'll need it for CLI and GUI access.\n", yellow("!"))
	case 1:
		cfg.APIKey = prompt("  API key", "")
	case 2:
		cfg.APIKey = ""
		fmt.Printf("  %s %s\n", yellow("⚠"), yellow("No API key — gateway will be open to anyone who can reach it."))
	}
	fmt.Println()
}

func setupLLM(cfg *setupConfig) {
	fmt.Printf("  %s\n", dim("Choose your primary LLM provider:"))
	choice := promptChoices("  Provider", []string{
		"Ollama (local, private, no API key needed) ← recommended",
		"OpenAI (GPT-4o, requires API key)",
		"Anthropic (Claude, requires API key)",
		"Other (OpenAI-compatible endpoint)",
	})

	switch choice {
	case 0:
		cfg.LLMProvider = "ollama"
		cfg.OllamaURL = prompt("  Ollama URL", cfg.OllamaURL)
		cfg.LLMModel = pickOllamaModel(cfg.OllamaURL)

	case 1:
		cfg.LLMProvider = "openai"
		cfg.OpenAIKey = prompt("  OpenAI API key", "")
		models := []string{"gpt-4o", "gpt-4o-mini", "gpt-4-turbo", "gpt-3.5-turbo"}
		cfg.LLMModel = models[promptChoices("  Model", models)]

	case 2:
		cfg.LLMProvider = "anthropic"
		cfg.AnthropicKey = prompt("  Anthropic API key", "")
		models := []string{"claude-sonnet-4-6", "claude-opus-4-6", "claude-haiku-4-5-20251001"}
		cfg.LLMModel = models[promptChoices("  Model", models)]

	case 3:
		cfg.LLMProvider = "openai" // compatible
		cfg.OllamaURL = prompt("  Base URL (e.g. http://my-server:8080/v1)", "")
		cfg.OpenAIKey = prompt("  API key (if required)", "")
		cfg.LLMModel = prompt("  Model name", "")
	}
	fmt.Println()

	// Web Search Tool configuration — choose a provider for the built-in
	// web_search tool. Works with any LLM provider above.
	fmt.Printf("  %s The built-in web_search tool gives agents live web results.\n", dim("Web Search"))
	switch promptChoices("  Web search provider", []string{
		"None / disabled",
		"Ollama Web Search (requires Ollama API key) — https://ollama.com/settings/keys",
		"Tavily (requires Tavily API key) — https://tavily.com",
		"Serper (requires Serper API key) — https://serper.dev",
	}) {
	case 1:
		cfg.SearchProvider = "ollama"
		cfg.SearchAPIKey = prompt("  Ollama API key", "")
		// Keep the legacy ollama provider key in sync so existing wiring still works.
		if cfg.OllamaAPIKey == "" {
			cfg.OllamaAPIKey = cfg.SearchAPIKey
		}
	case 2:
		cfg.SearchProvider = "tavily"
		cfg.SearchAPIKey = prompt("  Tavily API key", "")
	case 3:
		cfg.SearchProvider = "serper"
		cfg.SearchAPIKey = prompt("  Serper API key", "")
	default:
		cfg.SearchProvider = ""
	}
	fmt.Println()
}

func setupChannels(cfg *setupConfig) {
	fmt.Printf("  %s Enable the channels you want Soulacy to listen on.\n", dim("Channels"))
	fmt.Printf("  %s\n\n", gray("HTTP webhook is always on — others require external accounts/tokens."))

	fmt.Printf("  %s  HTTP webhook %s\n", green("✓"), gray("always enabled — POST to /api/v1/chat"))

	if confirm("  Enable Telegram?", false) {
		fmt.Printf("  %s Get your token from @BotFather on Telegram\n", gray("→"))
		cfg.TelegramToken = prompt("  Bot token", "")
		cfg.TelegramAgent = prompt("  Default agent ID to handle messages (create this agent before enabling)", "assistant")
	}

	if confirm("  Enable Discord?", false) {
		fmt.Printf("  %s Create a bot at discord.com/developers/applications\n", gray("→"))
		cfg.DiscordToken = prompt("  Bot token (Bot YOUR_TOKEN)", "")
		cfg.DiscordAgent = prompt("  Default agent ID to handle messages (create this agent before enabling)", "assistant")
	}

	if confirm("  Enable Slack?", false) {
		fmt.Printf("  %s Enable Socket Mode in your Slack app settings\n", gray("→"))
		cfg.SlackBotToken = prompt("  Bot token (xoxb-...)", "")
		cfg.SlackAppToken = prompt("  App-level token (xapp-...)", "")
		cfg.SlackAgent = prompt("  Default agent ID to handle messages (create this agent before enabling)", "assistant")
	}

	if confirm("  Enable WhatsApp?", false) {
		fmt.Printf("  %s Create a Meta Business app at developers.facebook.com\n", gray("→"))
		fmt.Printf("  %s Add the WhatsApp product, then get your Phone Number ID and access token.\n", gray("→"))
		fmt.Printf("  %s Point your Meta webhook to: https://YOUR-DOMAIN/channels/whatsapp/webhook\n", gray("→"))
		cfg.WAPhoneNumberID = prompt("  Phone Number ID", "")
		cfg.WAAccessToken = prompt("  Access token (permanent)", "")
		cfg.WAVerifyToken = prompt("  Verify token (choose any string — matches your Meta webhook config)", "")
		cfg.WAAgent = prompt("  Default agent ID to handle messages (create this agent before enabling)", "assistant")
	}

	fmt.Println()
}

func setupRuntime(cfg *setupConfig) {
	cfg.PythonBin = prompt("  Python binary", cfg.PythonBin)

	choice := promptChoices("  Log level", []string{
		"info  (recommended — key events only)",
		"debug (verbose — all internal events)",
		"warn  (quiet — warnings and errors only)",
		"error (silent — errors only)",
	})
	levels := []string{"info", "debug", "warn", "error"}
	cfg.LogLevel = levels[choice]
	fmt.Println()
}

// ── Config file writer ────────────────────────────────────────────────────────

func writeConfig(cfg *setupConfig) error {
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return err
	}

	apiKeyLine := `api_key: ""`
	if cfg.APIKey != "" {
		apiKeyLine = fmt.Sprintf(`api_key: "%s"`, cfg.APIKey)
	}

	ollamaSection := fmt.Sprintf(`    ollama:
      base_url: "%s"
      model: "%s"`, cfg.OllamaURL, cfg.LLMModel)
	if cfg.OllamaAPIKey != "" {
		ollamaSection = fmt.Sprintf(`    ollama:
      base_url: "%s"
      model: "%s"
      api_key: "%s"`, cfg.OllamaURL, cfg.LLMModel, cfg.OllamaAPIKey)
	}

	openaiSection := ""
	if cfg.OpenAIKey != "" {
		openaiSection = fmt.Sprintf(`
    openai:
      base_url: "https://api.openai.com/v1"
      api_key: "%s"
      model: "gpt-4o"`, cfg.OpenAIKey)
	}

	anthropicSection := ""
	if cfg.AnthropicKey != "" {
		anthropicSection = fmt.Sprintf(`
    anthropic:
      base_url: "https://api.anthropic.com"
      api_key: "%s"
      model: "claude-sonnet-4-6"`, cfg.AnthropicKey)
	}

	telegramSection := buildChannelSection("telegram",
		cfg.TelegramToken != "",
		map[string]string{"token": cfg.TelegramToken, "agent_id": cfg.TelegramAgent})

	discordSection := buildChannelSection("discord",
		cfg.DiscordToken != "",
		map[string]string{"token": cfg.DiscordToken, "agent_id": cfg.DiscordAgent})

	slackSection := buildChannelSection("slack",
		cfg.SlackBotToken != "",
		map[string]string{"bot_token": cfg.SlackBotToken, "app_token": cfg.SlackAppToken, "agent_id": cfg.SlackAgent})

	waSection := buildChannelSection("whatsapp",
		cfg.WAPhoneNumberID != "",
		map[string]string{
			"phone_number_id": cfg.WAPhoneNumberID,
			"access_token":    cfg.WAAccessToken,
			"verify_token":    cfg.WAVerifyToken,
			"agent_id":        cfg.WAAgent,
		})

	searchSection := ""
	if cfg.SearchProvider != "" {
		searchSection = fmt.Sprintf("search:\n  provider: \"%s\"", cfg.SearchProvider)
		if cfg.SearchAPIKey != "" {
			searchSection += fmt.Sprintf("\n  api_key: \"%s\"", cfg.SearchAPIKey)
		}
	}

	content := fmt.Sprintf(`# Soulacy configuration
# Generated by 'sy setup' on %s
# Docs: https://docs.soulacy.dev/configuration

server:
  host: "%s"
  port: %d
  gui_enabled: true
  %s

runtime:
  max_concurrent_sessions: 100
  default_max_turns: 20
  python_bin: "%s"
  tool_timeout: "30s"

memory:
  max_history: 50
  vector_db: ""

llm:
  default_provider: %s
  providers:
%s%s%s
%s
channels:
  http:
    enabled: true
%s%s%s%s

log:
  level: %s
  format: console
`,
		time.Now().Format("2006-01-02 15:04:05"),
		cfg.Host, cfg.Port, apiKeyLine,
		cfg.PythonBin,
		cfg.LLMProvider,
		ollamaSection, openaiSection, anthropicSection,
		searchSection,
		telegramSection, discordSection, slackSection, waSection,
		cfg.LogLevel,
	)

	return os.WriteFile(cfg.ConfigPath, []byte(content), 0600) // 0600 = owner read/write only
}

func buildChannelSection(name string, enabled bool, fields map[string]string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("  %s:\n    enabled: %v\n", name, enabled))
	for k, v := range fields {
		if v != "" {
			sb.WriteString(fmt.Sprintf("    %s: \"%s\"\n", k, v))
		}
	}
	return sb.String()
}

func createDataDirs(cfg *setupConfig) error {
	dirs := []string{
		filepath.Join(cfg.DataDir, "agents"),
		filepath.Join(cfg.DataDir, "plugins"),
		filepath.Join(cfg.DataDir, "memory"),
		filepath.Join(cfg.DataDir, "tools"),
		filepath.Join(cfg.DataDir, "gui"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return err
		}
	}
	return nil
}

func installExampleAgent(cfg *setupConfig) {
	// The built-in "system" agent is seeded in memory by the runtime loader.
	// Do not write any default SOUL.yaml here; user-defined agents should be
	// created explicitly through the GUI, CLI, or templates.
}

// ── Success screen ────────────────────────────────────────────────────────────

func printSummary(cfg *setupConfig) {
	fmt.Printf("\n%s\n", strings.Repeat("─", 56))
	fmt.Printf("%s\n\n", bold("  Configuration summary"))
	fmt.Printf("  %-20s %s\n", "Gateway", fmt.Sprintf("%s:%d", cfg.Host, cfg.Port))
	if cfg.APIKey != "" {
		fmt.Printf("  %-20s %s\n", "API key", bold(cfg.APIKey))
	} else {
		fmt.Printf("  %-20s %s\n", "API key", yellow("none (dev mode)"))
	}
	fmt.Printf("  %-20s %s / %s\n", "LLM", cfg.LLMProvider, cfg.LLMModel)
	if cfg.SearchProvider != "" {
		fmt.Printf("  %-20s %s\n", "Web search", bold(cfg.SearchProvider))
	} else {
		fmt.Printf("  %-20s %s\n", "Web search", yellow("disabled"))
	}
	fmt.Printf("  %-20s %s\n", "Python", cfg.PythonBin)
	fmt.Printf("  %-20s %s\n", "Log level", cfg.LogLevel)
	fmt.Printf("  %-20s %s\n", "Workspace", cfg.DataDir)
	fmt.Printf("  %-20s %s\n", "Config file", cfg.ConfigPath)

	channels := []string{"http (always on)"}
	if cfg.TelegramToken != "" {
		channels = append(channels, "telegram")
	}
	if cfg.DiscordToken != "" {
		channels = append(channels, "discord")
	}
	if cfg.SlackBotToken != "" {
		channels = append(channels, "slack")
	}
	if cfg.WAPhoneNumberID != "" {
		channels = append(channels, "whatsapp")
	}
	fmt.Printf("  %-20s %s\n", "Channels", strings.Join(channels, ", "))
	fmt.Printf("%s\n\n", strings.Repeat("─", 56))
}

func printSuccess(cfg *setupConfig) {
	fmt.Printf("\n%s Configuration written!\n\n", green("✓"))
	fmt.Printf("  %s\n", bold("Data directory:"))
	fmt.Printf("    %s\n\n", gray(cfg.DataDir))

	fmt.Printf("  %s\n", bold("Start the gateway:"))
	fmt.Printf("    %s\n\n", cyan("soulacy"))

	fmt.Printf("  %s\n", bold("Test it:"))
	if cfg.APIKey != "" {
		fmt.Printf("    %s\n", cyan(fmt.Sprintf("sy --api-key %s server status", cfg.APIKey)))
		fmt.Printf("    %s\n\n", cyan(fmt.Sprintf(`sy --api-key %s chat --agent system "Hi!"`, cfg.APIKey)))
	} else {
		fmt.Printf("    %s\n", cyan("sy server status"))
		fmt.Printf("    %s\n\n", cyan(`sy chat --agent system "Hi!"`))
	}

	if cfg.APIKey != "" {
		fmt.Printf("  %s Add this to your shell profile for a permanent shortcut:\n", bold("Tip:"))
		fmt.Printf("    %s\n\n",
			gray(fmt.Sprintf("export SOULACY_API_KEY=%s", cfg.APIKey)))
	}

	fmt.Printf("  %s\n    %s\n\n", bold("GUI (once gateway is running):"),
		cyan(fmt.Sprintf("open http://localhost:%d", cfg.Port)))
}

// ── Input helpers ─────────────────────────────────────────────────────────────

func prompt(label, defaultVal string) string {
	if defaultVal != "" {
		fmt.Printf("%s %s: ", label, gray("["+defaultVal+"]"))
	} else {
		fmt.Printf("%s: ", label)
	}
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)
	if input == "" {
		return defaultVal
	}
	return input
}

func confirm(label string, defaultYes bool) bool {
	hint := "y/N"
	if defaultYes {
		hint = "Y/n"
	}
	fmt.Printf("%s %s: ", label, gray("["+hint+"]"))
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToLower(input))
	if input == "" {
		return defaultYes
	}
	return input == "y" || input == "yes"
}

func promptChoices(label string, choices []string) int {
	fmt.Printf("  %s\n", label)
	for i, c := range choices {
		fmt.Printf("    %s %s\n", cyan(fmt.Sprintf("[%d]", i+1)), c)
	}
	for {
		fmt.Printf("  Choice %s: ", gray("[1]"))
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)
		if input == "" {
			return 0
		}
		n, err := strconv.Atoi(input)
		if err == nil && n >= 1 && n <= len(choices) {
			return n - 1
		}
		fmt.Printf("  %s Enter a number between 1 and %d\n", red("✗"), len(choices))
	}
}

func checkItem(label string, fn func() (string, bool)) bool {
	detail, ok := fn()
	icon := green("✓")
	if !ok {
		icon = yellow("!")
	}
	fmt.Printf("  %s  %-30s %s\n", icon, label, dim(detail))
	return ok
}

func printStep(n int, title string) {
	fmt.Printf("%s %s\n\n", bold(cyan(fmt.Sprintf("── Step %d:", n))), bold(title))
}

func printBanner() {
	fmt.Println(bold(cyan("  ╔═══════════════════════════════════════╗")))
	fmt.Println(bold(cyan("  ║   Soulacy Setup Wizard              ║")))
	fmt.Println(bold(cyan("  ║   Self-hosted · Privacy-first         ║")))
	fmt.Println(bold(cyan("  ╚═══════════════════════════════════════╝")))
	fmt.Println()
}

func clearScreen() {
	if runtime.GOOS == "windows" {
		return
	}
	fmt.Print("\033[H\033[2J")
}

// ── Utility ───────────────────────────────────────────────────────────────────

func generateAPIKey() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "sy_" + hex.EncodeToString(b)
}

func pickOllamaModel(baseURL string) string {
	names := fetchOllamaModels(baseURL)

	if len(names) == 0 {
		fmt.Printf("  %s Could not retrieve model list from Ollama.\n", yellow("!"))
		fmt.Printf("  %s Run %s to pull a model first, then re-run setup.\n",
			gray("→"), cyan("ollama pull llama3.2"))
		return prompt("  Model name", "")
	}

	fmt.Printf("  %s Found %d model(s) installed in Ollama:\n", green("✓"), len(names))
	for i, n := range names {
		fmt.Printf("    %s %s\n", cyan(fmt.Sprintf("[%d]", i+1)), n)
	}
	fmt.Printf("    %s Type a custom model name instead\n", cyan(fmt.Sprintf("[%d]", len(names)+1)))

	for {
		defaultHint := fmt.Sprintf("1 = %s", names[0])
		fmt.Printf("  Choose model %s: ", gray("["+defaultHint+"]"))
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)
		if input == "" {
			fmt.Printf("  %s Using %s\n", green("✓"), bold(names[0]))
			return names[0]
		}
		n, err := strconv.Atoi(input)
		if err == nil && n >= 1 && n <= len(names) {
			fmt.Printf("  %s Using %s\n", green("✓"), bold(names[n-1]))
			return names[n-1]
		}
		if err == nil && n == len(names)+1 {
			return prompt("  Model name", "")
		}
		// Non-numeric: treat as a direct model name
		if err != nil && len(input) > 0 {
			fmt.Printf("  %s Using %s\n", green("✓"), bold(input))
			return input
		}
		fmt.Printf("  %s Enter a number between 1 and %d\n", red("✗"), len(names)+1)
	}
}

// fetchOllamaModels queries the Ollama API and returns installed model names.
func fetchOllamaModels(baseURL string) []string {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(baseURL + "/api/tags")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil
	}

	names := make([]string, 0, len(result.Models))
	for _, m := range result.Models {
		if m.Name != "" {
			names = append(names, m.Name)
		}
	}
	return names
}

// defaultWorkspace is the canonical workspace root for new installs. It is
// ALWAYS ~/.soulacy/soulspace — the wizard offers an override, but this is the
// default every time.
func defaultWorkspace(home string) string {
	return filepath.Join(home, ".soulacy", config.SoulspaceDirName)
}

// setupDataDir picks the default workspace root the wizard pre-fills: an
// existing resolved workspace (so re-running setup keeps the current location,
// including a pointer-selected custom one), else the canonical soulspace path.
func setupDataDir(home string) string {
	if ws, err := config.ResolveWorkspace(); err == nil && ws.Root != "" {
		return ws.Root
	}
	return defaultWorkspace(home)
}

// expandPath resolves "~", "~/x", and relative paths to an absolute path so the
// operator can type a natural location at the prompt.
func expandPath(p, home string) string {
	p = strings.TrimSpace(p)
	switch {
	case p == "":
		return p
	case p == "~":
		return home
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(home, p[2:])
	}
	if !filepath.IsAbs(p) {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
	}
	return filepath.Clean(p)
}

// chooseWorkspaceLocation prompts for where the workspace should live, defaulting
// to ~/.soulacy/soulspace. A non-default choice is persisted as a pointer file
// after the config is written so the gateway finds it on every boot.
func chooseWorkspaceLocation(cfg *setupConfig, home string) {
	fmt.Printf("  %s Where should Soulacy store its workspace?\n", dim("Location"))
	fmt.Printf("  %s\n", gray("Holds config.yaml, agents, data, logs, and secrets — everything."))
	fmt.Printf("  %s\n", gray("Press Enter for the default, or type an absolute path."))
	chosen := expandPath(prompt("  Workspace", cfg.DataDir), home)
	if chosen != "" {
		cfg.DataDir = chosen
	}
	fmt.Println()
}
