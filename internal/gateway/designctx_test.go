package gateway

// The builder model must not be cancelled by the HTTP request that started it.
//
// Roughly one fan-out generation in four came back as a straight-line template
// carrying:
//
//	ollama-cloud: request failed: Post ".../chat/completions": context canceled
//
// Cancelled — not timed out, and not the provider's doing. Nor the client
// giving up: that request returned 200 to a caller still awaiting it, with the
// template, because the model call had been killed underneath. The request
// context died while the handler carrying it kept running, and a 40-second
// generation was discarded for it.
//
// This is a whole-file rule, not a single call site: studioDesignGraph runs
// four model calls (design and the structure retry, workflow and agent), and
// any one of them left on c.Context() reopens the hole for that path only. The
// streamed generate and Run Live already detach and say why in their comments;
// this asserts the design path does too, and keeps doing it.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// designGraphSource returns the body of studioDesignGraph.
func designGraphSource(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("studio.go")
	if err != nil {
		t.Fatalf("read studio.go: %v", err)
	}
	text := string(src)
	start := strings.Index(text, "func (s *Server) studioDesignGraph(")
	if start < 0 {
		t.Fatal("studioDesignGraph is gone or renamed — this rule now guards nothing")
	}
	// Up to the next top-level func.
	rest := text[start+1:]
	if end := strings.Index(rest, "\nfunc "); end >= 0 {
		return text[start : start+1+end]
	}
	return text[start:]
}

var modelCallRe = regexp.MustCompile(`studio\.Compile(?:Agent)?\(([A-Za-z0-9_.()]+),`)

func TestStudioDesignGraph_NeverPassesTheRequestContextToTheModel(t *testing.T) {
	body := designGraphSource(t)

	calls := modelCallRe.FindAllStringSubmatch(body, -1)
	if len(calls) < 2 {
		t.Fatalf("expected several builder-model calls to guard, found %d", len(calls))
	}
	for _, m := range calls {
		if strings.Contains(m[1], "c.Context()") {
			t.Errorf("a builder-model call still runs on the request context (%s) — "+
				"when that context is cancelled mid-handler the generation is thrown away "+
				"and the user silently gets the fallback template", m[0])
		}
	}
}

// Detaching without a deadline would trade a cancelled call for one that can
// hang forever, burning tokens with nobody waiting.
func TestStudioDesignGraph_BoundsTheDetachedContext(t *testing.T) {
	body := designGraphSource(t)

	if !strings.Contains(body, "context.WithoutCancel(c.Context())") {
		t.Fatal("the design context is not detached from the request")
	}
	if !strings.Contains(body, "context.WithTimeout(context.WithoutCancel(c.Context())") {
		t.Error("the detached context has no deadline — a stuck provider would hang the design forever")
	}
	if !strings.Contains(body, "defer cancelDesign()") {
		t.Error("the design context is never cancelled, so its timer leaks on every request")
	}
}
