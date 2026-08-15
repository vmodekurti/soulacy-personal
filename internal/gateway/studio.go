// studio.go — HTTP handlers for the Studio visual builder (Story S1.1).
//
// As of ARCH-6 the Studio UI is a first-class page of the core dashboard
// (gui/src/pages/Studio.svelte), not a sandboxed plugin. These endpoints are
// registered under /api/v1/studio/* with the standard user RBAC (agent
// read/write) and are NOT in the plugin route allowlist, so scoped plugin
// tokens are rejected with 403 — the dashboard calls them with the user's
// own session.
//
// Route (under /api/v1, user-authenticated, same RBAC as agent writes):
//
//	POST /api/v1/studio/compile — turn a plain-language intent into a draft
//	                              workflow plus clarifying questions.
//
// The handler is thin: it parses the body, adapts the gateway's llm.Router
// to the narrow studio.LLM interface (reaching the model exactly like the
// rest of the gateway does — through s.llmRouter, with provider/model
// resolved from config.LLM), calls studio.Compile, and returns the Result
// as JSON.
package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/valyala/fasthttp"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/soulacy/soulacy/pkg/message"

	"github.com/soulacy/soulacy/internal/agentvalidate"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/costs"
	"github.com/soulacy/soulacy/internal/llm"
	reasoningpkg "github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/secrets"
	"github.com/soulacy/soulacy/internal/studio"
	"github.com/soulacy/soulacy/internal/studio/consent"
	"github.com/soulacy/soulacy/pkg/agent"
)

// routerLLM adapts the gateway's *llm.Router to studio.LLM. It routes to
// the configured default provider and resolves that provider's model from
// config, mirroring how the gateway otherwise reaches the LLM layer.
type routerLLM struct {
	router    *llm.Router
	provider  string
	model     string
	store     *costs.Store
	runID     string
	confirmed bool
}

func (a routerLLM) Usage() costs.UsageRecord {
	if a.store == nil {
		return costs.UsageRecord{}
	}
	usage, _ := a.store.TotalsByRun(context.Background(), a.runID)
	return usage
}

func (a routerLLM) Complete(ctx context.Context, prompt string) (string, error) {
	metadata := llm.CallMetadataFromContext(ctx)
	metadata.Source = "studio"
	metadata.RunID = a.runID
	metadata.CostConfirmed = metadata.CostConfirmed || a.confirmed
	ctx = llm.WithCallMetadata(ctx, metadata)
	resp, err := a.router.Complete(ctx, a.provider, llm.CompletionRequest{
		Model: a.model,
		Messages: []llm.ChatMessage{
			{Role: "user", Content: prompt},
		},
		ResponseFormat: "json",
	})
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

// CompleteSchema constrains the builder model's output to the supplied JSON
// Schema (studio.SchemaLLM). The schema is a strong GUIDE, not a strict
// contract — it intentionally allows freeform sub-objects — so JSONSchemaLenient
// keeps OpenAI in non-strict mode instead of rejecting it. Providers without
// schema support (Ollama) transparently fall back to JSON mode + post-validation.
func (a routerLLM) CompleteSchema(ctx context.Context, prompt string, schema map[string]any) (string, error) {
	metadata := llm.CallMetadataFromContext(ctx)
	metadata.Source = "studio"
	metadata.RunID = a.runID
	metadata.CostConfirmed = metadata.CostConfirmed || a.confirmed
	ctx = llm.WithCallMetadata(ctx, metadata)
	resp, err := a.router.Complete(ctx, a.provider, llm.CompletionRequest{
		Model: a.model,
		Messages: []llm.ChatMessage{
			{Role: "user", Content: prompt},
		},
		ResponseFormat:    "json_schema",
		JSONSchema:        schema,
		JSONSchemaLenient: true,
	})
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

// studioLLM builds the studio.LLM the compiler will use, wiring the default
// provider + model out of config. Returns nil when no router is available.
func (s *Server) studioLLM(request ...*fiber.Ctx) studio.LLM {
	if s.llmRouter == nil {
		return nil
	}
	provider, model := s.studioProviderModel()
	confirmed := false
	if len(request) > 0 && request[0] != nil {
		confirmed = isTruthy(request[0].Get("X-Soulacy-Cost-Confirmed"))
	}
	return routerLLM{router: s.llmRouter, provider: provider, model: model, store: s.costStore,
		runID: uuid.New().String(), confirmed: confirmed}
}

// studioProviderModel resolves the builder (llm.studio) provider + model the
// same way studioLLM wires it: honour the llm.studio override, fall back to the
// default provider when it's unset or unregistered, and fill the model from the
// provider config when unset. Shared by studioLLM and the model-advice endpoint.
func (s *Server) studioProviderModel() (provider, model string) {
	provider = strings.TrimSpace(s.cfg.LLM.Studio.Provider)
	if provider == "" {
		provider = s.cfg.LLM.DefaultProvider
	} else if _, ok := s.cfg.LLM.Providers[provider]; !ok {
		provider = s.cfg.LLM.DefaultProvider
	}
	model = strings.TrimSpace(s.cfg.LLM.Studio.Model)
	if model == "" {
		if pc, ok := s.cfg.LLM.Providers[provider]; ok {
			model = pc.Model
		}
	}
	return provider, model
}

// defaultAgentLLM resolves the RUNTIME default provider + model that a generated
// agent should run on. This is distinct from studioProviderModel (the builder
// model used to GENERATE the agent): the agent runs on the gateway's default
// provider, not necessarily the (possibly cloud) builder model.
func (s *Server) defaultAgentLLM() (provider, model string) {
	provider = strings.TrimSpace(s.cfg.LLM.DefaultProvider)
	if pc, ok := s.cfg.LLM.Providers[provider]; ok {
		model = strings.TrimSpace(pc.Model)
	}
	return provider, model
}

// studioDraftWithRuntimeLLM resolves the provider/model exactly as the runtime
// does before readiness checks or a throwaway Run Live definition is built.
// Drafts are allowed to omit either value and inherit workspace defaults; the
// preflight inventory is intentionally stricter, so passing the unresolved
// draft to it produced a false "choose a model" blocker even though the runtime
// had a usable default.
func (s *Server) studioDraftWithRuntimeLLM(draft studio.Draft) studio.Draft {
	if strings.TrimSpace(draft.LLM.Provider) == "" {
		draft.LLM.Provider = strings.TrimSpace(s.cfg.LLM.DefaultProvider)
	}
	if strings.TrimSpace(draft.LLM.Model) == "" {
		if pc, ok := s.cfg.LLM.Providers[draft.LLM.Provider]; ok {
			draft.LLM.Model = strings.TrimSpace(pc.Model)
		}
	}
	return draft
}

// providerRegistered reports whether the LLM router actually has this provider.
//
// Presence in cfg.LLM.Providers is NOT the same thing. A provider block with a
// missing API key, an unreachable base URL, or a failed init is configured but
// never registers with the router — so config says it exists and the runtime
// cannot serve a single request through it.
//
// ok=false means "we asked and it is not there". ok is only meaningful when a
// router exists to ask; callers use canCheckProviders for that.
func (s *Server) providerRegistered(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" || s.llmRouter == nil {
		return false
	}
	for _, p := range s.llmRouter.ProviderIDs() {
		if p == id {
			return true
		}
	}
	return false
}

// canCheckProviders reports whether provider registration can be verified at
// all. With no router (tests, degraded boot) an unverified provider must not be
// treated as a missing one — refusing to stamp anything would be a worse
// failure than stamping the configured default.
func (s *Server) canCheckProviders() bool { return s.llmRouter != nil }

// stampDefaultLLM makes a FRESHLY generated draft run on the workspace's
// configured runtime default. The Studio builder model is an authoring detail,
// never a runtime choice; even when that provider is registered, preserving it
// here leaks llm.studio.provider/model into SOUL.yaml and makes an agent fail on
// a model the operator never selected for execution.
//
// Both callers are generation endpoints. Existing saved agents bypass this
// function and therefore retain deliberate per-agent provider/model pins during
// edit/save round-trips.
func (s *Server) stampDefaultLLM(d *studio.Draft) {
	if d == nil {
		return
	}
	p, m := s.defaultAgentLLM()
	// With no router we cannot verify registration, but config is still more
	// authoritative than model-authored JSON for a fresh draft.
	if !s.canCheckProviders() {
		d.LLM.Provider = p
		d.LLM.Model = m
		return
	}

	if s.providerRegistered(p) {
		d.LLM.Provider = p
		d.LLM.Model = m
		return
	}

	// A stale default should not make every generated agent dead on arrival.
	// Choose a provider the live router actually registered, deterministically,
	// and pair it only with that provider's configured model.
	ids := append([]string(nil), s.llmRouter.ProviderIDs()...)
	sort.Strings(ids)
	if len(ids) > 0 {
		fallback := ids[0]
		d.LLM.Provider = fallback
		if pc, ok := s.cfg.LLM.Providers[fallback]; ok {
			d.LLM.Model = strings.TrimSpace(pc.Model)
		} else {
			d.LLM.Model = ""
		}
		return
	}
	d.LLM.Provider = ""
	d.LLM.Model = ""
}

// handleStudioModelAdvice implements GET /api/v1/studio/model-advice. Local-first
// pivot: it reports the builder model, whether it runs locally, a supportive
// (non-shaming) complexity note for small local models, whether using it would
// send the prompt off-box (cloud-escalation), and whether a stronger frontier
// model is configured and can be OFFERED as optional assistance (hybrid use).
func (s *Server) handleStudioModelAdvice(c *fiber.Ctx) error {
	provider, model := s.studioProviderModel()
	if !s.providerRegistered(provider) {
		// Provider not actually usable → advise as unconfigured (block).
		return c.JSON(studio.AssessModel("", "", ""))
	}
	baseURL := ""
	if pc, ok := s.cfg.LLM.Providers[provider]; ok {
		baseURL = pc.BaseURL
	}
	adv := studio.AssessModel(provider, model, baseURL)

	// Hybrid (the user's question): if the builder is local, surface whether a
	// stronger CLOUD provider is also configured + registered, so the UI can
	// offer it as optional assistance for complex builds — opt-in, never forced.
	if adv.Local {
		if fp := s.firstConfiguredCloudProvider(); fp != "" {
			adv.FrontierAvailable = true
			adv.FrontierProvider = fp
		}
	}
	return c.JSON(adv)
}

// firstConfiguredCloudProvider returns the name of a registered, configured
// cloud LLM provider (if any), so Studio can offer it as optional frontier
// assistance alongside a local builder. Deterministic order: registered IDs.
func (s *Server) firstConfiguredCloudProvider() string {
	if s.llmRouter == nil {
		return ""
	}
	for _, id := range s.llmRouter.ProviderIDs() {
		pc, ok := s.cfg.LLM.Providers[id]
		if !ok {
			continue
		}
		if !studio.IsLocalProvider(id, pc.BaseURL) {
			return id
		}
	}
	return ""
}

// groundCatalog overwrites the caller-supplied catalog's Skills and MCP fields
// with the REAL, live, authoritative server-side inventory (installed skills +
// connected MCP servers and their tools). Both the compile and the pre-compile
// refine pass call this so they see the same world the engine will, and so loose
// references map to actual capabilities instead of being invented. It mutates
// the passed catalog in place.
func (s *Server) groundCatalog(cat *studio.Catalog) {
	// Inject the authoring rulebook so the builder follows the same rules the
	// validator and AI fixer enforce.
	cat.Rules = s.soulRules()
	s.groundStrategyFit(cat)
	// Successful multi-tool runs are distilled into payload-free procedural
	// patterns. RawIntent is authoritative here; refine callers also ground with
	// their request intent because older clients may omit RawIntent.
	if strings.TrimSpace(cat.RawIntent) != "" {
		s.groundWorkflowPatterns(cat, cat.RawIntent)
	}
	// Installed skills (so "yahoo finance" maps to the real "yfinance").
	if s.skillLoader != nil {
		cat.Skills = cat.Skills[:0]
		for _, sk := range s.skillLoader.All() {
			if sk == nil || strings.TrimSpace(sk.Name) == "" {
				continue
			}
			cat.Skills = append(cat.Skills, studio.CatalogSkill{
				Name: sk.Name, Description: sk.Description,
			})
		}
	}

	// Connected MCP servers and their tools, grouped by server, order preserved
	// as snapshotMCPTools returns them. Use the FULL callable name
	// (mcp__<server>__<tool>) so a tool node's "tool" resolves and classifies as
	// MCP rather than a builtin.
	mcpBySrv := map[string]int{}
	cat.MCP = nil
	for _, mt := range s.snapshotMCPTools() {
		idx, ok := mcpBySrv[mt.Server]
		if !ok {
			idx = len(cat.MCP)
			mcpBySrv[mt.Server] = idx
			cat.MCP = append(cat.MCP, studio.CatalogMCPServer{Server: mt.Server})
		}
		cat.MCP[idx].Tools = append(cat.MCP[idx].Tools,
			studio.CatalogMCPTool{Name: mt.FullName, Description: mt.Description, Params: mt.Params})
	}

	// Configured output channels (Story #1): a channel is groundable when it is
	// always-on (http) or has been configured + enabled. Studio wires delivery
	// to one of these instead of inventing a channel name.
	cat.Channels = s.groundedChannels()

	// Knowledge bases (Story #7): expose the KBs the agent could draw on so the
	// compiler can attach a relevant one. Best-effort: empty when the knowledge
	// store is disabled.
	cat.KnowledgeBases = nil
	if s.engine != nil {
		if ksvc := s.engine.Knowledge(); ksvc != nil && ksvc.Store != nil {
			if kbs, err := ksvc.Store.ListKBs(); err == nil {
				for _, kb := range kbs {
					cat.KnowledgeBases = append(cat.KnowledgeBases, studio.CatalogKB{
						Name: kb.Name, Description: kb.Description,
					})
				}
			}
		}
	}
	// Run semantic retrieval only after the authoritative tool inventory and its
	// descriptions have been assembled.
	s.groundLessons(cat, cat.RawIntent)
}

func studioLearningOwner(c *fiber.Ctx) string {
	if claims := auth.ClaimsFromCtx(c); claims != nil {
		if subject := strings.TrimSpace(claims.Subject); subject != "" {
			return subject
		}
		if email := strings.TrimSpace(claims.Email); email != "" {
			return strings.ToLower(email)
		}
	}
	// Open/dev and static-key installations remain one workspace-local scope.
	return ""
}

// groundGenerationProfile stamps the compile request with the actual builder
// provider/model from server config. The GUI may send a compact catalog, but
// generation quality mode must be authoritative: it decides whether Studio uses
// compact-local guardrails and what confidence it reports back.
func (s *Server) groundGenerationProfile(cat *studio.Catalog, intent string) {
	if cat == nil {
		return
	}
	provider, model := s.studioProviderModel()
	baseURL := ""
	if pc, ok := s.cfg.LLM.Providers[provider]; ok {
		baseURL = pc.BaseURL
	}
	gp := studio.BuildGenerationProfile(provider, model, baseURL, intent, *cat)
	cat.Generation = &gp
}

// groundedChannels returns the output channels available to a workflow: the
// always-on ones plus any configured-and-enabled channel. Mirrors the
// enabled/configured logic in handleListChannels but distilled to the names
// Studio can wire delivery to.
func (s *Server) groundedChannels() []string {
	statuses := s.channels.Statuses()
	var out []string
	for _, spec := range channelSpecs {
		cfg := s.cfg.Channels[spec.ID]
		enabled := spec.Always
		if v, ok := cfg["enabled"].(bool); ok {
			enabled = v
		}
		if !enabled {
			continue
		}
		// Require some configuration for non-always channels (a token/bot), or
		// a registered live adapter, so we don't advertise an unusable channel.
		if !spec.Always {
			configured := false
			for _, f := range spec.Fields {
				if valuePresent(cfg[f.Key]) {
					configured = true
					break
				}
			}
			if _, registered := statuses[spec.ID]; !configured && !registered {
				continue
			}
		}
		out = append(out, spec.ID)
	}
	return out
}

// studioRefinePromptRequest is the POST /api/v1/studio/refine-prompt body.
type studioRefinePromptRequest struct {
	Intent  string         `json:"intent"`
	Catalog studio.Catalog `json:"catalog,omitempty"`
	// Light requests a touch-up pass instead of a full rewrite. The UI sets it
	// when re-generating from an already-refined, user-edited prompt so the LLM
	// only cleans up the edits rather than re-refining the whole specification.
	Light bool `json:"light,omitempty"`
}

// handleStudioRefinePrompt implements POST /api/v1/studio/refine-prompt. It is
// the mandatory pre-generation step: it turns the user's rough intent into a
// clear, complete specification plus the assumptions it made and any clarifying
// questions, which the UI shows for confirmation BEFORE a workflow is compiled.
func (s *Server) handleStudioRefinePrompt(c *fiber.Ctx) error {
	var req studioRefinePromptRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if strings.TrimSpace(req.Intent) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "intent is required")
	}
	model := s.studioLLM(c)
	if model == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "LLM router unavailable")
	}
	s.groundCatalog(&req.Catalog)
	s.groundPreferencesFor(&req.Catalog, studioLearningOwner(c))
	s.groundLessons(&req.Catalog, req.Intent)
	s.groundWorkflowPatterns(&req.Catalog, req.Intent)

	refine := studio.RefinePrompt
	if req.Light {
		refine = studio.LightRefinePrompt
	}
	res, err := refine(c.Context(), model, req.Intent, req.Catalog)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.JSON(res)
}

// handleStudioStrategyFit exposes only aggregate compatibility evidence for a
// model. It powers warning icons in Studio without exposing run content.
func (s *Server) handleStudioStrategyFit(c *fiber.Ctx) error {
	provider := strings.TrimSpace(c.Query("provider"))
	model := strings.TrimSpace(c.Query("model"))
	if model == "" {
		provider, model = s.defaultAgentLLM()
	}
	rows := []studio.StrategyFit{}
	if store := s.strategyFitStore(); store != nil {
		rows = store.ForProviderModel(provider, model)
	}
	return c.JSON(fiber.Map{
		"provider": provider, "model": model, "strategies": rows,
		"min_runs": studio.StrategyFitMinRuns, "success_threshold": studio.StrategyFitSuccessThreshold,
	})
}

// studioPreflightRequest is the POST /api/v1/studio/preflight body.
type studioPreflightRequest struct {
	Workflow studio.Draft `json:"workflow"`
}

// handleStudioPreflight implements POST /api/v1/studio/preflight. It runs the
// consolidated pre-save validation (Stories #11/#12): missing tools/agents,
// disconnected MCP servers, empty required tool arguments, invalid schedules,
// and unconfigured channels — assembled against authoritative server-side state
// and returned as a single blockers/warnings report.
func (s *Server) handleStudioPreflight(c *fiber.Ctx) error {
	var req studioPreflightRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}

	// Ground a fresh catalog so tool/MCP/channel references are checked against
	// the real, live inventory rather than whatever the GUI happened to send.
	cat := s.studioCatalogSnapshot()
	s.groundCatalog(&cat)
	learningOwner := studioLearningOwner(c)
	s.groundPreferencesFor(&cat, learningOwner)

	res := studio.Preflight(req.Workflow, s.preflightInput(c, cat))
	return c.JSON(res)
}

// handleStudioContract implements POST /api/v1/studio/contract. It returns the
// platform-wide generation contract used by Build Until Works: fixed-graph
// validation, live runtime readiness, and Studio authoring-rule hygiene.
func (s *Server) handleStudioContract(c *fiber.Ctx) error {
	var req studioPreflightRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}

	cat := s.studioCatalogSnapshot()
	s.groundCatalog(&cat)

	// Story 2b (Cohort C): when the draft carries an existing agent id, look
	// up the saved Definition and pass it to AssessContract so the
	// security / persona / builtin-scope checks (which need fields Draft
	// doesn't round-trip) can run. Unsaved / new drafts skip them cleanly.
	opts := []studio.ContractOption{}
	if id := strings.TrimSpace(req.Workflow.ID); id != "" && s.loader != nil {
		if def := s.loader.Get(id); def != nil {
			opts = append(opts, studio.WithAgentDefinition(def))
		}
	}
	res := studio.AssessContract(req.Workflow, cat, s.preflightInput(c, cat), opts...)
	return c.JSON(res)
}

// handleStudioSecurityReview implements POST /api/v1/studio/security_review.
// S6 (Cohort F) — deterministic, no I/O — surfaces the security shape of a
// draft so Studio can render the pre-save modal: content trust boundaries,
// network / file / channel access, privileged tools, confirmation gates,
// and safer-scoped-tool recommendations.
func (s *Server) handleStudioSecurityReview(c *fiber.Ctx) error {
	var req studioPreflightRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	var def *agent.Definition
	if id := strings.TrimSpace(req.Workflow.ID); id != "" && s.loader != nil {
		def = s.loader.Get(id)
	}
	// F-Bridge — thread the workspace-scoped intent-gate default through so
	// the review summary matches the effective runtime mode.
	return c.JSON(studio.SecurityPreflight(req.Workflow, def, s.workspaceIntentGateDefault()))
}

