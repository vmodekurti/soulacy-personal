// api.go — REST API handlers for the Soulacy gateway.
// Every action available in the GUI is backed by one of these handlers.
// The GUI never talks directly to the filesystem — everything goes through this API.
package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/valyala/fasthttp"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/soulacy/soulacy/internal/agentvalidate"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/builder"
	"github.com/soulacy/soulacy/internal/channels"
	wawebchan "github.com/soulacy/soulacy/internal/channels/whatsappweb"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/costs"
	"github.com/soulacy/soulacy/internal/introspect"
	"github.com/soulacy/soulacy/internal/learning"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/internal/pkgregistry"
	"github.com/soulacy/soulacy/internal/plugininstall"
	"github.com/soulacy/soulacy/internal/policy"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/scheduler"
	"github.com/soulacy/soulacy/internal/secrets"
	"github.com/soulacy/soulacy/internal/templates"
	"github.com/soulacy/soulacy/internal/tenancy"
	"github.com/soulacy/soulacy/internal/tier"
	"github.com/soulacy/soulacy/internal/workspacesettings"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
	"github.com/soulacy/soulacy/pkg/plugin"
	sdkllm "github.com/soulacy/soulacy/sdk/llm"
	"github.com/soulacy/soulacy/sdk/registry"
)

// handleHealth returns gateway status, version, and a snapshot of dependency
// health. (PRODUCTION_AUDIT → MED/Observability) Each probe is independent
// and capped at a short timeout so a single slow backend can't make the
// health check itself time out — instead we report it as `degraded`.
//
// Status semantics:
//   - "ok"        all probed deps responded successfully
//   - "degraded"  at least one dep failed but the gateway can still serve
//   - "down"      a probe that's load-bearing for ANY work failed (e.g.
//     the SQLite archive). Currently we degrade rather than
//     returning down — operators can decide via the per-dep
//     statuses returned in the body.
func (s *Server) handleHealth(c *fiber.Ctx) error {
	deps := map[string]string{}

	// Provider registry — list IDs so operators can confirm the expected
	// providers loaded. No network call here; this is just an in-memory
	// snapshot.
	deps["providers"] = strings.Join(s.llmRouter.ProviderIDs(), ",")

	// Knowledge store — quick listKBs call. Capped at 50ms.
	if knowledge := s.engine.Knowledge(); knowledge != nil && knowledge.Store != nil {
		// ListKBs is not context-aware, so we probe in a goroutine with a short
		// timeout. The in-flight guard ensures repeated probes against a blocked
		// store don't accumulate goroutines: if one is already running we report
		// "checking" instead of spawning another (PERF-5).
		if s.healthProbeInFlight.CompareAndSwap(false, true) {
			done := make(chan error, 1)
			// Capture the workspace before the probe goroutine starts: this
			// handler may return before the probe finishes, and Fiber recycles
			// the request context the moment it does.
			probeWorkspace := s.agents(c).WorkspaceID()
			go func() {
				_, err := knowledge.Store.ListKBs(probeWorkspace)
				s.healthProbeInFlight.Store(false)
				done <- err
			}()
			ctx, cancel := context.WithTimeout(c.UserContext(), 50*time.Millisecond)
			defer cancel()
			select {
			case err := <-done:
				if err != nil {
					deps["knowledge"] = "error: " + err.Error()
				} else {
					deps["knowledge"] = "ok"
				}
			case <-ctx.Done():
				deps["knowledge"] = "timeout"
			}
		} else {
			deps["knowledge"] = "checking"
		}
	} else {
		deps["knowledge"] = "disabled"
	}

	// S2.13 — deep health (?deep=1) probes things a shallow 200-OK hides:
	// channel adapter connection state and the hot-reload watcher. A gateway
	// that returns "ok" while Slack is disconnected and the watcher is dead is
	// a lie that bites at 2am.
	if c.QueryBool("deep", false) {
		if s.channels != nil {
			for id, st := range s.channels.Statuses() {
				if st.Connected {
					deps["channel:"+id] = "connected"
				} else {
					detail := st.Detail
					if detail == "" {
						detail = "disconnected"
					}
					deps["channel:"+id] = "error: " + detail
				}
			}
		}
		if s.agentWatcher != nil {
			if s.agentWatcher.Healthy() {
				deps["hot_reload_watcher"] = "ok"
			} else {
				deps["hot_reload_watcher"] = "error: watcher not running"
			}
		}
	}

	status := "ok"
	for _, v := range deps {
		if strings.HasPrefix(v, "error:") || v == "timeout" {
			status = "degraded"
			break
		}
	}

	return c.JSON(fiber.Map{
		"status":     status,
		"version":    config.Version,
		"timestamp":  time.Now().UTC(),
		"deps":       deps,
		"request_id": c.Locals("request_id"),
	})
}

// restartRefusal returns why the API may not restart this process, or "" when
// it may.
//
// A separate function so BOTH answers are testable. The allowed path ends in
// os.Exit(0), so a test that drives the handler to completion takes the test
// binary with it — which is how "personal installations can still restart"
// ends up asserted by reading the source instead of by running it.
func (s *Server) restartRefusal(c *fiber.Ctx) string {
	if !config.IsMultiUserMode(s.config().DeploymentMode()) {
		return ""
	}
	if platformPrincipal(c) {
		// The operator restarting their own deployment. platformMW already
		// admits only this principal; the check is repeated here because the
		// consequence of the route being re-registered with ordinary RBAC
		// middleware some day is every tenant's work stopped by a customer,
		// and that is worth two lines.
		return ""
	}
	// WHO CAN REACH IT is the problem, not what it does.
	//
	// This endpoint is gated on config:write, which the workspace `owner` and
	// `admin` roles hold. Those are TENANT roles: the person who runs a
	// workspace, not the person who runs the machine. So any customer could
	// call os.Exit(0) on every other customer's runs, streams and scheduled
	// work.
	//
	// There is no role to move it to. No platform-administrator principal
	// exists in these modes: workspaceContextMW requires a verified membership
	// for every /api/v1 request and refuses the bootstrap server key outright.
	// Until one exists, this endpoint has no legitimate caller here — a
	// multi-user deployment is restarted by whatever supervises the process,
	// which is where its operator already is.
	return "restarting the gateway is not available to workspace roles in " + s.config().DeploymentMode() +
		" mode: it would stop every workspace's runs, and a workspace role is a tenant role. " +
		"The deployment's bootstrap server.api_key opens it, or restart the process from the host."
}

func (s *Server) handleRestart(c *fiber.Ctx) error {
	if refusal := s.restartRefusal(c); refusal != "" {
		return s.errMsg(c, fiber.StatusForbidden, refusal)
	}
	// A DELIBERATE ACT, not a reachable one.
	//
	// This stops every workspace's runs, streams and scheduled work, and it is
	// a bare POST with no body — so anything that walks the route table and
	// sends a request to each protected endpoint restarts the deployment. Our
	// own route-architecture prober did exactly that the moment the operator
	// credential was allowed through, and took the test binary with it. A
	// monitoring probe or a security scanner would do the same to production.
	//
	// The confirmation is required only in multi-user mode: in personal mode
	// the blast radius is the one person pressing the button, and adding a
	// step there is friction with nothing behind it.
	if config.IsMultiUserMode(s.config().DeploymentMode()) {
		var body struct {
			Confirm string `json:"confirm"`
		}
		_ = c.BodyParser(&body)
		if strings.TrimSpace(body.Confirm) != "restart" {
			return s.errMsg(c, fiber.StatusBadRequest,
				`restarting stops every workspace's work; send {"confirm":"restart"} to mean it`)
		}
	}
	s.log.Warn("gateway restart requested via API", zap.Any("request_id", c.Locals("request_id")))
	if err := startRestartChild(); err != nil {
		s.log.Error("gateway restart failed to spawn child", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "restart failed: " + err.Error(),
		})
	}
	s.recordAdminAudit(c, "restart.request", "gateway", "", "accepted", nil)
	exitAfterRestart()
	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
		"ok":      true,
		"message": "Restart requested. A replacement gateway process is starting.",
	})
}

func startRestartChild() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// Route/security tests exercise every mutating endpoint, including restart.
	// Re-executing a Go test binary here recursively starts the complete gateway
	// suite again and every child inherits the parent's output pipe. The original
	// test process reports PASS, then `go test` waits a minute for those inherited
	// descriptors and fails with "Test I/O incomplete". A test binary cannot be
	// a meaningful replacement gateway, so acknowledge the spawn boundary without
	// creating the impossible child. Production executables keep the real path.
	if strings.HasSuffix(filepath.Base(exe), ".test") {
		return nil
	}
	script := `sleep 0.75; exec "$@"`
	args := append([]string{"-c", script, "soulacy-restart", exe}, os.Args[1:]...)
	cmd := exec.Command("/bin/sh", args...)
	cmd.Env = os.Environ()
	if wd, err := os.Getwd(); err == nil {
		cmd.Dir = wd
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Start()
}

// exitAfterRestart lets a successfully spawned replacement take over without
// terminating a Go test process. Route/security tests intentionally exercise
// deployment mutation endpoints, so the test binary must never call os.Exit.
func exitAfterRestart() {
	exe, err := os.Executable()
	if err == nil && strings.HasSuffix(filepath.Base(exe), ".test") {
		return
	}
	go func() {
		time.Sleep(250 * time.Millisecond)
		os.Exit(0)
	}()
}

// --- Agents ---

func isProtectedSystemAgent(id string) bool {
	return strings.TrimSpace(id) == runtime.SystemAgentID
}

func protectedSystemAgentResponse(c *fiber.Ctx) error {
	return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
		"error": "system agent is protected and cannot be modified, deleted, cloned, disabled, or routed to external channels",
	})
}

func channelReferencesProtectedSystem(id string, settings map[string]any, bots []map[string]any) bool {
	if id == "http" {
		return false
	}
	if isProtectedSystemAgent(fmt.Sprint(settings["agent_id"])) {
		return true
	}
	for _, bot := range bots {
		if isProtectedSystemAgent(fmt.Sprint(bot["agent_id"])) {
			return true
		}
	}
	return false
}

func channelMapReferencesProtectedSystem(id string, chMap map[string]any) bool {
	if id == "http" || chMap == nil {
		return false
	}
	if isProtectedSystemAgent(fmt.Sprint(chMap["agent_id"])) {
		return true
	}
	switch raw := chMap["bots"].(type) {
	case []any:
		for _, item := range raw {
			if bot, ok := item.(map[string]any); ok && isProtectedSystemAgent(fmt.Sprint(bot["agent_id"])) {
				return true
			}
		}
	case []map[string]any:
		for _, bot := range raw {
			if isProtectedSystemAgent(fmt.Sprint(bot["agent_id"])) {
				return true
			}
		}
	}
	return false
}

func (s *Server) handleListAgents(c *fiber.Ctx) error {
	defs := s.agents(c).All()
	// A public-demo membership may inspect and chat with deliberately curated
	// showcase agents, but it must not turn GET /agents into a catalogue of
	// every definition in the workspace.  The label and capability validator
	// are the same ones enforced immediately before execution.
	if identity, ok := requestIdentity(c); ok && identity.Role() == tenancy.RoleDemoDeveloper {
		cfg := s.config().PublicDemo
		tools := normalizedSet(cfg.AllowedTools)
		skills := normalizedSet(cfg.AllowedSkills)
		mcpServers := normalizedSet(cfg.AllowedMCPServers)
		providers := normalizedSet(cfg.AllowedProviders)
		models := normalizedSet(cfg.AllowedModels)
		filtered := make([]*agent.Definition, 0, len(defs))
		for _, def := range defs {
			if validatePublicDemoAgent(def, tools, skills, mcpServers, providers, models) == "" {
				filtered = append(filtered, def)
			}
		}
		defs = filtered
	}
	// Interface-aware design (Stories #11/#12): surface where each agent should
	// appear so clients (the Chat picker, channel routers) can filter — e.g.
	// hide cron-only agents from Chat. Computed, not stored, so it stays correct
	// for older agents that predate the Surfaces field.
	meta := make(map[string]fiber.Map, len(defs))
	// versions is what makes conditional saves possible at all (MU-028
	// criterion 3). Nothing in the GUI ever fetches a single agent — every
	// edit screen works out of this list — so without a validator here there
	// is no token any client could put in If-Match, and criterion 2's
	// precondition would refuse every save in a Team deployment.
	//
	// The same derivation as the single-resource ETag, not a parallel one: a
	// token from the list that a save does not accept is worse than no token,
	// because the client sends it and is refused with a conflict that is not
	// a conflict.
	versions := make(map[string]string, len(defs))
	for _, d := range defs {
		if d == nil {
			continue
		}
		meta[d.ID] = fiber.Map{
			"surfaces":      d.EffectiveSurfaces(),
			"chat_eligible": d.AppearsOnChat(),
		}
		versions[d.ID] = resourceETag(d)
	}
	// Optional ?surface=chat filter returns only agents that appear there.
	if surface := strings.TrimSpace(c.Query("surface")); surface != "" {
		filtered := make([]*agent.Definition, 0, len(defs))
		for _, d := range defs {
			if d != nil && d.AppearsOn(surface) {
				filtered = append(filtered, d)
			}
		}
		defs = filtered
	}
	return c.JSON(fiber.Map{"agents": defs, "count": len(defs), "interfaces": meta, "versions": versions})
}

func (s *Server) handleGetAgent(c *fiber.Ctx) error {
	def := s.agents(c).Get(c.Params("id"))
	if def == nil {
		return s.errMsg(c, fiber.StatusNotFound, "agent not found")
	}
	// Hand the caller the validator it needs to make its next write
	// conditional. Without this, "read, edit, write" silently discards a
	// concurrent editor's change.
	if etag := resourceETag(def); etag != "" {
		c.Set(fiber.HeaderETag, etag)
	}
	return c.JSON(def)
}

// handleGetAgentYAML returns the raw SOUL.yaml text for one agent so the GUI can
// show it in an editor for hand fixes (syntax, template references, fields the
// form doesn't expose). It reads the exact on-disk file when available, and
// falls back to marshalling the in-memory Definition (e.g. a built-in agent with
// no source file) so the endpoint always returns something editable.
func (s *Server) handleGetAgentYAML(c *fiber.Ctx) error {
	id := c.Params("id")
	def := s.agents(c).Get(id)
	if def == nil {
		return s.errMsg(c, fiber.StatusNotFound, "agent not found")
	}
	var (
		raw  []byte
		path = def.SourcePath
		err  error
	)
	if path != "" {
		raw, err = os.ReadFile(path)
	}
	if path == "" || err != nil {
		// No file on disk (or unreadable): serialize the live definition instead.
		raw, err = yaml.Marshal(def)
		if err != nil {
			return s.errJSON(c, fiber.StatusInternalServerError, err)
		}
		path = ""
	}
	// The same validator handleUpdateAgentYAML will check, so "open the editor,
	// save" can be conditional. An editor that cannot obtain a version cannot
	// send one, and the requirement below would reject every save.
	if etag := resourceETag(def); etag != "" {
		c.Set(fiber.HeaderETag, etag)
	}
	return c.JSON(fiber.Map{"id": def.ID, "path": path, "yaml": string(raw), "version": resourceETag(def)})
}

// handleUpdateAgentYAML accepts edited SOUL.yaml text, parses + validates it, and
// (on success) writes it back via the loader. YAML syntax or structural errors
// are returned as 400s with the parser/validator message so the user can fix
// them in the editor — nothing is written to disk unless the YAML is valid.
func (s *Server) handleUpdateAgentYAML(c *fiber.Ctx) error {
	id := c.Params("id")
	existing := s.agents(c).Get(id)
	if existing == nil {
		return s.errMsg(c, fiber.StatusNotFound, "agent not found")
	}

	// MU-028. The raw YAML editor is the most collaborative surface there is —
	// a whole-file replace, so a stale save discards every change made since
	// the editor was opened, not just the overlapping field. It was the one
	// agent-mutating handler with no concurrency check at all.
	if rejected, err := s.checkIfMatch(c, existing); rejected {
		return err
	}

	body := c.Body()
	if len(strings.TrimSpace(string(body))) == 0 {
		return s.errMsg(c, fiber.StatusBadRequest, "YAML body is empty")
	}

	// Parse leniently — matching the loader, which tolerates unknown fields (it
	// only warns on them) so editing a SOUL.yaml that carries extra keys doesn't
	// get rejected. A real syntax error still fails here and is reported back.
	var def agent.Definition
	if err := yaml.Unmarshal(body, &def); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "YAML error: "+err.Error())
	}

	// The id is part of the resource path and the on-disk folder; a raw edit must
	// not move the agent. Carry over the non-serialized bookkeeping fields.
	def.ID = id
	def.SourcePath = existing.SourcePath
	def.LoadedAt = existing.LoadedAt

	// Protected system agents keep their required invariants regardless of edits,
	// mirroring handleUpdateAgent, so a raw edit can't brick the system agent.
	if isProtectedSystemAgent(id) {
		def.Enabled = true
		def.SystemTools = true
		def.Channels = []string{"http"}
		if len(def.ConfirmTools) == 0 {
			def.ConfirmTools = existing.ConfirmTools
		}
	}

	// Structural validation (same checks as POST /agents/validate). Blocking
	// errors are returned so the user can fix them; the report also rides along
	// on success so the UI can surface non-blocking warnings.
	report := agentvalidate.Definition(&def, "", s.agentValidationOptionsFor(c), agentvalidate.Report{})
	if report.Errors > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error":      "validation failed",
			"validation": report,
		})
	}

	dir := ""
	if len(s.config().AgentDirs) > 0 {
		dir = s.config().AgentDirs[0]
	}
	// See handleUpdateAgent for the ack-gate rationale — raw YAML saves take
	// the same audit path so a text edit can't sneak past the modal.
	peek := s.computeAgentCapabilityAudit(s.agents(c), existing, &def)
	if peek.RequiresAck && !hasCapabilityAck(c) {
		return s.respondCapabilityAckRequired(c, peek)
	}
	if err := s.agents(c).Upsert(dir, &def); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	audit := s.auditAgentCapabilityChange(s.agents(c), existing, &def)

	// Re-register schedule, mirroring handleUpdateAgent. Through the scoped
	// accessor: the schedule belongs to the workspace that owns the agent, not
	// to whichever workspace the scheduler process was configured with.
	s.schedules(c).Deregister(id)
	if err := s.schedules(c).Register(&def); err != nil {
		s.log.Warn("scheduler re-registration failed", zap.String("agent", id), zap.Error(err))
	}

	if def.HasCapability("system") {
		c.Set("X-Soulacy-Warning", "WARNING: This agent has been granted 'system' capabilities. It has raw access to shell execution and file writing. Ensure you fully trust the agent's prompt to avoid local machine compromise.")
	}

	return c.JSON(fiber.Map{"agent": &def, "validation": report, "capability_audit": audit})
}

func (s *Server) handleListAgentVersions(c *fiber.Ctx) error {
	id := c.Params("id")
	if s.agents(c).Get(id) == nil {
		return s.errMsg(c, fiber.StatusNotFound, "agent not found")
	}
	versions, err := s.agents(c).AgentVersions(id)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.JSON(fiber.Map{"agent_id": id, "versions": versions, "count": len(versions)})
}

func (s *Server) handleGetAgentVersion(c *fiber.Ctx) error {
	id := c.Params("id")
	if s.agents(c).Get(id) == nil {
		return s.errMsg(c, fiber.StatusNotFound, "agent not found")
	}
	data, version, err := s.agents(c).ReadAgentVersion(id, c.Params("version"))
	if err != nil {
		return s.errJSON(c, fiber.StatusNotFound, err)
	}
	return c.JSON(fiber.Map{"agent_id": id, "version": version, "yaml": string(data)})
}

func (s *Server) handleRollbackAgent(c *fiber.Ctx) error {
	id := c.Params("id")
	if isProtectedSystemAgent(id) {
		return protectedSystemAgentResponse(c)
	}
	existing := s.agents(c).Get(id)
	if existing == nil {
		return s.errMsg(c, fiber.StatusNotFound, "agent not found")
	}
	// A rollback is the most destructive agent write there is: it replaces the
	// live definition wholesale with an older one. Guarded against the version
	// the caller was LOOKING at, because the failure this prevents is somebody
	// opening the history page, a colleague saving in the meantime, and the
	// rollback silently discarding that save along with the change it was
	// meant to undo.
	if rejected, err := s.guardAgentUpdate(c, existing); rejected {
		return err
	}
	var req struct {
		Version string `json:"version"`
	}
	if err := c.BodyParser(&req); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if strings.TrimSpace(req.Version) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "version is required")
	}
	dir := ""
	if len(s.config().AgentDirs) > 0 {
		dir = s.config().AgentDirs[0]
	}
	def, version, err := s.agents(c).RestoreAgentVersion(dir, id, req.Version)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.schedules(c).Deregister(id)
	if err := s.schedules(c).Register(def); err != nil {
		s.log.Warn("scheduler re-registration failed", zap.String("agent", id), zap.Error(err))
	}
	return c.JSON(fiber.Map{"agent": def, "restored_version": version})
}

// handleGetAgentTier returns the capability tier for an agent plus the
// reasons that produced it. Companion to the `sy agent tier` CLI and a
// future GUI badge — see docs/CHANNEL_DESIGN.md Q1 and the implementation
// in internal/tier. The endpoint is RBAC-gated under agents:read so the
// same audience that can view an agent can see its tier.
//
// Response shape:
//
//	{ "agent_id": "system", "tier": "privileged",
//	  "reasons": ["system_tools: true (OS-level shell access)", ...] }
func (s *Server) handleGetAgentTier(c *fiber.Ctx) error {
	id := c.Params("id")
	def := s.agents(c).Get(id)
	if def == nil {
		return s.errMsg(c, fiber.StatusNotFound, "agent not found")
	}
	exp := tier.Explain(def, s.agents(c).Get)
	return c.JSON(fiber.Map{
		"agent_id": def.ID,
		"tier":     exp.Tier.String(),
		"reasons":  exp.Reasons,
	})
}

func (s *Server) handleValidateAgent(c *fiber.Ctx) error {
	var def agent.Definition
	if err := c.BodyParser(&def); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	report := agentvalidate.Definition(&def, "", s.agentValidationOptionsFor(c), agentvalidate.Report{})
	return c.JSON(report)
}

func (s *Server) handleCreateAgent(c *fiber.Ctx) error {
	var def agent.Definition
	if err := c.BodyParser(&def); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if def.ID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "id is required")
	}
	if isProtectedSystemAgent(def.ID) {
		return protectedSystemAgentResponse(c)
	}

	// Default LLM to configured provider
	if def.LLM.Provider == "" {
		def.LLM.Provider, def.LLM.Model = s.defaultAgentLLM(c)
	} else if def.LLM.Model == "" {
		effective := s.effectiveWorkspaceConfig(s.workspaceSettingsFor(c))
		if pc, ok := effective.LLM.Providers[def.LLM.Provider]; ok {
			def.LLM.Model = strings.TrimSpace(pc.Model)
		}
	}
	def.Enabled = true

	// Write to first agent dir
	dir := ""
	if len(s.config().AgentDirs) > 0 {
		dir = s.config().AgentDirs[0]
	}
	// New agents rarely have pre-existing interactive bindings, so this peek
	// is usually a no-op. When it isn't (an operator re-creates an agent ID
	// that channel mappings already point at), we still refuse to write
	// without X-Acknowledge-Audit — mirroring the update path.
	peek := s.computeAgentCapabilityAudit(s.agents(c), nil, &def)
	if peek.RequiresAck && !hasCapabilityAck(c) {
		return s.respondCapabilityAckRequired(c, peek)
	}
	// POST /agents writes through Upsert, so before this an ID that already
	// existed was REPLACED and the response said 201 Created. Two members both
	// naming an agent "support-bot" is not an exotic race — it is the ordinary
	// way two people name the same thing — and the loser was told they had
	// created something.
	if rejected, err := s.guardAgentCreate(c, s.agents(c).Get(def.ID)); rejected {
		return err
	}
	if err := s.agents(c).Upsert(dir, &def); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	audit := s.auditAgentCapabilityChange(s.agents(c), nil, &def)

	// Register with scheduler if applicable
	if err := s.schedules(c).Register(&def); err != nil {
		s.log.Warn("scheduler registration failed", zap.String("agent", def.ID), zap.Error(err))
	}

	if def.HasCapability("system") {
		c.Set("X-Soulacy-Warning", "WARNING: This agent has been granted 'system' capabilities. It has raw access to shell execution and file writing. Ensure you fully trust the agent's prompt to avoid local machine compromise.")
	}

	return c.Status(fiber.StatusCreated).JSON(agentDefinitionResponse(&def, audit))
}

