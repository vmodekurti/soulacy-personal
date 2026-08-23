package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/sandbox"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// PrivilegedCommand is the only process-execution request shape available to
// privileged builtins. Implementations must enforce isolation themselves;
// failure to establish it is an error, never a host-process fallback.
type PrivilegedCommand struct {
	Argv       []string
	WorkingDir string
	Env        []string

	// Workspace is the ONLY host tree the runner may expose to the command.
	// MU-021: it is stamped by runPrivilegedCommand from the run's workspace,
	// never by the individual builtins — a per-call-site field is a scoping
	// step a future builtin can forget, and forgetting it here means mounting
	// every tenant's scratch space into one container.
	//
	// A runner treats it as a NARROWING of its own configured workspace, never
	// a replacement: a value outside that is refused.
	Workspace string
}

type PrivilegedCommandRunner interface {
	Run(context.Context, PrivilegedCommand) (string, error)
	Mode() string
}

type isolationReadyKey struct{}

// denyPrivilegedRunner is the Engine default. Embedders must make an explicit
// choice; merely constructing an Engine does not grant host execution.
type denyPrivilegedRunner struct{}

func (denyPrivilegedRunner) Mode() string { return "unavailable" }
func (denyPrivilegedRunner) Run(context.Context, PrivilegedCommand) (string, error) {
	return "", fmt.Errorf("privileged isolation is unavailable; refusing host execution")
}

// HostPrivilegedRunner is the explicitly unsafe compatibility mode. It exists
// only behind runtime.sandbox.mode=unsandboxed and emits a startup warning.
type HostPrivilegedRunner struct{}

