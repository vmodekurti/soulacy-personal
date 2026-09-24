package runtime

import (
	"errors"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/mcpinstall"
	"github.com/soulacy/soulacy/pkg/message"
)

func toolErr(name, content string) message.ToolResult {
	return message.ToolResult{Name: name, Content: content, IsError: true}
}

// The refusals seen in the maverick-mcp run are exactly the ones that must be
// recognised as durable (#230); the ordinary failures around them must not be,
// or an agent loses the retries it needs to make progress.
func TestBlockedReasonSeparatesPolicyFromOrdinaryFailure(t *testing.T) {
	durable := []struct{ name, content string }{
		{"shell_exec", `error: tool "shell_exec" requires the 'system' capability in the agent's SOUL.yaml and server-level allow_system_agents`},
		{"list_dir", `error: list_dir: filesystem access denied for "/usr/local/bin": outside configured workspace roots`},
		{"session_search", "error: learning requires an authenticated, learning-enabled agent run"},
		{"http_request", `error: http_request: request failed: Get "http://127.0.0.1:18789/api/health": ssrf: dial validated address`},
		{"write_file", "error: insufficient permissions"},
	}
	for _, c := range durable {
		if blockedReason(c.name, c.content) == "" {
			t.Errorf("%s should be a durable block: %s", c.name, c.content)
		}
	}

	ordinary := []struct{ name, content string }{
		{"fetch_url", "error: fetch_url: 404 Not Found"},
		{"fetch_url", "error: fetch_url: 403 access denied by the origin server"}, // someone else's wall
		{"http_request", "error: http_request: context deadline exceeded"},
		{"read_file", "error: read_file: no such file or directory"},
		{"package_install", "error: package_install: installer failed: build error"},
		{"list_dir", "list_dir returned 4 entries"}, // not an error at all
	}
	for _, c := range ordinary {
		if r := blockedReason(c.name, c.content); r != "" {
			t.Errorf("%s should be retryable, got %q for: %s", c.name, r, c.content)
		}
	}
}

// A declined approval is durable but is the person's decision, not a
// misconfiguration, and must not be described as one.
func TestBlockedReasonNamesADeclinedApprovalAsSuch(t *testing.T) {
	got := blockedReason("package_install", `error: tool "package_install" was denied by the user`)
	if !strings.Contains(got, "declined the approval") {
		t.Fatalf("a declined approval should say so, got %q", got)
	}
}

// The run stops once enough different doors are locked, and says which.
func TestRunStopsAfterEnoughDurableBlocks(t *testing.T) {
	f := &runFailures{}

	nudges := f.observe([]message.ToolResult{toolErr("shell_exec", "error: requires the 'system' capability in the agent's SOUL.yaml")})
	if len(nudges) != 1 || !strings.Contains(nudges[0], "do NOT call") {
		t.Fatalf("first block should steer the model once, got %v", nudges)
	}
	if f.shouldStop() {
		t.Fatal("one locked door is not enough to abandon a run")
	}

	// The same tool again must not re-steer: the repetition is the cost we are avoiding.
	if again := f.observe([]message.ToolResult{toolErr("shell_exec", "error: requires the 'system' capability in the agent's SOUL.yaml")}); len(again) != 0 {
		t.Fatalf("a repeated block should not repeat the steer, got %v", again)
	}

	f.observe([]message.ToolResult{toolErr("list_dir", "error: filesystem access denied: outside configured workspace roots")})
	if f.shouldStop() {
		t.Fatal("two locked doors is still not enough")
	}

	f.observe([]message.ToolResult{toolErr("session_search", "error: learning requires an authenticated, learning-enabled agent run")})
	if !f.shouldStop() {
		t.Fatal("three different locked doors should end the run")
	}

	msg := f.stopMessage()
	for _, want := range []string{"shell_exec", "list_dir", "session_search", "not a temporary error"} {
		if !strings.Contains(msg, want) {
			t.Errorf("stop message should mention %q, got:\n%s", want, msg)
		}
	}
}

