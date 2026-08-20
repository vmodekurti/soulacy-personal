package approvals

import (
	"context"
	"testing"
)

// The list is what lets the boot path repair the runs it is about to strand,
// so it has to be taken before the invalidation and has to carry each row's
// own workspace.
func TestPendingRunRefsNamesEveryBlockedRunWithItsOwnWorkspace(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	request(t, store, "apr_a", "ws-a", nil)
	request(t, store, "apr_b", "ws-b", nil)

	refs, err := store.PendingRunRefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, ref := range refs {
		seen[ref.RunID] = ref.WorkspaceID
	}
	if len(seen) == 0 {
		t.Fatal("no blocked runs listed; the boot path would strand every one of them")
	}

	if _, err := store.InvalidateAllPending(ctx, ReasonWorkspaceRestarting); err != nil {
		t.Fatal(err)
	}
	after, err := store.PendingRunRefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("after the invalidation %d refs remain; the list must be taken first", len(after))
	}
}
