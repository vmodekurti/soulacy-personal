package runtime

import (
	"bytes"
	"context"
	"fmt"
	"github.com/soulacy/soulacy/internal/processutil"
	"path/filepath"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/sandbox"
)

// PrivilegedCommand is the only process-execution request shape available to
// privileged builtins. Implementations must enforce isolation themselves;
// failure to establish it is an error, never a host-process fallback.
type PrivilegedCommand struct {
	Argv       []string
	WorkingDir string
	Env        []string
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
	cmd := processutil.CommandContext(ctx, req.Argv[0], req.Argv[1:]...)
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
	Workspace string
	Image     string
	Limits    sandbox.Limits
	PIDs      int
	Binary    string
}

func (DockerPrivilegedRunner) Mode() string { return "docker" }

func (r DockerPrivilegedRunner) Ready(ctx context.Context) error {
	binary := strings.TrimSpace(r.Binary)
	if binary == "" {
		binary = "docker"
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := processutil.CommandContext(checkCtx, binary, "version", "--format", "{{.Server.Version}}").Run(); err != nil {
		return fmt.Errorf("docker isolation unavailable (no host fallback): %w", err)
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
	image := strings.TrimSpace(r.Image)
	if image == "" {
		image = "python:3.12-slim"
	}
	root, err := filepath.Abs(r.Workspace)
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
	// Only the already-filtered, non-secret environment is copied, and host
	// workspace path hints are translated into their container equivalent.
	var env []string
	for _, pair := range req.Env {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			continue
		}
		env = append(env, parts[0]+"="+strings.ReplaceAll(parts[1], root, "/workspace"))
	}
	profile := sandbox.ContainerProfile{
		Network: "none",
		Limits:  r.Limits,
		PIDs:    r.PIDs,
		Mounts:  []string{root + ":/workspace:rw"},
		Workdir: containerWork,
		Env:     env,
	}
	args := profile.RunArgs(image, translateWorkspaceArgs(req.Argv, root)...)
	binary := strings.TrimSpace(r.Binary)
	if binary == "" {
		binary = "docker"
	}
	cmd := processutil.CommandContext(ctx, binary, args...)
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

func (e *Engine) runPrivilegedCommand(ctx context.Context, req PrivilegedCommand, outputLimit int) (string, error) {
	if e.privilegedRunner == nil {
		e.privilegedRunner = denyPrivilegedRunner{}
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
