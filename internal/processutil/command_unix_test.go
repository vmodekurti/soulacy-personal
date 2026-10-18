//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package processutil

import (
	"bufio"
	"context"
	"testing"
	"time"
)

func TestCancellationClosesDescendantPipes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := CommandContext(ctx, "/bin/sh", "-c", "sleep 30 & echo ready; wait")
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(pipe)
	if !scanner.Scan() {
		t.Fatal("child did not start")
	}
	cancel()
	done := make(chan error, 1)
	go func() {
		for scanner.Scan() {
		}
		done <- cmd.Wait()
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled process succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("descendant retained stdout after cancellation")
	}
}