// preflightInput assembles the live-state PreflightInput from a grounded
// catalog: connected MCP servers, configured channels, and stored secrets.
// Shared by the preflight endpoint and the compile handler (which surfaces the
// result as the explanation's NeedsConfig).
func (s *Server) preflightInput(c *fiber.Ctx, cat studio.Catalog) studio.PreflightInput {
	in := studio.PreflightInput{
		Catalog:            cat,
		ConnectedMCP:       s.connectedMCPSet(),
		ChannelsConfigured: s.configuredChannelSet(),
	}
	if mgr := secrets.New(s.CredentialVault()); mgr.Enabled() {
		set := map[string]bool{}
		for _, d := range mgr.Catalog(c.Context(), s.cfg) {
			set[d.Name] = d.Set
		}
		in.SecretsSet = set
	}
	// Provider/model availability, judged by REGISTRATION rather than by config
	// presence. Preflight already knew how to report an unusable provider; it was
	// simply never given the data, so an agent could be reported ready while its
	// runtime provider had never registered and could not serve a request.
	//
	// Left nil when there is no router to ask: nil means "unknown" and emits no
	// verdict, which is right — claiming every provider is missing because we
	// could not check would be a confident lie.
	if s.canCheckProviders() {
		providers := map[string]bool{}
		models := map[string]bool{}
		// Everything the config names starts as unavailable, so a configured but
		// unregistered provider is reported rather than simply absent from the map.
		for id := range s.cfg.LLM.Providers {
			providers[id] = false
		}
		for _, id := range s.llmRouter.ProviderIDs() {
			providers[id] = true
			if pc, ok := s.cfg.LLM.Providers[id]; ok && strings.TrimSpace(pc.Model) != "" {
				models[pc.Model] = true
				models[id+"/"+pc.Model] = true
			}
		}
		in.ProvidersAvailable = providers
		in.ModelsAvailable = models
	}
	return in
}

// studioCatalogSnapshot builds the agents/tools/providers portion of the
// catalog from authoritative live state (the agent loader, the unified tool
// catalog, and the LLM router). groundCatalog then fills Skills/MCP/Channels/
// KBs. Used by preflight (no GUI-supplied catalog) and reusable elsewhere.
func (s *Server) studioCatalogSnapshot() studio.Catalog {
	var cat studio.Catalog
	if s.loader != nil {
		for _, d := range s.loader.All() {
			if d == nil || strings.TrimSpace(d.ID) == "" {
				continue
			}
			cat.Agents = append(cat.Agents, d.ID)
		}
	}
	tc := s.toolCatalog()
	for _, b := range tc.Builtins {
		if strings.TrimSpace(b.Name) != "" {
			cat.Tools = append(cat.Tools, b.Name)
		}
	}
	for _, p := range tc.PythonTools {
		if strings.TrimSpace(p.Name) != "" {
			cat.Tools = append(cat.Tools, p.Name)
		}
	}
	if s.llmRouter != nil {
		cat.Providers = append(cat.Providers, s.llmRouter.ProviderIDs()...)
	}
	return cat
}

// connectedMCPSet returns the set of currently connected MCP server names.
func (s *Server) connectedMCPSet() map[string]bool {
	set := map[string]bool{}
	for _, mt := range s.snapshotMCPTools() {
		set[mt.Server] = true
	}
	return set
}

// configuredChannelSet returns channel id → configured+enabled, lowercased.
func (s *Server) configuredChannelSet() map[string]bool {
	set := map[string]bool{}
	for _, id := range s.groundedChannels() {
		set[strings.ToLower(id)] = true
	}
	return set
}

// studioAutowireRequest is the POST /api/v1/studio/autowire body.
type studioAutowireRequest struct {
	Workflow studio.Draft `json:"workflow"`
}

// handleStudioAutowire implements POST /api/v1/studio/autowire. It runs the
// deterministic data-flow repair over a draft (fill empty required tool args +
// reconcile dangling {{ .var }} references to the right upstream output) and
// returns the repaired workflow plus the number of fixes. This lets the GUI
// offer "Fix automatically" on a draft that was loaded or edited outside a
// fresh compile (where the repair already runs). No LLM call.
func (s *Server) handleStudioAutowire(c *fiber.Ctx) error {
	var req studioAutowireRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	cat := s.studioCatalogSnapshot()
	s.groundCatalog(&cat)
	in := s.preflightInput(c, cat)
	model := s.studioLLM(c)

	// 1) Contract-driven structural repair. Pre-save "Fix automatically" must
	//    be able to fix architecture blockers (empty graph, invalid graph,
	//    oversized brittle graph), not only small wiring mistakes.
	fixed := studio.RepairWiring(&req.Workflow, cat)
	contract := studio.AssessContract(req.Workflow, cat, in)
	if studio.RepairContractStructure(&req.Workflow, studioAutowireIntent(req.Workflow), cat, nil, contract) {
		studio.RepairWiring(&req.Workflow, cat)
		fixed++
	}

	// 2) Deterministic repair (auto-wire empty args, reconcile dangling vars,
	//    collapse template typos).
	fixed += studio.RepairWiring(&req.Workflow, cat)

	// 3) Iterative LLM repair over the FULL preflight report: validate, hand the
	//    model EVERY remaining blocker (template/wiring/MCP/python/etc.), apply
	//    its corrected draft, re-run deterministic passes, and re-validate. Up to
	//    a few rounds so it converges instead of fixing one thing at a time.
	if model != nil {
		for round := 0; round < 3; round++ {
			pf := studio.Preflight(req.Workflow, in)
			if pf.OK || len(pf.Blockers) == 0 {
				break
			}
			problems := make([]string, 0, len(pf.Blockers))
			for _, b := range pf.Blockers {
				problems = append(problems, studioProblemLine(b))
			}
			repaired, changed := studio.RepairWithProblems(c.Context(), model, req.Workflow, problems, cat)
			if !changed {
				break // model couldn't improve it; stop rather than loop pointlessly
			}
			studio.RepairWiring(&repaired, cat)
			req.Workflow = repaired
			fixed++
		}
	}

	final := studio.Preflight(req.Workflow, in)
	return c.JSON(fiber.Map{"workflow": req.Workflow, "fixed": fixed, "preflight": final})
}

func studioAutowireIntent(d studio.Draft) string {
	if s := strings.TrimSpace(d.Intent); s != "" {
		return s
	}
	if s := strings.TrimSpace(d.RawIntent); s != "" {
		return s
	}
	return strings.TrimSpace(d.Name)
}

// studioProblemLine renders a preflight issue as a single repair instruction
// (message + the node it applies to + the suggested fix).
func studioProblemLine(i studio.PreflightIssue) string {
	out := i.Message
	if i.NodeID != "" {
		out = "node \"" + i.NodeID + "\": " + out
	}
	if i.Fix != "" {
		out += " (" + i.Fix + ")"
	}
	return out
}

// studioTroubleshootRequest is the POST /api/v1/studio/troubleshoot body: a
// draft plus a runtime error message to fix.
type studioTroubleshootRequest struct {
	Workflow studio.Draft `json:"workflow"`
	Error    string       `json:"error"`
	Input    string       `json:"input,omitempty"`
	Evidence string       `json:"evidence,omitempty"`
}

// handleStudioTroubleshoot implements POST /api/v1/studio/troubleshoot. Given a
// draft and a RUNTIME error (e.g. from a failed scheduled run), it asks the
// model to fix the draft so that error won't recur, runs the deterministic
// passes, and returns the corrected draft + a fresh preflight. This is the
// "Fix with AI" loop for run-time failures, not just pre-save validation.
func (s *Server) handleStudioTroubleshoot(c *fiber.Ctx) error {
	var req studioTroubleshootRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if strings.TrimSpace(req.Error) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "error message is required")
	}
	model := s.studioLLM(c)
	if model == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "LLM router unavailable")
	}
	cat := s.studioCatalogSnapshot()
	s.groundCatalog(&cat)
	problem := "At RUN TIME the agent failed with this error — change the workflow so it cannot happen again: " + strings.TrimSpace(req.Error)
	if strings.TrimSpace(req.Input) != "" {
		problem += "\nSample input that triggered it: " + strings.TrimSpace(req.Input)
	}
	if strings.TrimSpace(req.Evidence) != "" {
		problem += "\nObserved run evidence:\n" + strings.TrimSpace(req.Evidence)
	}
	problems := []string{problem}
	repaired, changed := studio.RepairWithProblems(c.Context(), model, req.Workflow, problems, cat)
	studio.RepairWiring(&repaired, cat)
	pf := studio.Preflight(repaired, s.preflightInput(c, cat))
	return c.JSON(fiber.Map{"workflow": repaired, "changed": changed, "preflight": pf})
}

// studioTraceStore lazily initialises the bounded build-trace store. Disk
// persistence is enabled when SOULACY_STUDIO_TRACE_DIR names a writable
// directory; otherwise the store is in-memory only (still fully readable via the
// build-trace endpoints, just not durable across restarts).
func (s *Server) studioTraceStore() *studio.BuildTraceStore {
	s.buildTracesOnce.Do(func() {
		s.buildTraces = studio.NewBuildTraceStore(50, os.Getenv("SOULACY_STUDIO_TRACE_DIR"))
	})
	return s.buildTraces
}

// handleStudioBuildTrace implements GET /api/v1/studio/build-trace. With ?id it
// returns that build's full structured trace; without, the most recent build.
// The trace is the durable record of every phase the autonomous loop ran —
// snapshots, preflight, each repair, each verify — with timings and detail, so a
// build that failed (or a 6am scheduled one) is debuggable without server logs.
func (s *Server) handleStudioBuildTrace(c *fiber.Ctx) error {
	st := s.studioTraceStore()
	id := strings.TrimSpace(c.Query("id"))
	var (
		tr *studio.BuildTrace
		ok bool
	)
	if id != "" {
		tr, ok = st.Get(id)
	} else {
		tr, ok = st.Latest()
	}
	if !ok {
		return c.JSON(studio.TraceDump{ID: id, Events: []studio.TraceEvent{}})
	}
	return c.JSON(tr.Dump())
}

// handleStudioBuildTraces implements GET /api/v1/studio/build-traces — compact
// summaries of retained builds (newest first) for a "recent builds" picker.
func (s *Server) handleStudioBuildTraces(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"traces": s.studioTraceStore().List(), "dir": s.studioTraceStore().Dir()})
}

// studioBuildRequest is the POST /api/v1/studio/build body: the current draft to
// make work, plus the originating intent (used to synthesize self-tests).
type studioBuildRequest struct {
	Workflow studio.Draft `json:"workflow"`
	Intent   string       `json:"intent,omitempty"`
	// Verify is the LEGACY flag, kept so existing clients keep working. It no
	// longer defaults to true: `verify:true` now means "opt in to real side
	// effects", anything else (including absent) means mocked. See
	// sideEffectPolicy for why the default flipped.
	Verify *bool `json:"verify,omitempty"`
	// SideEffects states the intent explicitly on the wire ("mocked" | "real")
	// and WINS over Verify. A build loop repairs a draft that is, by definition,
	// not yet known to be correct — running it for real can post to a channel,
	// write a file or file a ticket once PER ATTEMPT. So the wire protocol should
	// say out loud which world the build is allowed to touch instead of encoding
	// it in a boolean whose name ("verify") does not mention side effects at all.
	SideEffects string `json:"side_effects,omitempty"`
}

// sideEffectPolicy resolves the build's side-effect policy from the request.
//
// The failure mode this prevents: a client that omits both fields used to get
// REAL execution — a half-built agent could spam production during its own
// repair loop. The default is now mocked, and real execution requires an
// explicit, named opt-in. Precedence: side_effects (explicit) > verify (legacy)
// > mocked (safe default).
func (r studioBuildRequest) sideEffectPolicy() studio.SideEffectPolicy {
	switch strings.ToLower(strings.TrimSpace(r.SideEffects)) {
	case string(studio.SideEffectsReal):
		return studio.SideEffectsReal
	case string(studio.SideEffectsMocked):
		return studio.SideEffectsMocked
	}
	if r.Verify != nil && *r.Verify {
		return studio.SideEffectsReal
	}
	return studio.SideEffectsMocked
}

// studioBuildBudget returns the report fields the GUI needs to explain HOW a
// build ended — "we ran out of time" reads identically to "we couldn't fix it"
// without StoppedReason, and an operator can't judge a build's cost without the
// elapsed/token/spend totals.
func studioBuildBudget(rep studio.BuildReport) fiber.Map {
	return fiber.Map{
		"stopped_reason": rep.StoppedReason,
		"side_effects":   rep.SideEffects,
		"elapsed_ms":     rep.Elapsed.Milliseconds(),
		"tokens_used":    rep.TokensUsed,
		"cost_usd":       rep.CostUSD,
	}
}

// handleStudioBuild implements POST /api/v1/studio/build — the Architect's
// autonomous build-verify-repair loop. It (1) fills capability holes with
// generated glue code, (2) synthesizes self-tests from the intent, (3) drives
// studio.BuildUntilWorks with a REAL-execution verifier backed by the engine
// (tool + Python steps run for real), repairing every blocker and every runtime
// error it hits until the agent works or it exhausts its budget, and (4) returns
// the final draft plus a full, transparent attempt transcript.
func (s *Server) handleStudioBuild(c *fiber.Ctx) error {
	var req studioBuildRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	model := s.studioLLM(c)
	if model == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "LLM router unavailable")
	}
	cat := s.studioCatalogSnapshot()
	s.groundCatalog(&cat)
	in := s.preflightInput(c, cat)

	intent := strings.TrimSpace(req.Intent)
	if intent == "" {
		intent = req.Workflow.Intent
	}

	// Open a durable build trace covering the WHOLE flow — glue, self-tests, and
	// the loop — so every build is debuggable end to end.
	tr := s.studioTraceStore().New(intent)
	defer func() { _ = tr.Close() }()

	// (1) Fill capability holes with generated glue code before the loop.
	var glueNotes []string
	doneGlue := tr.Step("phase", "glue", 0, "filling capability gaps with generated glue")
	if _, notes := studio.EnsureCapabilities(c.Context(), model, &req.Workflow, cat); len(notes) > 0 {
		glueNotes = notes
	}
	doneGlue(nil, map[string]any{"notes": glueNotes})

	// (2) Synthesize self-tests so "it works" is checked, not assumed.
	doneTests := tr.Step("phase", "tests", 0, "synthesizing self-tests from the intent")
	tests := studio.SynthesizeTests(c.Context(), model, intent, req.Workflow, cat)
	doneTests(nil, map[string]any{"count": len(tests)})

	// (3) Choose the verifier from the side-effect POLICY, never from a bare
	// boolean at this call site. Default is MOCKED: a build that was never asked
	// to touch the real world must not, and studio.VerifierFor is the single
	// place that decision is made.
	policy := req.sideEffectPolicy()
	opts := studio.BuildOptions{
		In: in, Tests: tests, Trace: tr, ExtraProblems: s.pythonBuildProblems,
		SideEffects: policy,
		Verifier:    studio.VerifierFor(policy, s.studioRealRunner()),
		// Bound the whole loop explicitly. Under SideEffectsReal an unbounded
		// "still making progress" loop would hold a live tool connection and an
		// LLM budget open indefinitely.
		MaxElapsed: studio.DefaultMaxElapsed,
		MaxTokens:  s.cfg.LLM.Studio.MaxBuildTokens,
		MaxCostUSD: s.cfg.LLM.Studio.MaxBuildCostUSD,
	}

	rep := studio.BuildUntilWorks(c.Context(), model, req.Workflow, cat, opts)

	final := studio.Preflight(rep.Workflow, in)
	out := fiber.Map{
		"report":    rep,
		"preflight": final,
		"glue":      glueNotes,
		"traceId":   tr.ID,
	}
	for k, v := range studioBuildBudget(rep) {
		out[k] = v
	}
	return c.JSON(out)
}

// handleStudioBuildStream is the streaming variant of /studio/build. It runs the
// same autonomous loop but emits a text/event-stream so the GUI shows live
// progress (each attempt starting, what's being repaired, when it's running,
// the outcome) instead of a frozen "Building…". The stream ends with an
// `event: done` frame carrying the full {report, preflight, glue} payload.
func (s *Server) handleStudioBuildStream(c *fiber.Ctx) error {
	var req studioBuildRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	model := s.studioLLM(c)
	if model == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "LLM router unavailable")
	}
	cat := s.studioCatalogSnapshot()
	s.groundCatalog(&cat)
	in := s.preflightInput(c, cat)
	// Detach from the request context so the loop isn't cancelled when the
	// handler returns to take over the connection as a stream writer.
	ctx := detachedRequestContext(c)

	// Events are produced by the loop (in a goroutine) and drained by the SSE
	// writer. Buffered so the loop never blocks on a slow client.
	type sse struct{ event, data string }
	events := make(chan sse, 64)

	intent := strings.TrimSpace(req.Intent)
	if intent == "" {
		intent = req.Workflow.Intent
	}
	// Durable trace for the whole streamed build (glue → tests → loop).
	tr := s.studioTraceStore().New(intent)

	// Heartbeat: during a long verify step the build can legitimately produce no
	// events for minutes (it's running the agent against a real model + tools).
	// A periodic keepalive frame keeps the connection visibly alive end-to-end so
	// no intermediary (or browser) treats the idle stream as dead. The frame is a
	// harmless `{"kind":"ping"}` with no message — the client's parser ignores it
	// (non-'done', no .message). hbStop + WaitGroup guarantee the heartbeat has
	// fully stopped BEFORE the producer closes `events`, so there is no send on a
	// closed channel.
	hbStop := make(chan struct{})
	var hbWG sync.WaitGroup
	hbWG.Add(1)
	go func() {
		defer hbWG.Done()
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-hbStop:
				return
			case <-t.C:
				select {
				case events <- sse{event: "event", data: `{"kind":"ping"}`}:
				case <-hbStop:
					return
				default: // buffer full → a real event is already in flight; skip
				}
			}
		}
	}()

	go func() {
		// Order matters: close(events) is registered first so it runs LAST —
		// after the heartbeat goroutine has been stopped and joined.
		defer close(events)
		defer func() { close(hbStop); hbWG.Wait() }()
		defer func() { _ = tr.Close() }()
		emit := func(kind, data string) {
			select {
			case events <- sse{event: kind, data: data}:
			default: // drop if the client is gone / buffer full
			}
		}
		// (1) Glue, then (2) self-tests — reported as their own steps.
		doneGlue := tr.Step("phase", "glue", 0, "filling capability gaps with generated glue")
		var glueNotes []string
		if _, notes := studio.EnsureCapabilities(ctx, model, &req.Workflow, cat); len(notes) > 0 {
			glueNotes = notes
			for _, nt := range notes {
				emit("event", jsonMsg("glue", "🧩 "+nt))
			}
		}
		doneGlue(nil, map[string]any{"notes": glueNotes})

		emit("event", jsonMsg("tests", "Writing self-tests…"))
		doneTests := tr.Step("phase", "tests", 0, "synthesizing self-tests from the intent")
		tests := studio.SynthesizeTests(ctx, model, intent, req.Workflow, cat)
		doneTests(nil, map[string]any{"count": len(tests)})

		// Same side-effect policy as the sync path (see sideEffectPolicy): mocked
		// unless the caller explicitly asked for real execution. Keeping the two
		// entry points on one helper is what stops the streamed variant from
		// quietly having a different (more dangerous) default than /studio/build.
		policy := req.sideEffectPolicy()
		opts := studio.BuildOptions{
			In: in, Tests: tests, Trace: tr, ExtraProblems: s.pythonBuildProblems,
			SideEffects: policy,
			Verifier:    studio.VerifierFor(policy, s.studioRealRunner()),
			MaxElapsed:  studio.DefaultMaxElapsed,
			MaxTokens:   s.cfg.LLM.Studio.MaxBuildTokens,
			MaxCostUSD:  s.cfg.LLM.Studio.MaxBuildCostUSD,
		}
		opts.OnEvent = func(ev studio.BuildEvent) {
			b, _ := json.Marshal(ev)
			emit("event", string(b))
		}

		rep := studio.BuildUntilWorks(ctx, model, req.Workflow, cat, opts)
		final := studio.Preflight(rep.Workflow, in)
		payload := fiber.Map{"report": rep, "preflight": final, "traceId": tr.ID}
		for k, v := range studioBuildBudget(rep) {
			payload[k] = v
		}
		done, _ := json.Marshal(payload)
		emit("done", string(done))
	}()

	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("X-Accel-Buffering", "no")
	c.Context().SetBodyStreamWriter(fasthttp.StreamWriter(func(w *bufio.Writer) {
		for ev := range events {
			if ev.event != "" {
				fmt.Fprintf(w, "event: %s\n", ev.event) //nolint:errcheck
			}
			fmt.Fprintf(w, "data: %s\n\n", ev.data) //nolint:errcheck
			w.Flush()                               //nolint:errcheck
		}
	}))
	return nil
}

// jsonMsg renders a {kind,message} progress frame for the build stream.
func jsonMsg(kind, msg string) string {
	b, _ := json.Marshal(studio.BuildEvent{Kind: kind, Message: msg})
	return string(b)
}

