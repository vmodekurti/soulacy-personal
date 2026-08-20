package redact

import "strings"

// keyname.go — the one test for "does this key name hold a credential".
//
// FOUR IMPLEMENTATIONS OF IT EXISTED, and no two agreed:
//
//	internal/redact        api_key password passwd secret token credential
//	                       authorization private_key signing_key dsn
//	                       database_url connection_string cookie
//	internal/audit         api_key password secret token credential auth
//	internal/approvals     password passwd secret token apikey accesskey
//	                       privatekey credential authorization auth bearer
//	                       cookie session signature certificate passphrase
//	                       pin otp seed mnemonic
//	internal/gateway       secret token password passwd api_key apikey
//	                       credential private_key privatekey auth
//
// Each is missing terms another has. `internal/audit` — the file an operator
// SHIPS TO SOMEBODY ELSE when asking for help — does not know about `bearer`,
// `cookie`, `passphrase` or `private_key`. `internal/redact` does not know
// about `bearer`, `accesskey` or `passphrase`. That is what a rule looks like
// when it is a convention: it is correct in the place somebody last thought
// about it and stale everywhere else.
//
// This is the mechanism, and `TestOnlyOnePackageDecidesWhatLooksSecret` fails
// the build on a fifth list.

// NORMALISATION comes before matching: separators and case are stripped, so
// `X-Api-Key`, `x_api_key` and `apiKey` are one case rather than three. Four
// spellings of the same header is exactly how a key-name list ends up with a
// hole nobody sees.
func normaliseKeyName(key string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		default:
			return -1
		}
	}, key)
}

// secretSubstrings are unambiguous: a key containing one of these holds
// credential material, wherever in the name it appears.
var secretSubstrings = []string{
	"password", "passwd", "passphrase", "secret", "token", "credential",
	"apikey", "accesskey", "privatekey", "signingkey", "authorization",
	"bearer", "cookie", "mnemonic", "connectionstring", "databaseurl",
	"oauth", "signature", "certificate", "session",
}

// secretAffixes are SHORT and ambiguous as substrings, so they match only at a
// word boundary of the normalised key — as the whole name, a prefix or a
// suffix.
//
// `pin` is the one that proves the rule. Matched as a substring it redacts
// every field called `mapping`, which contains p-i-n and nothing sensitive.
// The existing approvals redactor does exactly that today: a tool argument
// named `mapping` is elided from what an approver is shown, for no benefit at
// all. Affix matching keeps `pincode` and `userpin` and lets `mapping` through.
var secretAffixes = []string{"auth", "pin", "otp", "seed", "dsn", "key", "jwt", "sig"}

// SecretKeyName reports whether a key name looks like it holds credential
// material.
//
// IT ERRS TOWARDS REDACTING, and the trade is worth stating because it has a
// visible cost: a field named `author` starts with `auth` and is therefore
// elided. That is a lost diagnostic. Under-redacting a field named `auth` is a
// leaked credential. This package's own doc comment already chose that
// direction — "prefers losing diagnostic values over leaking credentials" —
// and this is what choosing it looks like in practice.
func SecretKeyName(key string) bool {
	normalised := normaliseKeyName(key)
	if normalised == "" {
		return false
	}
	for _, needle := range secretSubstrings {
		if strings.Contains(normalised, needle) {
			return true
		}
	}
	for _, affix := range secretAffixes {
		if normalised == affix ||
			strings.HasPrefix(normalised, affix) ||
			strings.HasSuffix(normalised, affix) {
			return true
		}
	}
	return false
}
