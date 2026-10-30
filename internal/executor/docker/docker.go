// Package docker implements executor.Backend by running each Python call in a
// short-lived Docker container behind the shared hardened profile
// (sandbox.ContainerProfile): read-only root, no capabilities, no privilege
// escalation, PID/memory/CPU caps, no network by default, and no host
// environment or mounts beyond the explicit volume allowlist.
package docker

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/google/uuid"

	"github.com/soulacy/soulacy/internal/executor"
	"github.com/soulacy/soulacy/internal/executor/process"
	"github.com/soulacy/soulacy/internal/sandbox"
	"github.com/soulacy/soulacy/pkg/message"
)

var _ executor.Backend = (*Executor)(nil)

type Executor struct {
	image      string
	pythonBin  string
	network    string
	volumes    []string // explicit -v mount specs (host:container[:ro]); the allowlist
	limits     sandbox.Limits
	pids       int
	user       string // --user value; "" keeps the image's default user
	binary     string
	onProgress func(message.ProgressEvent)
}

// Options configures a hardened docker executor. Zero values pick safe
// defaults: python:3.12-slim, python3, no network, no mounts, the image's
// default user, and only the profile's built-in PID cap.
type Options struct {
	Image     string
	PythonBin string
	Network   string
	// Volumes is the explicit mount allowlist; nothing is mounted implicitly.
	Volumes []string
	// Limits and PIDs come from runtime.sandbox so containerised tools get
	// the same caps as privileged builtins.
	Limits sandbox.Limits
	PIDs   int
	// User is the --user value (see sandbox.ContainerUser); "" keeps the
	// image's default user.
	User string
	// Binary overrides the docker CLI (tests, podman); "" means "docker".
	Binary string
}

func New(image, pythonBin, network string) *Executor {
	return NewWithVolumes(image, pythonBin, network, nil)
}

// NewWithVolumes is New plus an explicit volume allowlist. Only the mounts named
// here are bound into the container — there are no implicit host mounts — so the
// operator fully controls what container-run tool code can touch on disk.
func NewWithVolumes(image, pythonBin, network string, volumes []string) *Executor {
	return NewWithOptions(Options{Image: image, PythonBin: pythonBin, Network: network, Volumes: volumes})
}

// NewWithOptions builds an executor from the full option set.
func NewWithOptions(o Options) *Executor {
	if strings.TrimSpace(o.Image) == "" {
		o.Image = "python:3.12-slim"
	}
	if strings.TrimSpace(o.PythonBin) == "" {
		o.PythonBin = "python3"
	}
	if strings.TrimSpace(o.Network) == "" {
		o.Network = "none"
	}
	if strings.TrimSpace(o.Binary) == "" {
		o.Binary = "docker"
	}
	clean := make([]string, 0, len(o.Volumes))
	for _, v := range o.Volumes {
		if v = strings.TrimSpace(v); v != "" {
			clean = append(clean, v)
		}
	}
	return &Executor{
		image: o.Image, pythonBin: o.PythonBin, network: o.Network, volumes: clean,
		limits: o.Limits, pids: o.PIDs, user: strings.TrimSpace(o.User), binary: o.Binary,
	}
}

func (e *Executor) SetOnProgress(fn func(message.ProgressEvent)) { e.onProgress = fn }

func (e *Executor) Run(ctx context.Context, pyFile, funcName, inline string, argsJSON []byte) (string, error) {
	script, err := process.BuildScript(pyFile, funcName, inline)
	if err != nil {
		return "", err
	}
	if argsJSON == nil {
		argsJSON = []byte("{}")
	}
	runID := uuid.New().String()
	cmd := exec.CommandContext(ctx, e.binary, e.runArgs(script)...)
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
		return "", fmt.Errorf("executor/docker: %w (no host fallback)\n%s", runErr, msg)
	}
	return strings.TrimSpace(strings.Join(outputLines, "\n")), nil
}

// runArgs builds the `docker run` argv through the shared hardened profile.
// The root filesystem is read-only, so the tool works in a tmpfs /tmp (also
// HOME), and Python is told not to write .pyc files. Each allowlisted volume
// becomes a -v mount; nothing is mounted implicitly, and no host environment
// is forwarded.
func (e *Executor) runArgs(script string) []string {
	profile := sandbox.ContainerProfile{
		Network: e.network,
		Limits:  e.limits,
		PIDs:    e.pids,
		User:    e.user,
		Workdir: "/tmp",
		Mounts:  e.volumes,
		Env:     []string{"HOME=/tmp", "PYTHONDONTWRITEBYTECODE=1"},
	}
	return profile.RunArgs(e.image, e.pythonBin, "-c", script)
}

func (e *Executor) Close() error { return nil }
