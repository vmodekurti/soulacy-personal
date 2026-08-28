package gateway

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/credentials"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/requestctx"
	"github.com/soulacy/soulacy/internal/studio"
	"github.com/soulacy/soulacy/internal/workspacesettings"
	"github.com/soulacy/soulacy/sdk/llm"
	"github.com/soulacy/soulacy/sdk/registry"
)

func (s *Server) SetWorkspaceSettingsStore(store *workspacesettings.Store) {
	s.workspaceSettings = store
	s.configureWorkspaceSearchResolver()
	s.configureWorkspaceProviderResolver()
}

func (s *Server) configureWorkspaceProviderResolver() {
	if s == nil || s.llmRouter == nil || s.workspaceSettings == nil || s.credVault == nil {
		return
	}
	s.llmRouter.SetProviderResolver(func(ctx context.Context, providerID string) (llm.Provider, bool) {
		identity, ok := requestctx.From(ctx)
		if !ok || identity.WorkspaceID() == "" {
			return nil, false
		}
		settings, err := s.workspaceSettings.Get(ctx, identity.WorkspaceID())
		if err != nil {
			return nil, false
		}
		providerID = strings.ToLower(strings.TrimSpace(providerID))
		configured, overridden := settings.LLM.Providers[providerID]
		if !overridden {
			if _, inherited := s.config().LLM.Providers[providerID]; !inherited {
				return nil, false
			}
		}
		provider, err := s.workspaceProvider(ctx, identity.WorkspaceID(), providerID, configured)
		return provider, err == nil && provider != nil
	})
}

func (s *Server) configureWorkspaceSearchResolver() {
	if s == nil || s.engine == nil || s.workspaceSettings == nil || s.credVault == nil {
		return
	}
	s.engine.SetWorkspaceSearchResolver(func(ctx context.Context) (string, string, string, bool) {
		identity, ok := requestctx.From(ctx)
		if !ok {
			return "", "", "", false
		}
		settings, err := s.workspaceSettings.Get(ctx, identity.WorkspaceID())
		if err != nil {
			return "", "", "", false
		}
		key := ""
		if value, err := s.credVault.Get(ctx, identity.WorkspaceID(), workspacesettings.SecretNamespace, workspacesettings.SearchAPIKey); err == nil {
			key = string(value)
		}
		return settings.Search.Provider, key, settings.Search.Timeout, true
	})
}

func (s *Server) registerWorkspaceSettingsRoutes(api fiber.Router) {
	api.Get("/workspace/config", s.rbacMW(rbac.ResourceConfig, rbac.ActionRead), s.handleGetWorkspaceSettings)
	api.Patch("/workspace/config", s.rbacMW(rbac.ResourceConfig, rbac.ActionWrite),
		s.auditing("workspace.config_set", "workspace", "", s.handlePatchWorkspaceSettings))
	api.Get("/workspace/providers", s.rbacMW(rbac.ResourceProviders, rbac.ActionRead), s.handleListWorkspaceProviders)
	api.Get("/workspace/providers/doctor", s.rbacMW(rbac.ResourceProviders, rbac.ActionRead), s.handleWorkspaceProviderDoctor)
	api.Get("/workspace/providers/:id/models", s.rbacMW(rbac.ResourceProviders, rbac.ActionRead), s.handleListWorkspaceProviderModels)
	api.Post("/workspace/providers/:id/model", s.rbacMW(rbac.ResourceProviders, rbac.ActionWrite), s.handleSetWorkspaceProviderModel)
	api.Post("/workspace/providers/:id", s.rbacMW(rbac.ResourceProviders, rbac.ActionWrite),
		s.auditing("workspace.provider_set", "provider", "", s.handleSetWorkspaceProvider))
	api.Delete("/workspace/providers/:id", s.rbacMW(rbac.ResourceProviders, rbac.ActionWrite),
		s.auditing("workspace.provider_delete", "provider", "", s.handleDeleteWorkspaceProvider))
}

var knownWorkspaceProviders = []string{"nvidia", "openai", "anthropic", "google", "ollama", "groq", "mistral", "openrouter", "deepseek", "together"}

const workspaceProviderProbeTimeout = 15 * time.Second