func (HostPrivilegedRunner) Mode() string { return "unsandboxed" }
func (HostPrivilegedRunner) Run(ctx context.Context, req PrivilegedCommand) (string, error) {
	if len(req.Argv) == 0 {
		return "", fmt.Errorf("empty privileged command")
	}
	cmd := exec.CommandContext(ctx, req.Argv[0], req.Argv[1:]...)
	cmd.Dir, cmd.Env = req.WorkingDir, req.Env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// DockerPrivilegedRunner uses one disposable, read-only, unnetworked container
// per command. The workspace is the only host mount; gateway config, process
// environment, credentials and the metadata network are absent.
type DockerPrivilegedRunner struct {
	// Workspace is the DEFAULT host tree to mount, used when a request names
	// none. In a personal deployment it is the only tree ever mounted.
	Workspace string

	// Root is the outer containment bound for per-request mounts (MU-021).
	// A PrivilegedCommand.Workspace outside it is refused, so a request can
	// only ever select a subtree the operator already exposed — never widen
	// the mount to an arbitrary host path. Empty means "bound by Workspace",
	// which is the correct single-tenant reading.
	Root string

	Image              string
	Limits             sandbox.Limits
	PIDs               int
	Binary             string
	ContainerRuntime   string
	RequireSignedImage bool
	CosignKey          string
	EgressProxy        string
	EgressNetwork      string
	AllowedEgressHosts []string
}

func (DockerPrivilegedRunner) Mode() string { return "docker" }

func (r DockerPrivilegedRunner) Ready(ctx context.Context) error {
	binary := strings.TrimSpace(r.Binary)
	if binary == "" {
		binary = "docker"
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := exec.CommandContext(checkCtx, binary, "version", "--format", "{{.Server.Version}}").Run(); err != nil {
		return fmt.Errorf("docker isolation unavailable (no host fallback): %w", err)
	}
	if rt := strings.TrimSpace(r.ContainerRuntime); rt != "" {
		out, err := exec.CommandContext(checkCtx, binary, "info", "--format", "{{json .Runtimes}}").Output()
		if err != nil || !strings.Contains(string(out), `"`+rt+`"`) {
			return fmt.Errorf("hardened container runtime %q is unavailable", rt)
		}
	}
	if r.RequireSignedImage {
		if !strings.Contains(r.Image, "@sha256:") {
			return fmt.Errorf("signed execution image must be pinned by sha256 digest")
		}
		args := []string{"verify"}
		if strings.TrimSpace(r.CosignKey) != "" {
			args = append(args, "--key", r.CosignKey)
		}
		args = append(args, r.Image)
		if err := exec.CommandContext(checkCtx, "cosign", args...).Run(); err != nil {
			return fmt.Errorf("execution image signature verification failed: %w", err)
		}
	}
	return nil
}

func (r DockerPrivilegedRunner) Run(ctx context.Context, req PrivilegedCommand) (string, error) {
	if ok, _ := ctx.Value(isolationReadyKey{}).(bool); !ok {
		if err := r.Ready(ctx); err != nil {
			return "", err
		}
	}
	if strings.TrimSpace(r.Workspace) == "" {
		return "", fmt.Errorf("docker isolation: workspace is empty")
	}
	if len(req.Argv) == 0 {
		return "", fmt.Errorf("docker isolation: empty command")
	}
	// MU-021: mount the RUN'S tree, not the deployment's. req.Workspace may
	// only narrow r.Workspace — a request naming a tree outside it is refused
	// rather than honoured, so a caller can never widen the mount.
	mount := r.Workspace
	if requested := strings.TrimSpace(req.Workspace); requested != "" {
		bound := strings.TrimSpace(r.Root)
		if bound == "" {
			bound = r.Workspace
		}
		outer, err := filepath.Abs(bound)
		if err != nil {
			return "", err
		}
		inner, err := filepath.Abs(requested)
		if err != nil {
			return "", err
		}
		rel, err := filepath.Rel(outer, inner)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("docker isolation: requested workspace is outside the configured workspace")
		}
		mount = inner
	}
	image := strings.TrimSpace(r.Image)
	if image == "" {
		image = "python:3.12-slim"
	}
	root, err := filepath.Abs(mount)
	if err != nil {
		return "", err
	}
	work := req.WorkingDir
	if work == "" {
		work = root
	}
	work, err = filepath.Abs(work)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, work)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("docker isolation: working directory is outside workspace")
	}
	containerWork := "/workspace"
	if rel != "." {
		containerWork = filepath.ToSlash(filepath.Join(containerWork, rel))
	}
	argv := translateWorkspaceArgs(req.Argv, root)
	network := "none"
	if strings.TrimSpace(r.EgressProxy) != "" {
		network = strings.TrimSpace(r.EgressNetwork)
		if network == "" || network == "none" {
			return "", fmt.Errorf("docker isolation: proxy egress requires a dedicated egress network")
		}
	}
	args := []string{"run", "--rm", "-i", "--network", network, "--read-only", "--user", "65532:65532",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", strconv.Itoa(defaultInt(r.PIDs, 128)),
		"--tmpfs", "/tmp:rw,noexec,nosuid,size=64m", "-v", root + ":/workspace:rw", "-w", containerWork,
	}
	if rt := strings.TrimSpace(r.ContainerRuntime); rt != "" {
		args = append(args, "--runtime", rt)
	}
	if proxy := strings.TrimSpace(r.EgressProxy); proxy != "" {
		args = append(args, "-e", "HTTPS_PROXY="+proxy, "-e", "HTTP_PROXY="+proxy, "-e", "ALL_PROXY="+proxy,
			"-e", "SOULACY_EGRESS_ALLOWLIST="+strings.Join(r.AllowedEgressHosts, ","))
	}
	if r.Limits.MemoryMB > 0 {
		args = append(args, "--memory", strconv.Itoa(r.Limits.MemoryMB)+"m")
	}
	if r.Limits.CPUSeconds > 0 {
		args = append(args, "--cpus", "1")
	}
	if r.Limits.OpenFiles > 0 {
		n := strconv.Itoa(r.Limits.OpenFiles)
		args = append(args, "--ulimit", "nofile="+n+":"+n)
	}
	if r.Limits.FileSizeMB > 0 {
		n := strconv.FormatInt(int64(r.Limits.FileSizeMB)<<20, 10)
		args = append(args, "--ulimit", "fsize="+n+":"+n)
	}
	// Only the already-filtered, non-secret environment is copied, and host
	// workspace path hints are translated into their container equivalent.
	for _, pair := range req.Env {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			continue
		}
		value := strings.ReplaceAll(parts[1], root, "/workspace")
		args = append(args, "-e", parts[0]+"="+value)
	}
	args = append(args, image)
	args = append(args, argv...)
	binary := strings.TrimSpace(r.Binary)
	if binary == "" {
		binary = "docker"
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("docker isolation failed (no host fallback): %w", err)
	}
	return out.String(), nil
}

