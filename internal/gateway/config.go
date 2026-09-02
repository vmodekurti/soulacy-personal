// config.go — GET /api/v1/config and PATCH /api/v1/config handlers.
// GET returns the live in-memory config with secrets redacted.
// PATCH accepts a subset of editable fields, merges them into the config.yaml
// on disk, and returns the updated (redacted) view. A gateway restart is needed
// for most changes to take full effect.
package gateway

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/internal/voice"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// ── GET /api/v1/config ────────────────────────────────────────────────────────

// handleGetConfig returns the current gateway configuration.
// Sensitive fields (API keys, tokens) are redacted to "***".
func (s *Server) handleGetConfig(c *fiber.Ctx) error {
	return c.JSON(s.safeConfigView())
}

// safeConfigView builds a sanitised map from the live config.
func (s *Server) safeConfigView() fiber.Map {
	cfg := s.cfg

	// Build providers map with redacted keys
	providers := make(map[string]fiber.Map, len(cfg.LLM.Providers))
	for name, pc := range cfg.LLM.Providers {
		apiKey := ""
		if pc.APIKey != "" {
			apiKey = "***"
		}
		providers[name] = fiber.Map{
			"base_url":                   pc.BaseURL,
			"api_key":                    apiKey,
			"model":                      pc.Model,
			"keep_alive":                 pc.KeepAlive,
			"options":                    pc.Options,
			"region":                     pc.Region,
			"retention":                  pc.Retention,
			"local":                      pc.Local,
			"allowed_data_classes":       pc.AllowedDataClasses,
			"cache_allowed_data_classes": pc.CacheAllowedDataClasses,
			"max_tokens_per_minute":      pc.MaxTokensPerMinute,
		}
	}

	serverAPIKey := ""
	if cfg.Server.APIKey != "" {
		serverAPIKey = "***"
	}

	searchAPIKey := ""
	if cfg.Search.APIKey != "" {
		searchAPIKey = "***"
	}

	// Cohort G — expose the schema version + drift status so doctor
	// surfaces / GUI can render the advisory without a second endpoint.
	schemaStatus := config.CheckSchemaVersion(cfg)

	return fiber.Map{
		// Cohort G — schema stamp for the config file. See config.CurrentSchemaVersion.
		"schema_version": cfg.SchemaVersion,
		"schema_status": fiber.Map{
			"have":            schemaStatus.Have,
			"want":            schemaStatus.Want,
			"out_of_date":     schemaStatus.OutOfDate,
			"unstamped_fresh": schemaStatus.UnstampedFresh,
			"message":         schemaStatus.Message,
		},
		"server": fiber.Map{
			"host":        cfg.Server.Host,
			"port":        cfg.Server.Port,
			"gui_enabled": cfg.Server.GUIEnabled,
			"api_key":     serverAPIKey,
			"tls_cert":    cfg.Server.TLSCert,
			"tls_key":     cfg.Server.TLSKey,
		},
		"runtime": fiber.Map{
			"max_concurrent_sessions": cfg.Runtime.MaxConcurrentSessions,
			"default_max_turns":       cfg.Runtime.DefaultMaxTurns,
			"max_agent_call_depth":    cfg.Runtime.MaxAgentCallDepth,
			"python_bin":              cfg.Runtime.PythonBin,
			"tool_timeout":            cfg.Runtime.ToolTimeout,
		},
		"memory": fiber.Map{
			"dir":         cfg.Memory.Dir,
			"sqlite_path": cfg.Memory.SQLitePath,
			"vector_db":   cfg.Memory.VectorDB,
			"vector_url":  cfg.Memory.VectorURL,
			"max_history": cfg.Memory.MaxHistory,
		},
		"llm": fiber.Map{
			"default_provider":  cfg.LLM.DefaultProvider,
			"allowed_providers": cfg.LLM.AllowedProviders,
			"allowed_models":    cfg.LLM.AllowedModels,
			"providers":         providers,
			"studio": fiber.Map{
				"provider":           cfg.LLM.Studio.Provider,
				"model":              cfg.LLM.Studio.Model,
				"max_build_tokens":   cfg.LLM.Studio.MaxBuildTokens,
				"max_build_cost_usd": cfg.LLM.Studio.MaxBuildCostUSD,
			},
			"reasoner": fiber.Map{
				"provider": cfg.LLM.Reasoner.Provider,
				"model":    cfg.LLM.Reasoner.Model,
			},
		},
		"log": fiber.Map{
			"level":  cfg.Log.Level,
			"format": cfg.Log.Format,
			"file":   cfg.Log.File,
		},
		"search": fiber.Map{
			"provider": cfg.Search.Provider,
			"api_key":  searchAPIKey,
		},
		"voice": fiber.Map{
			"provider":     cfg.Voice.Provider,
			"model":        cfg.Voice.Model,
			"base_url":     cfg.Voice.BaseURL,
			"sidecar_url":  cfg.Voice.SidecarURL,
			"voice":        cfg.Voice.Voice,
			"timeout":      cfg.Voice.Timeout,
			"allow_remote": cfg.Voice.AllowRemote,
		},
		"costs": fiber.Map{
			"daily_budget_usd":            cfg.Costs.DailyBudgetUSD,
			"monthly_budget_usd":          cfg.Costs.MonthlyBudgetUSD,
			"per_user_daily_budget_usd":   cfg.Costs.PerUserDailyBudgetUSD,
			"per_agent_daily_budget_usd":  cfg.Costs.PerAgentDailyBudgetUSD,
			"alert_threshold":             cfg.Costs.AlertThreshold,
			"enforcement_mode":            cfg.Costs.EnforcementMode,
			"unknown_pricing":             cfg.Costs.UnknownPricing,
			"default_max_output_tokens":   cfg.Costs.DefaultMaxOutputTokens,
			"max_output_tokens_ceiling":   cfg.Costs.MaxOutputTokensCeiling,
			"reservation_ttl":             cfg.Costs.ReservationTTL,
			"max_concurrent_per_provider": cfg.Costs.MaxConcurrentPerProvider,
			"circuit_failure_threshold":   cfg.Costs.CircuitFailureThreshold,
			"circuit_cooldown":            cfg.Costs.CircuitCooldown,
			"pricing":                     cfg.Costs.Pricing,
		},
		"ops": fiber.Map{
			"slo_window":           cfg.Ops.SLOWindow,
			"max_failure_rate":     cfg.Ops.MaxFailureRate,
			"max_incomplete_rate":  cfg.Ops.MaxIncompleteRate,
			"max_p95_run_duration": cfg.Ops.MaxP95RunDuration,
			"min_runs_for_signal":  cfg.Ops.MinRunsForSignal,
			"alert_channel":        cfg.Ops.AlertChannel,
			"alert_to":             cfg.Ops.AlertTo,
			"alert_min_status":     cfg.Ops.AlertMinStatus,
		},
		"deployment": fiber.Map{
			"profile": cfg.Deployment.Profile,
			"owner":   cfg.Deployment.Owner,
			"region":  cfg.Deployment.Region,
			"notes":   cfg.Deployment.Notes,
		},
		// F-Bridge — surface the workspace security defaults so the GUI's
		// F-GUI-6 radio actually seeds from a saved value. IntentGate is not
		// a secret, so no redaction.
		"security": fiber.Map{
			"intent_gate": cfg.Security.IntentGate,
		},
		"agent_dirs": cfg.AgentDirs,
		"skill_dirs": cfg.SkillDirs,
		// GUI preferences that belong to the install, not to one browser.
		"ui": fiber.Map{
			"walkthrough_seen":    cfg.UI.WalkthroughSeen,
			"walkthrough_step":    cfg.UI.WalkthroughStep,
			"walkthrough_version": cfg.UI.WalkthroughVersion,
		},
		"channels":       safeChannelsView(cfg.Channels),
		"plugins_config": safePluginsConfigView(cfg.PluginsConfig),
		"_meta": fiber.Map{
			"config_path": s.cfgPath,
			"writable":    s.cfgPath != "",
			"note":        "Most changes require a gateway restart to take effect.",
		},
	}
}

