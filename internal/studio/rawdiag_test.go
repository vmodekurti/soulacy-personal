package studio

import (
	"strings"
	"testing"
)

func TestDiagnoseRawOutput_Truncated(t *testing.T) {
	// A workflow JSON that got cut off mid-object — the signature of a response
	// that hit a context/token ceiling. This is the case a bigger model does NOT
	// fix, so it must be classified distinctly.
	raw := `{"name":"Stock Price","flow":{"nodes":[{"id":"fetch","kind":"tool","tool":"fetch_url"},{"id":"fmt","kind":"python","code":"def run(`
	d := DiagnoseRawOutput(raw)
	if d.Kind != "truncated" {
		t.Fatalf("expected truncated, got %q (%s)", d.Kind, d.Reason)
	}
	if !strings.Contains(strings.ToLower(d.Reason), "cut off") {
		t.Errorf("reason should say the output was cut off: %s", d.Reason)
	}
	if d.Chars == 0 || d.Excerpt == "" {
		t.Errorf("expected the raw output to be preserved as evidence")
	}
}

func TestDiagnoseRawOutput_Prose(t *testing.T) {
	d := DiagnoseRawOutput("Sure! Here is how I would build that workflow: first fetch the ticker...")
	if d.Kind != "prose" {
		t.Fatalf("expected prose, got %q", d.Kind)
	}
}

func TestDiagnoseRawOutput_Empty(t *testing.T) {
	if d := DiagnoseRawOutput("   \n  "); d.Kind != "empty" {
		t.Fatalf("expected empty, got %q", d.Kind)
	}
}

func TestDiagnoseRawOutput_Malformed(t *testing.T) {
	// Balanced braces but structurally invalid JSON (the '{' -as-key slip).
	d := DiagnoseRawOutput(`{"flow": {{"id": "a"}}}`)
	if d.Kind != "malformed" {
		t.Fatalf("expected malformed, got %q (%s)", d.Kind, d.Reason)
	}
}

// A complete, well-formed object must NOT be reported as truncated.
func TestLooksTruncated_CompleteJSON(t *testing.T) {
	if looksTruncated(`{"a":1,"b":[1,2,{"c":"}"}]}`) {
		t.Error("a complete object (with braces inside strings) must not look truncated")
	}
}

// Braces inside string literals must not confuse the depth counter.
func TestLooksTruncated_BracesInsideStrings(t *testing.T) {
	if looksTruncated(`{"code":"def run(i): return {'x': 1}"}`) {
		t.Error("braces inside a string must not be counted as structure")
	}
	if !looksTruncated(`{"code":"def run(i): return {'x': 1}"`) {
		t.Error("a genuinely unclosed object must be flagged")
	}
}

// An unterminated string is also a truncation signature.
func TestLooksTruncated_UnterminatedString(t *testing.T) {
	if !looksTruncated(`{"name":"Stock Pri`) {
		t.Error("an unterminated string means the response was cut off")
	}
}
