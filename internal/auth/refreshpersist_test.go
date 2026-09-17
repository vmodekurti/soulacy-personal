package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The bug this exists for: restarting the gateway signed every device out.
// The phone kept working for the access token's fifteen minutes, then tried to
// refresh, found the store wiped, and demanded a fresh sign-in — for a deploy,
// a reboot, or a config change.
func TestARefreshTokenSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "refresh-tokens.json")

	before := newRefreshStore(path)
	tok := before.put("vasu", "v@example.com", "admin", time.Now().Add(720*time.Hour))
	before.close()

	// A new process, the same workspace.
	after := newRefreshStore(path)
	defer after.close()
	subject, email, role, ok := after.get(tok)
	if !ok {
		t.Fatal("the token issued before the restart was rejected after it — this is the sign-out")
	}
	if subject != "vasu" || email != "v@example.com" || role != "admin" {
		t.Errorf("identity did not survive: %q %q %q", subject, email, role)
	}
}

// Rotation still holds across a restart: a refresh token is single-use, and
// persistence must not turn it into a reusable one.
func TestARestoredTokenIsStillSingleUse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "refresh-tokens.json")
	before := newRefreshStore(path)
	tok := before.put("vasu", "", "admin", time.Now().Add(time.Hour))
	before.close()

	after := newRefreshStore(path)
	defer after.close()
	if _, _, _, ok := after.get(tok); !ok {
		t.Fatal("first use should work")
	}
	if _, _, _, ok := after.get(tok); ok {
		t.Error("a used refresh token must not work twice")
	}

	// And the rotation is on disk, not just in memory.
	third := newRefreshStore(path)
	defer third.close()
	if _, _, _, ok := third.get(tok); ok {
		t.Error("a token spent before the restart came back to life after it")
	}
}

// The file is a list of session credentials. Storing the tokens themselves
// would make it directly replayable, for the same reason passwords are not
// stored either.
func TestTheFileHoldsNoUsableToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "refresh-tokens.json")
	s := newRefreshStore(path)
	defer s.close()
	tok := s.put("vasu", "v@example.com", "admin", time.Now().Add(time.Hour))

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if strings.Contains(string(raw), tok) {
		t.Error("the refresh token itself is on disk — a stolen file would be a stolen session")
	}
	var stored map[string]persistedEntry
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("store is not readable json: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("expected one entry, got %d", len(stored))
	}
	for _, e := range stored {
		if e.Subject != "vasu" {
			t.Errorf("the identity should be recorded: %+v", e)
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions = %o, want 600 for session credentials", perm)
	}
}

// A token that expired while the gateway was down is not resurrected by the
// restart.
func TestExpiredTokensDoNotComeBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "refresh-tokens.json")
	before := newRefreshStore(path)
	tok := before.put("vasu", "", "admin", time.Now().Add(-time.Minute))
	before.close()

	after := newRefreshStore(path)
	defer after.close()
	if _, _, _, ok := after.get(tok); ok {
		t.Error("an expired token was restored")
	}
}

// A corrupt or unreadable file costs one sign-in. Refusing to start would take
// the whole gateway down over a cache, which is worse than the bug this fixes.
func TestACorruptStoreDoesNotStopTheGateway(t *testing.T) {
	path := filepath.Join(t.TempDir(), "refresh-tokens.json")
	if err := os.WriteFile(path, []byte("{this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newRefreshStore(path)
	defer s.close()

	// It still works from here, which is the part that matters.
	tok := s.put("vasu", "", "admin", time.Now().Add(time.Hour))
	if _, _, _, ok := s.get(tok); !ok {
		t.Error("the store should be usable after ignoring a corrupt file")
	}
}

// No path means the old behaviour, deliberately: an install with no workspace
// to write to should still run.
func TestNoPathKeepsTokensInMemory(t *testing.T) {
	s := newRefreshStore("")
	defer s.close()
	tok := s.put("vasu", "", "admin", time.Now().Add(time.Hour))
	if _, _, _, ok := s.get(tok); !ok {
		t.Error("an in-memory store should still work within one process")
	}
}

// The issuer is what the HTTP layer uses, so the guarantee is asserted there
// too rather than only on the store underneath it.
func TestIssuerRefreshWorksAcrossARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "refresh-tokens.json")

	first, err := newIssuer("a-stable-secret", 15*time.Minute, 720*time.Hour, path)
	if err != nil {
		t.Fatal(err)
	}
	_, refresh, _, err := first.Issue("vasu", "v@example.com", "admin")
	if err != nil {
		t.Fatal(err)
	}
	first.Close()

	second, err := newIssuer("a-stable-secret", 15*time.Minute, 720*time.Hour, path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	access, _, _, err := second.Refresh(refresh)
	if err != nil {
		t.Fatalf("refreshing after a restart failed: %v", err)
	}
	if access == "" {
		t.Error("no access token was issued")
	}
}
