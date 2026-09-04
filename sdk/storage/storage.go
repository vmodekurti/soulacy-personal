// Package storage defines provider-agnostic contracts for Soulacy's durable
// data stores: the append-only action log and the long-term memory archive.
//
// Compatibility: interfaces are FROZEN per SDK major version; see the SDK
// README.
package storage

import (
	"time"

	"github.com/soulacy/soulacy/sdk/memory"
	"github.com/soulacy/soulacy/sdk/message"
)

// ActionLogBackend is the interface satisfied by every action-log implementation.
type ActionLogBackend interface {
	// Append enqueues one event for durable storage. Must never block the caller.
	Append(ev message.Event)

	// Tail returns the most recent limit events for agentID, oldest-first.
	Tail(agentID string, limit int) ([]message.Event, error)

	// EventFilePath returns the on-disk log path for agentID. Backends that
	// don't use flat files should return a best-effort path or empty string.
	EventFilePath(agentID string) string

	// IncompleteMessageIns returns raw JSON payloads of message.in events that
	// have no corresponding outcome (message.out / error) since `since`. Used
	// by the startup recovery pass to re-enqueue crashed runs.
	IncompleteMessageIns(since time.Time) ([][]byte, error)

	// CountMessageInAttempts returns how many message.in events exist for
	// (agentID, sessionID) since `since`. Used by the poison-pill guard.
	CountMessageInAttempts(agentID, sessionID string, since time.Time) (int, error)

	// MarkDeadLetter writes a synchronous message.dead_letter event so the next
	// boot's IncompleteMessageIns query skips the poisoned session.
	MarkDeadLetter(agentID, sessionID, reason string) error

	// Close flushes pending events and releases all held resources.
	Close() error
}

// MemoryBackend is the interface satisfied by every memory-archive implementation.
type MemoryBackend interface {
	// Archive persists a memory entry. Duplicate IDs are silently ignored.
	Archive(entry memory.Entry) error

	// Search performs a substring/FTS search across memory content for agentID.
	Search(agentID, query string, limit int) ([]memory.Entry, error)

	// ReadByScope returns archived entries for (agentID, sessionID, scope), newest-first.
	ReadByScope(agentID, sessionID string, scope memory.Scope, limit int) ([]memory.Entry, error)

	// ReadGlobal returns the most recent entries across all sessions for agentID.
	ReadGlobal(agentID string, limit int) ([]memory.Entry, error)

	// Prune deletes entries older than before for agentID. Returns rows deleted.
	Prune(agentID string, before time.Time) (int64, error)

	// Close releases all held resources.
	Close() error
}