func translateWorkspaceArgs(argv []string, root string) []string {
	out := append([]string(nil), argv...)
	for i, arg := range out {
		out[i] = strings.ReplaceAll(arg, root, "/workspace")
	}
	return out
}

func defaultInt(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}

func (e *Engine) SetPrivilegedCommandRunner(r PrivilegedCommandRunner) {
	if r == nil {
		e.privilegedRunner = denyPrivilegedRunner{}
		return
	}
	e.privilegedRunner = r
}

func (e *Engine) SetPrivilegedWorkDir(path string) { e.privilegedWorkDir = filepath.Clean(path) }

func (e *Engine) executePrivilegedBuiltin(ctx context.Context, tool string, handler func(context.Context) (string, error)) (string, error) {
	if !isPrivilegedSystemTool(tool) {
		return "", fmt.Errorf("tool %q is not privileged", tool)
	}
	if e.privilegedRunner == nil || e.privilegedRunner.Mode() == "unavailable" {
		return "", fmt.Errorf("privileged tool %q refused: isolation unavailable", tool)
	}
	if ready, ok := e.privilegedRunner.(interface{ Ready(context.Context) error }); ok {
		if err := ready.Ready(ctx); err != nil {
			return "", fmt.Errorf("privileged tool %q refused: %w", tool, err)
		}
		ctx = context.WithValue(ctx, isolationReadyKey{}, true)
	}
	return handler(ctx)
}

// runPrivilegedCommand is the single choke point every privileged builtin
// reaches the host through. It stamps the run's workspace onto the request so
// no builtin has to remember to, and refuses when the workspace has no scratch
// directory rather than falling back to the shared one.
func (e *Engine) runPrivilegedCommand(ctx context.Context, req PrivilegedCommand, outputLimit int) (string, error) {
	if e.privilegedRunner == nil {
		e.privilegedRunner = denyPrivilegedRunner{}
	}
	workspaceID := WorkspaceFromContext(ctx)
	req.Workspace = e.workspaceScratchDir(workspaceID)
	// Empty is the correct answer for the personal workspace and only for it:
	// a single-tenant install may have configured no scratch root at all, and
	// the runner's own workspace is then the right — and only — mount. For a
	// NAMED workspace, empty means the namespaced directory could not be
	// created, and falling through would mount the shared tree. Refuse.
	if req.Workspace == "" && wsroot.Normalize(workspaceID) != wsroot.PersonalWorkspaceID {
		return "", fmt.Errorf("privileged execution refused: workspace %q has no isolated scratch directory", workspaceID)
	}
	out, runErr := e.privilegedRunner.Run(ctx, req)
	result := strings.TrimSpace(out)
	if outputLimit > 0 && len(result) > outputLimit {
		result = result[:outputLimit] + "\n[output truncated]"
	}
	if runErr != nil {
		if e.privilegedRunner.Mode() != "unsandboxed" {
			return "", runErr
		}
		return fmt.Sprintf("exit_code: non-zero\n%s\nerror: %v", result, runErr), nil
	}
	return result, nil
}
