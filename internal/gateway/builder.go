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
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/pkg/agent"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/llm"
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

// buildToolCatalogPrompt returns a system-message-friendly summary of every
// tool an agent could be wired to. Injected into BuilderChat so the LLM picks
// real names + real file paths instead of inventing them.
func (s *Server) buildToolCatalogPrompt() string {
	var sb strings.Builder
	sb.WriteString("## Available tools\n")
	sb.WriteString("Pick from these EXACT names + python_file paths when populating `tools[]`. ")
	sb.WriteString("Do NOT invent tool names. If the user wants a capability not covered here, list it in `missing` and ask whether to skip it or have them install something.\n\n")

	// Python tools — scan ~/.soulacy/tools/ and each configured agent_dir/tools/
	type pyT struct{ name, path, desc string }
	seen := map[string]bool{}
	var pys []pyT
	scan := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".py") {
				continue
			}
			full := filepath.Join(dir, e.Name())
			if seen[full] {
				continue
			}
			seen[full] = true
			pys = append(pys, pyT{
				name: strings.TrimSuffix(e.Name(), ".py"),
				path: full,
				desc: extractPythonDocstring(full),
			})
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if wsPaths, werr := config.ResolveWorkspace(); werr == nil {
			scan(wsPaths.Tools)
		} else {
			scan(filepath.Join(home, ".soulacy", "tools"))
		}
	}
	for _, ad := range s.cfg.AgentDirs {
		scan(filepath.Join(ad, "tools"))
	}
	if len(pys) > 0 {
		sb.WriteString("### Python tools\n")
		for _, p := range pys {
			fmt.Fprintf(&sb, "- **%s** (python_file: `%s`) — %s\n", p.name, p.path, firstLine(p.desc))
		}
		sb.WriteString("\n")
	}

	// MCP tools — currently-connected servers
	if s.mcp != nil {
		hadAny := false
		for _, srv := range s.mcp.ServersSnapshot() {
			if !srv.Connected || len(srv.Tools) == 0 {
				continue
			}
			if !hadAny {
				sb.WriteString("### MCP server tools (use the full namespaced name verbatim)\n")
				hadAny = true
			}
			for _, t := range srv.Tools {
				fmt.Fprintf(&sb, "- **%s** — %s\n", t.FullName, firstLine(t.Description))
			}
		}
		if hadAny {
			sb.WriteString("\n")
		}
	}

	// Built-in tools
	if s.engine != nil {
		bs := s.engine.Builtins()
		if len(bs) > 0 {
			sb.WriteString("### Built-in tools (no python_file needed — engine handles them)\n")
			for _, b := range bs {
				fmt.Fprintf(&sb, "- **%s** — %s\n", b.Name, firstLine(b.Description))
			}
			sb.WriteString("\n")
		}
	}

	sb.WriteString("When you put an entry in `tools[]`, use the tool's exact name. The deploy step will look up the python_file path from this catalog automatically.\n")
	return sb.String()
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
	def.Enabled = true

	dir := ""
	if len(s.cfg.AgentDirs) > 0 {
		dir = s.cfg.AgentDirs[0]
	}
	if err := s.agents(c).Upsert(dir, &def); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "deploy failed: " + err.Error(),
		})
	}

	// Register with scheduler if it has a cron trigger
	_ = s.scheduler.RegisterAgent(&def)

	s.log.Info("builder deployed agent",
		zap.String("agent_id", def.ID),
		zap.String("builder_session", body.SessionID),
	)

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"agent_id":  def.ID,
		"soul_yaml": soulYAML,
	})
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
