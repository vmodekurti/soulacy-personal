package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestUnwrapCodeEnvelope(t *testing.T) {
	wrapped := `{"code": "import datetime\ndef run(inputs):\n    return 1\n"}`
	got := unwrapCodeEnvelope(wrapped)
	if !strings.HasPrefix(got, "import datetime") || !strings.Contains(got, "def run(inputs):") {
		t.Errorf("envelope not unwrapped: %q", got)
	}
	// Raw python passes through untouched.
	raw := "def run(inputs):\n    return 1"
	if unwrapCodeEnvelope(raw) != raw {
		t.Error("raw python should pass through unchanged")
	}
	// A dict literal that isn't a code envelope is left alone.
	other := `{"x": 1}`
	if unwrapCodeEnvelope(other) != other {
		t.Error("non-code JSON should pass through")
	}
}

func TestExtractPythonCode_UnwrapsEnvelope(t *testing.T) {
	if got := extractPythonCode("```json\n{\"code\":\"def run(inputs):\\n    return 2\"}\n```"); !strings.Contains(got, "def run(inputs):") || strings.Contains(got, "\"code\"") {
		t.Errorf("fenced JSON envelope not unwrapped to raw code: %q", got)
	}
}

func TestNormalizeFlow_UnwrapsPythonNodeCode(t *testing.T) {
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "p", Kind: "python", Code: `{"code":"def run(inputs):\n    return 3"}`},
	}}}
	normalizeFlow(&d)
	if strings.Contains(d.Flow.Nodes[0].Code, "\"code\"") || !strings.Contains(d.Flow.Nodes[0].Code, "def run") {
		t.Errorf("normalizeFlow should unwrap node code: %q", d.Flow.Nodes[0].Code)
	}
}
