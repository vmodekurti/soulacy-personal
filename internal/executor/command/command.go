// Package command implements executor.Backend by running each Python call
// through an arbitrary wrapper command — e.g. a cloud sandbox CLI such as
// `modal run`, `runpodctl exec`, or `daytona ssh`. The wrapper argv is prepended
// to `<pythonBin> -c <script>`, so any runner that can execute a remote/isolated
// Python process becomes an execution backend without bespoke code. The docker
// and ssh backends are special cases of this shape; this generalizes them for
// cloud presets.
package command

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

// Executor runs python through a fixed wrapper command.
type Executor struct {
	runner     []string // e.g. ["modal","run","--"]
	pythonBin  string
	label      string
	onProgress func(message.ProgressEvent)
}

// New builds a command executor. runner is the wrapper argv; pythonBin defaults
// to "python3". label is used only in error messages.
func New(label string, runner []string, pythonBin string) *Executor {
	if strings.TrimSpace(pythonBin) == "" {
		pythonBin = "python3"
	}
	clean := make([]string, 0, len(runner))
	for _, r := range runner {
		if r = strings.TrimSpace(r); r != "" {
			clean = append(clean, r)
		}
	}
	if label == "" {
		label = "command"
	}
	return &Executor{runner: clean, pythonBin: pythonBin, label: label}
}

func (e *Executor) SetOnProgress(fn func(message.ProgressEvent)) { e.onProgress = fn }

// Argv exposes the full argv for a given script (used in tests).
func (e *Executor) Argv(script string) []string {
	return buildArgv(e.runner, e.pythonBin, script)
}

func buildArgv(runner []string, pythonBin, script string) []string {
	argv := make([]string, 0, len(runner)+3)
	argv = append(argv, runner...)
	argv = append(argv, pythonBin, "-c", script)
	return argv
}

func (e *Executor) Run(ctx context.Context, pyFile, funcName, inline string, argsJSON []byte) (string, error) {
	if len(e.runner) == 0 {
		return "", fmt.Errorf("executor/%s: no runner command configured", e.label)
	}
	script, err := process.BuildScript(pyFile, funcName, inline)
	if err != nil {
		return "", err
	}
	if argsJSON == nil {
		argsJSON = []byte("{}")
	}
	runID := uuid.New().String()
	argv := buildArgv(e.runner, e.pythonBin, script)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = bytes.NewReader(argsJSON)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("executor/%s: stdout pipe: %w", e.label, err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("executor/%s: start: %w", e.label, err)
	}
	var outputLines []string
	sc := bufio.NewScanner(stdoutPipe)
	sc.Buffer(make([]byte, 4096), 1024*1024)
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
		return "", fmt.Errorf("executor/%s: read stdout: %w", e.label, err)
	}
	if runErr := cmd.Wait(); runErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = runErr.Error()
		}
		if len(msg) > 4000 {
			msg = msg[len(msg)-4000:]
		}
		return "", fmt.Errorf("executor/%s: %w\n%s", e.label, runErr, msg)
	}
	return strings.TrimSpace(strings.Join(outputLines, "\n")), nil
}

func (e *Executor) Close() error { return nil }