func (s *Server) handleUpdateAgent(c *fiber.Ctx) error {
	id := c.Params("id")
	existing := s.agents(c).Get(id)
	if existing == nil {
		return s.errMsg(c, fiber.StatusNotFound, "agent not found")
	}

	// Optimistic concurrency: a caller that read this agent and sends the
	// ETag back gets a 409 instead of silently overwriting someone else's
	// edit. A caller that sends no If-Match behaves exactly as before.
	if rejected, err := s.checkIfMatch(c, existing); rejected {
		return err
	}

	var updates agent.Definition
	if err := c.BodyParser(&updates); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	updates.ID = id // ID cannot be changed via update
	updates.SourcePath = existing.SourcePath
	updates.LoadedAt = existing.LoadedAt

	preserveHiddenAgentUpdateFields(&updates, existing)

	if isProtectedSystemAgent(id) {
		updates.Enabled = true
		updates.SystemTools = true
		updates.Channels = []string{"http"}
		if len(updates.ConfirmTools) == 0 {
			updates.ConfirmTools = existing.ConfirmTools
		}
	}

	dir := ""
	if len(s.config().AgentDirs) > 0 {
		dir = s.config().AgentDirs[0]
	}
	// Peek at the capability audit BEFORE writing. If the change requires
	// explicit ack (privileged tier + exposed via interactive channels) and
	// the client hasn't set X-Acknowledge-Audit, refuse the write and let the
	// GUI show a blocking modal. No side effect is recorded here.
	peek := s.computeAgentCapabilityAudit(s.agents(c), existing, &updates)
	if peek.RequiresAck && !hasCapabilityAck(c) {
		return s.respondCapabilityAckRequired(c, peek)
	}
	if err := s.agents(c).Upsert(dir, &updates); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	audit := s.auditAgentCapabilityChange(s.agents(c), existing, &updates)

	// Re-register schedule
	s.schedules(c).Deregister(id)
	if err := s.schedules(c).Register(&updates); err != nil {
		s.log.Warn("scheduler re-registration failed", zap.String("agent", id), zap.Error(err))
	}

	if updates.HasCapability("system") {
		c.Set("X-Soulacy-Warning", "WARNING: This agent has been granted 'system' capabilities. It has raw access to shell execution and file writing. Ensure you fully trust the agent's prompt to avoid local machine compromise.")
	}

	return c.JSON(agentDefinitionResponse(&updates, audit))
}