// studioGenerateStreamRequest bundles the inputs for the streamed generate
// pipeline (Story 9 M). Intent is the raw user prompt; Answers are optional
// clarifying-question answers; Light chooses the light-touch refine variant;
// AutoRepair enables the deterministic wiring-repair phase.
type studioGenerateStreamRequest struct {
	Intent     string            `json:"intent"`
	Answers    map[string]string `json:"answers,omitempty"`
	Light      bool              `json:"light,omitempty"`
	AutoRepair bool              `json:"auto_repair,omitempty"`
	// ForceWorkflow is the GUI's "Workflow" switch. The field was absent, so the
	// switch could not reach this endpoint even in principle: the streamed
	// generate always chose its own strategy, and a user who turned Workflow on
	// got a reasoning agent with no graph. /studio/compile has honoured the same
	// flag all along, which is what made the two buttons disagree.
	ForceWorkflow bool `json:"force_workflow,omitempty"`
}

// handleStudioGenerateStream implements POST /api/v1/studio/generate/stream.
// Streams the 5 pipeline phases (clarify_intent → choose_strategy →
// build_graph → validate → repair) as SSE `event: event` frames plus a
// terminating `event: done` frame carrying the full PipelineResult, so the
// GUI can render a live transcript (streamed default) or buffer events and
// reveal one at a time (wizard mode). Reuses the same code path as the
// synchronous pipeline runner in internal/studio/generatepipeline.go.
func (s *Server) handleStudioGenerateStream(c *fiber.Ctx) error {
	var req studioGenerateStreamRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if strings.TrimSpace(req.Intent) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "intent is required")
	}
	model := s.studioLLM(c)
	if model == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "LLM router unavailable")
	}
	cat := s.studioCatalogSnapshot()
	s.groundCatalog(&cat)
	learningOwner := studioLearningOwner(c)
	s.groundPreferencesFor(&cat, learningOwner)
	in := s.preflightInput(c, cat)
	// Detached from the request context — Fiber hands the connection to a stream
	// writer, so c.Context() is done before the pipeline finishes — but STILL
	// cancellable. Previously this was a bare WithoutCancel, which meant a user
	// who closed the tab or hit Cancel could not stop the run: it kept burning
	// model tokens with nobody listening. The writer cancels this when the client
	// goes away (ST-04).
	//
	// NOTE: no `defer cancelRun()` here. Fiber hands the connection to the stream
	// writer and this handler RETURNS IMMEDIATELY — so a handler-level defer
	// cancelled the run before the writer had even started, and every streamed
	// generate died on its first LLM call with "context canceled" in a few
	// milliseconds. Cancellation belongs to the two places that actually know the
	// run is over: the producer goroutine (work finished) and the stream writer
	// (client went away).
	ctx, cancelRun := context.WithCancel(detachedRequestContext(c))

	type sse struct{ event, data string }
	events := make(chan sse, 32)

	// Heartbeat mirrors the build stream so intermediaries don't kill the
	// idle connection during a long clarify_intent LLM call.
	hbStop := make(chan struct{})
	var hbWG sync.WaitGroup
	hbWG.Add(1)
	go func() {
		defer hbWG.Done()
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-hbStop:
				return
			case <-t.C:
				select {
				case events <- sse{event: "event", data: `{"phase":"heartbeat","status":"tick"}`}:
				case <-hbStop:
					return
				default:
				}
			}
		}
	}()

	go func() {
		// Releases the run context once the pipeline is done, whether it
		// succeeded or failed, so nothing leaks if the writer never runs.
		defer cancelRun()
		defer close(events)
		defer func() { close(hbStop); hbWG.Wait() }()
		emit := func(kind, data string) {
			select {
			case events <- sse{event: kind, data: data}:
			default:
			}
		}
		opts := studio.PipelineOptions{
			Answers:       req.Answers,
			Light:         req.Light,
			In:            in,
			AutoRepair:    req.AutoRepair,
			ForceWorkflow: req.ForceWorkflow,
			Emit: func(ev studio.PipelineEvent) {
				b, _ := json.Marshal(ev)
				emit("event", string(b))
			},
		}
		res, err := studio.RunGeneratePipeline(ctx, model, req.Intent, cat, opts)

		// Pin the RUNTIME provider/model, exactly as the two synchronous generate
		// handlers do. Without it this path emitted a draft carrying whatever the
		// generator left in `llm` — usually nothing, sometimes the builder model —
		// so the YAML named a provider/model the router had never registered. The
		// agent then failed at run time on a model the operator never chose, and
		// Save refused it with "does not specify a model to run on" while Run Live
		// (which resolves the default first) reported the same draft as fine.
		s.stampDefaultLLM(&res.Compile.Workflow)
		generatedStrategy := res.Compile.Workflow.Strategy
		if !res.Compile.Workflow.IsAgent() {
			generatedStrategy = "workflow"
		}
		if s.unreliableStrategy(res.Compile.Workflow.LLM.Provider, res.Compile.Workflow.LLM.Model, generatedStrategy) {
			err = fmt.Errorf("the generated execution strategy is historically unreliable for the active provider/model")
		}

		// Route the streamed result through the SAME finalization the synchronous
		// compile path uses. Without this, res.Compile.Contract stayed nil on the
		// streamed path while the sync path populated it — and the GUI's
		// post-generation blocker gate keys off exactly that field
		// (gui/src/pages/Studio.svelte applyCompile → `data.contract`). The failure
		// mode: a draft with execution blockers landed on the canvas presented as a
		// clean success, and the first the user heard of it was a 422 at Save (or a
		// broken agent at run time). Finalization runs even on the error path so a
		// partial draft still carries an honest verdict rather than no verdict.
		s.finalizeStudioResult(&res.Compile, cat, in)
		if err == nil {
			s.issueGenerationProof(learningOwner, &res.Compile.Workflow)
		}

		// `blocked` is the unambiguous "do not treat this as a good draft" signal.
		// The partial draft is preserved in `result` either way (the story requires
		// partial drafts survive failure), so a client can render what was built —
		// it just can't mistake it for a success.
		contract := res.Contract
		if res.Compile.Contract != nil {
			contract = *res.Compile.Contract
		}
		blocked := err != nil || contract.Blockers > 0
		payload := fiber.Map{
			"result":   res,
			"contract": contract,
			"blocked":  blocked,
		}
		if err != nil {
			payload["error"] = err.Error()
		}
		if blocked {
			// Deliberately NOT folded into `error`: the draft must still reach the
			// canvas so the user can see and fix it. `error` means "the pipeline
			// failed"; `blocked` means "there IS a draft, but it must not be
			// presented as ready".
			payload["blocked_reason"] = studioBlockedReason(err, contract)
		}
		done, _ := json.Marshal(payload)
		emit("done", string(done))
	}()

	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("X-Accel-Buffering", "no")
	c.Context().SetBodyStreamWriter(fasthttp.StreamWriter(func(w *bufio.Writer) {
		// A failed flush is the ONLY signal that the client went away once the
		// connection is handed to the stream writer. Cancelling the run then stops
		// work nobody is waiting for — previously the pipeline ran to completion
		// burning model tokens for a closed tab.
		defer cancelRun()
		for ev := range events {
			if ev.event != "" {
				fmt.Fprintf(w, "event: %s\n", ev.event) //nolint:errcheck
			}
			fmt.Fprintf(w, "data: %s\n\n", ev.data) //nolint:errcheck
			if err := w.Flush(); err != nil {
				cancelRun()
				// Keep draining: the producer's buffered sends must not block on a
				// channel nobody is reading, or its goroutine leaks.
				for range events {
				}
				return
			}
		}
	}))
	return nil
}

// studioBlockedReason renders the one-line explanation that accompanies
// `blocked:true` on the generate stream's done frame.
func studioBlockedReason(err error, contract studio.ContractResult) string {
	if err != nil {
		return err.Error()
	}
	if s := strings.TrimSpace(contract.Summary); s != "" {
		return s
	}
	return fmt.Sprintf("this draft has %d execution blocker(s) and is not ready to run", contract.Blockers)
}

// studioRealRunner wires the studio RealRunVerifier to the engine's execution
// primitives so a build-time verification run invokes real tools and real Python
// — the only way to catch failures that only appear when the agent actually runs.
func (s *Server) studioRealRunner() studio.RealRunner {
	if s.engine == nil {
		return studio.RealRunner{}
	}
	return studio.RealRunner{
		Tool: func(ctx context.Context, name, argsJSON string) (json.RawMessage, error) {
			return s.engine.RunTool(ctx, name, argsJSON)
		},
		Python: func(ctx context.Context, code string, argsJSON []byte) (json.RawMessage, error) {
			return s.engine.RunInlinePython(ctx, code, argsJSON)
		},
	}
}

// truncate shortens s to at most n runes, appending an ellipsis when cut, so a
// try-run trace stays compact without dumping large tool payloads.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// studioTryAgentRequest runs an UNSAVED reasoning agent against one question.
//
// ConfirmSideEffects / AcknowledgedTools are the Run Live consent surface. Run
// Live fires REAL tools, so it can send a message, write a file or run a
// privileged command on the operator's behalf. Previously it did that silently
// (and force-enabled Unattended so nothing could even pause to ask). The request
// must now carry an explicit acknowledgement of exactly what it is about to
// cause; without one the handler returns 409 + a preview instead of running.
type studioTryAgentRequest struct {
	Workflow studio.Draft `json:"workflow"`
	Question string       `json:"question"`
	// ConfirmSideEffects is the blanket acknowledgement: "I have seen the
	// preview and I accept every side effect in it." It is also what grants
	// privileged exposure to ToAgentDefinition.
	ConfirmSideEffects bool `json:"confirm_side_effects,omitempty"`
	// AcknowledgedTools is the per-tool form, for a GUI that lists each tool with
	// its own checkbox. The run proceeds only when it covers EVERY tool in the
	// preview; anything left over comes back in the 409 as unacknowledged_tools.
	AcknowledgedTools []string `json:"acknowledged_tools,omitempty"`
}

// studioRunPreview is the structured "what would this actually do?" payload,
// shared by POST /studio/run-preview and the 409 body of /studio/try-agent so
// the confirmation dialog and the refusal can never describe different runs.
type studioRunPreview struct {
	// SideEffectTools is the union the operator must acknowledge before Run Live
	// starts: privileged (S3 high-risk) tools, tools the author marked
	// confirm-required, and channel tools. Read-only surfaces (a web fetch, a
	// directory listing) are deliberately excluded — they are visible in Summary
	// but they do not escape the throwaway run, so gating on them would train the
	// operator to click through the dialog without reading it.
	SideEffectTools []string `json:"side_effect_tools"`
	// RequiresConfirmation is true when SideEffectTools is non-empty or the draft
	// needs privileged-exposure consent.
	RequiresConfirmation bool `json:"requires_confirmation"`
	// RequiresPrivilegedExposure mirrors the Save-path consent gate
	// (studio.Plan): a privileged-tier agent bound to a channel.
	RequiresPrivilegedExposure bool                 `json:"requires_privileged_exposure"`
	ConsentItems               []studio.ConsentItem `json:"consent_items,omitempty"`
	// Summary is the full studio.SecuritySummary — NetworkTools, FileTools,
	// ChannelTools, PrivilegedTools, ConfirmTools, UntrustedContentSources,
	// intent-gate mode — so the GUI can render the complete picture.
	Summary   studio.SecuritySummary   `json:"summary"`
	Blockers  []studio.SecurityFinding `json:"security_blockers,omitempty"`
	Warnings  []studio.SecurityFinding `json:"security_warnings,omitempty"`
	Contract  studio.ContractResult    `json:"contract"`
	Runnable  bool                     `json:"runnable"` // contract has no blockers
	Preflight studio.PreflightResult   `json:"preflight"`
}

// studioRunPreviewFor computes the Run Live preview for a draft. Pure over the
// live catalog + preflight state: it never runs anything.
func (s *Server) studioRunPreviewFor(c *fiber.Ctx, draft studio.Draft) studioRunPreview {
	draft = s.studioDraftWithRuntimeLLM(draft)
	cat := s.studioCatalogSnapshot()
	s.groundCatalog(&cat)
	in := s.preflightInput(c, cat)

	var def *agent.Definition
	if id := strings.TrimSpace(draft.ID); id != "" && s.loader != nil {
		def = s.loader.Get(id)
	}
	rev := studio.SecurityPreflight(draft, def, s.workspaceIntentGateDefault())
	contract := studio.AssessContract(draft, cat, in)

	out := studioRunPreview{
		SideEffectTools: studioSideEffectTools(rev.Summary),
		Summary:         rev.Summary,
		Blockers:        rev.Blockers,
		Warnings:        rev.Warnings,
		Contract:        contract,
		Runnable:        contract.Blockers == 0,
		Preflight:       studio.Preflight(draft, in),
	}
	// Same consent decision Save uses (studio.Plan), so Run Live and Save can't
	// disagree about whether a draft needs privileged-exposure consent.
	if plan, err := studio.Plan(draft); err == nil && plan.RequiresConsent {
		out.RequiresPrivilegedExposure = true
		out.ConsentItems = plan.ConsentItems
	}
	out.RequiresConfirmation = len(out.SideEffectTools) > 0 || out.RequiresPrivilegedExposure
	return out
}

// studioSideEffectTools is the acknowledgement set derived from a security
// summary. Kept in ONE place so the preview endpoint, the 409 body and the gate
// itself can never drift apart — a gate that lists different tools than the
// dialog is a gate that can be walked past.
func studioSideEffectTools(sum studio.SecuritySummary) []string {
	var out []string
	for _, group := range [][]string{sum.PrivilegedTools, sum.ConfirmTools, sum.ChannelTools} {
		for _, t := range group {
			if t = strings.TrimSpace(t); t != "" && !studioContainsTool(out, t) {
				out = append(out, t)
			}
		}
	}
	sort.Strings(out)
	return out
}

func studioContainsTool(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// unacknowledged returns the tools in `need` that the request has not covered.
// A blanket ConfirmSideEffects covers everything; otherwise every tool must be
// named individually.
func (r studioTryAgentRequest) unacknowledged(need []string) []string {
	if r.ConfirmSideEffects {
		return nil
	}
	ack := make(map[string]bool, len(r.AcknowledgedTools))
	for _, t := range r.AcknowledgedTools {
		ack[strings.TrimSpace(t)] = true
	}
	var missing []string
	for _, t := range need {
		if !ack[t] {
			missing = append(missing, t)
		}
	}
	return missing
}

// studioRunPreviewRequest is the POST /api/v1/studio/run-preview body.
type studioRunPreviewRequest struct {
	Workflow studio.Draft `json:"workflow"`
}

// handleStudioRunPreview implements POST /api/v1/studio/run-preview. It returns
// exactly what a Run Live of this draft would be allowed to do — the same
// payload /studio/try-agent refuses with — WITHOUT running anything, so the GUI
// can render the confirmation dialog before the operator commits. Read-only and
// deterministic: no engine, no tools, no LLM.
func (s *Server) handleStudioRunPreview(c *fiber.Ctx) error {
	var req studioRunPreviewRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	return c.JSON(s.studioRunPreviewFor(c, req.Workflow))
}

// handleStudioTryAgent implements POST /api/v1/studio/try-agent. It runs an
// UNSAVED ReAct/Plan-Execute agent against a single sample question so the author
// can see real behaviour before saving. The agent is registered in memory ONLY
// (never written to disk) and is removed immediately after. Real tools/skills DO
// fire (that's the point of a real try), but the reply is returned to the caller
// and is NOT delivered to any channel — calling Handle directly never routes the
// reply out.
//
// Because the tools are real, the run is gated exactly like Save:
//
//   - 422 when the draft has contract BLOCKERS. Run Live must not start a
//     workflow that is already known not to execute; previously it started
//     anyway and the failure surfaced as an opaque runtime error.
//   - 409 + a studioRunPreview when the draft has side-effecting / privileged /
//     confirm-required tools the request has not acknowledged. A second request
//     carrying confirm_side_effects (or acknowledged_tools covering the list)
//     proceeds.
//   - privileged exposure is passed to ToAgentDefinition ONLY when acknowledged;
//     it used to be hardcoded true, which silently granted the "system"
//     capability to a throwaway run.
//
// Deadlock safety (the original reason Unattended was force-enabled here): the
// run stays bounded by the 120s timeout below, and the engine FAILS FAST rather
// than hanging when a confirmation cannot be satisfied — with no confirm channel
// in this context, maybeConfirm/dynamicConfirm deny with a clear error instead of
// blocking on a channel nobody will ever answer. So we get the non-hanging
// property without auto-approving guardrails the operator never saw.
func (s *Server) handleStudioTryAgent(c *fiber.Ctx) error {
	var req studioTryAgentRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if s.engine == nil || s.loader == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "runtime engine unavailable")
	}
	// Runnable when it's a reasoning agent OR a workflow with actual steps. A bare
	// empty draft has nothing to run.
	if !req.Workflow.IsAgent() && len(req.Workflow.Flow.Nodes) == 0 {
		return s.errMsg(c, fiber.StatusBadRequest, "nothing to run — add steps, or use a ReAct/Plan-Execute agent")
	}
	q := strings.TrimSpace(req.Question)
	if q == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "question is required")
	}

	// (1) Contract gate — the same authoritative assessment /studio/save runs.
	// A workflow with execution blockers cannot produce a meaningful try; running
	// it just converts a precise, fixable blocker list into a runtime error.
	req.Workflow = s.studioDraftWithRuntimeLLM(req.Workflow)
	preview := s.studioRunPreviewFor(c, req.Workflow)
	if preview.Contract.Blockers > 0 {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{
			"error":     preview.Contract.Summary,
			"contract":  preview.Contract,
			"preflight": preview.Preflight,
			"preview":   preview,
		})
	}

	// (2) Side-effect gate — refuse to cause real, possibly irreversible effects
	// the caller has not explicitly acknowledged. The 409 body IS the preview, so
	// the GUI can render a dialog listing exactly what would happen and retry with
	// the acknowledgement.
	missing := req.unacknowledged(preview.SideEffectTools)
	needsExposureAck := preview.RequiresPrivilegedExposure && !req.ConfirmSideEffects
	if len(missing) > 0 || needsExposureAck {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{
			"error":                 "Run Live would cause real side effects; explicit acknowledgement required",
			"requires_confirmation": true,
			"unacknowledged_tools":  missing,
			"preview":               preview,
		})
	}

	// (3) Privileged exposure is granted only by the acknowledgement above —
	// never implicitly. Passing `true` unconditionally (the old behaviour) stamped
	// the "system" capability onto the throwaway definition, so a Run Live could
	// reach privileged tools the operator had not agreed to expose.
	acceptPrivileged := req.ConfirmSideEffects
	defVal, err := studio.ToAgentDefinition(req.Workflow, acceptPrivileged)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	def := &defVal
	def.ID = "studio-try-" + uuid.NewString()
	def.Enabled = true
	// Unattended is deliberately NOT forced on. See the deadlock-safety note in
	// the doc comment: the bounded timeout plus the engine's fail-fast denial when
	// no confirmer is present give us the "can't hang" property without silently
	// auto-approving every guardrail. Whatever the draft itself declares is
	// carried through by ToAgentDefinition and left alone.
	def.SourcePath = "" // never persisted

	s.loader.Register(def)
	defer s.loader.Unregister(def.ID)

	// Register ephemeral stubs for every helper agent the workflow references but
	// that isn't persisted yet. A Studio-generated workflow can contain `agent`
	// nodes pointing at brand-new peers that live only in draft.NewAgents — they
	// are created on disk at SAVE time (handleStudioSave), but Run Live runs an
	// UNSAVED draft, so those peers are not in the loader. Without this, an agent
	// node dispatches `agent__<peer>` and runAgentCall fails with "not loaded".
	// Registered non-persisted (SourcePath="") and Unregistered after the run.
	cleanupPeers := s.registerEphemeralPeers(def, req.Workflow.NewAgents)
	defer cleanupPeers()

	ctx, cancel := context.WithTimeout(detachedRequestContext(c), 120*time.Second)
	defer cancel()

	// Capture the exact sequence of skills/tools the agent invokes, so the author
	// can see whether it routed to the right one. read_skill calls record which
	// skill was loaded.
	var (
		traceMu sync.Mutex
		trace   = []fiber.Map{}
		// executed records the side-effecting calls that actually reached the
		// outside world, in order, so a cancelled or failed run can answer
		// "what did it already do?" (ST-11).
		executed = []fiber.Map{}
	)
	// The preview already classified which of this draft's tools have side
	// effects; reuse that set rather than re-deriving it, so the run reports
	// against exactly what the operator was shown and acknowledged.
	sideEffectTool := make(map[string]bool, len(preview.SideEffectTools))
	for _, t := range preview.SideEffectTools {
		sideEffectTool[t] = true
	}
	ctx = runtime.WithToolObserver(ctx, func(call message.ToolCall, result string, isErr bool) {
		detail := ""
		if call.Name == "read_skill" {
			if sk, ok := call.Arguments["skill_name"].(string); ok {
				detail = sk
			}
		}
		args, argsFull := "", ""
		if len(call.Arguments) > 0 {
			if b, err := json.Marshal(call.Arguments); err == nil {
				args = truncate(string(b), 240)
				argsFull = truncate(string(b), 100000)
			}
		}
		traceMu.Lock()
		trace = append(trace, fiber.Map{
			"name":   call.Name,
			"detail": detail,
			"args":   args,
			"result": truncate(strings.TrimSpace(result), 400),
			"error":  isErr,
			// Full argument/result payloads alongside the truncated summary, so a
			// structured event can be expanded rather than being a clipped string
			// the author has to guess the rest of.
			"args_full":   argsFull,
			"result_full": truncate(strings.TrimSpace(result), 100000),
		})
		// Record side-effecting calls that ACTUALLY happened. If a run is
		// cancelled or fails halfway, "what did it already do to the outside
		// world?" is the first question, and previously nothing answered it —
		// the trace listed every call without saying which ones touched anything
		// real.
		if sideEffectTool[call.Name] {
			executed = append(executed, fiber.Map{
				"tool": call.Name, "at": time.Now().UTC().Format(time.RFC3339),
				"failed": isErr,
			})
		}
		traceMu.Unlock()
	})

	// Capture EVERY workflow node (python, branch-adjacent tool, agent, llm) with
	// its input/output/error — not just tool calls — so the author can see what
	// each node returned and why a branch went the way it did (e.g. a Python node
	// refused by the consent gate, or one that produced no data).
	var nodeTrace []fiber.Map
	ctx = runtime.WithFlowNodeObserver(ctx, func(rec reasoningpkg.FlowNodeRun) {
		out := strings.TrimSpace(string(rec.Output))
		traceMu.Lock()
		// Truncated fields drive the compact trace list; the *_full fields carry
		// the complete input/output (generously capped) so the author can copy the
		// exact data and reproduce a node's behavior outside Studio. The full data
		// also gives live-repair an accurate shape to diagnose against.
		nodeTrace = append(nodeTrace, fiber.Map{
			"node_id":     rec.NodeID,
			"kind":        rec.Kind,
			"input":       truncate(rec.Input, 300),
			"output":      truncate(out, 600),
			"input_full":  truncate(rec.Input, 100000),
			"output_full": truncate(out, 100000),
			"adapted":     rec.Adapted,
			"error":       rec.Error,
			"skipped":     rec.Error != "" && strings.Contains(strings.ToLower(rec.Error), "consent"),
			"duration_ms": rec.DurationMS,
		})
		traceMu.Unlock()
	})

	msg := message.Message{
		ID:        uuid.NewString(),
		SessionID: "studio-try-" + def.ID,
		AgentID:   def.ID,
		Channel:   "studio-try",
		ThreadID:  "studio",
		UserID:    "studio",
		Username:  "studio",
		Role:      message.RoleUser,
		Parts:     message.Text(q),
		CreatedAt: time.Now().UTC(),
	}

	ctx = withRequestPrincipal(c, ctx)
	reply, runErr := s.engine.Handle(ctx, msg)
	replyText := ""
	for _, p := range reply.Parts {
		if p.Type == message.ContentText && p.Text != "" {
			replyText = p.Text
			break
		}
	}
	traceMu.Lock()
	usedTrace := trace
	usedNodeTrace := nodeTrace
	usedExecuted := executed
	traceMu.Unlock()
	if usedNodeTrace == nil {
		usedNodeTrace = []fiber.Map{}
	}
	resp := fiber.Map{
		"reply": replyText, "parts": reply.Parts,
		"trace": usedTrace, "node_trace": usedNodeTrace,
		// What this run actually did to the outside world. Present even on the
		// error path — especially on the error path, since a half-completed run
		// is exactly when it matters whether the message was already sent.
		"executed_side_effects": usedExecuted,
		// Report the posture the run ACTUALLY had, so "it worked in Run Live" is
		// an honest claim: unattended=false means guardrail confirmations were
		// enforced (and denied, not auto-approved), and acknowledged_tools records
		// what the operator signed off on.
		"unattended":         def.Unattended,
		"acknowledged_tools": preview.SideEffectTools,
		"preview":            preview,
	}
	if runErr != nil {
		resp["error"] = runErr.Error()
	}
	return c.JSON(resp)
}

