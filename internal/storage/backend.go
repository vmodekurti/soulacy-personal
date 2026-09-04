// Package storage defines provider-agnostic interfaces for Soulacy's two
// durable data stores:
//
//   - ActionLogBackend — structured append-only event log (agent runs, tool calls, etc.)
//   - MemoryBackend    — long-term agent memory archive with scoped retrieval
//
// Two implementations ship out of the box:
//
//	internal/storage/sqlite   — embedded SQLite (zero-dependency, default)
//	internal/storage/postgres — PostgreSQL via pgx/v5 (production / multi-node)
//
// The active backend is selected at startup from config.yaml:
//
//	storage:
//	  backend: sqlite   # or "postgres"
//	  postgres_dsn: "postgres://user:pass@host:5432/soulacy"
//
// Engine code imports only this package; it never references sqlite or postgres
// directly, so swapping backends requires only a config change.
package storage

import sdkstorage "github.com/soulacy/soulacy/sdk/storage"

// Canonical storage contracts live in the versioned SDK (Story E9).
type (
	// ActionLogBackend is the interface satisfied by every action-log implementation.
	ActionLogBackend = sdkstorage.ActionLogBackend
	// MemoryBackend is the interface satisfied by every memory-archive implementation.
	MemoryBackend = sdkstorage.MemoryBackend
)
