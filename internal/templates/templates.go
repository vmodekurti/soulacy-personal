// Package templates serves agent starter templates — pre-built SOUL.yaml
// definitions a user can clone with one click to get a working agent without
// starting from a blank file. Inspired by Langflow's "New Project from
// Template" flow: the magic in those product demos is that the user lands on
// a wired-up flow in ~5 seconds.
//
// Discovery order (later entries take precedence):
//  1. Embedded defaults — shipped with the binary so the gateway works
//     out-of-the-box. Located in ./embedded/*.yaml relative to this file.
//  2. User dir (optional) — every *.yaml in the configured user templates
//     dir (default ~/.soulacy/templates). Same-name files override the
//     embedded ones, so a user can replace any default by dropping a file
//     with the matching basename.
//
// Each template is a regular SOUL.yaml. Its `id`, `name`, and `description`
// fields double as the template's display metadata — no separate manifest.
// The `id` is rewritten on instantiation so multiple instances can coexist.
package templates

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/soulacy/soulacy/pkg/agent"

	"github.com/soulacy/soulacy/internal/config"
)

//go:embed embedded/*.yaml
var embedded embed.FS

// Catalog scans embedded defaults + the user dir and returns the merged set.
// Safe to call repeatedly; results are NOT cached because users may drop new
// files into the user dir while the gateway is running and expect them to
// show up without a restart (mirrors the agent-loader hot-reload behaviour).
type Catalog struct {
	userDir string
}

// New creates a Catalog. userDir may be empty (only embedded defaults will
// be returned in that case).
func New(userDir string) *Catalog {
	return &Catalog{userDir: userDir}
}

// Entry is a single template's metadata + the raw YAML bytes. The bytes are
// returned alongside the parsed Definition so callers can either write the
// file verbatim (after ID rewriting) or use the parsed form for inspection.
type Entry struct {
	Name            string            `json:"name"`         // basename without extension, the public handle
	DisplayName     string            `json:"display_name"` // from Definition.Name
	Description     string            `json:"description"`  // from Definition.Description
	Tags            []string          `json:"tags"`
	Source          string            `json:"source"` // "embedded" | "user"
	Setup           []SetupItem       `json:"setup"`
	RequiredSecrets []RequiredSecret  `json:"required_secrets"`
	MockPrompt      string            `json:"mock_prompt"`
	ScheduleHint    string            `json:"schedule_hint,omitempty"`
	OutputHint      string            `json:"output_hint,omitempty"`
	Definition      *agent.Definition `json:"definition"` // parsed (for previews — engine isn't going to run this)
}

// SetupItem is derived readiness metadata for a template. It is intentionally
// conservative: "ready" means the SOUL.yaml has enough configuration to run
// locally, "needs_setup" means the user must connect data or delivery first,
// and "optional" means useful polish that is not a hard blocker.
type SetupItem struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"` // ready | needs_setup | optional
	Detail string `json:"detail,omitempty"`
}

// RequiredSecret describes credentials a template may need after creation.
// The catalog does not inspect the user's vault, so this is a checklist, not
// a live secret status.
type RequiredSecret struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Reason string `json:"reason,omitempty"`
}

