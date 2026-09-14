package memory

import (
	"context"
	"path/filepath"
	"testing"
)

func TestChangesFeedCollapsesEventsAndTombstonesDeletes(t *testing.T) {
	store, err := OpenFactSQLite(filepath.Join(t.TempDir(), "facts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	scope := FactScope{Owner: "ada", AgentID: "helper"}.Normalize()

	a, err := store.Insert(ctx, Fact{Workspace: scope.Workspace, Owner: "ada", AgentID: "helper", Category: FactPreference, Content: "User prefers tea", Source: "manual", Status: FactStatusActive})
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.Insert(ctx, Fact{Workspace: scope.Workspace, Owner: "ada", AgentID: "helper", Category: FactIdentity, Content: "User lives in Denver", Source: "manual", Status: FactStatusActive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(ctx, scope, a.ID, FactInput{Content: "User prefers green tea", Category: FactPreference}, nil, "user"); err != nil {
		t.Fatal(err)
	}

	page, err := store.Changes(ctx, scope, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Changes) != 2 || page.HasMore || page.Reset {
		t.Fatalf("expected two collapsed changes, got %+v", page)
	}
	// b was inserted before a's update, so a (last event later) comes last.
	if page.Changes[0].FactID != b.ID || page.Changes[1].FactID != a.ID || page.Changes[1].Fact == nil || page.Changes[1].Fact.Content != "User prefers green tea" {
		t.Fatalf("collapse order/state wrong: %+v", page.Changes)
	}
	cursor := page.NextCursor

	if err := store.Delete(ctx, scope, b.ID, "user"); err != nil {
		t.Fatal(err)
	}
	page, err = store.Changes(ctx, scope, cursor, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Changes) != 1 || !page.Changes[0].Deleted || page.Changes[0].FactID != b.ID || page.Changes[0].Fact != nil {
		t.Fatalf("expected a tombstone for the deleted fact, got %+v", page)
	}

	// Another owner's changes never leak into the feed.
	other := FactScope{Owner: "bob", AgentID: "helper"}.Normalize()
	if p, _ := store.Changes(ctx, other, 0, 10); len(p.Changes) != 0 {
		t.Fatalf("owner isolation broken: %+v", p)
	}

	// Paging: limit 1 reports has_more and advances one event at a time.
	if p, _ := store.Changes(ctx, scope, 0, 1); !p.HasMore || len(p.Changes) != 1 {
		t.Fatalf("paging wrong: %+v", p)
	}

	// A purge drops history; a device ahead of the log is told to reset.
	if _, err := store.Purge(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if p, _ := store.Changes(ctx, scope, page.NextCursor, 10); !p.Reset {
		t.Fatalf("expected reset after purge, got %+v", p)
	}
}
