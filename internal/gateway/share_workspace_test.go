// share_workspace_test.go — the isolation and lifecycle contract for shared
// conversations (MU-019).
//
// A share is deliberately readable by anyone holding its token; that is the
// feature. What was missing is everything around it: a tenant could not see
// which of their conversations had been published, could not withdraw one, and
// the act of publishing left no audit trail. Creating one needs only
// chat:READ — the weakest permission in the table.
package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/wsroot"
)

func writeShare(t *testing.T, dir string, snap sharedSession) {
	t.Helper()
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, snap.Token+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func shareToken(n byte) string {
	hex := strings.Repeat(string("0123456789abcdef"[n%16]), 32)
	return hex[0:8] + "-" + hex[8:12] + "-" + hex[12:16] + "-" + hex[16:20] + "-" + hex[20:32]
}

// A listing shows the caller's own shares and nobody else's — the intent line
// alone reveals what another team is discussing.
func TestShareListingIsScopedToOneWorkspace(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	writeShare(t, dir, sharedSession{
		Token: shareToken(1), Version: 2, WorkspaceID: wsroot.PersonalWorkspaceID,
		CreatedAt: now, ExpiresAt: now.Add(shareTTL), Title: "mine",
	})
	writeShare(t, dir, sharedSession{
		Token: shareToken(2), Version: 2, WorkspaceID: "ws_other",
		CreatedAt: now, ExpiresAt: now.Add(shareTTL), Title: "someone else's",
	})

	var listed []shareSummary
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		snap, err := readShare(dir, strings.TrimSuffix(e.Name(), ".json"))
		if err != nil || snapWorkspace(snap) != wsroot.PersonalWorkspaceID {
			continue
		}
		listed = append(listed, shareSummary{Token: snap.Token, Title: snap.Title})
	}
	if len(listed) != 1 || listed[0].Title != "mine" {
		t.Fatalf("listing = %+v, want only the caller's own share", listed)
	}
}

// A pre-tenant snapshot has no workspace field, and belongs to the personal
// workspace — what a single-user installation's shares were. Getting this
// wrong would hide an existing user's own shares from them.
func TestLegacySharesBelongToPersonal(t *testing.T) {
	dir := t.TempDir()
	writeShare(t, dir, sharedSession{Token: shareToken(3), Version: 1, Title: "written before tenants"})
	snap, err := readShare(dir, shareToken(3))
	if err != nil {
		t.Fatal(err)
	}
	if got := snapWorkspace(snap); got != wsroot.PersonalWorkspaceID {
		t.Fatalf("a pre-tenant share resolved to %q", got)
	}
}

// The count cap must not let one tenant's burst evict another tenant's live
// links. Those are URLs people have already pasted elsewhere, so eviction is
// both a denial of service and a lever one tenant could pull against another.
func TestOneWorkspacesSharesCannotEvictAnothers(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	// A neighbour's single, live share.
	neighbour := shareToken(4)
	writeShare(t, dir, sharedSession{
		Token: neighbour, Version: 2, WorkspaceID: "ws_neighbour",
		CreatedAt: now, ExpiresAt: now.Add(shareTTL), Title: "neighbour",
	})
	// The busy tenant blows past the cap on its own.
	for i := 0; i < maxShares+10; i++ {
		token := shareToken(byte(i % 16)) // shape only; uniqueness comes from the suffix
		token = token[:len(token)-4] + pad(i)
		writeShare(t, dir, sharedSession{
			Token: token, Version: 2, WorkspaceID: "ws_busy",
			CreatedAt: now, ExpiresAt: now.Add(shareTTL), Title: "busy",
		})
	}

	pruneShares(dir, "ws_busy")

	if _, err := os.Stat(filepath.Join(dir, neighbour+".json")); err != nil {
		t.Fatalf("another workspace's burst evicted a live share: %v", err)
	}
	busy := 0
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		snap, err := readShare(dir, strings.TrimSuffix(e.Name(), ".json"))
		if err == nil && snap.WorkspaceID == "ws_busy" {
			busy++
		}
	}
	if busy > maxShares {
		t.Fatalf("the busy workspace kept %d shares, over its cap of %d", busy, maxShares)
	}
}

// Expiry is enforced on read, not only by the prune sweep. Pruning runs when
// someone creates a share, so on a quiet deployment an expired link would
// otherwise stay live indefinitely.
func TestAnExpiredShareIsRefusedOnRead(t *testing.T) {
	s := newTestGateway(t, "secret")
	dir, err := sharesDir()
	if err != nil {
		t.Skipf("share storage unavailable: %v", err)
	}
	token := shareToken(7)
	past := time.Now().UTC().Add(-time.Hour)
	writeShare(t, dir, sharedSession{
		Token: token, Version: 2, WorkspaceID: wsroot.PersonalWorkspaceID,
		CreatedAt: past.Add(-shareTTL), ExpiresAt: past, Title: "stale",
	})
	t.Cleanup(func() { _ = os.Remove(filepath.Join(dir, token+".json")) })

	status, _ := gatewayJSON(t, s, http.MethodGet, "/api/v1/shared/"+token, "", "")
	if status != http.StatusNotFound {
		t.Fatalf("expired share read = %d, want 404", status)
	}
	if _, err := os.Stat(filepath.Join(dir, token+".json")); err == nil {
		t.Error("an expired share was served-then-left on disk")
	}
}

