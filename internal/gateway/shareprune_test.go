package gateway

import (
	"github.com/soulacy/soulacy/internal/wsroot"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Nothing ever removed a share. There was no expiry, no per-caller cap and no
// delete route, and POST /chat/share is gated on chat:READ — the weakest
// permission in the table. A single authenticated viewer could grow the disk
// indefinitely within the rate limiter's budget.
func TestPruneShares_DropsExpiredSnapshots(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "11111111-1111-1111-1111-111111111111.json")
	fresh := filepath.Join(dir, "22222222-2222-2222-2222-222222222222.json")
	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-shareTTL - time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	pruneShares(dir, wsroot.PersonalWorkspaceID)

	if _, err := os.Stat(stale); err == nil {
		t.Error("an expired share survived the prune")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("a live share was deleted: %v", err)
	}
}

// The TTL alone does not bound anything: shares can be created far faster than
// a month retires them. The count cap is what actually holds.
func TestPruneShares_EnforcesTheCountCapByAge(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	for i := 0; i < maxShares+50; i++ {
		p := filepath.Join(dir, strings.Repeat("a", 8)+"-0000-0000-0000-00000000"+pad(i)+".json")
		if err := os.WriteFile(p, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		ts := base.Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	pruneShares(dir, wsroot.PersonalWorkspaceID)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) >= maxShares+50 {
		t.Fatalf("%d shares remain — the cap did nothing", len(entries))
	}
	if len(entries) > maxShares {
		t.Fatalf("%d shares remain, over the cap of %d", len(entries), maxShares)
	}
}

// And the prune has to actually be wired into the write path, not merely exist.
func TestCreateShare_PrunesBeforeWriting(t *testing.T) {
	src := readGatewaySource(t, "share.go")
	line := findLine(t, src, "pruneShares(dir, workspaceID)")
	if strings.TrimSpace(line) != "pruneShares(dir, workspaceID)" {
		t.Fatalf("unexpected prune call site: %s", line)
	}
}

func pad(i int) string {
	s := "0000" + strconv.Itoa(i)
	return s[len(s)-4:]
}
