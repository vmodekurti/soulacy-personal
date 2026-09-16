package runtime

import (
	"strings"
	"testing"
)

// Reported from production: the first run of a new agent showed a wall of raw
// JSON wrapped in <external_content trust="untrusted" source="…">. That
// wrapper is scaffolding for the model, and leaving it on also stopped the
// payload parsing as JSON, so the humaniser fell through to raw text.
func TestToolResultsAreShownWithoutTheUntrustedWrapper(t *testing.T) {
	wrapped := `<external_content trust="untrusted" source="mcp__maverick-mcp__market_data_get_market_overview">` +
		`{"indices":{"^GSPC":{"name":"S&P 500","price":7551.81}}}` +
		`</external_content>`

	out := humanizeToolResult(wrapped)
	if strings.Contains(out, "external_content") || strings.Contains(out, "trust=") {
		t.Fatalf("internal scaffolding reached the user:\n%s", out)
	}
	// With the wrapper gone the payload parses, so it renders as readable
	// lines rather than a wall of braces.
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("payload should be humanised once it parses, got:\n%s", out)
	}
}

func TestStripEnvelopeHandlesTheOrdinaryCases(t *testing.T) {
	cases := map[string]string{
		`<external_content trust="untrusted" source="x">hello</external_content>`: "hello",
		`plain text with no wrapper`: "plain text with no wrapper",
		``:                           "",
	}
	for in, want := range cases {
		if got := stripExternalContentEnvelope(in); got != want {
			t.Errorf("stripExternalContentEnvelope(%q) = %q, want %q", in, got, want)
		}
	}
}

// A cut-off envelope still carries the useful part; giving up would show the
// opening tag to the user.
func TestStripEnvelopeSalvagesATruncatedWrapper(t *testing.T) {
	got := stripExternalContentEnvelope(`<external_content trust="untrusted" source="x">half a payl`)
	if strings.Contains(got, "external_content") {
		t.Errorf("the opening tag must not survive: %q", got)
	}
	if got != "half a payl" {
		t.Errorf("got %q, want the payload", got)
	}
}

// Ten turns was not enough for "call a tool, then write the answer", and
// running out is what produced the raw dump.
func TestBuiltAgentsGetRoomToFinish(t *testing.T) {
	u := &BuilderUnderstanding{Name: "x", Purpose: "y"}
	m := understandingToAgentMap(u, "ollama", "m")
	turns, ok := m["max_turns"].(int)
	if !ok || turns < 20 {
		t.Fatalf("max_turns = %v; a tool call plus a write-up needs more room", m["max_turns"])
	}
}