// Revoking a share the caller does not own must be indistinguishable from
// revoking one that never existed, so tokens cannot be probed and one tenant
// cannot take down another's link.
func TestRevokingAnotherWorkspacesShareIsANotFound(t *testing.T) {
	s := newTestGateway(t, "secret")
	dir, err := sharesDir()
	if err != nil {
		t.Skipf("share storage unavailable: %v", err)
	}
	token := shareToken(9)
	now := time.Now().UTC()
	writeShare(t, dir, sharedSession{
		Token: token, Version: 2, WorkspaceID: "ws_other",
		CreatedAt: now, ExpiresAt: now.Add(shareTTL), Title: "not yours",
	})
	t.Cleanup(func() { _ = os.Remove(filepath.Join(dir, token+".json")) })

	status, _ := gatewayJSON(t, s, http.MethodDelete, "/api/v1/chat/share/"+token, "secret", "")
	if status != http.StatusNotFound {
		t.Fatalf("cross-workspace revoke = %d, want 404", status)
	}
	if _, err := os.Stat(filepath.Join(dir, token+".json")); err != nil {
		t.Fatal("another workspace's revoke deleted the owner's share")
	}
	// An unknown token gives exactly the same answer.
	unknown, _ := gatewayJSON(t, s, http.MethodDelete, "/api/v1/chat/share/"+shareToken(11), "secret", "")
	if unknown != http.StatusNotFound {
		t.Fatalf("unknown token revoke = %d, want the same 404", unknown)
	}
}

// The owner can withdraw their own link, which is the point of revocation.
func TestTheOwnerCanRevokeTheirShare(t *testing.T) {
	s := newTestGateway(t, "secret")
	dir, err := sharesDir()
	if err != nil {
		t.Skipf("share storage unavailable: %v", err)
	}
	token := shareToken(13)
	now := time.Now().UTC()
	writeShare(t, dir, sharedSession{
		Token: token, Version: 2, WorkspaceID: wsroot.PersonalWorkspaceID,
		CreatedAt: now, ExpiresAt: now.Add(shareTTL), Title: "mine",
	})
	t.Cleanup(func() { _ = os.Remove(filepath.Join(dir, token+".json")) })

	status, _ := gatewayJSON(t, s, http.MethodDelete, "/api/v1/chat/share/"+token, "secret", "")
	if status != http.StatusOK {
		t.Fatalf("owner revoke = %d, want 200", status)
	}
	if _, err := os.Stat(filepath.Join(dir, token+".json")); err == nil {
		t.Fatal("the owner's revoke did not delete the share")
	}
	// And the public link stops resolving.
	if view, _ := gatewayJSON(t, s, http.MethodGet, "/api/v1/shared/"+token, "", ""); view != http.StatusNotFound {
		t.Fatalf("a revoked share still resolved: %d", view)
	}
}

func TestWorkspacePurgeRevokesOnlyTargetShares(t *testing.T) {
	dir, err := sharesDir()
	if err != nil {
		t.Skipf("share storage unavailable: %v", err)
	}
	now := time.Now().UTC()
	deletedToken, keptToken := shareToken(14), shareToken(15)
	writeShare(t, dir, sharedSession{Token: deletedToken, Version: 2, WorkspaceID: "ws_delete", CreatedAt: now, ExpiresAt: now.Add(shareTTL)})
	writeShare(t, dir, sharedSession{Token: keptToken, Version: 2, WorkspaceID: "ws_keep", CreatedAt: now, ExpiresAt: now.Add(shareTTL)})
	t.Cleanup(func() {
		_ = os.Remove(filepath.Join(dir, deletedToken+".json"))
		_ = os.Remove(filepath.Join(dir, keptToken+".json"))
	})

	removed, err := purgeWorkspaceShares(context.Background(), "ws_delete")
	if err != nil {
		t.Fatal(err)
	}
	if removed.Rows != 1 || removed.Bytes == 0 {
		t.Fatalf("share purge = %+v, want one non-empty snapshot", removed)
	}
	if _, err := os.Stat(filepath.Join(dir, deletedToken+".json")); !os.IsNotExist(err) {
		t.Fatalf("target share survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, keptToken+".json")); err != nil {
		t.Fatalf("neighbouring workspace share changed: %v", err)
	}
}
