package studio

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	reasoning "github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/internal/studio/codeclass"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// The catalog must ship the NotebookLM podcast block as the Phase-2 prototype.
func TestCompositeBlocks_CatalogHasNotebookLM(t *testing.T) {
	blocks := CompositeBlocks()
	if len(blocks) == 0 {
		t.Fatal("expected at least one composite block")
	}
	b, ok := CompositeBlockByID("notebooklm_podcast")
	if !ok {
		t.Fatal("notebooklm_podcast block not found in catalog")
	}
	if strings.TrimSpace(b.Code) == "" {
		t.Fatal("notebooklm_podcast block has empty Code")
	}
	if strings.TrimSpace(b.NodeID) == "" || strings.TrimSpace(b.OutputVar) == "" {
		t.Fatal("block must declare NodeID and OutputVar")
	}
	// Lookup is case/space tolerant.
	if _, ok := CompositeBlockByID("  NotebookLM_Podcast "); !ok {
		t.Error("CompositeBlockByID should be case/space tolerant")
	}
	if _, ok := CompositeBlockByID("nope"); ok {
		t.Error("unknown id should not match")
	}
}

// The block's public contract must be urls+title in, audio_url out — and the
// output port must carry the result's audio_url field for template-free wiring.
func TestCompositeBlocks_PortContract(t *testing.T) {
	b, _ := CompositeBlockByID("notebooklm_podcast")

	inNames := portNames(b.Inputs)
	if !inNames["urls"] || !inNames["title"] {
		t.Errorf("expected input ports urls+title, got %v", inNames)
	}
	if len(b.Outputs) != 1 || b.Outputs[0].Name != "audio_url" {
		t.Fatalf("expected single output port audio_url, got %+v", b.Outputs)
	}
	if b.Outputs[0].Field != "audio_url" {
		t.Errorf("audio_url output port should carry Field=audio_url for wire resolution, got %q", b.Outputs[0].Field)
	}
}

// Materialising the block yields a single typed-port python node that is honest
// about needing the system capability (it shells out) and compiles on its own.
func TestCompositeBlocks_MaterializeNode(t *testing.T) {
	b, _ := CompositeBlockByID("notebooklm_podcast")
	n := b.MaterializeNode()

	if n.Kind != sdkr.FlowNodePython {
		t.Errorf("expected python node, got %q", n.Kind)
	}
	if n.ID != b.NodeID || n.Output != b.OutputVar {
		t.Errorf("node id/output mismatch: %q/%q", n.ID, n.Output)
	}
	if strings.TrimSpace(n.Code) == "" {
		t.Error("materialised node has no code")
	}
	if !contains(n.Requires, codeclass.CapSystem) {
		t.Errorf("a CLI-shelling block must require %q; got %v", codeclass.CapSystem, n.Requires)
	}
	// Ports survive materialisation.
	if !portNames(n.Inputs)["urls"] || !portNames(n.Outputs)["audio_url"] {
		t.Errorf("ports not carried onto node: in=%v out=%v", portNames(n.Inputs), portNames(n.Outputs))
	}

	// The node must be a valid one-node flow on its own.
	if _, err := reasoning.CompileFlow(sdkr.FlowSpec{Nodes: []sdkr.FlowNode{n}, Entry: n.ID}); err != nil {
		t.Fatalf("materialised node failed CompileFlow: %v", err)
	}
}

// The classifier independently confirms the embedded code is beyond-guardrail
// (so the consent gate fires) — i.e. the block isn't quietly ReadOnly.
func TestCompositeBlocks_ClassifiesBeyondGuardrail(t *testing.T) {
	b, _ := CompositeBlockByID("notebooklm_podcast")
	res := codeclass.Classify(b.Code)
	if !res.Beyond() {
		t.Error("expected the NotebookLM block to be beyond guardrails (system)")
	}
}

// Grounding fires for a matching intent and stays silent otherwise, and steers
// the model away from the fine-grained template dance.
func TestCompositeBlockGrounding(t *testing.T) {
	var sb strings.Builder
	writeCompositeBlockGrounding(&sb, "every morning make a NotebookLM podcast from the top AI articles")
	g := sb.String()
	if !strings.Contains(g, "COARSE COMPOSITE BLOCKS") {
		t.Error("expected composite-block grounding for a notebooklm intent")
	}
	if !strings.Contains(g, "make_podcast") || !strings.Contains(g, "audio_url") {
		t.Errorf("grounding should name the node id and output port; got:\n%s", g)
	}
	if !strings.Contains(g, "Do NOT decompose") {
		t.Error("grounding should discourage decomposing into the template dance")
	}

	var none strings.Builder
	writeCompositeBlockGrounding(&none, "summarize my emails and send a slack message")
	if none.Len() != 0 {
		t.Errorf("expected no grounding for an unrelated intent, got:\n%s", none.String())
	}
}

// BuildPrompt wires the grounding in for a matching intent.
func TestBuildPrompt_IncludesCompositeGrounding(t *testing.T) {
	p := BuildPrompt("make a notebooklm podcast from these urls", Catalog{}, nil)
	if !strings.Contains(p, "COARSE COMPOSITE BLOCKS") {
		t.Error("BuildPrompt should include composite-block grounding for a notebooklm intent")
	}
}

