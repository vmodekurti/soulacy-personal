package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/platform"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/updates"
	"github.com/soulacy/soulacy/pkg/message"
)

// Auto-update.
//
// The gateway checks the signed release manifest on a schedule. When a newer
// release exists and installing is safe, it downloads and verifies the
// artifact, replaces its own binaries, verifies the new binary runs and
// reports the expected version (rolling back if not), waits until no agent
// run is in flight, and restarts into the new version. When installing is
// not safe (container image, read-only install dir, incomparable dev build)
// it only notifies. Every decision is visible on /system/updates/status.

// Seams for tests: install, verify, restart and environment probes.
var (
	autoInstall            = updates.InstallUpdate
	autoVerifyBinary       = updates.VerifyInstalledBinary
	autoRestoreBackups     = updates.RestoreBackups
	autoRestart            = func() error { return startRestartChild() }
	autoExit               = func() { os.Exit(0) }
	autoInContainer        = updates.InContainer
	autoInstallDirWritable = updates.InstallDirWritable
	autoInstallDir         = updates.InstallDir
	autoPlatform           = platform.Detect
)

// updateMode tells the operator what the checker will do with a new release.
const (
	updateModeInstall = "install" // download, verify, replace, restart
	updateModeNotify  = "notify"  // report only
	updateModeOff     = "off"     // auto-update disabled in config

	upgradeStrategyInline       = "inline"
	upgradeStrategyInstructions = "instructions"
)

const upgradeHelpURL = "https://vmodekurti.github.io/soulacy/deployment/upgrades/"

type upgradeGuidance struct {
	Strategy     string   `json:"upgrade_strategy"`
	Platform     string   `json:"deployment_platform"`
	Reason       string   `json:"upgrade_reason,omitempty"`
	Instructions []string `json:"upgrade_instructions,omitempty"`
	TargetImage  string   `json:"target_image,omitempty"`
	HelpURL      string   `json:"upgrade_help_url"`
}

type updatesState struct {
	LastAppliedVersion string    `json:"last_applied_version,omitempty"`
	LastAppliedAt      time.Time `json:"last_applied_at,omitempty"`
	LastError          string    `json:"last_error,omitempty"`
	LastErrorAt        time.Time `json:"last_error_at,omitempty"`
}

type updatesManager struct {
	sync.RWMutex
	lastCheckTime  time.Time
	nextCheckTime  time.Time
	status         updates.UpdateCheckResult
	checking       bool
	installing     bool
	pendingRestart string // version installed on disk awaiting an idle restart
	deferredSince  time.Time
	mode           string
	modeReason     string
	state          updatesState
	statePath      string
	wake           chan struct{}
}

var globalUpdates = newUpdatesManager()

func newUpdatesManager() *updatesManager {
	return &updatesManager{wake: make(chan struct{}, 1)}
}

// Wake asks the checker loop to run now (config changed, manual trigger).
func (m *updatesManager) Wake() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *updatesManager) loadState(path string) {
	m.Lock()
	defer m.Unlock()
	m.statePath = path
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &m.state)
}

func (m *updatesManager) saveStateLocked() {
	if m.statePath == "" {
		return
	}
	data, err := json.MarshalIndent(m.state, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(m.statePath), 0o700)
	_ = os.WriteFile(m.statePath, data, 0o600)
}

// autoUpdateEnv is everything the decision needs, gathered up front so the
// decision itself is a pure function that tests can table-drive.
type autoUpdateEnv struct {
	AutoEnabled bool
	InContainer bool
	Writable    bool
	ActiveRuns  int
}

// decideAutoUpdate returns the mode the checker is operating in and a reason
// an operator can act on.
func decideAutoUpdate(cfg config.UpdateConfig, env autoUpdateEnv) (mode, reason string) {
	if !cfg.AutoOn() {
		return updateModeOff, "Automatic installation is off (updates.auto: false); new releases are reported only."
	}
	if env.InContainer {
		return updateModeNotify, "Running in a container or managed deployment: upgrade by redeploying a newer image; binaries are not replaced in place."
	}
	if !env.Writable {
		return updateModeNotify, "The install directory is not writable by the gateway; run `sudo sy update install --yes` or fix permissions."
	}
	return updateModeInstall, "New releases are downloaded, verified, installed and applied at the next idle moment."
}

