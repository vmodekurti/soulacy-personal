package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/config"
)

// newTestManager builds a Manager rooted in a temp home, for a named OS, with
// a recording fake runner. Nothing here touches the real launchctl/systemctl:
// a test that did would be unrunnable in CI and, on a developer's machine,
// would install a service.
func newTestManager(t *testing.T, goos string) (*Manager, *[]string) {
	t.Helper()
	home := t.TempDir()
	ws := config.Paths{Root: filepath.Join(home, "soulspace"), Logs: filepath.Join(home, "soulspace", "logs")}
	if err := os.MkdirAll(ws.Logs, 0o755); err != nil {
		t.Fatalf("make logs dir: %v", err)
	}
	var calls []string
	m := &Manager{
		paths: ws,
		goos:  goos,
		home:  home,
		run: func(name string, args ...string) ([]byte, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return []byte("active"), nil
		},
		lookBinary: func() (string, error) { return "/usr/local/bin/soulacy", nil },
	}
	return m, &calls
}

// The state a brand-new install is in, and the one that silently breaks
// scheduled agents. It must report cleanly rather than erroring.
func TestNotInstalledIsAReportedStateNotAnError(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		m, _ := newTestManager(t, goos)
		st := m.Status()
		if st.State != StateNotInstalled {
			t.Fatalf("%s: state = %q, want not_installed", goos, st.State)
		}
		if !st.Supported {
			t.Fatalf("%s should be supported", goos)
		}
		if st.Detail == "" {
			t.Fatal("a user needs to be told what not-installed costs them")
		}
		if st.UnitPath == "" {
			t.Fatal("status should say where the unit would go")
		}
	}
}

func TestUnsupportedPlatformSaysSoWithoutPretendingToWork(t *testing.T) {
	m, _ := newTestManager(t, "windows")
	st := m.Status()
	if st.Supported || st.State != StateUnsupported {
		t.Fatalf("windows should be unsupported; got %+v", st)
	}
	if _, err := m.Install(); err == nil {
		t.Fatal("installing on an unsupported OS must fail loudly")
	}
}

func TestInstallWritesTheUnitAndLoadsIt(t *testing.T) {
	m, calls := newTestManager(t, "darwin")
	st, err := m.Install()
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	path, _ := m.UnitPath()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("unit file not written: %v", err)
	}
	// The absolute binary path is the point of the unit: a future PATH must
	// not decide what runs at login.
	if !strings.Contains(string(body), "/usr/local/bin/soulacy") {
		t.Error("plist should record the resolved absolute binary path")
	}
	if !strings.Contains(string(body), LaunchdLabel) {
		t.Error("plist should carry the launchd label")
	}
	if !strings.Contains(string(body), "<key>RunAtLoad</key>") {
		t.Error("the whole point is starting at login")
	}
	joined := strings.Join(*calls, "|")
	if !strings.Contains(joined, "launchctl unload") {
		t.Error("an earlier copy must be unloaded first, or an upgrade leaves the old job running")
	}
	if !strings.Contains(joined, "launchctl load -w") {
		t.Error("the new unit must be loaded")
	}
	if st.Binary != "/usr/local/bin/soulacy" {
		t.Errorf("status should report the binary it installed; got %q", st.Binary)
	}
}

func TestLinuxInstallEnablesTheUnitAndWarnsAboutLingering(t *testing.T) {
	m, calls := newTestManager(t, "linux")
	st, err := m.Install()
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	joined := strings.Join(*calls, "|")
	for _, want := range []string{"systemctl --user daemon-reload", "systemctl --user enable --now soulacy.service"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q; calls were %v", want, *calls)
		}
	}
	// A --user service dies at logout without lingering, and enabling that
	// needs a password we will not ask for. Silence here would look like a
	// product bug the first time the user logs out.
	if !strings.Contains(st.Warning, "enable-linger") {
		t.Errorf("linux install must warn about lingering; got %q", st.Warning)
	}
}

func TestInstalledButNotRunningIsDistinguishedFromRunning(t *testing.T) {
	m, _ := newTestManager(t, "darwin")
	if _, err := m.Install(); err != nil {
		t.Fatalf("install: %v", err)
	}
	// launchctl list succeeding means loaded.
	if st := m.Status(); st.State != StateRunning {
		t.Fatalf("state = %q, want running", st.State)
	}
	// Now make launchctl list fail, as it does when the plist exists but
	// nothing is loaded. The unit is still installed.
	m.run = func(name string, args ...string) ([]byte, error) {
		return []byte("Could not find service"), fmt.Errorf("exit status 113")
	}
	st := m.Status()
	if st.State != StateInstalled {
		t.Fatalf("state = %q, want installed", st.State)
	}
	if !strings.Contains(st.Detail, "next login") {
		t.Errorf("detail should tell the user it comes back at login; got %q", st.Detail)
	}
}

func TestUninstallRemovesTheUnitAndIsSafeToRepeat(t *testing.T) {
	m, _ := newTestManager(t, "darwin")
	if _, err := m.Install(); err != nil {
		t.Fatalf("install: %v", err)
	}
	path, _ := m.UnitPath()
	if _, err := m.Uninstall(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("unit file should be gone")
	}
	// Asking for a state that already holds is success, not an error.
	if _, err := m.Uninstall(); err != nil {
		t.Fatalf("second uninstall should be a no-op, got %v", err)
	}
}

func TestStartRefusesWhenNothingIsInstalled(t *testing.T) {
	m, _ := newTestManager(t, "darwin")
	if _, err := m.Start(); err == nil {
		t.Fatal("starting a service that was never installed must fail with a reason")
	}
}

func TestStopLeavesTheUnitInstalledSoItReturnsAtLogin(t *testing.T) {
	m, _ := newTestManager(t, "linux")
	if _, err := m.Install(); err != nil {
		t.Fatalf("install: %v", err)
	}
	path, _ := m.UnitPath()
	if _, err := m.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("stop must not remove the unit; that is what uninstall is for")
	}
}

func TestInstallFailsClearlyWhenTheBinaryCannotBeFound(t *testing.T) {
	m, _ := newTestManager(t, "darwin")
	m.lookBinary = func() (string, error) { return "", fmt.Errorf("could not find the soulacy binary on PATH") }
	if _, err := m.Install(); err == nil || !strings.Contains(err.Error(), "soulacy binary") {
		t.Fatalf("want a binary-not-found error, got %v", err)
	}
	path, _ := m.UnitPath()
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("a failed install must not leave a unit file behind")
	}
}

// Status is shown in a browser, so it must never carry anything secret.
func TestStatusCarriesNoCredentials(t *testing.T) {
	m, _ := newTestManager(t, "darwin")
	st, err := m.Install()
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	blob := strings.ToLower(fmt.Sprintf("%+v", st))
	for _, banned := range []string{"api_key", "sy_", "token", "secret", "password"} {
		if strings.Contains(blob, banned) {
			t.Errorf("status leaked %q: %+v", banned, st)
		}
	}
}