func workspaceProviderMap(p workspacesettings.Provider, keySet, inherited bool) fiber.Map {
	return fiber.Map{"base_url": p.BaseURL, "model": p.Model, "keep_alive": p.KeepAlive,
		"options": p.Options, "prompt_caching": p.PromptCaching, "thinking_budget": p.ThinkingBudget,
		"safety_level": p.SafetyLevel, "extended_thinking": p.ExtendedThinking,
		"organization": p.Organization, "parallel_tool_calls": p.ParallelToolCalls,
		"api_key": keySet, "registered": true, "inherited": inherited}
}

func workspaceProviderFromDeployment(pcfg map[string]any) workspacesettings.Provider {
	baseURL, _ := pcfg["base_url"].(string)
	model, _ := pcfg["model"].(string)
	return workspacesettings.Provider{BaseURL: baseURL, Model: model}
}

func (s *Server) handleListWorkspaceProviders(c *fiber.Ctx) error {
	id, ok := identityForWorkspaceSettings(c)
	if !ok {
		return s.errMsg(c, fiber.StatusUnauthorized, "workspace identity is unavailable")
	}
	settings, err := s.workspaceSettings.Get(c.UserContext(), id.WorkspaceID())
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace providers could not be read")
	}
	out := map[string]fiber.Map{}
	for providerID, configured := range s.config().LLM.Providers {
		out[providerID] = workspaceProviderMap(workspaceProviderFromDeployment(providerCfgMap(configured)), configured.APIKey != "", true)
	}
	for providerID, configured := range settings.LLM.Providers {
		_, keyErr := s.credVault.Get(c.UserContext(), id.WorkspaceID(), workspacesettings.SecretNamespace, workspacesettings.ProviderAPIKey(providerID))
		inherited, hasInherited := s.config().LLM.Providers[providerID]
		out[providerID] = workspaceProviderMap(configured, keyErr == nil || (hasInherited && inherited.APIKey != ""), false)
	}
	return c.JSON(fiber.Map{"scope": "workspace", "providers": out, "default_provider": firstNonEmpty(settings.LLM.Default.Provider, s.config().LLM.DefaultProvider), "known": knownWorkspaceProviders, "registered": workspaceProviderKeys(out)})
}

func workspaceProviderKeys(in map[string]fiber.Map) []string {
	out := make([]string, 0, len(in))
	for key := range in {
		out = append(out, key)
	}
	return out
}

