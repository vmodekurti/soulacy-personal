// rawdiag.go — classify what the builder model ACTUALLY returned when a draft
// fails to parse.
//
// "invalid character '{' looking for beginning of object key string" tells an
// operator nothing. The useful question is: did the model return prose? nothing
// at all? valid-looking JSON that got cut off mid-object? Each has a different
// fix, and until now none of them were visible — the raw output was discarded.
//
// DiagnoseRawOutput turns the raw completion into a plain-English classification
// plus a short excerpt, so the failure is evidence-based instead of guesswork.
package studio

import (
	"strings"
	"unicode/utf8"
)

// RawDiagnosis describes what the model returned.
type RawDiagnosis struct {
	// Kind is a stable classification: empty | prose | truncated | malformed.
	Kind string
	// Reason is a plain-English explanation with the concrete fix.
	Reason string
	// Excerpt is a short, safe sample of the raw output.
	Excerpt string
	// Chars is the total length of the raw output.
	Chars int
}

// DiagnoseRawOutput classifies a builder-model completion that failed to parse.
func DiagnoseRawOutput(raw string) RawDiagnosis {
	trimmed := strings.TrimSpace(stripFences(raw))
	d := RawDiagnosis{Chars: utf8.RuneCountInString(raw), Excerpt: excerpt(trimmed, 300)}

	switch {
	case trimmed == "":
		d.Kind = "empty"
		d.Reason = "The model returned nothing at all. This usually means the request failed upstream or the model ran out of room to answer."
		return d

	case !strings.Contains(trimmed, "{"):
		d.Kind = "prose"
		d.Reason = "The model replied with prose instead of JSON — it ignored the schema entirely. Use a model that supports structured output, or a stronger instruction-following model."
		return d

	case looksTruncated(trimmed):
		d.Kind = "truncated"
		d.Reason = "The model's JSON was CUT OFF mid-object — the response ended before the workflow was complete. This is a length/context limit, not a reasoning failure: raise the context window (num_ctx) and/or the max output tokens so the whole workflow fits."
		return d

	default:
		d.Kind = "malformed"
		d.Reason = "The model produced JSON-like output that isn't valid JSON."
		return d
	}
}

// looksTruncated reports whether the output began a JSON object but never closed
// it — the signature of a response that hit a token/context ceiling.
func looksTruncated(s string) bool {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return false
	}
	// Walk the JSON, tracking depth outside of strings. If we end with unclosed
	// braces/brackets, the response was cut off.
	depth := 0
	inStr := false
	escaped := false
	for _, r := range s[start:] {
		switch {
		case escaped:
			escaped = false
		case r == '\\' && inStr:
			escaped = true
		case r == '"':
			inStr = !inStr
		case inStr:
			// ignore structural chars inside strings
		case r == '{', r == '[':
			depth++
		case r == '}', r == ']':
			depth--
		}
	}
	// Unclosed structure (or an unterminated string) ⇒ truncated.
	return depth > 0 || inStr
}

// excerpt returns at most n runes of s, marking any elision.
func excerpt(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n]) + "…"
}
