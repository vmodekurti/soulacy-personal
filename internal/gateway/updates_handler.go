package gateway

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/updates"
)

type updatesManager struct {
	sync.RWMutex
	lastCheckTime time.Time
	status        updates.UpdateCheckResult
	checking      bool
}

var globalUpdates = &updatesManager{}

func (s *Server) startUpdatesChecker() {
	go func() {
		// Run initial check after a small delay to not block server startup
		time.Sleep(5 * time.Second)
		for {
			s.log.Info("running background release update check...")
			globalUpdates.Lock()
			globalUpdates.checking = true
			globalUpdates.Unlock()

			res, err := updates.CheckForUpdate(context.Background(), s.cfg.Updates.ManifestURL, "")

			globalUpdates.Lock()
			globalUpdates.checking = false
			if err == nil {
				globalUpdates.status = res
				globalUpdates.lastCheckTime = time.Now()
			}
			globalUpdates.Unlock()

			if err != nil {
				s.log.Warn("background update check failed", zap.Error(err))
			} else if res.UpdateAvailable {
				s.log.Warn("newer version of Soulacy is available", zap.String("latest", res.LatestVersion))
			}

			// Check every 12 hours
			time.Sleep(12 * time.Hour)
		}
	}()
}

func (s *Server) handleGetUpdatesStatus(c *fiber.Ctx) error {
	globalUpdates.RLock()
	defer globalUpdates.RUnlock()
	return c.JSON(fiber.Map{
		"last_check_time":  globalUpdates.lastCheckTime.Format(time.RFC3339),
		"checking":         globalUpdates.checking,
		"update_available": globalUpdates.status.UpdateAvailable,
		"current_version":  globalUpdates.status.CurrentVersion,
		"latest_version":   globalUpdates.status.LatestVersion,
		"message":          globalUpdates.status.Message,
	})
}

func (s *Server) handleTriggerUpdatesCheck(c *fiber.Ctx) error {
	globalUpdates.Lock()
	globalUpdates.checking = true
	globalUpdates.Unlock()

	res, err := updates.CheckForUpdate(c.Context(), s.cfg.Updates.ManifestURL, "")

	globalUpdates.Lock()
	globalUpdates.checking = false
	if err == nil {
		globalUpdates.status = res
		globalUpdates.lastCheckTime = time.Now()
	}
	globalUpdates.Unlock()

	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(res)
}

func (s *Server) handleTriggerUpgrade(c *fiber.Ctx) error {
	s.log.Warn("gateway self-upgrade requested via API", zap.Any("request_id", c.Locals("request_id")))

	opts := updates.UpdateInstallOptions{
		ManifestSource: s.cfg.Updates.ManifestURL,
		Yes:            true,
	}

	res, err := updates.InstallUpdate(c.Context(), opts)
	if err != nil {
		s.log.Error("self-upgrade failed", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	s.recordAdminAudit(c, "upgrade.request", "gateway", "", "accepted", nil)

	// Spawns a replacement process of the newly downloaded binary and exits!
	if err := startRestartChild(); err != nil {
		s.log.Error("restart after upgrade failed", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "upgrade succeeded but restart failed: " + err.Error(),
		})
	}

	go func() {
		time.Sleep(250 * time.Millisecond)
		os.Exit(0)
	}()

	return c.JSON(fiber.Map{
		"ok":      true,
		"message": "Upgrade completed successfully. Gateway is restarting.",
		"result":  res,
	})
}

func (s *Server) registerUpdatesRoutes(api fiber.Router) {
	api.Get("/system/updates/status", s.rbacMW(rbac.ResourceConfig, rbac.ActionRead), s.handleGetUpdatesStatus)
	api.Post("/system/updates/check", s.rbacMW(rbac.ResourceConfig, rbac.ActionRead), s.handleTriggerUpdatesCheck)
	api.Post("/system/updates/upgrade", s.rbacMW(rbac.ResourceConfig, rbac.ActionWrite), s.handleTriggerUpgrade)
}
