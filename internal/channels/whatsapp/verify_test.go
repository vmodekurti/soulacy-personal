package whatsapp

import (
	"os"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"go.uber.org/zap/zapcore"
)

func TestVerify_AcceptsTheConfiguredTokenAndRejectsEverythingElse(t *testing.T) {
	a := New("pn", "at", "the-real-verify-token", "sec", "agent", zap.NewNop())

	got, ok := a.Verify("subscribe", "the-real-verify-token", "challenge-123")
	if !ok || got != "challenge-123" {
		t.Fatalf("the correct token was rejected: ok=%v challenge=%q", ok, got)
	}
	for _, bad := range []string{"", "wrong", "the-real-verify-toke", "the-real-verify-tokenX"} {
		if _, ok := a.Verify("subscribe", bad, "c"); ok {
			t.Errorf("token %q was accepted", bad)
		}
	}
	// The mode matters too: only Meta's subscribe handshake may return the
	// challenge, even with the right token.
	if _, ok := a.Verify("unsubscribe", "the-real-verify-token", "c"); ok {
		t.Error("a non-subscribe mode was accepted")
	}
}

// The failed attempt used to be logged WITH the token in it. That writes the
// caller's guess — and, on a misconfiguration where the real token reached the
// wrong adapter, the real secret — into a log file that gets collected in
// support bundles and pasted into issue reports.
func TestVerify_DoesNotLogTheAttemptedToken(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	a := New("pn", "at", "the-real-verify-token", "sec", "agent", zap.New(core))

	a.Verify("subscribe", "a-guessed-token-value", "c")
	a.Verify("subscribe", "the-real-verify-token", "c")

	if logs.Len() == 0 {
		t.Fatal("nothing was logged at all, so this test would pass no matter what")
	}
	for _, entry := range logs.All() {
		blob := entry.Message
		for _, f := range entry.Context {
			blob += " " + f.String
		}
		for _, secret := range []string{"a-guessed-token-value", "the-real-verify-token"} {
			if strings.Contains(blob, secret) {
				t.Errorf("a verify token reached the log: %q", blob)
			}
		}
	}
}

// A timing side channel cannot be observed reliably from a unit test — a timing
// assertion here would be flaky and would fail for reasons unrelated to the
// code. So this asserts the guarantee where it actually lives: that the
// comparison is the constant-time one. The HMAC check in this same file has
// always used subtle; the verify token was the odd one out.
func TestVerify_UsesAConstantTimeComparison(t *testing.T) {
	src, err := os.ReadFile("adapter.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	i := strings.Index(body, "func (a *Adapter) Verify(")
	if i < 0 {
		t.Fatal("Verify has been renamed; this rule now guards nothing")
	}
	end := strings.Index(body[i:], "\n}\n")
	if end < 0 {
		t.Fatal("could not delimit Verify")
	}
	fn := body[i : i+end]

	if strings.Contains(fn, "token == a.verifyToken") || strings.Contains(fn, "a.verifyToken == token") {
		t.Error("the verify token is compared with ==, which is not constant-time")
	}
	if !strings.Contains(fn, "subtle.ConstantTimeCompare") {
		t.Error("Verify no longer uses a constant-time comparison")
	}
}