func (s *Server) workspaceProvider(ctx context.Context, workspaceID, providerID string, configured workspacesettings.Provider) (llm.Provider, error) {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	// A workspace override is an overlay, not a fork of the deployment
	// registration. This lets an owner choose a different model while still
	// using a deployment-approved shared endpoint/key; a workspace-owned key
	// always wins and is never returned by the API.
	configured, key := s.effectiveWorkspaceProvider(providerID, configured)
	if s.credVault != nil {
		if value, err := s.credVault.Get(ctx, workspaceID, workspacesettings.SecretNamespace, workspacesettings.ProviderAPIKey(providerID)); err == nil {
			key = string(value)
		}
	}
	m := map[string]any{"id": providerID, "base_url": configured.BaseURL, "api_key": key, "model": configured.Model,
		"keep_alive": configured.KeepAlive, "options": configured.Options, "prompt_caching": configured.PromptCaching,
		"thinking_budget": configured.ThinkingBudget, "safety_level": configured.SafetyLevel,
		"extended_thinking": configured.ExtendedThinking, "organization": configured.Organization,
		"parallel_tool_calls": configured.ParallelToolCalls}
	p, known, err := registry.NewProvider(providerID, m)
	if !known && configured.BaseURL != "" {
		p, _, err = registry.NewProvider("openai", m)
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Server) effectiveWorkspaceProvider(providerID string, configured workspacesettings.Provider) (workspacesettings.Provider, string) {
	key := ""
	if inherited, ok := s.config().LLM.Providers[providerID]; ok {
		if configured.BaseURL == "" {
			configured.BaseURL = inherited.BaseURL
		}
		if configured.Model == "" {
			configured.Model = inherited.Model
		}
		if configured.KeepAlive == "" {
			configured.KeepAlive = inherited.KeepAlive
		}
		if configured.Options == nil {
			configured.Options = inherited.Options
		}
		if configured.Organization == "" {
			configured.Organization = inherited.Organization
		}
		key = inherited.APIKey
	}
	return configured, key
}

// effectiveWorkspaceConfig projects the deployment configuration through the
// workspace's safe overrides. It is deliberately credential-free: callers use
// it for validation, defaults, and model selection while workspaceProvider is
// the only path allowed to resolve the tenant's encrypted API key.
func (s *Server) effectiveWorkspaceConfig(settings workspacesettings.Settings) *config.Config {
	base := s.config()
	out := *base
	out.LLM = base.LLM
	out.LLM.Providers = make(map[string]config.ProviderConfig, len(base.LLM.Providers)+len(settings.LLM.Providers))
	for id, provider := range base.LLM.Providers {
		provider.APIKey = ""
		out.LLM.Providers[id] = provider
	}
	for id, provider := range settings.LLM.Providers {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "" {
			continue
		}
		effective, _ := s.effectiveWorkspaceProvider(id, provider)
		pc := out.LLM.Providers[id]
		pc.BaseURL = effective.BaseURL
		pc.Model = effective.Model
		pc.KeepAlive = effective.KeepAlive
		pc.Options = effective.Options
		pc.PromptCaching = effective.PromptCaching
		pc.ThinkingBudget = effective.ThinkingBudget
		pc.SafetyLevel = effective.SafetyLevel
		pc.ExtendedThinking = effective.ExtendedThinking
		pc.Organization = effective.Organization
		pc.ParallelToolCalls = effective.ParallelToolCalls
		pc.APIKey = ""
		out.LLM.Providers[id] = pc
	}
	if value := strings.TrimSpace(settings.LLM.Default.Provider); value != "" {
		out.LLM.DefaultProvider = value
	}
	if value := strings.TrimSpace(settings.LLM.Studio.Provider); value != "" {
		out.LLM.Studio.Provider = value
	}
	if value := strings.TrimSpace(settings.LLM.Studio.Model); value != "" {
		out.LLM.Studio.Model = value
	}
	if value := strings.TrimSpace(settings.LLM.Reasoner.Provider); value != "" {
		out.LLM.Reasoner.Provider = value
	}
	if value := strings.TrimSpace(settings.LLM.Reasoner.Model); value != "" {
		out.LLM.Reasoner.Model = value
	}
	return &out
}

func (s *Server) workspaceProviderForRequest(c *fiber.Ctx) (llm.Provider, workspacesettings.Settings, error) {
	id, ok := identityForWorkspaceSettings(c)
	if !ok {
		return nil, workspacesettings.Settings{}, errors.New("workspace identity is unavailable")
	}
	settings, err := s.workspaceSettings.Get(c.UserContext(), id.WorkspaceID())
	if err != nil {
		return nil, settings, err
	}
	providerID := strings.ToLower(strings.TrimSpace(c.Params("id")))
	configured, ok := settings.LLM.Providers[providerID]
	if !ok {
		if _, exists := s.config().LLM.Providers[providerID]; exists {
			p, err := s.workspaceProvider(c.UserContext(), id.WorkspaceID(), providerID, workspacesettings.Provider{})
			return p, settings, err
		}
		return nil, settings, fmt.Errorf("provider %q is not configured for this workspace", providerID)
	}
	p, err := s.workspaceProvider(c.UserContext(), id.WorkspaceID(), providerID, configured)
	return p, settings, err
}

func (s *Server) handleListWorkspaceProviderModels(c *fiber.Ctx) error {
	p, settings, err := s.workspaceProviderForRequest(c)
	if err != nil || p == nil {
		return s.errMsg(c, fiber.StatusBadRequest, errorText(err, "provider unavailable"))
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), workspaceProviderProbeTimeout)
	defer cancel()
	models, err := p.Models(ctx)
	if err != nil {
		return s.providerErrJSON(c, fiber.StatusBadGateway, c.Params("id"), err)
	}
	selected := ""
	if configured, ok := settings.LLM.Providers[strings.ToLower(c.Params("id"))]; ok {
		selected = configured.Model
	}
	return c.JSON(fiber.Map{"models": models, "selected": selected})
}

func errorText(err error, fallback string) string {
	if err != nil {
		return err.Error()
	}
	return fallback
}

type workspaceProviderRequest struct {
	BaseURL           string         `json:"base_url"`
	APIKey            string         `json:"api_key"`
	Model             string         `json:"model"`
	KeepAlive         string         `json:"keep_alive"`
	Options           map[string]any `json:"options"`
	PromptCaching     *bool          `json:"prompt_caching"`
	ThinkingBudget    *int           `json:"thinking_budget"`
	SafetyLevel       *string        `json:"safety_level"`
	ExtendedThinking  *bool          `json:"extended_thinking"`
	Organization      *string        `json:"organization"`
	ParallelToolCalls *bool          `json:"parallel_tool_calls"`
}

