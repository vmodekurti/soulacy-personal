package reasoning

import (
	"strings"
	"testing"
)

func TestRecoverThinkResponseFromRawAcceptsPlainTextFinalAnswer(t *testing.T) {
	raw := `Here is the summary:

- The first article is about AI governance.
- The second article should be processed tomorrow.`

	got, ok := recoverThinkResponseFromRaw(raw, []string{"fetch_url"})
	if !ok {
		t.Fatal("expected plain text final answer recovery")
	}
	if !got.IsDone {
		t.Fatalf("IsDone = false, want true")
	}
	if !strings.Contains(got.FinalAnswer, "AI governance") {
		t.Fatalf("FinalAnswer = %q", got.FinalAnswer)
	}
}

func TestRecoverThinkResponseFromRawRejectsProgressNotes(t *testing.T) {
	raw := `I'll fetch the article and then summarize it.`

	if got, ok := recoverThinkResponseFromRaw(raw, []string{"fetch_url"}); ok {
		t.Fatalf("unexpected recovery for progress note: %+v", got)
	}
}

func TestRecoverThinkResponseFromRawRejectsMalformedControlPayload(t *testing.T) {
	raw := `{"thought":"need fetch","is_done":false,"action":`

	if got, ok := recoverThinkResponseFromRaw(raw, []string{"fetch_url"}); ok {
		t.Fatalf("unexpected recovery for malformed control payload: %+v", got)
	}
}