type agentCapabilityAudit struct {
	Changed     bool     `json:"changed"`
	Escalated   bool     `json:"escalated"`
	OldTier     string   `json:"old_tier"`
	NewTier     string   `json:"new_tier"`
	Reasons     []string `json:"reasons,omitempty"`
	Bindings    []string `json:"bindings,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
	RequiresAck bool     `json:"requires_ack,omitempty"`
}

// capabilityAckHeader is the request header operators (or the GUI on their
// behalf) set to acknowledge a save that escalates an agent's capability tier
// while the agent is already exposed through interactive channel mappings.
// Without it, the save handler returns 409 with the peeked audit so the GUI
// can render a blocking modal.
const capabilityAckHeader = "X-Acknowledge-Audit"

// hasCapabilityAck reports whether the client explicitly acknowledged the
// capability change for this save. Accepts "1", "true", "yes" (case-insensitive).
func hasCapabilityAck(c *fiber.Ctx) bool {
	v := strings.ToLower(strings.TrimSpace(c.Get(capabilityAckHeader)))
	return v == "1" || v == "true" || v == "yes"
}

// respondCapabilityAckRequired returns the standard 409 body used by every
// save path that computes an ack-requiring audit before writing. Callers pass
// the peeked audit — no side effect is recorded until the retried save
// (with capabilityAckHeader set) actually writes.
func (s *Server) respondCapabilityAckRequired(c *fiber.Ctx, audit agentCapabilityAudit) error {
	return c.Status(fiber.StatusConflict).JSON(fiber.Map{
		"error":            "capability escalation requires acknowledgement",
		"needs_ack":        true,
		"capability_audit": audit,
	})
}

// computeAgentCapabilityAudit is the pure form of auditAgentCapabilityChange:
// it computes the tier diff and warnings without any side effects (no
// actionlog append). Callers use this to PEEK at what a save would do — the
// save-blocking modal path relies on this: check RequiresAck first, and only
// call recordAgentCapabilityAudit after the confirmed write actually happens.
// computeAgentCapabilityAudit resolves peer agents while explaining a tier, so
// it takes the caller's scope: a peer that exists in another workspace must not
// make this workspace's change look less privileged than it is.
func (s *Server) computeAgentCapabilityAudit(scope agentScope, oldDef, newDef *agent.Definition) agentCapabilityAudit {
	oldExp := tier.Explain(oldDef, scope.Get)
	newExp := tier.Explain(newDef, scope.Get)
	audit := agentCapabilityAudit{
		Changed:   oldExp.Tier != newExp.Tier,
		Escalated: newExp.Tier > oldExp.Tier,
		OldTier:   oldExp.Tier.String(),
		NewTier:   newExp.Tier.String(),
		Reasons:   newExp.Reasons,
	}
	if newDef != nil {
		audit.Bindings = s.interactiveChannelBindingsForAgent(newDef.ID)
	}
	if audit.Escalated {
		audit.Warnings = append(audit.Warnings, fmt.Sprintf("Capability tier changed from %s to %s.", audit.OldTier, audit.NewTier))
	}
	if newExp.Tier == tier.Privileged && len(audit.Bindings) > 0 {
		audit.RequiresAck = true
		audit.Warnings = append(audit.Warnings, "This privileged agent is exposed through interactive channel mappings: "+strings.Join(audit.Bindings, ", ")+". Review each mapping's privileged exposure approval.")
	}
	return audit
}

// auditAgentCapabilityChange keeps the old signature for callers that want the
// full "compute + record" side-effect (e.g. imports/rollbacks that don't run
// through the ack modal). Save handlers now compute the peek first, gate on
// RequiresAck, and record only after the write succeeds.
func (s *Server) auditAgentCapabilityChange(scope agentScope, oldDef, newDef *agent.Definition) agentCapabilityAudit {
	audit := s.computeAgentCapabilityAudit(scope, oldDef, newDef)
	if audit.Changed {
		s.recordAgentCapabilityAudit(newDef, audit)
	}
	return audit
}

func (s *Server) recordAgentCapabilityAudit(def *agent.Definition, audit agentCapabilityAudit) {
	if s.actions == nil || def == nil {
		return
	}
	s.actions.Append(message.Event{
		Type:      "agent.capability_tier_changed",
		AgentID:   def.ID,
		SessionID: "config",
		Payload: map[string]any{
			"old_tier":     audit.OldTier,
			"new_tier":     audit.NewTier,
			"escalated":    audit.Escalated,
			"bindings":     audit.Bindings,
			"warnings":     audit.Warnings,
			"requires_ack": audit.RequiresAck,
			"reasons":      audit.Reasons,
		},
		Timestamp: time.Now().UTC(),
	})
}

func (s *Server) interactiveChannelBindingsForAgent(agentID string) []string {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil
	}
	var out []string
	for channelID, cfg := range s.config().Channels {
		if channelID == "http" || cfg == nil {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(cfg["agent_id"])) == agentID && !channels.ParseBoolValue(cfg["outbound_only"], false) {
			out = append(out, channelID)
		}
		for i, bot := range rawBotList(cfg["bots"]) {
			if strings.TrimSpace(fmt.Sprint(bot["agent_id"])) != agentID || channels.ParseBoolValue(bot["outbound_only"], false) {
				continue
			}
			botName := strings.TrimSpace(fmt.Sprint(bot["bot_name"]))
			if botName == "" {
				botName = channelAdapterID(channelID, agentID, "", i, false)
			}
			out = append(out, channelID+"/"+botName)
		}
	}
	return out
}

func agentDefinitionResponse(def *agent.Definition, audit agentCapabilityAudit) fiber.Map {
	raw, _ := json.Marshal(def)
	var out fiber.Map
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		out = fiber.Map{}
	}
	out["capability_audit"] = audit
	return out
}

// preserveHiddenAgentUpdateFields protects advanced SOUL.yaml fields that are
// not rendered by every GUI edit surface. Without this, a normal Save from a
// partial editor payload can silently wipe security, tool policy, workflow, and
// routing fields that the user configured by hand.
func preserveHiddenAgentUpdateFields(updates, existing *agent.Definition) {
	if updates == nil || existing == nil {
		return
	}
	if updates.Security == nil {
		updates.Security = existing.Security
	}
	if updates.Builtins == nil {
		updates.Builtins = existing.Builtins
	}
	if updates.MCPServers == nil {
		updates.MCPServers = existing.MCPServers
	}
	if updates.MCPTools == nil {
		updates.MCPTools = existing.MCPTools
	}
	if updates.Workflow == nil {
		updates.Workflow = existing.Workflow
	}
	if updates.NotifyOnFailure == nil {
		updates.NotifyOnFailure = existing.NotifyOnFailure
	}
	if updates.RunTimeout == "" {
		updates.RunTimeout = existing.RunTimeout
	}
	if !updates.SystemTools {
		updates.SystemTools = existing.SystemTools
	}
	if updates.ConfirmTools == nil {
		updates.ConfirmTools = existing.ConfirmTools
	}
	if len(updates.Labels) == 0 {
		updates.Labels = existing.Labels
	}
	if len(updates.Agents) == 0 {
		updates.Agents = existing.Agents
	}
	if len(updates.Knowledge) == 0 {
		updates.Knowledge = existing.Knowledge
	}
	if len(updates.Tags) == 0 {
		updates.Tags = existing.Tags
	}
}

func (s *Server) handleDeleteAgent(c *fiber.Ctx) error {
	id := c.Params("id")
	if isProtectedSystemAgent(id) {
		return protectedSystemAgentResponse(c)
	}
	// A delete is a write and needs the same precondition, for a sharper
	// reason than an update does: the work it destroys is not merged, it is
	// gone. Deleting on a stale view is deleting an agent somebody edited
	// after you last looked at it.
	//
	// Checked BEFORE the deregistration, so a refused delete does not leave
	// the agent alive with its schedule silently removed — which is the worst
	// of both outcomes and the one nobody notices until 03:00.
	if rejected, err := s.guardAgentUpdate(c, s.agents(c).Get(id)); rejected {
		return err
	}
	s.schedules(c).Deregister(id)
	if err := s.agents(c).Delete(id); err != nil {
		return s.errJSON(c, fiber.StatusNotFound, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (s *Server) handleEnableAgent(c *fiber.Ctx) error {
	return s.setAgentEnabled(c, true)
}

func (s *Server) handleDisableAgent(c *fiber.Ctx) error {
	return s.setAgentEnabled(c, false)
}

func (s *Server) setAgentEnabled(c *fiber.Ctx, enabled bool) error {
	id := c.Params("id")
	if isProtectedSystemAgent(id) {
		return protectedSystemAgentResponse(c)
	}
	def := s.agents(c).Get(id)
	if def == nil {
		return s.errMsg(c, fiber.StatusNotFound, "agent not found")
	}
	def.Enabled = enabled
	dir := ""
	if len(s.config().AgentDirs) > 0 {
		dir = s.config().AgentDirs[0]
	}
	if err := s.agents(c).Upsert(dir, def); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if enabled {
		if err := s.schedules(c).Register(def); err != nil {
			s.log.Warn("scheduler registration failed", zap.String("agent", id), zap.Error(err))
		}
	} else {
		s.schedules(c).Deregister(id)
	}
	return c.JSON(fiber.Map{"id": id, "enabled": enabled})
}

// handleCloneAgent duplicates an existing agent under a new, unique ID.
// The clone is created disabled so duplicate schedules don't fire unexpectedly.
func (s *Server) handleCloneAgent(c *fiber.Ctx) error {
	id := c.Params("id")
	if isProtectedSystemAgent(id) {
		return protectedSystemAgentResponse(c)
	}
	src := s.agents(c).Get(id)
	if src == nil {
		return s.errMsg(c, fiber.StatusNotFound, "agent not found")
	}

	// Value copy, then deep-copy the Schedule pointer so the clone and original
	// don't share a *Schedule (editing one would otherwise affect the other).
	clone := *src
	if src.Schedule != nil {
		sc := *src.Schedule
		clone.Schedule = &sc
	}
	clone.ID = s.uniqueAgentID(s.agents(c), id+"-copy")
	if clone.Name != "" {
		clone.Name = clone.Name + " (copy)"
	}
	clone.Enabled = false
	clone.SourcePath = "" // force a fresh file in its own folder

	dir := ""
	if len(s.config().AgentDirs) > 0 {
		dir = s.config().AgentDirs[0]
	}
	if err := s.agents(c).Upsert(dir, &clone); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.Status(fiber.StatusCreated).JSON(clone)
}

// uniqueAgentID returns base if free, otherwise base-2, base-3, …
// uniqueAgentID picks a free ID inside one workspace. Collision is scoped:
// an ID another tenant uses is not taken from this tenant's point of view.
func (s *Server) uniqueAgentID(scope agentScope, base string) string {
	if scope.Get(base) == nil {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if scope.Get(candidate) == nil {
			return candidate
		}
	}
}

// --- Chat ---

// handleChat is the synchronous HTTP channel: POST a message, get a reply.
// This is what the GUI's built-in chat tester and `sy chat` use.
// We call engine.Handle() directly so we never touch the async inbox path.
func (s *Server) handleChat(c *fiber.Ctx) error {
	var req struct {
		AgentID       string   `json:"agent_id"`
		SessionID     string   `json:"session_id"`
		UserID        string   `json:"user_id"`
		Username      string   `json:"username"`
		Text          string   `json:"text"`
		ResponseMode  string   `json:"response_mode"`
		AttachmentIDs []string `json:"attachment_ids"`
		ConfirmCost   bool     `json:"confirm_cost"`
		Overrides     struct {
			Provider         string   `json:"provider"`
			Model            string   `json:"model"`
			Temperature      *float64 `json:"temperature"`
			TopP             *float64 `json:"top_p"`
			MaxTokens        *int     `json:"max_tokens"`
			MaxTurns         *int     `json:"max_turns"`
			ResponseFormat   string   `json:"response_format"`
			ReasoningEffort  string   `json:"reasoning_effort"`
			PresencePenalty  *float64 `json:"presence_penalty"`
			FrequencyPenalty *float64 `json:"frequency_penalty"`
			ToolChoice       string   `json:"tool_choice"`
			RunBudget        struct {
				MaxTokens   *int `json:"max_tokens"`
				MaxLLMCalls *int `json:"max_llm_calls"`
			} `json:"run_budget"`
			LLM struct {
				Provider         string   `json:"provider"`
				Model            string   `json:"model"`
				Temperature      *float64 `json:"temperature"`
				TopP             *float64 `json:"top_p"`
				MaxTokens        *int     `json:"max_tokens"`
				ResponseFormat   string   `json:"response_format"`
				ReasoningEffort  string   `json:"reasoning_effort"`
				PresencePenalty  *float64 `json:"presence_penalty"`
				FrequencyPenalty *float64 `json:"frequency_penalty"`
				ToolChoice       string   `json:"tool_choice"`
			} `json:"llm"`
		} `json:"overrides"`
	}
	if err := c.BodyParser(&req); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if req.AgentID == "" || req.Text == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "agent_id and text are required")
	}
	responseMode := strings.ToLower(strings.TrimSpace(req.ResponseMode))
	if responseMode != "" && responseMode != "text" && responseMode != "voice" {
		return s.errMsg(c, fiber.StatusBadRequest, "response_mode must be text or voice")
	}
	claims := auth.ClaimsFromCtx(c)
	// Billing identity comes from authenticated claims. A body-supplied user_id
	// is only accepted in open mode, where no trusted subject exists.
	if claims != nil && claims.Subject != "" {
		req.UserID = claims.Subject
	} else if req.UserID == "" {
		req.UserID = "api-user"
	}
	if req.Username == "" {
		req.Username = req.UserID
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = "http-" + uuid.NewString()
	}
	if err := s.claimSession(c, req.AgentID, sessionID); err != nil {
		return err
	}
	text := req.Text
	if len(req.AttachmentIDs) > 0 {
		expanded, err := s.expandChatAttachments(c.UserContext(), s.attachmentWorkspace(c), req.AgentID, sessionID, req.Text, req.AttachmentIDs)
		if err != nil {
			return s.errJSON(c, fiber.StatusBadRequest, err)
		}
		text = expanded
	}
	def := s.agents(c).Get(req.AgentID)

	ovProvider, ovModel, ovTemp, ovTopP, ovMaxTokens := req.Overrides.Provider, req.Overrides.Model, req.Overrides.Temperature, req.Overrides.TopP, req.Overrides.MaxTokens
	ovResponseFormat, ovReasoningEffort := req.Overrides.ResponseFormat, req.Overrides.ReasoningEffort
	ovPresencePenalty, ovFrequencyPenalty, ovToolChoice := req.Overrides.PresencePenalty, req.Overrides.FrequencyPenalty, req.Overrides.ToolChoice
	if strings.TrimSpace(req.Overrides.LLM.Provider) != "" {
		ovProvider = req.Overrides.LLM.Provider
	}
	if strings.TrimSpace(req.Overrides.LLM.Model) != "" {
		ovModel = req.Overrides.LLM.Model
	}
	if req.Overrides.LLM.Temperature != nil {
		ovTemp = req.Overrides.LLM.Temperature
	}
	if req.Overrides.LLM.TopP != nil {
		ovTopP = req.Overrides.LLM.TopP
	}
	if req.Overrides.LLM.MaxTokens != nil {
		ovMaxTokens = req.Overrides.LLM.MaxTokens
	}
	if strings.TrimSpace(req.Overrides.LLM.ResponseFormat) != "" {
		ovResponseFormat = req.Overrides.LLM.ResponseFormat
	}
	if strings.TrimSpace(req.Overrides.LLM.ReasoningEffort) != "" {
		ovReasoningEffort = req.Overrides.LLM.ReasoningEffort
	}
	if req.Overrides.LLM.PresencePenalty != nil {
		ovPresencePenalty = req.Overrides.LLM.PresencePenalty
	}
	if req.Overrides.LLM.FrequencyPenalty != nil {
		ovFrequencyPenalty = req.Overrides.LLM.FrequencyPenalty
	}
	if strings.TrimSpace(req.Overrides.LLM.ToolChoice) != "" {
		ovToolChoice = req.Overrides.LLM.ToolChoice
	}
	if (strings.TrimSpace(ovProvider) != "" || strings.TrimSpace(ovModel) != "") && !canOverrideModel(claims) {
		return s.errMsg(c, fiber.StatusForbidden, "provider/model overrides require the admin or operator role")
	}
	if (req.Overrides.RunBudget.MaxTokens != nil || req.Overrides.RunBudget.MaxLLMCalls != nil) && !s.canOverrideRunBudget(c) {
		return s.errMsg(c, fiber.StatusForbidden, "run budget overrides require the workspace owner or admin role")
	}
	if (req.Overrides.RunBudget.MaxTokens == nil) != (req.Overrides.RunBudget.MaxLLMCalls == nil) {
		return s.errMsg(c, fiber.StatusBadRequest, "run budget overrides require both max_tokens and max_llm_calls")
	}
	if req.Overrides.RunBudget.MaxTokens != nil && *req.Overrides.RunBudget.MaxTokens <= 0 {
		return s.errMsg(c, fiber.StatusBadRequest, "run_budget.max_tokens must be greater than zero")
	}
	if req.Overrides.RunBudget.MaxLLMCalls != nil && *req.Overrides.RunBudget.MaxLLMCalls < 0 {
		return s.errMsg(c, fiber.StatusBadRequest, "run_budget.max_llm_calls must not be negative")
	}
	workspaceDefault := false
	if strings.TrimSpace(ovProvider) == "" && strings.TrimSpace(ovModel) == "" {
		ovProvider, ovModel = s.workspaceChatLLM(c)
		workspaceDefault = ovProvider != "" || ovModel != ""
	}

	chatMeta := chatOverrideMetadata(ovProvider, ovModel, ovTemp, ovTopP, ovMaxTokens, req.Overrides.MaxTurns, ovToolChoice, ovResponseFormat, ovReasoningEffort, ovPresencePenalty, ovFrequencyPenalty)
	if req.Overrides.RunBudget.MaxTokens != nil || req.Overrides.RunBudget.MaxLLMCalls != nil {
		if chatMeta == nil {
			chatMeta = map[string]string{}
		}
		if req.Overrides.RunBudget.MaxTokens != nil {
			chatMeta["playground.run_budget.max_tokens"] = strconv.Itoa(*req.Overrides.RunBudget.MaxTokens)
		}
		if req.Overrides.RunBudget.MaxLLMCalls != nil {
			chatMeta["playground.run_budget.max_llm_calls"] = strconv.Itoa(*req.Overrides.RunBudget.MaxLLMCalls)
		}
	}
	if responseMode == "voice" {
		if chatMeta == nil {
			chatMeta = map[string]string{}
		}
		chatMeta["response.mode"] = "voice"
	}

	msg := message.Message{
		ID:        uuid.New().String(),
		SessionID: sessionID,
		AgentID:   req.AgentID,
		Channel:   "http",
		ThreadID:  req.UserID,
		UserID:    req.UserID,
		Username:  req.Username,
		Role:      message.RoleUser,
		Parts:     message.Text(text),
		Metadata:  chatMeta,
		CreatedAt: time.Now().UTC(),
	}

	// Decouple client connection drop from background execution. Use the
	// agent's declared run_timeout if set, otherwise the gateway default.
	ctx, cancel := context.WithTimeout(detachedRequestContext(c), s.resolveRunTimeout(def))
	defer cancel()
	ctx = withWorkspaceIdentity(c, ctx)
	if principal, ok := requestPrincipal(c); ok {
		ctx = runtime.WithPrincipal(ctx, principal)
	}

	// Register the run under its session id so a client can cancel a slow
	// (local-model) run via POST /chat/cancel {run_id: <session_id>} (Story #22).
	if s.runReg != nil {
		if err := s.runReg.RegisterScoped(ctx, s.requestWorkspace(c), sessionID, cancel, s.resolveRunTimeout(def)); err != nil {
			return s.errJSON(c, fiber.StatusServiceUnavailable, err)
		}
		defer s.runReg.DoneScoped(s.requestWorkspace(c), sessionID)
	}

	// Per-request dry-run: header lets a client preview an agent without side
	// effects even if the agent isn't configured for dry-run.
	if isTruthy(c.Get("X-Soulacy-Dry-Run")) {
		ctx = runtime.WithDryRun(ctx, true)
	}
	ctx = llm.WithCallMetadata(ctx, llm.CallMetadata{
		Subject: req.UserID, AgentID: req.AgentID, SessionID: sessionID,
		RunID: msg.ID, Source: "http", CostConfirmed: req.ConfirmCost || isTruthy(c.Get("X-Soulacy-Cost-Confirmed")),
		OverrideAuthorized: canOverrideModel(claims) || workspaceDefault,
	})

	// Inject confirm sender so synchronous GUI chats can still receive tool confirmation
	// requests over the global WebSocket event stream.
	ctx = runtime.WithConfirmSender(ctx, func(req runtime.ConfirmRequest) <-chan bool {
		// The workspace and requester come off the context the engine is
		// running under, not off this handler's closure — see Broker.Register.
		resultCh := s.engine.Broker().Register(ctx, runtime.ApprovalRequest{
			CallID: req.CallID, Tool: req.Tool, Args: req.Args, Reason: req.Reason,
			AgentID: msg.AgentID, SessionID: msg.SessionID,
		})
		s.hub.Emit(message.Event{
			Type:      "tool_confirm",
			AgentID:   msg.AgentID,
			SessionID: msg.SessionID,
			Payload:   req,
			Timestamp: time.Now().UTC(),
		})
		return resultCh
	})

	reply, err := s.engine.Handle(ctx, msg)
	if err != nil {
		var confirm *costs.ConfirmationRequiredError
		if errors.As(err, &confirm) {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": confirm.Error(), "confirmation_required": true,
				"estimate": confirm})
		}
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}

	replyText := ""
	for _, p := range reply.Parts {
		if p.Type == message.ContentText && p.Text != "" {
			replyText = p.Text
			break
		}
	}
	// Return the full typed parts (text/image/audio/file) so the UI can render
	// rich results — images, charts, audio (podcasts), files — not just text
	// (Stories #26/#27/#28). `reply` stays for backward compatibility.
	responseID := strings.TrimSpace(reply.ID)
	if responseID == "" {
		responseID = uuid.NewString()
	}
	payload := fiber.Map{"reply": replyText, "parts": reply.Parts, "session_id": sessionID, "run_id": msg.ID, "response_id": responseID}
	if spoken := strings.TrimSpace(reply.Metadata["response.spoken"]); spoken != "" {
		payload["spoken_reply"] = spoken
	}
	return c.JSON(payload)
}

func canOverrideModel(claims *auth.Claims) bool {
	if claims == nil { // explicitly open development mode
		return true
	}
	return strings.EqualFold(claims.Role, "admin") || strings.EqualFold(claims.Role, "operator")
}

// canOverrideRunBudget is workspace authority, not deployment authority. The
// hard ceiling remains deployment-owned, but choosing a smaller per-run limit
// for one workspace's agent is a tenant operation. Always use the verified
// membership projected into request context; a stale token role must not grant
// control after the member was demoted or moved to another workspace.
func (s *Server) canOverrideRunBudget(c *fiber.Ctx) bool {
	if !s.authorizationRequired() {
		return true
	}
	identity, ok := requestIdentity(c)
	return ok && (identity.Role() == tenancy.RoleOwner || identity.Role() == tenancy.RoleAdmin)
}

func chatOverrideMetadata(provider, model string, temperature, topP *float64, maxTokens, maxTurns *int, toolChoice, responseFormat, reasoningEffort string, presencePenalty, frequencyPenalty *float64) map[string]string {
	meta := map[string]string{}
	if provider = strings.TrimSpace(provider); provider != "" {
		meta["playground.llm.provider"] = provider
	}
	if model = strings.TrimSpace(model); model != "" {
		meta["playground.llm.model"] = model
	}
	if temperature != nil {
		meta["playground.llm.temperature"] = fmt.Sprintf("%g", *temperature)
	}
	if topP != nil && *topP > 0 {
		meta["playground.llm.top_p"] = fmt.Sprintf("%g", *topP)
	}
	if maxTokens != nil && *maxTokens > 0 {
		meta["playground.llm.max_tokens"] = fmt.Sprintf("%d", *maxTokens)
	}
	if maxTurns != nil && *maxTurns > 0 {
		meta["playground.max_turns"] = fmt.Sprintf("%d", *maxTurns)
	}
	if toolChoice = strings.TrimSpace(toolChoice); toolChoice != "" {
		meta["playground.llm.tool_choice"] = toolChoice
	}
	if responseFormat = strings.TrimSpace(responseFormat); responseFormat != "" {
		meta["playground.llm.response_format"] = responseFormat
	}
	if reasoningEffort = strings.TrimSpace(reasoningEffort); reasoningEffort != "" {
		meta["playground.llm.reasoning_effort"] = reasoningEffort
	}
	if presencePenalty != nil {
		meta["playground.llm.presence_penalty"] = fmt.Sprintf("%g", *presencePenalty)
	}
	if frequencyPenalty != nil {
		meta["playground.llm.frequency_penalty"] = fmt.Sprintf("%g", *frequencyPenalty)
	}
	if len(meta) == 0 {
		return nil
	}
	return meta
}

// handleChatStream is the SSE (Server-Sent Events) variant of handleChat.
// The response is a text/event-stream where each token from the LLM is sent
// as a `data: <token>\n\n` frame. The stream ends with `data: [DONE]\n\n`.
// If the agent has no streaming-capable LLM reply (tool calls are present),
// it falls back to a single data frame containing the full reply text.
//
// Client usage:
//
//	const es = new EventSource('/api/v1/chat/stream?agent_id=X&text=Hello');
//	es.onmessage = e => { if(e.data==='[DONE]') es.close(); else buf+=e.data; };
//
// The endpoint also accepts POST with the same JSON body as /chat. A GET form
// is also supported for EventSource compatibility (query params mapped to fields).
func (s *Server) handleChatStream(c *fiber.Ctx) error {
	// Accept both POST (JSON body) and GET (query params) for EventSource compat.
	var req struct {
		AgentID     string `json:"agent_id"`
		UserID      string `json:"user_id"`
		Username    string `json:"username"`
		Text        string `json:"text"`
		ConfirmCost bool   `json:"confirm_cost"`
	}
	if c.Method() == "POST" {
		if err := c.BodyParser(&req); err != nil {
			return s.errJSON(c, fiber.StatusBadRequest, err)
		}
	} else {
		req.AgentID = c.Query("agent_id")
		req.UserID = c.Query("user_id")
		req.Username = c.Query("username")
		req.Text = c.Query("text")
		req.ConfirmCost = isTruthy(c.Query("confirm_cost"))
	}
	if req.AgentID == "" || req.Text == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "agent_id and text are required")
	}
	if claims := auth.ClaimsFromCtx(c); claims != nil && claims.Subject != "" {
		req.UserID = claims.Subject
	} else if req.UserID == "" {
		req.UserID = "api-user"
	}
	if req.Username == "" {
		req.Username = req.UserID
	}
	sessionID := "http-" + uuid.NewString()
	if err := s.claimSession(c, req.AgentID, sessionID); err != nil {
		return err
	}
	def := s.agents(c).Get(req.AgentID)
	chatProvider, chatModel := s.workspaceChatLLM(c)

	msg := message.Message{
		ID:        uuid.New().String(),
		SessionID: sessionID,
		AgentID:   req.AgentID,
		Channel:   "http",
		ThreadID:  req.UserID,
		UserID:    req.UserID,
		Username:  req.Username,
		Role:      message.RoleUser,
		Parts:     message.Text(req.Text),
		CreatedAt: time.Now().UTC(),
		Metadata:  chatOverrideMetadata(chatProvider, chatModel, nil, nil, nil, nil, "", "", "", nil, nil),
	}

	// Decouple the client connection lifetime from background execution.
	//
	// NOTE: no `defer cancel()` and no `defer s.runReg.Done()` here. Fiber hands
	// the connection to the stream writer below and this handler RETURNS
	// IMMEDIATELY — a handler-scoped defer therefore fired before the writer, and
	// before the engine goroutine's first LLM call, had got anywhere. Every
	// streamed chat died a few hundred microseconds in with nothing on the wire
	// but the opening `run` event, and POST /chat/cancel could never find the run
	// because it had already been deregistered.
	//
	// This is the same bug, and the same fix, as studioDesignGraph's generate
	// stream (see studio.go). Ownership belongs to the two places that actually
	// know the run is over: the producer goroutine (work finished) and the stream
	// writer (client went away).
	runCtx, cancel := context.WithTimeout(detachedRequestContext(c), s.resolveRunTimeout(def))
	runCtx = withWorkspaceIdentity(c, runCtx)
	if principal, ok := requestPrincipal(c); ok {
		runCtx = runtime.WithPrincipal(runCtx, principal)
	}

	// Register this run so it can be cancelled mid-flight (Story #22). The id is
	// emitted to the client below as a "run" event; POST /chat/cancel cancels it.
	runID := msg.ID
	runWorkspace := s.requestWorkspace(c)
	if err := s.claimSession(c, req.AgentID, runID); err != nil {
		cancel()
		return err
	}
	if s.runReg != nil {
		if err := s.runReg.RegisterScoped(runCtx, runWorkspace, runID, cancel, s.resolveRunTimeout(def)); err != nil {
			cancel()
			return s.errJSON(c, fiber.StatusServiceUnavailable, err)
		}
	}

	// sseEvent is the unified event type for the SSE stream.
	// Event == "" → default "message" frame (token).
	// Event != "" → named SSE event frame (tool_confirm, error).
	type sseEvent struct {
		Event string // SSE event name (omit for default "message")
		Data  string // SSE data payload
	}

	// Single channel for all SSE events: tokens, confirms, errors, done.
	// Buffered so the engine goroutine doesn't block on slow writers.
	events := make(chan sseEvent, 256)

	// Stream callback delivers LLM tokens as default SSE message events.
	streamCtx := runtime.WithStreamCallback(runCtx, func(token string) {
		select {
		case events <- sseEvent{Data: strings.ReplaceAll(token, "\n", "\\n")}:
		case <-runCtx.Done():
		}
	})
	streamCtx = llm.WithCallMetadata(streamCtx, llm.CallMetadata{
		Subject: req.UserID, AgentID: req.AgentID, SessionID: msg.SessionID,
		RunID: runID, Source: "http", CostConfirmed: req.ConfirmCost || isTruthy(c.Get("X-Soulacy-Cost-Confirmed")), OverrideAuthorized: chatProvider != "" || chatModel != "",
	})

	// Confirm sender emits a tool_confirm event and registers a result channel
	// in the broker. The engine blocks on the result channel until the user
	// approves or denies via POST /api/v1/chat/confirm.
	streamCtx = runtime.WithConfirmSender(streamCtx, func(req runtime.ConfirmRequest) <-chan bool {
		resultCh := s.engine.Broker().Register(streamCtx, runtime.ApprovalRequest{
			CallID: req.CallID, Tool: req.Tool, Args: req.Args, Reason: req.Reason,
			AgentID: msg.AgentID, SessionID: msg.SessionID,
		})
		data, _ := json.Marshal(req)
		select {
		case events <- sseEvent{Event: "tool_confirm", Data: string(data)}:
		case <-runCtx.Done():
		}
		return resultCh
	})

	go func() {
		// The run is over when this goroutine returns, whatever happened — so
		// this is where the registry entry is released and the run context torn
		// down. Deferred LIFO: the final frame is queued by the body below, then
		// the channel closes so the writer's range terminates, then the context
		// is released.
		defer func() {
			cancel()
			if s.runReg != nil {
				s.runReg.DoneScoped(runWorkspace, runID)
			}
		}()
		defer close(events)
		_, err := s.engine.Handle(streamCtx, msg)
		if err != nil {
			select {
			case events <- sseEvent{Event: "error", Data: err.Error()}:
			case <-runCtx.Done():
			}
		} else {
			select {
			case events <- sseEvent{Data: "[DONE]"}:
			case <-runCtx.Done():
			}
		}
	}()

	// Switch to SSE mode. SetBodyStreamWriter takes ownership of the connection.
	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("X-Accel-Buffering", "no") // disable nginx/proxy buffering

	c.Context().SetBodyStreamWriter(fasthttp.StreamWriter(func(w *bufio.Writer) {
		// Tell the client the run id up front so it can cancel this run (#22).
		// A failed flush is the ONLY signal that the client went away once the
		// connection has been handed to the stream writer. Cancelling the run then
		// stops work nobody is waiting for, instead of burning model tokens for a
		// closed tab.
		defer cancel()
		fmt.Fprintf(w, "event: run\ndata: {\"run_id\":%q}\n\n", runID) //nolint:errcheck
		w.Flush()                                                      //nolint:errcheck
		for ev := range events {
			if ev.Event != "" {
				fmt.Fprintf(w, "event: %s\n", ev.Event) //nolint:errcheck
			}
			fmt.Fprintf(w, "data: %s\n\n", ev.Data) //nolint:errcheck
			if err := w.Flush(); err != nil {
				cancel()
				// Keep draining: the producer's sends must not block on a channel
				// nobody is reading, or its goroutine leaks holding a session lock.
				for range events {
				}
				return
			}
		}
	}))

	return nil
}

// handleChatCancel cancels an in-flight run by its run id (Story #22), so a user
// can stop a slow local-model run. Returns 404 if the run already finished.
//
//	POST /api/v1/chat/cancel  {"run_id": "<id>"}
func (s *Server) handleChatCancel(c *fiber.Ctx) error {
	var req struct {
		RunID string `json:"run_id"`
	}
	if err := c.BodyParser(&req); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if req.RunID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "run_id is required")
	}
	if err := s.requireSession(c, "", req.RunID); err != nil {
		return err
	}
	if s.runReg == nil || !s.runReg.CancelScoped(c.UserContext(), s.requestWorkspace(c), req.RunID) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "run not found — it may have already finished",
		})
	}
	return c.JSON(fiber.Map{"ok": true, "cancelled": req.RunID})
}

// handleToolConfirm resolves a pending tool-confirmation request.
// The client POSTs here after the user approves or denies a "tool_confirm" SSE event.
//
//	POST /api/v1/chat/confirm
//	{"call_id": "<id>", "approved": true}
func (s *Server) handleToolConfirm(c *fiber.Ctx) error {
	var req struct {
		CallID   string `json:"call_id"`
		Approved bool   `json:"approved"`
	}
	if err := c.BodyParser(&req); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if req.CallID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "call_id is required")
	}
	// Capture the pending request's context (tool/agent/session) before it is
	// resolved and removed, so we can record who approved what.
	var tool, agentID, sessionID string
	by := s.approver(c)
	for _, p := range s.engine.Broker().List(c.UserContext(), by.WorkspaceID) {
		if p.CallID == req.CallID {
			tool, agentID, sessionID = p.Tool, p.AgentID, p.SessionID
			break
		}
	}
	if err := s.engine.Broker().Resolve(c.UserContext(), by.WorkspaceID, req.CallID, req.Approved, by, ""); err != nil {
		return s.approvalError(c, err)
	}
	s.recordApproval(agentID, sessionID, tool, req.CallID, req.Approved, approverIdentity(c))
	return c.JSON(fiber.Map{"ok": true})
}

// approverIdentity resolves who made an approval decision. This system uses a
// single API key rather than per-user identity, so we record the client-provided
// name when given, else the calling device by IP — the meaningful granularity of
// "who approved what" available here.
func approverIdentity(c *fiber.Ctx) string {
	principal, authenticated, _ := authenticatedPrincipal(c)
	if authenticated && principal != "" {
		return principal
	}
	return "authenticated-unknown"
}

// recordApproval writes an approval/denial to the durable action log so the
// Activity view can show who approved what, at which risk tier.
func (s *Server) recordApproval(agentID, sessionID, tool, callID string, approved bool, approver string) {
	if s.actions == nil {
		return
	}
	s.actions.Append(message.Event{
		Type:      "tool.approval",
		AgentID:   agentID,
		SessionID: sessionID,
		Payload: map[string]any{
			"call_id":  callID,
			"tool":     tool,
			"approved": approved,
			"approver": approver,
			"risk":     policy.RiskTierOf(tool).String(),
		},
		Timestamp: time.Now().UTC(),
	})
}

// --- Channels ---

// channelField describes one editable setting on a channel.
type channelField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Type     string `json:"type"` // "text", "password", or "checkbox"
	Required bool   `json:"required"`
	Secret   bool   `json:"secret"`
	Help     string `json:"help,omitempty"`
}

// channelSpec is the static definition of a supported channel type.
type channelSpec struct {
	ID     string
	Name   string
	Always bool // always-on (e.g. http) — cannot be disabled
	Fields []channelField
}

type channelDiagnostic struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Remedy   string `json:"remedy,omitempty"`
}

// channelSpecs is the catalog of every channel Soulacy supports. The GUI uses
// this to render configuration forms even for channels not yet configured.
var channelSpecs = []channelSpec{
	{ID: "http", Name: "HTTP", Always: true, Fields: nil},
	{ID: "telegram", Name: "Telegram", Fields: []channelField{
		{Key: "bot_name", Label: "Default outbound bot name", Type: "text", Required: false, Help: "Friendly name for the default sender used by scheduled/manual output"},
		{Key: "token", Label: "Default outbound bot token", Type: "password", Required: true, Secret: true, Help: "Get one from @BotFather; this top-level bot is the default sender for cron jobs"},
		{Key: "outbound_only", Label: "Send only", Type: "checkbox", Required: false, Help: "Recommended for the default outbound bot; interactive agents should use bot mappings below"},
		{Key: "default_output_to", Label: "Default output destination", Type: "text", Required: false, Help: "Optional default chat/channel ID used by scheduled agents when no destination is set"},
		{Key: "default_output_template", Label: "Default output template", Type: "text", Required: false, Help: "Optional template for scheduled output; use {reply}, {agent_id}, {agent_name}, {trigger}, {timestamp}"},
		{Key: "agent_id", Label: "Default interactive agent ID", Type: "text", Required: false, Help: "Legacy single-bot mode only; prefer Add bot mapping for interactive agents"},
		{Key: "accept_privileged_exposure", Label: "Allow privileged agent exposure", Type: "checkbox", Required: false, Help: "Required before an interactive bot can expose an agent with system/file/write capabilities. Bot mappings inherit this unless they set their own value."},
		{Key: "trigger_phrase", Label: "Trigger phrase", Type: "text", Required: false, Help: "Only messages beginning with this phrase will trigger the agent; defaults to !soulacy"},
		{Key: "ignore_groups", Label: "Ignore group chats", Type: "text", Required: false, Help: "true by default; set false only for deliberate group usage"},
		{Key: "allowed_chat_ids", Label: "Allowed chat IDs", Type: "text", Required: false, Help: "Optional comma-separated Telegram chat IDs to allow"},
		{Key: "allowed_user_ids", Label: "Allowed user IDs", Type: "text", Required: false, Help: "Comma-separated Telegram user IDs"},
	}},
	{ID: "discord", Name: "Discord", Fields: []channelField{
		{Key: "bot_name", Label: "Bot name", Type: "text", Required: false, Help: "Friendly name shown in mappings and schedules"},
		{Key: "token", Label: "Bot token", Type: "password", Required: true, Secret: true},
		{Key: "default_output_to", Label: "Default output destination", Type: "text", Required: false, Help: "Optional default channel ID used by scheduled agents when no destination is set"},
		{Key: "default_output_template", Label: "Default output template", Type: "text", Required: false, Help: "Optional template for scheduled output; use {reply}, {agent_id}, {agent_name}, {trigger}, {timestamp}"},
		{Key: "agent_id", Label: "Default agent ID", Type: "text", Required: true},
		{Key: "accept_privileged_exposure", Label: "Allow privileged agent exposure", Type: "checkbox", Required: false, Help: "Required before this bot can expose an agent with system/file/write capabilities."},
		{Key: "trigger_phrase", Label: "Trigger phrase", Type: "text", Required: false, Help: "Only messages beginning with this phrase will trigger the agent; defaults to !soulacy"},
		{Key: "ignore_groups", Label: "Ignore servers", Type: "text", Required: false, Help: "true by default; set false only for deliberate server usage"},
		{Key: "allowed_chat_ids", Label: "Allowed channel IDs", Type: "text", Required: false, Help: "Optional comma-separated Discord channel IDs to allow"},
		{Key: "allowed_user_ids", Label: "Allowed user IDs", Type: "text", Required: false, Help: "Optional comma-separated Discord user IDs to allow"},
		{Key: "guild_id", Label: "Guild ID", Type: "text", Required: false},
	}},
	{ID: "slack", Name: "Slack", Fields: []channelField{
		{Key: "bot_name", Label: "Bot name", Type: "text", Required: false, Help: "Friendly name shown in mappings and schedules"},
		{Key: "bot_token", Label: "Bot token", Type: "password", Required: true, Secret: true, Help: "xoxb-..."},
		{Key: "app_token", Label: "App token", Type: "password", Required: true, Secret: true, Help: "xapp-..."},
		{Key: "default_output_to", Label: "Default output destination", Type: "text", Required: false, Help: "Optional default channel ID used by scheduled agents when no destination is set"},
		{Key: "default_output_template", Label: "Default output template", Type: "text", Required: false, Help: "Optional template for scheduled output; use {reply}, {agent_id}, {agent_name}, {trigger}, {timestamp}"},
		{Key: "agent_id", Label: "Default agent ID", Type: "text", Required: true},
		{Key: "accept_privileged_exposure", Label: "Allow privileged agent exposure", Type: "checkbox", Required: false, Help: "Required before this bot can expose an agent with system/file/write capabilities."},
		{Key: "trigger_phrase", Label: "Trigger phrase", Type: "text", Required: false, Help: "Only messages beginning with this phrase will trigger the agent; defaults to !soulacy"},
		{Key: "ignore_groups", Label: "Ignore channels", Type: "text", Required: false, Help: "true by default; set false only for deliberate channel usage"},
		{Key: "allowed_chat_ids", Label: "Allowed channel IDs", Type: "text", Required: false, Help: "Optional comma-separated Slack channel IDs to allow"},
		{Key: "allowed_user_ids", Label: "Allowed user IDs", Type: "text", Required: false, Help: "Optional comma-separated Slack user IDs to allow"},
	}},
	{ID: "teams", Name: "Microsoft Teams", Fields: []channelField{
		{Key: "webhook_url", Label: "Webhook / Workflow URL", Type: "password", Required: true, Secret: true, Help: "Teams Incoming Webhook or Workflow URL used for outbound agent and schedule messages"},
		{Key: "title", Label: "Message title", Type: "text", Required: false, Help: "Optional heading prepended to each Teams message"},
		{Key: "timeout_seconds", Label: "Timeout seconds", Type: "text", Required: false, Help: "Request timeout; defaults to 10"},
		{Key: "default_output_to", Label: "Default output destination", Type: "password", Required: false, Secret: true, Help: "Optional alternate webhook URL used by scheduled agents. Leave empty to use Webhook / Workflow URL."},
		{Key: "default_output_template", Label: "Default output template", Type: "text", Required: false, Help: "Optional scheduled-output wrapper; use {reply}, {agent_id}, {agent_name}, {trigger}, {timestamp}"},
	}},
	{ID: "google_chat", Name: "Google Chat", Fields: []channelField{
		{Key: "webhook_url", Label: "Webhook URL", Type: "password", Required: true, Secret: true, Help: "Google Chat incoming webhook URL used for outbound agent and schedule messages"},
		{Key: "prefix", Label: "Message prefix", Type: "text", Required: false, Help: "Optional text prepended to each Google Chat message"},
		{Key: "timeout_seconds", Label: "Timeout seconds", Type: "text", Required: false, Help: "Request timeout; defaults to 10"},
		{Key: "default_output_to", Label: "Default output destination", Type: "password", Required: false, Secret: true, Help: "Optional alternate webhook URL used by scheduled agents. Leave empty to use Webhook URL."},
		{Key: "default_output_template", Label: "Default output template", Type: "text", Required: false, Help: "Optional scheduled-output wrapper; use {reply}, {agent_id}, {agent_name}, {trigger}, {timestamp}"},
	}},
	{ID: "whatsapp", Name: "WhatsApp", Fields: []channelField{
		{Key: "bot_name", Label: "Bot name", Type: "text", Required: false, Help: "Friendly name shown in mappings and schedules"},
		{Key: "phone_number_id", Label: "Phone number ID", Type: "text", Required: true},
		{Key: "access_token", Label: "Access token", Type: "password", Required: true, Secret: true},
		{Key: "verify_token", Label: "Verify token", Type: "password", Required: true, Secret: true},
		{Key: "app_secret", Label: "App secret", Type: "password", Required: true, Secret: true, Help: "Meta app secret used to verify webhook signatures"},
		{Key: "default_output_to", Label: "Default output destination", Type: "text", Required: false, Help: "Optional default phone/user ID used by scheduled agents when no destination is set"},
		{Key: "default_output_template", Label: "Default output template", Type: "text", Required: false, Help: "Optional template for scheduled output; use {reply}, {agent_id}, {agent_name}, {trigger}, {timestamp}"},
		{Key: "agent_id", Label: "Default agent ID", Type: "text", Required: true},
		{Key: "trigger_phrase", Label: "Trigger phrase", Type: "text", Required: false, Help: "Only messages beginning with this phrase will trigger the agent; defaults to !soulacy"},
		{Key: "allowed_user_ids", Label: "Allowed phone numbers", Type: "text", Required: false, Help: "Optional comma-separated WhatsApp sender IDs to allow"},
	}},
	{ID: "whatsapp_web", Name: "WhatsApp Web (experimental)", Fields: []channelField{
		{Key: "bot_name", Label: "Bot name", Type: "text", Required: false, Help: "Friendly name shown in mappings and schedules"},
		{Key: "default_output_to", Label: "Default output destination", Type: "text", Required: false, Help: "Optional default chat JID used by scheduled agents when no destination is set"},
		{Key: "default_output_template", Label: "Default output template", Type: "text", Required: false, Help: "Optional template for scheduled output; use {reply}, {agent_id}, {agent_name}, {trigger}, {timestamp}"},
		{Key: "trigger_phrase", Label: "Trigger phrase", Type: "text", Required: false, Help: "Only messages beginning with this phrase will trigger the agent; defaults to !soulacy"},
		{Key: "ignore_groups", Label: "Ignore group chats", Type: "text", Required: false, Help: "true by default; set false only for deliberate group usage"},
		{Key: "allowed_chat_ids", Label: "Allowed chat IDs", Type: "text", Required: false, Help: "Optional comma-separated WhatsApp chat JIDs to allow"},
		{Key: "allowed_sender_ids", Label: "Allowed sender IDs", Type: "text", Required: false, Help: "Optional comma-separated WhatsApp sender JIDs to allow"},
		{Key: "command", Label: "Command", Type: "text", Required: false, Help: "Runtime executable; defaults to node"},
		{Key: "args", Label: "Arguments", Type: "text", Required: true, Help: "Command args, e.g. scripts/whatsapp-web-sidecar.mjs"},
		{Key: "session_dir", Label: "Session directory", Type: "text", Required: false, Help: "Where QR-linked auth state is stored"},
		{Key: "account_id", Label: "Account ID", Type: "text", Required: false, Help: "Session subdirectory for this linked account"},
		{Key: "agent_id", Label: "Default agent ID", Type: "text", Required: true},
	}},
	{ID: "email", Name: "Email (SMTP)", Fields: []channelField{
		{Key: "host", Label: "SMTP host", Type: "text", Required: true, Help: "e.g. smtp.gmail.com"},
		{Key: "port", Label: "Port", Type: "text", Help: "587 for STARTTLS (default), 465 for implicit TLS"},
		{Key: "username", Label: "Username", Type: "text", Help: "Usually your full email address."},
		{Key: "password", Label: "Password", Type: "password", Secret: true, Help: "Use an app password — not your account password."},
		{Key: "from", Label: "From", Type: "text", Help: "Defaults to the username. Accepts \"Name <addr@example.com>\"."},
		{Key: "default_output_to", Label: "Default recipient", Type: "text", Help: "Where scheduled output is emailed when no destination is set."},
		{Key: "subject", Label: "Default subject", Type: "text"},
		{Key: "tls", Label: "TLS", Type: "text", Help: "starttls | implicit | none (inferred from the port when blank)."},
	}},
	{ID: "webhook", Name: "Outgoing Webhook", Fields: []channelField{
		{Key: "url", Label: "Webhook URL", Type: "password", Required: true, Secret: true, Help: "Absolute http(s) endpoint that receives outbound JSON messages from agents and schedules"},
		{Key: "method", Label: "HTTP method", Type: "text", Required: false, Help: "Defaults to POST"},
		{Key: "headers", Label: "Headers", Type: "text", Required: false, Secret: true, Help: "Optional request headers as Key: value lines; useful for Authorization or shared secrets"},
		{Key: "template", Label: "Body template", Type: "text", Required: false, Help: "Optional plain-text body template. Use {text}, {agent_id}, {session_id}, {to}, {timestamp}. Leave empty for structured JSON."},
		{Key: "secret", Label: "Signing secret", Type: "password", Required: false, Secret: true, Help: "Optional. When set, each request is signed with HMAC-SHA256 — your receiver can verify X-Soulacy-Signature against X-Soulacy-Timestamp to prove the payload came from Soulacy and was not tampered with. Leave blank to send unsigned."},
		{Key: "timeout_seconds", Label: "Timeout seconds", Type: "text", Required: false, Help: "Request timeout; defaults to 10"},
		{Key: "default_output_to", Label: "Default output destination", Type: "password", Required: false, Secret: true, Help: "Optional override URL used by scheduled agents. Leave empty to use Webhook URL."},
		{Key: "default_output_template", Label: "Default output template", Type: "text", Required: false, Help: "Optional scheduled-output wrapper; use {reply}, {agent_id}, {agent_name}, {trigger}, {timestamp}"},
	}},
}

func channelSpecByID(id string) *channelSpec {
	for i := range channelSpecs {
		if channelSpecs[i].ID == id {
			return &channelSpecs[i]
		}
	}
	return nil
}

func channelSupportsBots(id string) bool {
	switch id {
	case "telegram", "discord", "slack":
		return true
	default:
		return false
	}
}

func valuePresent(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(t) != ""
	case []any:
		return len(t) > 0
	case []int64:
		return len(t) > 0
	case []int:
		return len(t) > 0
	default:
		return true
	}
}

func displayChannelValue(v any) any {
	switch t := v.(type) {
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, fmt.Sprint(item))
		}
		return strings.Join(parts, ", ")
	case []int64:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, strconv.FormatInt(item, 10))
		}
		return strings.Join(parts, ", ")
	case []int:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, strconv.Itoa(item))
		}
		return strings.Join(parts, ", ")
	case []string:
		return strings.Join(t, " ")
	default:
		return t
	}
}

func normalizeChannelValue(key, val string) any {
	if key == "outbound_only" || key == "ignore_groups" || key == "accept_privileged_exposure" {
		return strings.EqualFold(strings.TrimSpace(val), "true") || strings.TrimSpace(val) == "1"
	}
	if key == "args" {
		return strings.Fields(val)
	}
	if key != "allowed_user_ids" {
		return val
	}
	parts := strings.FieldsFunc(val, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t'
	})
	out := make([]int64, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			if n, err := strconv.ParseInt(part, 10, 64); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

func normalizeChannelBots(spec channelSpec, bots []map[string]any, existingRaw any) []map[string]any {
	existing := rawBotList(existingRaw)
	out := make([]map[string]any, 0, len(bots))
	for i, bot := range bots {
		row := map[string]any{}
		var existingBot map[string]any
		if i < len(existing) {
			existingBot = existing[i]
		}
		for _, f := range spec.Fields {
			raw, present := bot[f.Key]
			if !present {
				continue
			}
			val := strings.TrimSpace(fmt.Sprint(raw))
			if f.Secret && (val == "" || val == "***") {
				if existingBot != nil && valuePresent(existingBot[f.Key]) {
					row[f.Key] = existingBot[f.Key]
				}
				continue
			}
			row[f.Key] = normalizeChannelValue(f.Key, val)
		}
		out = append(out, row)
	}
	return out
}

func maskChannelBots(spec channelSpec, cfg map[string]any, statuses map[string]channels.AdapterStatus, loader *runtime.Loader) []fiber.Map {
	botList := rawBotList(cfg["bots"])
	out := make([]fiber.Map, 0, len(botList))
	defaultReserved := valuePresent(cfg["token"]) || valuePresent(cfg["bot_token"])
	for i, bot := range botList {
		row := fiber.Map{}
		for _, f := range spec.Fields {
			rawVal := bot[f.Key]
			if f.Secret && valuePresent(rawVal) {
				row[f.Key] = "***"
			} else {
				row[f.Key] = displayChannelValue(rawVal)
			}
		}
		agentID, _ := bot["agent_id"].(string)
		botName, _ := bot["bot_name"].(string)
		adapterID := channelAdapterID(spec.ID, agentID, botName, i, defaultReserved)
		st := statuses[adapterID]
		row["_adapter_id"] = adapterID
		row["_connected"] = st.Connected
		row["_detail"] = st.Detail
		if privilegedBotBlocked(bot, loader) {
			row["_blocked_reason"] = "privileged agent requires exposure approval"
		}
		out = append(out, row)
	}
	return out
}

func privilegedBotBlocked(bot map[string]any, loader *runtime.Loader) bool {
	if loader == nil || channels.ParseBoolValue(bot["outbound_only"], false) {
		return false
	}
	agentID, _ := bot["agent_id"].(string)
	agentID = strings.TrimSpace(agentID)
	if agentID == "" || channels.ParseBoolValue(bot["accept_privileged_exposure"], false) {
		return false
	}
	def := loader.Get(agentID)
	return tier.Compute(def, loader.Get) == tier.Privileged
}

func channelDiagnostics(spec channelSpec, cfg map[string]any, enabled, registered bool, st channels.AdapterStatus, bots []fiber.Map) []channelDiagnostic {
	var out []channelDiagnostic
	add := func(severity, message, remedy string) {
		out = append(out, channelDiagnostic{Severity: severity, Message: message, Remedy: remedy})
	}
	if spec.Always {
		return out
	}
	if cfg == nil || !enabled {
		add("info", "Channel is disabled.", "Enable it after saving required settings.")
		return out
	}
	for _, f := range spec.Fields {
		if !f.Required {
			continue
		}
		if channelSupportsBots(spec.ID) && len(bots) > 0 && !valuePresent(cfg[f.Key]) {
			continue
		}
		if f.Key == "agent_id" && spec.ID == "telegram" && channels.ParseBoolValue(cfg["outbound_only"], false) {
			continue
		}
		if !valuePresent(cfg[f.Key]) {
			add("fail", f.Label+" is missing.", "Open channel settings and fill this field.")
		}
	}
	if !registered {
		add("warn", "Adapter is not registered in the live gateway.", "Save the channel again — saving connects the adapter immediately; if it stays unregistered the settings are incomplete or the credentials are rejected.")
	} else if !st.Connected {
		detail := strings.TrimSpace(st.Detail)
		if detail == "" {
			detail = "adapter is offline"
		}
		add("warn", "Adapter is not connected: "+detail+".", "Check credentials, allowlists, network access, then reconnect.")
	}
	if valuePresent(cfg["token"]) || valuePresent(cfg["bot_token"]) || valuePresent(cfg["access_token"]) {
		if !valuePresent(cfg["default_output_to"]) {
			add("info", "No default output destination is configured.", "Set a default output destination if cron agents should send here without per-agent schedule.output.to.")
		}
	}
	for _, bot := range bots {
		agentID, _ := bot["agent_id"].(string)
		adapterID, _ := bot["_adapter_id"].(string)
		connected, _ := bot["_connected"].(bool)
		outboundOnly := channels.ParseBoolValue(bot["outbound_only"], false)
		if !outboundOnly && strings.TrimSpace(agentID) == "" {
			add("fail", "Bot mapping "+adapterID+" has no agent.", "Select an agent for interactive bot mappings or mark the row send-only.")
		}
		if !connected {
			if reason, _ := bot["_blocked_reason"].(string); reason != "" {
				add("fail", "Bot mapping "+adapterID+" is blocked: "+reason+".", "Open the bot mapping, enable privileged exposure, and save again.")
			} else {
				add("warn", "Bot mapping "+adapterID+" is not connected.", "Reconnect the mapping or check that the bot token is valid.")
			}
		}
	}
	return out
}

func rawBotList(raw any) []map[string]any {
	switch list := raw.(type) {
	case []map[string]any:
		return list
	case []any:
		out := make([]map[string]any, 0, len(list))
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}

func channelAdapterID(channelID, agentID, botName string, index int, defaultReserved bool) string {
	if index == 0 && !defaultReserved {
		return channelID
	}
	suffix := sanitizeChannelID(agentID)
	if suffix == "" {
		suffix = sanitizeChannelID(botName)
	}
	if suffix == "" {
		suffix = strconv.Itoa(index + 1)
	}
	return channelID + "-" + suffix
}

func sanitizeChannelID(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

// handleListChannels returns every supported channel merged with its current
// config (secrets masked) and live connection status. This is an ARRAY so the
// GUI can iterate it directly.
func (s *Server) handleListChannels(c *fiber.Ctx) error {
	statuses := s.channels.Statuses()

	out := make([]fiber.Map, 0, len(channelSpecs))
	for _, spec := range channelSpecs {
		cfg := s.config().Channels[spec.ID] // may be nil

		enabled := spec.Always
		if v, ok := cfg["enabled"].(bool); ok {
			enabled = v
		}

		// Build masked settings view + flag whether anything is configured
		settings := make(fiber.Map, len(spec.Fields))
		configured := false
		for _, f := range spec.Fields {
			raw := cfg[f.Key]
			if valuePresent(raw) {
				configured = true
			}
			if f.Secret && valuePresent(raw) {
				settings[f.Key] = "***"
			} else {
				settings[f.Key] = displayChannelValue(raw)
			}
		}
		bots := maskChannelBots(spec, cfg, statuses, s.loader)
		if len(bots) > 0 {
			configured = true
		}

		st, registered := statuses[spec.ID]

		out = append(out, fiber.Map{
			"id":          spec.ID,
			"name":        spec.Name,
			"always":      spec.Always,
			"enabled":     enabled,
			"configured":  configured,
			"registered":  registered,
			"schema":      spec.Fields,
			"bot_schema":  spec.Fields,
			"multi_bot":   channelSupportsBots(spec.ID),
			"bots":        bots,
			"settings":    settings,
			"diagnostics": channelDiagnostics(spec, cfg, enabled, registered, st, bots),
			"status": fiber.Map{
				"connected": st.Connected,
				"detail":    st.Detail,
				"qr_code":   st.QRCode,
			},
		})
	}
	return c.JSON(fiber.Map{"channels": out})
}

// handleTestChannelDelivery sends a real outbound message through the live
// channel registry. It exists so operators can verify a channel destination
// from the same screen where they configure it, instead of guessing from logs.
func (s *Server) handleTestChannelDelivery(c *fiber.Ctx) error {
	id := strings.TrimSpace(c.Params("id"))
	if channelSpecByID(id) == nil {
		return s.errMsg(c, fiber.StatusNotFound, "unknown channel: "+id)
	}
	if s.channels == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "channel registry is unavailable")
	}
	statuses := s.channels.Statuses()

	var req struct {
		AdapterID   string `json:"adapter_id"`
		To          string `json:"to"`
		Destination string `json:"destination"`
		ChatID      string `json:"chat_id"`
		ChannelID   string `json:"channel_id"`
		Text        string `json:"text"`
		Message     string `json:"message"`
	}
	if len(c.Body()) > 0 {
		if err := c.BodyParser(&req); err != nil {
			return s.errJSON(c, fiber.StatusBadRequest, err)
		}
	}

	adapterID := strings.TrimSpace(req.AdapterID)
	if adapterID == "" {
		adapterID = id
	}
	cfg := s.config().Channels[id]
	to := firstNonBlank(req.To, req.Destination, req.ChatID, req.ChannelID)
	if to == "" {
		to = channelDefaultDestination(cfg, id, adapterID)
	}
	// WhatsApp Web's safest test destination is the linked account's own
	// "Message yourself" chat. The sidecar resolves this sentinel after
	// pairing, so a user does not need to discover or type a WhatsApp JID just
	// to verify delivery.
	if to == "" && adapterID == "whatsapp_web" {
		to = "self"
	}
	if to == "" && adapterID != "webhook" && adapterID != "teams" && adapterID != "google_chat" {
		return s.errMsg(c, fiber.StatusBadRequest, "destination is required; provide to or configure default_output_to")
	}

	text := firstNonBlank(req.Text, req.Message)
	if text == "" {
		text = "Soulacy channel delivery test from " + adapterID + " at " + time.Now().Format(time.RFC3339)
	}
	if _, ok := statuses[adapterID]; !ok {
		return s.errMsg(c, fiber.StatusBadRequest, "channel "+adapterID+" is not registered; save its settings again to reconnect it")
	}
	out := message.Message{
		ID:        uuid.New().String(),
		SessionID: "channel-test-" + uuid.New().String(),
		AgentID:   "channel-test",
		Channel:   adapterID,
		ThreadID:  to,
		UserID:    "operator",
		Username:  "operator",
		Role:      message.RoleAssistant,
		Parts:     message.Text(text),
		Metadata:  map[string]string{"source": "channels.test"},
		CreatedAt: time.Now().UTC(),
	}
	if err := s.channels.Send(c.UserContext(), out); err != nil {
		status := statuses[adapterID]
		diag := channels.DiagnoseDelivery(adapterID, to, true, status.Connected, err)
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error":          "channel delivery test failed: " + err.Error(),
			"channel":        adapterID,
			"channel_family": id,
			"to":             to,
			"diagnosis":      diag,
		})
	}
	return c.JSON(fiber.Map{"ok": true, "channel": adapterID, "channel_family": id, "to": to})
}

// handleDiagnoseChannelDelivery is the "delivery doctor" behind each mapping's
// Diagnose button. Unlike /test (which returns a raw error on failure), this
// always returns 200 with a structured, plain-language Diagnosis: what happened
// and how to fix it. With {"dry": true} it only runs precondition checks
// (destination set? adapter registered? connected?) without sending a message.
func (s *Server) handleDiagnoseChannelDelivery(c *fiber.Ctx) error {
	id := strings.TrimSpace(c.Params("id"))
	if channelSpecByID(id) == nil {
		return s.errMsg(c, fiber.StatusNotFound, "unknown channel: "+id)
	}
	if s.channels == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "channel registry is unavailable")
	}
	statuses := s.channels.Statuses()

	var req struct {
		AdapterID   string `json:"adapter_id"`
		To          string `json:"to"`
		Destination string `json:"destination"`
		ChatID      string `json:"chat_id"`
		ChannelID   string `json:"channel_id"`
		Text        string `json:"text"`
		Message     string `json:"message"`
		Dry         bool   `json:"dry"`
	}
	if len(c.Body()) > 0 {
		if err := c.BodyParser(&req); err != nil {
			return s.errJSON(c, fiber.StatusBadRequest, err)
		}
	}

	adapterID := strings.TrimSpace(req.AdapterID)
	if adapterID == "" {
		adapterID = id
	}
	cfg := s.config().Channels[id]
	to := firstNonBlank(req.To, req.Destination, req.ChatID, req.ChannelID)
	if to == "" {
		to = channelDefaultDestination(cfg, id, adapterID)
	}
	if to == "" && adapterID == "whatsapp_web" {
		to = "self"
	}

	status, registered := statuses[adapterID]

	respond := func(d channels.Diagnosis) error {
		return c.JSON(fiber.Map{
			"channel":        adapterID,
			"channel_family": id,
			"to":             to,
			"diagnosis":      d,
		})
	}

	// Precondition problems short-circuit before any send attempt.
	if to == "" && adapterID != "webhook" && adapterID != "teams" && adapterID != "google_chat" {
		return respond(channels.DiagnoseDelivery(adapterID, "", registered, status.Connected, nil))
	}
	if !registered {
		return respond(channels.DiagnoseDelivery(adapterID, to, false, false, nil))
	}
	if req.Dry {
		// No live send: report readiness only.
		return respond(channels.DiagnoseDelivery(adapterID, to, true, status.Connected, nil))
	}

	text := firstNonBlank(req.Text, req.Message)
	if text == "" {
		text = "Soulacy delivery doctor check from " + adapterID + " at " + time.Now().Format(time.RFC3339)
	}
	out := message.Message{
		ID:        uuid.New().String(),
		SessionID: "channel-diagnose-" + uuid.New().String(),
		AgentID:   "channel-diagnose",
		Channel:   adapterID,
		ThreadID:  to,
		UserID:    "operator",
		Username:  "operator",
		Role:      message.RoleAssistant,
		Parts:     message.Text(text),
		Metadata:  map[string]string{"source": "channels.diagnose"},
		CreatedAt: time.Now().UTC(),
	}
	sendErr := s.channels.Send(c.UserContext(), out)
	return respond(channels.DiagnoseDelivery(adapterID, to, true, status.Connected, sendErr))
}

func channelDefaultDestination(cfg map[string]any, channelID, adapterID string) string {
	if valuePresent(cfg["default_output_to"]) && (adapterID == "" || adapterID == channelID) {
		return strings.TrimSpace(fmt.Sprint(cfg["default_output_to"]))
	}
	hasDefaultBot := valuePresent(cfg["token"]) || valuePresent(cfg["bot_token"])
	for i, bot := range rawBotList(cfg["bots"]) {
		botAdapterID := channelAdapterID(channelID, cfgStringFromMap(bot, "agent_id"), cfgStringFromMap(bot, "bot_name"), i, hasDefaultBot)
		if botAdapterID == adapterID && valuePresent(bot["default_output_to"]) {
			return strings.TrimSpace(fmt.Sprint(bot["default_output_to"]))
		}
	}
	return ""
}

func cfgStringFromMap(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if s := strings.TrimSpace(value); s != "" {
			return s
		}
	}
	return ""
}

// handleUpdateChannel merges channel settings (and optional enabled flag) into
// config.yaml. Secret fields sent as "***" or empty are preserved (not clobbered).
func (s *Server) handleUpdateChannel(c *fiber.Ctx) error {
	id := c.Params("id")
	spec := channelSpecByID(id)
	if spec == nil {
		return s.errMsg(c, fiber.StatusNotFound, "unknown channel: "+id)
	}
	if s.cfgPath == "" {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "config file path unknown — cannot persist channel settings")
	}

	var req struct {
		Enabled  *bool            `json:"enabled"`
		Settings map[string]any   `json:"settings"`
		Bots     []map[string]any `json:"bots"`
	}
	if err := c.BodyParser(&req); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if channelReferencesProtectedSystem(id, req.Settings, req.Bots) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "system agent is web-only and cannot be assigned to external channels",
		})
	}

	// Existing in-memory settings for this channel (source of truth for secrets we keep).
	existing := s.config().Channels[id]

	// Apply to on-disk config.
	raw, err := readRawConfig(s.cfgPath)
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, "read config: "+err.Error())
	}
	channelsMap := getOrCreateMap(raw, "channels")
	chMap := getOrCreateMap(channelsMap, id)

	if req.Enabled != nil {
		chMap["enabled"] = *req.Enabled
	}
	for _, f := range spec.Fields {
		rawVal, present := req.Settings[f.Key]
		if !present {
			continue
		}
		var val string
		switch v := rawVal.(type) {
		case bool:
			val = strconv.FormatBool(v)
		case string:
			val = v
		case nil:
			val = ""
		default:
			val = fmt.Sprint(v)
		}
		// Preserve a secret when the client sends back the mask or blank.
		if f.Secret && (val == "" || val == "***") {
			if prev, ok := existing[f.Key].(string); ok && prev != "" {
				chMap[f.Key] = prev
			}
			continue
		}
		chMap[f.Key] = normalizeChannelValue(f.Key, val)
	}
	if req.Bots != nil {
		if len(req.Bots) == 0 {
			delete(chMap, "bots")
		} else if !channelSupportsBots(id) {
			return s.errMsg(c, fiber.StatusBadRequest, id+" does not support multiple bot mappings")
		} else {
			chMap["bots"] = normalizeChannelBots(*spec, req.Bots, existing["bots"])
		}
	}
	if channelMapReferencesProtectedSystem(id, chMap) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "system agent is web-only and cannot be assigned to external channels",
		})
	}

	if err := writeRawConfig(s.cfgPath, raw); err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, "write config: "+err.Error())
	}

	// Mirror into the live in-memory config so the list reflects changes pre-restart.
	// The PREVIOUS config is captured before the in-memory copy is replaced,
	// because it is what says which adapters are running right now and
	// therefore which have to be stopped. Reading it after the overwrite would
	// stop the adapters the NEW config describes — which, for an unchanged
	// channel, is the same set, and for a changed one is the wrong set.
	previous := s.channelConfigSnapshot(id)
	s.applyChannelToMemory(id, chMap)
	applied := s.applyChannelLive(c, id, previous, chMap)

	s.log.Info("channel settings updated via API", zap.String("channel", id))
	details := map[string]any{
		"enabled_changed": auditBoolPtrSet(req.Enabled),
		"setting_keys":    channelSettingKeys(req.Settings, spec),
	}
	if req.Bots != nil {
		details["bots_changed"] = true
		details["bots_count"] = len(req.Bots)
	}
	s.recordAdminAudit(c, "channel.update", "channel", id, "ok", details)
	return c.JSON(fiber.Map{
		"ok":               true,
		"message":          "Channel saved. " + applied,
		"restart_required": false,
	})
}

func (s *Server) handleStartWhatsAppWebPairing(c *fiber.Ctx) error {
	if s.cfgPath == "" {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "config file path unknown — cannot persist WhatsApp Web settings")
	}
	var req struct {
		AgentID          string `json:"agent_id"`
		TriggerPhrase    string `json:"trigger_phrase"`
		IgnoreGroups     *bool  `json:"ignore_groups"`
		AllowedChatIDs   string `json:"allowed_chat_ids"`
		AllowedSenderIDs string `json:"allowed_sender_ids"`
	}
	if err := c.BodyParser(&req); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	agentID := strings.TrimSpace(req.AgentID)
	if agentID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "agent_id is required")
	}
	if isProtectedSystemAgent(agentID) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "system agent is web-only and cannot be assigned to WhatsApp Web",
		})
	}
	if s.agents(c).Get(agentID) == nil {
		return s.errMsg(c, fiber.StatusBadRequest, "unknown agent: "+agentID)
	}

	raw, err := readRawConfig(s.cfgPath)
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, "read config: "+err.Error())
	}
	chMap := getOrCreateMap(getOrCreateMap(raw, "channels"), "whatsapp_web")
	chMap["enabled"] = true
	chMap["agent_id"] = agentID
	// NOTE: cfgMapStr (not fmt.Sprint) — a missing key is nil and
	// fmt.Sprint(nil) == "<nil>", which silently skipped every default on
	// FRESH configs (the "mkdir : no such file or directory" pair failure).
	if cfgMapStr(chMap, "command") == "" {
		chMap["command"] = "node"
	}
	resolvedCommand, resolveErr := wawebchan.ResolveExecutable(cfgMapStr(chMap, "command"))
	if resolveErr != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable,
			"WhatsApp Web needs Node.js, but the gateway service could not find it. Install Node.js or set channels.whatsapp_web.command to its absolute path: "+resolveErr.Error())
	}
	// Persist the absolute path so later service restarts do not depend on the
	// interactive shell's PATH.
	chMap["command"] = resolvedCommand
	if cfgMapStr(chMap, "session_dir") == "" {
		base := filepath.Dir(s.config().Memory.Dir)
		if base == "." || base == "" {
			base = filepath.Dir(s.cfgPath)
		}
		chMap["session_dir"] = filepath.Join(base, "whatsapp-web")
	}
	// The sidecar script + its Baileys dependency must exist for INSTALLED
	// binaries, not just repo checkouts: materialise the embedded script
	// into the session dir and npm-install the dependency next to it when
	// the configured args are absent or point at a missing file.
	sessionDirVal := cfgMapStr(chMap, "session_dir")
	if sessionDirVal == "" {
		return s.errMsg(c, fiber.StatusInternalServerError, "whatsapp_web: session_dir could not be resolved")
	}
	existingArgs := parseStringListValue(chMap["args"])
	switch {
	case len(existingArgs) == 0 || !pathExists(existingArgs[0]):
		scriptPath, serr := wawebchan.EnsureSidecarScript(sessionDirVal)
		if serr != nil {
			return s.errMsg(c, fiber.StatusInternalServerError, serr.Error())
		}
		chMap["args"] = []string{scriptPath}
	case filepath.Base(existingArgs[0]) == wawebchan.SidecarScriptName:
		// Managed script: re-sync its content so binary upgrades actually
		// ship sidecar fixes (the file existing must not freeze it forever).
		if _, serr := wawebchan.EnsureSidecarScript(filepath.Dir(existingArgs[0])); serr != nil {
			return s.errMsg(c, fiber.StatusInternalServerError, serr.Error())
		}
	}
	if scriptArgs := parseStringListValue(chMap["args"]); len(scriptArgs) > 0 {
		if berr := wawebchan.EnsureBaileys(c.Context(), filepath.Dir(scriptArgs[0])); berr != nil {
			return s.errMsg(c, fiber.StatusInternalServerError, berr.Error())
		}
	}
	if cfgMapStr(chMap, "account_id") == "" {
		chMap["account_id"] = "default"
	}
	if cfgMapStr(chMap, "bot_name") == "" {
		chMap["bot_name"] = "WhatsApp Web"
	}
	if strings.TrimSpace(req.TriggerPhrase) != "" {
		chMap["trigger_phrase"] = strings.TrimSpace(req.TriggerPhrase)
	} else if cfgMapStr(chMap, "trigger_phrase") == "" {
		chMap["trigger_phrase"] = "!soulacy"
	}
	if req.IgnoreGroups != nil {
		chMap["ignore_groups"] = *req.IgnoreGroups
	} else if _, ok := chMap["ignore_groups"]; !ok {
		chMap["ignore_groups"] = true
	}
	if strings.TrimSpace(req.AllowedChatIDs) != "" {
		chMap["allowed_chat_ids"] = strings.TrimSpace(req.AllowedChatIDs)
	}
	if strings.TrimSpace(req.AllowedSenderIDs) != "" {
		chMap["allowed_sender_ids"] = strings.TrimSpace(req.AllowedSenderIDs)
	}

	if err := writeRawConfig(s.cfgPath, raw); err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, "write config: "+err.Error())
	}
	s.applyChannelToMemory("whatsapp_web", chMap)

	command, _ := chMap["command"].(string)
	args := parseStringListValue(chMap["args"])
	sessionDir, _ := chMap["session_dir"].(string)
	accountID, _ := chMap["account_id"].(string)
	activation := channels.ActivationPolicy{
		TriggerPhrase:    strings.TrimSpace(fmt.Sprint(chMap["trigger_phrase"])),
		IgnoreGroups:     parseBoolValue(chMap["ignore_groups"], true),
		AllowedThreadIDs: parseDelimitedStringList(chMap["allowed_chat_ids"]),
		AllowedUserIDs:   parseDelimitedStringList(chMap["allowed_sender_ids"]),
	}
	adapter := wawebchan.New("whatsapp_web", command, args, sessionDir, agentID, accountID, activation, s.log)
	if err := s.channels.StartAdapter(context.Background(), adapter); err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, "start WhatsApp Web sidecar: "+err.Error())
	}

	return c.JSON(fiber.Map{
		"ok":      true,
		"message": "WhatsApp Web pairing started. Scan the QR when it appears.",
	})
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// cfgMapStr returns a trimmed string value from a raw config map. Missing
// keys and non-string values yield "" (unlike fmt.Sprint, which renders nil
// as "<nil>" and silently defeats empty-checks).
func cfgMapStr(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}

func parseStringListValue(raw any) []string {
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		return strings.Fields(v)
	default:
		return nil
	}
}

func parseDelimitedStringList(raw any) []string {
	switch v := raw.(type) {
	case []string:
		return parseStringListValue(v)
	case []any:
		return parseStringListValue(v)
	case string:
		parts := strings.FieldsFunc(v, func(r rune) bool {
			return r == ',' || r == '\n' || r == '\t'
		})
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	default:
		return nil
	}
}

func parseBoolValue(raw any, fallback bool) bool {
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "yes", "1", "on":
			return true
		case "false", "no", "0", "off":
			return false
		}
	}
	return fallback
}

// channelConfigSnapshot copies a channel's live configuration.
//
// A copy rather than the map itself: applyChannelToMemory replaces the entry,
// and a caller holding the old map would otherwise be holding whatever the
// replacement left behind. The snapshot is what the hot-apply path uses to work
// out which adapters are currently running.
func (s *Server) channelConfigSnapshot(id string) map[string]any {
	if s == nil || s.config() == nil || s.config().Channels == nil {
		return nil
	}
	current, ok := s.config().Channels[id]
	if !ok {
		return nil
	}
	out := make(map[string]any, len(current))
	for key, value := range current {
		out[key] = value
	}
	return out
}

// applyChannelToMemory copies a freshly-written channel config block into the
// live config so GET /channels reflects it without a restart.
func (s *Server) applyChannelToMemory(id string, chMap map[string]any) {
	merged := map[string]any{}
	for k, v := range chMap {
		merged[k] = v
	}
	s.setChannelConfig(id, merged)
}

func (s *Server) setChannelEnabled(c *fiber.Ctx, enabled bool) error {
	id := c.Params("id")
	spec := channelSpecByID(id)
	if spec == nil {
		return s.errMsg(c, fiber.StatusNotFound, "unknown channel: "+id)
	}
	if spec.Always {
		return s.errMsg(c, fiber.StatusBadRequest, id+" is always enabled")
	}
	if s.cfgPath == "" {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "config file path unknown — cannot persist")
	}
	raw, err := readRawConfig(s.cfgPath)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	chMap := getOrCreateMap(getOrCreateMap(raw, "channels"), id)
	chMap["enabled"] = enabled
	if err := writeRawConfig(s.cfgPath, raw); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	previous := s.channelConfigSnapshot(id)
	s.applyChannelToMemory(id, chMap)
	applied := s.applyChannelLive(c, id, previous, chMap)
	action := "channel.disable"
	if enabled {
		action = "channel.enable"
	}
	s.recordAdminAudit(c, action, "channel", id, "ok", map[string]any{"enabled": enabled})
	return c.JSON(fiber.Map{
		"ok":               true,
		"id":               id,
		"enabled":          enabled,
		"message":          applied,
		"restart_required": false,
	})
}

func (s *Server) handleEnableChannel(c *fiber.Ctx) error  { return s.setChannelEnabled(c, true) }
func (s *Server) handleDisableChannel(c *fiber.Ctx) error { return s.setChannelEnabled(c, false) }

// --- Schedule ---

func (s *Server) handleListSchedule(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"schedule": s.schedules(c).Entries()})
}

func (s *Server) handleManualTrigger(c *fiber.Ctx) error {
	id := c.Params("id")
	if isProtectedSystemAgent(id) {
		return protectedSystemAgentResponse(c)
	}
	def := s.agents(c).Get(id)
	if def == nil {
		return s.errMsg(c, fiber.StatusNotFound, "agent not found")
	}

	// Block concurrent runs: if this agent is already executing (manual or
	// scheduled), refuse rather than running it twice.
	// Scoped to the request's workspace. The run lock is per (workspace,
	// agent): unscoped, one tenant's manual run of "daily-report" refused
	// every other tenant's, and the 409 named an agent that was not theirs.
	if !s.schedules(c).TryStartRun(id) {
		return s.errMsg(c, fiber.StatusConflict, "agent is already running")
	}
	defer s.schedules(c).FinishRun(id)

	sessionID := fmt.Sprintf("manual-%s-%d", id, time.Now().UnixNano())
	msg := message.Message{
		ID:        uuid.New().String(),
		SessionID: sessionID,
		AgentID:   id,
		Channel:   "http",
		ThreadID:  "manual-trigger",
		UserID:    "manual-trigger",
		Username:  "manual-trigger",
		Role:      message.RoleUser,
		Parts:     message.Text("__trigger:manual__"),
		CreatedAt: time.Now().UTC(),
	}

	runStart := time.Now()
	s.log.Info("manual trigger started",
		zap.String("agent", id),
		zap.String("session", sessionID),
		zap.String("llm_provider", def.LLM.Provider),
		zap.String("llm_model", def.LLM.Model),
	)

	// Decouple client connection drop from background execution. Use the
	// agent's run_timeout — long-running tools (e.g. NotebookLM audio gen)
	// would blow the old 120s ceiling.
	ctx, cancel := context.WithTimeout(detachedRequestContext(c), s.resolveRunTimeout(def))
	defer cancel()
	ctx = withRequestPrincipal(c, ctx)

	reply, err := s.engine.Handle(ctx, msg)
	elapsed := time.Since(runStart).Round(time.Millisecond)
	if err != nil {
		s.log.Error("manual trigger failed",
			zap.String("agent", id),
			zap.String("session", sessionID),
			zap.Duration("elapsed", elapsed),
			zap.Error(err),
		)
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}

	replyText := ""
	for _, p := range reply.Parts {
		if p.Type == message.ContentText && p.Text != "" {
			replyText = p.Text
			break
		}
	}
	s.log.Info("manual trigger completed",
		zap.String("agent", id),
		zap.String("session", sessionID),
		zap.Duration("elapsed", elapsed),
		zap.Int("reply_len", len(replyText)),
		zap.String("reply_preview", func() string {
			if len(replyText) > 120 {
				return replyText[:120] + "…"
			}
			return replyText
		}()),
	)

	// Publish to the configured output channel too, so a manual "Run" of a
	// scheduled agent tests the WHOLE path (not just the GUI preview) and the
	// result actually reaches Telegram/Slack/etc. Gated to agents that declare a
	// cron trigger or a resolvable output target so we never spam a channel from
	// an ad-hoc run of a plain chat agent.
	delivered := false
	if s.scheduler != nil && strings.TrimSpace(replyText) != "" &&
		(def.AppearsOn(agent.SurfaceSchedule) || s.scheduler.HasScheduledOutputTarget(def)) {
		s.scheduler.DeliverScheduledReply(ctx, def, msg, replyText, "manual", reply.Metadata)
		delivered = s.scheduler.HasScheduledOutputTarget(def)
	}
	return c.JSON(fiber.Map{"result": replyText, "delivered": delivered})
}

func (s *Server) handleReplayAgentRun(c *fiber.Ctx) error {
	id := c.Params("id")
	if isProtectedSystemAgent(id) {
		return protectedSystemAgentResponse(c)
	}
	def := s.agents(c).Get(id)
	if def == nil {
		return s.errMsg(c, fiber.StatusNotFound, "agent not found")
	}
	if s.actions == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "action logging disabled")
	}
	var req struct {
		SessionID string `json:"session_id"`
	}
	if err := c.BodyParser(&req); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	if req.SessionID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "session_id is required")
	}

	events, err := s.actionLog(c).Tail(id, 5000)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	orig, found, err := replaySourceMessage(events, id, req.SessionID)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if !found {
		return s.errMsg(c, fiber.StatusNotFound, "message.in event not found for session")
	}

	if !s.schedules(c).TryStartRun(id) {
		return s.errMsg(c, fiber.StatusConflict, "agent is already running")
	}
	defer s.schedules(c).FinishRun(id)

	replaySession := fmt.Sprintf("replay-%s-%s", req.SessionID, uuid.NewString()[:8])
	msg := orig
	msg.ID = uuid.NewString()
	msg.SessionID = replaySession
	msg.AgentID = id
	msg.Channel = "http"
	msg.ThreadID = "replay:" + req.SessionID
	if msg.UserID == "" {
		msg.UserID = "replay"
	}
	if msg.Username == "" {
		msg.Username = "replay"
	}
	msg.Role = message.RoleUser
	msg.CreatedAt = time.Now().UTC()
	if msg.Metadata == nil {
		msg.Metadata = map[string]string{}
	}
	msg.Metadata["trigger"] = "replay"
	msg.Metadata["replay_from_session"] = req.SessionID
	msg.Metadata["replay_from_channel"] = orig.Channel

	ctx, cancel := context.WithTimeout(detachedRequestContext(c), s.resolveRunTimeout(def))
	defer cancel()
	ctx = withRequestPrincipal(c, ctx)

	reply, err := s.engine.Handle(ctx, msg)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	replyText := ""
	for _, p := range reply.Parts {
		if p.Type == message.ContentText && p.Text != "" {
			replyText = p.Text
			break
		}
	}
	return c.JSON(fiber.Map{
		"agent_id":            id,
		"source_session_id":   req.SessionID,
		"replay_session_id":   replaySession,
		"source_channel":      orig.Channel,
		"source_message_id":   orig.ID,
		"replayed_as_channel": msg.Channel,
		"result":              replyText,
	})
}

func replaySourceMessage(events []message.Event, agentID, sessionID string) (message.Message, bool, error) {
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Type != "message.in" || ev.AgentID != agentID || ev.SessionID != sessionID {
			continue
		}
		var msg message.Message
		data, err := json.Marshal(ev.Payload)
		if err != nil {
			return message.Message{}, false, fmt.Errorf("marshal source payload: %w", err)
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			return message.Message{}, false, fmt.Errorf("parse source message: %w", err)
		}
		if msg.AgentID == "" {
			msg.AgentID = agentID
		}
		if msg.SessionID == "" {
			msg.SessionID = sessionID
		}
		return msg, true, nil
	}
	return message.Message{}, false, nil
}

func (s *Server) handleTestScheduledOutput(c *fiber.Ctx) error {
	id := c.Params("id")
	if isProtectedSystemAgent(id) {
		return protectedSystemAgentResponse(c)
	}
	def := s.agents(c).Get(id)
	if def == nil {
		return s.errMsg(c, fiber.StatusNotFound, "agent not found")
	}
	if def.Schedule == nil || def.Schedule.Output == nil {
		return s.errMsg(c, fiber.StatusBadRequest, "agent has no schedule.output configured")
	}
	outCfg := def.Schedule.Output
	channelID := strings.TrimSpace(outCfg.Channel)
	to := strings.TrimSpace(outCfg.To)
	if channelID == "" || to == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "schedule.output requires both channel and destination")
	}
	if s.channels == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "channel registry is unavailable")
	}
	statuses := s.channels.Statuses()
	st, ok := statuses[channelID]
	if !ok {
		return s.errMsg(c, fiber.StatusBadRequest, "scheduled output channel is not registered: "+channelID)
	}
	if !st.Connected {
		detail := strings.TrimSpace(st.Detail)
		if detail == "" {
			detail = "adapter is offline"
		}
		return s.errMsg(c, fiber.StatusBadGateway, "scheduled output channel is not connected: "+detail)
	}

	replyText := fmt.Sprintf("Soulacy scheduled-output test for %s at %s.", def.ID, time.Now().UTC().Format(time.RFC3339))
	text := scheduler.RenderScheduledOutput(outCfg.Template, def, replyText, "test_output")
	msg := message.Message{
		ID:        uuid.New().String(),
		SessionID: fmt.Sprintf("test-output-%s-%d", def.ID, time.Now().UnixNano()),
		AgentID:   def.ID,
		Channel:   channelID,
		ThreadID:  to,
		UserID:    "schedule-test",
		Username:  "schedule-test",
		Role:      message.RoleAssistant,
		Parts:     message.Text(text),
		Metadata: map[string]string{
			"trigger":  "test_output",
			"bot_name": outCfg.BotName,
		},
		CreatedAt: time.Now().UTC(),
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.UserContext()), 20*time.Second)
	defer cancel()
	if err := s.channels.Send(ctx, msg); err != nil {
		s.log.Warn("scheduled output test failed",
			zap.String("agent", def.ID),
			zap.String("channel", channelID),
			zap.String("to", to),
			zap.String("bot_name", outCfg.BotName),
			zap.Error(err))
		return s.errJSON(c, fiber.StatusBadGateway, err)
	}
	s.log.Info("scheduled output test sent",
		zap.String("agent", def.ID),
		zap.String("channel", channelID),
		zap.String("to", to),
		zap.String("bot_name", outCfg.BotName))
	return c.JSON(fiber.Map{
		"ok":       true,
		"agent_id": def.ID,
		"channel":  channelID,
		"to":       to,
		"message":  text,
	})
}

// resolveRunTimeout returns the max wall-clock duration for one agent run.
// Honors agent.RunTimeout (Go duration string) when set, else falls back to
// the gateway default (15 minutes — enough for chains involving slow tools
// like NotebookLM audio generation).
func resolveRunTimeout(def *agent.Definition) time.Duration {
	return def.ResolvedRunTimeout(15 * time.Minute)
}

func (s *Server) resolveRunTimeout(def *agent.Definition) time.Duration {
	fallback := 15 * time.Minute
	if s != nil && s.config() != nil {
		if d, err := time.ParseDuration(s.config().Runtime.Timeouts.Run); err == nil && d > 0 {
			fallback = d
		}
	}
	return def.ResolvedRunTimeout(fallback)
}

// handleScheduleStatus returns live run state for polling by the GUI:
//   - running:   agentID → ISO start time for agents currently executing
//   - next:      agentID → ISO next scheduled fire time (enabled cron agents)
//   - backfills: agentID → {missed_at, replayed_at, cron, window, late_by} for
//     the most recent startup catch-up since the process started
//
// The `backfills` map exists so the Schedule page can render "Auto-replayed
// on ..." beside an agent whose gateway missed one of its scheduled fires —
// otherwise a boot-time backfill is invisible to the operator, which is
// exactly the E4 (Cohort E — Schedule failure handling) gap.
func (s *Server) handleScheduleStatus(c *fiber.Ctx) error {
	schedules := s.schedules(c)
	running := schedules.RunningSnapshot()
	runningOut := make(fiber.Map, len(running))
	for id, t := range running {
		runningOut[id] = t.UTC()
	}

	next := fiber.Map{}
	for _, e := range schedules.Entries() {
		if !e.Next.IsZero() {
			next[e.AgentID] = e.Next.UTC()
		}
	}

	backfillsOut := fiber.Map{}
	for id, b := range schedules.LastBackfills() {
		backfillsOut[id] = fiber.Map{
			"missed_at":   b.MissedAt,
			"replayed_at": b.ReplayedAt,
			"cron":        b.Cron,
			"window":      b.Window,
			"late_by":     b.LateBy,
		}
	}
	return c.JSON(fiber.Map{"running": runningOut, "next": next, "backfills": backfillsOut})
}

// handleToolCatalog returns every tool an agent could be wired to:
//   - python_tools: every *.py in ~/.soulacy/tools/ (the convention) and in
//     <agent_dir>/tools/ for each configured agent dir.
//   - mcp_tools:    every tool exposed by a currently-connected MCP server,
//     with the full namespaced name (mcp__<server>__<tool>).
//   - builtins:     Go-native tools shipped with the engine (web_search,
//     read_skill, etc.).
//
// Used by the Agents Edit page's python_file picker and by the Builder so the
// LLM picks real tools instead of inventing names.
//
// PRODUCTION_AUDIT → HIGH/Caching: this used to do dozens of stat/open/read
// syscalls per request (GUI polls, Builder calls every turn). Now wrapped in
// a 30s TTL cache + extractPythonDocstring memoises on (path, mtime). The
// file watcher additionally calls InvalidateToolCatalog() when it sees a
// *.py change so freshly-added tools surface immediately instead of waiting
// up to 30s. MCP tools refresh on every call because the MCP client tracks
// connection state separately and we want that to be live.
func (s *Server) handleToolCatalog(c *fiber.Ctx) error {
	catalog := s.toolCatalog(mcpWorkspace(c))
	return c.JSON(catalog)
}

// toolCatalogPayload is the shape returned by /tool-catalog. Defined as a
// named type so we can cache fully-rendered responses without re-marshalling.
type toolCatalogPayload struct {
	PythonTools []pyToolView      `json:"python_tools"`
	MCPTools    []mcpToolView     `json:"mcp_tools"`
	Builtins    []builtinToolView `json:"builtins"`
}
type pyToolView struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Description string `json:"description"`
}
type mcpToolView struct {
	FullName    string `json:"full_name"`
	Name        string `json:"name"`
	Server      string `json:"server"`
	Description string `json:"description"`
	// Params is a compact hint of the tool's argument names (required marked
	// with *), e.g. "title*:string, description:string" — so a caller (notably
	// the Studio compiler) passes the RIGHT keyword arguments instead of guessing.
	Params string `json:"params,omitempty"`
	// Risk is the 5-tier risk classification for this MCP tool.
	Risk string `json:"risk,omitempty"`
}
type builtinToolView struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Risk is the 5-tier risk classification (safe/write/network/privileged/
	// shell_system) so the UI can show a tool's blast radius before an agent is
	// bound to a channel.
	Risk string `json:"risk,omitempty"`
}

const toolCatalogTTL = 30 * time.Second

// toolCatalog returns a fresh-enough catalog. The Python-tools portion is
// memoised behind a TTL; MCP and built-ins are recomputed because they're
// cheap (in-memory snapshots) and we want them live.
func (s *Server) toolCatalog(workspaceID string) toolCatalogPayload {
	// Hot-path: serve the cached Python list if still fresh.
	s.toolCatalogMu.Lock()
	cached := s.toolCatalogCache
	cachedAt := s.toolCatalogAt
	s.toolCatalogMu.Unlock()

	var pys []pyToolView
	if cached != nil && time.Since(cachedAt) < toolCatalogTTL {
		pys = cached
	} else {
		pys = s.scanPythonTools()
		s.toolCatalogMu.Lock()
		s.toolCatalogCache = pys
		s.toolCatalogAt = time.Now()
		s.toolCatalogMu.Unlock()
	}

	mcps := s.snapshotMCPTools(workspaceID)
	builtins := s.snapshotBuiltins()
	return toolCatalogPayload{PythonTools: pys, MCPTools: mcps, Builtins: builtins}
}

// InvalidateToolCatalog drops the cache so the next call rescans. Called by
// the file watcher when it sees a *.py change under any tool dir.
func (s *Server) InvalidateToolCatalog() {
	s.toolCatalogMu.Lock()
	s.toolCatalogCache = nil
	s.toolCatalogMu.Unlock()
}

// PythonToolDirs returns the on-disk directories the catalog watches for
// .py files. Used by the file watcher to know which paths to monitor.
func (s *Server) PythonToolDirs() []string {
	var dirs []string
	if home, err := os.UserHomeDir(); err == nil {
		if wsPaths, werr := config.ResolveWorkspace(); werr == nil {
			dirs = append(dirs, wsPaths.Tools)
		} else {
			dirs = append(dirs, filepath.Join(home, ".soulacy", "tools"))
		}
	}
	for _, ad := range s.config().AgentDirs {
		dirs = append(dirs, filepath.Join(ad, "tools"))
	}
	return dirs
}

func (s *Server) scanPythonTools() []pyToolView {
	var pys []pyToolView
	seen := map[string]bool{}
	scanDir := func(dir string) {
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
			name := strings.TrimSuffix(e.Name(), ".py")
			pys = append(pys, pyToolView{
				Name: name, Path: full,
				Description: extractPythonDocstring(full),
			})
		}
	}
	for _, d := range s.PythonToolDirs() {
		scanDir(d)
	}
	return pys
}

func (s *Server) snapshotMCPTools(workspaceID string) []mcpToolView {
	var mcps []mcpToolView
	client := s.mcpForWorkspace(workspaceID)
	if client == nil {
		return mcps
	}
	for _, srv := range client.ServersSnapshot() {
		if !srv.Connected {
			continue
		}
		for _, t := range srv.Tools {
			mcps = append(mcps, mcpToolView{
				FullName: t.FullName, Name: t.Name,
				Server: srv.ID, Description: t.Description,
				Params: t.Params,
				Risk:   policy.RiskTierOf(t.FullName).String(),
			})
		}
	}
	return mcps
}

func (s *Server) snapshotBuiltins() []builtinToolView {
	var builtins []builtinToolView
	if s.engine == nil {
		return builtins
	}
	for _, b := range s.engine.Builtins() {
		builtins = append(builtins, builtinToolView{
			Name: b.Name, Description: b.Description,
			Risk: policy.RiskTierOf(b.Name).String(),
		})
	}
	return builtins
}

// docstringCache memoises extractPythonDocstring results by (path, mtime).
// PRODUCTION_AUDIT → LOW/Performance: was an 8KB read + regex per file per
// Builder turn. Now O(1) for unchanged files.
var (
	docstringMu    sync.RWMutex
	docstringCache = map[string]docstringEntry{}
)

type docstringEntry struct {
	mtime time.Time
	doc   string
}

// extractPythonDocstring reads the first triple-quoted docstring out of a
// Python file (capped at the first 8KB). Cached by (path, mtime).
func extractPythonDocstring(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	mtime := info.ModTime()

	docstringMu.RLock()
	hit, ok := docstringCache[path]
	docstringMu.RUnlock()
	if ok && hit.mtime.Equal(mtime) {
		return hit.doc
	}

	doc := extractPythonDocstringUncached(path)
	docstringMu.Lock()
	docstringCache[path] = docstringEntry{mtime: mtime, doc: doc}
	docstringMu.Unlock()
	return doc
}

func extractPythonDocstringUncached(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, _ := f.Read(buf)
	src := string(buf[:n])
	// Look for the first """ or ''' after optional shebang/imports.
	for _, q := range []string{`"""`, `'''`} {
		start := strings.Index(src, q)
		if start < 0 {
			continue
		}
		rest := src[start+3:]
		end := strings.Index(rest, q)
		if end < 0 {
			continue
		}
		doc := strings.TrimSpace(rest[:end])
		// Keep just the first paragraph — short and useful for tooltips.
		if i := strings.Index(doc, "\n\n"); i >= 0 {
			doc = doc[:i]
		}
		// Single-line collapse
		doc = strings.ReplaceAll(doc, "\n", " ")
		if len(doc) > 240 {
			doc = doc[:240] + "…"
		}
		return doc
	}
	return ""
}

// handleListMCP returns the configured MCP servers with connection status and
// each server's tool list. Used by the MCP page in the GUI.
func (s *Server) handleListMCP(c *fiber.Ctx) error {
	client := s.mcpFor(c)
	if client == nil {
		return c.JSON(fiber.Map{"servers": []any{}, "note": "MCP not initialised"})
	}
	return c.JSON(fiber.Map{"servers": redactMCPServers(client.ServersSnapshot())})
}

// redactMCPServers masks the credential-bearing fields of an MCP server before
// the status list leaves the process.
//
// An MCP server is configured with `env` and `headers` — which is where its
// GITHUB_TOKEN or `Authorization: Bearer sk-…` lives — and the snapshot
// returned them verbatim to anyone holding mcp:read, a permission the VIEWER
// role has. The channels list and the providers list have both redacted for
// this exact reason (see safeChannelsView); the MCP list did not.
//
// Names are kept and values masked: an operator debugging a server needs to see
// THAT a token is configured, never what it is.
func redactMCPServers(servers []mcp.ServerStatus) []mcp.ServerStatus {
	out := make([]mcp.ServerStatus, 0, len(servers))
	for _, srv := range servers {
		srv.Env = maskValues(srv.Env)
		srv.Headers = maskValues(srv.Headers)
		out = append(out, srv)
	}
	return out
}

// maskValues copies a map, replacing every non-empty value with "***". Every
// value is masked rather than only the secret-looking ones: an MCP server's env
// is arbitrary operator-supplied configuration, so there is no reliable way to
// tell a credential from a setting, and guessing wrong leaks the credential.
func maskValues(m map[string]string) map[string]string {
	if len(m) == 0 {
		return m
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if strings.TrimSpace(v) == "" {
			out[k] = v
			continue
		}
		out[k] = "***"
	}
	return out
}

// mcpServerBody is the shape the GUI POSTs/PATCHes to add or edit an MCP
// server. Mirrors config.MCPServerConfig with JSON tags so the same payload
// can be re-marshalled back into YAML cleanly.
type mcpServerBody struct {
	ID        string            `json:"id"`
	Transport string            `json:"transport"`
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	Env       map[string]string `json:"env"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
}

// validateMCPServer enforces the per-transport invariants. Returns "" on success
// or a user-facing error string on failure (so the GUI can surface it inline).
func validateMCPServer(body mcpServerBody) string {
	t := strings.ToLower(strings.TrimSpace(body.Transport))
	switch t {
	case "", "stdio":
		if strings.TrimSpace(body.Command) == "" {
			return "stdio transport requires a `command` (e.g. 'npx' or '/usr/local/bin/server')"
		}
	case "http", "https":
		if strings.TrimSpace(body.URL) == "" {
			return "http transport requires a `url`"
		}
	default:
		return fmt.Sprintf("unknown transport %q — expected stdio or http", body.Transport)
	}
	return ""
}

// mcpBodyToServerConfig converts an mcpServerBody to an mcp.ServerConfig for
// hot-adding to the live client.
func mcpBodyToServerConfig(body mcpServerBody) mcp.ServerConfig {
	return mcp.ServerConfig{
		Transport: body.Transport,
		Command:   body.Command,
		Args:      body.Args,
		Env:       body.Env,
		URL:       body.URL,
		Headers:   body.Headers,
	}
}

// handleCreateMCPServer adds a new MCP server to config.yaml and hot-connects
// it immediately — no gateway restart required.
func (s *Server) handleCreateMCPServer(c *fiber.Ctx) error {
	if s.cfgPath == "" {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "config file path unknown — cannot persist")
	}
	var body mcpServerBody
	if err := c.BodyParser(&body); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	id := strings.TrimSpace(body.ID)
	if id == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "id is required")
	}
	if !validMCPID(id) {
		return s.errMsg(c, fiber.StatusBadRequest, "id may contain only letters, digits, '-' and '_'")
	}
	if msg := validateMCPServer(body); msg != "" {
		return s.errMsg(c, fiber.StatusBadRequest, msg)
	}

	raw, err := readRawConfig(s.cfgPath)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	mcpMap := getOrCreateMap(raw, "mcp")
	serversMap := getOrCreateMap(mcpMap, "servers")
	if _, exists := serversMap[id]; exists {
		return s.errMsg(c, fiber.StatusConflict, fmt.Sprintf("server %q already exists; use PATCH to edit", id))
	}
	serversMap[id] = mcpServerToYAML(body)

	if err := writeRawConfig(s.cfgPath, raw); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.log.Info("mcp server created", zap.String("server", id))

	// Hot-connect: start the server live without a restart.
	connectErr := ""
	if err := s.mcpAddServer(c, id, mcpBodyToServerConfig(body)); err != nil {
		connectErr = err.Error()
	}

	resp := fiber.Map{"ok": true, "id": id, "restart_needed": false, "message": "Connected."}
	if connectErr != "" {
		resp["message"] = "Saved, but could not connect: " + connectErr
		resp["connect_error"] = connectErr
	}
	return c.Status(fiber.StatusCreated).JSON(resp)
}

// handleUpdateMCPServer overwrites an existing MCP server config in
// config.yaml. The id in the URL is authoritative — body.id is ignored if set.
func (s *Server) handleUpdateMCPServer(c *fiber.Ctx) error {
	if s.cfgPath == "" {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "config file path unknown — cannot persist")
	}
	id := c.Params("id")
	if id == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "id path parameter is required")
	}
	var body mcpServerBody
	if err := c.BodyParser(&body); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	body.ID = id // URL wins
	if msg := validateMCPServer(body); msg != "" {
		return s.errMsg(c, fiber.StatusBadRequest, msg)
	}

	raw, err := readRawConfig(s.cfgPath)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	mcpMap := getOrCreateMap(raw, "mcp")
	serversMap := getOrCreateMap(mcpMap, "servers")
	if _, exists := serversMap[id]; !exists {
		return s.errMsg(c, fiber.StatusNotFound, fmt.Sprintf("server %q not found", id))
	}
	serversMap[id] = mcpServerToYAML(body)

	if err := writeRawConfig(s.cfgPath, raw); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.log.Info("mcp server updated", zap.String("server", id))

	// Hot-reconnect: remove old process, start updated one.
	connectErr := ""
	if err := s.mcpAddServer(c, id, mcpBodyToServerConfig(body)); err != nil {
		connectErr = err.Error()
	}

	resp := fiber.Map{"ok": true, "id": id, "restart_needed": false, "message": "Updated and reconnected."}
	if connectErr != "" {
		resp["message"] = "Saved, but could not reconnect: " + connectErr
		resp["connect_error"] = connectErr
	}
	return c.JSON(resp)
}

// handleDeleteMCPServer removes an MCP server from config.yaml.
func (s *Server) handleDeleteMCPServer(c *fiber.Ctx) error {
	if s.cfgPath == "" {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "config file path unknown — cannot persist")
	}
	id := c.Params("id")
	if id == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "id is required")
	}

	raw, err := readRawConfig(s.cfgPath)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if mcpMap, ok := raw["mcp"].(map[string]any); ok {
		if serversMap, ok := mcpMap["servers"].(map[string]any); ok {
			delete(serversMap, id)
		}
	}
	if err := writeRawConfig(s.cfgPath, raw); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.log.Info("mcp server deleted", zap.String("server", id))

	// Hot-disconnect: stop the process immediately.
	_ = s.mcpRemoveServer(c, id)

	return c.JSON(fiber.Map{
		"ok":             true,
		"id":             id,
		"restart_needed": false,
		"message":        "Removed and disconnected.",
	})
}

// handleTestMCPServer attempts a minimal reachability check WITHOUT modifying
// any state. For stdio it confirms the executable resolves on PATH; for http
// it issues a HEAD request with a short timeout. Returns ok=true on success
// so the GUI can show a green checkmark before the user clicks Save.
func (s *Server) handleTestMCPServer(c *fiber.Ctx) error {
	var body mcpServerBody
	if err := c.BodyParser(&body); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if msg := validateMCPServer(body); msg != "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"ok": false, "error": msg})
	}

	transport := strings.ToLower(strings.TrimSpace(body.Transport))
	switch transport {
	case "", "stdio":
		// LookPath returns the resolved path if the command exists. Doesn't run it.
		path, err := exec.LookPath(body.Command)
		if err != nil {
			return c.JSON(fiber.Map{
				"ok":      false,
				"error":   fmt.Sprintf("command %q not found in PATH", body.Command),
				"details": err.Error(),
			})
		}
		return c.JSON(fiber.Map{"ok": true, "resolved_command": path})
	case "http", "https":
		req, err := http.NewRequestWithContext(c.Context(), http.MethodHead, body.URL, nil)
		if err != nil {
			return c.JSON(fiber.Map{"ok": false, "error": err.Error()})
		}
		for k, v := range body.Headers {
			req.Header.Set(k, v)
		}
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return c.JSON(fiber.Map{"ok": false, "error": fmt.Sprintf("could not reach %s: %v", body.URL, err)})
		}
		// defer ensures we close the body regardless of which branch we return on.
		defer resp.Body.Close()
		// 405/501 from a HEAD is acceptable — the server is reachable, just doesn't
		// support HEAD. Real MCP handshake happens at gateway boot.
		return c.JSON(fiber.Map{"ok": true, "status_code": resp.StatusCode})
	}
	return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"ok": false, "error": "unknown transport"})
}

// mcpServerToYAML renders an mcpServerBody as the YAML-friendly map shape
// that ends up in config.yaml.
func mcpServerToYAML(body mcpServerBody) map[string]any {
	t := strings.ToLower(strings.TrimSpace(body.Transport))
	if t == "" {
		t = "stdio"
	}
	out := map[string]any{"transport": t}
	if t == "stdio" {
		if body.Command != "" {
			out["command"] = body.Command
		}
		if len(body.Args) > 0 {
			args := make([]any, len(body.Args))
			for i, a := range body.Args {
				args[i] = a
			}
			out["args"] = args
		}
		if len(body.Env) > 0 {
			out["env"] = mapToAny(body.Env)
		}
	} else {
		if body.URL != "" {
			out["url"] = body.URL
		}
		if len(body.Headers) > 0 {
			out["headers"] = mapToAny(body.Headers)
		}
	}
	return out
}

// mapToAny widens map[string]string → map[string]any so YAML serializes it
// as a plain map.
func mapToAny(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// handleProvisionGlama fetches an MCP server spec from the Glama registry and
// writes it to config.yaml in one shot — no manual copy-pasting required.
//
// POST /api/v1/mcp/provision-glama
//
// Request:
//
//	{
//	  "glama_url": "https://glama.ai/mcp/servers/adamzaidi/icloud-mcp",
//	  "env": { "IMAP_USER": "you@icloud.com", "IMAP_PASSWORD": "xxxx-xxxx-xxxx-xxxx" }
//	}
//
// Response (env fields missing — returns what's required):
//
//	{ "ok": false, "spec": {...}, "env_required": ["IMAP_USER","IMAP_PASSWORD"] }
//
// Response (saved successfully):
//
//	{ "ok": true, "id": "icloud-mcp", "restart_needed": true, "message": "..." }
func (s *Server) handleProvisionGlama(c *fiber.Ctx) error {
	if s.cfgPath == "" {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "config file path unknown — cannot persist",
		})
	}

	var body struct {
		GlamaURL string            `json:"glama_url"`
		Env      map[string]string `json:"env"`
	}
	if err := c.BodyParser(&body); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if strings.TrimSpace(body.GlamaURL) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "glama_url is required")
	}

	// Fetch the spec from Glama (4s timeout inside FetchGlamaServer).
	spec, err := builder.FetchGlamaServer(body.GlamaURL)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": "could not fetch Glama server spec: " + err.Error(),
		})
	}

	// Collect required env vars that are missing from the request.
	var missing []string
	for _, ev := range spec.EnvSchema {
		if ev.Required {
			if v := strings.TrimSpace(body.Env[ev.Name]); v == "" {
				missing = append(missing, ev.Name)
			}
		}
	}
	if len(missing) > 0 {
		return c.JSON(fiber.Map{
			"ok":           false,
			"spec":         spec,
			"env_required": missing,
		})
	}

	// All required env vars present — write to config.yaml.
	raw, err := readRawConfig(s.cfgPath)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	mcpMap := getOrCreateMap(raw, "mcp")
	serversMap := getOrCreateMap(mcpMap, "servers")

	id := spec.ID
	if _, exists := serversMap[id]; exists {
		// Server already exists — update it in place.
		s.log.Info("glama provision: updating existing server", zap.String("server", id))
	}

	serverCfg := mcpServerBody{
		ID:        id,
		Transport: "stdio",
		Command:   spec.Command,
		Args:      spec.Args,
		Env:       body.Env,
	}
	serversMap[id] = mcpServerToYAML(serverCfg)

	if err := writeRawConfig(s.cfgPath, raw); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.log.Info("glama provision: server saved",
		zap.String("server", id),
		zap.String("glama_url", body.GlamaURL),
	)

	// Hot-connect immediately.
	connectErr := ""
	hotCfg := mcp.ServerConfig{
		Transport: "stdio",
		Command:   spec.Command,
		Args:      spec.Args,
		Env:       body.Env,
	}
	if err := s.mcpAddServer(c, id, hotCfg); err != nil {
		connectErr = err.Error()
	}

	resp := fiber.Map{
		"ok":             true,
		"id":             id,
		"spec":           spec,
		"restart_needed": false,
		"message":        fmt.Sprintf("%q connected with %d tools.", id, 0),
	}
	if connectErr != "" {
		resp["message"] = fmt.Sprintf("Saved, but connection failed: %s", connectErr)
		resp["connect_error"] = connectErr
	}
	return c.Status(fiber.StatusCreated).JSON(resp)
}

// handleMCPRegistrySearch proxies search queries to registry.modelcontextprotocol.io.
//
// The GUI cannot call the registry directly because the registry does not set
// CORS headers that would allow browser requests from a different origin. By
// routing through this backend endpoint the browser talks only to the Soulacy
// gateway (same origin), and the gateway makes the outbound call server-side.
// This also provides a natural place to add caching or rate-limiting later
// without touching the frontend.
//
// GET /api/v1/mcp/registry/search?q=...&limit=20&cursor=...
func (s *Server) handleMCPRegistrySearch(c *fiber.Ctx) error {
	query := c.Query("q", "")
	cursor := c.Query("cursor", "")
	limit := c.QueryInt("limit", 20)

	servers, nextCursor, err := builder.SearchMCPRegistry(query, cursor, limit)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": "registry search failed: " + err.Error(),
		})
	}
	return c.JSON(fiber.Map{
		"servers":    servers,
		"nextCursor": nextCursor,
	})
}

// handleProvisionMCPRegistry installs an MCP server from the official registry.
// Mirrors the two-phase flow used by handleProvisionGlama:
//
// Phase 1 — spec fetch + env-var check:
//  1. Fetch the server detail from registry.modelcontextprotocol.io via
//     builder.FetchMCPRegistryServer.
//  2. Inspect the spec's EnvSchema for required fields not present in the
//     request body's env map.
//  3. If any required env vars are missing, return
//     { "ok": false, "spec": {...}, "env_required": [...] } with HTTP 200.
//     The GUI renders a form for the missing vars and resubmits.
//
// Phase 2 — save + hot-connect:
//  4. Write the server config to config.yaml (stdio transport, command + args
//     derived from the registry package, env vars from the request).
//  5. Hot-connect via mcp.AddServer so the tools are available immediately
//     without restarting the gateway.
//
// POST /api/v1/mcp/provision-registry
//
// Request:  { "server_name": "io.modelcontextprotocol/filesystem", "env": {...} }
// Response: same shape as provision-glama (ok, spec, env_required, id, message)
func (s *Server) handleProvisionMCPRegistry(c *fiber.Ctx) error {
	if s.cfgPath == "" {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "config file path unknown — cannot persist",
		})
	}

	var body struct {
		ServerName string            `json:"server_name"`
		Env        map[string]string `json:"env"`
		Preview    bool              `json:"preview"`
	}
	if err := c.BodyParser(&body); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if strings.TrimSpace(body.ServerName) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "server_name is required")
	}

	spec, err := builder.FetchMCPRegistryServer(body.ServerName)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": "could not fetch registry server spec: " + err.Error(),
		})
	}

	// Collect required env vars that are missing.
	var missing []string
	for _, ev := range spec.EnvSchema {
		if ev.Required {
			if v := strings.TrimSpace(body.Env[ev.Name]); v == "" {
				missing = append(missing, ev.Name)
			}
		}
	}
	for _, ev := range spec.URLVariables {
		if ev.Required {
			if v := strings.TrimSpace(body.Env[ev.Name]); v == "" {
				missing = append(missing, ev.Name)
			}
		}
	}
	for _, ev := range spec.HeaderSchema {
		if ev.Required {
			if v := strings.TrimSpace(body.Env[ev.Name]); v == "" {
				missing = append(missing, ev.Name)
			}
		}
	}
	if body.Preview {
		return c.JSON(fiber.Map{
			"ok":           false,
			"preview":      true,
			"spec":         spec,
			"env_required": missing,
		})
	}
	if len(missing) > 0 {
		return c.JSON(fiber.Map{
			"ok":           false,
			"spec":         spec,
			"env_required": missing,
		})
	}

	// Write to config.yaml.
	raw, err := readRawConfig(s.cfgPath)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	mcpMap := getOrCreateMap(raw, "mcp")
	serversMap := getOrCreateMap(mcpMap, "servers")

	id := spec.ID
	serverBody, err := registrySpecToServerBody(id, spec, body.Env)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	serversMap[id] = mcpServerToYAML(serverBody)

	if err := writeRawConfig(s.cfgPath, raw); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.log.Info("mcp registry provision: server saved",
		zap.String("server", id),
		zap.String("registry_name", body.ServerName),
	)

	// Hot-connect immediately.
	connectErr := ""
	if err := s.mcpAddServer(c, id, mcp.ServerConfig{
		Transport: serverBody.Transport,
		Command:   serverBody.Command,
		Args:      serverBody.Args,
		Env:       serverBody.Env,
		URL:       serverBody.URL,
		Headers:   serverBody.Headers,
	}); err != nil {
		connectErr = err.Error()
	}

	resp := fiber.Map{
		"ok":             true,
		"id":             id,
		"spec":           spec,
		"restart_needed": false,
		"message":        fmt.Sprintf("%q connected.", id),
	}
	if connectErr != "" {
		resp["message"] = fmt.Sprintf("Saved, but connection failed: %s", connectErr)
		resp["connect_error"] = connectErr
	}
	return c.Status(fiber.StatusCreated).JSON(resp)
}

func registrySpecToServerBody(id string, spec *builder.GlamaProvisionSpec, values map[string]string) (mcpServerBody, error) {
	if spec == nil {
		return mcpServerBody{}, fmt.Errorf("registry spec is missing")
	}
	transport := strings.ToLower(strings.TrimSpace(spec.Transport))
	if transport == "" {
		transport = "stdio"
	}
	switch transport {
	case "stdio":
		if strings.TrimSpace(spec.Command) == "" {
			return mcpServerBody{}, fmt.Errorf("registry package did not provide a command")
		}
		return mcpServerBody{
			ID:        id,
			Transport: "stdio",
			Command:   spec.Command,
			Args:      spec.Args,
			Env:       values,
		}, nil
	case "http", "https":
		resolvedURL := spec.URL
		for _, ev := range spec.URLVariables {
			resolvedURL = strings.ReplaceAll(resolvedURL, "{"+ev.Name+"}", strings.TrimSpace(values[ev.Name]))
		}
		if strings.TrimSpace(resolvedURL) == "" {
			return mcpServerBody{}, fmt.Errorf("registry remote did not provide a URL")
		}
		headers := map[string]string{}
		for _, ev := range spec.HeaderSchema {
			if v := strings.TrimSpace(values[ev.Name]); v != "" {
				headers[ev.Name] = v
			}
		}
		return mcpServerBody{
			ID:        id,
			Transport: "http",
			URL:       resolvedURL,
			Headers:   headers,
		}, nil
	default:
		return mcpServerBody{}, fmt.Errorf("unsupported registry transport %q", spec.Transport)
	}
}

// validMCPID accepts the same characters the rest of the codebase uses for
// safe identifiers (filenames, table-name sanitisation, etc.).
func validMCPID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

// handleAgentActions returns recent events from the agent's action log.
//
// The action log is an append-only record of every event the engine emits for
// an agent: message.in, message.out, tool.call, tool.result, error, etc.
// This handler tails the log and optionally filters it.
//
// Query params:
//
//	limit (int, default 500)  — maximum number of events to return (most recent)
//	types (string, optional)  — comma-separated allowlist of event types.
//	                            When supplied, only events whose Type field
//	                            appears in the list are returned. Example:
//	                            "message.in,message.out,error" returns only
//	                            conversation turns and failures, omitting the
//	                            verbose tool.call / tool.result lines that
//	                            inflate the History view payload. When omitted,
//	                            all event types are returned.
func (s *Server) handleAgentActions(c *fiber.Ctx) error {
	id := c.Params("id")
	if s.actions == nil {
		return c.JSON(fiber.Map{"agent_id": id, "path": "", "events": []message.Event{}, "note": "action logging disabled"})
	}
	limit := c.QueryInt("limit", 500)

	// Optional event-type filter — callers like the History panel only need a
	// subset (message.in,message.out,error,tool.result). Filtering must happen
	// DURING the tail: otherwise verbose tool.log lines from a few recent runs
	// consume the whole window and older runs' boundary events fall off, so those
	// runs vanish from the History panel. When the backend supports it, ask it to
	// count only the allowed types toward the limit.
	allowed := map[string]bool{}
	if typesParam := c.Query("types", ""); typesParam != "" {
		for _, t := range strings.Split(typesParam, ",") {
			if t = strings.TrimSpace(t); t != "" {
				allowed[t] = true
			}
		}
	}

	actions := s.actionLog(c)
	var events []message.Event
	var err error
	if c.QueryBool("durable", false) {
		events, _, err = actions.QueryFiltered(id, limit, allowed)
	}
	if events == nil && err == nil {
		events, err = actions.TailFiltered(id, limit, allowed)
	}
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}

	// Fallback post-filter for backends without TailFiltered (keeps behaviour
	// correct even if the tail returned mixed types).
	if len(allowed) > 0 {
		filtered := events[:0]
		for _, ev := range events {
			if allowed[ev.Type] {
				filtered = append(filtered, ev)
			}
		}
		events = filtered
	}
	for i := range events {
		events[i].Payload = compactActionPayload(events[i].Payload, 64*1024)
	}

	return c.JSON(fiber.Map{
		"agent_id": id,
		"path":     actions.EventFilePath(id),
		"events":   events,
		"count":    len(events),
	})
}

func compactActionPayload(payload any, maxBytes int) any {
	if maxBytes <= 0 {
		maxBytes = 64 * 1024
	}
	b, err := json.Marshal(payload)
	if err != nil || len(b) <= maxBytes {
		return payload
	}
	return fiber.Map{
		"truncated": true,
		"bytes":     len(b),
		"preview":   compactPayloadPreview(payload, 1200),
	}
}

func compactPayloadPreview(payload any, max int) string {
	if max <= 0 {
		max = 1200
	}
	var s string
	switch v := payload.(type) {
	case string:
		s = v
	default:
		b, err := json.Marshal(payload)
		if err != nil {
			s = fmt.Sprint(payload)
		} else {
			s = string(b)
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}

// --- Memory ---

func (s *Server) handleListMemory(c *fiber.Ctx) error {
	agentID := c.Params("agent_id")
	query := c.Query("q", "")

	// The same workspace the delete handler below uses. Reading and erasing the
	// same records through two different scopes is how one of them ends up
	// wrong.
	workspaceID := s.agents(c).WorkspaceID()

	var entries interface{}
	var err error
	if query != "" {
		entries, err = s.engine.MemorySearchContext(c.UserContext(), workspaceID, agentID, query, 200)
	} else {
		entries, err = s.engine.MemoryListContext(c.UserContext(), workspaceID, agentID, 200)
	}
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.JSON(fiber.Map{"entries": entries})
}

func (s *Server) handleDeleteMemorySession(c *fiber.Ctx) error {
	sessionID := c.Params("session_id")
	if err := s.engine.MemoryPurgeSession(s.agents(c).WorkspaceID(), sessionID); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	// Erasing a conversation has to reach what was derived from it. Otherwise
	// deleting the evidence leaves the conclusion in place — and a pending
	// proposal could still be accepted into the agent's behaviour afterwards,
	// citing a conversation that no longer exists.
	invalidated := learning.InvalidationResult{}
	if store := s.learningStore(c); store != nil {
		got, err := store.InvalidateBySession(sessionID)
		if err != nil {
			s.log.Warn("learning: invalidate by session failed",
				zap.String("session", sessionID), zap.Error(err))
		}
		invalidated = got
	}
	s.recordAdminAudit(c, "memory.purge_session", "session", sessionID, "ok", map[string]any{
		"learning_deleted": invalidated.Deleted, "learning_disabled": invalidated.Disabled,
	})
	return c.JSON(fiber.Map{
		"message": "session memory purged", "session_id": sessionID,
		"learning_deleted": invalidated.Deleted, "learning_disabled": invalidated.Disabled,
	})
}

// --- Providers ---

// handleListProviders returns each configured provider with its API key redacted.
// Also includes which provider IDs are currently registered (live) so the GUI
// can show a "configured but needs restart" state for newly-added providers.
func (s *Server) handleListProviders(c *fiber.Ctx) error {
	registered := map[string]bool{}
	if s.llmRouter != nil {
		for _, id := range s.llmRouter.ProviderIDs() {
			registered[id] = true
		}
	}
	providers := make(fiber.Map, len(s.config().LLM.Providers))
	for name, pc := range s.config().LLM.Providers {
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
			"prompt_caching":             pc.PromptCaching,
			"region":                     pc.Region,
			"retention":                  pc.Retention,
			"local":                      pc.Local,
			"allowed_data_classes":       pc.AllowedDataClasses,
			"cache_allowed_data_classes": pc.CacheAllowedDataClasses,
			"max_tokens_per_minute":      pc.MaxTokensPerMinute,
			"thinking_budget":            pc.ThinkingBudget,
			"safety_level":               pc.SafetyLevel,
			"extended_thinking":          pc.ExtendedThinking,
			"organization":               pc.Organization,
			"parallel_tool_calls":        pc.ParallelToolCalls,
			"registered":                 registered[name],
		}
	}
	// Known provider IDs the GUI should always offer (even when not yet
	// configured), so users can pick them from a dropdown to add credentials.
	known := []string{"ollama", "openai", "anthropic", "google", "groq", "mistral", "openrouter", "deepseek", "together"}
	return c.JSON(fiber.Map{
		"providers":        providers,
		"default_provider": s.config().LLM.DefaultProvider,
		"known":            known,
		"registered":       s.llmRouter.ProviderIDs(),
	})
}

// handleListModels asks the live provider for its available models.
// For Ollama this performs the equivalent of `ollama list` (GET /api/tags).
//
// E4 — Failure handling: when the provider call fails, the raw error is passed
// through llm.ClassifyProviderError so the GUI Providers page can show a
// friendly reason + concrete fix instead of a bare `openai: /models returned
// 401: {...}` string. The classifier output is attached as `diagnosis` on the
// response body; the top-level `error` is set to the diagnosis reason so older
// callers that only read `error` still see the friendly text.
func (s *Server) handleListModels(c *fiber.Ctx) error {
	id := c.Params("id")
	if s.llmRouter == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "LLM router unavailable")
	}
	p := s.llmRouter.Provider(id)
	if p == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": fmt.Sprintf("provider %q is not registered (configured: %v)", id, s.llmRouter.ProviderIDs()),
		})
	}

	ctx, cancel := context.WithTimeout(c.Context(), 15*time.Second)
	defer cancel()

	models, err := p.Models(ctx)
	if err != nil {
		return s.providerErrJSON(c, fiber.StatusBadGateway, id, err)
	}

	selected := ""
	if pc, ok := s.config().LLM.Providers[id]; ok {
		selected = pc.Model
	}
	return c.JSON(fiber.Map{"models": models, "selected": selected})
}

// providerErrJSON wraps a provider call failure with llm.ClassifyProviderError
// so the response is the same friendly shape everywhere in the gateway. Use
// this instead of s.errJSON whenever a provider driver returned the error.
//
// Response body:
//
//	{
//	  "error":     "<friendly reason so old GUI still shows something useful>",
//	  "detail":    "<raw provider error>",
//	  "diagnosis": {"ok":false, "category":"bad_key", "reason":"…", "fix":"…", "detail":"…"}
//	}
func (s *Server) providerErrJSON(c *fiber.Ctx, status int, providerID string, err error) error {
	d := llm.ClassifyProviderError(providerID, err)
	msg := d.Reason
	if msg == "" {
		msg = err.Error()
	}
	return c.Status(status).JSON(fiber.Map{
		"error":     msg,
		"detail":    d.Detail,
		"diagnosis": d,
	})
}

// agentValidationProviderProbeTimeout bounds provider model discovery during
// validation. Keep this request-path policy in one place so personal and
// workspace-scoped provider inventories cannot drift.
const agentValidationProviderProbeTimeout = 3 * time.Second

func (s *Server) agentValidationOptions(ctx context.Context) agentvalidate.Options {
	opts := agentvalidate.Options{Config: s.config(), ProviderModels: map[string][]string{}}
	if s.llmRouter == nil {
		return opts
	}
	if ctx == nil {
		ctx = context.Background()
	}
	opts.RegisteredProviders = s.llmRouter.ProviderIDs()
	for _, id := range opts.RegisteredProviders {
		p := s.llmRouter.Provider(id)
		if p == nil {
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, agentValidationProviderProbeTimeout)
		models, err := p.Models(probeCtx)
		cancel()
		if err == nil && len(models) > 0 {
			opts.ProviderModels[id] = models
		}
	}
	return opts
}

// agentValidationOptionsFor validates against the same effective provider
// inventory the request can execute with. Team/Scale workspaces may own a
// provider and encrypted credential without registering either globally.
func (s *Server) agentValidationOptionsFor(c *fiber.Ctx) agentvalidate.Options {
	if c == nil {
		return s.agentValidationOptions(context.Background())
	}
	settings := s.workspaceSettingsFor(c)
	id, ok := identityForWorkspaceSettings(c)
	if !ok || strings.TrimSpace(id.WorkspaceID()) == "" {
		return s.agentValidationOptions(c.UserContext())
	}
	return s.agentValidationOptionsForWorkspace(c.UserContext(), id.WorkspaceID(), settings)
}

func (s *Server) agentValidationOptionsForWorkspace(ctx context.Context, workspaceID string, settings workspacesettings.Settings) agentvalidate.Options {
	if ctx == nil {
		ctx = context.Background()
	}
	opts := agentvalidate.Options{Config: s.effectiveWorkspaceConfig(settings), ProviderModels: map[string][]string{}}
	registered := map[string]struct{}{}
	if s.llmRouter != nil {
		for _, id := range s.llmRouter.ProviderIDs() {
			registered[id] = struct{}{}
			provider := s.llmRouter.Provider(id)
			if _, overridden := settings.LLM.Providers[id]; overridden {
				provider, _ = s.workspaceProvider(ctx, workspaceID, id, settings.LLM.Providers[id])
			}
			s.probeValidationProvider(ctx, id, provider, opts.ProviderModels)
		}
	}
	for id, configured := range settings.LLM.Providers {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "" {
			continue
		}
		provider, err := s.workspaceProvider(ctx, workspaceID, id, configured)
		if err != nil || provider == nil {
			continue
		}
		registered[id] = struct{}{}
		s.probeValidationProvider(ctx, id, provider, opts.ProviderModels)
	}
	for id := range registered {
		opts.RegisteredProviders = append(opts.RegisteredProviders, id)
	}
	sort.Strings(opts.RegisteredProviders)
	return opts
}

func (s *Server) probeValidationProvider(ctx context.Context, id string, provider sdkllm.Provider, models map[string][]string) {
	if provider == nil || len(models[id]) > 0 {
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, agentValidationProviderProbeTimeout)
	available, err := provider.Models(probeCtx)
	cancel()
	if err == nil && len(available) > 0 {
		models[id] = available
	}
}

// handleSetProviderModel persists the chosen default model for a provider into
// config.yaml (llm.providers.<id>.model) and updates the live config.
func (s *Server) handleSetProviderModel(c *fiber.Ctx) error {
	id := c.Params("id")
	if s.cfgPath == "" {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "config file path unknown — cannot persist")
	}
	var req struct {
		Model string `json:"model"`
	}
	if err := c.BodyParser(&req); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if req.Model == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "model is required")
	}

	raw, err := readRawConfig(s.cfgPath)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	llmMap := getOrCreateMap(raw, "llm")
	provsMap := getOrCreateMap(llmMap, "providers")
	provMap := getOrCreateMap(provsMap, id)
	provMap["model"] = req.Model

	if err := writeRawConfig(s.cfgPath, raw); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}

	// Mirror into live config.
	pc := s.providerConfig(id)
	pc.Model = req.Model
	s.setProviderConfig(id, pc)

	// Re-register the provider in s.llmRouter so it updates without a gateway restart
	if s.llmRouter != nil {
		m := providerCfgMap(pc)
		m["id"] = id
		p, ok, perr := registry.NewProvider(id, m)
		if !ok {
			if pc.BaseURL != "" {
				p, _, perr = registry.NewProvider("openai", m)
			}
		}
		if perr == nil && p != nil {
			s.llmRouter.Register(p)
			s.log.Info("provider model dynamically re-registered in llm router", zap.String("provider", id), zap.String("model", req.Model))
		} else {
			s.log.Warn("failed to dynamically re-register provider in llm router on model update", zap.String("provider", id), zap.Error(perr))
		}
	}

	s.log.Info("provider model updated via API", zap.String("provider", id), zap.String("model", req.Model))
	return c.JSON(fiber.Map{
		"ok":      true,
		"model":   req.Model,
		"message": "Model saved.",
	})
}

func providerCfgMap(p config.ProviderConfig) map[string]any {
	m := map[string]any{}
	if p.BaseURL != "" {
		m["base_url"] = p.BaseURL
	}
	if p.APIKey != "" {
		m["api_key"] = p.APIKey
	}
	if p.Model != "" {
		m["model"] = p.Model
	}
	if p.KeepAlive != "" {
		m["keep_alive"] = p.KeepAlive
	}
	if p.Options != nil {
		m["options"] = p.Options
	}
	if p.PromptCaching {
		m["prompt_caching"] = true
	}
	if p.ExtendedThinking {
		m["extended_thinking"] = true
	}
	if p.ThinkingBudget != 0 {
		m["thinking_budget"] = p.ThinkingBudget
	}
	if p.SafetyLevel != "" {
		m["safety_level"] = p.SafetyLevel
	}
	if p.Organization != "" {
		m["organization"] = p.Organization
	}
	if p.ParallelToolCalls != nil {
		m["parallel_tool_calls"] = p.ParallelToolCalls
	}
	return m
}

// handleSetProviderCredentials persists base_url / api_key for a provider into
// config.yaml. The new provider takes effect after a gateway restart — the
// response surfaces that hint to the GUI.
func (s *Server) handleSetProviderCredentials(c *fiber.Ctx) error {
	id := c.Params("id")
	if s.cfgPath == "" {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "config file path unknown — cannot persist")
	}
	var req struct {
		BaseURL                 string         `json:"base_url"`
		APIKey                  string         `json:"api_key"`
		Model                   string         `json:"model"`
		KeepAlive               string         `json:"keep_alive"`
		Options                 map[string]any `json:"options"`
		PromptCaching           *bool          `json:"prompt_caching"`      // pointer: omitted ≠ false
		ThinkingBudget          *int           `json:"thinking_budget"`     // Google/Anthropic: 0=off, -1=auto, N=tokens
		SafetyLevel             *string        `json:"safety_level"`        // Google: ""|"default"|"off"|"strict"
		ExtendedThinking        *bool          `json:"extended_thinking"`   // Anthropic: Claude 3.7+ thinking
		Organization            *string        `json:"organization"`        // OpenAI: Org ID header
		ParallelToolCalls       *bool          `json:"parallel_tool_calls"` // OpenAI: false=serialize tool calls
		Region                  *string        `json:"region"`
		Retention               *string        `json:"retention"`
		Local                   *bool          `json:"local"`
		AllowedDataClasses      *[]string      `json:"allowed_data_classes"`
		CacheAllowedDataClasses *[]string      `json:"cache_allowed_data_classes"`
		MaxTokensPerMinute      *int           `json:"max_tokens_per_minute"`
	}
	if err := c.BodyParser(&req); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if id == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "provider id is required")
	}

	raw, err := readRawConfig(s.cfgPath)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	llmMap := getOrCreateMap(raw, "llm")
	provsMap := getOrCreateMap(llmMap, "providers")
	provMap := getOrCreateMap(provsMap, id)
	realAPIKey := strings.TrimSpace(req.APIKey)
	hasNewAPIKey := realAPIKey != "" && realAPIKey != "***"
	keySavedToVault := false
	if hasNewAPIKey && s.credVault != nil {
		name := "llm.providers." + id + ".api_key"
		if err := secrets.New(s.credVault).Set(c.Context(), name, realAPIKey); err != nil {
			return s.errJSON(c, fiber.StatusInternalServerError, err)
		}
		keySavedToVault = true
	}
	// Only update fields the caller actually sent (so a Save Model only doesn't
	// clobber the api_key — *** is not a real value).
	if req.BaseURL != "" {
		provMap["base_url"] = req.BaseURL
	}
	if hasNewAPIKey {
		if keySavedToVault {
			delete(provMap, "api_key")
		} else {
			provMap["api_key"] = realAPIKey
		}
	}
	if req.Model != "" {
		provMap["model"] = req.Model
	}
	if id == "ollama" {
		provMap["keep_alive"] = req.KeepAlive
	}
	if req.PromptCaching != nil {
		provMap["prompt_caching"] = *req.PromptCaching
	}
	if req.Region != nil {
		provMap["region"] = strings.TrimSpace(*req.Region)
	}
	if req.Retention != nil {
		provMap["retention"] = strings.TrimSpace(*req.Retention)
	}
	if req.Local != nil {
		provMap["local"] = *req.Local
	}
	if req.AllowedDataClasses != nil {
		provMap["allowed_data_classes"] = *req.AllowedDataClasses
	}
	if req.CacheAllowedDataClasses != nil {
		provMap["cache_allowed_data_classes"] = *req.CacheAllowedDataClasses
	}
	if req.MaxTokensPerMinute != nil {
		provMap["max_tokens_per_minute"] = *req.MaxTokensPerMinute
	}
	if req.ThinkingBudget != nil {
		provMap["thinking_budget"] = *req.ThinkingBudget
	}
	if req.SafetyLevel != nil {
		provMap["safety_level"] = *req.SafetyLevel
	}
	if req.ExtendedThinking != nil {
		provMap["extended_thinking"] = *req.ExtendedThinking
	}
	if req.Organization != nil {
		provMap["organization"] = *req.Organization
	}
	if req.ParallelToolCalls != nil {
		provMap["parallel_tool_calls"] = *req.ParallelToolCalls
	}
	if req.Options != nil {
		if len(req.Options) == 0 {
			delete(provMap, "options")
		} else {
			provMap["options"] = req.Options
		}
	}

	if err := writeRawConfig(s.cfgPath, raw); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}

	// Mirror to live config.
	pc := s.providerConfig(id)
	if req.BaseURL != "" {
		pc.BaseURL = req.BaseURL
	}
	if hasNewAPIKey {
		pc.APIKey = realAPIKey
	}
	if req.Model != "" {
		pc.Model = req.Model
	}
	if id == "ollama" {
		pc.KeepAlive = req.KeepAlive
	}
	if req.PromptCaching != nil {
		pc.PromptCaching = *req.PromptCaching
	}
	if req.Region != nil {
		pc.Region = strings.TrimSpace(*req.Region)
	}
	if req.Retention != nil {
		pc.Retention = strings.TrimSpace(*req.Retention)
	}
	if req.Local != nil {
		pc.Local = *req.Local
	}
	if req.AllowedDataClasses != nil {
		pc.AllowedDataClasses = append([]string(nil), (*req.AllowedDataClasses)...)
	}
	if req.CacheAllowedDataClasses != nil {
		pc.CacheAllowedDataClasses = append([]string(nil), (*req.CacheAllowedDataClasses)...)
	}
	if req.MaxTokensPerMinute != nil {
		pc.MaxTokensPerMinute = *req.MaxTokensPerMinute
	}
	if req.ThinkingBudget != nil {
		pc.ThinkingBudget = *req.ThinkingBudget
	}
	if req.SafetyLevel != nil {
		pc.SafetyLevel = *req.SafetyLevel
	}
	if req.ExtendedThinking != nil {
		pc.ExtendedThinking = *req.ExtendedThinking
	}
	if req.Organization != nil {
		pc.Organization = *req.Organization
	}
	if req.ParallelToolCalls != nil {
		pc.ParallelToolCalls = req.ParallelToolCalls
	}
	if req.Options != nil {
		pc.Options = req.Options
	}
	s.setProviderConfig(id, pc)

	if s.llmRouter != nil {
		m := providerCfgMap(pc)
		m["id"] = id
		p, ok, perr := registry.NewProvider(id, m)
		if !ok {
			if pc.BaseURL != "" {
				p, _, perr = registry.NewProvider("openai", m)
			}
		}
		if perr == nil && p != nil {
			s.llmRouter.Register(p)
			s.log.Info("provider dynamically registered in llm router", zap.String("provider", id))
		} else {
			s.log.Warn("failed to dynamically register provider in llm router", zap.String("provider", id), zap.Error(perr))
		}
	}

	if id == "ollama" && hasNewAPIKey && s.engine != nil {
		s.engine.SetOllamaAPIKey(realAPIKey)
	}

	s.log.Info("provider credentials updated", zap.String("provider", id))
	s.recordAdminAudit(c, "provider.credentials.update", "provider", id, "ok", map[string]any{"credential_changed": hasNewAPIKey})
	return c.JSON(fiber.Map{
		"ok":      true,
		"message": "Saved.",
	})
}

// handleDeleteProvider removes a provider from config.yaml
func (s *Server) handleDeleteProvider(c *fiber.Ctx) error {
	id := c.Params("id")
	if id == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "provider id is required")
	}
	if s.cfgPath == "" {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "config file path unknown — cannot persist")
	}

	raw, err := readRawConfig(s.cfgPath)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}

	llmMap := getOrCreateMap(raw, "llm")
	provsMap := getOrCreateMap(llmMap, "providers")

	// Check if it exists
	if _, ok := provsMap[id]; !ok {
		return s.errMsg(c, fiber.StatusNotFound, "provider not found")
	}

	delete(provsMap, id)

	if err := writeRawConfig(s.cfgPath, raw); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}

	// Mirror to live config
	if s.config().LLM.Providers != nil {
		delete(s.config().LLM.Providers, id)
	}
	// And to the live ROUTER, which is the half that was missing. Removing it
	// from the config only stopped the settings page listing it; the
	// constructed client stayed callable by every agent until the process
	// restarted. See llmhot.go.
	s.applyLLMLive(s.config().LLM)
	s.recordAdminAudit(c, "provider.delete", "provider", id, "ok", nil)

	return c.JSON(fiber.Map{"message": "Provider deleted. No restart needed."})
}

// --- Skills ---

// handleListSkills returns the catalog of all loaded Agent Skills.
func (s *Server) handleListSkills(c *fiber.Ctx) error {
	if s.skillCatalog(c) == nil {
		return c.JSON(fiber.Map{"skills": []struct{}{}, "count": 0})
	}
	all := s.skillCatalog(c).All()

	type skillSummary struct {
		Name          string            `json:"name"`
		Description   string            `json:"description"`
		License       string            `json:"license,omitempty"`
		Compatibility string            `json:"compatibility,omitempty"`
		Metadata      map[string]string `json:"metadata,omitempty"`
		Dir           string            `json:"dir"`
		Resources     []string          `json:"resources"`
	}
	out := make([]skillSummary, len(all))
	for i, sk := range all {
		out[i] = skillSummary{
			Name:          sk.Name,
			Description:   sk.Description,
			License:       sk.License,
			Compatibility: sk.Compatibility,
			Metadata:      sk.Metadata,
			Dir:           sk.Dir,
			Resources:     sk.ResourceFiles(),
		}
	}
	return c.JSON(fiber.Map{"skills": out, "count": len(out)})
}

// handleGetSkill returns the full content (instructions) of a single skill.
func (s *Server) handleGetSkill(c *fiber.Ctx) error {
	if s.skillCatalog(c) == nil {
		return s.errMsg(c, fiber.StatusNotFound, "skills not enabled")
	}
	name := c.Params("name")
	sk := s.skillCatalog(c).Get(name)
	if sk == nil {
		return s.errMsg(c, fiber.StatusNotFound, "skill not found")
	}
	return c.JSON(fiber.Map{
		"name":          sk.Name,
		"description":   sk.Description,
		"license":       sk.License,
		"compatibility": sk.Compatibility,
		"metadata":      sk.Metadata,
		"allowed_tools": sk.AllowedTools,
		"body":          sk.Body,
		"dir":           sk.Dir,
		"resources":     sk.ResourceFiles(),
	})
}

// handleProvisionAgenticSkill fetches a skill from agenticskills.io and
// installs it into ~/.soulacy/skills/ — no restart required.
//
// Request:  POST /api/skills/provision-agenticskills
//
//	Body:   { "url": "https://agenticskills.io/skills/frontend-design" }
//	        or { "slug": "frontend-design" }
//
// Response (success): { "ok": true, "slug": "...", "source": "org/repo@sha", "message": "..." }
// Response (error):   { "ok": false, "error": "..." }
//
// Flow:
//  1. Derive slug from the URL or the slug field.
//  2. Fetch the agenticskills.io skill page HTML.
//  3. Extract the "View on GitHub" blob URL via regex.
//  4. Download the raw SKILL.md from raw.githubusercontent.com.
//  5. Write to ~/.soulacy/skills/<slug>/SKILL.md.
//  6. Hot-rescan via the Scan() method on the loader (if available).
var githubBlobRe = regexp.MustCompile(`href="(https://github\.com/[^/]+/[^/]+/blob/[^/"]+/[^"]+/SKILL\.md)"`)
var githubTreeSkillRe = regexp.MustCompile(`href="(https://github\.com/[^/]+/[^/]+/tree/[^/"]+/[^"]+)"`)

func (s *Server) writableSkillsDir(c *fiber.Ctx) (string, error) {
	if s.skillStores != nil {
		return s.skillStores.EnsureDir(s.requestWorkspace(c))
	}
	wsPaths, err := config.ResolveWorkspace()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(wsPaths.Skills, 0o755); err != nil {
		return "", err
	}
	return wsPaths.Skills, nil
}

func (s *Server) rescanRequestSkills(c *fiber.Ctx) []error {
	if s.skillStores != nil {
		return s.skillStores.Rescan(s.requestWorkspace(c))
	}
	if scanner, ok := s.skillCatalog(c).(interface{ Scan() []error }); ok {
		return scanner.Scan()
	}
	return nil
}

// agenticSkillRawURLs returns the immutable source advertised by the page
// first, followed by the current branch source. AgenticSkills pages can retain
// a blob link after an upstream force-push; falling back to the visible tree
// link keeps the install button useful without guessing a repository or path.
func agenticSkillRawURLs(pageHTML []byte) []string {
	urls := make([]string, 0, 2)
	seen := map[string]bool{}
	for _, match := range githubBlobRe.FindAllSubmatch(pageHTML, -1) {
		raw := strings.Replace(string(match[1]), "https://github.com/", "https://raw.githubusercontent.com/", 1)
		raw = strings.Replace(raw, "/blob/", "/", 1)
		if !seen[raw] {
			seen[raw] = true
			urls = append(urls, raw)
		}
	}
	for _, match := range githubTreeSkillRe.FindAllSubmatch(pageHTML, -1) {
		raw := strings.Replace(string(match[1]), "https://github.com/", "https://raw.githubusercontent.com/", 1)
		raw = strings.Replace(raw, "/tree/", "/", 1)
		raw = strings.TrimSuffix(raw, "/") + "/SKILL.md"
		if !seen[raw] {
			seen[raw] = true
			urls = append(urls, raw)
		}
	}
	return urls
}

// handleRescanSkills re-scans the skill directories so freshly installed
// skills (e.g. `sy skill install <slug>`, Story E18) hot-load without a
// gateway restart.
//
// POST /api/v1/skills/rescan → { "ok": true, "count": <loaded skills> }
func (s *Server) handleRescanSkills(c *fiber.Ctx) error {
	if s.skillCatalog(c) == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"ok": false, "error": "skill loader unavailable",
		})
	}
	scanner, ok := s.skillCatalog(c).(interface{ Scan() []error })
	if !ok {
		return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{
			"ok": false, "error": "skill loader does not support rescanning",
		})
	}
	if errs := scanner.Scan(); len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return c.JSON(fiber.Map{"ok": true, "count": len(s.skillCatalog(c).All()), "warnings": msgs})
	}
	return c.JSON(fiber.Map{"ok": true, "count": len(s.skillCatalog(c).All())})
}

func (s *Server) handleInstallRegistrySkill(c *fiber.Ctx) error {
	if s.skillCatalog(c) == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"ok": false, "error": "skill loader unavailable",
		})
	}
	var body struct {
		Slug            string `json:"slug"`
		AllowUnverified bool   `json:"allow_unverified"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"ok": false, "error": "invalid JSON body"})
	}
	slug := strings.TrimSpace(body.Slug)
	if slug == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"ok": false, "error": "slug is required"})
	}

	ctx, cancel := context.WithTimeout(c.UserContext(), 90*time.Second)
	defer cancel()
	eng, warnings := pkgregistry.FromConfig(s.configuredOrDefaultRegistries(), s.log)
	pkg, err := eng.Resolve(ctx, slug)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"ok":       false,
			"error":    err.Error(),
			"warnings": registryWarningStrings(warnings),
		})
	}

	verified := pkg.Signature != "" && eng.VerifiesSignatures(pkg.Provider)
	if !verified && !body.AllowUnverified {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{
			"ok":       false,
			"error":    "authenticity cannot be verified for this package",
			"code":     "unverified_package",
			"slug":     pkg.Slug,
			"provider": pkg.Provider,
			"remedy":   "Enable \"Allow unverified install\" only if you trust the source, or configure signing_key for the registry.",
		})
	}

	skillsDir, err := s.writableSkillsDir(c)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"ok": false, "error": "cannot resolve workspace: " + err.Error()})
	}
	staging := filepath.Join(skillsDir, fmt.Sprintf(".staging-%d", time.Now().UnixNano()))
	if err := eng.Fetch(ctx, pkg, staging); err != nil {
		_ = os.RemoveAll(staging)
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"ok": false, "error": "fetch failed: " + err.Error()})
	}
	cleanup := func() { _ = os.RemoveAll(staging) }
	if _, err := os.Stat(filepath.Join(staging, "SKILL.md")); err != nil {
		cleanup()
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{
			"ok": false, "error": fmt.Sprintf("package %q has no SKILL.md at its root", pkg.Slug),
		})
	}

	var manifest *plugin.Manifest
	if m, merr := plugininstall.ReadManifest(staging); merr == nil {
		manifest = &m
	}
	pipeline := introspect.Pipeline{DryRun: &introspect.DryRunConfig{Timeout: 5 * time.Second}}
	report := pipeline.Run(ctx, staging, manifest)
	if report.Verdict == introspect.VerdictDanger {
		cleanup()
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{
			"ok":              false,
			"error":           "skill failed safety introspection",
			"code":            "danger_verdict",
			"security_report": report,
		})
	}

	name := registrySkillDirName(pkg.Slug)
	dest := filepath.Join(skillsDir, name)
	if _, err := os.Stat(dest); err == nil {
		cleanup()
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{
			"ok": false, "error": fmt.Sprintf("skill %q is already installed", name), "code": "already_installed",
		})
	}
	if err := os.Rename(staging, dest); err != nil {
		cleanup()
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"ok": false, "error": "activate skill: " + err.Error()})
	}

	rescanWarnings := []string{}
	for _, e := range s.rescanRequestSkills(c) {
		if e != nil {
			rescanWarnings = append(rescanWarnings, e.Error())
		}
	}
	s.log.Info("skill installed from registry via API",
		zap.String("slug", pkg.Slug), zap.String("provider", pkg.Provider), zap.String("dest", dest), zap.Bool("verified", verified))
	return c.JSON(fiber.Map{
		"ok":              true,
		"slug":            pkg.Slug,
		"name":            name,
		"version":         pkg.Version,
		"provider":        pkg.Provider,
		"verified":        verified,
		"path":            dest,
		"security_report": report,
		"warnings":        append(registryWarningStrings(warnings), rescanWarnings...),
		"message":         fmt.Sprintf("Skill %q installed and hot-loaded.", name),
	})
}

func registryWarningStrings(warnings []error) []string {
	out := make([]string, 0, len(warnings))
	for _, w := range warnings {
		if w != nil {
			out = append(out, w.Error())
		}
	}
	return out
}

func registrySkillDirName(slug string) string {
	name := path.Base(strings.TrimSuffix(strings.TrimSuffix(slug, "/"), ".git"))
	name = strings.TrimSuffix(name, ".git")
	if name == "" || name == "." || name == "/" {
		return "skill"
	}
	return name
}

func (s *Server) handleProvisionAgenticSkill(c *fiber.Ctx) error {
	if s.skillCatalog(c) == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"ok": false, "error": "skill loader not initialised",
		})
	}

	var body struct {
		URL  string `json:"url"`
		Slug string `json:"slug"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"ok": false, "error": "invalid JSON body"})
	}

	// Derive slug.
	slug := strings.TrimSpace(body.Slug)
	if slug == "" && body.URL != "" {
		u, err := url.Parse(strings.TrimSpace(body.URL))
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"ok": false, "error": "invalid URL"})
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 2 || parts[0] != "skills" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"ok": false, "error": "URL must be https://agenticskills.io/skills/<slug>",
			})
		}
		slug = parts[1]
	}
	if slug == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"ok": false, "error": "url or slug required"})
	}

	ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
	defer cancel()

	client := &http.Client{Timeout: 30 * time.Second}

	// 1. Fetch agenticskills.io page.
	pageURL := fmt.Sprintf("https://agenticskills.io/skills/%s", url.PathEscape(slug))
	pageReq, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"ok": false, "error": err.Error()})
	}
	pageReq.Header.Set("User-Agent", "Soulacy/1.0 (+https://github.com/soulacy/soulacy)")

	pageResp, err := client.Do(pageReq)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"ok": false, "error": fmt.Sprintf("agenticskills.io unreachable: %v", err),
		})
	}
	defer pageResp.Body.Close()

	if pageResp.StatusCode == http.StatusNotFound {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"ok": false, "error": fmt.Sprintf("skill %q not found on agenticskills.io", slug),
		})
	}
	if pageResp.StatusCode >= 300 {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"ok": false, "error": fmt.Sprintf("agenticskills.io: HTTP %d", pageResp.StatusCode),
		})
	}

	pageHTML, err := io.ReadAll(io.LimitReader(pageResp.Body, 2<<20)) // 2 MB cap
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"ok": false, "error": "failed to read page"})
	}

	// 2. Extract downloadable GitHub sources from the page. Prefer its pinned
	// blob, but try the advertised branch tree when that revision has gone
	// stale (which currently happens for some AgenticSkills entries).
	rawURLs := agenticSkillRawURLs(pageHTML)
	if len(rawURLs) == 0 {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{
			"ok":    false,
			"error": "could not find SKILL.md GitHub source on agenticskills.io — the skill may not have a public GitHub source",
		})
	}
	var skillMD []byte
	var rawURL string
	var failures []string
	for _, candidate := range rawURLs {
		rawReq, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, candidate, nil)
		if reqErr != nil {
			failures = append(failures, reqErr.Error())
			continue
		}
		rawResp, fetchErr := client.Do(rawReq)
		if fetchErr != nil {
			failures = append(failures, fetchErr.Error())
			continue
		}
		if rawResp.StatusCode >= 300 {
			failures = append(failures, fmt.Sprintf("HTTP %d", rawResp.StatusCode))
			_ = rawResp.Body.Close()
			continue
		}
		skillMD, err = io.ReadAll(io.LimitReader(rawResp.Body, 1<<20)) // 1 MB cap
		_ = rawResp.Body.Close()
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		rawURL = candidate
		break
	}
	if len(skillMD) == 0 {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"ok": false, "error": "GitHub did not provide SKILL.md: " + strings.Join(failures, "; "),
		})
	}

	// 4. Write to <workspace>/skills/<slug>/SKILL.md.
	skillsDir, err := s.writableSkillsDir(c)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"ok": false, "error": "cannot resolve workspace: " + err.Error(),
		})
	}
	skillDir := filepath.Join(skillsDir, slug)
	if _, err := os.Stat(skillDir); err == nil {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{
			"ok": false, "error": fmt.Sprintf("skill %q is already installed", slug), "code": "already_installed",
		})
	}
	staging := filepath.Join(skillsDir, fmt.Sprintf(".staging-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"ok": false, "error": fmt.Sprintf("create skill dir: %v", err),
		})
	}
	defer os.RemoveAll(staging)
	if err := os.WriteFile(filepath.Join(staging, "SKILL.md"), skillMD, 0o644); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"ok": false, "error": fmt.Sprintf("write SKILL.md: %v", err),
		})
	}
	if err := os.Rename(staging, skillDir); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"ok": false, "error": fmt.Sprintf("activate skill: %v", err),
		})
	}

	// 5. Hot-rescan (skills.Loader implements Scan(); use type assertion to
	//    avoid adding the method to the runtime.SkillLoader interface).
	_ = s.rescanRequestSkills(c)

	// Derive a friendly source label from the raw GitHub URL.
	// e.g. "anthropics/skills@5be498e".
	source := strings.TrimPrefix(rawURL, "https://raw.githubusercontent.com/")
	if parts := strings.Split(source, "/"); len(parts) >= 3 {
		ref := parts[2]
		if len(ref) > 8 {
			ref = ref[:8]
		}
		source = parts[0] + "/" + parts[1] + "@" + ref
	}

	return c.JSON(fiber.Map{
		"ok":      true,
		"slug":    slug,
		"source":  source,
		"message": fmt.Sprintf("Skill %q installed from %s.", slug, source),
	})
}

// --- Templates ---
//
// Starter agent definitions a user can clone with one click. Inspired by
// Langflow's "New Project from Template" flow. Default templates ship
// embedded in the binary; users can drop additional *.yaml files under
// ~/.soulacy/templates to extend or override.

// templatesCatalog is constructed per-request rather than cached on Server
// so a user can drop new files into ~/.soulacy/templates without a
// restart. The List() call is cheap (parses ~4 small YAML files).
func (s *Server) templatesCatalog() *templates.Catalog {
	return templates.New(templates.DefaultUserDir())
}

func (s *Server) handleListTemplates(c *fiber.Ctx) error {
	entries, err := s.templatesCatalog().List()
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.applyTemplateRuntimeDefaults(c, entries)
	return c.JSON(fiber.Map{"templates": entries, "count": len(entries)})
}

func (s *Server) applyTemplateRuntimeDefaults(c *fiber.Ctx, entries []templates.Entry) {
	for i := range entries {
		if entries[i].Definition == nil {
			continue
		}
		s.applyTemplateDefinitionDefaults(c, entries[i].Definition)
		modelDetail := strings.TrimSpace(entries[i].Definition.LLM.Provider)
		if model := strings.TrimSpace(entries[i].Definition.LLM.Model); model != "" {
			if modelDetail != "" {
				modelDetail += " / "
			}
			modelDetail += model
		}
		for j := range entries[i].Setup {
			if entries[i].Setup[j].Key != "model" {
				continue
			}
			entries[i].Setup[j].Status = "ready"
			entries[i].Setup[j].Detail = modelDetail
		}
	}
}

func (s *Server) applyTemplateDefinitionDefaults(c *fiber.Ctx, def *agent.Definition) {
	if def == nil {
		return
	}
	provider, model := s.defaultAgentLLM(c)
	if provider != "" {
		def.LLM.Provider = provider
	}
	if model != "" {
		def.LLM.Model = model
	}
}

// handleInstantiateTemplate clones a template into a fresh agent via the
// loader. Body (all optional):
//
//	{
//	  "id": "my-bot",                         // desired agent ID; auto-derived if omitted
//	  "cron": "0 7 * * *",                   // optional schedule override
//	  "output": {"channel":"telegram","to":"@my_channel","template":"{reply}"}
//	}
//
// Returns the created Definition. The agent is created enabled (matches the
// normal create-agent flow) so it shows up in the GUI immediately. The
// scheduler is also notified so cron-triggered templates start ticking.
func (s *Server) handleInstantiateTemplate(c *fiber.Ctx) error {
	name := c.Params("name")
	if name == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "template name is required")
	}
	var req struct {
		ID     string `json:"id"`
		Cron   string `json:"cron"`
		Output struct {
			Channel  string `json:"channel"`
			To       string `json:"to"`
			BotName  string `json:"bot_name"`
			Template string `json:"template"`
		} `json:"output"`
	}
	// Body is optional — empty body is fine; only log unexpected parse errors.
	if err := c.BodyParser(&req); err != nil && err != fiber.ErrUnprocessableEntity {
		s.log.Debug("template instantiate body parse", zap.Error(err))
	}

	def, err := s.templatesCatalog().Instantiate(name, req.ID, func(candidate string) bool {
		return s.agents(c).Get(candidate) == nil
	})
	if err != nil {
		return s.errJSON(c, fiber.StatusNotFound, err)
	}

	// Starter templates should run on the user's configured default model.
	// Embedded templates intentionally carry conservative example providers
	// (often local Ollama), but a one-click install should not create an agent
	// that gets disabled at boot because the example model is unavailable.
	s.applyTemplateDefinitionDefaults(c, def)
	if strings.TrimSpace(req.Cron) != "" {
		if def.Schedule == nil {
			def.Schedule = &agent.Schedule{}
		}
		def.Schedule.Cron = strings.TrimSpace(req.Cron)
		def.Trigger = agent.TriggerCron
	}
	if strings.TrimSpace(req.Output.Channel) != "" || strings.TrimSpace(req.Output.To) != "" {
		if def.Schedule == nil {
			def.Schedule = &agent.Schedule{}
		}
		def.Schedule.Output = &agent.ScheduleOutput{
			Channel:  strings.TrimSpace(req.Output.Channel),
			To:       strings.TrimSpace(req.Output.To),
			BotName:  strings.TrimSpace(req.Output.BotName),
			Template: strings.TrimSpace(req.Output.Template),
		}
	}
	def.Enabled = true

	dir := ""
	if len(s.config().AgentDirs) > 0 {
		dir = s.config().AgentDirs[0]
	}
	if err := s.agents(c).Upsert(dir, def); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}

	// Register with scheduler if applicable (cron / oneshot templates). If this
	// fails for a scheduled template, roll back the just-created agent so the
	// install doesn't leave a half-broken agent that never fires — the wizard
	// then reports a clean, recoverable failure.
	if err := s.schedules(c).Register(def); err != nil {
		if def.Trigger == agent.TriggerCron {
			s.schedules(c).Deregister(def.ID)
			if delErr := s.agents(c).Delete(def.ID); delErr != nil {
				s.log.Warn("template install rollback: delete failed", zap.String("agent", def.ID), zap.Error(delErr))
			}
			return s.errMsg(c, fiber.StatusBadGateway,
				"could not schedule the agent ("+err.Error()+"); the partially-created agent was removed — fix the schedule and try again")
		}
		// Non-scheduled template: registration failure is non-fatal.
		s.log.Warn("scheduler registration failed", zap.String("agent", def.ID), zap.Error(err))
	}

	return c.Status(fiber.StatusCreated).JSON(def)
}
