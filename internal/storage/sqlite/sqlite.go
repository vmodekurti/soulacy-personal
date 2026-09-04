// Package sqlite wraps internal/actionlog and internal/memory so that both
// satisfy the storage.ActionLogBackend and storage.MemoryBackend interfaces
// without any changes to the underlying implementations.
//
// These are thin shim types — they add only the methods that aren't already
// present on the concrete types (EventFilePath for ActionLog) and compile-time
// interface assertions to catch drift.
package sqlite

import (
	"github.com/soulacy/soulacy/internal/actionlog"
	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/internal/storage"
)

// ---- compile-time interface checks ----------------------------------------
// If actionlog.Logger or memory.SQLiteArchive ever gain or lose methods these
// assertions will cause a build error, prompting the developer to update either
// the concrete type or the shim.

var _ storage.ActionLogBackend = (*ActionLog)(nil)
var _ storage.MemoryBackend = (*MemoryArchive)(nil)

// ---------------------------------------------------------------------------

// ActionLog wraps *actionlog.Logger to satisfy storage.ActionLogBackend.
// All methods delegate to the embedded Logger; EventFilePath is the one
// new method the interface requires over the concrete type.
type ActionLog struct {
	*actionlog.Logger
}

// EventFilePath returns the on-disk log file path for agentID.
// Implements the storage.ActionLogBackend method that actionlog.Logger
// exposes as Path().
func (a *ActionLog) EventFilePath(agentID string) string {
	return a.Logger.Path(agentID)
}

// NewActionLog wraps an existing *actionlog.Logger in the storage interface.
func NewActionLog(l *actionlog.Logger) *ActionLog {
	return &ActionLog{Logger: l}
}

// ---------------------------------------------------------------------------

// MemoryArchive wraps *memory.SQLiteArchive to satisfy storage.MemoryBackend.
// All required methods are already present on SQLiteArchive; this type exists
// purely to provide the compile-time guarantee.
type MemoryArchive struct {
	*memory.SQLiteArchive
}

// NewMemoryArchive wraps an existing *memory.SQLiteArchive in the storage interface.
func NewMemoryArchive(a *memory.SQLiteArchive) *MemoryArchive {
	return &MemoryArchive{SQLiteArchive: a}
}
