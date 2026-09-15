package person

import (
	"context"
	"time"
)

// Change is one entry in the model's change feed. The feed exists so a phone
// or watch can hold a local copy and catch up incrementally, exactly as it
// does for adaptive memory facts.
type Change struct {
	Cursor  int64   `json:"cursor"`
	Section Section `json:"section"`
	Key     string  `json:"key"`
	// Deleted marks a tombstone: the entry was removed or expired.
	Deleted bool   `json:"deleted"`
	Entry   *Entry `json:"entry,omitempty"`
}

// Changes is one page of the feed.
type Changes struct {
	Changes    []Change `json:"changes"`
	NextCursor int64    `json:"next_cursor"`
	HasMore    bool     `json:"has_more"`
	// Reset tells the client its cursor is ahead of anything we know (the
	// model was purged): drop the local copy and start from zero.
	Reset bool `json:"reset"`
}

// Query narrows a read.
type Query struct {
	Sections []Section
	// Keys, when set, restricts to these keys within the sections.
	Keys []string
	// IncludeExpired is for the management UI, which should show a stale
	// entry rather than pretend it never existed.
	IncludeExpired bool
	Limit          int
}

// Store is the durable person model for every owner on this gateway.
//
// Every method is owner-scoped: there is no way to read across people by
// accident, which matters because a household shares one gateway.
type Store interface {
	// Put writes one entry, honouring precedence (see Outranks).
	Put(ctx context.Context, entry Entry) (PutResult, error)
	// PutAll writes several entries in one transaction, which is what an
	// observer does at the end of a pass.
	PutAll(ctx context.Context, entries []Entry) ([]PutResult, error)
	// Get reads one entry.
	Get(ctx context.Context, owner string, section Section, key string) (Entry, error)
	// List reads entries for one owner.
	List(ctx context.Context, owner string, query Query) ([]Entry, error)
	// Delete removes one entry and records a tombstone.
	Delete(ctx context.Context, owner string, section Section, key string) error
	// Purge removes everything for one owner (or one section of it) and
	// resets the feed, so clients drop their copies.
	Purge(ctx context.Context, owner string, sections ...Section) (int, error)
	// Changes reads the feed after a cursor.
	Changes(ctx context.Context, owner string, since int64, limit int) (Changes, error)
	Close() error
}

// Model assembles the whole model for one owner. A convenience over List
// used by the agent tool and the API.
func ModelFor(ctx context.Context, store Store, owner string, now time.Time) (Model, error) {
	entries, err := store.List(ctx, owner, Query{})
	if err != nil {
		return Model{}, err
	}
	return NewModel(owner, entries, now), nil
}
