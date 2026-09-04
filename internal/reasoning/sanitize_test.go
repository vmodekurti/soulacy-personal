package reasoning

import (
	"strings"
	"testing"
)

func TestSanitize_PassesThroughRealAnswer(t *testing.T) {
	ans := "Here is your playlist: 20 Carnatic jazz-fusion tracks added."
	if got := SanitizeFinalOutput(ans, nil); got != ans {
		t.Fatalf("real answer was altered: %q", got)
	}
}

func TestSanitize_ExtractsFinalAnswerFromControlJSON(t *testing.T) {
	in := `{"thought":"done now","is_done":true,"final_answer":"Playlist created with 20 tracks."}`
	got := SanitizeFinalOutput(in, nil)
	if got != "Playlist created with 20 tracks." {
		t.Fatalf("expected extracted final_answer, got %q", got)
	}
}

func TestSanitize_ExtractsOutputField(t *testing.T) {
	in := `{"output":"All done."}`
	if got := SanitizeFinalOutput(in, nil); got != "All done." {
		t.Fatalf("expected answer envelope to unwrap, got %q", got)
	}
	in2 := `{"thought":"finishing","action":null,"output":"All done."}`
	if got := SanitizeFinalOutput(in2, nil); got != "All done." {
		t.Fatalf("expected extracted output, got %q", got)
	}
}

func TestSanitize_FencedAnswerEnvelope(t *testing.T) {
	in := "```json\n{\"output\":\"## Report\\n\\nUseful markdown.\"}\n```"
	if got := SanitizeFinalOutput(in, nil); got != "## Report\n\nUseful markdown." {
		t.Fatalf("expected fenced answer envelope to unwrap, got %q", got)
	}
}

func TestSanitize_EnvelopeWithMetadata(t *testing.T) {
	in := `{"reply":"Queued for processing.","confidence":"high","updated_rules":""}`
	if got := SanitizeFinalOutput(in, nil); got != "Queued for processing." {
		t.Fatalf("expected reply envelope to unwrap, got %q", got)
	}
}

func TestSanitize_LeakedStepWithNoAnswer_FallsBack(t *testing.T) {
	in := `{"thought":"Now I'll search spotify","is_done":false,"action":{"tool":"python_eval","input":{"code":"import requests"}}}`
	got := SanitizeFinalOutput(in, nil)
	if strings.Contains(got, "python_eval") || strings.Contains(got, "\"thought\"") {
		t.Fatalf("control JSON leaked into output: %q", got)
	}
	if !strings.Contains(strings.ToLower(got), "reasoning") {
		t.Fatalf("expected graceful fallback message, got %q", got)
	}
}

func TestSanitize_LeakedStep_UsesReadableObservation(t *testing.T) {
	steps := []Step{
		{Thought: "search", Obs: Observation{Content: "Found 20 tracks; playlist URL: https://open.spotify.com/x"}},
	}
	in := `{"thought":"continuing","is_done":false,"action":{"tool":"python_eval"}}`
	got := SanitizeFinalOutput(in, steps)
	if !strings.Contains(got, "Found 20 tracks") {
		t.Fatalf("expected fallback to readable observation, got %q", got)
	}
}

// A leaked step whose observation is structured JSON should be compacted into a
// readable summary, never dumped verbatim as {"query":…,"results":[…]}.
func TestSanitize_LeakedStep_SummarizesRawJSONObservation(t *testing.T) {
	steps := []Step{
		{Thought: "search", Action: ToolCall{Tool: "web_search"}, Obs: Observation{Source: "web_search", Content: `{"query":"SNDK","result_count":5,"results":[{"title":"SNDK Stock Analysis","url":"https://example.com/sndk"}]}`}},
	}
	in := `{"thought":"continuing","is_done":false,"action":{"tool":"web_search"}}`
	got := SanitizeFinalOutput(in, steps)
	if strings.Contains(got, `"results"`) || strings.HasPrefix(strings.TrimSpace(got), "{") {
		t.Fatalf("raw JSON observation was surfaced as the answer: %q", got)
	}
	if !strings.Contains(got, "Search returned 5 result(s) for SNDK") || !strings.Contains(got, "SNDK Stock Analysis") {
		t.Fatalf("expected compact search summary, got %q", got)
	}
}

