// browserlibs.go — the HTTP surface for installing Chromium's shared
// libraries into the workspace.
//
// The image ships without them: ~30MB most installs never use, and system
// packages an unprivileged deployment could not add later anyway. This is how
// a deployment that wants a local browser asks for them — a bundle downloaded
// once onto the volume, where it survives a redeploy.
package gateway

import (
	"path/filepath"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/browserlibs"
)

// workspaceRoot is where installs land. The config file sits in it, which is
// the only handle the server has on the workspace.
func (s *Server) workspaceRoot() string {
	if s.cfgPath == "" {
		return ""
	}
	return filepath.Dir(s.cfgPath)
}

// handleBrowserLibsStatus reports whether the libraries are present, and if
// not, what installing them would take.
func (s *Server) handleBrowserLibsStatus(c *fiber.Ctx) error {
	ws := s.workspaceRoot()
	out := fiber.Map{"dir": browserlibs.Dir(ws)}
	if m, ok := browserlibs.Installed(ws); ok {
		out["installed"] = true
		out["files"] = m.Files
		out["bytes"] = m.Bytes
		out["source"] = m.Source
		out["installed_at"] = m.InstalledAt
		return c.JSON(out)
	}
	out["installed"] = false
	out["hint"] = "install a bundle built with scripts/build-browser-libs.sh, or drive a remote browser with --cdp-endpoint and install nothing"
	return c.JSON(out)
}

// handleBrowserLibsInstall downloads and unpacks a bundle.
//
// The checksum is required rather than optional: these files are loaded into
// the browser process, so an unverified bundle is code execution, not a
// corrupt download.
func (s *Server) handleBrowserLibsInstall(c *fiber.Ctx) error {
	ws := s.workspaceRoot()
	if ws == "" {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "no workspace is known, so there is nowhere to install to")
	}
	var body struct {
		URL    string `json:"url"`
		SHA256 string `json:"sha256"`
	}
	if err := c.BodyParser(&body); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}

	m, err := browserlibs.Install(c.Context(), ws, browserlibs.Options{URL: body.URL, SHA256: body.SHA256})
	if err != nil {
		// A refused install is the caller's problem to fix — a missing
		// checksum, a bad URL, a bundle that is not what was expected — so it
		// is a 400 with the reason, not a 500 with a stack trace.
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}
	s.log.Info("browser libraries installed",
		zap.String("dir", browserlibs.Dir(ws)),
		zap.Int("files", m.Files),
		zap.Int64("bytes", m.Bytes))

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"ok":    true,
		"dir":   browserlibs.Dir(ws),
		"files": m.Files,
		"bytes": m.Bytes,
		// Servers already running were started with the old environment and
		// cannot see the new directory. Saying so beats letting someone
		// conclude the install did not work.
		"restart_needed": true,
		"message":        "Installed. Restart the gateway so browser servers can find the libraries.",
	})
}