func (s *Server) autoUpdateEnv() autoUpdateEnv {
	detected := autoPlatform()
	env := autoUpdateEnv{
		AutoEnabled: s.cfg.Updates.AutoOn(),
		InContainer: autoInContainer() || detected.Kind != platform.Host,
	}
	if dir, err := autoInstallDir(""); err == nil {
		env.Writable = autoInstallDirWritable(dir)
	}
	if s.runReg != nil {
		env.ActiveRuns = s.runReg.Active()
	}
	return env
}

// upgradeGuidanceFor describes whether this process can safely replace its
// binaries, and gives the GUI concrete redeploy steps when it cannot. The
// upgrade strategy is independent of updates.auto: disabling unattended
// updates must not disable an explicitly requested host upgrade.
func upgradeGuidanceFor(env autoUpdateEnv, detected platform.Info, latest string) upgradeGuidance {
	g := upgradeGuidance{
		Strategy: upgradeStrategyInline,
		Platform: detected.Name,
		HelpURL:  upgradeHelpURL,
	}
	if env.InContainer || detected.Kind != platform.Host {
		if detected.Kind == platform.Host {
			detected = platform.Info{Name: "Container", Kind: platform.Container}
			g.Platform = detected.Name
		}
		g.Strategy = upgradeStrategyInstructions
		g.Reason = "This deployment is image-based, so its running binaries cannot be replaced in place. Redeploy it with the newer image."
		tag := strings.TrimPrefix(strings.TrimSpace(latest), "v")
		if tag == "" {
			tag = "latest"
		}
		g.TargetImage = "ghcr.io/vmodekurti/soulacy-personal:" + tag
		switch detected.Name {
		case "Railway":
			g.Instructions = []string{
				"Open the Soulacy service in Railway.",
				"If the service pins an image tag, change it to " + g.TargetImage + ".",
				"Choose Redeploy and wait for the new deployment to become healthy.",
			}
		case "Render":
			g.Instructions = []string{
				"Open the Soulacy service in Render.",
				"If this is an image service, set its image to " + g.TargetImage + ".",
				"Choose Manual Deploy, then Deploy latest, and wait for the health check.",
			}
		case "Fly.io":
			g.Instructions = []string{
				"Create a new Fly.io release using " + g.TargetImage + " or run fly deploy from the app source.",
				"Wait for the replacement machines to become healthy.",
			}
		case "Google Cloud Run":
			g.Instructions = []string{
				"Open the Soulacy Cloud Run service and edit a new revision.",
				"Set the container image to " + g.TargetImage + ".",
				"Deploy the revision and send traffic to it after the health check passes.",
			}
		case "Heroku":
			g.Instructions = []string{
				"Create a new Heroku release from the latest source or " + g.TargetImage + ".",
				"Wait for the new dyno to start, then verify the Soulacy health endpoint.",
			}
		case "Kubernetes":
			g.Instructions = []string{
				"Update the Soulacy workload image to " + g.TargetImage + ".",
				"Apply the workload and wait for the rollout to complete.",
				"Verify the new pods are healthy before removing the previous replica set.",
			}
		default:
			g.Instructions = []string{
				"Update the Soulacy service image to " + g.TargetImage + ".",
				"Pull the image and recreate or redeploy the service.",
				"Wait for the new container to become healthy.",
			}
		}
		return g
	}
	if !env.Writable {
		g.Strategy = upgradeStrategyInstructions
		g.Reason = "The gateway cannot write to its install directory, so it cannot replace its own binaries."
		g.Instructions = []string{
			"Run sy update install --yes as the account that owns the Soulacy binaries (use sudo only for a root-owned install).",
			"Restart the Soulacy service.",
			"Open Soulacy again and verify the version and gateway health.",
		}
	}
	return g
}

