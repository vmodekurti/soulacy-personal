package studio

// scrub.go — remove secrets from captured examples (P0-3).
//
// Studio captures real tool output to ground generation and to feed repair
// prompts. The existing "redaction" (redactSample / redactLeaves) truncates
// long strings and long arrays — a SIZE limiter, not secret detection. A short
// secret sits under the truncation threshold and passes through untouched, into
// an LLM prompt and into a stored corpus case.
//
// This scrubs by two independent signals, because either alone misses:
//
//	by KEY   — a field named api_key/token/authorization/password is redacted
//	           whatever it contains, since the name is the operator's own
//	           statement of what it holds.
//	by SHAPE — a value that LOOKS like a credential (bearer token, JWT, AWS
//	           key id, long high-entropy opaque string, URL userinfo) is
//	           redacted whatever it is called, because a captured example from
//	           a third-party API will not use our naming conventions.
//
// Deliberately biased toward over-redaction: a redacted field costs the model
// one piece of grounding, while a leaked one is unrecoverable once it is in a
// prompt or a saved corpus.

import (
	"regexp"
	"strings"
)

// RedactedMarker replaces any value identified as secret.
const RedactedMarker = "[redacted]"

var secretKeyMarkers = []string{
	"password", "passwd", "secret", "token", "api_key", "apikey", "api-key",
	"authorization", "auth", "cookie", "credential", "private_key", "privatekey",
	"access_key", "accesskey", "session", "signature", "client_secret",
	"refresh_token", "id_token", "bearer",
}

var (
	// Bearer/JWT/AWS-style and generic long opaque credentials.
	reBearer = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._\-]{8,}`)
	reJWT    = regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]{5,}\b`)
	reAWSKey = regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)
	// Vendor-prefixed keys: sk-…, ghp_…, xoxb-…, and similar.
	reVendorKey = regexp.MustCompile(`\b(sk|pk|rk)-[A-Za-z0-9]{16,}\b|\bgh[pousr]_[A-Za-z0-9]{16,}\b|\bxox[bapsr]-[A-Za-z0-9\-]{10,}\b`)
	// URL userinfo: https://user:password@host
	reURLUserinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)[^/\s:@]+:[^/\s@]+@`)
)

// IsSecretKey reports whether a field NAME declares that it holds a credential.
func IsSecretKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	if k == "" {
		return false
	}
	for _, m := range secretKeyMarkers {
		if strings.Contains(k, m) {
			return true
		}
	}
	return false
}

// LooksLikeSecret reports whether a VALUE has the shape of a credential,
// regardless of the field it arrived under.
func LooksLikeSecret(v string) bool {
	s := strings.TrimSpace(v)
	if len(s) < 12 {
		return false // too short to be a meaningful credential; avoids false hits
	}
	if reJWT.MatchString(s) || reAWSKey.MatchString(s) || reVendorKey.MatchString(s) ||
		reBearer.MatchString(s) || reURLUserinfo.MatchString(s) {
		return true
	}
	// A long, unbroken, mixed-case+digit opaque run with no spaces is very
	// likely a key. Requiring all three classes keeps prose, URLs, and ids out.
	if len(s) >= 32 && !strings.ContainsAny(s, " \t\n") {
		var upper, lower, digit bool
		for _, r := range s {
			switch {
			case r >= 'A' && r <= 'Z':
				upper = true
			case r >= 'a' && r <= 'z':
				lower = true
			case r >= '0' && r <= '9':
				digit = true
			}
		}
		if upper && lower && digit {
			return true
		}
	}
	return false
}

// ScrubString redacts credential-shaped substrings inside free text, leaving
// the surrounding prose intact so the example still teaches shape.
func ScrubString(s string) string {
	if s == "" {
		return s
	}
	out := reURLUserinfo.ReplaceAllString(s, "$1"+RedactedMarker+"@")
	out = reBearer.ReplaceAllString(out, "Bearer "+RedactedMarker)
	out = reJWT.ReplaceAllString(out, RedactedMarker)
	out = reAWSKey.ReplaceAllString(out, RedactedMarker)
	out = reVendorKey.ReplaceAllString(out, RedactedMarker)
	if LooksLikeSecret(out) {
		return RedactedMarker
	}
	return out
}

// ScrubValue walks a decoded JSON value and redacts secrets by key and by
// shape. Structure is preserved — a captured example must still show the SHAPE
// of a response, which is the whole reason it was captured.
func ScrubValue(v any) any {
	return scrubValue("", v, 0)
}

func scrubValue(key string, v any, depth int) any {
	if depth > 12 {
		return v
	}
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if IsSecretKey(k) {
				out[k] = RedactedMarker
				continue
			}
			out[k] = scrubValue(k, val, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = scrubValue(key, item, depth+1)
		}
		return out
	case string:
		if IsSecretKey(key) {
			return RedactedMarker
		}
		return ScrubString(t)
	default:
		return v
	}
}
