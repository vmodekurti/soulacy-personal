package studio

import (
	"strings"
	"testing"
)

// The compile prompt must teach the model the python node kind + the run(inputs)
// contract so intents that need glue (e.g. a local CLI) compile into a Custom
// Python node instead of inventing a non-existent tool.
func TestBuildPrompt_DocumentsPythonNode(t *testing.T) {
	p := BuildPrompt("shell out to the notebooklm CLI", Catalog{}, nil)
	for _, want := range []string{
		"python", "code", "def run(inputs):", "kind\": \"python",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
}
