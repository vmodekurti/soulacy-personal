package process

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// The inline harness must: pass stdin JSON to run(inputs) as one arg, print the
// JSON-encoded return value, and quarantine stray top-level prints to stderr so
// they don't corrupt the captured output. (Studio Custom Python contract.)
func TestRunInlineHarness(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	ex := New("python3")

	out, err := ex.Run(context.Background(), "", "run",
		"def run(inputs):\n    return {'echo': inputs.get('x', 0) + 1}",
		[]byte(`{"x": 41}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, `"echo"`) || !strings.Contains(out, "42") {
		t.Fatalf("unexpected output: %q", out)
	}

	// A top-level print during load must NOT leak into the result.
	out2, err := ex.Run(context.Background(), "", "run",
		"print('loading...')\ndef run(i):\n    return 'ok'",
		[]byte(`{}`))
	if err != nil {
		t.Fatalf("Run(2): %v", err)
	}
	if strings.TrimSpace(out2) != "ok" {
		t.Fatalf("stray print leaked into output: %q", out2)
	}
}

func TestRunInlineHarnessPreservesLargeSingleLineJSON(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	ex := New("python3")

	out, err := ex.Run(context.Background(), "", "run",
		"def run(inputs):\n    return {'text': 'x' * (128 * 1024), 'count': 1}",
		[]byte(`{}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var got struct {
		Text  string `json:"text"`
		Count int    `json:"count"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("large output is not valid JSON: %v", err)
	}
	if len(got.Text) != 128*1024 || got.Count != 1 {
		t.Fatalf("large output was truncated: text=%d count=%d", len(got.Text), got.Count)
	}
}
