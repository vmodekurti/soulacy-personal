// Package ssh implements executor.Backend by sending each Python call to a
// remote host over the system ssh client.
package ssh

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
	target       string
	pythonBin    string
	identityFile string
	onProgress   func(message.ProgressEvent)
}

func New(host, user, pythonBin, identityFile string) *Executor {
	target := strings.TrimSpace(host)
	if u := strings.TrimSpace(user); u != "" && target != "" && !strings.Contains(target, "@") {
		target = u + "@" + target
	}
	if strings.TrimSpace(pythonBin) == "" {
		pythonBin = "python3"
	}
	return &Executor{target: target, pythonBin: pythonBin, identityFile: strings.TrimSpace(identityFile)}
}

func (e *Executor) SetOnProgress(fn func(message.ProgressEvent)) { e.onProgress = fn }

func (e *Executor) Run(ctx context.Context, pyFile, funcName, inline string, argsJSON []byte) (string, error) {
	if e.target == "" {
		return "", fmt.Errorf("executor/ssh: host is required")
	}
	script, err := process.BuildScript(pyFile, funcName, inline)
	if err != nil {
		return "", err
	}
	if argsJSON == nil {
		argsJSON = []byte("{}")
	}
	runID := uuid.New().String()
	args := []string{"-o", "BatchMode=yes"}
	if e.identityFile != "" {
		args = append(args, "-i", e.identityFile)
	}
	args = append(args, e.target, e.pythonBin, "-c", script)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = bytes.NewReader(argsJSON)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("executor/ssh: stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("executor/ssh: start: %w", err)
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
		return "", fmt.Errorf("executor/ssh: read stdout: %w", err)
	}
	if runErr := cmd.Wait(); runErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = runErr.Error()
		}
		if len(msg) > 4000 {
			msg = msg[len(msg)-4000:]
		}
		return "", fmt.Errorf("executor/ssh: %w\n%s", runErr, msg)
	}
	return strings.TrimSpace(strings.Join(outputLines, "\n")), nil
}

func (e *Executor) Close() error { return nil }
