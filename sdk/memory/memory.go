// Package memory defines the canonical long-term memory entry types shared
// by memory archives and vector backends.
//
// Compatibility: append-only fields; scope values never change meaning.
package memory

import "time"

// Scope controls retrieval visibility for a memory entry.
type Scope string

const (
	ScopeSession Scope = "session" // only the current session
	ScopeAgent   Scope = "agent"   // any session of the owning agent
	ScopeGlobal  Scope = "global"  // all agents
)

// Entry is one stored memory record.
//
// WorkspaceID is the tenant boundary. It is append-only and omitempty, so an
// older client keeps decoding these records unchanged and an older server
// ignores the field — but a multi-tenant deployment refuses an entry that does
// not carry one, because a memory with no owner is a memory every tenant can
// read.
type Entry struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id,omitempty"`

	AgentID   string            `json:"agent_id"`
	SessionID string            `json:"session_id"`
	Scope     Scope             `json:"scope"`
	Key       string            `json:"key,omitempty"` // optional structured key
	Content   string            `json:"content"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	ExpiresAt *time.Time        `json:"expires_at,omitempty"`
}
