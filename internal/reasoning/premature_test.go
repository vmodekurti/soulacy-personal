package reasoning

import (
	"strings"
	"testing"
)

func TestIsPrematureFinalAnswer(t *testing.T) {
	premature := []string{
		"I'll scout both routes in parallel — searching for current fares and recent fare-drop news.",
		"Searching for current fares now.",
		"Let me now gather the data.",
		"I'm going to check each competitor and report back.",
		"Next, I will summarize the findings.",
		"Proceeding to step 2.",
		"Gathering the latest earnings figures.",
		"Looking for the latest earnings figures.",
		"First, I need to search the web.",
	}
	for _, s := range premature {
		if !isPrematureFinalAnswer(s) {
			t.Fatalf("expected premature for: %q", s)
		}
	}

	realAnswers := []string{
		"Here are the best fares I found: SFO→NRT $612 round trip on ANA (Sep 12–22), booked via Google Flights. SFO→LHR $498 on United (weekend of Oct 4).",
		"The playlist was created with 20 Carnatic jazz-fusion tracks. URL: https://open.spotify.com/playlist/abc",
		"No significant fare drops this week on either route.",
		"## Market brief\n\n- NVDA: guidance raised (source).\n- AAPL: no material change.",
		"Searching returned 20 results, and the top three are summarized below with prices and booking links so you can decide quickly and confidently today.", // long + substantive
	}
	for _, s := range realAnswers {
		if isPrematureFinalAnswer(s) {
			t.Fatalf("did NOT expect premature for: %q", s)
		}
	}

	// A real multi-paragraph deliverable that merely contains progress-y phrases
	// ("I will", "let me") in its prose must NOT be flagged premature — doing so
	// discarded good briefings and degraded the run.
	realWithProgressWords := []string{
		"## AI Articles Podcast Briefing\n\nThis week's theme is enterprise AI adoption.\n\n" +
			"First, HBR reports executives will lean on AI copilots. Let me highlight the key takeaway: measurable ROI.\n\n" +
			"Second, MIT Technology Review covers what's next for AI regulation.",
		strings.Repeat("A thorough analysis. ", 45) + "I will note the caveats above are important.",
	}
	for _, s := range realWithProgressWords {
		if isPrematureFinalAnswer(s) {
			t.Fatalf("multi-paragraph/long deliverable wrongly flagged premature: %q", s)
		}
	}
}
