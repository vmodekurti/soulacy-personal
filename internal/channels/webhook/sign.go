// sign.go — signed webhooks.
//
// An unsigned outbound webhook is unauthenticated: the receiver has no way to
// know a payload actually came from Soulacy, and anyone who learns the endpoint
// URL can forge agent output into a downstream system. Signing closes that.
//
// The scheme is the industry-standard one (Stripe/GitHub/Slack shape):
//
//	X-Soulacy-Timestamp: 1730000000
//	X-Soulacy-Signature: sha256=<hex HMAC-SHA256(secret, "<timestamp>.<body>")>
//
// The timestamp is INSIDE the signed payload, which is what makes replay
// protection meaningful — an attacker can't take a captured request and change
// its timestamp without invalidating the signature. Verification compares in
// constant time and rejects anything outside a tolerance window.
//
// Signing is opt-in: no secret configured → no signature headers → existing
// deployments are unaffected.

package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Signature header names.
const (
	HeaderTimestamp = "X-Soulacy-Timestamp"
	HeaderSignature = "X-Soulacy-Signature"
)

// DefaultTolerance is how far a request's timestamp may drift from now before
// it is rejected as a replay. Generous enough for clock skew, tight enough that
// a captured payload isn't replayable forever.
const DefaultTolerance = 5 * time.Minute

// Errors returned by VerifySignature. They are distinct so a receiver can tell
// "you forgot to sign" from "this is a forgery" from "this is a replay".
var (
	ErrNoSignature      = errors.New("webhook: request carries no signature")
	ErrBadSignature     = errors.New("webhook: signature does not match — the payload is not from this sender, or it was tampered with")
	ErrBadTimestamp     = errors.New("webhook: signature timestamp is missing or malformed")
	ErrStaleSignature   = errors.New("webhook: signature timestamp is outside the tolerance window (possible replay)")
	ErrSecretNotSet     = errors.New("webhook: no signing secret configured")
	errUnknownAlgorithm = errors.New("webhook: unsupported signature algorithm")
)

// signingPayload is what actually gets signed: timestamp, a dot, then the exact
// bytes of the body. Binding the timestamp into the MAC is what prevents an
// attacker from replaying a captured body under a fresh timestamp.
func signingPayload(ts int64, body []byte) []byte {
	prefix := strconv.FormatInt(ts, 10) + "."
	out := make([]byte, 0, len(prefix)+len(body))
	out = append(out, prefix...)
	out = append(out, body...)
	return out
}

// Sign returns the header value for a body at a given time.
func Sign(secret string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(signingPayload(ts, body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature checks a received webhook. Receivers (including Soulacy's own
// inbound handlers) should call this before trusting a payload.
//
// It is deliberately strict: a missing signature is an error rather than a pass,
// so enabling a secret can't silently fail open.
func VerifySignature(secret, timestamp, signature string, body []byte, tolerance time.Duration) error {
	if strings.TrimSpace(secret) == "" {
		return ErrSecretNotSet
	}
	if strings.TrimSpace(signature) == "" {
		return ErrNoSignature
	}
	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}

	ts, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return ErrBadTimestamp
	}
	// Reject both stale AND far-future timestamps — a clock-skewed forgery is
	// still a forgery.
	drift := time.Since(time.Unix(ts, 0))
	if drift < 0 {
		drift = -drift
	}
	if drift > tolerance {
		return fmt.Errorf("%w: drifted %s (tolerance %s)", ErrStaleSignature, drift.Round(time.Second), tolerance)
	}

	// Accept "sha256=<hex>" or a bare hex digest.
	got := strings.TrimSpace(signature)
	if alg, rest, found := strings.Cut(got, "="); found {
		if !strings.EqualFold(strings.TrimSpace(alg), "sha256") {
			return errUnknownAlgorithm
		}
		got = strings.TrimSpace(rest)
	}
	gotRaw, err := hex.DecodeString(got)
	if err != nil {
		return ErrBadSignature
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(signingPayload(ts, body))
	want := mac.Sum(nil)

	// Constant-time compare: a byte-by-byte comparison would leak the correct
	// signature one byte at a time under a timing attack.
	if !hmac.Equal(gotRaw, want) {
		return ErrBadSignature
	}
	return nil
}
