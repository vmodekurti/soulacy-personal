// daemon.go — `sy daemon` subcommands for installing/managing soulacy as
// a background service (launchd on macOS, systemd --user on Linux).
//
// Why this exists separately from install.sh: install.sh asks once during
// install whether to set up LaunchAgent. After that, an operator who wants
// to enable/disable auto-start, check status, or tail logs would have to
// know launchctl/systemctl syntax. `sy daemon` makes those operations
// first-class commands that survive upgrades and work the same way on
// both platforms.
//
// Design:
//   - Use ~/Library/LaunchAgents/com.soulacy.soulacy.plist on macOS
//     (user-scoped — no sudo, no root daemon, matches how install.sh has
//     historically worked).
//   - Use ~/.config/systemd/user/soulacy.service on Linux (also user-
//     scoped via systemctl --user — no sudo). systemd --user requires the
//     user be logged in or `loginctl enable-linger <user>` set; we surface
//     that as a warning in the install output.
//   - All file paths and binary paths come from `syWorkspace()` and
//     `exec.LookPath("soulacy")` — never hardcoded.

package main

import (
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

const (
	launchdLabel    = "com.soulacy.soulacy"
	systemdUnitName = "soulacy.service"
)

func buildDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run soulacy as a background service (launchd / systemd --user)",
		Long: `Manage the soulacy gateway as a per-user background service.

On macOS this writes a LaunchAgent plist to ~/Library/LaunchAgents/.
On Linux this writes a systemd user unit to ~/.config/systemd/user/.
Both modes are user-scoped — no sudo, no root daemon.

Subcommands:
  install     Write the unit file and load/enable the service.
  uninstall   Stop, unload, and remove the unit file.
  start       Start the installed service without reinstalling it.
  stop        Stop the running service, leaving it installed.
  status      Show whether the service is loaded and its recent state.
  logs        Tail the service log.`,
	}
	cmd.AddCommand(
		buildDaemonInstallCmd(),
		buildDaemonUninstallCmd(),
		buildDaemonStartCmd(),
		buildDaemonStopCmd(),
		buildDaemonStatusCmd(),
		buildDaemonLogsCmd(),
	)
	return cmd
}

func buildDaemonInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Write the service file and start the gateway on login",
		RunE: func(cmd *cobra.Command, args []string) error {
			return daemonInstall()
		},
	}
}

func buildDaemonUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Stop the gateway service and remove the service file",
		RunE: func(cmd *cobra.Command, args []string) error {
			return daemonUninstall()
		},
	}
}

func buildDaemonStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the installed gateway service",
		RunE: func(cmd *cobra.Command, args []string) error {
			return daemonStart()
		},
	}
}

func buildDaemonStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the running gateway service (leaves it installed)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return daemonStop()
		},
	}
}

func buildDaemonStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether the gateway service is loaded and recent state",
		RunE: func(cmd *cobra.Command, args []string) error {
			return daemonStatus()
		},
	}
}

func buildDaemonLogsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Tail the gateway service log",
		RunE: func(cmd *cobra.Command, args []string) error {
			follow, _ := cmd.Flags().GetBool("follow")
			lines, _ := cmd.Flags().GetInt("lines")
			return daemonLogs(follow, lines)
		},
	}
	cmd.Flags().BoolP("follow", "f", false, "follow the log (tail -f style)")
	cmd.Flags().IntP("lines", "n", 50, "number of lines to print before following")
	return cmd
}

// ── Platform dispatch ────────────────────────────────────────────────────────

func daemonInstall() error {
	bin, err := resolveSoulacyBinary()
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "darwin":
		return installLaunchAgent(bin)
	case "linux":
		return installSystemdUserUnit(bin)
	default:
		return fmt.Errorf("daemon install: unsupported OS %q (macOS and Linux only)", runtime.GOOS)
	}
}

func daemonUninstall() error {
	switch runtime.GOOS {
	case "darwin":
		return uninstallLaunchAgent()
	case "linux":
		return uninstallSystemdUserUnit()
	default:
		return fmt.Errorf("daemon uninstall: unsupported OS %q", runtime.GOOS)
	}
}

func daemonStatus() error {
	switch runtime.GOOS {
	case "darwin":
		return statusLaunchAgent()
	case "linux":
		return statusSystemdUserUnit()
	default:
		return fmt.Errorf("daemon status: unsupported OS %q", runtime.GOOS)
	}
}

func daemonStart() error {
	switch runtime.GOOS {
	case "darwin":
		return startLaunchAgent()
	case "linux":
		return startSystemdUserUnit()
	default:
		return fmt.Errorf("daemon start: unsupported OS %q", runtime.GOOS)
	}
}

