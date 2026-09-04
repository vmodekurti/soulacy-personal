package runtime

import "testing"

// A raw newline inside a JSON string literal (as produced when an upstream
// agent's multi-line output is templated into a tool-args JSON template) must
// be repaired and parsed, not rejected.
func TestParseToolArgs_RepairsNewlineInStringLiteral(t *testing.T) {
	in := "{\"query\": \"current price of AAPL\nstock\"}"
	args, err := parseToolArgs(in)
	if err != nil {
		t.Fatalf("expected repair to succeed, got error: %v", err)
	}
	got, _ := args["query"].(string)
	if want := "current price of AAPL\nstock"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
}

func TestParseToolArgs_ValidJSONUnchanged(t *testing.T) {
	in := `{"query": "AAPL", "n": 3}`
	args, err := parseToolArgs(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if args["query"] != "AAPL" {
		t.Fatalf("query = %v", args["query"])
	}
}

// Tabs and carriage returns inside string literals are also repaired.
func TestRepairJSONControlChars(t *testing.T) {
	in := "{\"a\": \"x\ty\rz\"}"
	out := repairJSONControlChars(in)
	if out == in {
		t.Fatal("expected repair to change the input")
	}
	if _, err := parseToolArgs(in); err != nil {
		t.Fatalf("repaired input should parse: %v", err)
	}
}

// Structural newlines (between tokens, outside string literals) are valid JSON
// and must be left alone — repair is a no-op that returns the input unchanged.
func TestRepairJSONControlChars_NoopOutsideStrings(t *testing.T) {
	in := "{\n  \"a\": 1\n}"
	if out := repairJSONControlChars(in); out != in {
		t.Fatalf("structural whitespace should be untouched; got %q", out)
	}
}

// Already-escaped sequences must not be double-escaped.
func TestRepairJSONControlChars_PreservesEscapes(t *testing.T) {
	in := `{"a": "line1\nline2"}`
	if out := repairJSONControlChars(in); out != in {
		t.Fatalf("already-valid escapes should be untouched; got %q", out)
	}
}
