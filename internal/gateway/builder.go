// builder.go — HTTP handlers for the conversational agent builder API.
//
// Routes (all under /api/v1/builder, protected by authMiddleware):
//
//	POST   /api/v1/builder/chat            — one turn of the builder conversation
//	POST   /api/v1/builder/generate        — compile current understanding → SOUL.yaml + agent map
//	POST   /api/v1/builder/deploy          — generate AND register the agent in one shot
//	DELETE /api/v1/builder/session/:id     — discard a builder session from memory
//
// The builder is a thin HTTP layer over runtime.Engine builder methods. The
// gateway handler resolves the configured LLM provider/model from config so
// the frontend doesn't need to know about provider topology.
package gateway

import (
	"encoding/json"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/pkg/agent"

	"github.com/soulacy/soulacy/internal/agentsave"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/runtime"
)

// handleBuilderChat processes one conversational turn of the agent builder.
//
// Request body:
//
//	{
//	  "session_id": "uuid",   // omit on first turn — server assigns one
//	  "message":    "...",    // required — the user's message
//	  "provider":   "ollama"  // optional — overrides config default
//	}
//
// Response: runtime.BuilderResponse (session_id, reply, understanding, ready).
func (s *Server) handleBuilderChat(c *fiber.Ctx) error {
	var body struct {
		SessionID   string `json:"session_id"`
		Message     string `json:"message"`
		Provider    string `json:"provider"`
		ConfirmCost bool   `json:"confirm_cost"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid request body: " + err.Error(),
		})
	}
	if body.Message == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "message is required",
		})
	}

	// Assign a session ID server-side on the first turn
	if body.SessionID == "" {
		body.SessionID = uuid.New().String()
	}

	provider := body.Provider
	if provider == "" {
		provider = s.cfg.LLM.DefaultProvider
	}
	claims := auth.ClaimsFromCtx(c)
	if strings.TrimSpace(body.Provider) != "" && !canOverrideModel(claims) {
		return s.errMsg(c, fiber.StatusForbidden, "provider overrides require the admin or operator role")
	}
	subject := ""
	if claims != nil {
		subject = claims.Subject
	}

	catalog := s.buildToolCatalogPrompt()

	ctx := llm.WithCallMetadata(c.Context(), llm.CallMetadata{
		Subject: subject, SessionID: body.SessionID, RunID: uuid.New().String(),
		Source: "builder", CostConfirmed: body.ConfirmCost || isTruthy(c.Get("X-Soulacy-Cost-Confirmed")), OverrideAuthorized: canOverrideModel(claims),
	})
	resp, err := s.engine.BuilderChat(ctx, body.SessionID, body.Message, provider, catalog)
	if err != nil {
		s.log.Error("builder chat failed",
			zap.String("session", body.SessionID),
			zap.Error(err),
		)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	return c.JSON(resp)
}

// buildToolCatalogPrompt renders the live tool catalog for the builder's
// system message. The catalog itself lives in buildercatalog.go, because
// deploy resolves against the same object — the prompt promises that it will.
func (s *Server) buildToolCatalogPrompt() string {
	return s.toolCatalog().BuilderPrompt()
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// handleBuilderGenerate compiles the current session understanding into a
// SOUL.yaml string and an agent map ready for review or deploy.
//
// Request body:
//
//	{
//	  "session_id": "uuid",   // required
//	  "provider":   "ollama", // optional
//	  "model":      "llama3"  // optional
//	}
//
// Response:
//
//	{ "soul_yaml": "...", "agent": { ...agent map... } }
func (s *Server) handleBuilderGenerate(c *fiber.Ctx) error {
	var body struct {
		SessionID string `json:"session_id"`
		Provider  string `json:"provider"`
		Model     string `json:"model"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid request body: " + err.Error(),
		})
	}
	if body.SessionID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "session_id is required",
		})
	}

	provider, model := s.resolveProviderModel(body.Provider, body.Model)

	understanding := s.engine.GetBuilderUnderstanding(body.SessionID)
	if understanding == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "builder session not found or has no understanding yet",
		})
	}

	soulYAML, agentMap := s.engine.BuilderGenerate(understanding, provider, model)
	return c.JSON(fiber.Map{
		"soul_yaml": soulYAML,
		"agent":     agentMap,
	})
}

