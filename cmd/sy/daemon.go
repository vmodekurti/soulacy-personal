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
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/soulacy/soulacy/internal/service"
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

// ── Implementation ───────────────────────────────────────────────────────────
//
// All platform behaviour lives in internal/service, which the dashboard calls
// too. These functions only turn a Status into terminal output; if they grew
// logic of their own, the browser and the terminal would start disagreeing
// about what "installed" means.

func daemonManager() *service.Manager {
	return service.New(syWorkspace())
}

// printStatus renders a Status the way the rest of `sy` prints things.
func printStatus(st service.Status) {
	switch st.State {
	case service.StateRunning:
		fmt.Printf("%s %s\n", green("✓"), st.Detail)
	case service.StateInstalled:
		fmt.Printf("%s %s\n", yellow("⚠"), st.Detail)
	case service.StateNotInstalled:
		fmt.Printf("%s %s\n", yellow("•"), st.Detail)
		fmt.Printf("  Enable it with: sy daemon install\n")
	default:
		fmt.Printf("%s %s\n", yellow("⚠"), st.Detail)
	}
	if st.UnitPath != "" {
		fmt.Printf("  unit: %s\n", st.UnitPath)
	}
	if st.Binary != "" {
		fmt.Printf("  binary: %s\n", st.Binary)
	}
	if st.LogPath != "" {
		fmt.Printf("  logs: %s\n", st.LogPath)
	}
	if st.Warning != "" {
		fmt.Printf("%s %s\n", yellow("⚠"), st.Warning)
	}
}

func daemonInstall() error {
	st, err := daemonManager().Install()
	if err != nil {
		return err
	}
	fmt.Printf("%s Soulacy will now start automatically at login.\n", green("✓"))
	printStatus(st)
	fmt.Printf("\nManage it with: sy daemon {status,stop,uninstall,logs}\n")
	return nil
}

func daemonUninstall() error {
	st, err := daemonManager().Uninstall()
	if err != nil {
		return err
	}
	fmt.Printf("%s Autostart removed. Soulacy will no longer start on its own.\n", green("✓"))
	printStatus(st)
	return nil
}

func daemonStatus() error {
	printStatus(daemonManager().Status())
	return nil
}

func daemonStart() error {
	st, err := daemonManager().Start()
	if err != nil {
		return err
	}
	fmt.Printf("%s Gateway service started.\n", green("✓"))
	printStatus(st)
	return nil
}

func daemonStop() error {
	st, err := daemonManager().Stop()
	if err != nil {
		return err
	}
	fmt.Printf("%s Gateway service stopped. It is still installed and returns at next login.\n", green("✓"))
	printStatus(st)
	return nil
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