func TestSanitize_PlaceholderFinalAnswerFallsBackToSearchSummary(t *testing.T) {
	steps := []Step{
		{Thought: "search", Action: ToolCall{Tool: "web_search"}, Obs: Observation{Source: "web_search", Content: `{"query":"best stocks","result_count":2,"results":[{"title":"33 Undervalued Stocks to Buy Before the Market Catches On","url":"https://morningstar.example/stocks"},{"title":"3 New Analyst Picks for Q3","url":"https://morningstar.example/q3"}]}`}},
		{Thought: "load skill", Action: ToolCall{Tool: "read_skill"}, Obs: Observation{Source: "read_skill", Content: `<skill_content name="yfinance-data">Do not show this internal instruction.</skill_content>`}},
	}
	in := `{"thought":"done","is_done":true,"final_answer":"answer"}`
	got := SanitizeFinalOutput(in, steps)
	if got == "answer" || strings.Contains(got, "<skill_content") || strings.Contains(got, "Do not show this") {
		t.Fatalf("placeholder or instructional content leaked: %q", got)
	}
	if !strings.Contains(got, "Search returned 2 result(s) for best stocks") || !strings.Contains(got, "33 Undervalued Stocks") {
		t.Fatalf("expected fallback to search summary, got %q", got)
	}
}

func TestSanitize_BarePlaceholderOutputFallsBack(t *testing.T) {
	steps := []Step{
		{Thought: "tool", Obs: Observation{Source: "web_search", Content: "Found relevant analyst picks for the user's portfolio question."}},
	}
	got := SanitizeFinalOutput(" answer ", steps)
	if got == "answer" || !strings.Contains(got, "Found relevant analyst picks") {
		t.Fatalf("expected placeholder output to fall back to observation, got %q", got)
	}
}

// A credential/cookie file that a tool read must NEVER be surfaced as the reply
// (it once leaked a whole cookies.txt to the delivery channel).
func TestSanitize_LeakedStep_SkipsCookieFile(t *testing.T) {
	steps := []Step{
		{Thought: "read cookies", Obs: Observation{Content: "# Netscape HTTP Cookie File\n.hbr.org\tTRUE\t/\tTRUE\t0\tsession\tSECRETVALUE"}},
	}
	in := `{"thought":"continuing","is_done":false,"action":{"tool":"read_file"}}`
	got := SanitizeFinalOutput(in, steps)
	if strings.Contains(got, "SECRETVALUE") || strings.Contains(got, "Netscape") {
		t.Fatalf("cookie file leaked into the reply: %q", got)
	}
}

func TestSanitize_FencedControlJSON(t *testing.T) {
	in := "```json\n{\"thought\":\"x\",\"is_done\":false,\"action\":{\"tool\":\"t\"}}\n```"
	got := SanitizeFinalOutput(in, nil)
	if strings.Contains(got, "thought") {
		t.Fatalf("fenced control JSON leaked: %q", got)
	}
}

func TestSanitize_EmptyFallsBack(t *testing.T) {
	if got := SanitizeFinalOutput("   ", nil); !strings.Contains(strings.ToLower(got), "reasoning") {
		t.Fatalf("empty output should yield graceful message, got %q", got)
	}
}

func TestSanitize_LegitJSONAnswerPreserved(t *testing.T) {
	// An agent legitimately returning JSON data (no control fields) is untouched.
	in := `{"name":"Alice","score":42}`
	if got := SanitizeFinalOutput(in, nil); got != in {
		t.Fatalf("legit JSON answer was altered: %q", got)
	}
}

