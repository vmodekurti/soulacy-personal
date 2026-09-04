package gateway

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/studio"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func draftWithPython(code string) studio.Draft {
	var d studio.Draft
	d.Flow.Nodes = []sdkr.FlowNode{{ID: "py", Kind: sdkr.FlowNodePython, Code: code}}
	return d
}

func TestValidatePythonNodes(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	srv := newTestGateway(t, "secret")

	// Valid: no findings.
	if errs := srv.validatePythonNodes(draftWithPython("def run(inputs):\n    return {\"ok\": True}\n")); len(errs) != 0 {
		t.Fatalf("valid python should have no findings, got %+v", errs)
	}

	// Syntax error → a finding mentioning syntax.
	errs := srv.validatePythonNodes(draftWithPython("def run(inputs)\n    return 1\n"))
	if len(errs) != 1 || !strings.Contains(strings.ToLower(errs[0].Message), "syntax") {
		t.Fatalf("expected a syntax-error finding, got %+v", errs)
	}
	if errs[0].NodeID != "py" {
		t.Fatalf("finding should carry the node id, got %q", errs[0].NodeID)
	}

	// Missing run(inputs) → a finding requiring run.
	errs = srv.validatePythonNodes(draftWithPython("def helper():\n    return 1\n"))
	if len(errs) != 1 || !strings.Contains(errs[0].Message, "run(inputs)") {
		t.Fatalf("expected a missing-run finding, got %+v", errs)
	}

	// A python node that references a deployed tool (no inline code) is skipped.
	var d studio.Draft
	d.Flow.Nodes = []sdkr.FlowNode{{ID: "t", Kind: sdkr.FlowNodePython, Tool: "some_py_tool"}}
	if errs := srv.validatePythonNodes(d); len(errs) != 0 {
		t.Fatalf("tool-backed python node should be skipped, got %+v", errs)
	}
}
