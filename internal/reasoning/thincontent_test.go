package reasoning

import (
	"strings"
	"testing"
)

func htmlObs(text string) string {
	return "URL: https://example.com/a\nStatus: 200\nContent-Type: text/html\n\n" +
		htmlExcerptMarker + "\n" + text
}

func TestAnnotateThinContent_PaywallTeaser(t *testing.T) {
	// The motivating case: HTTP 200, real prose, but it's a subscriber wall.
	got := annotateThinContent(htmlObs(
		"Design AI Systems That Actually Strengthen Human Judgment. " +
			strings.Repeat("Summary sentence about the article. ", 40) +
			"Subscribe to continue reading this article."))
	if !strings.Contains(got, "controller:") {
		t.Fatal("paywall teaser should be annotated even when it clears the word floor")
	}
	for _, want := range []string{"subscribe to continue", "do not re-fetch", "next candidate source"} {
		if !strings.Contains(got, want) {
			t.Errorf("annotation missing %q in: %s", want, got)
		}
	}
	// The original content must survive — a teaser can still hold the headline.
	if !strings.Contains(got, "Design AI Systems") {
		t.Error("annotation must append, never replace the fetched content")
	}
}

func TestAnnotateThinContent_ThinBody(t *testing.T) {
	got := annotateThinContent(htmlObs("Just a headline and a byline."))
	if !strings.Contains(got, "controller:") {
		t.Fatal("a body under the word floor should be annotated")
	}
	if !strings.Contains(got, "readable words") {
		t.Errorf("annotation should report the word count: %s", got)
	}
}

func TestAnnotateThinContent_LeavesGoodContentAlone(t *testing.T) {
	full := htmlObs(strings.Repeat("This is a real paragraph of article prose. ", 40))
	if got := annotateThinContent(full); got != full {
		t.Fatal("a full article must not be annotated")
	}
	// Non-HTML observations (JSON API responses, tool output) are never touched,
	// however short — a 3-word JSON reply is a legitimate result.
	for _, raw := range []string{`{"ok":true}`, "short tool output", ""} {
		if got := annotateThinContent(raw); got != raw {
			t.Errorf("non-HTML observation was modified: %q → %q", raw, got)
		}
	}
}

func TestAnnotateThinContent_DoesNotFlipConfidence(t *testing.T) {
	// The steer must not look like a tool error, or every paywalled fetch would
	// mark the whole run degraded and trip the delivery gate.
	obs := boundObservation(Observation{Content: htmlObs("Sign in to read the rest."), Source: "fetch_url"})
	if strings.HasPrefix(obs.Content, "tool error:") {
		t.Error("annotation must not masquerade as a tool error")
	}
	if obs.Source != "fetch_url" {
		t.Errorf("annotation must not rewrite provenance, got %q", obs.Source)
	}
	if containsToolErrors([]Step{{Obs: obs}}) {
		t.Error("a paywalled fetch is a redirect, not a degraded run")
	}
}
