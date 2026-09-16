// Package service manages Soulacy's per-user autostart service — a launchd
// LaunchAgent on macOS, a systemd --user unit on Linux. Both are user-scoped:
// no sudo, no root daemon.
//
// Why this is a package rather than living in `sy daemon`: without autostart
// the gateway dies with the terminal that launched it, and every scheduled
// agent stops with it. That failure is silent and it lands exactly where a
// habit would have formed — the user schedules a morning briefing, closes the
// window, and nothing ever arrives. Someone who only ever opens the dashboard
// could neither see nor fix it, because installing the service was reachable
// only from a shell.
//
// So the logic lives here and both surfaces call it: `sy daemon` prints the
// results, the gateway returns them as JSON. Behaviour cannot drift between
// the two, which is the whole point.
package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/soulacy/soulacy/internal/config"
)

const (
	// LaunchdLabel and SystemdUnitName are the identities the service is
	// registered under. They are part of the installed footprint: changing
	// either strands the unit an older build wrote, so they stay put.
	LaunchdLabel    = "com.soulacy.soulacy"
	SystemdUnitName = "soulacy.service"
)

// State is the coarse answer to "will this start on its own?".
type State string

const (
	// StateNotInstalled means no unit file exists. The gateway is running
	// because someone started it by hand, and it stops when they stop.
	StateNotInstalled State = "not_installed"
	// StateInstalled means the unit exists but is not currently loaded or
	// active — it will come back at next login, but is not running now.
	StateInstalled State = "installed"
	// StateRunning means the unit exists and the service manager reports it
	// as loaded (launchd) or active (systemd).
	StateRunning State = "running"
	// StateUnsupported is Windows and anything else without a handler.
	StateUnsupported State = "unsupported"
)

// Status describes the autostart service. Every field is safe to show a user:
// paths and a label, never credentials.
type Status struct {
	Supported bool   `json:"supported"`
	Platform  string `json:"platform"` // launchd | systemd-user | ""
	State     State  `json:"state"`
	UnitPath  string `json:"unit_path,omitempty"`
	Binary    string `json:"binary,omitempty"`
	LogPath   string `json:"log_path,omitempty"`
	Detail    string `json:"detail,omitempty"`  // one plain sentence
	Warning   string `json:"warning,omitempty"` // a caveat that needs the user
}

// Manager installs and inspects the autostart service.
//
// run and lookBinary are fields rather than direct calls so tests can drive
// every branch without a real launchctl or systemctl on the machine. A test
// that shells out to the host's service manager would either be skipped in CI
// or, worse, install something.
type Manager struct {
	paths      config.Paths
	goos       string
	home       string
	run        func(name string, args ...string) ([]byte, error)
	lookBinary func() (string, error)
}

// New returns a Manager for the current machine.
func New(paths config.Paths) *Manager {
	home, _ := os.UserHomeDir()
	return &Manager{
		paths: paths,
		goos:  runtime.GOOS,
		home:  home,
		run: func(name string, args ...string) ([]byte, error) {
			return exec.Command(name, args...).CombinedOutput()
		},
		lookBinary: ResolveBinary,
	}
}

