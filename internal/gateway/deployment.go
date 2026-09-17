// deployment.go — what this particular deployment can and cannot do.
//
// Soulacy runs in places with very different powers. A laptop install can
// apt-get anything and open a shell. A container on a platform like Railway or
// Fly has no shell the user can reach, runs unprivileged, and its filesystem
// resets on every deploy except the mounted volume.
//
// Today that difference is discovered the hard way: a user follows an install
// guide written for a laptop, the step that needs a shell fails, and nothing
// says "this deployment was never going to be able to do that — here is what
// to do instead". The alternative to telling them is bundling every heavy
// dependency into the image on the chance somebody wants it, which makes every
// install pay for what few of them use.
//
// So the gateway reports its own limits, and every limit names the way around
// it. Honest capability reporting is cheaper than a browser nobody asked for.
package gateway

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/browserlibs"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/platform"
)

// deploymentCapability is one thing this deployment can or cannot do.
//
// Workaround is the important field. "No shell access" is a complaint; "no
// shell access, install MCP servers with package_install instead" is an
// answer, and it is the difference between a user who continues and a user who
// concludes the product does not work here.
type deploymentCapability struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Available  bool   `json:"available"`
	Detail     string `json:"detail"`
	Workaround string `json:"workaround,omitempty"`
}

// deploymentReport is the whole answer, including where it is running.
type deploymentReport struct {
	Platform     string                 `json:"platform"`
	PlatformKind string                 `json:"platform_kind"` // "paas" | "container" | "host"
	Workspace    string                 `json:"workspace"`
	Capabilities []deploymentCapability `json:"capabilities"`
}

// deploymentProbes are the questions this file asks of the machine. They are
// injectable so the tests can describe a deployment rather than describing
// whatever machine happens to run them — a test that asserts "node is present"
// on a developer's laptop proves nothing about a container.
type deploymentProbes struct {
	lookPath func(string) (string, error)
	version  func(ctx context.Context, bin string, args ...string) (string, error)
	glob     func(string) ([]string, error)
	euid     func() int
	env      func(string) string
	inDocker func() bool
}

func defaultDeploymentProbes() deploymentProbes {
	return deploymentProbes{
		lookPath: exec.LookPath,
		version:  probeVersion,
		glob:     filepath.Glob,
		euid:     os.Geteuid,
		env:      os.Getenv,
		inDocker: func() bool {
			if _, err := os.Stat("/.dockerenv"); err == nil {
				return true
			}
			return false
		},
	}
}

// deploymentProbeTimeout bounds a local version check. It is deliberately not
// part of the engine's timeout hierarchy: that hierarchy governs LLM, step,
// run and tool deadlines on the request path, and this is a diagnostics
// endpoint asking a local binary to print its version. It is named rather than
// inlined so it stays a reviewable number — the same shape as
// providerProbeTimeout next door in doctor.go.
var deploymentProbeTimeout = 3 * time.Second