func (s *Server) updatesStatePath() string {
	if s.cfg == nil || strings.TrimSpace(s.cfg.Memory.SQLitePath) == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.cfg.Memory.SQLitePath), "updates-state.json")
}

// startUpdatesChecker runs the check/install loop for the life of the server.
func (s *Server) startUpdatesChecker(ctx context.Context) {
	globalUpdates.loadState(s.updatesStatePath())
	go func() {
		// Initial check after a small delay so it never competes with startup.
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			case <-globalUpdates.wake:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			}
			next := s.runUpdateCycle(ctx)
			globalUpdates.Lock()
			globalUpdates.nextCheckTime = time.Now().Add(next)
			globalUpdates.Unlock()
			timer.Reset(next)
		}
	}()
}

// pendingRestartRetry is how often an installed-but-not-yet-applied update
// re-checks for an idle gateway.
const pendingRestartRetry = 30 * time.Second

// runUpdateCycle performs one check and, when the policy allows, one install
// and restart. It returns how long to wait before the next cycle.
func (s *Server) runUpdateCycle(ctx context.Context) time.Duration {
	interval := s.cfg.Updates.CheckIntervalDuration()
	env := s.autoUpdateEnv()
	mode, reason := decideAutoUpdate(s.cfg.Updates, env)

	globalUpdates.Lock()
	globalUpdates.mode, globalUpdates.modeReason = mode, reason
	pending := globalUpdates.pendingRestart
	globalUpdates.Unlock()

	// An update is already on disk: only the restart is outstanding.
	if pending != "" {
		if s.tryRestartForUpdate(pending, env.ActiveRuns) {
			return interval
		}
		return pendingRestartRetry
	}

	s.log.Info("release update check", zap.String("mode", mode))
	globalUpdates.Lock()
	globalUpdates.checking = true
	globalUpdates.Unlock()
	res, err := updates.CheckForUpdate(ctx, s.cfg.Updates.ManifestURL, "")
	globalUpdates.Lock()
	globalUpdates.checking = false
	if err == nil {
		globalUpdates.status = res
		globalUpdates.lastCheckTime = time.Now()
	}
	globalUpdates.Unlock()
	if err != nil {
		s.log.Warn("release update check failed", zap.Error(err))
		return interval
	}
	if !res.UpdateAvailable {
		return interval
	}
	s.log.Warn("newer Soulacy release available", zap.String("current", res.CurrentVersion), zap.String("latest", res.LatestVersion), zap.String("mode", mode))
	s.emitUpdateEvent("update.available", res.LatestVersion, res.Message)
	if mode != updateModeInstall || res.Artifact == nil {
		return interval
	}
	if err := s.installUpdate(ctx, res.LatestVersion); err != nil {
		return interval
	}
	if s.tryRestartForUpdate(res.LatestVersion, env.ActiveRuns) {
		return interval
	}
	return pendingRestartRetry
}

