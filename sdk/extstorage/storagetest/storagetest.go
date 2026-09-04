// Package storagetest is the official External Storage Protocol conformance
// kit (Story E24, mirroring sdk/extchannel/sidecartest from E11). Sidecar
// authors run it out-of-tree against their own command in any language:
//
//	func TestMyStorageSidecarConforms(t *testing.T) {
//	    if err := storagetest.RunConformance(context.Background(),
//	        t.TempDir(), "python3", "my_sidecar.py"); err != nil {
//	        t.Fatal(err)
//	    }
//	}
//
// The host runs the same kit against the reference sidecar in CI, so the
// contract and the implementations cannot drift.
package storagetest

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/soulacy/soulacy/sdk/extstorage"
)

// RunConformance exercises a storage sidecar command against the External
// Storage Protocol v1 contract:
//
//  1. negotiate answered within 5s — valid version (1..host), shared dir
//     echoed back, at least one capability advertised;
//  2. an unknown method answered with error code -32601 (never a crash);
//  3. when "vector" is advertised: vector.write accepted and vector.search
//     answered with a results array;
//  4. malformed-frame tolerance: a junk line must not kill the process;
//  5. shutdown → process exit within 5 seconds.
//
// sharedDir must be an existing absolute directory (use t.TempDir()).
// Returns nil when the sidecar conforms; a descriptive error otherwise.
func RunConformance(ctx context.Context, sharedDir, command string, args ...string) error {
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

	msgs := make(chan extstorage.Message, 32)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 4096), 4*1024*1024)
		for sc.Scan() {
			if m, err := extstorage.ParseMessage(sc.Bytes()); err == nil {
				msgs <- m
			}
			// Malformed lines are skipped; strict mode could flag them.
		}
		close(msgs)
	}()

	nextID := int64(0)
	call := func(method string, params any, timeout time.Duration) (extstorage.Message, error) {
		nextID++
		req, err := extstorage.NewRequest(nextID, method, params)
		if err != nil {
			return extstorage.Message{}, fmt.Errorf("conformance: build %s: %w", method, err)
		}
		if err := extstorage.WriteMessage(stdin, req); err != nil {
			return extstorage.Message{}, fmt.Errorf("conformance: write %s: %w", method, err)
		}
		deadline := time.After(timeout)
		for {
			select {
			case m, ok := <-msgs:
				if !ok {
					return extstorage.Message{}, fmt.Errorf("conformance: sidecar exited while awaiting %s response", method)
				}
				if m.IsResponse() && *m.ID == nextID {
					return m, nil
				}
				// Other traffic (notifications, stale responses) is fine.
			case <-deadline:
				return extstorage.Message{}, fmt.Errorf("conformance: no %s response within %s", method, timeout)
			case <-ctx.Done():
				return extstorage.Message{}, ctx.Err()
			}
		}
	}

	// 1. negotiate.
	resp, err := call(extstorage.MethodNegotiate, extstorage.NegotiateParams{
		Protocol:  extstorage.ProtocolVersion,
		Name:      "conformance-host",
		SharedDir: sharedDir,
	}, 5*time.Second)
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("conformance: negotiate refused: %v", resp.Error)
	}
	var neg extstorage.NegotiateResult
	if err := json.Unmarshal(resp.Result, &neg); err != nil {
		return fmt.Errorf("conformance: negotiate result malformed: %w", err)
	}
	if neg.Protocol < 1 || neg.Protocol > extstorage.ProtocolVersion {
		return fmt.Errorf("conformance: negotiated protocol %d outside 1..%d", neg.Protocol, extstorage.ProtocolVersion)
	}
	if neg.SharedDir != sharedDir {
		return fmt.Errorf("conformance: shared dir not echoed (got %q, want %q) — sidecar must parse the shared-mount contract", neg.SharedDir, sharedDir)
	}
	if len(neg.Capabilities) == 0 {
		return fmt.Errorf("conformance: negotiate result advertises no capabilities")
	}

	// 2. Unknown method → -32601, not a crash.
	resp, err = call("conformance.probe.v99", struct{}{}, 5*time.Second)
	if err != nil {
		return err
	}
	if resp.Error == nil || resp.Error.Code != extstorage.CodeMethodNotFound {
		return fmt.Errorf("conformance: unknown method must yield error %d, got %+v",
			extstorage.CodeMethodNotFound, resp.Error)
	}

	// 3. Vector round-trip when advertised.
	if hasCap(neg.Capabilities, "vector") {
		resp, err = call(extstorage.MethodVectorWrite, extstorage.VectorWriteParams{
			ID: "conf-1", AgentID: "conformance", Content: "hello world",
			Timestamp: time.Now().Unix(),
		}, 5*time.Second)
		if err != nil {
			return err
		}
		if resp.Error != nil {
			return fmt.Errorf("conformance: vector.write failed: %v", resp.Error)
		}
		resp, err = call(extstorage.MethodVectorSearch, extstorage.VectorSearchParams{
			AgentID: "conformance", Query: "hello", TopK: 1,
		}, 5*time.Second)
		if err != nil {
			return err
		}
		if resp.Error != nil {
			return fmt.Errorf("conformance: vector.search failed: %v", resp.Error)
		}
		var sr extstorage.VectorSearchResult
		if err := json.Unmarshal(resp.Result, &sr); err != nil {
			return fmt.Errorf("conformance: vector.search result malformed: %w", err)
		}
		if sr.Results == nil {
			return fmt.Errorf("conformance: vector.search result missing results array")
		}
	}

	// 4. Junk line tolerance.
	if _, err := stdin.Write([]byte("this is not json\n")); err != nil {
		return fmt.Errorf("conformance: write junk line: %w", err)
	}
	select {
	case <-time.After(300 * time.Millisecond):
	case <-ctx.Done():
		return ctx.Err()
	}
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		return fmt.Errorf("conformance: sidecar exited after a malformed line")
	}

	// 5. shutdown → exit ≤ 5s.
	nextID++
	req, _ := extstorage.NewRequest(nextID, extstorage.MethodShutdown, struct{}{})
	if err := extstorage.WriteMessage(stdin, req); err != nil {
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

func hasCap(caps []string, want string) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}
