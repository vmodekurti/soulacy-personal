package gateway

// Plugin install & management API (Story E13). Local-first: installs come
// from a git URL, a checksummed archive, or a local directory — no central
// marketplace. The flow is stage → human approval of the manifest's
// requested capabilities/credentials → activate; nothing runs before
// approval. All management actions take effect at the next gateway restart
// (plugins load at boot), which every response states explicitly.

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/caps"
	"github.com/soulacy/soulacy/internal/introspect"
	"github.com/soulacy/soulacy/internal/plugininstall"
	"github.com/soulacy/soulacy/internal/plugins"
	"github.com/soulacy/soulacy/pkg/plugin"
)

// pluginPartialNote names what a plugin lifecycle change does NOT reach.
//
// It used to read "Restart the gateway for plugin changes to take effect",
// which was wrong in both directions once the tool surface became hot: it told
// operators to restart for a change that had already applied, and it gave them
// no way to know that a DIFFERENT part of the same change had not.
//
// Naming the four contributions is the point. An operator who has just revoked
// a plugin needs to know its tools are gone now and its sidecar process is
// still running, because those have different consequences and only one of
// them is urgent.
const pluginPartialNote = "Tools, and the plugin's own settings, apply immediately. A plugin's sidecar channels, " +
	"GUI panels and contributed LLM providers are wired at process start and keep running until the next one."

// SetPluginInstaller wires the installer. Call after New(); routes return
// 503 until wired (same pattern as SetWorkboardStore).
func (s *Server) SetPluginInstaller(ins *plugininstall.Installer) { s.pluginInstaller = ins }

// SetPluginInstallers wires the per-workspace installers. When set it takes
// precedence, so every install lands in the caller's own plugin directory
// rather than in one shared root where a single tenant's approval would
// activate a plugin for the whole deployment.
func (s *Server) SetPluginInstallers(all *plugininstall.Installers) { s.pluginInstallers = all }

// SetPluginStores installs the per-workspace plugin registry so a lifecycle
// change takes effect without a restart.
func (s *Server) SetPluginStores(stores *plugins.Stores) { s.pluginStores = stores }

// applyPluginsConfigLive pushes an edited plugins_config into every workspace's
// loaders.
//
// Separate from pluginsChanged because they answer different events:
// pluginsChanged is "this workspace's installed set changed, rescan it", and
// this is "the settings every plugin reads changed, re-attach them". Conflating
// them would make a settings edit rescan every plugin directory in the
// deployment, which is disk work for a change that touches no files.
func (s *Server) applyPluginsConfigLive(settings map[string]map[string]any) {
	if s == nil || s.pluginStores == nil {
		return
	}
	s.pluginStores.SetSettings(settings)
}

// pluginsChanged makes a lifecycle decision take effect NOW.
//
// Every handler below used to answer with restartNote and nothing else, which
// meant a revoked plugin stayed callable for the life of the process. The
// engine resolves a workspace's plugin tools through Stores.For on every
// dispatch, so dropping the cached loader is the whole mechanism: the next
// tool call rescans and the removed plugin is simply not there.
//
// pluginPartialNote stays on the responses, because it is still true for the
// parts a rescan cannot reach — sidecar channel processes, GUI mounts, and
// provider registrations are wired at boot. It NAMES them rather than saying
// "restart", so an operator can tell which half of their change landed.
func (s *Server) pluginsChanged(c *fiber.Ctx) {
	if s == nil || s.pluginStores == nil {
		return
	}
	workspaceID := mcpWorkspace(c)
	s.pluginStores.Invalidate(workspaceID)

	// AND the capability grants, which are the half that was missing.
	//
	// Invalidating the loader removes a revoked plugin's TOOLS — an agent can
	// no longer call it — and left its capability grant standing in the
	// enforcer. So a plugin revoked *because it was doing something it should
	// not* could still reach every host API its manifest had asked for, until
	// the gateway restarted.
	//
	// That is the worse half of a revocation to get wrong, and it was the
	// silent one: the tools disappear visibly, so the revocation looks
	// complete.
	s.reconcileCapabilities(workspaceID)
}

// reconcileCapabilities makes a workspace's capability grants exactly those of
// the plugins it currently has.
//
// Read back from the (just-rescanned) loader rather than computed from what
// changed. A caller that had to work out whether this was an install, a
// revoke or a re-approval would get it wrong on the third case — a plugin
// whose manifest now asks for MORE than it did — which is the one nobody
// tests and the one that matters.
func (s *Server) reconcileCapabilities(workspaceID string) {
	if s == nil || s.capsEnforcer == nil || s.pluginStores == nil {
		return
	}
	loader := s.pluginStores.For(workspaceID)
	if loader == nil {
		return
	}
	sets := make([]*caps.Set, 0, loader.Count())
	for _, lp := range loader.All() {
		if lp != nil && lp.Caps != nil {
			sets = append(sets, lp.Caps)
		}
	}
	s.capsEnforcer.ReplaceWorkspaceSets(workspaceID, sets)
}

