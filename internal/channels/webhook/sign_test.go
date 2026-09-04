package webhook

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

// A deliberately low-entropy, obviously-fake fixture. The old value
// ("s3cr3t-signing-key") was leetspeak assigned to an identifier named
// "secret", which is indistinguishable from a real leaked credential to a
// scanner — and the point of a secret scanner is that it does not have to
// guess. Test data should never look like the thing it is testing.
const signingFixture = "not-a-real-key-test-fixture"

func TestVerifySignature_AcceptsAGenuineSignature(t *testing.T) {
	body := []byte(`{"text":"hello"}`)
	ts := time.Now().Unix()
	sig := Sign(signingFixture, ts, body)

	if err := VerifySignature(signingFixture, strconv.FormatInt(ts, 10), sig, body, DefaultTolerance); err != nil {
		t.Fatalf("a genuine signature must verify, got: %v", err)
	}
}

// The whole point: someone who doesn't hold the secret cannot forge a payload.
func TestVerifySignature_RejectsForgeryWithWrongSecret(t *testing.T) {
	body := []byte(`{"text":"hello"}`)
	ts := time.Now().Unix()
	forged := Sign("attacker-guessed-key", ts, body)

	err := VerifySignature(signingFixture, strconv.FormatInt(ts, 10), forged, body, DefaultTolerance)
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("a signature made with the wrong secret must be rejected, got: %v", err)
	}
}

// A payload altered in flight must fail, even though the signature is otherwise
// well-formed and fresh.
func TestVerifySignature_RejectsTamperedBody(t *testing.T) {
	original := []byte(`{"amount":10}`)
	ts := time.Now().Unix()
	sig := Sign(signingFixture, ts, original)

	tampered := []byte(`{"amount":1000000}`)
	err := VerifySignature(signingFixture, strconv.FormatInt(ts, 10), sig, tampered, DefaultTolerance)
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("a tampered body must be rejected, got: %v", err)
	}
}

// Replay protection: a captured-but-old request must not be accepted.
func TestVerifySignature_RejectsStaleReplay(t *testing.T) {
	body := []byte(`{"text":"replay me"}`)
	old := time.Now().Add(-30 * time.Minute).Unix()
	sig := Sign(signingFixture, old, body) // genuinely signed — just old

	err := VerifySignature(signingFixture, strconv.FormatInt(old, 10), sig, body, DefaultTolerance)
	if !errors.Is(err, ErrStaleSignature) {
		t.Fatalf("a stale (replayed) request must be rejected, got: %v", err)
	}
}

// An attacker cannot lift a captured body and re-stamp it with a fresh
// timestamp — the timestamp is bound into the MAC.
func TestVerifySignature_TimestampIsBoundIntoTheMAC(t *testing.T) {
	body := []byte(`{"text":"captured"}`)
	captured := time.Now().Add(-30 * time.Minute).Unix()
	sig := Sign(signingFixture, captured, body)

	// Re-present the same signature under a *fresh* timestamp to dodge the
	// staleness check. It must still fail, because the MAC covers the timestamp.
	fresh := strconv.FormatInt(time.Now().Unix(), 10)
	err := VerifySignature(signingFixture, fresh, sig, body, DefaultTolerance)
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("re-stamping a captured signature must fail, got: %v", err)
	}
}

// A far-future timestamp is just as suspect as a stale one.
func TestVerifySignature_RejectsFarFutureTimestamp(t *testing.T) {
	body := []byte(`{"text":"x"}`)
	future := time.Now().Add(2 * time.Hour).Unix()
	sig := Sign(signingFixture, future, body)

	err := VerifySignature(signingFixture, strconv.FormatInt(future, 10), sig, body, DefaultTolerance)
	if !errors.Is(err, ErrStaleSignature) {
		t.Fatalf("a far-future timestamp must be rejected, got: %v", err)
	}
}

// Enabling a secret must not silently fail open when the sender omits a signature.
func TestVerifySignature_MissingSignatureDoesNotFailOpen(t *testing.T) {
	body := []byte(`{"text":"unsigned"}`)
	ts := strconv.FormatInt(time.Now().Unix(), 10)

	if err := VerifySignature(signingFixture, ts, "", body, DefaultTolerance); !errors.Is(err, ErrNoSignature) {
		t.Fatalf("an unsigned request must be rejected when a secret is configured, got: %v", err)
	}
}

func TestVerifySignature_MalformedInputs(t *testing.T) {
	body := []byte(`{}`)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	good := Sign(signingFixture, time.Now().Unix(), body)

	if err := VerifySignature("", ts, good, body, DefaultTolerance); !errors.Is(err, ErrSecretNotSet) {
		t.Errorf("no secret → ErrSecretNotSet, got %v", err)
	}
	if err := VerifySignature(signingFixture, "not-a-number", good, body, DefaultTolerance); !errors.Is(err, ErrBadTimestamp) {
		t.Errorf("bad timestamp → ErrBadTimestamp, got %v", err)
	}
	if err := VerifySignature(signingFixture, ts, "sha256=zzzz", body, DefaultTolerance); !errors.Is(err, ErrBadSignature) {
		t.Errorf("non-hex digest → ErrBadSignature, got %v", err)
	}
	// A bare hex digest (no "sha256=" prefix) is also accepted.
	now := time.Now().Unix()
	bare := Sign(signingFixture, now, body)[len("sha256="):]
	if err := VerifySignature(signingFixture, strconv.FormatInt(now, 10), bare, body, DefaultTolerance); err != nil {
		t.Errorf("a bare hex digest should verify, got %v", err)
	}
}

// End-to-end: the adapter must actually SIGN what it sends, and a receiver
// verifying with the shared secret must accept it.
func TestAdapter_SendsAVerifiableSignature(t *testing.T) {
	type captured struct {
		ts, sig string
		body    []byte
	}
	got := make(chan captured, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		got <- captured{ts: r.Header.Get(HeaderTimestamp), sig: r.Header.Get(HeaderSignature), body: buf}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a, err := New("webhook", srv.URL, "POST", nil, "", signingFixture, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	msg := message.Message{Parts: message.Text("hello from soulacy")}
	if err := a.Send(t.Context(), msg); err != nil {
		t.Fatalf("send: %v", err)
	}

	c := <-got
	if c.sig == "" || c.ts == "" {
		t.Fatal("adapter did not sign the request")
	}
	if err := VerifySignature(signingFixture, c.ts, c.sig, c.body, DefaultTolerance); err != nil {
		t.Fatalf("the receiver could not verify the adapter's signature: %v", err)
	}
	// And a receiver holding a DIFFERENT secret must reject it.
	if err := VerifySignature("someone-elses-key", c.ts, c.sig, c.body, DefaultTolerance); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("a wrong-secret receiver must reject, got: %v", err)
	}
}

// Back-compat: with no secret configured, requests go out unsigned exactly as
// before (enabling signing must be opt-in, not a breaking change).
func TestAdapter_UnsignedWhenNoSecretConfigured(t *testing.T) {
	seen := make(chan http.Header, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a, err := New("webhook", srv.URL, "POST", nil, "", "", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Send(t.Context(), message.Message{Parts: message.Text("hi")}); err != nil {
		t.Fatal(err)
	}
	h := <-seen
	if h.Get(HeaderSignature) != "" || h.Get(HeaderTimestamp) != "" {
		t.Error("no secret configured → the request must be unsigned (back-compat)")
	}
}
