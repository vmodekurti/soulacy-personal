package approvals

import (
	"fmt"
	"strings"
)

// redact.go — what an approver is shown.
//
// An approver needs enough to judge and no more. The arguments of a paused
// tool call are, by construction, the most sensitive payload the system holds:
// they are the things something decided were dangerous enough to stop. They
// routinely contain the very material MU-015 encrypts at rest — an API key
// being passed to an HTTP call, a connection string, a token in a header.
//
// Storing them verbatim would put that material in a second durable place with
// none of the vault's protections, readable by every eligible approver and by
// anything that later reads the approvals table for a different reason. So the
// durable record holds a redacted view and the full arguments stay only in the
// process that is blocked on the answer.
//
// The redaction is DENY-BY-DEFAULT ON SHAPE, not a list of bad key names. A
// blocklist of "password, token, secret" fails on the first argument called
// `authorization`, `pat`, `bearer`, `x-api-key` or `cookie` — and it fails
// silently, which is the worst way for a redactor to fail. Instead: short
// scalars are shown, long ones are summarised, and anything whose key looks
// credential-ish is elided regardless of length.

// maxShownRunes is the longest scalar shown verbatim. Chosen so a path, a URL,
// a table name or a short command survives intact — the things an approver
// actually reads to make the decision — while a pasted key, a token, a base64
// blob or a document body does not.
const maxShownRunes = 120

// Redact returns the display form of a tool call's arguments.
func Redact(args map[string]any) map[string]any {
	if len(args) == 0 {
		return nil
	}
	out := make(map[string]any, len(args))
	for key, value := range args {
		if looksCredentialish(key) {
			out[key] = "[redacted]"
			continue
		}
		out[key] = redactValue(value)
	}
	return out
}

func redactValue(value any) any {
	switch typed := value.(type) {
	case string:
		return truncate(typed)
	case map[string]any:
		return Redact(typed)
	case []any:
		// Elements are redacted but the length is kept: "how many files is
		// this about to delete" is exactly the kind of thing an approver is
		// being asked to judge, and it is not itself sensitive.
		out := make([]any, 0, len(typed))
		for _, element := range typed {
			out = append(out, redactValue(element))
		}
		return out
	case nil, bool, float64, float32,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return typed
	default:
		// An unrecognised shape is summarised rather than rendered. A type
		// this function has not been taught about is exactly the one whose
		// String() might spill something.
		return fmt.Sprintf("[%T]", typed)
	}
}

func truncate(s string) string {
	runes := []rune(s)
	if len(runes) <= maxShownRunes {
		return s
	}
	return string(runes[:maxShownRunes]) + fmt.Sprintf("… [%d more characters]", len(runes)-maxShownRunes)
}

// credentialish are substrings that make a key name suspicious. Matching is on
// the NORMALISED key (lowercased, separators stripped) so `X-Api-Key`,
// `x_api_key` and `apiKey` are one case rather than three.
//
// This list makes the redactor stricter, never more permissive: a key it does
// not recognise is still length-limited. It exists so that a short credential
// — and most are short — is elided rather than shown in full.
var credentialish = []string{
	"password", "passwd", "secret", "token", "apikey", "accesskey", "privatekey",
	"credential", "authorization", "auth", "bearer", "cookie", "session",
	"signature", "certificate", "passphrase", "pin", "otp", "seed", "mnemonic",
}

func looksCredentialish(key string) bool {
	normalised := strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		default:
			return -1
		}
	}, key)
	for _, needle := range credentialish {
		if strings.Contains(normalised, needle) {
			return true
		}
	}
	return false
}
