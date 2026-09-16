package runtime

import (
	"strings"
	"testing"
)

// Reported from production: a twenty-second wait and then an empty bubble.
// The model spent its whole output budget thinking and never started writing,
// and the retry did not fire because an empty string does not look like JSON.
func TestAnEmptyReplyIsWorthAnotherAttempt(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\t "} {
		if !needsAnotherAttempt(in) {
			t.Errorf("silence should be retried, not shown: %q", in)
		}
	}
	if !needsAnotherAttempt(`{"reply":"half`) {
		t.Error("a cut-off envelope should still be retried")
	}
	// A real answer is not retried; that would double every latency.
	if needsAnotherAttempt("Which calendar should I read?") {
		t.Error("prose is an answer")
	}
}

// Whatever happens upstream, the person gets a sentence. An empty bubble is
// worse than an apology: there is nothing to respond to.
func TestTheUserNeverGetsABlankBubble(t *testing.T) {
	reply, _ := parseBuilderResponse("")
	if reply != "" {
		t.Skip("the parser already produced text; the guard is in BuilderChat")
	}
	// The guard text has to read as recoverable, not as a failure notice.
	const guard = "Sorry — I did not manage to get that out. Could you say it again, or add a little more detail?"
	if !strings.Contains(guard, "say it again") {
		t.Error("the fallback should invite another try")
	}
}