// installUpdate downloads, verifies and installs the release, then proves the
// new binary runs. A binary that fails verification is rolled back.
func (s *Server) installUpdate(ctx context.Context, version string) error {
	globalUpdates.Lock()
	if globalUpdates.installing {
		globalUpdates.Unlock()
		return nil
	}
	globalUpdates.installing = true
	globalUpdates.Unlock()
	defer func() {
		globalUpdates.Lock()
		globalUpdates.installing = false
		globalUpdates.Unlock()
	}()

	res, err := autoInstall(ctx, updates.UpdateInstallOptions{ManifestSource: s.cfg.Updates.ManifestURL, Yes: true})
	if err == nil && res.Installed {
		if verr := autoVerifyBinary(ctx, res.InstallDir, version); verr != nil {
			rerr := autoRestoreBackups(res.Backups)
			err = verr
			if rerr != nil {
				s.log.Error("auto-update: rollback failed after verification failure", zap.Error(rerr))
			} else {
				s.log.Warn("auto-update: new binary failed verification; previous binaries restored", zap.Error(verr))
			}
		}
	}
	globalUpdates.Lock()
	defer globalUpdates.Unlock()
	if err != nil {
		globalUpdates.state.LastError = err.Error()
		globalUpdates.state.LastErrorAt = time.Now().UTC()
		globalUpdates.saveStateLocked()
		s.log.Error("auto-update: install failed", zap.String("version", version), zap.Error(err))
		s.emitUpdateEvent("update.failed", version, err.Error())
		return err
	}
	if !res.Installed {
		return nil
	}
	globalUpdates.state.LastAppliedVersion = version
	globalUpdates.state.LastAppliedAt = time.Now().UTC()
	globalUpdates.state.LastError = ""
	globalUpdates.pendingRestart = version
	globalUpdates.deferredSince = time.Now()
	globalUpdates.saveStateLocked()
	s.log.Warn("auto-update: release installed; restarting when idle", zap.String("version", version), zap.String("dir", res.InstallDir))
	s.emitUpdateEvent("update.installed", version, res.Message)
	return nil
}

// tryRestartForUpdate restarts into the installed version when no agent run
// is in flight, or once the idle wait has been exhausted. Returns true when a
// restart was initiated.
func (s *Server) tryRestartForUpdate(version string, activeRuns int) bool {
	globalUpdates.RLock()
	since := globalUpdates.deferredSince
	globalUpdates.RUnlock()
	idleWait := s.cfg.Updates.IdleWaitDuration()
	if activeRuns > 0 && time.Since(since) < idleWait {
		s.log.Info("auto-update: restart deferred, agent runs in flight", zap.Int("active_runs", activeRuns), zap.String("version", version))
		return false
	}
	if activeRuns > 0 {
		s.log.Warn("auto-update: idle wait exhausted, restarting with runs in flight", zap.Int("active_runs", activeRuns))
	}
	if err := autoRestart(); err != nil {
		s.log.Error("auto-update: restart failed", zap.Error(err))
		globalUpdates.Lock()
		globalUpdates.state.LastError = "restart failed: " + err.Error()
		globalUpdates.state.LastErrorAt = time.Now().UTC()
		globalUpdates.saveStateLocked()
		globalUpdates.Unlock()
		return false
	}
	s.log.Warn("auto-update: restarting into new release", zap.String("version", version))
	s.emitUpdateEvent("update.restarting", version, "Restarting the gateway into "+version+".")
	globalUpdates.Lock()
	globalUpdates.pendingRestart = ""
	globalUpdates.Unlock()
	exit := autoExit
	go func() {
		time.Sleep(250 * time.Millisecond)
		exit()
	}()
	return true
}

func (s *Server) emitUpdateEvent(kind, version, detail string) {
	if s.hub == nil {
		return
	}
	s.hub.Emit(message.Event{
		Type:      "system.update",
		Timestamp: time.Now().UTC(),
		Payload:   map[string]any{"event": kind, "version": version, "detail": detail},
	})
}