func (s *Server) handleSetWorkspaceProvider(c *fiber.Ctx) error {
	id, ok := identityForWorkspaceSettings(c)
	if !ok {
		return s.errMsg(c, fiber.StatusUnauthorized, "workspace identity is unavailable")
	}
	providerID := strings.ToLower(strings.TrimSpace(c.Params("id")))
	if providerID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "provider id is required")
	}
	var req workspaceProviderRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body")
	}
	settings, err := s.workspaceSettings.Get(c.UserContext(), id.WorkspaceID())
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace settings could not be read")
	}
	if settings.LLM.Providers == nil {
		settings.LLM.Providers = map[string]workspacesettings.Provider{}
	}
	p := settings.LLM.Providers[providerID]
	if req.BaseURL != "" {
		p.BaseURL = strings.TrimSpace(req.BaseURL)
	}
	if req.Model != "" {
		p.Model = strings.TrimSpace(req.Model)
	}
	if req.KeepAlive != "" {
		p.KeepAlive = strings.TrimSpace(req.KeepAlive)
	}
	if req.Options != nil {
		p.Options = req.Options
	}
	if req.PromptCaching != nil {
		p.PromptCaching = *req.PromptCaching
	}
	if req.ThinkingBudget != nil {
		p.ThinkingBudget = *req.ThinkingBudget
	}
	if req.SafetyLevel != nil {
		p.SafetyLevel = strings.TrimSpace(*req.SafetyLevel)
	}
	if req.ExtendedThinking != nil {
		p.ExtendedThinking = *req.ExtendedThinking
	}
	if req.Organization != nil {
		p.Organization = strings.TrimSpace(*req.Organization)
	}
	if req.ParallelToolCalls != nil {
		p.ParallelToolCalls = req.ParallelToolCalls
	}
	if key := strings.TrimSpace(req.APIKey); key != "" && key != "***" {
		if s.credVault == nil {
			return s.errMsg(c, fiber.StatusServiceUnavailable, "credential vault is unavailable")
		}
		if err := s.credVault.Set(c.UserContext(), id.WorkspaceID(), workspacesettings.SecretNamespace, workspacesettings.ProviderAPIKey(providerID), []byte(key)); err != nil {
			return s.errMsg(c, fiber.StatusServiceUnavailable, "provider credential could not be saved")
		}
	}
	settings.LLM.Providers[providerID] = p
	if _, err := s.workspaceProvider(c.UserContext(), id.WorkspaceID(), providerID, p); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}
	if _, err := s.workspaceSettings.Set(c.UserContext(), id.WorkspaceID(), id.Subject(), settings); err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace provider could not be saved")
	}
	return c.JSON(fiber.Map{"ok": true, "message": "Saved for this workspace."})
}

func (s *Server) handleSetWorkspaceProviderModel(c *fiber.Ctx) error {
	var req struct {
		Model string `json:"model"`
	}
	if err := c.BodyParser(&req); err != nil || strings.TrimSpace(req.Model) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "model is required")
	}
	providerID := strings.ToLower(strings.TrimSpace(c.Params("id")))
	id, ok := identityForWorkspaceSettings(c)
	if !ok {
		return s.errMsg(c, fiber.StatusUnauthorized, "workspace identity is unavailable")
	}
	settings, err := s.workspaceSettings.Get(c.UserContext(), id.WorkspaceID())
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace settings could not be read")
	}
	if settings.LLM.Providers == nil {
		settings.LLM.Providers = map[string]workspacesettings.Provider{}
	}
	p := settings.LLM.Providers[providerID]
	model := strings.TrimSpace(req.Model)
	provider, err := s.workspaceProvider(c.UserContext(), id.WorkspaceID(), providerID, p)
	if err != nil || provider == nil {
		return s.errMsg(c, fiber.StatusBadRequest, errorText(err, "provider unavailable"))
	}
	if err := validateWorkspaceProviderModel(c.UserContext(), provider, model); err != nil {
		return s.errMsg(c, fiber.StatusUnprocessableEntity, err.Error())
	}
	p.Model = model
	settings.LLM.Providers[providerID] = p
	if _, err := s.workspaceSettings.Set(c.UserContext(), id.WorkspaceID(), id.Subject(), settings); err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace model could not be saved")
	}
	return c.JSON(fiber.Map{"ok": true, "model": p.Model, "message": "Model saved for this workspace."})
}