// safeChannelsView returns a deep-copied channels map with secret values
// redacted to "***". Known channel types use their channelSpec Secret flags;
// keys not covered by a spec fall back to a generic secret-name heuristic so
// new/unknown channel types never leak credentials by default.
func safeChannelsView(chans map[string]map[string]any) map[string]map[string]any {
	out := make(map[string]map[string]any, len(chans))
	for chID, settings := range chans {
		spec := channelSpecByID(chID)
		safe := make(map[string]any, len(settings))
		for k, v := range settings {
			switch {
			case k == "bots":
				safe[k] = redactBotList(spec, v)
			case isSecretChannelKey(spec, k) && valuePresent(v):
				safe[k] = "***"
			default:
				safe[k] = v
			}
		}
		out[chID] = safe
	}
	return out
}

// isSecretChannelKey reports whether a channel settings key holds a secret.
// Spec-declared fields are authoritative; unknown keys are matched against
// common secret-name markers.
func isSecretChannelKey(spec *channelSpec, key string) bool {
	if spec != nil {
		for _, f := range spec.Fields {
			if f.Key == key {
				return f.Secret
			}
		}
	}
	lk := strings.ToLower(key)
	for _, marker := range []string{"token", "secret", "password", "api_key", "apikey", "credential"} {
		if strings.Contains(lk, marker) {
			return true
		}
	}
	return false
}