// List returns all templates sorted by Name, with user-dir entries shadowing
// embedded ones of the same name.
func (c *Catalog) List() ([]Entry, error) {
	out := map[string]Entry{}

	// Embedded first.
	embEntries, err := readEmbedded()
	if err != nil {
		return nil, err
	}
	for _, e := range embEntries {
		out[e.Name] = e
	}

	// User overrides.
	if c.userDir != "" {
		userEntries, err := readUserDir(c.userDir)
		if err != nil {
			return nil, fmt.Errorf("read user templates: %w", err)
		}
		for _, e := range userEntries {
			out[e.Name] = e
		}
	}

	entries := make([]Entry, 0, len(out))
	for _, e := range out {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// Get returns one template by name (case-sensitive, matches the file basename
// without extension).
func (c *Catalog) Get(name string) (*Entry, error) {
	all, err := c.List()
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Name == name {
			return &all[i], nil
		}
	}
	return nil, fmt.Errorf("template %q not found", name)
}

// readEmbedded parses every *.yaml under embedded/.
func readEmbedded() ([]Entry, error) {
	dir := "embedded"
	files, err := fs.ReadDir(embedded, dir)
	if err != nil {
		return nil, fmt.Errorf("read embedded dir: %w", err)
	}
	var out []Entry
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		if !strings.HasSuffix(f.Name(), ".yaml") && !strings.HasSuffix(f.Name(), ".yml") {
			continue
		}
		data, err := embedded.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			return nil, err
		}
		e, err := parseEntry(f.Name(), data, "embedded")
		if err != nil {
			// Skip a broken embedded template rather than crashing the
			// whole catalog — a build-time test will catch it before ship.
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// readUserDir parses every *.yaml in the user templates dir (non-recursive).
// A missing dir is fine — just returns an empty list.
func readUserDir(dir string) ([]Entry, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		if !strings.HasSuffix(f.Name(), ".yaml") && !strings.HasSuffix(f.Name(), ".yml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			continue // tolerate transient read failures
		}
		e, err := parseEntry(f.Name(), data, "user")
		if err != nil {
			continue // tolerate user-broken templates rather than 500'ing
		}
		out = append(out, e)
	}
	return out, nil
}

func parseEntry(filename string, data []byte, source string) (Entry, error) {
	var def agent.Definition
	if err := yaml.Unmarshal(data, &def); err != nil {
		return Entry{}, fmt.Errorf("parse %s: %w", filename, err)
	}
	base := strings.TrimSuffix(strings.TrimSuffix(filename, ".yml"), ".yaml")
	setup, secrets, scheduleHint, outputHint := deriveSetup(&def)
	return Entry{
		Name:            base,
		DisplayName:     def.Name,
		Description:     def.Description,
		Tags:            def.Tags,
		Source:          source,
		Setup:           setup,
		RequiredSecrets: secrets,
		MockPrompt:      mockPrompt(base, &def),
		ScheduleHint:    scheduleHint,
		OutputHint:      outputHint,
		Definition:      &def,
	}, nil
}

func deriveSetup(def *agent.Definition) ([]SetupItem, []RequiredSecret, string, string) {
	if def == nil {
		return nil, nil, "", ""
	}
	var setup []SetupItem
	var secrets []RequiredSecret

	provider := strings.TrimSpace(def.LLM.Provider)
	model := strings.TrimSpace(def.LLM.Model)
	if provider != "" {
		detail := provider
		if model != "" {
			detail += " / " + model
		}
		status := "ready"
		if provider != "ollama" {
			status = "needs_setup"
			secrets = append(secrets, RequiredSecret{
				Key:    provider + ".api_key",
				Label:  providerDisplayName(provider) + " API key",
				Reason: "Required before this template can call the selected model provider.",
			})
		}
		setup = append(setup, SetupItem{Key: "model", Label: "Model", Status: status, Detail: detail})
	}

	if def.Trigger == agent.TriggerCron {
		cron := ""
		if def.Schedule != nil {
			cron = strings.TrimSpace(def.Schedule.Cron)
		}
		if cron == "" {
			setup = append(setup, SetupItem{Key: "schedule", Label: "Schedule", Status: "needs_setup", Detail: "Choose when this agent should run."})
		} else {
			setup = append(setup, SetupItem{Key: "schedule", Label: "Schedule", Status: "ready", Detail: cron})
		}

		channel, to := "", ""
		if def.Schedule != nil && def.Schedule.Output != nil {
			channel = strings.TrimSpace(def.Schedule.Output.Channel)
			to = strings.TrimSpace(def.Schedule.Output.To)
		}
		if channel == "" || to == "" {
			setup = append(setup, SetupItem{Key: "delivery", Label: "Scheduled output", Status: "needs_setup", Detail: "Select a channel and destination on the Schedule page."})
		} else {
			setup = append(setup, SetupItem{Key: "delivery", Label: "Scheduled output", Status: "ready", Detail: channel + " -> " + to})
		}
	}

	if strings.Contains(strings.ToLower(def.SystemPrompt), "knowledge base") || strings.Contains(strings.ToLower(def.SystemPrompt), "kb_search") || len(def.Knowledge) > 0 {
		status := "needs_setup"
		detail := "Create or attach a knowledge base before relying on grounded answers."
		if len(def.Knowledge) > 0 {
			status = "ready"
			detail = "Uses: " + strings.Join(def.Knowledge, ", ")
		}
		setup = append(setup, SetupItem{Key: "knowledge", Label: "Knowledge", Status: status, Detail: detail})
	}

	if def.Builtins != nil && len(*def.Builtins) > 0 {
		setup = append(setup, SetupItem{Key: "tools", Label: "Built-in tools", Status: "ready", Detail: strings.Join(*def.Builtins, ", ")})
	} else if len(def.Tools) > 0 {
		names := make([]string, 0, len(def.Tools))
		for _, tool := range def.Tools {
			if strings.TrimSpace(tool.Name) != "" {
				names = append(names, tool.Name)
			}
		}
		if len(names) > 0 {
			setup = append(setup, SetupItem{Key: "tools", Label: "Custom tools", Status: "ready", Detail: strings.Join(names, ", ")})
		}
	}

	if def.SystemTools || def.AllowShell || def.HasCapability("system") {
		setup = append(setup, SetupItem{Key: "system", Label: "System access", Status: "needs_setup", Detail: "Requires runtime.allow_system_tools and explicit operator review."})
	}

	// web_search needs a search provider credential regardless of provider — the
	// default (Ollama) hits ollama.com and needs an OLLAMA_API_KEY, and
	// Tavily/Serper need their own keys. Flag it so the user configures a search
	// provider before the agent runs, instead of hitting a runtime error.
	if usesWebSearch(def) {
		setup = append(setup, SetupItem{
			Key:    "search",
			Label:  "Web search",
			Status: "needs_setup",
			Detail: "This agent uses web_search — configure a search provider (Ollama, Tavily, or Serper) key.",
		})
		secrets = append(secrets, RequiredSecret{
			Key:    "search.api_key",
			Label:  "Web search provider key",
			Reason: "Required for the web_search tool. Default provider is Ollama (needs an ollama.com API key at https://ollama.com/settings/keys); Tavily and Serper are also supported.",
		})
	}

	scheduleHint := ""
	outputHint := ""
	if def.Trigger == agent.TriggerCron {
		scheduleHint = "Edit the cron and missed-run behavior on the Schedule page after creating the agent."
		outputHint = "Pick a Telegram, Slack, WhatsApp, or sidecar channel destination before enabling production delivery."
	}
	return setup, secrets, scheduleHint, outputHint
}

// usesWebSearch reports whether the agent relies on the web_search built-in,
// either by declaring it in builtins or referencing it in the system prompt.
func usesWebSearch(def *agent.Definition) bool {
	if def == nil {
		return false
	}
	if def.Builtins != nil {
		for _, b := range *def.Builtins {
			if strings.EqualFold(strings.TrimSpace(b), "web_search") {
				return true
			}
		}
	}
	return strings.Contains(strings.ToLower(def.SystemPrompt), "web_search")
}

func providerDisplayName(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai":
		return "OpenAI"
	case "anthropic":
		return "Anthropic"
	case "gemini", "google":
		return "Google Gemini"
	case "ollama_cloud":
		return "Ollama Cloud"
	case "openrouter":
		return "OpenRouter"
	case "groq":
		return "Groq"
	default:
		if provider == "" {
			return "Provider"
		}
		return strings.ToUpper(provider[:1]) + provider[1:]
	}
}