// validateWorkspaceProviderModel prevents a typo or stale catalog entry from
// becoming the workspace default. Some compatible providers do not implement
// model discovery; an unavailable catalog is therefore advisory, while a
// successful non-empty catalog is authoritative.
func validateWorkspaceProviderModel(ctx context.Context, provider llm.Provider, model string) error {
	probeCtx, cancel := context.WithTimeout(ctx, workspaceProviderProbeTimeout)
	defer cancel()
	models, err := provider.Models(probeCtx)
	if err != nil || len(models) == 0 {
		return nil
	}
	for _, candidate := range models {
		if strings.EqualFold(strings.TrimSpace(candidate), model) {
			return nil
		}
	}
	return fmt.Errorf("model %q is not available from this provider; choose a model returned by List models", model)
}

func (s *Server) handleDeleteWorkspaceProvider(c *fiber.Ctx) error {
	id, ok := identityForWorkspaceSettings(c)
	if !ok {
		return s.errMsg(c, fiber.StatusUnauthorized, "workspace identity is unavailable")
	}
	providerID := strings.ToLower(strings.TrimSpace(c.Params("id")))
	settings, err := s.workspaceSettings.Get(c.UserContext(), id.WorkspaceID())
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace settings could not be read")
	}
	delete(settings.LLM.Providers, providerID)
	if s.credVault != nil {
		_ = s.credVault.Delete(c.UserContext(), id.WorkspaceID(), workspacesettings.SecretNamespace, workspacesettings.ProviderAPIKey(providerID))
	}
	if _, err := s.workspaceSettings.Set(c.UserContext(), id.WorkspaceID(), id.Subject(), settings); err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace provider could not be deleted")
	}
	return c.JSON(fiber.Map{"ok": true, "message": "Workspace provider removed; the deployment provider remains unchanged."})
}

func (s *Server) handleWorkspaceProviderDoctor(c *fiber.Ctx) error {
	if _, ok := identityForWorkspaceSettings(c); !ok {
		return s.errMsg(c, fiber.StatusUnauthorized, "workspace identity is unavailable")
	}
	list, err := s.workspaceProviderDoctorChecks(c)
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace providers could not be read")
	}
	return c.JSON(fiber.Map{"providers": list, "vault": fiber.Map{"status": "ok", "detail": "Workspace credentials are isolated in the encrypted vault."}})
}

// workspaceProviderDoctorChecks reports provider readiness through the same
// tenant-aware resolver used by Chat, Studio, and agent execution. Workspace
// providers must never be copied into the process-global router merely to make
// readiness checks pass: doing so would mix credentials across tenants.
func (s *Server) workspaceProviderDoctorChecks(c *fiber.Ctx) ([]doctorProviderCheck, error) {
	id, ok := identityForWorkspaceSettings(c)
	if !ok {
		return nil, errors.New("workspace identity is unavailable")
	}
	if s.workspaceSettings == nil {
		return nil, errors.New("workspace settings are unavailable")
	}
	settings, err := s.workspaceSettings.Get(c.UserContext(), id.WorkspaceID())
	if err != nil {
		return nil, err
	}
	available := map[string]workspacesettings.Provider{}
	for providerID := range s.config().LLM.Providers {
		available[providerID] = workspacesettings.Provider{}
	}
	for providerID, configured := range settings.LLM.Providers {
		available[providerID] = configured
	}
	ids := make([]string, 0, len(available))
	for providerID := range available {
		ids = append(ids, providerID)
	}
	sort.Strings(ids)

	checks := make([]doctorProviderCheck, 0, len(ids))
	for _, providerID := range ids {
		configured := available[providerID]
		effective, inheritedKey := s.effectiveWorkspaceProvider(providerID, configured)
		keySource := "missing"
		key := inheritedKey
		if strings.TrimSpace(inheritedKey) != "" {
			keySource = "deployment"
		}
		if s.credVault != nil {
			if value, getErr := s.credVault.Get(c.UserContext(), id.WorkspaceID(), workspacesettings.SecretNamespace, workspacesettings.ProviderAPIKey(providerID)); getErr == nil {
				key = string(value)
				keySource = "workspace vault"
			}
		}
		local := studio.IsLocalProvider(providerID, effective.BaseURL)
		if local && strings.TrimSpace(key) == "" {
			keySource = "not required"
		}
		provider, resolveErr := s.workspaceProvider(c.UserContext(), id.WorkspaceID(), providerID, configured)
		check := doctorProviderCheck{
			ID: providerID, Registered: resolveErr == nil && provider != nil,
			KeySource: keySource, BaseURL: effective.BaseURL, Model: effective.Model,
			Status: "ok", Detail: "provider is available to this workspace",
		}
		switch {
		case !check.Registered:
			check.Status = "fail"
			check.Detail = "provider could not be constructed for this workspace"
			check.Remedy = "check the provider base URL and configuration"
		case strings.TrimSpace(effective.Model) == "":
			check.Status = "warn"
			check.Detail = "provider has no default model"
			check.Remedy = "select and save a default model"
		case !local && strings.TrimSpace(key) == "":
			check.Status = "fail"
			check.Detail = "remote provider has no workspace or deployment API key"
			check.Remedy = "edit this provider and add its API key"
		}
		if check.Status == "ok" && provider != nil {
			probeCtx, cancel := context.WithTimeout(c.UserContext(), workspaceProviderProbeTimeout)
			models, modelsErr := provider.Models(probeCtx)
			cancel()
			if modelsErr == nil && len(models) > 0 {
				modelFound := false
				for _, candidate := range models {
					if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(effective.Model)) {
						modelFound = true
						break
					}
				}
				if !modelFound {
					check.Status = "fail"
					check.Detail = fmt.Sprintf("configured model %q is not offered by this provider", effective.Model)
					check.Remedy = "use List models and save an available model"
				}
			}
		}
		checks = append(checks, check)
	}
	return checks, nil
}

