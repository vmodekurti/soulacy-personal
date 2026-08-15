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
	"github.com/soulacy/soulacy/internal/wsroot"
	sdkstorage "github.com/soulacy/soulacy/sdk/storage"
)

// ---- compile-time interface checks ----------------------------------------
// If actionlog.Logger or memory.SQLiteArchive ever gain or lose methods these
// assertions will cause a build error, prompting the developer to update either
// the concrete type or the shim.

var _ storage.ActionLogBackend = (*ActionLog)(nil)
var _ sdkstorage.WorkspaceActionLogBackend = (*ActionLog)(nil)
var _ storage.MemoryBackend = (*MemoryArchive)(nil)
var _ sdkstorage.WorkspaceMemoryBackend = (*MemoryArchive)(nil)

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

// EventFilePathInWorkspace returns the on-disk log file path for agentID
// inside one workspace. A tenant's events are a different file, so this is the
// only way to reach them.
func (a *ActionLog) EventFilePathInWorkspace(workspaceID, agentID string) string {
	return a.Logger.PathInWorkspace(workspaceID, agentID)
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

// The frozen storage.MemoryBackend methods have no workspace parameter, so
// they resolve to the implicit personal workspace — which is exactly what a
// caller using the un-scoped interface is: a single-tenant one. Multi-tenant
// callers use the *InWorkspace methods below, and fail closed if a backend
// does not offer them.
func (m *MemoryArchive) Search(agentID, query string, limit int) ([]memory.Entry, error) {
	return m.SQLiteArchive.Search(wsroot.PersonalWorkspaceID, agentID, query, limit)
}

func (m *MemoryArchive) ReadByScope(agentID, sessionID string, scope memory.Scope, limit int) ([]memory.Entry, error) {
	return m.SQLiteArchive.ReadByScope(wsroot.PersonalWorkspaceID, agentID, sessionID, scope, limit)
}

func (m *MemoryArchive) ReadGlobal(agentID string, limit int) ([]memory.Entry, error) {
	return m.SQLiteArchive.ReadGlobal(wsroot.PersonalWorkspaceID, agentID, limit)
}

func (m *MemoryArchive) SearchInWorkspace(workspaceID, agentID, query string, limit int) ([]memory.Entry, error) {
	return m.SQLiteArchive.Search(workspaceID, agentID, query, limit)
}

func (m *MemoryArchive) ReadByScopeInWorkspace(workspaceID, agentID, sessionID string, scope memory.Scope, limit int) ([]memory.Entry, error) {
	return m.SQLiteArchive.ReadByScope(workspaceID, agentID, sessionID, scope, limit)
}

func (m *MemoryArchive) ReadGlobalInWorkspace(workspaceID, agentID string, limit int) ([]memory.Entry, error) {
	return m.SQLiteArchive.ReadGlobal(workspaceID, agentID, limit)
}