// ensurePeerAgents makes a workflow's delegation actually work: it persists a
// real agent for every peer the workflow calls but which does not exist yet,
// and makes sure the caller DECLARES every peer it calls. It returns the peer
// ids it had to create.
//
// Both halves are needed and they fail differently. Without the first, the
// saved workflow names an agent that is not there. Without the second, the
// agent exists and the runtime still refuses the call — it only allows peers
// listed in the caller's `agents:` — so every run dies at the delegating node
// with "is not in this agent's declared peer list". That second failure was
// live for real users: the wizard derives the list from the flow, but the
// SOUL.yaml path writes the definition verbatim, so a workflow saved from the
// code view had its peers created and then could not call them.
//
// This is the durable twin of registerEphemeralPeers: that one registers peers
// in memory for the length of a test run, this one writes them to disk so the
// saved workflow can actually run. It lives here, shared, because Studio has
// more than one way to persist the same workflow — the wizard's Save and the
// code view's "Save SOUL.yaml" — and when only one of them materialised peers,
// which button you pressed decided whether the agent you just saved was
// runnable. The workflow saved fine either way; it just referenced an agent
// that was never created, and the run died at the delegating node.
//
// `newAgents` is the draft's profile list when there is a draft (the wizard
// path). It may be nil — every missing profile is synthesized from the node.
//
// Errors are returned, not swallowed. A peer that fails to persist leaves the
// caller with exactly the broken agent this function exists to prevent, so the
// save should fail loudly rather than report success.
func (s *Server) ensurePeerAgents(dir string, def *agent.Definition, newAgents []studio.NewAgent) ([]string, error) {
	if def == nil || def.Workflow == nil {
		return nil, nil
	}
	byID := make(map[string]studio.NewAgent, len(newAgents))
	for _, na := range newAgents {
		byID[na.ID] = na
	}
	created := []string{}
	seen := map[string]bool{}
	declared := make(map[string]bool, len(def.Agents))
	for _, p := range def.Agents {
		declared[p] = true
	}
	for _, node := range def.Workflow.Nodes {
		if node.Kind != "agent" || node.Agent == "" || seen[node.Agent] {
			continue
		}
		seen[node.Agent] = true
		// Authorise the call. The runtime checks this list, not the graph.
		if !declared[node.Agent] {
			def.Agents = append(def.Agents, node.Agent)
			declared[node.Agent] = true
		}
		if existing := s.loader.Get(node.Agent); existing != nil {
			continue // a real agent already answers to this name
		}
		// Prefer the profile the draft carries; if it is missing or thin,
		// synthesize a complete, reusable persona from the node so no helper
		// agent is ever saved blank.
		na := byID[node.Agent]
		synth := studio.SynthesizeAgent(node.Agent, node, def.Name)
		if strings.TrimSpace(na.Name) == "" {
			na.Name = synth.Name
		}
		if strings.TrimSpace(na.Description) == "" {
			na.Description = synth.Description
		}
		if strings.TrimSpace(na.SystemPrompt) == "" {
			na.SystemPrompt = synth.SystemPrompt
		}
		peer := agent.Definition{
			ID:           node.Agent,
			Name:         na.Name,
			Description:  na.Description,
			SystemPrompt: na.SystemPrompt,
			Enabled:      true,
			MaxTurns:     15,
			Memory:       agent.MemoryPolicy{MaxTokens: 8000},
			LLM: agent.LLMConfig{
				Provider:    s.cfg.LLM.DefaultProvider,
				Temperature: 0.7,
			},
		}
		if err := s.loader.Upsert(dir, &peer); err != nil {
			return created, fmt.Errorf("could not create helper agent %q that this workflow delegates to: %w", node.Agent, err)
		}
		s.log.Info("studio: created helper agent referenced by workflow",
			zap.String("peer", node.Agent), zap.String("workflow", def.ID))
		created = append(created, node.Agent)
	}
	return created, nil
}

// registerEphemeralPeers registers an in-memory, non-persisted stub for every
// helper agent that def's workflow references via an `agent` node but that is
// not already in the loader. It mirrors the auto-stubbing handleStudioSave does
// on disk (studio.SynthesizeAgent to fill any thin/blank profile) so a Run Live
// of an UNSAVED draft can resolve `agent__<peer>` peers instead of failing with
// "agent call: <id> not loaded". The returned cleanup func Unregisters every
// stub it added and must be deferred by the caller. It is safe to call with a
// nil/agent (no workflow) def — it simply registers nothing.
func (s *Server) registerEphemeralPeers(def *agent.Definition, newAgents []studio.NewAgent) func() {
	if def == nil || def.Workflow == nil {
		return func() {}
	}
	byID := make(map[string]studio.NewAgent, len(newAgents))
	for _, na := range newAgents {
		byID[na.ID] = na
	}
	var registered []string
	stubbed := map[string]bool{}
	for _, node := range def.Workflow.Nodes {
		if node.Kind != "agent" || node.Agent == "" || stubbed[node.Agent] {
			continue
		}
		if existing := s.loader.Get(node.Agent); existing != nil {
			continue // real (or already-registered) agent — leave it be
		}
		na, ok := byID[node.Agent]
		if !ok || na.Name == "" || na.Description == "" || strings.TrimSpace(na.SystemPrompt) == "" {
			synth := studio.SynthesizeAgent(node.Agent, node, def.Name)
			if na.Name == "" {
				na.Name = synth.Name
			}
			if na.Description == "" {
				na.Description = synth.Description
			}
			if strings.TrimSpace(na.SystemPrompt) == "" {
				na.SystemPrompt = synth.SystemPrompt
			}
		}
		s.loader.Register(&agent.Definition{
			ID:           node.Agent,
			Name:         na.Name,
			Description:  na.Description,
			SystemPrompt: na.SystemPrompt,
			Enabled:      true,
			// Inherit the PARENT's attendance rather than forcing true. Forcing it
			// here reopened the exact hole the Run Live consent gate closes: an
			// attended parent could still reach a confirmation-requiring tool
			// THROUGH a synthesized peer and have it auto-approved, because the
			// stub — not the parent — is what the runtime consults for that call.
			Unattended: def.Unattended,
			MaxTurns:   15,
			Memory:     agent.MemoryPolicy{MaxTokens: 8000},
			LLM: agent.LLMConfig{
				Provider:    s.cfg.LLM.DefaultProvider,
				Temperature: 0.7,
			},
			SourcePath: "", // in-memory only, never persisted
		})
		registered = append(registered, node.Agent)
		stubbed[node.Agent] = true
	}
	return func() {
		for _, id := range registered {
			s.loader.Unregister(id)
		}
	}
}

// handleStudioFailedRuns implements GET /api/v1/studio/failed-runs. It surfaces
// the runs that FAILED at run time (including unattended scheduled runs), drawn
// from the dead-letter queue the engine writes on every failed Handle(). Each
// entry names the agent, the real error, and when it failed — so the user can
// self-heal a 6am scheduled failure without pasting anything anywhere. This is
// the "Soulacy is the only savior" feed: every failure is actionable in-product.
func (s *Server) handleStudioFailedRuns(c *fiber.Ctx) error {
	if s.dlqStore == nil {
		return c.JSON(fiber.Map{"runs": []any{}})
	}
	items, err := s.dlqStore.List(c.Context(), "")
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	type failedRun struct {
		ID        string `json:"id"`
		AgentID   string `json:"agentId"`
		AgentName string `json:"agentName"`
		Error     string `json:"error"`
		Attempts  int    `json:"attempts"`
		FailedAt  string `json:"failedAt"`
		Healable  bool   `json:"healable"`
	}
	runs := make([]failedRun, 0, len(items))
	for _, it := range items {
		fr := failedRun{
			ID: it.ID, AgentID: it.Queue, Error: it.ErrorMsg,
			Attempts: it.Attempts, FailedAt: it.LastAttemptAt.UTC().Format("2006-01-02T15:04:05Z"),
		}
		if s.loader != nil {
			if def := s.loader.Get(it.Queue); def != nil {
				fr.AgentName = def.Name
				fr.Healable = true // the saved agent still exists, so we can repair it
			}
		}
		runs = append(runs, fr)
	}
	return c.JSON(fiber.Map{"runs": runs})
}

// handleStudioRunTrace implements GET /api/v1/studio/run-trace. It returns the
// per-block run trace (Story S0.3 Phase 1) of a flow run — each executed block's
// input, output, duration, error, and whether its input came from typed port
// wires — so the GUI can show a non-technical user WHERE a run went wrong.
//
// Query: runId selects a specific run; otherwise agentId returns that agent's
// most recent run. The trace is best-effort and in-memory, so a run the gateway
// no longer retains returns an empty (not error) trace.
func (s *Server) handleStudioRunTrace(c *fiber.Ctx) error {
	if s.engine == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "engine unavailable")
	}
	agentID := strings.TrimSpace(c.Query("agentId"))
	runID := strings.TrimSpace(c.Query("runId"))
	if agentID == "" && runID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "agentId or runId is required")
	}
	// Types inferred so studio.go needn't import the runtime package.
	tr, ok := s.engine.LatestFlowTrace(agentID)
	if runID != "" {
		// Disk-aware: an old run that aged out of memory is still served from the
		// durable run history.
		tr, ok = s.engine.FlowTraceFor(agentID, runID)
	}
	if !ok {
		return c.JSON(fiber.Map{"agentId": agentID, "runId": runID, "entries": []any{}})
	}
	return c.JSON(tr)
}

// handleStudioRunDiagnosis implements GET /api/v1/studio/run-diagnosis. It
// returns a deterministic diagnosis for a retained run trace: the failing node,
// likely root cause, evidence, and next action. This gives Studio and Activity a
// shared troubleshooting vocabulary without requiring an LLM just to classify
// common platform failures.
func (s *Server) handleStudioRunDiagnosis(c *fiber.Ctx) error {
	if s.engine == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "engine unavailable")
	}
	agentID := strings.TrimSpace(c.Query("agentId"))
	runID := strings.TrimSpace(c.Query("runId"))
	if agentID == "" && runID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "agentId or runId is required")
	}
	d, ok := s.engine.FlowRunDiagnosis(agentID, runID)
	if !ok {
		return c.JSON(fiber.Map{
			"agentId": agentID,
			"runId":   runID,
			"status":  "empty",
			"summary": "No retained run trace was found.",
			"suggestions": []string{
				"Run the agent once and refresh this panel.",
				"Check Activity if the failure happened before the workflow emitted a trace.",
			},
			"retryable": true,
		})
	}
	return c.JSON(d)
}

// handleStudioRunHistory implements GET /api/v1/studio/run-history. It returns a
// summary of EVERY retained run for an agent — scheduled and on-demand alike,
// newest first, each with its trigger source and verdict — so the GUI can show a
// complete run history instead of just the latest run. In-memory and best-effort
// (a run the gateway no longer retains is dropped), so it returns an empty list,
// not an error, when nothing is recorded.
func (s *Server) handleStudioRunHistory(c *fiber.Ctx) error {
	if s.engine == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "engine unavailable")
	}
	agentID := strings.TrimSpace(c.Query("agentId"))
	if agentID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "agentId is required")
	}
	return c.JSON(fiber.Map{"agentId": agentID, "runs": s.completeRunHistory(agentID)})
}

type studioRunHistoryRow struct {
	RunID           string    `json:"runId"`
	SessionID       string    `json:"sessionId,omitempty"`
	Trigger         string    `json:"trigger,omitempty"`
	Source          string    `json:"source,omitempty"`
	StartedAt       time.Time `json:"startedAt"`
	UpdatedAt       time.Time `json:"updatedAt,omitempty"`
	Steps           int       `json:"steps,omitempty"`
	Ok              bool      `json:"ok"`
	Status          string    `json:"status"`
	Error           string    `json:"error,omitempty"`
	Output          string    `json:"output,omitempty"`
	DeliveryChannel string    `json:"deliveryChannel,omitempty"`
	DeliveryTo      string    `json:"deliveryTo,omitempty"`
	DeliveryStatus  string    `json:"deliveryStatus,omitempty"`
	DeliveryError   string    `json:"deliveryError,omitempty"`
}

func (s *Server) completeRunHistory(agentID string) []studioRunHistoryRow {
	byID := map[string]studioRunHistoryRow{}
	for _, r := range s.engine.FlowRunHistory(agentID) {
		status := "success"
		if !r.Ok {
			status = "failed"
		}
		byID[r.RunID] = studioRunHistoryRow{
			RunID:     r.RunID,
			SessionID: r.RunID,
			Trigger:   r.Trigger,
			Source:    "flow",
			StartedAt: r.StartedAt,
			UpdatedAt: r.UpdatedAt,
			Steps:     r.Steps,
			Ok:        r.Ok,
			Status:    status,
			Error:     r.Error,
			Output:    r.Error,
		}
	}

	for _, r := range s.durableRunHistory(agentID) {
		if cur, ok := byID[r.RunID]; ok {
			cur.SessionID = studioFirstNonEmpty(cur.SessionID, r.SessionID)
			cur.Trigger = studioFirstNonEmpty(cur.Trigger, r.Trigger)
			cur.Source = "flow+durable"
			cur.StartedAt = studioFirstNonZeroTime(cur.StartedAt, r.StartedAt)
			if r.UpdatedAt.After(cur.UpdatedAt) {
				cur.UpdatedAt = r.UpdatedAt
			}
			cur.Output = studioFirstNonEmpty(cur.Output, r.Output)
			cur.Error = studioFirstNonEmpty(cur.Error, r.Error)
			cur.DeliveryChannel = studioFirstNonEmpty(cur.DeliveryChannel, r.DeliveryChannel)
			cur.DeliveryTo = studioFirstNonEmpty(cur.DeliveryTo, r.DeliveryTo)
			cur.DeliveryStatus = studioFirstNonEmpty(cur.DeliveryStatus, r.DeliveryStatus)
			cur.DeliveryError = studioFirstNonEmpty(cur.DeliveryError, r.DeliveryError)
			if cur.Status == "" || cur.Status == "success" && r.Status == "failed" {
				cur.Status = r.Status
				cur.Ok = r.Ok
			}
			byID[r.RunID] = cur
			continue
		}
		byID[r.RunID] = r
	}

	rows := make([]studioRunHistoryRow, 0, len(byID))
	for _, r := range byID {
		if r.UpdatedAt.IsZero() {
			r.UpdatedAt = r.StartedAt
		}
		if r.Status == "" {
			r.Status = "unknown"
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].UpdatedAt.After(rows[j].UpdatedAt)
	})
	return rows
}

func (s *Server) durableRunHistory(agentID string) []studioRunHistoryRow {
	if s.actions == nil {
		return nil
	}
	allowed := map[string]bool{
		"message.in":      true,
		"message.out":     true,
		"error":           true,
		"tool.result":     true,
		"schedule.output": true,
	}
	var events []message.Event
	var err error
	if qf, ok := s.actions.(interface {
		QueryFiltered(string, int, map[string]bool) ([]message.Event, error)
	}); ok {
		events, err = qf.QueryFiltered(agentID, 10000, allowed)
	} else if tf, ok := s.actions.(interface {
		TailFiltered(string, int, map[string]bool) ([]message.Event, error)
	}); ok {
		events, err = tf.TailFiltered(agentID, 5000, allowed)
	} else {
		events, err = s.actions.Tail(agentID, 5000)
	}
	if err != nil {
		return []studioRunHistoryRow{{
			RunID:     "history-error",
			Source:    "durable",
			StartedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
			Status:    "failed",
			Error:     "could not load durable run history: " + err.Error(),
		}}
	}

	sort.Slice(events, func(i, j int) bool { return events[i].Timestamp.Before(events[j].Timestamp) })

	byRun := map[string][]message.Event{}
	runSession := map[string]string{}
	currentBySession := map[string]string{}
	startsBySession := map[string]int{}
	order := []string{}
	for i, ev := range events {
		if len(allowed) > 0 && !allowed[ev.Type] {
			continue
		}
		sid := strings.TrimSpace(ev.SessionID)
		if sid == "" {
			sid = fmt.Sprintf("event-%d", i)
		}
		runID := currentBySession[sid]
		if ev.Type == "message.in" || runID == "" {
			if startsBySession[sid] == 0 {
				runID = sid
			} else {
				runID = durableHistoryRunID(sid, ev.Timestamp, i)
			}
			startsBySession[sid]++
			currentBySession[sid] = runID
			order = append(order, runID)
		}
		if _, ok := byRun[runID]; !ok {
			runSession[runID] = sid
		}
		byRun[runID] = append(byRun[runID], ev)
	}

	rows := make([]studioRunHistoryRow, 0, len(byRun))
	for _, runID := range order {
		if row, ok := summarizeActionEvents(runID, runSession[runID], byRun[runID]); ok {
			rows = append(rows, row)
		}
	}
	return rows
}

func durableHistoryRunID(sessionID string, ts time.Time, idx int) string {
	if ts.IsZero() {
		return fmt.Sprintf("%s:%06d", sessionID, idx)
	}
	return fmt.Sprintf("%s:%s:%06d", sessionID, ts.UTC().Format("20060102T150405.000000000Z"), idx)
}

