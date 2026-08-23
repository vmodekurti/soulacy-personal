package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

type recordingExecutor struct {
	calls  int
	script string
	args   []byte
	out    string
}

func (r *recordingExecutor) Run(_ context.Context, _, _, inline string, args []byte) (string, error) {
	r.calls++
	r.script = inline
	r.args = append([]byte(nil), args...)
	return r.out, nil
}
func (*recordingExecutor) Close() error { return nil }

func TestMultiUserPythonAlwaysUsesIsolatedExecutor(t *testing.T) {
	e := newMinimalEngine(t)
	worker := &recordingExecutor{out: "worker-result"}
	e.SetExecutor(worker)
	e.RequireIsolatedExecutor(true)
	def := &agent.Definition{ID: "tenant-agent", Tools: []agent.ToolDef{{
		Name: "calculate", Inline: "print('host execution must never reach this')",
	}}}

	out, err := e.runTool(context.Background(), def, "session", message.ToolCall{
		ID: "call", Name: "calculate", Arguments: map[string]any{"n": 7},
	})
	if err != nil {
		t.Fatalf("runTool: %v", err)
	}
	if out != "worker-result" || worker.calls != 1 || !strings.Contains(string(worker.args), `"n":7`) {
		t.Fatalf("isolated dispatch: out=%q calls=%d args=%s", out, worker.calls, worker.args)
	}
}

func TestMultiUserPythonFailsClosedWithoutWorker(t *testing.T) {
	e := newMinimalEngine(t)
	e.RequireIsolatedExecutor(true)
	def := &agent.Definition{ID: "tenant-agent", Tools: []agent.ToolDef{{Name: "x", Inline: "print('x')"}}}
	_, err := e.runTool(context.Background(), def, "session", message.ToolCall{ID: "call", Name: "x"})
	if err == nil || !strings.Contains(err.Error(), "isolated execution worker is unavailable") {
		t.Fatalf("error = %v, want fail-closed worker error", err)
	}
}

func TestMultiUserPythonCannotRequestLocalBackend(t *testing.T) {
	e := newMinimalEngine(t)
	e.SetExecutor(&recordingExecutor{})
	e.RequireIsolatedExecutor(true)
	def := &agent.Definition{ID: "tenant-agent", Execution: agent.ExecutionConfig{Backend: "local"}, Tools: []agent.ToolDef{{Name: "x", Inline: "print('x')"}}}
	_, err := e.runTool(context.Background(), def, "session", message.ToolCall{ID: "call", Name: "x"})
	if err == nil || !strings.Contains(err.Error(), "not permitted in a multi-user deployment") {
		t.Fatalf("error = %v, want local-backend refusal", err)
	}
}

func TestMultiUserPluginSourceIsEmbeddedIntoWorkerJob(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "handler.py")
	if err := os.WriteFile(path, []byte("def invoke(value):\n    return value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	name := "plugin__example__invoke"
	grants := []string{name}
	worker := &recordingExecutor{out: "plugin-result"}
	e := newMinimalEngine(t)
	e.pluginProvider = &fakePluginProvider{tools: []PluginTool{{Name: name, Handler: "python:" + path + "::invoke"}}}
	e.SetExecutor(worker)
	e.RequireIsolatedExecutor(true)
	def := &agent.Definition{ID: "tenant-agent", PluginTools: &grants}

	out, err := e.runTool(context.Background(), def, "session", message.ToolCall{ID: "call", Name: name, Arguments: map[string]any{"value": "ok"}})
	if err != nil {
		t.Fatalf("run plugin: %v", err)
	}
	if out != "plugin-result" || worker.calls != 1 {
		t.Fatalf("isolated plugin dispatch: out=%q calls=%d", out, worker.calls)
	}
	if strings.Contains(worker.script, path+"\")") || !strings.Contains(worker.script, "base64.b64decode") {
		t.Fatalf("worker script was not source-embedded: %s", worker.script)
	}
}