// ResolveBinary finds the soulacy gateway binary to run at login.
//
// The unit file records an absolute path, so this must resolve to the binary
// the user actually installed rather than whatever happens to be on a future
// PATH. SOULACY_BIN wins when set, which is what makes a non-standard install
// (or a test) able to point somewhere specific.
func ResolveBinary() (string, error) {
	if v := strings.TrimSpace(os.Getenv("SOULACY_BIN")); v != "" {
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
	for _, c := range []string{
		filepath.Join(home, ".local", "bin", "soulacy"),
		"/usr/local/bin/soulacy",
		"/opt/homebrew/bin/soulacy",
	} {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("could not find the soulacy binary on PATH — install it first, or set SOULACY_BIN")
}

// Platform names the service manager this OS uses, or "" when unsupported.
func (m *Manager) Platform() string {
	switch m.goos {
	case "darwin":
		return "launchd"
	case "linux":
		return "systemd-user"
	default:
		return ""
	}
}

// UnitPath is where the unit file lives for this platform.
func (m *Manager) UnitPath() (string, error) {
	if m.home == "" {
		return "", fmt.Errorf("cannot determine the home directory")
	}
	switch m.goos {
	case "darwin":
		return filepath.Join(m.home, "Library", "LaunchAgents", LaunchdLabel+".plist"), nil
	case "linux":
		return filepath.Join(m.home, ".config", "systemd", "user", SystemdUnitName), nil
	default:
		return "", m.unsupported()
	}
}

// LogPath is where the service writes stdout. systemd sends output to the
// journal, so this is only meaningful on launchd; callers show it when set.
func (m *Manager) LogPath() string {
	if m.goos != "darwin" || m.paths.Logs == "" {
		return ""
	}
	return filepath.Join(m.paths.Logs, "soulacy.log")
}

func (m *Manager) unsupported() error {
	return fmt.Errorf("autostart is not supported on %s — macOS and Linux only", m.goos)
}

// Status reports whether the service is installed and running. It never
// returns an error for the ordinary "not installed" case: not having autostart
// is a state to report, not a failure.
func (m *Manager) Status() Status {
	st := Status{Supported: m.Platform() != "", Platform: m.Platform(), State: StateUnsupported}
	if !st.Supported {
		st.Detail = fmt.Sprintf("Soulacy cannot start automatically on %s.", m.goos)
		return st
	}
	path, err := m.UnitPath()
	if err != nil {
		st.Detail = err.Error()
		return st
	}
	st.UnitPath = path
	st.LogPath = m.LogPath()

	if _, err := os.Stat(path); os.IsNotExist(err) {
		st.State = StateNotInstalled
		st.Detail = "Soulacy will not start on its own. Scheduled agents stop when the gateway does."
		return st
	}

	// The unit exists. Ask the service manager whether it is actually up,
	// because a present unit file and a running service are different claims
	// and the difference is what the user needs to know.
	switch m.goos {
	case "darwin":
		if _, err := m.run("launchctl", "list", LaunchdLabel); err != nil {
			st.State = StateInstalled
			st.Detail = "Installed but not currently loaded. It will start at your next login."
			return st
		}
		st.State = StateRunning
		st.Detail = "Soulacy starts automatically at login and is running now."
	case "linux":
		out, _ := m.run("systemctl", "--user", "is-active", SystemdUnitName)
		if strings.TrimSpace(string(out)) == "active" {
			st.State = StateRunning
			st.Detail = "Soulacy starts automatically at login and is running now."
		} else {
			st.State = StateInstalled
			st.Detail = "Installed but not active. It will start at your next login."
		}
	}
	return st
}

// Install writes the unit file and loads it, then reports the resulting
// status. It is idempotent: installing over an existing unit replaces it,
// which is what makes this safe to offer as a plain button.
func (m *Manager) Install() (Status, error) {
	if m.Platform() == "" {
		return m.Status(), m.unsupported()
	}
	bin, err := m.lookBinary()
	if err != nil {
		return m.Status(), err
	}
	path, err := m.UnitPath()
	if err != nil {
		return m.Status(), err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return m.Status(), fmt.Errorf("create service directory: %w", err)
	}

	switch m.goos {
	case "darwin":
		body := fmt.Sprintf(launchdPlistTemplate, LaunchdLabel, bin, m.home, m.paths.Logs, m.paths.Logs)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return m.Status(), fmt.Errorf("write LaunchAgent: %w", err)
		}
		// Unload any earlier copy first. This fails harmlessly when nothing
		// was loaded, and skipping it leaves the old job running after an
		// upgrade rewrites the plist.
		_, _ = m.run("launchctl", "unload", path)
		if out, err := m.run("launchctl", "load", "-w", path); err != nil {
			return m.Status(), fmt.Errorf("launchctl load failed: %v: %s", err, strings.TrimSpace(string(out)))
		}
	case "linux":
		body := fmt.Sprintf(systemdUnitTemplate, bin, m.paths.Root)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return m.Status(), fmt.Errorf("write systemd unit: %w", err)
		}
		if out, err := m.run("systemctl", "--user", "daemon-reload"); err != nil {
			return m.Status(), fmt.Errorf("systemctl daemon-reload: %v: %s", err, strings.TrimSpace(string(out)))
		}
		if out, err := m.run("systemctl", "--user", "enable", "--now", SystemdUnitName); err != nil {
			return m.Status(), fmt.Errorf("systemctl enable --now: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}

	st := m.Status()
	st.Binary = bin
	if m.goos == "linux" {
		// A systemd --user service stops at logout unless lingering is on,
		// and enabling it needs a password we must not ask for. Saying so is
		// the only honest option: a user who logs out and finds nothing ran
		// would otherwise blame the product.
		st.Warning = "To keep running after you log out, enable lingering: sudo loginctl enable-linger $USER"
	}
	return st, nil
}

// Uninstall stops the service and removes the unit file. Removing something
// that is not there is a success, not an error — the caller asked for a state,
// and that state already holds.
func (m *Manager) Uninstall() (Status, error) {
	if m.Platform() == "" {
		return m.Status(), m.unsupported()
	}
	path, err := m.UnitPath()
	if err != nil {
		return m.Status(), err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return m.Status(), nil
	}
	switch m.goos {
	case "darwin":
		_, _ = m.run("launchctl", "unload", path)
	case "linux":
		_, _ = m.run("systemctl", "--user", "disable", "--now", SystemdUnitName)
	}
	if err := os.Remove(path); err != nil {
		return m.Status(), fmt.Errorf("remove unit file: %w", err)
	}
	if m.goos == "linux" {
		_, _ = m.run("systemctl", "--user", "daemon-reload")
	}
	return m.Status(), nil
}

// Start starts an installed service without reinstalling it.
func (m *Manager) Start() (Status, error) {
	if m.Platform() == "" {
		return m.Status(), m.unsupported()
	}
	path, err := m.UnitPath()
	if err != nil {
		return m.Status(), err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return m.Status(), fmt.Errorf("autostart is not installed yet")
	}
	switch m.goos {
	case "darwin":
		if out, err := m.run("launchctl", "load", "-w", path); err != nil {
			return m.Status(), fmt.Errorf("launchctl load failed: %v: %s", err, strings.TrimSpace(string(out)))
		}
	case "linux":
		if out, err := m.run("systemctl", "--user", "start", SystemdUnitName); err != nil {
			return m.Status(), fmt.Errorf("systemctl start failed: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	return m.Status(), nil
}

// Stop stops the service but leaves it installed, so it returns at next login.
func (m *Manager) Stop() (Status, error) {
	if m.Platform() == "" {
		return m.Status(), m.unsupported()
	}
	path, err := m.UnitPath()
	if err != nil {
		return m.Status(), err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return m.Status(), nil
	}
	switch m.goos {
	case "darwin":
		// unload stops the job even with KeepAlive set; the plist stays, so
		// status still reports it installed and Start can bring it back.
		if out, err := m.run("launchctl", "unload", path); err != nil {
			return m.Status(), fmt.Errorf("launchctl unload failed: %v: %s", err, strings.TrimSpace(string(out)))
		}
	case "linux":
		if out, err := m.run("systemctl", "--user", "stop", SystemdUnitName); err != nil {
			return m.Status(), fmt.Errorf("systemctl stop failed: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	return m.Status(), nil
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

const systemdUnitTemplate = `[Unit]
Description=Soulacy gateway
Documentation=https://github.com/vmodekurti/soulacy-personal
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