func summarizeActionEvents(runID, sessionID string, events []message.Event) (studioRunHistoryRow, bool) {
	if len(events) == 0 {
		return studioRunHistoryRow{}, false
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Timestamp.Before(events[j].Timestamp) })
	row := studioRunHistoryRow{
		RunID:     runID,
		SessionID: sessionID,
		Source:    "durable",
		StartedAt: events[0].Timestamp,
		UpdatedAt: events[len(events)-1].Timestamp,
		Status:    "unknown",
	}
	var outParts []string
	for _, ev := range events {
		switch ev.Type {
		case "message.in":
			row.Trigger = studioFirstNonEmpty(row.Trigger, triggerFromMessagePayload(ev.Payload))
			if row.StartedAt.IsZero() {
				row.StartedAt = ev.Timestamp
			}
		case "message.out":
			if txt := messagePayloadText(ev.Payload); txt != "" {
				outParts = append(outParts, txt)
			}
			// A reply sent AFTER a failure does not undo the failure.
			//
			// Events are replayed in timestamp order, and this arm used to set
			// success unconditionally — so any run that errored and then still
			// emitted something was filed as successful. That is the normal shape
			// of a degraded run, not an exotic one: the loop gives up, the
			// framework sends the last thing it has, and the run is recorded
			// ok: true, status: "success", error: "context deadline exceeded" —
			// all three at once, which cannot all be right.
			//
			// It is not cosmetic. Failed runs, the dead-letter queue and the
			// scheduler's consecutive-failure auto-disable all read this. An agent
			// that timed out every morning and replied with a fragment would never
			// appear in any of them.
			if row.Status != "failed" {
				row.Status = "success"
				row.Ok = true
			}
		case "error":
			row.Status = "failed"
			row.Ok = false
			row.Error = studioFirstNonEmpty(row.Error, payloadErrorText(ev.Payload))
		case "tool.result":
			row.Steps++
			if isToolError(ev.Payload) {
				row.Status = "failed"
				row.Ok = false
				row.Error = studioFirstNonEmpty(row.Error, payloadErrorText(ev.Payload))
			}
		case "schedule.output":
			ch, to, delivered, fallback, reason, preview, trigger := scheduleOutputSummary(ev.Payload)
			row.DeliveryChannel = studioFirstNonEmpty(row.DeliveryChannel, ch)
			row.DeliveryTo = studioFirstNonEmpty(row.DeliveryTo, to)
			row.Trigger = studioFirstNonEmpty(row.Trigger, trigger, "cron")
			if preview != "" {
				outParts = append(outParts, preview)
			}
			if delivered {
				if fallback {
					row.DeliveryStatus = "delivered via fallback"
				} else {
					row.DeliveryStatus = "delivered"
				}
				if row.Status == "unknown" || row.Status == "pending" {
					row.Status = "success"
					row.Ok = true
				}
			} else {
				row.DeliveryStatus = "failed"
				row.DeliveryError = studioFirstNonEmpty(row.DeliveryError, reason)
				row.Status = "failed"
				row.Ok = false
			}
		}
	}
	if len(outParts) > 0 {
		row.Output = strings.Join(studioUniqueStrings(outParts), "\n\n")
	}
	if row.Error != "" && row.Output == "" {
		row.Output = row.Error
	}
	if row.Status == "unknown" && row.Output == "" && row.Error == "" {
		row.Status = "pending"
	}
	return row, true
}

func studioUniqueStrings(vals []string) []string {
	out := make([]string, 0, len(vals))
	seen := map[string]bool{}
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func studioFirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func studioFirstNonZeroTime(vals ...time.Time) time.Time {
	for _, v := range vals {
		if !v.IsZero() {
			return v
		}
	}
	return time.Time{}
}

func payloadMap(payload any) map[string]any {
	switch v := payload.(type) {
	case map[string]any:
		return v
	case map[string]string:
		out := make(map[string]any, len(v))
		for k, val := range v {
			out[k] = val
		}
		return out
	case message.Message:
		b, _ := json.Marshal(v)
		var out map[string]any
		_ = json.Unmarshal(b, &out)
		return out
	default:
		return nil
	}
}

func triggerFromMessagePayload(payload any) string {
	m := payloadMap(payload)
	if m == nil {
		return ""
	}
	if meta, ok := m["metadata"].(map[string]any); ok {
		if trig, _ := meta["trigger"].(string); trig != "" {
			return trig
		}
	}
	txt := messagePayloadText(payload)
	if strings.HasPrefix(txt, "__trigger:") && strings.HasSuffix(txt, "__") {
		return strings.TrimSuffix(strings.TrimPrefix(txt, "__trigger:"), "__")
	}
	if ch, _ := m["channel"].(string); ch != "" {
		return ch
	}
	return ""
}

func messagePayloadText(payload any) string {
	if msg, ok := payload.(message.Message); ok {
		parts := make([]string, 0, len(msg.Parts))
		for _, p := range msg.Parts {
			if p.Type == message.ContentText && strings.TrimSpace(p.Text) != "" {
				parts = append(parts, p.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	m := payloadMap(payload)
	if m == nil {
		if s, ok := payload.(string); ok {
			return s
		}
		return ""
	}
	rawParts, _ := m["parts"].([]any)
	parts := make([]string, 0, len(rawParts))
	for _, raw := range rawParts {
		pm, _ := raw.(map[string]any)
		if pm == nil {
			continue
		}
		typ, _ := pm["type"].(string)
		txt, _ := pm["text"].(string)
		if (typ == "" || typ == "text") && strings.TrimSpace(txt) != "" {
			parts = append(parts, txt)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	for _, key := range []string{"text", "message", "reply_preview", "error"} {
		if txt, _ := m[key].(string); txt != "" {
			return txt
		}
	}
	return ""
}

func payloadErrorText(payload any) string {
	if txt := messagePayloadText(payload); txt != "" {
		return txt
	}
	m := payloadMap(payload)
	if m == nil {
		return fmt.Sprint(payload)
	}
	for _, key := range []string{"error", "message", "content", "detail"} {
		if txt, _ := m[key].(string); txt != "" {
			return txt
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func isToolError(payload any) bool {
	m := payloadMap(payload)
	if m == nil {
		return false
	}
	if v, ok := m["is_error"].(bool); ok {
		return v
	}
	if v, ok := m["isError"].(bool); ok {
		return v
	}
	return false
}

func scheduleOutputSummary(payload any) (channel, to string, delivered, fallback bool, reason, preview, trigger string) {
	m := payloadMap(payload)
	if m == nil {
		return "", "", false, false, "", "", ""
	}
	channel, _ = m["channel"].(string)
	to, _ = m["to"].(string)
	delivered, _ = m["delivered"].(bool)
	fallback, _ = m["fallback"].(bool)
	reason = studioFirstNonEmpty(stringField(m, "reason"), stringField(m, "detail"))
	preview = studioFirstNonEmpty(stringField(m, "reply_preview"), stringField(m, "text"), stringField(m, "message"))
	trigger = stringField(m, "trigger")
	return channel, to, delivered, fallback, reason, preview, trigger
}

func stringField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// studioDiagnoseRunRequest is the POST /api/v1/studio/diagnose-run body: a
// dead-letter entry id to diagnose and self-heal.
type studioDiagnoseRunRequest struct {
	ID string `json:"id"`
}

type studioDiagnoseSessionRequest struct {
	AgentID   string `json:"agentId"`
	SessionID string `json:"sessionId"`
}

// handleStudioDiagnoseRun implements POST /api/v1/studio/diagnose-run. Given a
// failed run, it loads the SAVED agent, reconstructs its draft, repairs it
// against the REAL runtime error, then runs the full build-verify loop so the
// fix is validated (and, for workflows, actually re-executed). It returns the
// healed draft + a transcript so the user can review and apply it — turning an
// opaque scheduled-run failure into a one-click fix.
func (s *Server) handleStudioDiagnoseRun(c *fiber.Ctx) error {
	var req studioDiagnoseRunRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if strings.TrimSpace(req.ID) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "failed-run id is required")
	}
	if s.dlqStore == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "no failed-run history available")
	}
	model := s.studioLLM(c)
	if model == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "LLM router unavailable")
	}
	entry, err := s.dlqStore.Get(c.Context(), req.ID)
	if err != nil {
		return s.errMsg(c, fiber.StatusNotFound, "failed run not found")
	}
	def := s.loader.Get(entry.Queue)
	if def == nil {
		return s.errMsg(c, fiber.StatusNotFound, "the agent for this run no longer exists")
	}
	draft := studio.FromAgentDefinition(*def)

	cat := s.studioCatalogSnapshot()
	s.groundCatalog(&cat)
	in := s.preflightInput(c, cat)

	// (1) Repair from the concrete per-node trace first. This gives the bounded
	// repair model the failing node's original template/code and the real
	// producer output shape, instead of asking it to guess from a final error.
	var traceRepairs []studio.RepairProposal
	traceRunID := ""
	if s.engine != nil {
		if tr, ok := s.engine.LatestFlowTrace(entry.Queue); ok && len(tr.Entries) > 0 {
			draft, traceRepairs = studioRepairDraftFromTrace(c.Context(), model, draft, tr.Entries)
			traceRunID = tr.RunID
			studio.RepairWiring(&draft, cat)
		}
	}

	// (2) Repair any remaining cross-node or structural problem against the real
	// runtime error. The trace repair above is intentionally node-local.
	problem := "At RUN TIME this agent failed with: " + strings.TrimSpace(entry.ErrorMsg) +
		" — change the agent so this cannot happen again."
	healed, changed := studio.RepairWithProblems(c.Context(), model, draft, []string{problem}, cat)
	changed = changed || len(traceRepairs) > 0
	studio.RepairWiring(&healed, cat)

	// (3) Validate (and, for workflows, re-run) to confirm the fix holds.
	//
	// SideEffectsReal is set DELIBERATELY here (it is now required — a real
	// verifier without the policy is downgraded to the mock). Self-heal repairs a
	// failure that actually happened at run time, taken from the dead-letter
	// queue: a mocked walk cannot reproduce a real tool's error, so verifying
	// against mocks would report "fixed" on the exact evidence that proves it
	// isn't. The operator explicitly asked to heal THIS failed run, which is the
	// same consent shape as re-running the agent, and MaxElapsed bounds it.
	rep := studio.BuildUntilWorks(c.Context(), model, healed, cat, studio.BuildOptions{
		In:            in,
		SideEffects:   studio.SideEffectsReal,
		Verifier:      studio.VerifierFor(studio.SideEffectsReal, s.studioRealRunner()),
		ExtraProblems: s.pythonBuildProblems,
		MaxElapsed:    studio.DefaultMaxElapsed,
		MaxTokens:     s.cfg.LLM.Studio.MaxBuildTokens,
		MaxCostUSD:    s.cfg.LLM.Studio.MaxBuildCostUSD,
	})

	return c.JSON(fiber.Map{
		"agentId":      entry.Queue,
		"agentName":    def.Name,
		"error":        entry.ErrorMsg,
		"changed":      changed,
		"traceRunId":   traceRunID,
		"traceRepairs": traceRepairs,
		"workflow":     rep.Workflow,
		"report":       rep,
		"preflight":    studio.Preflight(rep.Workflow, in),
	})
}

// handleStudioDiagnoseSession repairs a saved agent from a concrete Activity
// session. Unlike handleStudioDiagnoseRun, this does not require a dead-letter
// queue entry; it reconstructs evidence from the action log, so any visible
// Activity error can become a Studio debugging session.
func (s *Server) handleStudioDiagnoseSession(c *fiber.Ctx) error {
	var req studioDiagnoseSessionRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	req.AgentID = strings.TrimSpace(req.AgentID)
	req.SessionID = strings.TrimSpace(req.SessionID)
	if req.AgentID == "" || req.SessionID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "agentId and sessionId are required")
	}
	if s.actions == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "action logging disabled")
	}
	model := s.studioLLM(c)
	if model == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "LLM router unavailable")
	}
	def := s.loader.Get(req.AgentID)
	if def == nil {
		return s.errMsg(c, fiber.StatusNotFound, "the agent for this run no longer exists")
	}
	events, err := s.actions.Tail(req.AgentID, 5000)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	evidence, errText, found := studioSessionEvidence(events, req.AgentID, req.SessionID)
	if !found {
		return s.errMsg(c, fiber.StatusNotFound, "no action-log events found for that agent/session")
	}
	if strings.TrimSpace(errText) == "" {
		errText = "The run did not emit an explicit error, but the operator requested debugging from Activity."
	}

	draft := studio.FromAgentDefinition(*def)
	cat := s.studioCatalogSnapshot()
	s.groundCatalog(&cat)
	in := s.preflightInput(c, cat)

	var traceRepairs []studio.RepairProposal
	traceRunID := ""
	if s.engine != nil {
		tr, ok := s.engine.FlowTraceFor(req.AgentID, req.SessionID)
		if !ok {
			tr, ok = s.engine.LatestFlowTrace(req.AgentID)
		}
		if ok && len(tr.Entries) > 0 {
			draft, traceRepairs = studioRepairDraftFromTrace(c.Context(), model, draft, tr.Entries)
			traceRunID = tr.RunID
			studio.RepairWiring(&draft, cat)
		}
	}

	problem := "A REAL execution of this agent failed or needs debugging.\n" +
		"Agent: " + req.AgentID + "\nSession: " + req.SessionID + "\n" +
		"Error: " + strings.TrimSpace(errText) + "\n" +
		"Recent action-log evidence:\n" + evidence + "\n" +
		"Change the agent workflow/tools/prompts so this failure is prevented. Preserve the user's intent and only make targeted fixes."
	healed, changed := studio.RepairWithProblems(c.Context(), model, draft, []string{problem}, cat)
	changed = changed || len(traceRepairs) > 0
	studio.RepairWiring(&healed, cat)

	// SideEffectsReal, deliberately — same reasoning as handleStudioDiagnoseRun:
	// the defect being repaired is a REAL execution failure reconstructed from the
	// action log, and a mocked verifier would declare it fixed without ever
	// touching the thing that broke. Bounded by MaxElapsed.
	rep := studio.BuildUntilWorks(c.Context(), model, healed, cat, studio.BuildOptions{
		In:            in,
		SideEffects:   studio.SideEffectsReal,
		Verifier:      studio.VerifierFor(studio.SideEffectsReal, s.studioRealRunner()),
		ExtraProblems: s.pythonBuildProblems,
		MaxElapsed:    studio.DefaultMaxElapsed,
		MaxTokens:     s.cfg.LLM.Studio.MaxBuildTokens,
		MaxCostUSD:    s.cfg.LLM.Studio.MaxBuildCostUSD,
	})

	failingInput := studioSessionFailingInput(events, req.AgentID, req.SessionID)

	return c.JSON(fiber.Map{
		"agentId":       req.AgentID,
		"agentName":     def.Name,
		"sessionId":     req.SessionID,
		"error":         errText,
		"evidence":      evidence,
		"changed":       changed,
		"traceRunId":    traceRunID,
		"traceRepairs":  traceRepairs,
		"workflow":      rep.Workflow,
		"report":        rep,
		"preflight":     studio.Preflight(rep.Workflow, in),
		"failing_input": failingInput,
	})
}

// studioSessionFailingInput returns the text of the original user message that
// triggered this failing session, so Studio can prefill sampleInput on debug
// entry (Story 3 AC2b). Walks events newest-first for a matching message.in
// and returns the first ContentText part's text. Returns "" when the session's
// inbound message can't be reconstructed — Studio's UI keeps its default in
// that case.
func studioSessionFailingInput(events []message.Event, agentID, sessionID string) string {
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Type != "message.in" || ev.AgentID != agentID || ev.SessionID != sessionID {
			continue
		}
		var msg message.Message
		data, err := json.Marshal(ev.Payload)
		if err != nil {
			return ""
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			return ""
		}
		for _, p := range msg.Parts {
			if p.Type == message.ContentText && strings.TrimSpace(p.Text) != "" {
				return p.Text
			}
		}
		// Fall back to a stringified payload if the message shape is unusual.
		if s, ok := ev.Payload.(string); ok {
			return s
		}
		return ""
	}
	return ""
}

func studioSessionEvidence(events []message.Event, agentID, sessionID string) (evidence, errText string, found bool) {
	var lines []string
	for _, ev := range events {
		if ev.AgentID != agentID || ev.SessionID != sessionID {
			continue
		}
		found = true
		line := studioEventEvidenceLine(ev)
		if line != "" {
			lines = append(lines, line)
		}
		if candidate := studioEventErrorText(ev); candidate != "" {
			errText = candidate
		}
	}
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	return strings.Join(lines, "\n"), errText, found
}

func studioEventEvidenceLine(ev message.Event) string {
	payload := studioCompactPayload(ev.Payload, 420)
	ts := ""
	if !ev.Timestamp.IsZero() {
		ts = ev.Timestamp.UTC().Format("15:04:05") + " "
	}
	return fmt.Sprintf("- %s%s: %s", ts, ev.Type, payload)
}