// redactBotList redacts secret fields in a channel's bots list.
func redactBotList(spec *channelSpec, raw any) []map[string]any {
	bots := rawBotList(raw)
	out := make([]map[string]any, 0, len(bots))
	for _, bot := range bots {
		row := make(map[string]any, len(bot))
		for k, v := range bot {
			if isSecretChannelKey(spec, k) && valuePresent(v) {
				row[k] = "***"
			} else {
				row[k] = v
			}
		}
		out = append(out, row)
	}
	return out
}

// ── PATCH /api/v1/config ─────────────────────────────────────────────────────

// PatchableConfig holds the subset of config fields editable via the API.
// Only fields present in the JSON body are applied; zero values are skipped.
//
// SECURITY NOTE (PRODUCTION_AUDIT → CRITICAL): TLSCert/TLSKey are
// deliberately NOT in the patchable surface. Anyone who could rotate those
// to attacker-readable paths would get file-read-as-gateway-user, so the
// only way to set them is to edit config.yaml on disk. Do NOT add TLS
// fields to this struct.
type PatchableConfig struct {
	Server *struct {
		Host       string `json:"host" yaml:"host"`
		Port       int    `json:"port" yaml:"port"`
		GUIEnabled *bool  `json:"gui_enabled" yaml:"gui_enabled"`
		APIKey     string `json:"api_key" yaml:"api_key"`
	} `json:"server" yaml:"server"`

	Runtime *struct {
		MaxConcurrentSessions int    `json:"max_concurrent_sessions" yaml:"max_concurrent_sessions"`
		DefaultMaxTurns       int    `json:"default_max_turns" yaml:"default_max_turns"`
		MaxAgentCallDepth     int    `json:"max_agent_call_depth" yaml:"max_agent_call_depth"`
		PythonBin             string `json:"python_bin" yaml:"python_bin"`
		ToolTimeout           string `json:"tool_timeout" yaml:"tool_timeout"`
	} `json:"runtime" yaml:"runtime"`

	Executor *struct {
		Backend               string   `json:"backend" yaml:"backend"`
		Workers               int      `json:"workers" yaml:"workers"`
		DockerImage           string   `json:"docker_image" yaml:"docker_image"`
		DockerNetwork         string   `json:"docker_network" yaml:"docker_network"`
		DockerVolumes         []string `json:"docker_volumes" yaml:"docker_volumes"`
		SSHHost               string   `json:"ssh_host" yaml:"ssh_host"`
		SSHUser               string   `json:"ssh_user" yaml:"ssh_user"`
		SSHPythonBin          string   `json:"ssh_python_bin" yaml:"ssh_python_bin"`
		SSHIdentity           string   `json:"ssh_identity" yaml:"ssh_identity"`
		SSHIdentityCredential string   `json:"ssh_identity_credential" yaml:"ssh_identity_credential"`
		CloudPreset           string   `json:"cloud_preset" yaml:"cloud_preset"`
		CloudTarget           string   `json:"cloud_target" yaml:"cloud_target"`
		CloudCLI              string   `json:"cloud_cli" yaml:"cloud_cli"`
	} `json:"executor" yaml:"executor"`

	LLM *struct {
		DefaultProvider  string   `json:"default_provider" yaml:"default_provider"`
		AllowedProviders []string `json:"allowed_providers" yaml:"allowed_providers"`
		AllowedModels    []string `json:"allowed_models" yaml:"allowed_models"`
		// Studio overrides the provider/model used for Studio reasoning + code
		// generation (llm.studio). Empty strings clear the override (fall back
		// to the default provider/model).
		Studio *struct {
			Provider        string  `json:"provider" yaml:"provider"`
			Model           string  `json:"model" yaml:"model"`
			MaxBuildTokens  int     `json:"max_build_tokens" yaml:"max_build_tokens"`
			MaxBuildCostUSD float64 `json:"max_build_cost_usd" yaml:"max_build_cost_usd"`
		} `json:"studio" yaml:"studio"`
		// Reasoner is the provider/model fallback for ReAct/Plan-Execute agents
		// without an agent-level assignment (llm.reasoner).
		Reasoner *struct {
			Provider string `json:"provider" yaml:"provider"`
			Model    string `json:"model" yaml:"model"`
		} `json:"reasoner" yaml:"reasoner"`
	} `json:"llm" yaml:"llm"`

	Log *struct {
		Level  string `json:"level" yaml:"level"`
		Format string `json:"format" yaml:"format"`
		File   string `json:"file" yaml:"file"`
	} `json:"log" yaml:"log"`

	// Search configures the built-in web_search tool. APIKey of "***" is the
	// redacted placeholder echoed by the GUI and is skipped on write so it
	// never overwrites the real key on disk.
	Search *struct {
		Provider string `json:"provider" yaml:"provider"`
		APIKey   string `json:"api_key" yaml:"api_key"`
	} `json:"search" yaml:"search"`

	Voice *struct {
		Provider    string `json:"provider" yaml:"provider"`
		Model       string `json:"model" yaml:"model"`
		BaseURL     string `json:"base_url" yaml:"base_url"`
		SidecarURL  string `json:"sidecar_url" yaml:"sidecar_url"`
		Voice       string `json:"voice" yaml:"voice"`
		Timeout     string `json:"timeout" yaml:"timeout"`
		AllowRemote *bool  `json:"allow_remote" yaml:"allow_remote"`
	} `json:"voice" yaml:"voice"`

	Costs *struct {
		DailyBudgetUSD           float64 `json:"daily_budget_usd" yaml:"daily_budget_usd"`
		MonthlyBudgetUSD         float64 `json:"monthly_budget_usd" yaml:"monthly_budget_usd"`
		PerUserDailyBudgetUSD    float64 `json:"per_user_daily_budget_usd" yaml:"per_user_daily_budget_usd"`
		PerAgentDailyBudgetUSD   float64 `json:"per_agent_daily_budget_usd" yaml:"per_agent_daily_budget_usd"`
		AlertThreshold           float64 `json:"alert_threshold" yaml:"alert_threshold"`
		EnforcementMode          string  `json:"enforcement_mode" yaml:"enforcement_mode"`
		UnknownPricing           string  `json:"unknown_pricing" yaml:"unknown_pricing"`
		DefaultMaxOutputTokens   int     `json:"default_max_output_tokens" yaml:"default_max_output_tokens"`
		MaxOutputTokensCeiling   int     `json:"max_output_tokens_ceiling" yaml:"max_output_tokens_ceiling"`
		ReservationTTL           string  `json:"reservation_ttl" yaml:"reservation_ttl"`
		MaxConcurrentPerProvider int     `json:"max_concurrent_per_provider" yaml:"max_concurrent_per_provider"`
		CircuitFailureThreshold  int     `json:"circuit_failure_threshold" yaml:"circuit_failure_threshold"`
		CircuitCooldown          string  `json:"circuit_cooldown" yaml:"circuit_cooldown"`
		Pricing                  map[string]struct {
			InputPerMTok       float64 `json:"input_per_mtok" yaml:"input_per_mtok"`
			OutputPerMTok      float64 `json:"output_per_mtok" yaml:"output_per_mtok"`
			CachedInputPerMTok float64 `json:"cached_input_per_mtok" yaml:"cached_input_per_mtok"`
			CacheWritePerMTok  float64 `json:"cache_write_per_mtok" yaml:"cache_write_per_mtok"`
			ReasoningPerMTok   float64 `json:"reasoning_per_mtok" yaml:"reasoning_per_mtok"`
			Source             string  `json:"source" yaml:"source"`
			EffectiveDate      string  `json:"effective_date" yaml:"effective_date"`
			Version            string  `json:"version" yaml:"version"`
		} `json:"pricing" yaml:"pricing"`
	} `json:"costs" yaml:"costs"`

	Ops *struct {
		SLOWindow         string  `json:"slo_window" yaml:"slo_window"`
		MaxFailureRate    float64 `json:"max_failure_rate" yaml:"max_failure_rate"`
		MaxIncompleteRate float64 `json:"max_incomplete_rate" yaml:"max_incomplete_rate"`
		MaxP95RunDuration string  `json:"max_p95_run_duration" yaml:"max_p95_run_duration"`
		MinRunsForSignal  int     `json:"min_runs_for_signal" yaml:"min_runs_for_signal"`
		AlertChannel      string  `json:"alert_channel" yaml:"alert_channel"`
		AlertTo           string  `json:"alert_to" yaml:"alert_to"`
		AlertMinStatus    string  `json:"alert_min_status" yaml:"alert_min_status"`
	} `json:"ops" yaml:"ops"`

	Deployment *struct {
		Profile string `json:"profile" yaml:"profile"`
		Owner   string `json:"owner" yaml:"owner"`
		Region  string `json:"region" yaml:"region"`
		Notes   string `json:"notes" yaml:"notes"`
	} `json:"deployment" yaml:"deployment"`

	// Security is the workspace-scoped security defaults section (Cohort
	// F-Bridge). Present-only-when-sent by the GUI; empty IntentGate leaves
	// the on-disk value untouched (mirrors the Log branch's empty-string
	// preservation, not Deployment's unconditional overwrite, because the
	// empty string is the meaningful "unset, fall through to per-agent"
	// sentinel).
	Security *struct {
		IntentGate string `json:"intent_gate" yaml:"intent_gate"`
	} `json:"security" yaml:"security"`

	AgentDirs []string `json:"agent_dirs" yaml:"agent_dirs"`
	SkillDirs []string `json:"skill_dirs" yaml:"skill_dirs"`

	// PluginsConfig edits plugin settings (Story 18). Merge semantics per
	// plugin section: present keys are set, JSON null deletes a key, a nil
	// section deletes the plugin's block, and "***" values are SKIPPED —
	// the GUI edits the redacted view, so round-tripped placeholders must
	// never overwrite real secrets on disk. Unknown keys are preserved.
	PluginsConfig map[string]map[string]any `json:"plugins_config" yaml:"plugins_config"`

	// UI holds web-UI preferences that persist per install (config.UIConfig).
	// Pointers throughout: a walkthrough that was explicitly dismissed writes
	// walkthrough_seen=false → true, and "explicitly false" has to survive the
	// round trip, so these cannot be plain bools with omitempty.
	UI *struct {
		WalkthroughSeen    *bool `json:"walkthrough_seen" yaml:"walkthrough_seen"`
		WalkthroughStep    *int  `json:"walkthrough_step" yaml:"walkthrough_step"`
		WalkthroughVersion *int  `json:"walkthrough_version" yaml:"walkthrough_version"`
	} `json:"ui" yaml:"ui"`
}

