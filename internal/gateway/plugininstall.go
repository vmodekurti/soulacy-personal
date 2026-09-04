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

	"github.com/soulacy/soulacy/internal/introspect"
	"github.com/soulacy/soulacy/internal/plugininstall"
	"github.com/soulacy/soulacy/pkg/plugin"
)

const restartNote = "Restart the gateway for plugin changes to take effect."

// SetPluginInstaller wires the installer. Call after New(); routes return
// 503 until wired (same pattern as SetWorkboardStore).
func (s *Server) SetPluginInstaller(ins *plugininstall.Installer) { s.pluginInstaller = ins }

// SetSafetyPipeline wires the E20 pre-installation introspection pipeline.
// When set, every staged plugin's Preview carries a SecurityReport for the
// approval dialog. nil (the default) leaves Preview.Security empty.
func (s *Server) SetSafetyPipeline(p *introspect.Pipeline) { s.safetyPipeline = p }

func (s *Server) requireInstaller(c *fiber.Ctx) (*plugininstall.Installer, bool) {
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
	}
	_ = c.BodyParser(&body) // optional; metadata enrichment only
	id, err := ins.Approve(c.Params("staged"), body.Source, body.Checksum)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	return c.JSON(fiber.Map{"ok": true, "id": id, "note": restartNote})
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
		return c.JSON(fiber.Map{"ok": true, "enabled": enabled, "note": restartNote})
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
	return c.JSON(fiber.Map{"ok": true, "note": restartNote})
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
	return c.JSON(fiber.Map{"ok": true, "note": restartNote})
}