func studioEventErrorText(ev message.Event) string {
	p, ok := ev.Payload.(map[string]any)
	if !ok {
		b, err := json.Marshal(ev.Payload)
		if err != nil {
			if ev.Type == "error" {
				return fmt.Sprint(ev.Payload)
			}
			return ""
		}
		_ = json.Unmarshal(b, &p)
	}
	if ev.Type == "error" {
		for _, k := range []string{"error", "message", "content"} {
			if v, ok := p[k].(string); ok && strings.TrimSpace(v) != "" {
				if stage, _ := p["stage"].(string); strings.TrimSpace(stage) != "" {
					return stage + ": " + v
				}
				return v
			}
		}
	}
	for _, k := range []string{"error", "message"} {
		if v, ok := p[k].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func studioCompactPayload(v any, max int) string {
	if max <= 0 {
		max = 400
	}
	var s string
	switch t := v.(type) {
	case string:
		s = t
	default:
		b, err := json.Marshal(v)
		if err != nil {
			s = fmt.Sprint(v)
		} else {
			s = string(b)
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// studioCompileAgentRequest is the POST /api/v1/studio/compile-agent body.
type studioCompileAgentRequest struct {
	Intent   string            `json:"intent"`
	Strategy string            `json:"strategy"` // react | plan_execute
	Catalog  studio.Catalog    `json:"catalog,omitempty"`
	Answers  map[string]string `json:"answers,omitempty"`
}

// handleStudioCompileAgent implements POST /api/v1/studio/compile-agent. It
// generates a ReAct/Plan-Execute AGENT (system prompt + tool allowlist + peers/
// skills/KBs, NO workflow) for intents that need a reasoning loop rather than a
// fixed graph (local-first pivot).
func (s *Server) handleStudioCompileAgent(c *fiber.Ctx) error {
	var req studioCompileAgentRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if strings.TrimSpace(req.Intent) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "intent is required")
	}
	s.groundCatalog(&req.Catalog)
	s.groundPreferencesFor(&req.Catalog, studioLearningOwner(c))
	s.groundGenerationProfile(&req.Catalog, req.Intent)
	advice := studio.AdviseStrategy(req.Intent, req.Catalog, req.Strategy, false)
	strategy := advice.RuntimeStrategy
	if strategy == "" {
		strategy = "auto"
	}
	if s.unreliableStrategy(req.Catalog.ActiveProvider, req.Catalog.ActiveModel, strategy) {
		return s.errMsg(c, fiber.StatusUnprocessableEntity, "the selected execution strategy is historically unreliable for the active provider/model")
	}
	res, ok := studio.CompileDeterministicAgent(req.Intent, req.Catalog, strategy, req.Answers)
	if !ok {
		return s.errMsg(c, fiber.StatusUnprocessableEntity, "deterministic agent planner could not build this agent; add at least one tool or choose a fixed workflow")
	}
	s.stampDefaultLLM(&res.Workflow)         // make the runtime provider/model explicit in the YAML
	studio.ApplyTemplateFixes(&res.Workflow) // deterministic self-heal (no-op when there's no flow graph)
	s.finalizeStudioCompileResult(c, &res, req.Catalog)
	s.issueGenerationProof(studioLearningOwner(c), &res.Workflow)
	return c.JSON(studioCompileResponseFor(res, advice))
}

// studioCompileResponse carries the parts of the Strategy Advisor's verdict that
// Draft.Recommendation{Mode, Rationale} cannot express.
//
// AdviseStrategy computes a capability warning, a confidence, and the resolved
// model profile, and the compile path then flattened all three away — so an
// operator who forced ReAct onto a model that demonstrably cannot emit
// well-formed tool arguments got a clean-looking draft and no warning, even
// though the backend had already written one. That is ST-02's "informed
// override": the override is honoured, but it stops being uninformed.
//
// The advisory is deliberately additive and never blocking.
type studioCompileResponse struct {
	studio.Result
	// CapabilityWarning is set when the chosen mode exceeds what the selected
	// model can do. Advisory: the operator may know something the registry
	// doesn't, but they should not learn it from a 3am failure.
	CapabilityWarning string `json:"capability_warning,omitempty"`
	// Confidence is how sure the advisor is of the mode it picked
	// (high | medium | low). A forced-but-unsupported mode reads "low".
	Confidence string `json:"confidence,omitempty"`
	// Capabilities is the profile the decision was based on, so the UI can show
	// WHY a mode was recommended instead of asserting it.
	Capabilities *studio.Capabilities `json:"capabilities,omitempty"`
	// StrategyMode / StrategyReason echo the advisor's own verdict. They are
	// separate from Draft.Recommendation because the recommendation records what
	// the PLANNER built, while these record what the ADVISOR decided — and when
	// those two disagree, the operator needs to see both.
	StrategyMode   string `json:"strategy_mode,omitempty"`
	StrategyReason string `json:"strategy_reason,omitempty"`
}

func studioCompileResponseFor(res studio.Result, advice studio.StrategyAdvice) studioCompileResponse {
	return studioCompileResponse{
		Result:            res,
		CapabilityWarning: advice.CapabilityWarning,
		Confidence:        advice.Confidence,
		Capabilities:      advice.Capabilities,
		StrategyMode:      advice.Mode,
		StrategyReason:    advice.Reason,
	}
}

// handleStudioCompile implements POST /api/v1/studio/compile.
func (s *Server) handleStudioCompile(c *fiber.Ctx) error {
	var req studio.Request
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if req.Intent == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "intent is required")
	}

	// Ground the compiler in the REAL installed skills + connected MCP tools
	// (authoritative, server-side) so it maps loose references to actual
	// capabilities and wires real MCP tools instead of inventing names.
	s.groundCatalog(&req.Catalog)
	s.groundPreferencesFor(&req.Catalog, studioLearningOwner(c))
	s.groundGenerationProfile(&req.Catalog, strings.TrimSpace(req.Intent+" "+req.RawIntent))

	// SINGLE authoritative architecture decision, evaluated over the raw + refined
	// text (the SAME input refine used) so it can't diverge by entry path. A
	// reasoning task (dynamic skill routing, async polling, per-item loops, or an
	// explicit multi-phase plan) is built as an AGENT here — never a fixed
	// workflow. This is the server-side guarantee behind "if it can't be a
	// workflow, don't build one": even if the client calls /compile, the server
	// returns the agent, so the user never gets a workflow carrying a "use ReAct"
	// sticker (or a brittle multi-agent flow with invented peers).
	// The user can force a fixed workflow (explicit Workflow-mode toggle). An
	// explicit human choice overrides the reasoning-fit heuristic below —
	// without this, RecommendAgentMode kept reverting their pick to an agent.
	advice := studio.AdviseStrategy(req.Intent+" "+req.RawIntent, req.Catalog, "", req.ForceWorkflow)
	chosen := advice.RuntimeStrategy
	if advice.Mode == "workflow" {
		chosen = "workflow"
	}
	if s.unreliableStrategy(req.Catalog.ActiveProvider, req.Catalog.ActiveModel, chosen) {
		// Historical evidence is an authoritative backend guard, not merely UI
		// decoration. Prefer Auto when it remains reliable; otherwise refuse to
		// manufacture a draft with a strategy known to fail for this model.
		if chosen != "auto" && !s.unreliableStrategy(req.Catalog.ActiveProvider, req.Catalog.ActiveModel, "auto") {
			advice.Mode = "auto"
			advice.RuntimeStrategy = "auto"
			advice.Reason = "Studio selected Auto because local run history marks " + chosen + " unreliable for the active model."
			advice.CapabilityWarning = advice.Reason
		} else {
			return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{
				"error":    "the selected execution strategy is historically unreliable for the active provider/model",
				"provider": req.Catalog.ActiveProvider, "model": req.Catalog.ActiveModel, "strategy": chosen,
			})
		}
	}

	// The MODEL designs, grounded in the whole catalogue; the deterministic
	// planner is the fallback.
	//
	// This path used to be deterministic-only ("Studio no longer asks the builder
	// model to invent graph JSON"), while the streamed pipeline had already been
	// inverted to model-first. Two entry points, two policies — so whether the
	// builder model was consulted at all depended on which button was pressed,
	// and pressing Generate produced a graph built from keyword patterns that
	// cannot see MCP servers or skills.
	res, designedByModel, err := s.studioDesignGraph(c, req.Intent, req.Catalog, advice, req.Answers)
	if err != nil {
		return s.errMsg(c, fiber.StatusUnprocessableEntity, err.Error())
	}
	s.stampDefaultLLM(&res.Workflow)
	studio.ApplyTemplateFixes(&res.Workflow)
	if !designedByModel {
		if short := studio.CoverageShortfall(req.Intent, req.Catalog, res); short != "" {
			res.Notes = append(res.Notes,
				"Heads up: the deterministic planner built this graph and "+short+
					". It will run, but it is not using a capability you asked for — review the steps before saving.")
		}
	}
	s.finalizeStudioCompileResult(c, &res, req.Catalog)
	s.issueGenerationProof(studioLearningOwner(c), &res.Workflow)
	return c.JSON(studioCompileResponseFor(res, advice))
}

// studioDesignGraph is the ONE graph-design policy, shared by the synchronous
// compile handler and anything else that needs a draft outside the streamed
// pipeline. Keeping it in one place is the point: the two entry points had
// drifted to opposite defaults, and a user could not tell which they had hit.
//
// Model first, contract-checked, deterministic planner as the floor.
func (s *Server) studioDesignGraph(
	c *fiber.Ctx,
	intent string,
	cat studio.Catalog,
	advice studio.StrategyAdvice,
	answers map[string]string,
) (studio.Result, bool, error) {
	strategy := advice.RuntimeStrategy
	if strategy == "" && advice.Mode != "workflow" {
		strategy = "auto"
	}
	deterministic := func() (studio.Result, bool) {
		if advice.Mode == "workflow" {
			return studio.CompileDeterministicWorkflow(intent, cat, answers)
		}
		return studio.CompileDeterministicAgent(intent, cat, strategy, answers)
	}

	// Compute the curated graph FIRST. It costs nothing (no model call) and it
	// serves two purposes: it is the fallback, and — when it encodes a real
	// multi-step procedure rather than a generic skeleton — it is handed to the
	// model as a worked example so the model designs from a shape known to work.
	detRes, detOK := deterministic()

	// Why the builder model produced nothing, kept for the fallback note. The
	// old code discarded lerr, so every model failure — a dead provider, a
	// context cancellation, unparseable JSON, a refusal — arrived at the user as
	// the same shrug. Nothing in the response, the notes or the logs said which,
	// which made the one failure that matters most the only one nobody could
	// diagnose.
	modelErr := ""

	// Design on a context of our own, not the request's.
	//
	// Every graph the model built for a fan-out request arrived fine; roughly one
	// attempt in four came back as:
	//
	//	ollama-cloud: request failed: Post ".../chat/completions": context canceled
	//
	// Cancelled, not timed out, and not the provider's doing. It cannot have been
	// the client giving up either: that same request returned 200 to a caller
	// still waiting on it — with the straight-line template, because the model
	// call had been killed underneath. So the request context died while the
	// handler carrying it kept running, and a 40-second generation was thrown
	// away for it.
	//
	// This file already knows the shape of that problem: the streamed generate
	// and Run Live both detach with context.WithoutCancel and say why. The
	// single most expensive call in Studio was the one still passing c.Context()
	// straight to the model. The timeout keeps a genuinely stuck call bounded —
	// generously, since the slowest honest generation observed was 96s.
	designCtx, cancelDesign := context.WithTimeout(detachedRequestContext(c), 5*time.Minute)
	defer cancelDesign()

	if model := s.studioLLM(c); model != nil {
		designCat := cat
		if detOK && studio.EncodesProcedure(detRes) {
			if ref, mErr := json.MarshalIndent(detRes.Workflow, "", "  "); mErr == nil {
				designCat.ReferenceGraph = string(ref)
			}
		}
		var res studio.Result
		var lerr error
		if advice.Mode == "workflow" {
			res, lerr = studio.Compile(designCtx, model, intent, designCat, answers)
		} else {
			res, lerr = studio.CompileAgent(designCtx, model, intent, designCat, strategy, answers)
		}
		if lerr != nil {
			modelErr = lerr.Error()
			s.log.Warn("studio: builder model produced no graph",
				zap.String("mode", advice.Mode), zap.Error(lerr))
		}
		if lerr == nil {
			// Structure retry, shared with the streamed pipeline. A graph that
			// flattens a described fan-out into one step is structurally valid and
			// passes every check below, so nothing here would otherwise notice.
			if StructureShortfallSeen := studio.StructureShortfall(intent, res); StructureShortfallSeen != "" {
				res, _, _ = studio.RetryForStructure(intent, res, func(rc studio.Catalog) (studio.Result, error) {
					if advice.Mode == "workflow" {
						return studio.Compile(designCtx, model, intent, rc, answers)
					}
					return studio.CompileAgent(designCtx, model, intent, rc, strategy, answers)
				}, designCat)
			}
			in := s.preflightInput(c, cat)
			if contract := studio.AssessContract(res.Workflow, cat, in); contract.Blockers > 0 {
				// Repair before discarding: most of what a weak builder model gets
				// wrong is structural, not a bad choice of capabilities.
				studio.RepairContractStructure(&res.Workflow, intent, cat, answers, contract)
				studio.RepairWiring(&res.Workflow, cat)
				contract = studio.AssessContract(res.Workflow, cat, in)
				if contract.Blockers > 0 {
					// Same decision as the streamed pipeline, taken by the same
					// function.
					//
					// This was a second copy of the rule, and it had only the
					// coverage half: keep the model's graph when falling back would
					// cost a named capability. So fixing the streamed path left this
					// one — the path the Workflow button actually uses, via
					// /studio/compile — still discarding a 1-blocker graph for a
					// 2-blocker skeleton. Live, that is exactly what happened: the
					// streamed run reported "Keeping the model's graph despite its
					// blockers", and the very next Workflow-mode run through this
					// handler produced the canned two-node graph again.
					//
					// One function, both callers, so the next change cannot land in
					// only one of them.
					if !detOK {
						// Nothing to fall back to. Discarding here dropped the
						// model's graph on the floor and returned "describe the
						// source, transform, and delivery steps more explicitly" —
						// which is both untrue (a graph was built) and unactionable
						// (it names nothing to change). A graph carrying blockers
						// the UI already lists, next to a Save button those blockers
						// already gate, is strictly more use than no graph.
						//
						// This is KeepModelGraph's own rule at its limit: do not
						// throw the model's work away for an alternative that is not
						// better. No alternative at all cannot be better.
						res.Notes = append(res.Notes,
							"This graph has unresolved blockers and there is no curated alternative for this shape, "+
								"so it is shown as built. Fix the blockers listed below rather than regenerating.")
						return res, true, nil
					}
					detC := studio.AssessContract(detRes.Workflow, cat, in)
					if _, note := studio.KeepModelGraph(
						studio.CoverageShortfall(intent, cat, res),
						studio.CoverageShortfall(intent, cat, detRes),
						contract.Blockers, detC.Blockers,
					); note != "" {
						res.Notes = append(res.Notes,
							"This graph still has unresolved blockers, kept because "+note+
								". Fix the blockers rather than regenerating.")
						return res, true, nil
					}
				} else {
					return res, true, nil
				}
			} else {
				return res, true, nil
			}
		}
	}

	if detOK {
		return detRes, false, nil
	}
	if advice.Mode == "workflow" {
		// Last resort. The curated templates declined because they cannot build
		// the shape this intent describes, and the builder model has produced
		// nothing at all — so the choice is no longer "right graph or wrong
		// graph", it is "wrong graph or no graph".
		//
		// A straight-line template the user can open, read and rewire on the
		// canvas beats an error telling them to describe it more explicitly,
		// which names nothing to change and is untrue besides — the request was
		// perfectly explicit, it just asked for a shape no template has.
		//
		// What made the original failure bad was not the graph, it was the
		// silence: pattern_matched, confidence "high", next_action "save". So
		// this says out loud what it is and what it is missing.
		if fallback, ok := studio.CompileDeterministicWorkflowIgnoringShape(intent, cat, answers); ok {
			fallback.Notes = append(fallback.Notes,
				"The builder model did not return a usable graph"+becauseOf(modelErr)+
					", so this is Soulacy's curated template for this kind of job. It runs its steps one "+
					"after another and does NOT contain the parallel specialists you described — wire them "+
					"in on the canvas, or press Generate again.")
			return fallback, false, nil
		}
		return studio.Result{}, false, fmt.Errorf(
			"the builder model could not produce a graph for this request%s, and no curated template matches its shape",
			becauseOf(modelErr))
	}
	return studio.Result{}, false, fmt.Errorf(
		"could not build this agent%s; add at least one tool or choose a fixed workflow", becauseOf(modelErr))
}

// becauseOf renders a model failure as a clause that can be dropped into a
// sentence, and nothing at all when there was no failure to report. Truncated
// because a provider error can carry a whole response body, and a note the user
// cannot read to the end is no better than no note.
func becauseOf(modelErr string) string {
	modelErr = strings.TrimSpace(modelErr)
	if modelErr == "" {
		return ""
	}
	return " (" + truncate(modelErr, 300) + ")"
}

// finalizeStudioCompileResult attaches the same deterministic contract used by
// Save to every generated Studio draft. This makes generation a gated authoring
// step: the UI can show blockers immediately, and save-time enforcement cannot
// disagree with what the user already saw on the canvas.
func (s *Server) finalizeStudioCompileResult(c *fiber.Ctx, res *studio.Result, cat studio.Catalog) {
	if res == nil {
		return
	}
	s.finalizeStudioResult(res, cat, s.preflightInput(c, cat))
}

// finalizeStudioResult is the ctx-free core of finalizeStudioCompileResult. It
// exists so the STREAMED generate path can run the identical finalization from
// inside its producer goroutine, where the fiber ctx is no longer safe to touch
// (the handler has already returned to take over the connection as a stream
// writer). Any field the sync path attaches to a Result must be attached here —
// that is the whole point: streamed and sync drafts must not disagree about
// whether a draft is runnable.
func (s *Server) finalizeStudioResult(res *studio.Result, cat studio.Catalog, in studio.PreflightInput) {
	if res == nil {
		return
	}
	// Generated graphs must cross the same deterministic repair boundary as
	// manually edited drafts. In particular, a parallel fan-out can imply its
	// join barrier from the graph even when the builder omitted join_node.
	studio.RepairWiring(&res.Workflow, cat)
	pf := studio.Preflight(res.Workflow, in)
	if res.Explanation != nil {
		res.Explanation.NeedsConfig = preflightLines(pf)
	}
	contract := studio.AssessContract(res.Workflow, cat, in)
	res.Contract = &contract
}

// applyLocalPreset fills patient timeout/turn defaults on an agent that will run
// on a LOCAL model, but only where the draft didn't already set them (Stories
// #23/#24). No-op for cloud-bound agents — the engine's defaults are fine there.
//
// Story 9 (Cohort B) — when the operator has picked an intent-named preset
// via the GUI (`llm.studio.preset` in config), we prefer that over the
// model-derived defaults. Explicit intent > model heuristic > engine default.
// "cloud_quality" applies even for cloud providers so the operator can lean
// into longer plans without editing YAML.
func (s *Server) applyLocalPreset(def *agent.Definition) {
	provider := def.LLM.Provider
	if provider == "" {
		provider = s.cfg.LLM.DefaultProvider
	}
	baseURL := ""
	if pc, ok := s.cfg.LLM.Providers[provider]; ok {
		baseURL = pc.BaseURL
	}
	intent := strings.TrimSpace(s.cfg.LLM.Studio.Preset)
	// Cloud-quality is a deliberate choice for cloud runs; other intents keep
	// the local-only gate to avoid mistakenly applying local-tuned generous
	// timeouts to a cloud model where it's usually money-per-token.
	local := studio.IsLocalProvider(provider, baseURL)
	var p studio.ModelPreset
	var have bool
	if intent != "" {
		if pp, ok := studio.LookupIntentPreset(intent); ok && (local || intent == studio.IntentPresetCloudQuality) {
			p = pp
			have = true
		}
	}
	if !have {
		if !local {
			return
		}
		p = studio.LocalPresetFor(def.LLM.Model)
	}
	if def.RunTimeout == "" && p.RunTimeout != "" {
		def.RunTimeout = p.RunTimeout
	}
	if def.Reasoning.StepTimeout == "" && p.StepTimeout != "" {
		def.Reasoning.StepTimeout = p.StepTimeout
	}
	if def.Reasoning.TotalTimeout == "" && p.TotalTimeout != "" {
		def.Reasoning.TotalTimeout = p.TotalTimeout
	}
	if def.MaxTurns == 0 && p.MaxTurns > 0 {
		def.MaxTurns = p.MaxTurns
	}
}

// handleStudioPresets returns the intent-named preset catalog (Story 9). The
// GUI renders these three names alongside the model picker so operators can
// pick a runtime intent — "fast local", "reliable local", "cloud quality" —
// without editing YAML or knowing the model tier heuristics.
func (s *Server) handleStudioPresets(c *fiber.Ctx) error {
	current := strings.TrimSpace(s.cfg.LLM.Studio.Preset)
	return c.JSON(fiber.Map{
		"presets": studio.ListIntentPresets(),
		"current": current,
	})
}

// preflightLine renders a preflight issue as a single human-readable line for
// the explanation's NeedsConfig list.
func preflightLine(i studio.PreflightIssue) string {
	if i.Fix != "" {
		return i.Message + " " + i.Fix
	}
	return i.Message
}

func preflightLines(pf studio.PreflightResult) []string {
	var needs []string
	for _, b := range pf.Blockers {
		needs = append(needs, preflightLine(b))
	}
	for _, w := range pf.Warnings {
		needs = append(needs, preflightLine(w))
	}
	return needs
}