// probeVersion runs a binary's version flag. It is the gateway asking about
// its own environment, not an agent running a command, so it needs no
// approval — but it still gets a short deadline, because a hung probe would
// hang the doctor page that is supposed to explain what is wrong.
func probeVersion(ctx context.Context, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, deploymentProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (s *Server) deploymentDoctor() deploymentReport {
	return s.deploymentDoctorWith(context.Background(), defaultDeploymentProbes())
}

func (s *Server) deploymentDoctorWith(ctx context.Context, p deploymentProbes) deploymentReport {
	info := platform.DetectWith(p.env, p.inDocker)
	name, kind := info.Name, string(info.Kind)
	workspace := ""
	if s.cfgPath != "" {
		workspace = filepath.Dir(s.cfgPath)
	}
	rep := deploymentReport{Platform: name, PlatformKind: kind, Workspace: workspace}

	// ── Shell ────────────────────────────────────────────────────────────
	// Not "is there a shell on the box" — there always is. The question a
	// user needs answered is whether an agent can run one, which is the
	// server-level grant, not the operating system.
	shellGranted := len(s.cfg.Runtime.AllowSystemAgents) > 0
	shell := deploymentCapability{
		ID: "agent_shell", Name: "Agents can run shell commands", Available: shellGranted,
	}
	if shellGranted {
		shell.Detail = "granted to: " + strings.Join(s.cfg.Runtime.AllowSystemAgents, ", ")
	} else if reason := config.ShellGrantWithheldReason; reason != "" {
		// They configured it and it was refused. Saying "no agent holds the
		// grant" here would be true and useless — they know they set one.
		shell.Detail = "withheld on this platform"
		shell.Workaround = reason
	} else {
		shell.Detail = "no agent holds the system grant, so shell_exec, run_script and write_file are not offered"
		shell.Workaround = "install Skills and MCP servers with package_install, which works without the grant and asks for approval each time; or set runtime.allow_system_agents to a list of agent IDs"
	}
	rep.Capabilities = append(rep.Capabilities, shell)

	// ── Root ─────────────────────────────────────────────────────────────
	// This is the one nobody can work around at runtime. A dependency that
	// needs a system package has to be in the image, decided by whoever
	// builds it — which is why the answer here is "rebuild", never "run".
	root := p.euid() == 0
	rootCap := deploymentCapability{
		ID: "system_packages", Name: "System packages can be installed", Available: root,
	}
	if root {
		rootCap.Detail = "running as root, so apt-get and equivalents are available"
	} else {
		rootCap.Detail = "running unprivileged, so no system libraries can be added at runtime"
		rootCap.Workaround = "anything needing a system package must be in the image — add it to the Dockerfile and redeploy"
	}
	rep.Capabilities = append(rep.Capabilities, rootCap)

	// ── Persistent storage ───────────────────────────────────────────────
	persist := deploymentCapability{ID: "persistent_workspace", Name: "Workspace survives a redeploy"}
	if workspace == "" {
		persist.Detail = "no config path is known, so the workspace cannot be located"
		persist.Workaround = "set SOULACY_CONFIG_PATH or mount a volume at ~/.soulacy"
	} else if err := probeWritable(workspace); err != nil {
		persist.Detail = "workspace at " + workspace + " is not writable: " + err.Error()
		persist.Workaround = "mount a writable volume at " + workspace
	} else {
		persist.Available = true
		persist.Detail = "installs land in " + workspace + "; mount this path as a volume so they survive a redeploy"
	}
	rep.Capabilities = append(rep.Capabilities, persist)

	// ── Runtimes ─────────────────────────────────────────────────────────
	// MCP servers are overwhelmingly Node or Python programs, and the usual
	// failure is not "missing" but "too old": a Node 18 runtime starts a
	// server that needs 20, which exits before the handshake and surfaces as
	// a closed pipe with no mention of Node.
	rep.Capabilities = append(rep.Capabilities,
		runtimeCapability(ctx, p, "node_runtime", "Node-based MCP servers", "node", "--version"),
		runtimeCapability(ctx, p, "python_runtime", "Python-based MCP servers", "python3", "--version"),
	)

	// ── Browser ──────────────────────────────────────────────────────────
	// A remote browser answers this question as well as a local one, and a
	// deployment already using one should not be told it cannot browse.
	remote := ""
	if s.mcp != nil {
		for _, b := range s.browserAutomationServers() {
			if b.Mode == "remote" {
				remote = b.ID
				break
			}
		}
	}
	_, bundle := browserlibs.Installed(workspace)
	rep.Capabilities = append(rep.Capabilities, browserCapability(p, root, bundle, remote))

	return rep
}

func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".soulacy-write-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

func runtimeCapability(ctx context.Context, p deploymentProbes, id, name, bin string, args ...string) deploymentCapability {
	out := deploymentCapability{ID: id, Name: name}
	path, err := p.lookPath(bin)
	if err != nil {
		out.Detail = bin + " is not installed"
		out.Workaround = "add " + bin + " to the image, or choose an MCP server that does not need it"
		return out
	}
	out.Available = true
	if v, err := p.version(ctx, path, args...); err == nil && v != "" {
		out.Detail = v + " at " + path
	} else {
		out.Detail = "present at " + path + " (version unknown)"
	}
	return out
}

// browserCapability answers the question that started this file.
//
// A browser has two halves, and only one of them can be installed at runtime:
// the shared libraries need root, the browser itself does not. So an
// unprivileged deployment without those libraries can never drive a local
// browser, however many times it downloads one — and the honest answer is to
// point at a remote browser over CDP rather than to bundle ~300MB of Chromium
// into every image on the chance it gets used.
func browserCapability(p deploymentProbes, root, bundleInstalled bool, remoteServer string) deploymentCapability {
	out := deploymentCapability{ID: "browser_automation", Name: "Browser automation"}

	// Already driving a browser elsewhere: nothing local is required, and
	// none of the advice below applies.
	if remoteServer != "" {
		out.Available = true
		out.Detail = "using a remote browser over CDP (" + remoteServer + "); no local browser needed"
		return out
	}
	const cdp = "point a browser MCP server at a remote browser with --cdp-endpoint; no local browser, libraries or download needed"

	libs, _ := p.glob("/usr/lib/*/libnss3.so*")
	if len(libs) == 0 {
		libs, _ = p.glob("/usr/lib/libnss3.so*")
	}
	if len(libs) == 0 && !bundleInstalled {
		// Not a dead end any more: the libraries do not have to be installed
		// as packages, only found, so a bundle on the volume does the job
		// without root. The image ships without them because most installs
		// never drive a browser.
		out.Detail = "Chromium's shared libraries are not present"
		out.Workaround = "install the library bundle (about 12MB, onto the volume, so it survives a redeploy), or " + cdp
		return out
	}

	browsers := p.env("PLAYWRIGHT_BROWSERS_PATH")
	if browsers == "" {
		out.Detail = "Chromium's libraries are present, but no browser directory is configured"
		out.Workaround = "set PLAYWRIGHT_BROWSERS_PATH to a path on the mounted volume, then install a browser — or " + cdp
		return out
	}
	found, _ := p.glob(filepath.Join(browsers, "chromium-*", "chrome-linux*", "chrome"))
	if len(found) == 0 {
		out.Detail = "Chromium's libraries are present; no browser is installed in " + browsers
		out.Workaround = "install one into that directory (it is on the volume, so it survives a redeploy) — or " + cdp
		return out
	}
	out.Available = true
	out.Detail = "ready: " + found[0]
	return out
}
