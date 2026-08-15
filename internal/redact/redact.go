// Package redact provides the single structured redaction boundary used before
// sensitive runtime data is persisted or exported. It deliberately prefers
// losing diagnostic values over leaking credentials.
package redact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

const Marker = "[REDACTED]"

const maxDepth = 16

var (
	secretKey  = regexp.MustCompile(`(?i)(api[_-]?key|password|passwd|secret|token|credential|authorization|private[_-]?key|signing[_-]?key|dsn|database[_-]?url|connection[_-]?string|cookie)`)
	assignment = regexp.MustCompile(`(?i)\b(api[_-]?key|password|passwd|secret|token|authorization|credential)=([^\s&;,]+)`)
	bearer     = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]+`)
)

// Value returns a JSON-compatible deep copy with sensitive leaves replaced.
// Structs are normalized through JSON so json field names, not Go internals,
// govern the persisted representation.
func Value(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return Marker
	}
	var normalized any
	if err := json.Unmarshal(b, &normalized); err != nil {
		return Marker
	}
	return walk(normalized, "", false, 0)
}

// Map is Value specialized for the common tool-argument shape.
func Map(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out, ok := Value(in).(map[string]any)
	if !ok {
		return map[string]any{"value": Marker}
	}
	return out
}

func walk(v any, key string, wholesale bool, depth int) any {
	if depth >= maxDepth {
		return Marker
	}
	if wholesale || secretKey.MatchString(key) {
		return redactLeaves(v, depth+1)
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, child := range x {
			// These objects contain operator-supplied arbitrary names, so key
			// heuristics cannot establish that an individual value is safe.
			all := isOpaqueConfigContainer(k)
			out[k] = walk(child, k, all, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = walk(x[i], key, false, depth+1)
		}
		return out
	case string:
		return Text(x)
	default:
		return x
	}
}

func redactLeaves(v any, depth int) any {
	if depth >= maxDepth {
		return Marker
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, child := range x {
			out[k] = redactLeaves(child, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = redactLeaves(x[i], depth+1)
		}
		return out
	case nil:
		return nil
	default:
		return Marker
	}
}

func isOpaqueConfigContainer(k string) bool {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case "env", "headers", "secrets", "credentials", "vault", "provider_config", "operator_config":
		return true
	default:
		return false
	}
}

// Text removes credentials embedded in otherwise unstructured output. It
// catches labelled assignments, auth headers, credential-bearing URLs and
// opaque provider tokens while retaining ordinary diagnostics.
func Text(s string) string {
	s = assignment.ReplaceAllString(s, "$1="+Marker)
	s = bearer.ReplaceAllString(s, "$1 "+Marker)
	for _, field := range strings.Fields(s) {
		trimmed := strings.Trim(field, `"'(),[]{}<>`)
		if credentialURL(trimmed) || looksOpaque(trimmed) {
			s = strings.ReplaceAll(s, trimmed, hashMarker(trimmed))
		}
	}
	return s
}

func credentialURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.User == nil {
		return false
	}
	_, hasPassword := u.User.Password()
	return hasPassword
}

func looksOpaque(s string) bool {
	if len(s) < 20 {
		return false
	}
	l := strings.ToLower(s)
	for _, prefix := range []string{"sk-", "xoxb-", "xapp-", "ghp_", "github_pat_", "ya29.", "eyj"} {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	alnum := 0
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			alnum++
		}
	}
	return len(s) >= 48 && alnum*100/len(s) >= 85
}

func hashMarker(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "[REDACTED:" + hex.EncodeToString(sum[:])[:12] + "]"
}

// ── personal data ────────────────────────────────────────────────────────────

var (
	// personalKey names fields that carry personal data rather than
	// credentials. It is deliberately separate from secretKey: losing a
	// password from a diagnostic is free, whereas losing a user_id would make
	// a bundle useless for tracing a run, so the two sets are not interchange-
	// able and callers opt into this one.
	personalKey = regexp.MustCompile(`(?i)^(e[-_]?mail|email[-_]?address|phone|phone[-_]?number|mobile|msisdn|full[-_]?name|first[-_]?name|last[-_]?name|given[-_]?name|family[-_]?name|display[-_]?name|street|postal[-_]?code|zip|ssn|tax[-_]?id|date[-_]?of[-_]?birth|dob)$`)
	emailText   = regexp.MustCompile(`(?i)\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)
)

// PersonalText removes personal data from free text. It is applied on export
// paths — support bundles — on top of Text, never on the persistence path,
// because an operator debugging their own deployment needs their own logs
// intact.
func PersonalText(s string) string {
	return emailText.ReplaceAllString(Text(s), "***REDACTED-EMAIL***")
}

// ValueForExport is Value plus personal-data removal. Support bundles leave
// the deployment and are routinely handed to a vendor, so they get the
// stricter treatment: MU-019 requires them to redact secrets *and* personal
// data, and those are different sets.
func ValueForExport(v any) any {
	return scrubPersonal(Value(v))
}

func scrubPersonal(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			if personalKey.MatchString(strings.TrimSpace(key)) {
				out[key] = "***REDACTED***"
				continue
			}
			out[key] = scrubPersonal(value)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = scrubPersonal(item)
		}
		return out
	case string:
		return emailText.ReplaceAllString(typed, "***REDACTED-EMAIL***")
	default:
		return v
	}
}