type modelPatch struct {
	Provider *string `json:"provider"`
	Model    *string `json:"model"`
}
type studioPatch struct {
	Provider        *string  `json:"provider"`
	Model           *string  `json:"model"`
	Preset          *string  `json:"preset"`
	BuildUX         *string  `json:"build_ux"`
	MaxBuildTokens  *int     `json:"max_build_tokens"`
	MaxBuildCostUSD *float64 `json:"max_build_cost_usd"`
}
type workspaceSettingsPatch struct {
	LLM *struct {
		Default  *modelPatch  `json:"default"`
		Chat     *modelPatch  `json:"chat"`
		Studio   *studioPatch `json:"studio"`
		Reasoner *modelPatch  `json:"reasoner"`
	} `json:"llm"`
	Search *struct {
		Provider *string `json:"provider"`
		Timeout  *string `json:"timeout"`
		APIKey   *string `json:"api_key"`
	} `json:"search"`
	Costs    *workspacesettings.Costs    `json:"costs"`
	Ops      *workspacesettings.Ops      `json:"ops"`
	Profile  *workspacesettings.Profile  `json:"profile"`
	Security *workspacesettings.Security `json:"security"`
	Runtime  *workspacesettings.Runtime  `json:"runtime"`
}

func identityForWorkspaceSettings(c *fiber.Ctx) (requestctx.Identity, bool) {
	return requestctx.From(c.UserContext())
}

func (s *Server) workspaceSettingsFor(c *fiber.Ctx) workspacesettings.Settings {
	if s == nil || s.workspaceSettings == nil || c == nil {
		return workspacesettings.Settings{}
	}
	id, ok := identityForWorkspaceSettings(c)
	if !ok {
		return workspacesettings.Settings{}
	}
	settings, err := s.workspaceSettings.Get(c.UserContext(), id.WorkspaceID())
	if err != nil {
		return workspacesettings.Settings{}
	}
	return settings
}

// workspaceChatLLM resolves the workspace's chat choice without combining a
// model from one provider with another provider. An omitted chat model means
// "that provider's deployment-approved default", not "the workspace default
// model regardless of provider".
func (s *Server) workspaceChatLLM(c *fiber.Ctx) (string, string) {
	settings := s.workspaceSettingsFor(c)
	provider := firstNonEmpty(settings.LLM.Chat.Provider, settings.LLM.Default.Provider)
	model := settings.LLM.Chat.Model
	if model == "" && provider == settings.LLM.Default.Provider {
		model = settings.LLM.Default.Model
	}
	if model == "" {
		if configured, ok := s.config().LLM.Providers[provider]; ok {
			model = configured.Model
		}
	}
	return provider, model
}

