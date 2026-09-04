package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestIsSlowStepTimeout(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"wrapped deadline", fmt.Errorf("tool call: %w", context.DeadlineExceeded), true},
		{"deadline text", errors.New("flow: node \"poll\": context deadline exceeded"), true},
		{"tool_timeout message", errors.New(`tool "x" exceeded the 2m0s tool_timeout`), true},
		{"real failure", errors.New("mcp: server rejected unknown argument"), false},
	}
	for _, c := range cases {
		if got := isSlowStepTimeout(c.err); got != c.want {
			t.Errorf("%s: isSlowStepTimeout=%v want %v", c.name, got, c.want)
		}
	}
}

// A real-run verify where a tool times out should be a SOFT PASS (OK=true), not a
// failure the build loop tries to repair forever — a slow external op isn't a
// wiring bug the loop can fix.
func TestRealRunVerifier_SoftPassesSlowTool(t *testing.T) {
	v := RealRunVerifier{Runner: RealRunner{
		Tool: func(ctx context.Context, name, args string) (json.RawMessage, error) {
			return nil, fmt.Errorf("tool %q: %w", name, context.DeadlineExceeded)
		},
	}}
	out := v.Verify(context.Background(), cleanWorkflow(), TestCase{})
	if !out.OK {
		t.Fatalf("a slow (timed-out) tool should soft-pass the build check; got %+v", out)
	}
	var noted bool
	for _, line := range out.Trace {
		if strings.Contains(line, "is slow") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("expected an 'is slow' trace note; got %+v", out.Trace)
	}
}

// A genuine tool failure (not a timeout) must still fail the verify so the loop
// repairs it.
func TestRealRunVerifier_RealFailureStillFails(t *testing.T) {
	v := RealRunVerifier{Runner: RealRunner{
		Tool: func(ctx context.Context, name, args string) (json.RawMessage, error) {
			return nil, errors.New("mcp: rejected unknown argument num_results")
		},
	}}
	out := v.Verify(context.Background(), cleanWorkflow(), TestCase{})
	if out.OK {
		t.Fatalf("a real tool failure must NOT soft-pass; got %+v", out)
	}
}