// End-to-end proof the encapsulated dance actually works: run the REAL embedded
// Python against a fake `nlm` CLI that simulates create -> add -> generate ->
// poll (pending once, then ready). The block must return the audio_url. This is
// the "tested once" guarantee for the whole multi-step handoff.
func TestNotebookLMPodcastCode_RunDanceWithFakeCLI(t *testing.T) {
	py := pythonBin(t)
	dir := t.TempDir()

	// Fake CLI: tracks state in a file so audio-status returns "pending" on the
	// first poll and "ready" with a url thereafter — exercising the poll loop.
	fake := `#!/usr/bin/env python3
import sys, json, os
state = os.path.join(os.path.dirname(os.path.abspath(__file__)), "polls.txt")
cmd = sys.argv[1] if len(sys.argv) > 1 else ""
if cmd == "create-notebook":
    print(json.dumps({"id": "nb-123"}))
elif cmd == "add-source":
    print(json.dumps({"ok": True}))
elif cmd == "generate-audio":
    print(json.dumps({"status": "pending"}))
elif cmd == "audio-status":
    n = 0
    try:
        with open(state) as f: n = int(f.read() or "0")
    except Exception: n = 0
    with open(state, "w") as f: f.write(str(n + 1))
    if n == 0:
        print(json.dumps({"status": "pending"}))
    else:
        print(json.dumps({"status": "ready", "audio_url": "https://nlm.example/nb-123.mp3"}))
else:
    sys.exit(2)
`
	nlmPath := filepath.Join(dir, "nlm")
	if err := os.WriteFile(nlmPath, []byte(fake), 0o755); err != nil {
		t.Fatalf("write fake cli: %v", err)
	}

	out := runComposite(t, py, map[string]any{
		"urls":            []string{"https://a.example/1", "https://b.example/2"},
		"title":           "My Podcast",
		"poll_interval_s": 0,
		"max_wait_s":      10,
	}, map[string]string{"NLM_BIN": nlmPath})

	if got := str(out["audio_url"]); got != "https://nlm.example/nb-123.mp3" {
		t.Fatalf("expected audio_url from the dance, got %v (full: %v)", out["audio_url"], out)
	}
	if got := str(out["notebook_id"]); got != "nb-123" {
		t.Errorf("expected notebook_id nb-123, got %v", out["notebook_id"])
	}
	if n, _ := out["sources_added"].(float64); int(n) != 2 {
		t.Errorf("expected 2 sources added, got %v", out["sources_added"])
	}
	if _, isErr := out["error"]; isErr {
		t.Errorf("unexpected error: %v", out["error"])
	}
}

// The block fails gracefully (an error object, never a crash) on empty input.
func TestNotebookLMPodcastCode_GracefulOnNoUrls(t *testing.T) {
	py := pythonBin(t)
	out := runComposite(t, py, map[string]any{"urls": []string{}, "title": "x"}, nil)
	if _, ok := out["error"]; !ok {
		t.Errorf("expected an error object for empty urls, got %v", out)
	}
	if _, ok := out["audio_url"]; !ok {
		t.Error("error result should still carry an audio_url key for safe downstream access")
	}
}

// --- helpers ---

func portNames(ports []sdkr.FlowPort) map[string]bool {
	m := map[string]bool{}
	for _, p := range ports {
		m[p.Name] = true
	}
	return m
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// pythonBin finds python3 or skips the test (CI without python still passes).
func pythonBin(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	if runtime.GOOS == "windows" {
		if p, err := exec.LookPath("py"); err == nil {
			return p
		}
	}
	t.Skip("python3 not available; skipping live composite-block execution test")
	return ""
}

// runComposite executes the embedded notebookLMPodcastCode the same way the
// runtime's inline harness does: load the code, parse stdin JSON, call
// run(inputs), print the JSON result. Returns the parsed result object.
func runComposite(t *testing.T, py string, inputs map[string]any, env map[string]string) map[string]any {
	t.Helper()
	harness := "import sys, json\n" +
		notebookLMPodcastCode + "\n" +
		"_args = json.loads(sys.stdin.read() or '{}')\n" +
		"_r = run(_args)\n" +
		"print(_r if isinstance(_r, str) else json.dumps(_r))\n"

	dir := t.TempDir()
	script := filepath.Join(dir, "harness.py")
	if err := os.WriteFile(script, []byte(harness), 0o644); err != nil {
		t.Fatalf("write harness: %v", err)
	}
	in, err := json.Marshal(inputs)
	if err != nil {
		t.Fatalf("marshal inputs: %v", err)
	}

	cmd := exec.Command(py, script)
	cmd.Stdin = strings.NewReader(string(in))
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("run composite python: %v\nstderr: %s", err, stderr)
	}
	var res map[string]any
	if uerr := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &res); uerr != nil {
		t.Fatalf("composite output not a JSON object: %v\nraw: %s", uerr, out)
	}
	return res
}
