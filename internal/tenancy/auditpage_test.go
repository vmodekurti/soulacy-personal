// auditpage_test.go — the cursor, tested where it can be: without a database.
//
// The SQL half of keyset pagination needs Postgres and is exercised by the
// integration suite. The cursor codec does not, and it is where the bugs
// actually live — an encoding that round-trips wrongly, or a decoder that
// accepts a value it did not produce, turns a pagination parameter into a
// query language nobody designed.
package tenancy

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAnAuditCursorRoundTripsExactly(t *testing.T) {
	// A timestamp with sub-second precision, because the tie-break only
	// matters for rows that share one — and a codec that truncated would make
	// the equality arm of the keyset predicate match the wrong rows.
	original := auditCursor{
		createdAt: time.Date(2026, 8, 19, 4, 5, 6, 123456789, time.UTC),
		id:        "aud_01H0ABCDEF",
	}
	decoded, err := decodeAuditCursor(encodeAuditCursor(original))
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.createdAt.Equal(original.createdAt) {
		t.Fatalf("createdAt = %v, want %v", decoded.createdAt, original.createdAt)
	}
	if decoded.id != original.id {
		t.Fatalf("id = %q, want %q", decoded.id, original.id)
	}
}

// The separator is not reserved in an audit ID, so the decoder splits into
// exactly three parts and lets the last one keep its pipes. A plain Split
// would reject a legitimate cursor for some rows and not others — a failure
// that appears long after this was written, for one customer, once.
func TestAnAuditIDContainingTheSeparatorStillRoundTrips(t *testing.T) {
	original := auditCursor{createdAt: time.Now().UTC().Truncate(time.Microsecond), id: "aud|weird|id"}
	decoded, err := decodeAuditCursor(encodeAuditCursor(original))
	if err != nil {
		t.Fatalf("a cursor for an id containing the separator was rejected: %v", err)
	}
	if decoded.id != original.id {
		t.Fatalf("id = %q, want %q", decoded.id, original.id)
	}
}

// A malformed cursor is a client bug or a tampering attempt. Answering either
// with an empty page makes both look like the end of the data, and an
// investigator would conclude the trail stopped there.
func TestAMalformedCursorIsRefusedRatherThanTreatedAsTheEnd(t *testing.T) {
	for _, bad := range []string{
		"not-base64!!",
		"",     // handled separately below — the empty cursor is the FIRST page
		"dg==", // "v" — decodes, wrong shape
		encodeAuditCursor(auditCursor{createdAt: time.Now(), id: ""}),
	} {
		if bad == "" {
			cursor, err := decodeAuditCursor(bad)
			if err != nil {
				t.Errorf("the empty cursor must mean the first page, not an error: %v", err)
			}
			if cursor.id != "" {
				t.Errorf("the empty cursor decoded to a position: %+v", cursor)
			}
			continue
		}
		if _, err := decodeAuditCursor(bad); !errors.Is(err, ErrInvalidAuditCursor) {
			t.Errorf("cursor %q was accepted (err=%v)", bad, err)
		}
	}
}

// The version prefix exists so a future change to the key is told apart from
// corruption rather than silently mis-parsed into a wrong position — which
// would return a page from the middle of the trail with no sign anything was
// wrong.
func TestACursorFromAnotherVersionIsRefused(t *testing.T) {
	encoded := encodeAuditCursor(auditCursor{createdAt: time.Now().UTC(), id: "aud_1"})
	if !strings.HasPrefix(decodeForTest(t, encoded), auditCursorVersion+"|") {
		t.Fatal("the encoding carries no version prefix")
	}
	forged := encodeRawForTest("v2|1|aud_1")
	if _, err := decodeAuditCursor(forged); !errors.Is(err, ErrInvalidAuditCursor) {
		t.Fatalf("a cursor from another version was accepted: %v", err)
	}
}

func decodeForTest(t *testing.T, encoded string) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func encodeRawForTest(raw string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}