// studioTestRequest is the POST /api/v1/studio/test body. Mocks, Assertions,
// and Mode are optional (M5, Stories S5.2/S5.3): existing {workflow,input}
// callers keep working unchanged.
//
//	{ workflow, input,
//	  mocks?:      {<nodeId>: <output>},
//	  assertions?: [{target, op, value}],
//	  mode?:       "dry" }
type studioTestRequest struct {
	Workflow   studio.Draft               `json:"workflow"`
	Input      string                     `json:"input"`
	Mocks      map[string]json.RawMessage `json:"mocks,omitempty"`
	Assertions []studio.Assertion         `json:"assertions,omitempty"`
	Mode       string                     `json:"mode,omitempty"`
	// ST-10 test inputs: named values seeded alongside the trigger, a test-run
	// environment exposed as .env, and an optional entry point so a long
	// pipeline can be iterated from the step that is actually broken.
	Variables   map[string]string `json:"variables,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	StartNode   string            `json:"start_node,omitempty"`
}

// handleStudioTest implements POST /api/v1/studio/test. It dry-runs the
// draft workflow through studio.TestRun (a mock node runner — no real
// tools/agents/LLM) and returns the per-node trace, the final result, the
// evaluated assertions, an aggregate passed flag, the echoed mode, and any
// warnings. Per-node mock overrides and assertions are optional.
func (s *Server) handleStudioTest(c *fiber.Ctx) error {
	var req studioTestRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}

	opts := &studio.TestOptions{
		Mocks:       req.Mocks,
		Assertions:  req.Assertions,
		Mode:        req.Mode,
		Variables:   req.Variables,
		Environment: req.Environment,
		StartNode:   req.StartNode,
	}
	res, err := studio.TestRun(c.Context(), req.Workflow, req.Input, opts)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	return c.JSON(res)
}

// studioValidateRequest is the POST /api/v1/studio/validate body. The canvas
// posts the in-progress draft and gets back structured errors + warnings.
type studioValidateRequest struct {
	Workflow studio.Draft `json:"workflow"`
}

// handleStudioValidate implements POST /api/v1/studio/validate. It validates
// the draft's flow graph (Story M3) and returns structured errors + soft
// warnings the canvas surfaces while the user edits. The handler is thin —
// all logic lives in studio.Validate, which NEVER fails on a bad graph (a bad
// graph is reported as data), so this endpoint never 500s on workflow content.
func (s *Server) handleStudioValidate(c *fiber.Ctx) error {
	var req studioValidateRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	res := studio.Validate(req.Workflow)
	// Argument-schema check against the live catalog: flag a tool node passing an
	// argument the tool doesn't accept (the "unexpected keyword argument" class),
	// before it fails at run time.
	res.Warnings = append(res.Warnings, studio.ValidateToolArgs(req.Workflow, s.studioCatalogSnapshot())...)
	// Python validity: syntax-check every inline python node and require the
	// run(inputs) entrypoint — catches broken generated code at build time
	// instead of at run time. Parse-only; never executes the code.
	if pyErrs := s.validatePythonNodes(req.Workflow); len(pyErrs) > 0 {
		res.Ok = false
		res.Errors = append(res.Errors, pyErrs...)
	}
	return c.JSON(res)
}

// studioPlanRequest is the POST /api/v1/studio/plan body.
type studioPlanRequest struct {
	Workflow studio.Draft `json:"workflow"`
}

// handleStudioPlan implements POST /api/v1/studio/plan. It classifies the
// agent the draft would become (capability tier) and reports whether saving
// would create a Privileged channel exposure that needs the operator's
// consent. Pure decision: nothing is persisted. The handler is thin — all
// logic lives in studio.Plan so it stays unit-testable.
func (s *Server) handleStudioPlan(c *fiber.Ctx) error {
	var req studioPlanRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}

	res, err := studio.Plan(req.Workflow)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	return c.JSON(res)
}

// studioYAMLRequest carries a draft to serialize into SOUL.yaml (the same form
// a Save would write), for the Studio "Code" view.
type studioYAMLRequest struct {
	Workflow studio.Draft `json:"workflow"`
}

// handleStudioYAML implements POST /api/v1/studio/yaml. It converts the current
// draft into the exact agent.Definition a Save would persist, then returns it
// marshalled as SOUL.yaml so the GUI can show (and let the user edit) the code
// behind the canvas. Conversion errors (e.g. an unnamed workflow) come back as
// 400s with a clear message.
func (s *Server) handleStudioYAML(c *fiber.Ctx) error {
	var req studioYAMLRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	def, err := studio.ToAgentDefinition(req.Workflow, true)
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}
	out, err := yaml.Marshal(&def)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.JSON(fiber.Map{"yaml": string(out)})
}

// studioFromYAMLRequest carries edited SOUL.yaml to parse back into a draft for
// the canvas.
type studioFromYAMLRequest struct {
	YAML string `json:"yaml"`
}

// handleStudioFromYAML implements POST /api/v1/studio/from-yaml. The Code view is
// authoritative, so when the user switches back to Canvas we parse the edited
// SOUL.yaml into an agent.Definition and map it onto a Studio draft. Because the
// draft⇄definition mapping is intentionally lossy (the canvas shows the flow
// graph, not every agent field), we also return human-readable warnings naming
// anything in the YAML that the canvas can't represent — so the user knows the
// YAML remains the source of truth for those parts.
func (s *Server) handleStudioFromYAML(c *fiber.Ctx) error {
	var req studioFromYAMLRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if strings.TrimSpace(req.YAML) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "YAML is empty")
	}
	var def agent.Definition
	if err := yaml.Unmarshal([]byte(req.YAML), &def); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "YAML error: "+err.Error())
	}
	draft := studio.FromAgentDefinition(def)

	// Warnings must reflect what the draft⇄definition mapping ACTUALLY preserves,
	// or they mislead the user about data loss. A reasoning agent round-trips its
	// system prompt, tools, skills, knowledge and peers losslessly (they're shown/
	// editable in the agent panel), so none of those warrant a "kept in YAML"
	// warning. Only a fixed WORKFLOW agent has canvas-invisible fields.
	strat := strings.ToLower(strings.TrimSpace(def.Reasoning.Strategy))
	isReasoningAgent := strat == "react" || strat == "plan_execute"
	var warnings []string
	switch {
	case isReasoningAgent:
		warnings = append(warnings, "This is a reasoning agent (no fixed graph) — edit its prompt, tools and skills in the agent panel or here in SOUL.yaml; everything round-trips.")
	case def.Workflow == nil || len(def.Workflow.Nodes) == 0:
		warnings = append(warnings, "This agent has no workflow graph and no reasoning strategy — it's incomplete. Add steps on the canvas, or switch it to a ReAct agent.")
	default:
		if strings.TrimSpace(def.SystemPrompt) != "" {
			warnings = append(warnings, "This workflow's system prompt is generated from its steps; a canvas re-save will regenerate it from the graph.")
		}
	}
	return c.JSON(fiber.Map{"workflow": draft, "warnings": warnings})
}

// handleStudioSaveYAML implements POST /api/v1/studio/save-yaml. In Code view the
// YAML is authoritative, so this writes it to disk directly (parse → validate →
// loader.Upsert) rather than re-deriving from the draft — preserving fields the
// canvas can't express. Privileged-node consent is still enforced fail-closed by
// the runtime, and Studio saves stay disabled unless the YAML says otherwise.
func (s *Server) handleStudioSaveYAML(c *fiber.Ctx) error {
	var req studioFromYAMLRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if strings.TrimSpace(req.YAML) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "YAML is empty")
	}
	var def agent.Definition
	if err := yaml.Unmarshal([]byte(req.YAML), &def); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "YAML error: "+err.Error())
	}
	if strings.TrimSpace(def.ID) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "the YAML needs an 'id' field before it can be saved")
	}
	if isProtectedSystemAgent(def.ID) {
		return protectedSystemAgentResponse(c)
	}

	report := agentvalidate.Definition(&def, "", s.agentValidationOptions(c.Context()), agentvalidate.Report{})
	if report.Errors > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error":      "validation failed",
			"validation": report,
		})
	}

	if def.LLM.Provider == "" {
		def.LLM.Provider = s.cfg.LLM.DefaultProvider
	}
	s.applyLocalPreset(&def)

	dir := ""
	if len(s.cfg.AgentDirs) > 0 {
		dir = s.cfg.AgentDirs[0]
	}
	// If this agent already exists, write back into its OWN directory and carry
	// its source path, so Save updates the agent in place instead of dropping a
	// duplicate copy under the first configured agent dir. SourcePath is
	// <baseDir>/<id>/SOUL.yaml, so the base dir is the parent of the agent dir.
	if existing := s.loader.Get(def.ID); existing != nil && existing.SourcePath != "" {
		def.SourcePath = existing.SourcePath
		dir = filepath.Dir(filepath.Dir(existing.SourcePath))
	}
	// BEFORE the write, not after: ensurePeerAgents adds any missing peer to
	// def.Agents, and Upsert is what serialises def to disk. Running it after
	// left the declaration in the loader's in-memory copy only — Get() saw it,
	// the file did not, and the peer list came back empty on the next restart.
	//
	// The code view saves the SAME workflow as the wizard, so it owes the same
	// guarantee. There is no draft on this path, so every missing profile is
	// synthesized from the node itself.
	createdPeers, perr := s.ensurePeerAgents(dir, &def, nil)
	if perr != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, perr)
	}
	if err := s.loader.Upsert(dir, &def); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.scheduler.DeregisterAgent(def.ID)
	if err := s.scheduler.RegisterAgent(&def); err != nil {
		s.log.Warn("scheduler registration failed", zap.String("agent", def.ID), zap.Error(err))
	}
	return c.JSON(fiber.Map{"id": def.ID, "agent": &def, "validation": report, "peerAgents": createdPeers})
}

// yamlValidateItem is one problem found validating SOUL.yaml, normalized across
// the three checkers so the GUI can render a single list. Source says which
// layer found it; Severity is "error" (must fix) or "warning" (should review).
type yamlValidateItem struct {
	Severity string `json:"severity"` // error | warning
	Source   string `json:"source"`   // yaml | definition | graph | runtime
	NodeID   string `json:"nodeId,omitempty"`
	Message  string `json:"message"`
	Fix      string `json:"fix,omitempty"`
}

// handleStudioValidateYAML implements POST /api/v1/studio/validate-yaml — the
// "Validate" button in the Code view. It runs the FULL battery against the
// edited SOUL.yaml so problems are caught before save/run:
//   - YAML syntax (it parses at all),
//   - definition correctness (agentvalidate: required fields, tool/channel sanity),
//   - graph integrity (studio.Validate → reasoning.CompileFlow: dangling edges,
//     bad entry/output, unreachable nodes),
//   - runtime-error avoidance (studio.Preflight against LIVE state: missing/
//     disconnected MCP servers, unfilled required tool args, unconfigured
//     channels, invalid schedules, and template-reference bugs like passing a
//     whole object where a scalar id is needed).
//
// It always returns 200 with a consolidated report (problems are data, not HTTP
// errors) so the UI can list them inline.
func (s *Server) handleStudioValidateYAML(c *fiber.Ctx) error {
	var req studioFromYAMLRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if strings.TrimSpace(req.YAML) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "YAML is empty")
	}

	var items []yamlValidateItem
	add := func(sev, src, node, msg, fix string) {
		items = append(items, yamlValidateItem{Severity: sev, Source: src, NodeID: node, Message: msg, Fix: fix})
	}

	// 1) Syntax. A parse failure is terminal — nothing else can run.
	var def agent.Definition
	if err := yaml.Unmarshal([]byte(req.YAML), &def); err != nil {
		add("error", "yaml", "", "YAML syntax error: "+err.Error(), "Fix the indentation, quoting, or a stray colon, then validate again.")
		return c.JSON(buildYAMLValidation(items, nil))
	}

	// 2) Definition correctness.
	if strings.TrimSpace(def.ID) == "" {
		add("error", "definition", "", "Missing required field 'id'.", "Add a top-level 'id:' so the agent can be saved and referenced.")
	}
	rep := agentvalidate.Definition(&def, "", s.agentValidationOptions(c.Context()), agentvalidate.Report{})
	for _, f := range rep.Findings {
		sev := "warning"
		if f.Severity == agentvalidate.Error {
			sev = "error"
		}
		msg := f.Message
		if strings.TrimSpace(f.Field) != "" {
			msg = f.Field + ": " + msg
		}
		add(sev, "definition", "", msg, f.Suggestion)
	}

	// 3) Graph integrity + 4) runtime checks operate on the flow form. Only run
	// the graph compiler when there IS a graph (a reasoning/ReAct agent has none;
	// its correctness is covered by the definition checks above).
	draft := studio.FromAgentDefinition(def)
	if def.Workflow != nil && len(def.Workflow.Nodes) > 0 {
		vr := studio.Validate(draft)
		for _, e := range vr.Errors {
			add("error", "graph", e.NodeID, e.Message, "Fix the workflow graph (edges, entry/output, node ids).")
		}
		for _, w := range vr.Warnings {
			add("warning", "graph", w.NodeID, w.Message, "")
		}
	}
	cat := s.studioCatalogSnapshot()
	s.groundCatalog(&cat)
	pf := studio.Preflight(draft, s.preflightInput(c, cat))
	for _, b := range pf.Blockers {
		add("error", "runtime", b.NodeID, b.Message, b.Fix)
	}
	for _, w := range pf.Warnings {
		add("warning", "runtime", w.NodeID, w.Message, w.Fix)
	}

	// One-click auto-fixes for the template-reference warnings.
	fixes := studio.SuggestTemplateFixes(draft)

	return c.JSON(buildYAMLValidation(items, fixes))
}

// buildYAMLValidation tallies the items into the response envelope, including any
// machine-applicable template fixes for the GUI's "Fix" button.
func buildYAMLValidation(items []yamlValidateItem, fixes []studio.TemplateFix) fiber.Map {
	errors, warnings := 0, 0
	for _, it := range items {
		if it.Severity == "error" {
			errors++
		} else {
			warnings++
		}
	}
	if items == nil {
		items = []yamlValidateItem{}
	}
	if fixes == nil {
		fixes = []studio.TemplateFix{}
	}
	return fiber.Map{"ok": errors == 0, "errors": errors, "warnings": warnings, "items": items, "fixes": fixes}
}

// issueLine formats one validation problem for the LLM fixer prompt.
func issueLine(sev, node, msg string) string {
	if strings.TrimSpace(msg) == "" {
		return ""
	}
	if strings.TrimSpace(node) != "" {
		return sev + " [" + node + "]: " + msg
	}
	return sev + ": " + msg
}

// handleStudioFixYAML implements POST /api/v1/studio/fix-yaml — the "Fix with
// AI" button. It collects the current validation problems and asks the framework
// LLM to rewrite the SOUL.yaml so they're resolved, then returns the corrected
// (and parse-checked) document. Unlike the deterministic auto-fix, the model can
// pick the right field and restructure, so it handles cases a string edit can't.
func (s *Server) handleStudioFixYAML(c *fiber.Ctx) error {
	var req studioFromYAMLRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if strings.TrimSpace(req.YAML) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "YAML is empty")
	}
	model := s.studioLLM(c)
	if model == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "LLM router unavailable")
	}

	var def agent.Definition
	if err := yaml.Unmarshal([]byte(req.YAML), &def); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "YAML error: "+err.Error())
	}
	draft := studio.FromAgentDefinition(def)

	// Gather every problem (graph + runtime + definition) to feed the model.
	var issues []string
	add := func(sev, node, msg string) {
		if line := issueLine(sev, node, msg); line != "" {
			issues = append(issues, line)
		}
	}
	if def.Workflow != nil && len(def.Workflow.Nodes) > 0 {
		vr := studio.Validate(draft)
		for _, e := range vr.Errors {
			add("ERROR", e.NodeID, e.Message)
		}
		for _, w := range vr.Warnings {
			add("WARNING", w.NodeID, w.Message)
		}
	}
	cat := s.studioCatalogSnapshot()
	s.groundCatalog(&cat)
	pf := studio.Preflight(draft, s.preflightInput(c, cat))
	for _, b := range pf.Blockers {
		add("ERROR", b.NodeID, b.Message)
	}
	for _, w := range pf.Warnings {
		add("WARNING", w.NodeID, w.Message)
	}
	rep := agentvalidate.Definition(&def, "", s.agentValidationOptions(c.Context()), agentvalidate.Report{})
	for _, f := range rep.Findings {
		add(strings.ToUpper(string(f.Severity)), f.Field, f.Message)
	}
	if len(issues) == 0 {
		return c.JSON(fiber.Map{"yaml": req.YAML, "changed": false})
	}

	prompt := studio.BuildYAMLFixInstruction(req.YAML, issues, s.soulRules())
	raw, err := model.Complete(c.Context(), prompt)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	fixed := studio.CleanYAMLOutput(raw)
	if strings.TrimSpace(fixed) == "" {
		return s.errMsg(c, fiber.StatusInternalServerError, "the model did not return a corrected file")
	}
	// Make sure the model returned parseable YAML before handing it back.
	var check agent.Definition
	if err := yaml.Unmarshal([]byte(fixed), &check); err != nil {
		return s.errMsg(c, fiber.StatusUnprocessableEntity, "the AI returned invalid YAML; please fix manually or try again")
	}

	// Deterministic safety net on the AI's output: a weaker model's rewrite must
	// not be allowed to REINTRODUCE the bugs the deterministic layers guarantee
	// against. Re-run wiring repair + template-ref heal on the corrected flow
	// before returning, so Fix-with-AI can only ever improve correctness.
	if check.Workflow != nil && len(check.Workflow.Nodes) > 0 {
		d := studio.Draft{Flow: studio.Flow{
			Nodes:  check.Workflow.Nodes,
			Edges:  check.Workflow.Edges,
			Entry:  check.Workflow.Entry,
			Output: check.Workflow.Output,
		}}
		cat := s.studioCatalogSnapshot()
		s.groundCatalog(&cat)
		studio.RepairWiring(&d, cat)
		studio.ApplyTemplateFixes(&d)
		check.Workflow.Nodes = d.Flow.Nodes
		check.Workflow.Edges = d.Flow.Edges
		check.Workflow.Entry = d.Flow.Entry
		check.Workflow.Output = d.Flow.Output
		if out, merr := yaml.Marshal(&check); merr == nil {
			fixed = strings.TrimSpace(string(out))
		}
	}
	return c.JSON(fiber.Map{"yaml": fixed, "changed": fixed != req.YAML})
}

// handleStudioReviewYAML implements POST /api/v1/studio/review-yaml — the
// rules-grounded LLM review. It complements the deterministic validator: the
// model checks the YAML against the (editable) rulebook and reports judgment-call
// problems a linter can't (wrong field/id, broken logic). Returns findings as
// items shaped like the validator's so the GUI merges them into one panel.
func (s *Server) handleStudioReviewYAML(c *fiber.Ctx) error {
	var req studioFromYAMLRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if strings.TrimSpace(req.YAML) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "YAML is empty")
	}
	model := s.studioLLM(c)
	if model == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "LLM router unavailable")
	}
	// Only review parseable YAML — syntax is the deterministic validator's job.
	var def agent.Definition
	if err := yaml.Unmarshal([]byte(req.YAML), &def); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "YAML error: "+err.Error())
	}

	prompt := studio.BuildYAMLReviewInstruction(req.YAML, s.soulRules())
	raw, err := model.Complete(c.Context(), prompt)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	findings := studio.ParseReviewFindings(raw)
	items := make([]yamlValidateItem, 0, len(findings))
	for _, f := range findings {
		items = append(items, yamlValidateItem{
			Severity: f.Severity, Source: "ai", NodeID: f.NodeID, Message: f.Message, Fix: f.Fix,
		})
	}
	return c.JSON(fiber.Map{"items": items, "count": len(items)})
}

// studioSaveRequest is the POST /api/v1/studio/save body. AcceptPrivilegedExposure
// is the operator's consent to expose a Privileged-tier workflow on a bound
// channel; it is required (true) when studio.Plan reports requiresConsent.
type studioSaveRequest struct {
	Workflow                 studio.Draft  `json:"workflow"`
	InitialWorkflow          *studio.Draft `json:"initial_workflow,omitempty"`
	AcceptPrivilegedExposure bool          `json:"acceptPrivilegedExposure"`
	// Grants carries the per-node code consent collected by the Studio consent
	// dialog (§13). One entry per beyond-guardrail Custom Python node.
	Grants []studioGrant `json:"grants,omitempty"`
	// AcceptWarningsReason is the operator's justification for saving past a
	// warnings-only readiness report (ST-16). Recorded in the audit log, because
	// a one-click bypass leaves "why was this deployed with a known warning?"
	// unanswerable — usually at the moment someone most needs the answer.
	// Blockers are never bypassable, so this only ever explains warnings.
	AcceptWarningsReason string `json:"accept_warnings_reason,omitempty"`
}

// studioGrant is one per-node code-consent grant from the save request.
type studioGrant struct {
	NodeID       string   `json:"nodeId"`
	Hash         string   `json:"hash"`
	Capabilities []string `json:"capabilities"`
	Scope        string   `json:"scope"`
}

// handleStudioSave implements POST /api/v1/studio/save. It converts the
// draft into a DISABLED agent.Definition and persists it via the same
// loader.Upsert path the create-agent handler uses, then returns the new
// agent id. The agent is saved with Enabled=false so the operator reviews
// and enables it explicitly.
func (s *Server) handleStudioSave(c *fiber.Ctx) error {
	var req studioSaveRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}

	// Authoritative pre-create gate: never persist a Studio draft that fails the
	// same generation contract shown on the canvas. The GUI runs this before
	// Save, but enforcing it here protects imports, stale tabs, and alternate API
	// clients from creating an agent that is born broken and only fails later.
	cat := s.studioCatalogSnapshot()
	s.groundCatalog(&cat)
	in := s.preflightInput(c, cat)
	// Judge the draft the RUNTIME will see, not the one the client sent. A draft
	// is allowed to omit provider/model and inherit the workspace default — Run
	// Live and Try Agent both resolve it that way — but the save gate did not,
	// so Studio refused to save an agent it had just told the user was runnable.
	// Resolving here also means the saved YAML names its provider/model outright
	// instead of depending on a workspace default that can change under it.
	req.Workflow = s.studioDraftWithRuntimeLLM(req.Workflow)
	saveStrategy := strings.TrimSpace(req.Workflow.Strategy)
	if req.Workflow.Flow.Nodes != nil && !req.Workflow.IsAgent() {
		saveStrategy = "workflow"
	}
	if s.unreliableStrategy(req.Workflow.LLM.Provider, req.Workflow.LLM.Model, saveStrategy) {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{
			"error":    "this execution strategy is historically unreliable for the selected provider/model",
			"provider": req.Workflow.LLM.Provider, "model": req.Workflow.LLM.Model, "strategy": saveStrategy,
		})
	}
	// Save is the authoritative last boundary before a graph becomes runnable.
	// Apply deterministic repairs here as well as during generation so imports,
	// stale browser tabs, and direct API clients cannot persist a known-fixable
	// structural defect such as a missing parallel join barrier.
	studio.RepairWiring(&req.Workflow, cat)
	contract := studio.AssessContract(req.Workflow, cat, in)
	if contract.Blockers > 0 {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{
			"error":     contract.Summary,
			"contract":  contract,
			"preflight": studio.Preflight(req.Workflow, in),
		})
	}

	// Consent gate: classify the draft and refuse to persist a Privileged
	// channel exposure unless the operator accepted it. The decision logic
	// lives in studio.Plan so it is identical to what /studio/plan reported.
	plan, err := studio.Plan(req.Workflow)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if plan.RequiresConsent && !req.AcceptPrivilegedExposure {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{
			"error":           "saving this workflow exposes a privileged-tier agent to a channel; explicit consent required",
			"requiresConsent": true,
			"consentItems":    plan.ConsentItems,
		})
	}

	def, err := studio.ToAgentDefinition(req.Workflow, req.AcceptPrivilegedExposure)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if isProtectedSystemAgent(def.ID) {
		return protectedSystemAgentResponse(c)
	}

	// Record the tool contracts this workflow was built against (P0-3). Captured
	// HERE rather than in ToAgentDefinition because the live catalog is the
	// gateway's to know — the pure Draft→Definition mapping has no business
	// reaching for connected-server state. A later schema change then reads as
	// drift with a named node, instead of a validation error indistinguishable
	// from a workflow that was always wrong.
	saveCatalog := s.studioCatalogSnapshot()
	s.groundCatalog(&saveCatalog)
	if snap := studio.CaptureToolSchemas(req.Workflow.Flow, saveCatalog, time.Now()); snap != nil {
		def.ToolSchemas = snap
	}

	// Default LLM to the configured provider, mirroring handleCreateAgent.
	if def.LLM.Provider == "" {
		def.LLM.Provider = s.cfg.LLM.DefaultProvider
	}
	// Timeout-aware defaults for local-model agents (Stories #23/#24): if this
	// agent will run on a LOCAL model, apply patient timeout/turn presets where
	// the draft didn't set them, so a slow local run isn't killed mid-thought.
	s.applyLocalPreset(&def)
	// Stamp per-node code consent (§13) onto the workflow nodes. ApplyGrants
	// refuses if any beyond-guardrail Custom Python node lacks a matching grant,
	// so a saved (and later enabled) agent can never carry unconsented host or
	// network code — the runtime fail-closed check in internal/runtime/flow.go
	// then honours these stamps. GrantedBy records who approved.
	if def.Workflow != nil {
		grantedBy := ""
		if cl := auth.ClaimsFromCtx(c); cl != nil {
			grantedBy = cl.Subject
		}
		grants := make([]consent.Grant, 0, len(req.Grants))
		for _, g := range req.Grants {
			grants = append(grants, consent.Grant{
				NodeID:       g.NodeID,
				Hash:         g.Hash,
				Capabilities: g.Capabilities,
				Scope:        g.Scope,
				GrantedBy:    grantedBy,
			})
		}
		if gerr := consent.ApplyGrants(def.Workflow.Nodes, grants); gerr != nil {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{
				"error":           gerr.Error(),
				"requiresConsent": true,
				"consentItems":    plan.ConsentItems,
			})
		}
	}

	// A NEW agent is staged disabled so an operator reviews it before it runs.
	// An EDIT of an agent the operator already turned on is not a new agent, and
	// switching it off is not a review step — it is a live schedule stopping
	// with no announcement. The Studio panel says as much in its own words:
	// "New agents are always saved disabled so you review and deploy them
	// explicitly." The code applied it to every save, so fixing a typo in a
	// running daily digest silently ended the digest, and the only evidence was
	// a briefing that stopped arriving.
	//
	// Whether a save should re-review a live agent is a real question, but it
	// cannot be answered by having the code and the copy say different things.
	// This makes them agree; the privileged-exposure consent gate above still
	// runs on every save, so an edit cannot quietly widen what the agent reaches.
	wasEnabled := false
	if existing := s.loader.Get(def.ID); existing != nil {
		wasEnabled = existing.Enabled
	}
	def.Enabled = wasEnabled

	dir := ""
	if len(s.cfg.AgentDirs) > 0 {
		dir = s.cfg.AgentDirs[0]
	}
	// Materialise any peer agent the workflow delegates to that does not exist
	// yet, and make sure the caller declares the ones it calls. A dangling peer
	// is not cosmetic: the run dies at the delegating node. Runs before Upsert
	// because it can add to def.Agents, and Upsert is the write.
	createdPeers, perr := s.ensurePeerAgents(dir, &def, req.Workflow.NewAgents)
	if perr != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, perr)
	}

	if err := s.loader.Upsert(dir, &def); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if req.InitialWorkflow != nil && s.verifyGenerationProof(studioLearningOwner(c), *req.InitialWorkflow) {
		s.minePreferences(studioLearningOwner(c), def.ID, *req.InitialWorkflow, req.Workflow)
	}

	// Tell the scheduler what just changed, exactly as the Code view's save does.
	// Writing enabled: false is not on its own enough — the cron table keeps its
	// own entry, and until this line existed a save left one pointing at an agent
	// the operator could see was off. (The engine now also refuses a disabled
	// agent at fire time; this keeps the table itself honest, and picks up an
	// edited cron expression rather than leaving the old one to tick.)
	s.scheduler.DeregisterAgent(def.ID)
	if err := s.scheduler.RegisterAgent(&def); err != nil {
		s.log.Warn("scheduler registration failed", zap.String("agent", def.ID), zap.Error(err))
	}

	// Record the save, and specifically record an ACCEPTED-WARNINGS save with the
	// operator's stated reason. Without this the audit trail cannot distinguish a
	// clean save from one that knowingly shipped past a warning, which is exactly
	// the distinction an incident review needs.
	auditDetails := map[string]any{"name": def.Name}
	status := "ok"
	if reason := strings.TrimSpace(req.AcceptWarningsReason); reason != "" {
		status = "accepted_warnings"
		auditDetails["accept_warnings_reason"] = reason
	}
	if req.AcceptPrivilegedExposure {
		auditDetails["accepted_privileged_exposure"] = true
	}
	s.recordAdminAudit(c, "studio.save", "agent", def.ID, status, auditDetails)

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"agentId": def.ID,
		// Report what was actually written, not what the old rule assumed. The
		// GUI renders "Saved as disabled agent … — enable it from Deployed" off
		// this, which was a lie for every edit of a running agent.
		"enabled": def.Enabled,
		// The helper agents this save had to create. Surfaced so the UI can say
		// so out loud instead of silently growing the user's agent list.
		"peerAgents": createdPeers,
	})
}

// handleStudioListAgents implements GET /api/v1/studio/agents. It returns every
// agent Studio can RE-OPEN, as lightweight summaries for the "My Workflows" list
// and the Describe step's "continue existing work".
//
// The filter used to be HasWorkflow alone, which silently excluded every
// REASONING agent Studio itself had built: an Auto/ReAct/Plan-Execute agent has
// no workflow graph by definition, so a user who generated one could not find it
// anywhere in Studio afterwards — it existed, ran, and was invisible to the tool
// that made it.
//
// So an agent qualifies if Studio can edit it: it has a workflow graph, OR it
// carries a reasoning strategy, OR it was authored here (StudioIntent). The last
// clause is what catches a Studio agent whose strategy was later cleared by hand.
func (s *Server) handleStudioListAgents(c *fiber.Ctx) error {
	type agentSummary struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Enabled     bool   `json:"enabled"`
		Trigger     string `json:"trigger"`
		Nodes       int    `json:"nodes"`
		// Strategy distinguishes a fixed workflow from a reasoning agent in the
		// list, so the two are not presented as interchangeable.
		Strategy string `json:"strategy,omitempty"`
	}
	out := []agentSummary{}
	for _, d := range s.loader.All() {
		if d == nil {
			continue
		}
		hasFlow := studio.HasWorkflow(*d)
		// The loop strategy lives on the Reasoning block, not on Definition
		// itself — Definition.Strategy does not exist.
		strategy := strings.TrimSpace(d.Reasoning.Strategy)
		reasoning := strategy != ""
		authored := strings.TrimSpace(d.StudioIntent) != ""
		if !hasFlow && !reasoning && !authored {
			continue
		}
		nodes := 0
		if hasFlow {
			nodes = len(d.Workflow.Nodes)
		}
		out = append(out, agentSummary{
			ID:          d.ID,
			Name:        d.Name,
			Description: d.Description,
			Enabled:     d.Enabled,
			Trigger:     string(d.Trigger),
			Nodes:       nodes,
			Strategy:    strategy,
		})
	}
	return c.JSON(fiber.Map{"agents": out})
}

// handleStudioLoadAgent implements GET /api/v1/studio/agents/:id. It returns the
// agent's workflow as a Studio Draft so it can be re-opened on the canvas for
// editing; re-saving (POST /studio/save) upserts the same id.
// handleStudioScaffolds implements GET /api/v1/studio/scaffolds. It returns the
// built-in framework Python scaffolds (deterministic, shipped code — no LLM) the
// Custom Python editor offers as "Insert scaffold".
func (s *Server) handleStudioScaffolds(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"scaffolds": studio.Scaffolds()})
}

// handleStudioCodegen implements POST /api/v1/studio/codegen. It asks the
// framework's configured model (llm.studio → studioLLM) to write a complete
// Custom Python node body for ONE node from its description + workflow context.
// In-framework only — the same llm.Router the rest of the gateway uses.
func (s *Server) handleStudioCodegen(c *fiber.Ctx) error {
	var req studio.CodegenRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	llm := s.studioLLM(c)
	if llm == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "no LLM provider configured for code generation")
	}
	code, err := studio.GenerateNodeCode(c.Context(), llm, req)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadGateway, err)
	}
	return c.JSON(fiber.Map{"code": code})
}

func (s *Server) handleStudioLoadAgent(c *fiber.Ctx) error {
	def := s.loader.Get(c.Params("id"))
	if def == nil {
		return s.errMsg(c, fiber.StatusNotFound, "agent not found")
	}
	// Studio can edit two shapes: a fixed-workflow agent (canvas) AND a
	// reasoning agent (ReAct/Plan-Execute — the agent-spec editor + SOUL.yaml).
	// Previously this gated on HasWorkflow alone, so every reasoning agent was
	// rejected with "no editable workflow" and couldn't be opened at all. Since
	// FromAgentDefinition now round-trips the agent form losslessly, let those
	// through too.
	strat := strings.ToLower(strings.TrimSpace(def.Reasoning.Strategy))
	isReasoningAgent := strat == "react" || strat == "plan_execute"
	// Studio-authored agents (studio_intent set) are openable even with an empty
	// graph — e.g. a 0-step build the user needs to inspect, fix, or switch to an
	// agent. Only truly external/library agents with nothing Studio can edit are
	// rejected.
	studioAuthored := strings.TrimSpace(def.StudioIntent) != ""
	if !studio.HasWorkflow(*def) && !isReasoningAgent && !studioAuthored {
		return s.errMsg(c, fiber.StatusBadRequest, "agent has no editable workflow")
	}
	return c.JSON(fiber.Map{"workflow": studio.FromAgentDefinition(*def)})
}

// --- Studio templates (Story S6.1) ---

// handleStudioTemplates implements GET /api/v1/studio/templates. It returns the
// built-in Studio starter Drafts the canvas offers as one-click starting
// points. Read-only: nothing is persisted and no LLM is involved. Every
// template.workflow is guaranteed (by studio.Templates + its tests) to pass
// reasoning.CompileFlow, so a user who picks one lands on a valid graph.
func (s *Server) handleStudioTemplates(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"templates": studio.Templates()})
}

// studioCompileGateRequest is the POST /api/v1/studio/compile-gate body: a
// plain-language connector gate + the flow vars available at that edge.
type studioCompileGateRequest struct {
	Phrase string   `json:"phrase"`
	Vars   []string `json:"vars,omitempty"`
}

// handleStudioCompileGate implements POST /api/v1/studio/compile-gate (Phase B):
// turn a plain-language connector condition into a validated flow predicate.
func (s *Server) handleStudioCompileGate(c *fiber.Ctx) error {
	var req studioCompileGateRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	pred, err := studio.CompileGate(c.Context(), s.studioLLM(c), req.Phrase, req.Vars)
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}
	return c.JSON(fiber.Map{"predicate": pred})
}

// handleStudioCompileNode implements POST /api/v1/studio/compile-node (Phase C):
// compile ONE node from its plain-language intent into concrete config.
func (s *Server) handleStudioCompileNode(c *fiber.Ctx) error {
	var req studio.CompileNodeRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	// Ground in the live catalog when the caller didn't supply one.
	if len(req.Catalog.Tools) == 0 && len(req.Catalog.MCP) == 0 && len(req.Catalog.Agents) == 0 {
		req.Catalog = s.studioCatalogSnapshot()
	}
	node, err := studio.CompileNode(c.Context(), s.studioLLM(c), req)
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}
	return c.JSON(fiber.Map{"node": node})
}

// handleStudioCompositeBlocks implements GET /api/v1/studio/composite-blocks —
// returns the coarse composite-block catalog (Phase 2): each block's id, name,
// summary, requirements, typed port contract, and the ready-to-drop python
// FlowNode that encapsulates its whole multi-step dance. The palette/canvas
// consumes this to offer one-click coarse blocks instead of hand-wired graphs.
func (s *Server) handleStudioCompositeBlocks(c *fiber.Ctx) error {
	blocks := studio.CompositeBlocks()
	out := make([]fiber.Map, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, fiber.Map{
			"id":           b.ID,
			"name":         b.Name,
			"summary":      b.Summary,
			"requirements": b.Requirements,
			"inputs":       b.Inputs,
			"outputs":      b.Outputs,
			// The materialised, drop-ready node (typed ports + inline code +
			// classifier-derived requires).
			"node": b.MaterializeNode(),
		})
	}
	return c.JSON(fiber.Map{"blocks": out})
}

// --- Studio draft library (Story S6.2) ---

// soulRulesPath is the workspace path of the editable SOUL.yaml rulebook:
// <workspace>/studio/soul-yaml-rules.md.
func (s *Server) soulRulesPath() (string, error) {
	ws, err := config.ResolveWorkspace()
	if err != nil {
		return "", err
	}
	return filepath.Join(ws.Root, "studio", "soul-yaml-rules.md"), nil
}

// soulRulesDir is the versioned rules store: <workspace>/studio/rules. The
// store is append-only, so this is a directory of records rather than the
// single flat file soulRulesPath describes.
func (s *Server) soulRulesDir() (string, error) {
	ws, err := config.ResolveWorkspace()
	if err != nil {
		return "", err
	}
	return filepath.Join(ws.Root, "studio", "rules"), nil
}

// soulRules returns the effective rulebook: the newest stored version if there
// is one, else a legacy flat file from before the store was adopted, else the
// built-in default. Never errors — a missing rulebook just falls back so
// generation/validation/fix always have rules.
//
// Reading the store rather than the flat file is what makes a deployment
// record's RulesVersion hash resolvable: the hash pinned at deploy time now
// names a version whose full text is actually retrievable.
func (s *Server) soulRules() string {
	if dir, err := s.soulRulesDir(); err == nil {
		if rec, found, rerr := studio.LatestRules(dir); rerr == nil && found &&
			strings.TrimSpace(rec.Rules) != "" {
			return rec.Rules
		}
	}
	// Legacy flat file, still authoritative for workspaces that predate the
	// store and have not saved since.
	if path, err := s.soulRulesPath(); err == nil {
		if b, rerr := os.ReadFile(path); rerr == nil && strings.TrimSpace(string(b)) != "" {
			return string(b)
		}
	}
	return studio.DefaultSOULRules
}

// handleStudioGetRules implements GET /api/v1/studio/rules — returns the
// effective rulebook, whether it's the built-in default, and the default text
// (so the GUI can offer a "reset to default").
func (s *Server) handleStudioGetRules(c *fiber.Ctx) error {
	rules := s.soulRules()
	return c.JSON(fiber.Map{
		"rules":     rules,
		"isDefault": rules == studio.DefaultSOULRules,
		"default":   studio.DefaultSOULRules,
	})
}

// studioRulesRequest is the PUT /api/v1/studio/rules body.
type studioRulesRequest struct {
	Rules string `json:"rules"`
	// Note is an optional reason for the change — the part of an audit trail a
	// content hash cannot supply.
	Note string `json:"note"`
}

// handleStudioSaveRules implements PUT /api/v1/studio/rules — saves the edited
// rulebook as a new version in the append-only store. An empty body resets to
// the built-in default by recording the default AS a version, not by deleting.
//
// This used to os.WriteFile over a single flat file and os.Remove it to reset,
// which meant the text injected into every subsequent generation could be
// overwritten or destroyed with no author, no timestamp, no prior copy, and no
// way to answer "what were the rules when this agent was deployed?" — even
// though deployment records were already pinning a RulesVersion hash that
// nothing could resolve.
func (s *Server) handleStudioSaveRules(c *fiber.Ctx) error {
	var req studioRulesRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	dir, err := s.soulRulesDir()
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	author := s.auditActor(c)

	// Reset is a version too. Recording the default as the new head keeps the
	// history contiguous and leaves the previous text recoverable, where the old
	// os.Remove discarded the only copy irreversibly.
	rules, isDefault := req.Rules, false
	note := strings.TrimSpace(req.Note)
	if strings.TrimSpace(rules) == "" {
		rules, isDefault = studio.DefaultSOULRules, true
		if note == "" {
			note = "reset to the built-in default"
		}
	}

	rec, err := studio.SaveRulesWithNote(dir, rules, author, note)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	// Retire the legacy flat file only once the store holds the text, so a
	// crash between the two leaves the rulebook readable by the old path.
	if path, perr := s.soulRulesPath(); perr == nil {
		_ = os.Remove(path)
	}
	return c.JSON(fiber.Map{
		"ok": true, "isDefault": isDefault,
		"version": rec.Version, "hash": rec.Hash, "saved": rec.Saved, "author": rec.Author,
		"rules": func() string {
			if isDefault {
				return studio.DefaultSOULRules
			}
			return ""
		}(),
	})
}

// handleStudioRulesHistory implements GET /api/v1/studio/rules/history — the
// stored versions, newest first. Without this the store's audit trail exists on
// disk but is unreachable from the product.
func (s *Server) handleStudioRulesHistory(c *fiber.Ctx) error {
	dir, err := s.soulRulesDir()
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	// RulesMeta, not RulesRecord: history lists versions, and shipping the full
	// rulebook text for every entry would make this response grow without bound.
	recs, err := studio.RulesHistory(dir)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if recs == nil {
		recs = []studio.RulesMeta{}
	}
	return c.JSON(fiber.Map{"versions": recs})
}

// studioDraftsDir derives the Studio drafts directory from the resolved
// workspace: <workspace>/studio/drafts. The store (internal/studio) creates the
// directory on first save, so this only needs to return the path. It is kept on
// Server so every draft handler reaches the same location.
func (s *Server) studioDraftsDir() (string, error) {
	ws, err := config.ResolveWorkspace()
	if err != nil {
		return "", err
	}
	return filepath.Join(ws.Root, "studio", "drafts"), nil
}

// studioSaveDraftRequest is the POST /api/v1/studio/drafts body.
type studioSaveDraftRequest struct {
	Name     string       `json:"name"`
	Workflow studio.Draft `json:"workflow"`
}

// handleStudioSaveDraft implements POST /api/v1/studio/drafts. It persists the
// draft as a JSON file under the Studio drafts dir and returns the new id. The
// store derives a slug+short-hash id and overwrites on an identical re-save.
func (s *Server) handleStudioSaveDraft(c *fiber.Ctx) error {
	var req studioSaveDraftRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if req.Name == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "name is required")
	}
	dir, err := s.studioDraftsDir()
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	id, err := studio.SaveDraft(dir, req.Name, req.Workflow)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"id": id})
}

// handleStudioListDrafts implements GET /api/v1/studio/drafts. It returns the
// metadata (id, name, updated) of every saved draft, most recent first.
func (s *Server) handleStudioListDrafts(c *fiber.Ctx) error {
	dir, err := s.studioDraftsDir()
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	drafts, err := studio.ListDrafts(dir)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.JSON(fiber.Map{"drafts": drafts})
}

// handleStudioLoadDraft implements GET /api/v1/studio/drafts/:id. It returns
// the full stored draft (id, name, workflow). The :id is validated against
// path traversal inside studio.LoadDraft.
func (s *Server) handleStudioLoadDraft(c *fiber.Ctx) error {
	dir, err := s.studioDraftsDir()
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	sd, err := studio.LoadDraft(dir, c.Params("id"))
	if err != nil {
		return s.errJSON(c, fiber.StatusNotFound, err)
	}
	return c.JSON(fiber.Map{"id": sd.ID, "name": sd.Name, "workflow": sd.Workflow})
}

// handleStudioDeleteDraft implements DELETE /api/v1/studio/drafts/:id. The :id
// is validated against path traversal inside studio.DeleteDraft.
func (s *Server) handleStudioDeleteDraft(c *fiber.Ctx) error {
	dir, err := s.studioDraftsDir()
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if err := studio.DeleteDraft(dir, c.Params("id")); err != nil {
		return s.errJSON(c, fiber.StatusNotFound, err)
	}
	return c.JSON(fiber.Map{"ok": true})
}

// --- Studio per-node re-describe (Story S6.3) ---

// studioRefineRequest is the POST /api/v1/studio/refine body: the current
// workflow, the target node id, and a plain-language instruction.
type studioRefineRequest struct {
	Workflow    studio.Draft `json:"workflow"`
	NodeID      string       `json:"nodeId"`
	Instruction string       `json:"instruction"`
}

// handleStudioRefine implements POST /api/v1/studio/refine. It applies a
// plain-language change to one node via studio.Refine (reusing the gateway's
// LLM router) and returns the full updated workflow. studio.Refine validates
// the result via reasoning.CompileFlow and never returns a broken draft, so an
// invalid model output surfaces as a clear error rather than a bad workflow.
func (s *Server) handleStudioRefine(c *fiber.Ctx) error {
	var req studioRefineRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if req.NodeID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "nodeId is required")
	}
	if req.Instruction == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "instruction is required")
	}

	model := s.studioLLM(c)
	if model == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "LLM router unavailable")
	}

	updated, err := studio.Refine(c.Context(), model, req.Workflow, req.NodeID, req.Instruction)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	return c.JSON(fiber.Map{"workflow": updated})
}