func (s *Server) handleGetWorkspaceSettings(c *fiber.Ctx) error {
	id, ok := identityForWorkspaceSettings(c)
	if !ok {
		return s.errMsg(c, fiber.StatusUnauthorized, "workspace identity is unavailable")
	}
	if s.workspaceSettings == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace settings are unavailable")
	}
	settings, err := s.workspaceSettings.Get(c.UserContext(), id.WorkspaceID())
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace settings could not be read")
	}
	return s.workspaceSettingsResponse(c, settings)
}

func (s *Server) workspaceSettingsResponse(c *fiber.Ctx, settings workspacesettings.Settings) error {
	apiKeySet := false
	if s.credVault != nil {
		_, err := s.credVault.Get(c.UserContext(), settings.WorkspaceID, workspacesettings.SecretNamespace, workspacesettings.SearchAPIKey)
		apiKeySet = err == nil
	}
	providers := map[string]fiber.Map{}
	for id, provider := range s.config().LLM.Providers {
		providers[id] = fiber.Map{"model": provider.Model, "base_url": provider.BaseURL}
	}
	for id, provider := range settings.LLM.Providers {
		inherited := providers[id]
		if inherited == nil {
			inherited = fiber.Map{}
		}
		if provider.Model != "" {
			inherited["model"] = provider.Model
		}
		if provider.BaseURL != "" {
			inherited["base_url"] = provider.BaseURL
		}
		providers[id] = inherited
	}
	return c.JSON(fiber.Map{
		"scope": "workspace", "workspace_id": settings.WorkspaceID,
		"llm": fiber.Map{
			"default_provider": firstNonEmpty(settings.LLM.Default.Provider, s.config().LLM.DefaultProvider),
			"default":          settings.LLM.Default, "chat": settings.LLM.Chat,
			"studio": effectiveStudioSettings(settings, s), "reasoner": settings.LLM.Reasoner,
			"providers": providers,
		},
		"search":     fiber.Map{"provider": firstNonEmpty(settings.Search.Provider, s.config().Search.Provider), "timeout": firstNonEmpty(settings.Search.Timeout, s.config().Search.Timeout), "api_key_set": apiKeySet},
		"costs":      settings.Costs,
		"ops":        settings.Ops,
		"profile":    settings.Profile,
		"security":   settings.Security,
		"runtime":    settings.Runtime,
		"updated_by": settings.UpdatedBy, "updated_at": settings.UpdatedAt,
	})
}

func effectiveStudioSettings(settings workspacesettings.Settings, s *Server) workspacesettings.Studio {
	out := settings.LLM.Studio
	out.Provider = firstNonEmpty(out.Provider, s.config().LLM.Studio.Provider, settings.LLM.Default.Provider, s.config().LLM.DefaultProvider)
	if out.Model == "" {
		if p, ok := s.config().LLM.Providers[out.Provider]; ok {
			out.Model = p.Model
		}
		if out.Model == "" {
			out.Model = s.config().LLM.Studio.Model
		}
	}
	out.Preset = firstNonEmpty(out.Preset, s.config().LLM.Studio.Preset)
	out.BuildUX = firstNonEmpty(out.BuildUX, s.config().LLM.Studio.BuildUX)
	if out.MaxBuildTokens == 0 {
		out.MaxBuildTokens = s.config().LLM.Studio.MaxBuildTokens
	}
	if out.MaxBuildCostUSD == 0 {
		out.MaxBuildCostUSD = s.config().LLM.Studio.MaxBuildCostUSD
	}
	return out
}

func setModel(dst *workspacesettings.ProviderModel, patch *modelPatch) {
	if patch == nil {
		return
	}
	if patch.Provider != nil {
		dst.Provider = strings.TrimSpace(*patch.Provider)
	}
	if patch.Model != nil {
		dst.Model = strings.TrimSpace(*patch.Model)
	}
}