// handleBuilderDeploy generates a SOUL.yaml from the session understanding and
// immediately registers the agent with the loader — saving a round trip.
//
// Request body identical to handleBuilderGenerate.
//
// Response:
//
//	{ "agent_id": "...", "soul_yaml": "..." }
func (s *Server) handleBuilderDeploy(c *fiber.Ctx) error {
	var body struct {
		SessionID string `json:"session_id"`
		Provider  string `json:"provider"`
		Model     string `json:"model"`
		// ActivateSchedule arms a cron trigger at deploy time. It defaults to
		// false so the user can run the agent once and watch it work before it
		// is allowed to act unattended.
		ActivateSchedule bool `json:"activate_schedule"`
		// AcceptPrivilegedExposure records that the person said yes to putting
		// a privileged agent on a channel. Never defaulted true: the whole
		// point of the gate is that the decision is theirs.
		AcceptPrivilegedExposure bool `json:"accept_privileged_exposure"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid request body: " + err.Error(),
		})
	}
	if body.SessionID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "session_id is required",
		})
	}

	provider, model := s.resolveProviderModel(body.Provider, body.Model)

	understanding := s.engine.GetBuilderUnderstanding(body.SessionID)
	if understanding == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "builder session not found or has no understanding yet",
		})
	}

	soulYAML, agentMap := s.engine.BuilderGenerate(understanding, provider, model)

	// Marshal agentMap → JSON → agent.Definition for the loader
	mapJSON, err := json.Marshal(agentMap)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to serialise agent map: " + err.Error(),
		})
	}
	var def agent.Definition
	if err := json.Unmarshal(mapJSON, &def); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to parse agent definition: " + err.Error(),
		})
	}

	// Wire the chosen tools to real ones. The builder names tools; it does not
	// know what kind each is, and it used to be handed a fabricated
	// `tools/<name>.py` path for every one of them.
	unknownTools := s.wireBuilderTools(understanding, &def)

	// A name the conversation invented cannot be wired, and shipping an agent
	// whose tools silently do nothing is the failure this whole path is for.
	if len(unknownTools) > 0 {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{
			"error":         "some tools could not be matched to anything installed",
			"unknown_tools": unknownTools,
			"hint":          "install the missing tool or skill, or rebuild without it",
		})
	}

	// The same gate Studio uses, and the same one Genie's monitors go through.
	// Three doors reached disk with three different sets of guarantees, and
	// the weakest was the one a model could open unattended.
	decision := agentsave.Gate(c.Context(), &def, agentsave.Options{
		Validate:                 s.agentValidationOptions(c.Context()),
		Peer:                     s.loader.Get,
		AcceptPrivilegedExposure: body.AcceptPrivilegedExposure,
	})
	if decision.Refused != "" {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": decision.Refused})
	}
	if len(decision.Blockers) > 0 {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{
			"error":    "the agent did not pass validation",
			"findings": decision.Blockers,
		})
	}
	if decision.RequiresConsent {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{
			"error":           "this agent is privileged and would be reachable on a channel; explicit consent required",
			"requiresConsent": true,
			"consentItems":    decision.ConsentItems,
		})
	}

	// A conversationally-built agent is enabled, so the user can run it
	// immediately and see it work — that moment is the entire point of this
	// path. Its schedule is a separate decision: arming a cron here meant a
	// user was told it was set up, never saw it run, and a daily job fired
	// unattended with no screen on which to see or stop it.
	def.Enabled = true

	dir := ""
	if len(s.cfg.AgentDirs) > 0 {
		dir = s.cfg.AgentDirs[0]
	}
	if err := s.loader.Upsert(dir, &def); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "deploy failed: " + err.Error(),
		})
	}

	scheduled := false
	if body.ActivateSchedule && def.Schedule != nil {
		if err := s.scheduler.RegisterAgent(&def); err != nil {
			s.log.Warn("builder agent saved but its schedule could not be registered",
				zap.String("agent_id", def.ID), zap.Error(err))
		} else {
			scheduled = true
		}
	}

	s.log.Info("builder deployed agent",
		zap.String("agent_id", def.ID),
		zap.String("builder_session", body.SessionID),
		zap.Bool("scheduled", scheduled),
	)

	resp := fiber.Map{
		"agent_id":  def.ID,
		"soul_yaml": soulYAML,
		"scheduled": scheduled,
	}
	// Tell the caller when a schedule exists but is not armed, so the UI can
	// offer it after the user has watched the agent run once.
	if def.Schedule != nil && !scheduled {
		resp["schedule_pending"] = true
	}
	// Naming a delivery channel that is not configured is how an agent
	// reports success and then silently delivers nothing, forever.
	if warn := s.unconfiguredDeliveryWarning(&def); warn != "" {
		resp["delivery_warning"] = warn
	}
	return c.Status(fiber.StatusCreated).JSON(resp)
}

// wireBuilderTools replaces the definition's tool wiring with the real thing,
// returning any names that matched nothing.
func (s *Server) wireBuilderTools(u *runtime.BuilderUnderstanding, def *agent.Definition) []string {
	if u == nil || len(u.Tools) == 0 {
		return nil
	}
	names := make([]string, 0, len(u.Tools))
	for _, t := range u.Tools {
		names = append(names, t.Name)
	}
	res := s.toolCatalog().ResolveToolNames(names)

	def.Tools = nil
	if len(res.Python) > 0 {
		raw, err := json.Marshal(res.Python)
		if err == nil {
			_ = json.Unmarshal(raw, &def.Tools)
		}
	}
	if len(res.Builtins) > 0 {
		b := append([]string{}, res.Builtins...)
		def.Builtins = &b
	}
	if len(res.MCPTools) > 0 {
		m := append([]string{}, res.MCPTools...)
		def.MCPTools = &m
	}
	return res.Unknown
}

// unconfiguredDeliveryWarning reports a schedule that delivers to a channel
// this install has not set up.
//
// Creating a scheduled agent used to validate nothing about where its output
// goes. Ask for email with no email adapter and it reported success, then
// delivered nothing for as long as it ran. A user in that state believes they
// set something up and concludes the product does not work.
func (s *Server) unconfiguredDeliveryWarning(def *agent.Definition) string {
	if def.Schedule == nil || def.Schedule.Output == nil {
		return ""
	}
	channel := strings.TrimSpace(def.Schedule.Output.Channel)
	if channel == "" || strings.EqualFold(channel, "http") {
		return ""
	}
	if s.channels != nil {
		if st, ok := s.channels.Statuses()[strings.ToLower(channel)]; ok && st.Connected {
			return ""
		}
	}
	return "This agent is set to deliver through " + channel +
		", which is not connected yet. Set it up in Delivery, or its results will have nowhere to go."
}

// handleBuilderDeleteSession discards a builder session from engine memory.
//
//	DELETE /api/v1/builder/session/:id
func (s *Server) handleBuilderDeleteSession(c *fiber.Ctx) error {
	sessionID := c.Params("id")
	if sessionID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "session id is required",
		})
	}
	s.engine.DeleteBuilderSession(sessionID)
	return c.JSON(fiber.Map{"deleted": true, "session_id": sessionID})
}

// ── helpers ────────────────────────────────────────────────────────────────────

// resolveProviderModel picks a provider and model, falling back to config defaults.
// The model is looked up from the provider's config entry when not supplied explicitly.
func (s *Server) resolveProviderModel(provider, model string) (string, string) {
	if provider == "" {
		provider = s.cfg.LLM.DefaultProvider
	}
	if model == "" {
		if pc, ok := s.cfg.LLM.Providers[provider]; ok && pc.Model != "" {
			model = pc.Model
		} else {
			model = "llama3"
		}
	}
	return provider, model
}
