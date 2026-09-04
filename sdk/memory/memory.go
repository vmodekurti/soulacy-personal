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
type Entry struct {
	ID        string            `json:"id"`
	AgentID   string            `json:"agent_id"`
	SessionID string            `json:"session_id"`
	Scope     Scope             `json:"scope"`
	Key       string            `json:"key,omitempty"` // optional structured key
	Content   string            `json:"content"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	ExpiresAt *time.Time        `json:"expires_at,omitempty"`
}
