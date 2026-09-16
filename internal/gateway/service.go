// service.go — autostart ("start on login") from the dashboard.
//
// Installing the background service used to be reachable only from `sy daemon`.
// That put the single most consequential first-day setting behind a shell: a
// user who never opens a terminal gets a gateway that dies with the window
// that launched it, taking every scheduled agent with it. Nothing in the
// dashboard said so, and nothing in the dashboard could fix it.
//
// These routes are deliberately thin. All behaviour lives in internal/service
// so the browser and `sy daemon` cannot drift apart.
package gateway

import (
	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/service"
)

// serviceManager builds a Manager from the live workspace. Resolving here
// rather than holding a field keeps the Server struct unchanged, and the
// workspace cannot move while the process is running.
func (s *Server) serviceManager() (*service.Manager, error) {
	ws, err := config.ResolveWorkspace()
	if err != nil {
		return nil, err
	}
	return service.New(ws), nil
}

func (s *Server) handleServiceStatus(c *fiber.Ctx) error {
	mgr, err := s.serviceManager()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(mgr.Status())
}

// handleServiceInstall writes and loads the unit. The response always carries
// the resulting status, including on failure, so the dashboard can re-render
// the true state rather than guessing from an error string.
func (s *Server) handleServiceInstall(c *fiber.Ctx) error {
	mgr, err := s.serviceManager()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	st, err := mgr.Install()
	if err != nil {
		s.log.Error("autostart install failed", zap.Error(err))
		s.recordAdminAudit(c, "service.install", "gateway", "", "failed", map[string]any{"error": err.Error()})
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error(), "status": st})
	}
	s.log.Info("autostart installed", zap.String("unit", st.UnitPath), zap.String("binary", st.Binary))
	s.recordAdminAudit(c, "service.install", "gateway", st.UnitPath, "ok", nil)
	return c.JSON(fiber.Map{"ok": true, "status": st})
}

func (s *Server) handleServiceUninstall(c *fiber.Ctx) error {
	mgr, err := s.serviceManager()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	st, err := mgr.Uninstall()
	if err != nil {
		s.log.Error("autostart uninstall failed", zap.Error(err))
		s.recordAdminAudit(c, "service.uninstall", "gateway", "", "failed", map[string]any{"error": err.Error()})
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error(), "status": st})
	}
	s.recordAdminAudit(c, "service.uninstall", "gateway", st.UnitPath, "ok", nil)
	return c.JSON(fiber.Map{"ok": true, "status": st})
}

// handleServiceStart and handleServiceStop act on an already-installed unit.
// Stop leaves it installed on purpose: "pause it for now" and "never start
// again" are different intents and the dashboard offers both.
func (s *Server) handleServiceStart(c *fiber.Ctx) error {
	mgr, err := s.serviceManager()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	st, err := mgr.Start()
	if err != nil {
		s.recordAdminAudit(c, "service.start", "gateway", "", "failed", map[string]any{"error": err.Error()})
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error(), "status": st})
	}
	s.recordAdminAudit(c, "service.start", "gateway", st.UnitPath, "ok", nil)
	return c.JSON(fiber.Map{"ok": true, "status": st})
}

func (s *Server) handleServiceStop(c *fiber.Ctx) error {
	mgr, err := s.serviceManager()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	st, err := mgr.Stop()
	if err != nil {
		s.recordAdminAudit(c, "service.stop", "gateway", "", "failed", map[string]any{"error": err.Error()})
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error(), "status": st})
	}
	s.recordAdminAudit(c, "service.stop", "gateway", st.UnitPath, "ok", nil)
	return c.JSON(fiber.Map{"ok": true, "status": st})
}