// SetSafetyPipeline wires the E20 pre-installation introspection pipeline.
// When set, every staged plugin's Preview carries a SecurityReport for the
// approval dialog. nil (the default) leaves Preview.Security empty.
func (s *Server) SetSafetyPipeline(p *introspect.Pipeline) { s.safetyPipeline = p }

func (s *Server) requireInstaller(c *fiber.Ctx) (*plugininstall.Installer, bool) {
	if s.pluginInstallers != nil {
		if ins := s.pluginInstallers.For(s.requestWorkspace(c)); ins != nil {
			return ins, true
		}
		// Deliberately not falling through to the shared installer: an install
		// that cannot be placed in the caller's own directory must fail, not
		// land in somebody else's.
		_ = c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "plugin installer unavailable for this workspace",
		})
		return nil, false
	}
	if s.pluginInstaller == nil {
		_ = c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "plugin installer unavailable (no plugin_dirs configured)",
		})
		return nil, false
	}
	return s.pluginInstaller, true
}

// GET /api/v1/plugins/installed
func (s *Server) handleListInstalledPlugins(c *fiber.Ctx) error {
	ins, ok := s.requireInstaller(c)
	if !ok {
		return nil
	}
	list, err := ins.List()
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if list == nil {
		list = []plugininstall.Installed{}
	}
	return c.JSON(fiber.Map{"plugins": list, "count": len(list)})
}

// POST /api/v1/plugins/install  {source, checksum?} → approval preview
func (s *Server) handleStagePlugin(c *fiber.Ctx) error {
	ins, ok := s.requireInstaller(c)
	if !ok {
		return nil
	}
	var body struct {
		Source   string `json:"source"`
		Checksum string `json:"checksum"`
	}
	if err := c.BodyParser(&body); err != nil || body.Source == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "body must be {source, checksum?}; archives require a sha256 checksum",
		})
	}
	pv, err := ins.Stage(c.Context(), body.Source, body.Checksum)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}

	// E20 — safety introspection on the staged dir before anyone approves.
	// Pipeline degradations become findings, never failures; a missing
	// pipeline simply leaves Security nil (older deployments).
	if s.safetyPipeline != nil {
		stagedDir := ins.StagedDir(pv.StagedID)
		var mp *plugin.Manifest
		if m, merr := plugininstall.ReadManifest(stagedDir); merr == nil {
			mp = &m
		}
		ictx, cancel := context.WithTimeout(c.Context(), 90*time.Second)
		report := s.safetyPipeline.Run(ictx, stagedDir, mp)
		cancel()
		pv.Security = &report
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"preview": pv,
		"note":    "Nothing is active yet. Review the requested permissions and credentials, then approve.",
	})
}

// POST /api/v1/plugins/install/:staged/approve  {source, checksum?}
func (s *Server) handleApprovePlugin(c *fiber.Ctx) error {
	ins, ok := s.requireInstaller(c)
	if !ok {
		return nil
	}
	var body struct {
		Source   string `json:"source"`
		Checksum string `json:"checksum"`
		// Revision is the commit the staging step resolved to. The client
		// echoes back what the preview showed, so the record names the exact
		// code the operator was looking at when they approved.
		Revision string `json:"revision"`
	}
	_ = c.BodyParser(&body) // optional; metadata enrichment only
	id, err := ins.Approve(c.Params("staged"), body.Source, body.Checksum, body.Revision)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	s.pluginsChanged(c)
	return c.JSON(fiber.Map{"ok": true, "id": id, "note": pluginPartialNote})
}

// DELETE /api/v1/plugins/install/:staged
func (s *Server) handleDiscardStagedPlugin(c *fiber.Ctx) error {
	ins, ok := s.requireInstaller(c)
	if !ok {
		return nil
	}
	if err := ins.Discard(c.Params("staged")); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.JSON(fiber.Map{"ok": true})
}

// POST /api/v1/plugins/:id/enable | /disable
func (s *Server) handleSetPluginEnabled(enabled bool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ins, ok := s.requireInstaller(c)
		if !ok {
			return nil
		}
		if err := ins.SetEnabled(c.Params("id"), enabled); err != nil {
			return s.errJSON(c, fiber.StatusNotFound, err)
		}
		s.pluginsChanged(c)
		return c.JSON(fiber.Map{"ok": true, "enabled": enabled, "note": pluginPartialNote})
	}
}

// POST /api/v1/plugins/:id/reapprove
func (s *Server) handleReapprovePlugin(c *fiber.Ctx) error {
	ins, ok := s.requireInstaller(c)
	if !ok {
		return nil
	}
	if err := ins.Reapprove(c.Params("id")); err != nil {
		return s.errJSON(c, fiber.StatusNotFound, err)
	}
	s.pluginsChanged(c)
	return c.JSON(fiber.Map{"ok": true, "note": pluginPartialNote})
}

// DELETE /api/v1/plugins/:id
func (s *Server) handleRemovePlugin(c *fiber.Ctx) error {
	ins, ok := s.requireInstaller(c)
	if !ok {
		return nil
	}
	if err := ins.Remove(c.Params("id")); err != nil {
		return s.errJSON(c, fiber.StatusNotFound, err)
	}
	s.pluginsChanged(c)
	return c.JSON(fiber.Map{"ok": true, "note": pluginPartialNote})
}