func mockPrompt(name string, def *agent.Definition) string {
	if def == nil {
		return "Say hello and explain what you can do."
	}
	tags := map[string]bool{}
	for _, tag := range def.Tags {
		tags[strings.ToLower(tag)] = true
	}
	lowerName := strings.ToLower(name + " " + def.Name)
	switch {
	case def.Trigger == agent.TriggerCron:
		return "Run one preview briefing now and clearly mark it as a test run."
	case tags["rag"] || strings.Contains(lowerName, "rag") || strings.Contains(lowerName, "compliance"):
		return "Use the attached knowledge base to answer: what are the three most important rules I should follow?"
	case strings.Contains(lowerName, "meeting"):
		return "Summarize this meeting transcript: Alice said the launch moves to Friday. Bob owns the pricing page. Priya will confirm analytics by Thursday."
	case strings.Contains(lowerName, "inbox"):
		return "Classify this email and draft a reply: Can you send the Q3 plan by tomorrow morning? We need it for the steering meeting."
	case tags["web"] || strings.Contains(lowerName, "research"):
		return "Research the latest public updates on this topic and return a concise sourced summary."
	default:
		return "Say hello, explain what you can do, and ask one useful follow-up question."
	}
}

// Instantiate clones the named template into a fresh Definition. The caller
// supplies the desired agent ID; if it's empty, the catalog derives one from
// the template name (e.g. "rag-over-docs"). The mustBeUnique predicate is
// applied to suffix-bump the ID until it returns true; callers should pass
// `func(id string) bool { return loader.Get(id) == nil }` so live agents
// aren't overwritten.
//
// The returned Definition is ready to hand to Loader.Upsert — SourcePath is
// blank so a fresh folder is created.
func (c *Catalog) Instantiate(templateName, desiredID string, mustBeUnique func(string) bool) (*agent.Definition, error) {
	entry, err := c.Get(templateName)
	if err != nil {
		return nil, err
	}
	if entry.Definition == nil {
		return nil, fmt.Errorf("template %q has no parsed definition", templateName)
	}

	// Shallow-copy so the catalog's cached Definition is not mutated. Slices
	// and maps are shared by reference, but the engine treats Definition
	// fields as read-only, so this is safe in the same way Loader.Get is.
	def := *entry.Definition

	// Pick a unique ID.
	base := strings.TrimSpace(desiredID)
	if base == "" {
		// Strip the "-template" suffix the embedded files use, so a fresh
		// instance lands at the natural name ("rag-over-docs" not
		// "rag-over-docs-template").
		base = strings.TrimSuffix(templateName, "-template")
	}
	def.ID = uniqueID(base, mustBeUnique)
	def.SourcePath = "" // force a new folder under <dir>/<id>/SOUL.yaml

	return &def, nil
}

func uniqueID(base string, mustBeUnique func(string) bool) string {
	if mustBeUnique == nil || mustBeUnique(base) {
		return base
	}
	for i := 2; i < 1_000_000; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if mustBeUnique(candidate) {
			return candidate
		}
	}
	return base // pathological — give up and let the loader complain
}

// DefaultUserDir returns the conventional location for user-supplied
// templates. The gateway is free to override via config.
func DefaultUserDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if ws, err := config.ResolveWorkspace(); err == nil {
		return ws.Templates
	}
	return filepath.Join(home, ".soulacy", "templates")
}