// handlePatchConfig merges partial config updates into config.yaml on disk.
func (s *Server) handlePatchConfig(c *fiber.Ctx) error {
	if s.cfgPath == "" {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "config file path unknown — restart with SOULACY_CONFIG_PATH set to enable writes",
		})
	}

	var patch PatchableConfig
	if err := c.BodyParser(&patch); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid request body: " + err.Error(),
		})
	}
	if patch.Voice != nil && patch.Voice.Provider == "sidecar" {
		allowRemote := patch.Voice.AllowRemote != nil && *patch.Voice.AllowRemote
		if _, err := voice.NewSidecar(patch.Voice.SidecarURL, patch.Voice.Voice, patch.Voice.Timeout, allowRemote); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
	}

	// Read current file
	current, err := readRawConfig(s.cfgPath)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to read config file: " + err.Error(),
		})
	}

	// Apply patch fields
	applyPatch(current, patch)

	// Write back
	if err := writeRawConfig(s.cfgPath, current); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to write config file: " + err.Error(),
		})
	}

	// Keep the in-memory view in step for the sections the running process
	// reads back without a restart. The walkthrough asks for its own state on
	// every page load, and a stale `false` here would re-open a tour the user
	// just dismissed — inside the same gateway process, no restart involved.
	if patch.UI != nil && s.cfg != nil {
		if patch.UI.WalkthroughSeen != nil {
			s.cfg.UI.WalkthroughSeen = *patch.UI.WalkthroughSeen
		}
		if patch.UI.WalkthroughStep != nil {
			s.cfg.UI.WalkthroughStep = *patch.UI.WalkthroughStep
		}
		if patch.UI.WalkthroughVersion != nil {
			s.cfg.UI.WalkthroughVersion = *patch.UI.WalkthroughVersion
		}
	}
	if patch.Voice != nil && s.cfg != nil {
		s.cfg.Voice.Provider = patch.Voice.Provider
		s.cfg.Voice.Model = patch.Voice.Model
		s.cfg.Voice.BaseURL = patch.Voice.BaseURL
		s.cfg.Voice.SidecarURL = patch.Voice.SidecarURL
		s.cfg.Voice.Voice = patch.Voice.Voice
		if patch.Voice.Timeout != "" {
			s.cfg.Voice.Timeout = patch.Voice.Timeout
		}
		if patch.Voice.AllowRemote != nil {
			s.cfg.Voice.AllowRemote = *patch.Voice.AllowRemote
		}
	}

	s.log.Info("config updated via API", zap.String("path", s.cfgPath))
	s.recordAdminAudit(c, "config.patch", "config", s.cfgPath, "ok", map[string]any{
		"sections": configPatchSections(patch),
	})

	return c.JSON(fiber.Map{
		"ok":      true,
		"message": "Config saved. Restart the gateway for changes to take full effect.",
		"config":  s.safeConfigView(),
	})
}

