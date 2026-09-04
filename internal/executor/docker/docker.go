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

	"github.com/google/uuid"

	"github.com/soulacy/soulacy/internal/executor"
	"github.com/soulacy/soulacy/internal/executor/process"
	"github.com/soulacy/soulacy/pkg/message"
)

var _ executor.Backend = (*Executor)(nil)

type Executor struct {
	image      string
	pythonBin  string
	network    string
	volumes    []string // explicit -v mount specs (host:container[:ro]); the allowlist
	onProgress func(message.ProgressEvent)
}

func New(image, pythonBin, network string) *Executor {
	return NewWithVolumes(image, pythonBin, network, nil)
}

// NewWithVolumes is New plus an explicit volume allowlist. Only the mounts named
// here are bound into the container — there are no implicit host mounts — so the
// operator fully controls what container-run tool code can touch on disk.
func NewWithVolumes(image, pythonBin, network string, volumes []string) *Executor {
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
	return &Executor{image: image, pythonBin: pythonBin, network: network, volumes: clean}
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
	args := dockerRunArgs(e.network, e.image, e.pythonBin, e.volumes, script)
	cmd := exec.CommandContext(ctx, "docker", args...)
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
	args := []string{"run", "--rm", "-i", "--network", network}
	for _, v := range volumes {
		args = append(args, "-v", v)
	}
	return append(args, image, pythonBin, "-c", script)
}

func (e *Executor) Close() error { return nil }
