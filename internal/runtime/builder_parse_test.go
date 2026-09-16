package runtime

import (
	"strings"
	"testing"
)

// The production failure: the model's JSON was cut off mid-object, so nothing
// parsed and the raw fragment was handed back as the assistant's reply. The
// user saw a wall of braces and the conversation could never complete.
func TestTruncatedJSONNeverReachesTheUser(t *testing.T) {
	truncated := `{"reply":"Happy to set that up. What industry should I track?","understanding":{"name":"industry-news-digest","`

	reply, u := parseBuilderResponse(truncated)
	if strings.Contains(reply, "{") || strings.Contains(reply, "understanding") {
		t.Fatalf("raw JSON leaked to the user: %q", reply)
	}
	if reply == "" {
		t.Error("a blank bubble is no better than a broken one")
	}
	if u != nil {
		t.Error("a truncated reply yields no understanding; the caller keeps the last good one")
	}
}

func TestWellFormedJSONStillParses(t *testing.T) {
	good := `{"reply":"What industry should I track?","understanding":{"name":"news","confidence":0.4}}`
	reply, u := parseBuilderResponse(good)
	if reply != "What industry should I track?" {
		t.Errorf("reply = %q", reply)
	}
	if u == nil || u.Name != "news" {
		t.Fatalf("understanding not parsed: %+v", u)
	}
}

func TestFencedJSONStillParses(t *testing.T) {
	fenced := "```json\n{\"reply\":\"Hello\",\"understanding\":{\"name\":\"x\"}}\n```"
	reply, u := parseBuilderResponse(fenced)
	if reply != "Hello" || u == nil {
		t.Fatalf("reply=%q understanding=%+v", reply, u)
	}
}

// Plain prose is not a failed JSON reply and must still be shown as written.
func TestPlainProseIsPassedThrough(t *testing.T) {
	reply, u := parseBuilderResponse("Which calendar should I read?")
	if reply != "Which calendar should I read?" {
		t.Errorf("reply = %q", reply)
	}
	if u != nil {
		t.Error("no understanding in plain prose")
	}
}

func TestLooksLikeJSONRecognisesTheEnvelope(t *testing.T) {
	for _, in := range []string{`{"reply":"x"`, "  {\"a\":1", "```json\n{\"a\":1"} {
		if !looksLikeJSON(in) {
			t.Errorf("%q should look like the JSON envelope", in)
		}
	}
	for _, in := range []string{"What industry?", "", "REPLY: hello"} {
		if looksLikeJSON(in) {
			t.Errorf("%q is prose, not JSON", in)
		}
	}
}

// The prompt must not ask the model to reproduce the shared contract: the
// generator prepends it, and copying it spends most of the output budget,
// which is what truncated the reply in production.
func TestPromptDoesNotAskTheModelToRepeatTheSharedContract(t *testing.T) {
	if strings.Contains(builderSystemPrompt, "Begin every system_prompt with the shared") {
		t.Error("the model is being told to duplicate the contract it does not need to write")
	}
	if !strings.Contains(builderSystemPrompt, "prepended automatically") {
		t.Error("the prompt should tell the model the contract is added for it")
	}
}
