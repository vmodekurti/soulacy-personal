// Package memory implements Soulacy's multi-layer memory system.
// Memory is organised into three tiers:
//  1. Hot (file-based JSONL) — recent session history, fast reads, human-inspectable.
//  2. Archive (SQLite) — long-term memory with full text search.
//  3. Semantic (vector DB, optional) — embedding-based retrieval for large memory sets.
package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/soulacy/soulacy/internal/wsroot"
	sdkmemory "github.com/soulacy/soulacy/sdk/memory"
)

// Scope determines which agents can access a memory entry. Canonical
// definition lives in the versioned SDK (Story E9).
type Scope = sdkmemory.Scope

const (
	ScopeSession = sdkmemory.ScopeSession // only the current session
	ScopeAgent   = sdkmemory.ScopeAgent   // any session of the owning agent
	ScopeGlobal  = sdkmemory.ScopeGlobal  // all agents
)

// Entry is a single unit of memory (SDK canonical type).
type Entry = sdkmemory.Entry

// Embedder is the interface VectorStore uses to turn text into a float vector.
// Satisfied by *llm.OllamaEmbedder and *llm.OpenAIEmbedder.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// Store is the central interface for all memory operations.
//
// Every method names its workspace explicitly rather than reading it from
// ambient state. That is the point of MU-003's rule for repositories: a filter
// that a caller can forget to apply is not a boundary, and a memory read is
// the single easiest place to leak another tenant's conversation.
type Store interface {
	// Write persists a new memory entry. The entry's WorkspaceID is required.
	Write(e Entry) error

	// Read retrieves recent entries for a session/agent in one workspace.
	Read(workspaceID, agentID, sessionID string, scope Scope, limit int) ([]Entry, error)

	// Search performs a substring search across one workspace's memory.
	Search(workspaceID, agentID, query string, limit int) ([]Entry, error)

	// Delete removes a specific memory entry owned by the workspace.
	Delete(workspaceID, id string) error

	// PurgeSession removes all ephemeral memories for a session in one
	// workspace.
	PurgeSession(workspaceID, sessionID string) error

	// Close releases any held resources.
	Close() error
}

// ErrWorkspaceRequired is returned when a memory operation arrives without a
// workspace. Memory with no owner is memory every tenant can read, so this
// fails closed rather than defaulting.
var ErrWorkspaceRequired = errors.New("memory: workspace is required")

// FileStore is the primary hot-memory backend. Entries are appended to per-session
// JSONL files under <dir>/<agentID>/<sessionID>.jsonl. Reads scan from the end of
// the file so recent entries surface first with no indexing overhead.
//
// PRODUCTION_AUDIT → HIGH/Performance:
//   - Write previously serialised through one global mutex; we now shard
//     by (agentID, sessionID) so disjoint sessions can write in parallel.
//   - Read previously slurped the whole file every turn; for files larger
//     than the tail window we now Seek to (size - readTailBytes) and only
//     scan the trailing window. Sessions that fit in the window are read
//     in full as before, so behaviour is unchanged for short conversations.
type FileStore struct {
	dir string
	// shards holds per-(agent,session) mutexes lazily; the outer shardMu
	// only protects the map itself.
	shardMu sync.Mutex
	shards  map[string]*sync.Mutex
}

// readTailBytes caps how much of a memory file we re-read on each turn. 64 KB
// holds plenty of recent entries (max_tokens defaults to 20, and a typical
// entry is well under 500 B). Sessions that exceed this size still get the
// most recent N entries — earlier entries are not visible to the agent loop
// but remain on disk for the Memory page / archive.
const readTailBytes = 64 * 1024

// NewFileStore creates (or opens) a FileStore rooted at dir.
func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("memory: creating dir %s: %w", dir, err)
	}
	return &FileStore{dir: dir, shards: make(map[string]*sync.Mutex)}, nil
}

// sessionPath places a session's memory inside its workspace. The personal
// workspace keeps <dir>/<agent>/<session>.jsonl exactly as before; every other
// workspace is namespaced, so one tenant's memory file is not merely filtered
// out of another's reads — it is not in the directory they read at all.
func (s *FileStore) sessionPath(workspaceID, agentID, sessionID string) string {
	return filepath.Join(s.workspaceDir(workspaceID), agentID, sessionID+".jsonl")
}

func (s *FileStore) workspaceDir(workspaceID string) string {
	return wsroot.Dir(s.dir, workspaceID)
}

// shardFor returns the mutex for (agentID, sessionID). Lazily creates it
// inside shardMu so two goroutines targeting the same session always see the
// same lock; two targeting different sessions never block each other.
func (s *FileStore) shardFor(agentID, sessionID string) *sync.Mutex {
	key := agentID + "|" + sessionID
	s.shardMu.Lock()
	defer s.shardMu.Unlock()
	if m, ok := s.shards[key]; ok {
		return m
	}
	m := &sync.Mutex{}
	s.shards[key] = m
	return m
}

