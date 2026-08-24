// Package docker implements executor.Backend by running each Python call in a
// short-lived Docker container.
package docker

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/soulacy/soulacy/internal/dockerutil"
	"github.com/soulacy/soulacy/internal/executor"
	"github.com/soulacy/soulacy/internal/executor/process"
	"github.com/soulacy/soulacy/pkg/message"
)

var _ executor.Backend = (*Executor)(nil)

type Executor struct {
	image              string
	pythonBin          string
	network            string
	volumes            []string // explicit -v mount specs (host:container[:ro]); the allowlist
	onProgress         func(message.ProgressEvent)
	runtime            string
	requireSignedImage bool
	cosignKey          string
}

type Config struct {
	Image, PythonBin, Network, Runtime, CosignKey string
	Volumes                                       []string
	RequireSignedImage                            bool
}

func New(image, pythonBin, network string) *Executor {
	return NewWithVolumes(image, pythonBin, network, nil)
}

// NewWithVolumes is New plus an explicit volume allowlist. Only the mounts named
// here are bound into the container — there are no implicit host mounts — so the
// operator fully controls what container-run tool code can touch on disk.
func NewWithVolumes(image, pythonBin, network string, volumes []string) *Executor {
	return NewHardened(Config{Image: image, PythonBin: pythonBin, Network: network, Volumes: volumes})
}

func NewHardened(cfg Config) *Executor {
	image, pythonBin, network, volumes := cfg.Image, cfg.PythonBin, cfg.Network, cfg.Volumes
	if strings.TrimSpace(image) == "" {
		image = "python:3.12-slim"
	}
	if strings.TrimSpace(pythonBin) == "" {
		pythonBin = "python3"
	}
	if strings.TrimSpace(network) == "" {
		network = "none"
	}
	clean := make([]string, 0, len(volumes))
	for _, v := range volumes {
		if v = strings.TrimSpace(v); v != "" {
			clean = append(clean, v)
		}
	}
	return &Executor{image: image, pythonBin: pythonBin, network: network, volumes: clean, runtime: strings.TrimSpace(cfg.Runtime), requireSignedImage: cfg.RequireSignedImage, cosignKey: cfg.CosignKey}
}

func (e *Executor) SetOnProgress(fn func(message.ProgressEvent)) { e.onProgress = fn }

// Ready proves the worker can establish the configured isolation boundary.
// Hosted workers call this before subscribing so an unavailable runtime or an
// untrusted image makes the worker fail closed instead of accepting jobs it
// cannot safely execute.
func (e *Executor) Ready(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	versionCmd, err := dockerutil.CommandContext(checkCtx, "version", "--format", "{{.Server.Version}}")
	if err != nil {
		return fmt.Errorf("executor/docker: %w", err)
	}
	if err := versionCmd.Run(); err != nil {
		return fmt.Errorf("executor/docker: Docker daemon unavailable: %w", err)
	}
	if e.runtime != "" {
		infoCmd, cmdErr := dockerutil.CommandContext(checkCtx, "info", "--format", "{{json .Runtimes}}")
		if cmdErr != nil {
			return fmt.Errorf("executor/docker: %w", cmdErr)
		}
		out, err := infoCmd.Output()
		if err != nil || !strings.Contains(string(out), `"`+e.runtime+`"`) {
			return fmt.Errorf("executor/docker: hardened runtime %q unavailable", e.runtime)
		}
	}
	return e.verifyImage(checkCtx)
}

func (e *Executor) verifyImage(ctx context.Context) error {
	if !e.requireSignedImage {
		return nil
	}
	if !strings.Contains(e.image, "@sha256:") {
		return fmt.Errorf("executor/docker: signed image must be digest-pinned")
	}
	verify := []string{"verify"}
	if strings.TrimSpace(e.cosignKey) != "" {
		verify = append(verify, "--key", e.cosignKey)
	}
	verify = append(verify, e.image)
	if err := exec.CommandContext(ctx, "cosign", verify...).Run(); err != nil {
		return fmt.Errorf("executor/docker: image signature verification: %w", err)
	}
	return nil
}

func (e *Executor) Run(ctx context.Context, pyFile, funcName, inline string, argsJSON []byte) (string, error) {
	script, err := process.BuildScript(pyFile, funcName, inline)
	if err != nil {
		return "", err
	}
	if argsJSON == nil {
		argsJSON = []byte("{}")
	}
	runID := uuid.New().String()
	if err := e.verifyImage(ctx); err != nil {
		return "", err
	}
	args := dockerRunArgs(e.network, e.image, e.pythonBin, e.volumes, script)
	if e.runtime != "" {
		args = append(args[:2], append([]string{"--runtime", e.runtime}, args[2:]...)...)
	}
	cmd, err := dockerutil.CommandContext(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("executor/docker: %w", err)
	}
	cmd.Stdin = bytes.NewReader(argsJSON)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("executor/docker: stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("executor/docker: start: %w", err)
	}
	var outputLines []string
	sc := bufio.NewScanner(stdoutPipe)
	for sc.Scan() {
		line := sc.Text()
		if ev, ok := executor.ParseProgressLine(line, runID); ok {
			if e.onProgress != nil {
				e.onProgress(ev)
			}
			continue
		}
		outputLines = append(outputLines, line)
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("executor/docker: read stdout: %w", err)
	}
	if runErr := cmd.Wait(); runErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = runErr.Error()
		}
		if len(msg) > 4000 {
			msg = msg[len(msg)-4000:]
		}
		return "", fmt.Errorf("executor/docker: %w\n%s", runErr, msg)
	}
	return strings.TrimSpace(strings.Join(outputLines, "\n")), nil
}

// dockerRunArgs builds the `docker run` argv. Each allowlisted volume becomes a
// `-v host:container[:ro]` mount; nothing is mounted implicitly.
func dockerRunArgs(network, image, pythonBin string, volumes []string, script string) []string {
	args := []string{"run", "--rm", "-i", "--network", network, "--read-only", "--user", "65532:65532", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "128", "--tmpfs", "/tmp:rw,noexec,nosuid,size=64m"}
	for _, v := range volumes {
		args = append(args, "-v", v)
	}
	return append(args, image, pythonBin, "-c", script)
}

func (e *Executor) Close() error { return nil }