func TestSanitize_PendingAsyncStatusFallsBack(t *testing.T) {
	in := `{"status":"success","notebook_id":"nb_123","summary":{"total":1,"completed":0,"in_progress":1},"artifacts":[{"artifact_id":"audio_1","status":"in_progress","audio_url":null}]}`
	steps := []Step{
		{ID: "audio", Action: ToolCall{Tool: "mcp__notebooklm__studio_create"}, Obs: Observation{Source: "mcp__notebooklm__studio_create", Content: `{"status":"success","artifact_id":"audio_1"}`}},
		{ID: "poll", Action: ToolCall{Tool: "mcp__notebooklm__studio_status"}, Obs: Observation{Source: "mcp__notebooklm__studio_status", Content: in}},
	}
	got := SanitizeFinalOutput(in, steps)
	low := strings.ToLower(got)
	if strings.HasPrefix(strings.TrimSpace(got), "{") || strings.Contains(got, `"in_progress"`) {
		t.Fatalf("pending async status leaked into output: %q", got)
	}
	if !strings.Contains(low, "still processing") || !strings.Contains(low, "raw status payload") {
		t.Fatalf("expected async incomplete fallback, got %q", got)
	}
}

func TestSanitize_CompletedAsyncJSONPreserved(t *testing.T) {
	in := `{"status":"success","summary":{"total":1,"completed":1,"in_progress":0},"artifacts":[{"artifact_id":"audio_1","status":"completed","audio_url":"https://example.com/audio.mp3"}]}`
	if got := SanitizeFinalOutput(in, nil); got != in {
		t.Fatalf("completed structured payload was altered: %q", got)
	}
}

func TestSanitizeControlOutput_PreservesStructuredAnswerJSON(t *testing.T) {
	in := `{"answer":"fixed"}`
	if got := SanitizeControlOutput(in, nil); got != in {
		t.Fatalf("structured JSON answer was altered: %q", got)
	}

	control := `{"thought":"done","is_done":true,"final_answer":"Human answer."}`
	if got := SanitizeControlOutput(control, nil); got != "Human answer." {
		t.Fatalf("control JSON was not unwrapped: %q", got)
	}
}

// Regression: a control envelope with trailing prose after the closing brace
// must still be unwrapped — the old whole-string check let the raw
// {"thought":…,"final_answer":…} leak to the user (the "SNDK" chat bug).
func TestSanitize_ExtractsEnvelopeWithTrailingText(t *testing.T) {
	in := `{"thought":"analysis complete","is_done":true,"final_answer":"## Verdict\n\nDo not enter now."}` +
		"\n\nHope that helps!"
	got := SanitizeFinalOutput(in, nil)
	if got != "## Verdict\n\nDo not enter now." {
		t.Fatalf("expected unwrapped final_answer, got %q", got)
	}
}

// An envelope wrapped in leading prose is also recovered.
func TestSanitize_ExtractsEnvelopeWithLeadingText(t *testing.T) {
	in := "Here is my answer: " +
		`{"thought":"ok","is_done":true,"final_answer":"The stock is a hold."}`
	got := SanitizeFinalOutput(in, nil)
	if got != "The stock is a hold." {
		t.Fatalf("expected unwrapped final_answer, got %q", got)
	}
}

// The final_answer may itself contain braces (e.g. JSON/code samples); brace
// balancing must not stop early.
func TestSanitize_EnvelopeWithBracesInAnswer(t *testing.T) {
	in := `{"thought":"x","is_done":true,"final_answer":"Use {\"a\":1} as the body."} trailing`
	got := SanitizeFinalOutput(in, nil)
	if got != `Use {"a":1} as the body.` {
		t.Fatalf("brace balancing failed, got %q", got)
	}
}

func TestSanitize_ExtractsControlEnvelopeAfterProviderMetadata(t *testing.T) {
	in := `{"id":"chatcmpl-1","object":"chat.completion"}` +
		"\n" +
		`{"thought":"I have enough data","is_done":true,"final_answer":"## Best Stocks\n\n| Ticker | Allocation |\n| --- | ---: |\n| V | $8,000 |"}`
	got := SanitizeFinalOutput(in, nil)
	if !strings.HasPrefix(got, "## Best Stocks") || strings.Contains(got, `"thought"`) {
		t.Fatalf("expected control envelope after metadata to unwrap, got %q", got)
	}
}

func TestSanitize_TolerantMalformedControlJSON(t *testing.T) {
	in := `{"thought":"done","is_done":true,"final_answer":"## Report\n\nUse this answer." trailing malformed`
	got := SanitizeFinalOutput(in, nil)
	if got != "## Report\n\nUse this answer." {
		t.Fatalf("expected tolerant final_answer extraction, got %q", got)
	}
}
