package reasoning

// thincontent.go detects the "successful fetch, useless content" case — a page
// that returns HTTP 200 with a paywall teaser, a consent wall, or a JavaScript
// shell instead of the article the loop asked for.
//
// The tool layer cannot see this: fetch_url only errors on StatusCode >= 400,
// so a 200 + subscriber teaser is indistinguishable from a real article to the
// executor. The model then has to infer "this source is dead" from the content
// alone, and typically re-fetches two or three more URLs on the same paywalled
// domain before giving up — burning steps that were budgeted for real work.
//
// So the controller says it explicitly. When a fetched HTML observation
// carries too little readable text (or carries paywall markers outright), a
// bounded note is APPENDED to the observation telling the loop to treat the
// source as unavailable and move to the next candidate. The note never
// replaces the content (a short page can still be the answer), never changes
// the observation's Source, and never marks the step as a tool error — the run
// is not degraded by this, it is redirected.

import (
	"fmt"
	"strings"
)

// thinContentWordFloor is the readable-word count below which a fetched HTML
// page is treated as unusable for article-scale work. Deliberately low: a
// genuine short page (a status blurb, a definition, a press release) must not
// be flagged, so this catches teasers and consent walls rather than merely
// brief articles.
const thinContentWordFloor = 120

// htmlExcerptMarker is the prefix compactObservation puts in front of text it
// extracted from an HTML body. Its presence is what makes an observation a
// fetched-page observation — no tool name needed, so this works for fetch_url,
// http_request, and any future fetching tool identically.
const htmlExcerptMarker = "HTML fetched; readable text excerpt:"

// paywallMarkers are phrases that identify a subscription/consent interstitial
// even when it carries enough words to clear the floor. Matched case-folded
// against the extracted text only (never the raw HTML), so boilerplate in a
// script tag or a footer link cannot trigger them.
var paywallMarkers = []string{
	"subscribe to continue",
	"subscribe to read",
	"already a subscriber",
	"already have an account",
	"sign in to read",
	"sign in to continue",
	"log in to continue",
	"become a member to",
	"this article is for subscribers",
	"subscribers only",
	"to continue reading",
	"continue reading this article",
	"start your free trial",
	"you have reached your article limit",
	"free articles remaining",
	"enable javascript",
	"please enable cookies",
	"verify you are human",
	"checking your browser",
}

// annotateThinContent appends a controller steer to a fetched-page observation
// whose readable text is too thin to work with, or that is plainly a paywall /
// interstitial. Returns content unchanged in every other case.
func annotateThinContent(content string) string {
	idx := strings.Index(content, htmlExcerptMarker)
	if idx < 0 {
		return content
	}
	excerpt := strings.TrimSpace(content[idx+len(htmlExcerptMarker):])
	if excerpt == "" {
		return content
	}
	words := len(strings.Fields(excerpt))
	marker := matchedPaywallMarker(excerpt)
	if marker == "" && words >= thinContentWordFloor {
		return content
	}

	var reason string
	switch {
	case marker != "":
		reason = fmt.Sprintf("the page returned HTTP 200 but its text is an access interstitial (matched %q) with only %d readable words", marker, words)
	default:
		reason = fmt.Sprintf("the page returned HTTP 200 but only %d readable words — too thin to be the full article", words)
	}
	return content + "\n\ncontroller: " + reason +
		". The fetch SUCCEEDED, so retrying it will return the same thing. Treat this URL and other article pages on the same domain as unavailable for this run:" +
		" do not re-fetch them. Move on to the next candidate source you already identified, and if none remain, search for an accessible alternative before concluding."
}

// matchedPaywallMarker returns the first paywall/interstitial phrase present in
// the extracted text, or "" when none match.
func matchedPaywallMarker(excerpt string) string {
	lower := strings.ToLower(excerpt)
	for _, m := range paywallMarkers {
		if strings.Contains(lower, m) {
			return m
		}
	}
	return ""
}
