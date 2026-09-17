package gateway

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/mcp"
)

// fakeDeployment describes a deployment instead of describing the machine the
// test happens to run on.
type fakeDeployment struct {
	env      map[string]string
	bins     map[string]string
	versions map[string]string
	files    map[string][]string
	euid     int
	docker   bool
}

func (f fakeDeployment) probes() deploymentProbes {
	return deploymentProbes{
		lookPath: func(bin string) (string, error) {
			if p, ok := f.bins[bin]; ok {
				return p, nil
			}
			return "", errors.New("not found")
		},
		version: func(_ context.Context, bin string, _ ...string) (string, error) {
			if v, ok := f.versions[bin]; ok {
				return v, nil
			}
			return "", errors.New("no version")
		},
		glob:     func(pattern string) ([]string, error) { return f.files[pattern], nil },
		euid:     func() int { return f.euid },
		env:      func(k string) string { return f.env[k] },
		inDocker: func() bool { return f.docker },
	}
}

func capByID(t *testing.T, rep deploymentReport, id string) deploymentCapability {
	t.Helper()
	for _, c := range rep.Capabilities {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no capability %q in report", id)
	return deploymentCapability{}
}

func reportFor(t *testing.T, f fakeDeployment) deploymentReport {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("server:\n  api_key: secret\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	return s.deploymentDoctorWith(context.Background(), f.probes())
}

// The case that started this: a platform with no shell the user can reach.
// Every limit has to name the way around it, or the report is just a list of
// things that do not work.
func TestPaaSDeploymentReportsItsLimitsWithWorkarounds(t *testing.T) {
	rep := reportFor(t, fakeDeployment{
		env:      map[string]string{"RAILWAY_ENVIRONMENT": "production"},
		bins:     map[string]string{"node": "/usr/bin/node"},
		versions: map[string]string{"/usr/bin/node": "v18.20.4"},
		euid:     1000,
	})

	if rep.Platform != "Railway" || rep.PlatformKind != "paas" {
		t.Errorf("platform = %q/%q, want Railway/paas — naming the host is what explains the limits",
			rep.Platform, rep.PlatformKind)
	}

	shell := capByID(t, rep, "agent_shell")
	if shell.Available {
		t.Error("no agent holds the system grant, so shell must be reported unavailable")
	}
	if !strings.Contains(shell.Workaround, "package_install") {
		t.Errorf("the shell limit must point at the way that does work; got %q", shell.Workaround)
	}

	pkgs := capByID(t, rep, "system_packages")
	if pkgs.Available {
		t.Error("an unprivileged process cannot install system packages")
	}
	if !strings.Contains(pkgs.Workaround, "image") {
		t.Errorf("the only fix for a missing system package is the image; got %q", pkgs.Workaround)
	}

	// Every unavailable capability owes the user an answer.
	for _, c := range rep.Capabilities {
		if !c.Available && strings.TrimSpace(c.Workaround) == "" {
			t.Errorf("capability %q is unavailable with no workaround — that is a complaint, not an answer", c.ID)
		}
	}
}

// A browser needs libraries that need root. An unprivileged deployment without
// them can never drive a local browser, so the report must say so and point at
// the remote-browser route rather than implying a download would fix it.
func TestBrowserWithoutLibrariesPointsAtCDP(t *testing.T) {
	rep := reportFor(t, fakeDeployment{
		env:  map[string]string{"FLY_APP_NAME": "soulacy"},
		euid: 1000,
	})
	browser := capByID(t, rep, "browser_automation")
	if browser.Available {
		t.Fatal("no libraries means no local browser")
	}
	if !strings.Contains(browser.Workaround, "cdp-endpoint") {
		t.Errorf("the workaround must name the remote-browser route; got %q", browser.Workaround)
	}
	// It must offer the route that works without root: the libraries do not
	// have to be installed as packages, only found.
	if !strings.Contains(browser.Workaround, "bundle") {
		t.Errorf("it must offer the downloadable bundle; got %q", browser.Workaround)
	}
}

// Libraries present but no browser yet is a different answer: here a download
// genuinely does fix it, and it belongs on the volume.
func TestBrowserWithLibrariesButNoBrowserSaysInstallOne(t *testing.T) {
	rep := reportFor(t, fakeDeployment{
		env:   map[string]string{"PLAYWRIGHT_BROWSERS_PATH": "/data/browsers"},
		files: map[string][]string{"/usr/lib/*/libnss3.so*": {"/usr/lib/x86_64-linux-gnu/libnss3.so"}},
		euid:  1000,
	})
	browser := capByID(t, rep, "browser_automation")
	if browser.Available {
		t.Fatal("the libraries are there but no browser is installed")
	}
	if !strings.Contains(browser.Detail, "/data/browsers") {
		t.Errorf("the detail should name where a browser would go; got %q", browser.Detail)
	}
	if !strings.Contains(browser.Workaround, "redeploy") {
		t.Errorf("it should say the install survives a redeploy; got %q", browser.Workaround)
	}
}

// Everything present: the report says ready and names the binary, so a user
// who is told "browser automation works" can see what it is using.
func TestFullyEquippedBrowserIsReported(t *testing.T) {
	rep := reportFor(t, fakeDeployment{
		env: map[string]string{"PLAYWRIGHT_BROWSERS_PATH": "/data/browsers"},
		files: map[string][]string{
			"/usr/lib/*/libnss3.so*":                         {"/usr/lib/x86_64-linux-gnu/libnss3.so"},
			"/data/browsers/chromium-*/chrome-linux*/chrome": {"/data/browsers/chromium-1244/chrome-linux64/chrome"},
		},
		euid: 1000,
	})
	browser := capByID(t, rep, "browser_automation")
	if !browser.Available {
		t.Fatalf("libraries and a browser are both present: %+v", browser)
	}
	if !strings.Contains(browser.Detail, "chromium-1244") {
		t.Errorf("detail should name the browser in use; got %q", browser.Detail)
	}
}

// A runtime that is present but too old is the failure that cost an evening:
// Node 18 starts a server needing 20, which exits before the handshake and
// reaches the gateway as a closed pipe with no mention of Node. The report
// cannot fix that, but it can put the version in front of the user.
func TestRuntimeVersionsAreReported(t *testing.T) {
	rep := reportFor(t, fakeDeployment{
		bins:     map[string]string{"node": "/usr/bin/node", "python3": "/usr/bin/python3"},
		versions: map[string]string{"/usr/bin/node": "v18.20.4", "/usr/bin/python3": "Python 3.12.1"},
		euid:     1000,
	})
	node := capByID(t, rep, "node_runtime")
	if !node.Available || !strings.Contains(node.Detail, "v18.20.4") {
		t.Errorf("node capability should report its version; got %+v", node)
	}
	py := capByID(t, rep, "python_runtime")
	if !py.Available || !strings.Contains(py.Detail, "3.12.1") {
		t.Errorf("python capability should report its version; got %+v", py)
	}
}

// A missing runtime is reported as missing rather than as a version we could
// not read — those are different problems with different answers.
func TestMissingRuntimeIsNotAVersionProblem(t *testing.T) {
	rep := reportFor(t, fakeDeployment{euid: 1000})
	node := capByID(t, rep, "node_runtime")
	if node.Available {
		t.Fatal("node is not installed in this deployment")
	}
	if !strings.Contains(node.Detail, "not installed") {
		t.Errorf("detail = %q, want it to say the runtime is absent", node.Detail)
	}
}

// A laptop is not a platform, and saying "Railway" there would be a lie. The
// grant is also reported when it exists, with who holds it.
func TestSelfHostedWithShellGrant(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("server:\n  api_key: secret\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	s.cfg.Runtime.AllowSystemAgents = []string{"system"}

	rep := s.deploymentDoctorWith(context.Background(), fakeDeployment{euid: 0}.probes())
	if rep.PlatformKind != "host" {
		t.Errorf("platform kind = %q, want host", rep.PlatformKind)
	}
	shell := capByID(t, rep, "agent_shell")
	if !shell.Available || !strings.Contains(shell.Detail, "system") {
		t.Errorf("the grant should be reported with who holds it; got %+v", shell)
	}
	if pkgs := capByID(t, rep, "system_packages"); !pkgs.Available {
		t.Error("running as root, system packages are installable")
	}
}

// The workspace is where installs land. If it is not writable nothing installs
// at all, which is worth saying before someone tries.
func TestWorkspaceIsReportedAsThePlaceInstallsLand(t *testing.T) {
	rep := reportFor(t, fakeDeployment{euid: 1000})
	ws := capByID(t, rep, "persistent_workspace")
	if !ws.Available {
		t.Fatalf("a temp dir is writable: %+v", ws)
	}
	if !strings.Contains(ws.Detail, "volume") {
		t.Errorf("the detail should tell the user to mount it; got %q", ws.Detail)
	}
}

// A deployment driving a browser over CDP can browse, and must not be told it
// cannot just because there is no Chromium on disk. This is the route a
// platform without shell access is meant to take.
func TestRemoteBrowserCountsAsBrowserAutomation(t *testing.T) {
	out := browserCapability(fakeDeployment{euid: 1000}.probes(), false, false, "playwright-remote")
	if !out.Available {
		t.Fatal("a configured remote browser means browser automation works")
	}
	if !strings.Contains(out.Detail, "playwright-remote") {
		t.Errorf("the detail should name the server in use; got %q", out.Detail)
	}
	if out.Workaround != "" {
		t.Errorf("nothing to work around when it already works; got %q", out.Workaround)
	}
}

// --cdp-endpoint is what makes a stdio server remote. It is usually paired
// with --headless, and classifying it as headless would hide the fact that
// there is no local browser involved at all.
func TestCDPEndpointIsRemoteNotHeadless(t *testing.T) {
	srv := mcp.ServerStatus{
		ID: "playwright-mcp", Transport: "stdio", Command: "node",
		Args: []string{"cli.js", "--headless", "--cdp-endpoint", "wss://browser.example/cdp"},
	}
	if got := browserServerMode(srv); got != "remote" {
		t.Errorf("mode = %q, want remote — the browser is somewhere else", got)
	}

	local := mcp.ServerStatus{
		ID: "playwright-mcp", Transport: "stdio", Command: "node",
		Args: []string{"cli.js", "--headless", "--executable-path", "/browsers/chromium/chrome"},
	}
	if got := browserServerMode(local); got != "headless" {
		t.Errorf("mode = %q, want headless — this one runs its own browser", got)
	}
}

// Libraries can arrive two ways. Once a bundle is installed on the volume, a
// deployment with none in the image is no longer told it cannot browse.
func TestAnInstalledBundleCountsAsLibraries(t *testing.T) {
	probes := fakeDeployment{
		env: map[string]string{"PLAYWRIGHT_BROWSERS_PATH": "/data/browsers"},
		files: map[string][]string{
			"/data/browsers/chromium-*/chrome-linux*/chrome": {"/data/browsers/chromium-1244/chrome-linux64/chrome"},
		},
		euid: 1000,
	}.probes()

	// No system libraries and no bundle: the bundle is offered.
	out := browserCapability(probes, false, false, "")
	if out.Available || !strings.Contains(out.Workaround, "bundle") {
		t.Errorf("without libraries it should offer the bundle: %+v", out)
	}

	// The same deployment with the bundle installed can drive a browser.
	out = browserCapability(probes, false, true, "")
	if !out.Available {
		t.Errorf("an installed bundle supplies the libraries: %+v", out)
	}
}
