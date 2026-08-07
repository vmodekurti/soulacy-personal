package studio

// A newline in the wrong place must not cost the user their whole workflow.
//
// JSON requires a newline inside a string to be written \n. Models routinely
// emit the real byte, because the values that carry newlines are exactly the
// ones they write most naturally: a system prompt, a node input holding an
// embedded document, a multi-line description. One raw newline invalidates the
// entire object.
//
// Observed live, generating a multi-agent workflow. The transcript read:
//
//	Falling back to the deterministic planner:
//	studio: parse agent spec: invalid character '\n' in string literal
//
// and the four-agent graph the user had described was replaced by a two-node
// skeleton with no agents in it. The model had done the work; it was discarded
// over quoting.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseDraft_SurvivesARawNewlineInsideAString(t *testing.T) {
	raw := "{\"name\":\"Digest\",\"system_prompt\":\"You are an editor.\nRank the tickers.\nBe brief.\",\"flow\":{\"entry\":\"n\",\"nodes\":[{\"id\":\"n\",\"kind\":\"llm\",\"input\":\"go\",\"output\":\"out\"}]}}"

	d, err := ParseDraft(raw)
	if err != nil {
		t.Fatalf("a whole workflow was thrown away over an unescaped newline: %v", err)
	}
	if d.Name != "Digest" {
		t.Errorf("name = %q", d.Name)
	}
	// The newlines must survive as newlines — repairing the quoting must not
	// flatten the prompt into one line.
	if !strings.Contains(d.SystemPrompt, "\nRank the tickers.\n") {
		t.Errorf("the prompt's line breaks were lost in the repair: %q", d.SystemPrompt)
	}
}

func TestParseAgentSpec_SurvivesARawNewlineInsideAString(t *testing.T) {
	raw := "{\"name\":\"Briefing\",\"system_prompt\":\"Line one.\nLine two.\",\"strategy\":\"plan_execute\"}"
	p, err := parseAgentSpec(raw)
	if err != nil {
		t.Fatalf("the agent spec was discarded over an unescaped newline: %v", err)
	}
	if p.Name != "Briefing" {
		t.Errorf("name = %q", p.Name)
	}
}

func TestEscapeRawControlChars_LeavesValidJSONAlone(t *testing.T) {
	// The repair runs only after a failed parse, but it must still be a no-op on
	// anything already valid — otherwise a retry could change the meaning of a
	// document that was fine.
	for _, valid := range []string{
		`{"a":"already \n escaped","b":[1,2,3]}`,
		`{"quote":"she said \"hi\"","tab":"a\tb"}`,
		`{"backslash":"c:\\path\\to","empty":""}`,
		`{"nested":{"deep":["x","y"]}}`,
	} {
		if got := escapeRawControlChars(valid); got != valid {
			t.Errorf("rewrote valid JSON:\n  in:  %s\n  out: %s", valid, got)
		}
	}
}

// An escaped quote must not be read as the end of the string. Getting this
// wrong would corrupt every value after the first \" in the document — the
// repair would then be worse than the failure it fixes.
func TestEscapeRawControlChars_TracksEscapedQuotes(t *testing.T) {
	raw := "{\"a\":\"he said \\\"go\\\" then\nstopped\",\"b\":\"plain\"}"
	var out map[string]string
	if err := json.Unmarshal([]byte(escapeRawControlChars(raw)), &out); err != nil {
		t.Fatalf("repair produced invalid JSON: %v", err)
	}
	if out["a"] != "he said \"go\" then\nstopped" {
		t.Errorf("value a corrupted: %q", out["a"])
	}
	if out["b"] != "plain" {
		t.Errorf("value b corrupted: %q", out["b"])
	}
}

// A newline BETWEEN tokens is legal JSON formatting and must be left alone —
// only bytes inside a string literal are the parser's problem.
func TestEscapeRawControlChars_LeavesStructuralWhitespace(t *testing.T) {
	pretty := "{\n  \"a\": 1,\n  \"b\": 2\n}"
	if got := escapeRawControlChars(pretty); got != pretty {
		t.Errorf("mangled pretty-printed JSON:\n%s", got)
	}
}

func TestEscapeRawControlChars_HandlesTabsAndReturns(t *testing.T) {
	raw := "{\"a\":\"x\ty\rz\"}"
	var out map[string]string
	if err := json.Unmarshal([]byte(escapeRawControlChars(raw)), &out); err != nil {
		t.Fatalf("repair produced invalid JSON: %v", err)
	}
	if out["a"] != "x\ty\rz" {
		t.Errorf("got %q", out["a"])
	}
}

// Genuinely broken JSON must still fail. A repair that swallows real errors
// turns a loud failure into a silently wrong draft.
func TestParseDraft_StillRejectsRealGarbage(t *testing.T) {
	if _, err := ParseDraft(`{"name":"Digest","flow":`); err == nil {
		t.Fatal("truncated JSON parsed as a draft")
	}
	if _, err := ParseDraft(`there is no object here`); err == nil {
		t.Fatal("prose parsed as a draft")
	}
}
