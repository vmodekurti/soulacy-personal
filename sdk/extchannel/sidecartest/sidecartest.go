// Package sidecartest is the official External Channel Protocol conformance
// kit (Story E11). Sidecar authors run it out-of-tree against their own
// command in any language:
//
//	func TestMySidecarConforms(t *testing.T) {
//	    if err := sidecartest.RunConformance(context.Background(),
//	        "python3", "my_sidecar.py"); err != nil {
//	        t.Fatal(err)
//	    }
//	}
//
// The host runs the same kit against the reference sidecars in CI, so the
// contract and the implementations cannot drift.
package sidecartest

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/soulacy/soulacy/sdk/extchannel"
)

// RunConformance exercises a sidecar command against the External Channel
// Protocol v1 contract: hello within the deadline, valid negotiation,
// tolerance of unknown frames, surviving a send frame, and clean shutdown
// within 5 seconds. Returns nil when the sidecar conforms; a descriptive
// error otherwise.
func RunConformance(ctx context.Context, command string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, command, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("conformance: stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("conformance: stdin pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("conformance: start: %w", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	frames := make(chan extchannel.Frame, 32)
	scanErr := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 4096), 1024*1024)
		for sc.Scan() {
			if f, err := extchannel.ParseFrame(sc.Bytes()); err == nil {
				frames <- f
			}
			// Malformed lines are skipped; strict mode could flag them.
		}
		scanErr <- sc.Err()
		close(frames)
	}()

	// 1. hello within 5 seconds (unknown frames before it are fine).
	var hello extchannel.Frame
	helloDeadline := time.After(5 * time.Second)
waitHello:
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				return fmt.Errorf("conformance: sidecar exited before sending hello")
			}
			if f.Type == "hello" {
				hello = f
				break waitHello
			}
		case <-helloDeadline:
			return fmt.Errorf("conformance: no hello frame within 5s")
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// 2. Negotiation must succeed.
	v, err := extchannel.Negotiate(hello)
	if err != nil {
		return fmt.Errorf("conformance: %w", err)
	}
	if err := extchannel.WriteFrame(stdin, extchannel.Frame{Type: "hello_ack", Protocol: v}); err != nil {
		return fmt.Errorf("conformance: write hello_ack: %w", err)
	}

	// 3. Sidecar must tolerate unknown frame types from the gateway.
	if err := extchannel.WriteFrame(stdin, extchannel.Frame{Type: "conformance.probe.v99"}); err != nil {
		return fmt.Errorf("conformance: write unknown frame: %w", err)
	}

	// 4. A send frame must not crash it (delivery isn't verifiable here).
	if err := extchannel.WriteFrame(stdin, extchannel.Frame{Type: "send", To: "conformance-chat", Text: "ping"}); err != nil {
		return fmt.Errorf("conformance: write send: %w", err)
	}

	// Give it a beat to crash if it's going to.
	select {
	case <-time.After(300 * time.Millisecond):
	case <-ctx.Done():
		return ctx.Err()
	}
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		return fmt.Errorf("conformance: sidecar exited after send/unknown frames")
	}

	// 5. shutdown must terminate the process within 5 seconds.
	if err := extchannel.WriteFrame(stdin, extchannel.Frame{Type: "shutdown"}); err != nil {
		return fmt.Errorf("conformance: write shutdown: %w", err)
	}
	_ = stdin.Close()
	exitCh := make(chan error, 1)
	go func() { exitCh <- cmd.Wait() }()
	select {
	case <-exitCh:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("conformance: sidecar did not exit within 5s of shutdown")
	}
}
