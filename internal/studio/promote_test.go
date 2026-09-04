package studio

import (
	"strings"
	"testing"
)

func TestBuildPromotedTool(t *testing.T) {
	code := "import subprocess\ndef run(inputs):\n    return subprocess.run(['echo', inputs['x']]).returncode"
	pt, err := BuildPromotedTool("NotebookLM: add sources!", code)
	if err != nil {
		t.Fatalf("BuildPromotedTool: %v", err)
	}
	// Identifier-safe name (no spaces/punctuation), used for fn + filename.
	if pt.Name != "notebooklm_add_sources" {
		t.Fatalf("Name = %q", pt.Name)
	}
	if pt.Filename != "notebooklm_add_sources.py" {
		t.Fatalf("Filename = %q", pt.Filename)
	}
	// Entry function named after the tool, forwarding kwargs to run(inputs).
	if !strings.Contains(pt.Contents, "def notebooklm_add_sources(**inputs):") {
		t.Fatalf("missing entry fn:\n%s", pt.Contents)
	}
	if !strings.Contains(pt.Contents, "return run(inputs)") {
		t.Fatalf("missing adapter call:\n%s", pt.Contents)
	}
	// Original body preserved.
	if !strings.Contains(pt.Contents, "import subprocess") || !strings.Contains(pt.Contents, "def run(inputs):") {
		t.Fatalf("original body not preserved:\n%s", pt.Contents)
	}
}

func TestBuildPromotedTool_Errors(t *testing.T) {
	if _, err := BuildPromotedTool("ok", ""); err == nil {
		t.Fatal("empty code must error")
	}
	if _, err := BuildPromotedTool("ok", "x = 1"); err == nil {
		t.Fatal("code without run() must error")
	}
	if _, err := BuildPromotedTool("!!!", "def run(i):\n    return i"); err == nil {
		t.Fatal("nameless tool must error")
	}
	// Leading-digit name gets a safe prefix.
	pt, err := BuildPromotedTool("3m sync", "def run(i):\n    return i")
	if err != nil || pt.Name != "_3m_sync" {
		t.Fatalf("leading-digit handling: name=%q err=%v", pt.Name, err)
	}
}
