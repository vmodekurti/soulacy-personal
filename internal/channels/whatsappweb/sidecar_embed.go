// sidecar_embed.go — the WhatsApp Web sidecar must work for INSTALLED
// binaries, not just repo checkouts. The Node script is embedded in the
// binary and materialised into the session directory on demand, and its
// one npm dependency (Baileys) is installed there automatically — so
// "Generate QR code" is one click on a fresh machine with Node installed.
package whatsappweb

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "embed"
)

//go:embed whatsapp-web-sidecar.mjs
var sidecarScript []byte

// SidecarScriptName is the filename written into the session directory.
const SidecarScriptName = "whatsapp-web-sidecar.mjs"

// baileysPackage is the sidecar's single npm dependency.
const baileysPackage = "@whiskeysockets/baileys"

// EnsureSidecarScript writes the embedded sidecar script into dir
// (creating it) and returns the script's absolute path. An existing file
// with different content is overwritten — upgrades ship the new script
// automatically.
func EnsureSidecarScript(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("whatsapp_web: sidecar dir is empty — session_dir not resolved")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("whatsapp_web: create sidecar dir: %w", err)
	}
	path := filepath.Join(dir, SidecarScriptName)
	if existing, err := os.ReadFile(path); err != nil || !bytes.Equal(existing, sidecarScript) {
		if err := os.WriteFile(path, sidecarScript, 0o644); err != nil {
			return "", fmt.Errorf("whatsapp_web: write sidecar script: %w", err)
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("whatsapp_web: resolve sidecar path: %w", err)
	}
	return abs, nil
}

// BaileysInstalled reports whether the sidecar's dependency is already
// resolvable next to the script in dir.
func BaileysInstalled(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "node_modules", "@whiskeysockets", "baileys", "package.json"))
	return err == nil
}

// EnsureBaileys installs the Baileys dependency into dir/node_modules via
// npm when it's missing. Node resolves bare imports by walking up from the
// script's own directory, so a node_modules sibling of the script is all
// the sidecar needs regardless of the gateway's working directory.
func EnsureBaileys(ctx context.Context, dir string) error {
	if BaileysInstalled(dir) {
		return nil
	}
	npm, err := exec.LookPath("npm")
	if err != nil {
		return fmt.Errorf("whatsapp_web: npm not found — WhatsApp Web needs Node.js (install from https://nodejs.org), or run manually: npm install --prefix %s %s", dir, baileysPackage)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, npm, "install", "--prefix", dir,
		"--no-fund", "--no-audit", "--loglevel", "error", baileysPackage)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := string(out)
		if len(msg) > 400 {
			msg = msg[len(msg)-400:]
		}
		return fmt.Errorf("whatsapp_web: installing %s failed: %v — %s", baileysPackage, err, msg)
	}
	if !BaileysInstalled(dir) {
		return fmt.Errorf("whatsapp_web: npm install reported success but %s is still missing in %s", baileysPackage, dir)
	}
	return nil
}