func daemonStop() error {
	switch runtime.GOOS {
	case "darwin":
		return stopLaunchAgent()
	case "linux":
		return stopSystemdUserUnit()
	default:
		return fmt.Errorf("daemon stop: unsupported OS %q", runtime.GOOS)
	}
}

func daemonLogs(follow bool, lines int) error {
	ws := syWorkspace()
	logPath := filepath.Join(ws.Logs, "soulacy.log")
	if _, err := os.Stat(logPath); err != nil {
		return fmt.Errorf("daemon logs: %s not found (has the service ever run?)", logPath)
	}
	args := []string{"-n", fmt.Sprintf("%d", lines)}
	if follow {
		args = append(args, "-f")
	}
	args = append(args, logPath)
	c := exec.Command("tail", args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// ── macOS / launchd ──────────────────────────────────────────────────────────

func launchAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"), nil
}

func installLaunchAgent(bin string) error {
	path, err := launchAgentPath()
	if err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	ws := syWorkspace()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	plist := fmt.Sprintf(launchdPlistTemplate, launchdLabel, bin, home, servicePATH(), ws.Logs, ws.Logs)
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}

	// Unload any prior version (idempotent — fine if it wasn't loaded).
	_ = exec.Command("launchctl", "unload", path).Run()
	if out, err := exec.Command("launchctl", "load", "-w", path).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl load failed: %v: %s", err, strings.TrimSpace(string(out)))
	}

	fmt.Printf("%s LaunchAgent installed:\n", green("✓"))
	fmt.Printf("  plist: %s\n", path)
	fmt.Printf("  label: %s\n", launchdLabel)
	fmt.Printf("  binary: %s\n", bin)
	fmt.Printf("  logs: %s\n", filepath.Join(ws.Logs, "soulacy.log"))
	fmt.Printf("\nThe gateway will start automatically at login and on reboot.\n")
	fmt.Printf("Manage it with: sy daemon {status,uninstall,logs}\n")
	return nil
}

func servicePATH() string {
	seen := map[string]bool{}
	parts := make([]string, 0, 12)
	for _, dir := range append(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)),
		"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin") {
		dir = strings.TrimSpace(dir)
		if dir != "" && !seen[dir] {
			seen[dir] = true
			parts = append(parts, dir)
		}
	}
	return html.EscapeString(strings.Join(parts, string(os.PathListSeparator)))
}

func uninstallLaunchAgent() error {
	path, err := launchAgentPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Printf("%s No LaunchAgent at %s (already uninstalled)\n", yellow("⚠"), path)
		return nil
	}
	// Best-effort unload — succeeds even if the agent wasn't loaded.
	_ = exec.Command("launchctl", "unload", path).Run()
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove plist: %w", err)
	}
	fmt.Printf("%s LaunchAgent unloaded and removed: %s\n", green("✓"), path)
	return nil
}

func statusLaunchAgent() error {
	path, err := launchAgentPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Printf("%s Not installed. Run `sy daemon install` to enable auto-start.\n", yellow("•"))
		return nil
	}
	out, err := exec.Command("launchctl", "list", launchdLabel).CombinedOutput()
	if err != nil {
		// launchctl list returns exit 0 with a "Could not find service" line if not loaded.
		fmt.Printf("%s Plist present but not loaded: %s\n", yellow("⚠"), path)
		fmt.Printf("  Try: launchctl load -w %s\n", path)
		return nil
	}
	fmt.Printf("%s LaunchAgent loaded.\n", green("✓"))
	fmt.Printf("  plist: %s\n", path)
	fmt.Printf("  launchctl list output:\n")
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fmt.Printf("    %s\n", line)
	}
	return nil
}

func startLaunchAgent() error {
	path, err := launchAgentPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("not installed: run `sy daemon install` first")
	}
	// load -w is idempotent and re-enables a service that `stop` unloaded.
	if out, err := exec.Command("launchctl", "load", "-w", path).CombinedOutput(); err != nil {
		// Already loaded is fine; only surface a genuine failure.
		msg := strings.TrimSpace(string(out))
		if msg != "" && !strings.Contains(strings.ToLower(msg), "already loaded") {
			return fmt.Errorf("launchctl load failed: %v: %s", err, msg)
		}
	}
	_ = exec.Command("launchctl", "start", launchdLabel).Run()
	fmt.Printf("%s Gateway service started.\n", green("✓"))
	return nil
}

func stopLaunchAgent() error {
	path, err := launchAgentPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Printf("%s Not installed (nothing to stop).\n", yellow("•"))
		return nil
	}
	// Unload stops the job even with KeepAlive=true; the plist file stays in
	// place so `sy daemon status` still reports it as installed and `start`
	// can bring it back.
	if out, err := exec.Command("launchctl", "unload", path).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl unload failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	fmt.Printf("%s Gateway service stopped (still installed; `sy daemon start` to resume).\n", green("✓"))
	return nil
}

const launchdPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>serve</string>
  </array>
  <key>WorkingDirectory</key>
  <string>%s</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>%s</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>%s/soulacy.log</string>
  <key>StandardErrorPath</key>
  <string>%s/soulacy-error.log</string>
</dict>
</plist>
`

// ── Linux / systemd --user ───────────────────────────────────────────────────

func systemdUnitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", systemdUnitName), nil
}

func installSystemdUserUnit(bin string) error {
	path, err := systemdUnitPath()
	if err != nil {
		return err
	}
	ws := syWorkspace()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create systemd user dir: %w", err)
	}
	unit := fmt.Sprintf(systemdUnitTemplate, bin, ws.Root)
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write unit: %w", err)
	}

	// Reload the user manager, enable + start.
	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("systemctl", "--user", "enable", "--now", systemdUnitName).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl enable --now: %v: %s", err, strings.TrimSpace(string(out)))
	}

	fmt.Printf("%s systemd user unit installed:\n", green("✓"))
	fmt.Printf("  unit: %s\n", path)
	fmt.Printf("  binary: %s\n", bin)
	fmt.Printf("  logs: journalctl --user -u %s\n", systemdUnitName)
	fmt.Printf("\nThe gateway will start on login.\n")
	fmt.Printf("%s If you want it to survive logout (and start on boot), enable lingering:\n", yellow("⚠"))
	fmt.Printf("    sudo loginctl enable-linger $USER\n")
	fmt.Printf("\nManage it with: sy daemon {status,uninstall,logs}\n")
	return nil
}

func uninstallSystemdUserUnit() error {
	path, err := systemdUnitPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Printf("%s No systemd unit at %s (already uninstalled)\n", yellow("⚠"), path)
		return nil
	}
	_ = exec.Command("systemctl", "--user", "disable", "--now", systemdUnitName).Run()
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove unit: %w", err)
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	fmt.Printf("%s systemd user unit disabled and removed: %s\n", green("✓"), path)
	return nil
}

func statusSystemdUserUnit() error {
	path, err := systemdUnitPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Printf("%s Not installed. Run `sy daemon install` to enable auto-start.\n", yellow("•"))
		return nil
	}
	// is-active returns non-zero when inactive — we still want to print its output.
	out, _ := exec.Command("systemctl", "--user", "is-active", systemdUnitName).CombinedOutput()
	active := strings.TrimSpace(string(out))
	if active == "active" {
		fmt.Printf("%s systemd unit active.\n", green("✓"))
	} else {
		fmt.Printf("%s systemd unit %s.\n", yellow("⚠"), active)
	}
	statusOut, _ := exec.Command("systemctl", "--user", "status", "--no-pager", "-n", "5", systemdUnitName).CombinedOutput()
	fmt.Printf("%s\n", strings.TrimSpace(string(statusOut)))
	return nil
}

func startSystemdUserUnit() error {
	path, err := systemdUnitPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("not installed: run `sy daemon install` first")
	}
	if out, err := exec.Command("systemctl", "--user", "start", systemdUnitName).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl start failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	fmt.Printf("%s Gateway service started.\n", green("✓"))
	return nil
}

func stopSystemdUserUnit() error {
	path, err := systemdUnitPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Printf("%s Not installed (nothing to stop).\n", yellow("•"))
		return nil
	}
	if out, err := exec.Command("systemctl", "--user", "stop", systemdUnitName).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl stop failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	fmt.Printf("%s Gateway service stopped (still installed; `sy daemon start` to resume).\n", green("✓"))
	return nil
}

const systemdUnitTemplate = `[Unit]
Description=Soulacy gateway
Documentation=https://github.com/vmodekurti/soulacy
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s serve
WorkingDirectory=%s
Restart=on-failure
RestartSec=10s
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=default.target
`

// ── Helpers ──────────────────────────────────────────────────────────────────

// resolveSoulacyBinary finds the absolute path of the soulacy binary the
// service should run. Strategy:
//  1. ${SOULACY_BIN} env var if set.
//  2. exec.LookPath("soulacy") — picks up whatever's on PATH.
//  3. Conventional locations: ~/.local/bin, /usr/local/bin, /opt/homebrew/bin.
//
// We deliberately do NOT use os.Args[0] because that's the `sy` binary,
// not `soulacy`. Returning a NON-absolute path would also break launchd.
func resolveSoulacyBinary() (string, error) {
	if v := os.Getenv("SOULACY_BIN"); v != "" {
		if abs, err := filepath.Abs(v); err == nil {
			return abs, nil
		}
	}
	if p, err := exec.LookPath("soulacy"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs, nil
		}
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".local", "bin", "soulacy"),
		"/usr/local/bin/soulacy",
		"/opt/homebrew/bin/soulacy",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("could not find the soulacy binary on PATH. Install it first, or set SOULACY_BIN")
}