func (s *Server) validateWorkspaceModels(settings workspacesettings.Settings) error {
	models := []workspacesettings.ProviderModel{settings.LLM.Default, settings.LLM.Chat, settings.LLM.Studio.ProviderModel, settings.LLM.Reasoner}
	for _, selected := range models {
		if selected.Provider == "" {
			continue
		}
		_, deploymentProvider := s.config().LLM.Providers[selected.Provider]
		_, workspaceProvider := settings.LLM.Providers[selected.Provider]
		if !deploymentProvider && !workspaceProvider {
			return errors.New("provider " + selected.Provider + " is not enabled by this deployment")
		}
		if len(s.config().LLM.AllowedProviders) > 0 && !containsFolded(s.config().LLM.AllowedProviders, selected.Provider) {
			return errors.New("provider " + selected.Provider + " is not allowed by this deployment")
		}
		if selected.Model != "" && len(s.config().LLM.AllowedModels) > 0 && !containsFolded(s.config().LLM.AllowedModels, selected.Model) {
			return errors.New("model " + selected.Model + " is not allowed by this deployment")
		}
	}
	return nil
}

func containsFolded(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(wanted)) {
			return true
		}
	}
	return false
}

func (s *Server) handlePatchWorkspaceSettings(c *fiber.Ctx) error {
	id, ok := identityForWorkspaceSettings(c)
	if !ok {
		return s.errMsg(c, fiber.StatusUnauthorized, "workspace identity is unavailable")
	}
	if s.workspaceSettings == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace settings are unavailable")
	}
	current, err := s.workspaceSettings.Get(c.UserContext(), id.WorkspaceID())
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace settings could not be read")
	}
	var patch workspaceSettingsPatch
	if err := c.BodyParser(&patch); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body")
	}
	if patch.LLM != nil {
		setModel(&current.LLM.Default, patch.LLM.Default)
		setModel(&current.LLM.Chat, patch.LLM.Chat)
		setModel(&current.LLM.Reasoner, patch.LLM.Reasoner)
		if p := patch.LLM.Studio; p != nil {
			if p.Provider != nil {
				current.LLM.Studio.Provider = strings.TrimSpace(*p.Provider)
			}
			if p.Model != nil {
				current.LLM.Studio.Model = strings.TrimSpace(*p.Model)
			}
			if p.Preset != nil {
				current.LLM.Studio.Preset = strings.TrimSpace(*p.Preset)
			}
			if p.BuildUX != nil {
				current.LLM.Studio.BuildUX = strings.TrimSpace(*p.BuildUX)
			}
			if p.MaxBuildTokens != nil {
				current.LLM.Studio.MaxBuildTokens = *p.MaxBuildTokens
			}
			if p.MaxBuildCostUSD != nil {
				current.LLM.Studio.MaxBuildCostUSD = *p.MaxBuildCostUSD
			}
		}
	}
	if patch.Search != nil {
		if patch.Search.Provider != nil {
			current.Search.Provider = strings.ToLower(strings.TrimSpace(*patch.Search.Provider))
		}
		if patch.Search.Timeout != nil {
			current.Search.Timeout = strings.TrimSpace(*patch.Search.Timeout)
		}
		if patch.Search.APIKey != nil {
			if s.credVault == nil {
				return s.errMsg(c, fiber.StatusServiceUnavailable, "credential vault is unavailable")
			}
			value := strings.TrimSpace(*patch.Search.APIKey)
			if value == "" {
				err = s.credVault.Delete(c.UserContext(), id.WorkspaceID(), workspacesettings.SecretNamespace, workspacesettings.SearchAPIKey)
			} else if value != "***" {
				err = s.credVault.Set(c.UserContext(), id.WorkspaceID(), workspacesettings.SecretNamespace, workspacesettings.SearchAPIKey, []byte(value))
			}
			if err != nil && !errors.Is(err, credentials.ErrNotFound) {
				return s.errMsg(c, fiber.StatusServiceUnavailable, "search credential could not be saved")
			}
		}
	}
	if patch.Costs != nil {
		current.Costs = *patch.Costs
	}
	if patch.Ops != nil {
		current.Ops = *patch.Ops
	}
	if patch.Profile != nil {
		current.Profile = *patch.Profile
	}
	if patch.Security != nil {
		current.Security = *patch.Security
	}
	if patch.Runtime != nil {
		current.Runtime = *patch.Runtime
	}
	if err := s.validateWorkspaceModels(current); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}
	saved, err := s.workspaceSettings.Set(c.UserContext(), id.WorkspaceID(), id.Subject(), current)
	if err != nil {
		if errors.Is(err, workspacesettings.ErrInvalid) {
			return s.errMsg(c, fiber.StatusBadRequest, err.Error())
		}
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace settings could not be saved")
	}
	return s.workspaceSettingsResponse(c, saved)
}