// Successes in the same turn must not reset the count: the model interleaves a
// working call with each refusal, which is why a consecutive counter never
// fired on the real run.
func TestInterleavedSuccessesDoNotResetTheCount(t *testing.T) {
	f := &runFailures{}
	for _, name := range []string{"shell_exec", "list_dir", "session_search"} {
		f.observe([]message.ToolResult{
			{Name: "read_file", Content: "fine", IsError: false},
			toolErr(name, "error: access denied"),
		})
	}
	if !f.shouldStop() {
		t.Fatal("three blocks spread across turns with successes between should still stop the run")
	}
}

// Whatever ends the run, the person is told what broke first (#229).
func TestAnnotateNamesTheFirstFailureNotTheCeiling(t *testing.T) {
	f := &runFailures{}
	f.observe([]message.ToolResult{toolErr("package_install", "error: package_install: installer failed: safety introspection returned DANGER\nmore output here")})
	f.observe([]message.ToolResult{toolErr("shell_exec", "error: requires the 'system' capability in the agent's SOUL.yaml")})

	got := f.annotate(errors.New("engine: llm call: agent token budget cannot fit the next prompt")).Error()
	if !strings.Contains(got, "token budget") {
		t.Fatalf("the ceiling should still be reported: %s", got)
	}
	if !strings.Contains(got, "package_install") || !strings.Contains(got, "DANGER") {
		t.Fatalf("the first failure should be named: %s", got)
	}
	if strings.Contains(got, "more output here") {
		t.Fatalf("only the first line of a failure belongs in a summary: %s", got)
	}

	// Nothing to add when nothing failed.
	empty := &runFailures{}
	base := errors.New("engine: run_timeout exceeded")
	if empty.annotate(base).Error() != base.Error() {
		t.Fatal("a run with no tool failure must not gain a parenthetical")
	}
	if empty.annotate(nil) != nil {
		t.Fatal("annotate(nil) must stay nil")
	}
}

// The inspection verdict binds the installer (#231).
func TestMCPInstallRefusalHonoursTheInspectionVerdict(t *testing.T) {
	const source = "https://github.com/example/needs-its-own-runtime"
	rememberMCPAdvice(source, mcpinstall.Recommendation{
		Source: source, Method: mcpinstall.MethodCompanion,
		Title:          "Deploy it as a companion service",
		Summary:        "This server has its own runtime or state dependencies.",
		Reasons:        []string{"a Dockerfile and a database were found"},
		Steps:          []string{"Create a separate service", "Register its /mcp endpoint"},
		Command:        "sy mcp add --name x --transport http --url http://private:8080/mcp",
		Alternative:    "For evaluation, force it with --allow-unverified",
		CanInstallHere: false,
	})

	refusal := mcpInstallRefusal(t.Context(), source, false)
	if refusal == "" {
		t.Fatal("an install judged unsuitable must be refused before cloning")
	}
	for _, want := range []string{"companion service", "a Dockerfile and a database", "Create a separate service", "sy mcp add"} {
		if !strings.Contains(refusal, want) {
			t.Errorf("refusal should carry the recommendation (%q), got:\n%s", want, refusal)
		}
	}

	// A trailing slash is the same source.
	if mcpInstallRefusal(t.Context(), source+"/", false) == "" {
		t.Error("the verdict should survive a trailing slash")
	}

	// The operator can still insist.
	if forced := mcpInstallRefusal(t.Context(), source, true); forced != "" {
		t.Errorf("force must override the recommendation, got: %s", forced)
	}

	// A server that belongs in the gateway is not refused.
	const ok = "https://github.com/example/self-contained"
	rememberMCPAdvice(ok, mcpinstall.Recommendation{Source: ok, Method: mcpinstall.MethodGateway, CanInstallHere: true})
	if r := mcpInstallRefusal(t.Context(), ok, false); r != "" {
		t.Errorf("a gateway-runnable server must install normally, got: %s", r)
	}
}