// readRawConfig parses a YAML file into a generic map.
func readRawConfig(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// ReloadConfig reads config.yaml from disk and applies hot-reloadable changes
// to the running gateway (e.g. MCP servers).
func (s *Server) ReloadConfig() error {
	if s.cfgPath == "" {
		return nil
	}
	newCfg, _, err := config.Load(s.cfgPath)
	if err != nil {
		return err
	}

	s.cfg = newCfg

	// Hot-reload MCP servers
	if s.mcp != nil {
		current := s.mcp.ServersSnapshot()
		// Remove deleted servers
		for _, cur := range current {
			if _, exists := newCfg.MCP.Servers[cur.ID]; !exists {
				_ = s.mcp.RemoveServer(cur.ID)
			}
		}
		// Add/Update existing servers
		for id, srvCfg := range newCfg.MCP.Servers {
			_ = s.mcp.AddServer(id, mcp.ServerConfig{
				Transport: srvCfg.Transport,
				Command:   srvCfg.Command,
				Args:      srvCfg.Args,
				Env:       srvCfg.Env,
				URL:       srvCfg.URL,
				Headers:   srvCfg.Headers,
			})
		}
	}

	s.log.Info("config.yaml reloaded")
	return nil
}

// writeRawConfig safely writes a map back to a YAML file.
func writeRawConfig(path string, m map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func applyPatch(dst map[string]any, patch PatchableConfig) {
	if patch.Server != nil {
		srv := getOrCreateMap(dst, "server")
		if patch.Server.Host != "" {
			srv["host"] = patch.Server.Host
		}
		if patch.Server.Port != 0 {
			srv["port"] = patch.Server.Port
		}
		if patch.Server.GUIEnabled != nil {
			srv["gui_enabled"] = *patch.Server.GUIEnabled
		}
		if patch.Server.APIKey != "" && patch.Server.APIKey != "***" {
			srv["api_key"] = patch.Server.APIKey
		}
	}
	if patch.UI != nil {
		ui := getOrCreateMap(dst, "ui")
		if patch.UI.WalkthroughSeen != nil {
			ui["walkthrough_seen"] = *patch.UI.WalkthroughSeen
		}
		if patch.UI.WalkthroughStep != nil {
			ui["walkthrough_step"] = *patch.UI.WalkthroughStep
		}
		if patch.UI.WalkthroughVersion != nil {
			ui["walkthrough_version"] = *patch.UI.WalkthroughVersion
		}
	}
	if patch.Runtime != nil {
		rt := getOrCreateMap(dst, "runtime")
		if patch.Runtime.MaxConcurrentSessions != 0 {
			rt["max_concurrent_sessions"] = patch.Runtime.MaxConcurrentSessions
		}
		if patch.Runtime.DefaultMaxTurns != 0 {
			rt["default_max_turns"] = patch.Runtime.DefaultMaxTurns
		}
		if patch.Runtime.MaxAgentCallDepth != 0 {
			rt["max_agent_call_depth"] = patch.Runtime.MaxAgentCallDepth
		}
		if patch.Runtime.PythonBin != "" {
			rt["python_bin"] = patch.Runtime.PythonBin
		}
		if patch.Runtime.ToolTimeout != "" {
			rt["tool_timeout"] = patch.Runtime.ToolTimeout
		}
	}
	if patch.Executor != nil {
		ex := getOrCreateMap(dst, "executor")
		ex["backend"] = patch.Executor.Backend
		if patch.Executor.Workers != 0 {
			ex["workers"] = patch.Executor.Workers
		}
		ex["docker_image"] = patch.Executor.DockerImage
		ex["docker_network"] = patch.Executor.DockerNetwork
		ex["docker_volumes"] = patch.Executor.DockerVolumes
		ex["ssh_host"] = patch.Executor.SSHHost
		ex["ssh_user"] = patch.Executor.SSHUser
		ex["ssh_python_bin"] = patch.Executor.SSHPythonBin
		if patch.Executor.SSHIdentity != "" && patch.Executor.SSHIdentity != "***" {
			ex["ssh_identity"] = patch.Executor.SSHIdentity
		}
		ex["ssh_identity_credential"] = patch.Executor.SSHIdentityCredential
		ex["cloud_preset"] = patch.Executor.CloudPreset
		ex["cloud_target"] = patch.Executor.CloudTarget
		ex["cloud_cli"] = patch.Executor.CloudCLI
	}
	if patch.LLM != nil {
		llm := getOrCreateMap(dst, "llm")
		if patch.LLM.DefaultProvider != "" {
			llm["default_provider"] = patch.LLM.DefaultProvider
		}
		if patch.LLM.AllowedProviders != nil {
			llm["allowed_providers"] = patch.LLM.AllowedProviders
		}
		if patch.LLM.AllowedModels != nil {
			llm["allowed_models"] = patch.LLM.AllowedModels
		}
		if patch.LLM.Studio != nil {
			st := getOrCreateMap(llm, "studio")
			st["provider"] = patch.LLM.Studio.Provider
			st["model"] = patch.LLM.Studio.Model
			st["max_build_tokens"] = patch.LLM.Studio.MaxBuildTokens
			st["max_build_cost_usd"] = patch.LLM.Studio.MaxBuildCostUSD
		}
		if patch.LLM.Reasoner != nil {
			rs := getOrCreateMap(llm, "reasoner")
			rs["provider"] = patch.LLM.Reasoner.Provider
			rs["model"] = patch.LLM.Reasoner.Model
		}
	}
	if patch.Search != nil {
		sr := getOrCreateMap(dst, "search")
		if patch.Search.Provider != "" {
			sr["provider"] = patch.Search.Provider
		}
		// "***" is the redacted placeholder the GUI echoes back; never persist it.
		if patch.Search.APIKey != "" && patch.Search.APIKey != "***" {
			sr["api_key"] = patch.Search.APIKey
		}
	}
	if patch.Voice != nil {
		vc := getOrCreateMap(dst, "voice")
		vc["provider"] = patch.Voice.Provider
		vc["model"] = patch.Voice.Model
		vc["base_url"] = patch.Voice.BaseURL
		vc["sidecar_url"] = patch.Voice.SidecarURL
		vc["voice"] = patch.Voice.Voice
		if patch.Voice.Timeout != "" {
			vc["timeout"] = patch.Voice.Timeout
		}
		if patch.Voice.AllowRemote != nil {
			vc["allow_remote"] = *patch.Voice.AllowRemote
		}
	}
	if patch.Costs != nil {
		cs := getOrCreateMap(dst, "costs")
		cs["daily_budget_usd"] = patch.Costs.DailyBudgetUSD
		cs["monthly_budget_usd"] = patch.Costs.MonthlyBudgetUSD
		cs["per_user_daily_budget_usd"] = patch.Costs.PerUserDailyBudgetUSD
		cs["per_agent_daily_budget_usd"] = patch.Costs.PerAgentDailyBudgetUSD
		cs["alert_threshold"] = patch.Costs.AlertThreshold
		cs["enforcement_mode"] = patch.Costs.EnforcementMode
		cs["unknown_pricing"] = patch.Costs.UnknownPricing
		cs["default_max_output_tokens"] = patch.Costs.DefaultMaxOutputTokens
		cs["max_output_tokens_ceiling"] = patch.Costs.MaxOutputTokensCeiling
		cs["reservation_ttl"] = patch.Costs.ReservationTTL
		cs["max_concurrent_per_provider"] = patch.Costs.MaxConcurrentPerProvider
		cs["circuit_failure_threshold"] = patch.Costs.CircuitFailureThreshold
		cs["circuit_cooldown"] = patch.Costs.CircuitCooldown
		pricing := map[string]any{}
		for selector, price := range patch.Costs.Pricing {
			key := strings.TrimSpace(selector)
			if key == "" {
				continue
			}
			pricing[key] = map[string]any{
				"input_per_mtok":        price.InputPerMTok,
				"output_per_mtok":       price.OutputPerMTok,
				"cached_input_per_mtok": price.CachedInputPerMTok,
				"cache_write_per_mtok":  price.CacheWritePerMTok,
				"reasoning_per_mtok":    price.ReasoningPerMTok,
				"source":                price.Source,
				"effective_date":        price.EffectiveDate,
				"version":               price.Version,
			}
		}
		cs["pricing"] = pricing
	}
	if patch.Ops != nil {
		ops := getOrCreateMap(dst, "ops")
		ops["slo_window"] = patch.Ops.SLOWindow
		ops["max_failure_rate"] = patch.Ops.MaxFailureRate
		ops["max_incomplete_rate"] = patch.Ops.MaxIncompleteRate
		ops["max_p95_run_duration"] = patch.Ops.MaxP95RunDuration
		ops["min_runs_for_signal"] = patch.Ops.MinRunsForSignal
		ops["alert_channel"] = patch.Ops.AlertChannel
		ops["alert_to"] = patch.Ops.AlertTo
		ops["alert_min_status"] = patch.Ops.AlertMinStatus
	}
	if patch.Deployment != nil {
		dep := getOrCreateMap(dst, "deployment")
		dep["profile"] = patch.Deployment.Profile
		dep["owner"] = patch.Deployment.Owner
		dep["region"] = patch.Deployment.Region
		dep["notes"] = patch.Deployment.Notes
	}
	if patch.Security != nil {
		// F-Bridge — mirror the Log branch's empty-string preservation so a
		// bare PATCH (e.g. saving the Deployment section from the same GUI)
		// doesn't accidentally clobber the on-disk intent_gate value. The
		// empty string is the meaningful "unset, defer to per-agent" sentinel;
		// operators clear it by explicitly removing the key from the YAML.
		sec := getOrCreateMap(dst, "security")
		if patch.Security.IntentGate != "" {
			sec["intent_gate"] = patch.Security.IntentGate
		}
	}
	if patch.Log != nil {
		lg := getOrCreateMap(dst, "log")
		if patch.Log.Level != "" {
			lg["level"] = patch.Log.Level
		}
		if patch.Log.Format != "" {
			lg["format"] = patch.Log.Format
		}
		if patch.Log.File != "" {
			lg["file"] = patch.Log.File
		}
	}
	if len(patch.AgentDirs) > 0 {
		dst["agent_dirs"] = patch.AgentDirs
	}
	if len(patch.SkillDirs) > 0 {
		dst["skill_dirs"] = patch.SkillDirs
	}
	if len(patch.PluginsConfig) > 0 {
		pc := getOrCreateMap(dst, "plugins_config")
		for pluginID, settings := range patch.PluginsConfig {
			if settings == nil {
				delete(pc, pluginID)
				continue
			}
			cur, _ := pc[pluginID].(map[string]any)
			if cur == nil {
				cur = map[string]any{}
			}
			mergePluginSettings(cur, settings)
			pc[pluginID] = cur
		}
	}
}

// mergePluginSettings applies one plugin's settings patch (Story 18):
// JSON null deletes a key; "***" placeholders are skipped so the redacted
// GUI view can round-trip without clobbering real secrets on disk; nested
// maps merge recursively; everything else is set verbatim. Keys absent
// from the patch are preserved.
func mergePluginSettings(dst, patch map[string]any) {
	for k, v := range patch {
		switch vv := v.(type) {
		case nil:
			delete(dst, k)
		case string:
			if vv == "***" {
				continue // redaction placeholder — keep the on-disk value
			}
			dst[k] = vv
		case map[string]any:
			sub, _ := dst[k].(map[string]any)
			if sub == nil {
				sub = map[string]any{}
			}
			mergePluginSettings(sub, vv)
			dst[k] = sub
		default:
			dst[k] = v
		}
	}
}

func getOrCreateMap(parent map[string]any, key string) map[string]any {
	if v, ok := parent[key]; ok {
		if m, ok := v.(map[string]any); ok {
			return m
		}
	}
	m := map[string]any{}
	parent[key] = m
	return m
}

// ── GET /api/v1/logs ──────────────────────────────────────────────────────────

// handleGetLogs returns the last N lines from the configured log file.
// Query params: lines=500, filter=<substring>
func (s *Server) handleGetLogs(c *fiber.Ctx) error {
	logPath := s.cfg.Log.File
	if logPath == "" {
		return c.JSON(fiber.Map{
			"lines":  []string{},
			"source": "stdout",
			"note":   "Logging to stdout — set log.file in config to enable log file tailing.",
		})
	}

	maxLines := c.QueryInt("lines", 500)
	if maxLines <= 0 || maxLines > 5000 {
		maxLines = 500
	}
	filter := strings.ToLower(c.Query("filter", ""))

	lines, err := tailFile(logPath, maxLines, filter)
	if err != nil {
		if os.IsNotExist(err) {
			return c.JSON(fiber.Map{
				"lines":  []string{},
				"source": logPath,
				"note":   fmt.Sprintf("No logs yet — the file %s appears once the gateway writes its first entry.", logPath),
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to read log file: " + err.Error(),
		})
	}

	return c.JSON(fiber.Map{
		"lines":  lines,
		"source": logPath,
		"count":  len(lines),
	})
}

// tailFile reads the last maxLines from a file, optionally filtered by substring.
func tailFile(path string, maxLines int, filter string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var all []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1 MB line buffer
	for scanner.Scan() {
		line := scanner.Text()
		if filter == "" || strings.Contains(strings.ToLower(line), filter) {
			all = append(all, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if len(all) <= maxLines {
		return all, nil
	}
	return all[len(all)-maxLines:], nil
}