func (s *Server) handleGetUpdatesStatus(c *fiber.Ctx) error {
	env := s.autoUpdateEnv()
	mode, reason := decideAutoUpdate(s.cfg.Updates, env)
	globalUpdates.RLock()
	defer globalUpdates.RUnlock()
	guidance := upgradeGuidanceFor(env, autoPlatform(), globalUpdates.status.LatestVersion)
	out := fiber.Map{
		"last_check_time":     globalUpdates.lastCheckTime.Format(time.RFC3339),
		"next_check_time":     globalUpdates.nextCheckTime.Format(time.RFC3339),
		"checking":            globalUpdates.checking,
		"installing":          globalUpdates.installing,
		"update_available":    globalUpdates.status.UpdateAvailable,
		"current_version":     globalUpdates.status.CurrentVersion,
		"latest_version":      globalUpdates.status.LatestVersion,
		"message":             globalUpdates.status.Message,
		"auto_enabled":        s.cfg.Updates.AutoOn(),
		"mode":                mode,
		"mode_reason":         reason,
		"check_interval":      s.cfg.Updates.CheckIntervalDuration().String(),
		"pending_restart":     globalUpdates.pendingRestart,
		"active_runs":         env.ActiveRuns,
		"upgrade_strategy":    guidance.Strategy,
		"deployment_platform": guidance.Platform,
		"upgrade_help_url":    guidance.HelpURL,
	}
	if guidance.Reason != "" {
		out["upgrade_reason"] = guidance.Reason
	}
	if len(guidance.Instructions) > 0 {
		out["upgrade_instructions"] = guidance.Instructions
	}
	if guidance.TargetImage != "" {
		out["target_image"] = guidance.TargetImage
	}
	if globalUpdates.state.LastAppliedVersion != "" {
		out["last_applied_version"] = globalUpdates.state.LastAppliedVersion
		out["last_applied_at"] = globalUpdates.state.LastAppliedAt.Format(time.RFC3339)
	}
	if globalUpdates.state.LastError != "" {
		out["last_error"] = globalUpdates.state.LastError
		out["last_error_at"] = globalUpdates.state.LastErrorAt.Format(time.RFC3339)
	}
	return c.JSON(out)
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
	// A manual check that finds a release lets the loop act on it now.
	if res.UpdateAvailable {
		globalUpdates.Wake()
	}
	return c.JSON(res)
}

func (s *Server) handleTriggerUpgrade(c *fiber.Ctx) error {
	s.log.Warn("gateway self-upgrade requested via API", zap.Any("request_id", c.Locals("request_id")))
	env := s.autoUpdateEnv()
	globalUpdates.RLock()
	latest := globalUpdates.status.LatestVersion
	globalUpdates.RUnlock()
	guidance := upgradeGuidanceFor(env, autoPlatform(), latest)
	if guidance.Strategy != upgradeStrategyInline {
		s.recordAdminAudit(c, "upgrade.request", "gateway", "", "rejected", map[string]any{"reason": guidance.Reason})
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{
			"error":                "inline upgrade is unavailable for this deployment",
			"upgrade_strategy":     guidance.Strategy,
			"deployment_platform":  guidance.Platform,
			"upgrade_reason":       guidance.Reason,
			"upgrade_instructions": guidance.Instructions,
			"target_image":         guidance.TargetImage,
			"upgrade_help_url":     guidance.HelpURL,
		})
	}

	res, err := autoInstall(c.Context(), updates.UpdateInstallOptions{ManifestSource: s.cfg.Updates.ManifestURL, Yes: true})
	if err != nil {
		s.log.Error("self-upgrade failed", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	if res.Installed {
		if verr := autoVerifyBinary(c.Context(), res.InstallDir, res.LatestVersion); verr != nil {
			_ = autoRestoreBackups(res.Backups)
			s.log.Error("self-upgrade: new binary failed verification; previous binaries restored", zap.Error(verr))
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "upgrade rolled back: " + verr.Error()})
		}
		globalUpdates.Lock()
		globalUpdates.state.LastAppliedVersion = res.LatestVersion
		globalUpdates.state.LastAppliedAt = time.Now().UTC()
		globalUpdates.state.LastError = ""
		globalUpdates.saveStateLocked()
		globalUpdates.Unlock()
	}

	s.recordAdminAudit(c, "upgrade.request", "gateway", "", "accepted", nil)

	if !res.Installed {
		return c.JSON(fiber.Map{"ok": true, "message": res.Message, "result": res})
	}
	// Spawns a replacement process of the newly downloaded binary and exits!
	if err := autoRestart(); err != nil {
		s.log.Error("restart after upgrade failed", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "upgrade succeeded but restart failed: " + err.Error(),
		})
	}
	s.emitUpdateEvent("update.restarting", res.LatestVersion, "Restarting the gateway into "+res.LatestVersion+".")
	exit := autoExit
	go func() {
		time.Sleep(250 * time.Millisecond)
		exit()
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
