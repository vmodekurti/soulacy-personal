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

// DurableActionLogBackend is the surface for writes that must not be dropped.
//
// A separate interface for the same reason WorkspaceActionLogBackend is one:
// ActionLogBackend is frozen for this SDK major version, and Append's contract
// — "must never block the caller" — is deliberate and correct for run
// telemetry. It is wrong for an audit record.
//
// An audit trail with holes under load is not a slightly worse audit trail. It
// is one that cannot be used to say an action did NOT happen, which is most of
// what an audit trail is for, and the holes appear exactly when the system is
// busiest. AppendDurable therefore waits briefly and, if it still cannot
// enqueue, REPORTS the loss — because the alternative failure is an absence,
// and an absence is undetectable by construction.
//
// A backend that does not implement this keeps working; a caller that needs
// the guarantee type-asserts and says so loudly when the assertion fails,
// rather than quietly writing an audit record it cannot promise to keep.
type DurableActionLogBackend interface {
	ActionLogBackend

	// AppendDurable enqueues an event, waiting briefly for room, and reports
	// whether it was accepted. False means the record is LOST and the caller
	// must surface that.
	AppendDurable(ev message.Event) bool
}

// WorkspaceActionLogBackend is the tenant-aware surface for an action log.
//
// It is a separate interface rather than new methods on ActionLogBackend
// because ActionLogBackend is frozen for this SDK major version. A backend
// that does not implement this one keeps working exactly as before; a caller
// that needs tenant isolation type-asserts for it and refuses to serve
// multi-tenant traffic when the assertion fails, rather than silently reading
// across tenants.
//
// Append needs no counterpart: message.Event carries WorkspaceID, so a write
// is already tenant-aware for any backend that persists the field.
//
// IncompleteMessageIns has no scoped twin either, and deliberately so. It is
// the boot recovery pass, which must recover every tenant's interrupted runs;
// isolation is preserved by each returned payload carrying the workspace of
// the run it belongs to, so the replay re-enters under its own tenant.
type WorkspaceActionLogBackend interface {
	ActionLogBackend

	// TailInWorkspace returns the most recent limit events for agentID within
	// one workspace, oldest-first.
	TailInWorkspace(workspaceID, agentID string, limit int) ([]message.Event, error)

	// CountMessageInAttemptsInWorkspace counts message.in events for
	// (workspace, agent, session) since `since`.
	CountMessageInAttemptsInWorkspace(workspaceID, agentID, sessionID string, since time.Time) (int, error)

	// MarkDeadLetterInWorkspace quarantines a session inside its own workspace.
	MarkDeadLetterInWorkspace(workspaceID, agentID, sessionID, reason string) error
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

// WorkspaceMemoryBackend is the tenant-aware read surface for a memory
// archive.
//
// It is a separate interface rather than new methods on MemoryBackend because
// MemoryBackend is frozen for this SDK major version. A backend that does not
// implement this one keeps working exactly as before; a caller that needs
// tenant isolation type-asserts for it and refuses to serve multi-tenant
// traffic when the assertion fails, rather than silently reading across
// tenants.
//
// Writes need no counterpart: memory.Entry carries WorkspaceID, so Archive is
// already tenant-aware for any backend that persists the field.
type WorkspaceMemoryBackend interface {
	MemoryBackend

	// SearchInWorkspace searches one workspace's memory for agentID.
	SearchInWorkspace(workspaceID, agentID, query string, limit int) ([]memory.Entry, error)

	// ReadByScopeInWorkspace returns entries for (workspace, agent, session, scope).
	ReadByScopeInWorkspace(workspaceID, agentID, sessionID string, scope memory.Scope, limit int) ([]memory.Entry, error)

	// ReadGlobalInWorkspace returns the most recent entries across one
	// workspace's sessions for agentID.
	ReadGlobalInWorkspace(workspaceID, agentID string, limit int) ([]memory.Entry, error)
}