func (s *FileStore) Write(e Entry) error {
	if strings.TrimSpace(e.WorkspaceID) == "" {
		return ErrWorkspaceRequired
	}
	if err := wsroot.Validate(e.WorkspaceID); err != nil {
		return err
	}
	if e.ID == "" {
		e.ID = uuid.New().String()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}

	path := s.sessionPath(e.WorkspaceID, e.AgentID, e.SessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("memory: mkdir: %w", err)
	}

	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("memory: marshal: %w", err)
	}

	// Per-session lock — disjoint (agent, session) writes proceed in parallel.
	m := s.shardFor(e.WorkspaceID+"\x00"+e.AgentID, e.SessionID)
	m.Lock()
	defer m.Unlock()

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("memory: open %s: %w", path, err)
	}
	defer f.Close()

	_, err = f.WriteString(string(line) + "\n")
	return err
}

func (s *FileStore) Read(workspaceID, agentID, sessionID string, scope Scope, limit int) ([]Entry, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return nil, ErrWorkspaceRequired
	}
	path := s.sessionPath(workspaceID, agentID, sessionID)

	m := s.shardFor(workspaceID+"\x00"+agentID, sessionID)
	m.Lock()
	data, err := readTail(path, readTailBytes)
	m.Unlock()

	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("memory: read %s: %w", path, err)
	}

	var entries []Entry
	lines := splitLines(data)
	// Read from the end to get most recent first
	for i := len(lines) - 1; i >= 0 && len(entries) < limit; i-- {
		if len(lines[i]) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(lines[i], &e); err != nil {
			continue
		}
		if scope != "" && e.Scope != scope {
			continue
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// readTail reads the last `tailBytes` of path. If the file is smaller than
// tailBytes it returns the whole thing. The first (possibly partial) line is
// discarded so we don't yield a half-decoded JSON object to the caller —
// unless the whole file fits in the window, in which case we keep the first
// line because it's complete.
func readTail(path string, tailBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	if size <= tailBytes {
		// Whole-file read — common path for short sessions.
		buf := make([]byte, size)
		_, err := f.Read(buf)
		return buf, err
	}
	off := size - tailBytes
	if _, err := f.Seek(off, 0); err != nil {
		return nil, err
	}
	buf := make([]byte, tailBytes)
	n, err := f.Read(buf)
	if err != nil {
		return nil, err
	}
	buf = buf[:n]
	// Drop the partial first line so callers can split safely.
	for i, b := range buf {
		if b == '\n' {
			return buf[i+1:], nil
		}
	}
	return nil, nil
}

func (s *FileStore) Search(workspaceID, agentID, query string, limit int) ([]Entry, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return nil, ErrWorkspaceRequired
	}
	agentDir := filepath.Join(s.workspaceDir(workspaceID), agentID)
	files, err := filepath.Glob(filepath.Join(agentDir, "*.jsonl"))
	if err != nil {
		return nil, err
	}

	var results []Entry
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range splitLines(data) {
			if len(line) == 0 {
				continue
			}
			var e Entry
			if err := json.Unmarshal(line, &e); err != nil {
				continue
			}
			if containsCI(e.Content, query) {
				results = append(results, e)
				if len(results) >= limit {
					return results, nil
				}
			}
		}
	}
	return results, nil
}

func (s *FileStore) Delete(workspaceID, id string) error {
	if strings.TrimSpace(workspaceID) == "" {
		return ErrWorkspaceRequired
	}
	// File-based delete is expensive; mark as deleted in metadata instead.
	// A compaction job (run on startup or via CLI) rewrites files without deleted entries.
	// For now, this is a no-op placeholder — full implementation uses SQLite as the
	// authoritative deleted-ID set.
	return nil
}

func (s *FileStore) PurgeSession(workspaceID, sessionID string) error {
	if strings.TrimSpace(workspaceID) == "" {
		return ErrWorkspaceRequired
	}
	// Walk only this workspace's agent dirs. Walking the shared root would
	// delete a same-named session belonging to another tenant, which is the
	// worst possible failure for a purge: silent, immediate, irreversible.
	root := s.workspaceDir(workspaceID)
	personal := wsroot.Normalize(workspaceID) == wsroot.PersonalWorkspaceID
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			// The personal root contains every other workspace's namespace;
			// a personal purge must not descend into it.
			if personal && info.Name() == wsroot.NamespaceDir {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Base(path) == sessionID+".jsonl" {
			return os.Remove(path)
		}
		return nil
	})
}

func (s *FileStore) Close() error { return nil }

// --- helpers ---

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

func containsCI(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	sl, subl := []byte(s), []byte(substr)
	for i := range sl {
		if i+len(subl) > len(sl) {
			break
		}
		match := true
		for j := range subl {
			a, b := sl[i+j], subl[j]
			if a >= 'A' && a <= 'Z' {
				a += 32
			}
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
