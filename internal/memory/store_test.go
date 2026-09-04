// store_test.go — tests for the FileStore hot-memory backend and its helpers.
// Pure Go; no CGO or external services required.
package memory

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func writeEntry(t *testing.T, s *FileStore, agentID, sessionID string, scope Scope, content string) Entry {
	t.Helper()
	e := Entry{
		AgentID:   agentID,
		SessionID: sessionID,
		Scope:     scope,
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.Write(e); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return e
}

func newFileStore(t *testing.T) *FileStore {
	t.Helper()
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return s
}

// ---------------------------------------------------------------------------
// FileStore — Write / Read
// ---------------------------------------------------------------------------

// TestFileStoreWriteAndRead verifies the basic write-then-read cycle.
func TestFileStoreWriteAndRead(t *testing.T) {
	s := newFileStore(t)
	writeEntry(t, s, "agent-a", "sess-1", ScopeSession, "hello world")

	entries, err := s.Read("agent-a", "sess-1", ScopeSession, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(entries))
	}
	if entries[0].Content != "hello world" {
		t.Errorf("content = %q, want 'hello world'", entries[0].Content)
	}
	if entries[0].AgentID != "agent-a" {
		t.Errorf("agentID = %q, want 'agent-a'", entries[0].AgentID)
	}
	if entries[0].Scope != ScopeSession {
		t.Errorf("scope = %q, want ScopeSession", entries[0].Scope)
	}
}

// TestFileStoreReadRespectsScopeFilter verifies that the scope filter
// excludes non-matching entries — e.g. a global entry does not appear
// when reading with scope=session.
func TestFileStoreReadRespectsScopeFilter(t *testing.T) {
	s := newFileStore(t)
	writeEntry(t, s, "ag", "s1", ScopeSession, "session content")
	writeEntry(t, s, "ag", "s1", ScopeGlobal, "global content")

	sessionEntries, err := s.Read("ag", "s1", ScopeSession, 10)
	if err != nil {
		t.Fatalf("Read session: %v", err)
	}
	if len(sessionEntries) != 1 || sessionEntries[0].Content != "session content" {
		t.Errorf("session-scoped read returned: %+v", sessionEntries)
	}

	globalEntries, err := s.Read("ag", "s1", ScopeGlobal, 10)
	if err != nil {
		t.Fatalf("Read global: %v", err)
	}
	if len(globalEntries) != 1 || globalEntries[0].Content != "global content" {
		t.Errorf("global-scoped read returned: %+v", globalEntries)
	}

	// Empty scope returns all entries.
	all, err := s.Read("ag", "s1", "", 10)
	if err != nil {
		t.Fatalf("Read all: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("unfiltered read count = %d, want 2", len(all))
	}
}

// TestFileStoreReadNewestFirst writes multiple entries and verifies they are
// returned newest-first (most recently written entry at index 0).
func TestFileStoreReadNewestFirst(t *testing.T) {
	s := newFileStore(t)
	for i := 0; i < 3; i++ {
		writeEntry(t, s, "ag", "s1", ScopeSession, fmt.Sprintf("msg-%d", i))
		// Small sleep so CreatedAt timestamps are distinct if needed by the
		// store implementation, and to give the FS time to flush.
		time.Sleep(time.Millisecond)
	}

	entries, err := s.Read("ag", "s1", ScopeSession, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entry count = %d, want 3", len(entries))
	}
	// Read scans from the end of the file so the last-written entry is first.
	if entries[0].Content != "msg-2" {
		t.Errorf("first entry = %q, want msg-2 (newest first)", entries[0].Content)
	}
	if entries[2].Content != "msg-0" {
		t.Errorf("last entry = %q, want msg-0 (oldest last)", entries[2].Content)
	}
}

// TestFileStoreReadRespectsLimit verifies the limit parameter caps results.
func TestFileStoreReadRespectsLimit(t *testing.T) {
	s := newFileStore(t)
	for i := 0; i < 5; i++ {
		writeEntry(t, s, "ag", "s1", ScopeSession, fmt.Sprintf("entry-%d", i))
	}

	entries, err := s.Read("ag", "s1", ScopeSession, 3)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entry count = %d, want 3 (limit)", len(entries))
	}
}

// TestFileStoreReadNonExistentSessionReturnsNil verifies that reading from a
// session that has never been written returns nil without error.
func TestFileStoreReadNonExistentSessionReturnsNil(t *testing.T) {
	s := newFileStore(t)
	entries, err := s.Read("nobody", "ghost-session", ScopeSession, 10)
	if err != nil {
		t.Fatalf("Read non-existent: %v", err)
	}
	if entries != nil {
		t.Errorf("expected nil for missing session, got %v", entries)
	}
}

// TestFileStoreDifferentAgentsDontInterfere verifies that writes for agent-a
// are not visible when reading for agent-b.
func TestFileStoreDifferentAgentsDontInterfere(t *testing.T) {
	s := newFileStore(t)
	writeEntry(t, s, "agent-a", "s1", ScopeSession, "alpha data")
	writeEntry(t, s, "agent-b", "s1", ScopeSession, "beta data")

	entriesA, _ := s.Read("agent-a", "s1", ScopeSession, 10)
	entriesB, _ := s.Read("agent-b", "s1", ScopeSession, 10)

	if len(entriesA) != 1 || entriesA[0].Content != "alpha data" {
		t.Errorf("agent-a entries = %+v", entriesA)
	}
	if len(entriesB) != 1 || entriesB[0].Content != "beta data" {
		t.Errorf("agent-b entries = %+v", entriesB)
	}
}

// ---------------------------------------------------------------------------
// FileStore — Search
// ---------------------------------------------------------------------------

// TestFileStoreSearch verifies that Search returns entries whose content
// contains the query substring and respects the limit.
func TestFileStoreSearch(t *testing.T) {
	s := newFileStore(t)
	writeEntry(t, s, "ag", "s1", ScopeSession, "the cat sat on the mat")
	writeEntry(t, s, "ag", "s2", ScopeSession, "the dog barked loudly")
	writeEntry(t, s, "ag", "s3", ScopeSession, "another cat entry")

	results, err := s.Search("ag", "cat", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Search 'cat' count = %d, want 2", len(results))
	}
	for _, r := range results {
		if r.AgentID != "ag" {
			t.Errorf("result has wrong agentID: %q", r.AgentID)
		}
	}
}

// TestFileStoreSearchCaseInsensitive verifies the case-insensitive match
// (containsCI returns true for "CAT" matching "cat" content).
func TestFileStoreSearchCaseInsensitive(t *testing.T) {
	s := newFileStore(t)
	writeEntry(t, s, "ag", "s1", ScopeSession, "the cat sat")

	results, err := s.Search("ag", "CAT", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("case-insensitive search count = %d, want 1", len(results))
	}
}

// TestFileStoreSearchNoMatch returns empty slice, not nil, when nothing matches.
func TestFileStoreSearchNoMatch(t *testing.T) {
	s := newFileStore(t)
	writeEntry(t, s, "ag", "s1", ScopeSession, "nothing here")

	results, err := s.Search("ag", "xyzzy", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// FileStore — PurgeSession
// ---------------------------------------------------------------------------

// TestFileStorePurgeSessionRemovesOnlyTarget writes two sessions and verifies
// that PurgeSession removes only the specified session's file.
func TestFileStorePurgeSessionRemovesOnlyTarget(t *testing.T) {
	s := newFileStore(t)
	writeEntry(t, s, "ag", "sess-keep", ScopeSession, "keep me")
	writeEntry(t, s, "ag", "sess-gone", ScopeSession, "delete me")

	if err := s.PurgeSession("sess-gone"); err != nil {
		t.Fatalf("PurgeSession: %v", err)
	}

	kept, err := s.Read("ag", "sess-keep", ScopeSession, 10)
	if err != nil {
		t.Fatalf("Read kept session: %v", err)
	}
	if len(kept) != 1 {
		t.Fatalf("kept session entry count = %d, want 1", len(kept))
	}

	purged, err := s.Read("ag", "sess-gone", ScopeSession, 10)
	if err != nil {
		t.Fatalf("Read purged session: %v", err)
	}
	if len(purged) != 0 {
		t.Errorf("purged session still has %d entries", len(purged))
	}
}

// TestFileStorePurgeSessionNonExistent verifies PurgeSession is a no-op
// (not an error) when the session never existed.
func TestFileStorePurgeSessionNonExistent(t *testing.T) {
	s := newFileStore(t)
	if err := s.PurgeSession("no-such-session"); err != nil {
		t.Fatalf("PurgeSession missing session: %v", err)
	}
}

// ---------------------------------------------------------------------------
// FileStore — concurrent shard safety
// ---------------------------------------------------------------------------

// TestFileStoreShardIsolation writes to different (agent, session) pairs
// concurrently and verifies no data loss occurs — each session must have
// exactly the expected number of entries.
func TestFileStoreShardIsolation(t *testing.T) {
	s := newFileStore(t)
	const sessions = 5
	const msgsPerSession = 20
	var wg sync.WaitGroup
	for i := 0; i < sessions; i++ {
		sid := fmt.Sprintf("session-%d", i)
		wg.Add(1)
		go func(sessionID string) {
			defer wg.Done()
			for j := 0; j < msgsPerSession; j++ {
				writeEntry(t, s, "ag", sessionID, ScopeSession, fmt.Sprintf("msg-%d", j))
			}
		}(sid)
	}
	wg.Wait()

	for i := 0; i < sessions; i++ {
		sid := fmt.Sprintf("session-%d", i)
		entries, err := s.Read("ag", sid, ScopeSession, msgsPerSession+1)
		if err != nil {
			t.Errorf("Read session %s: %v", sid, err)
			continue
		}
		if len(entries) != msgsPerSession {
			t.Errorf("session %s: entry count = %d, want %d", sid, len(entries), msgsPerSession)
		}
	}
}

// ---------------------------------------------------------------------------
// Helper unit tests
// ---------------------------------------------------------------------------

// TestSplitLines covers the JSONL-line splitter used by Read.
func TestSplitLines(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  int
	}{
		{"empty", "", 0},
		{"single no newline", `{"a":1}`, 1},
		{"single with newline", `{"a":1}` + "\n", 1},
		{"two lines", `{"a":1}` + "\n" + `{"b":2}`, 2},
		{"two lines trailing newline", `{"a":1}` + "\n" + `{"b":2}` + "\n", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitLines([]byte(tc.input))
			// Count non-empty lines for simplicity.
			count := 0
			for _, l := range got {
				if len(l) > 0 {
					count++
				}
			}
			if count != tc.want {
				t.Errorf("splitLines count = %d, want %d (lines: %v)", count, tc.want, got)
			}
		})
	}
}

// TestContainsCI covers the case-insensitive substring helper.
func TestContainsCI(t *testing.T) {
	cases := []struct {
		s, sub string
		want   bool
	}{
		{"hello world", "world", true},
		{"hello world", "WORLD", true},
		{"hello world", "xyz", false},
		{"", "x", false},
		{"hello", "", true},  // empty substr always matches
		{"HELLO", "hello", true},
		{"abc", "abcd", false}, // substr longer than string
	}
	for _, tc := range cases {
		got := containsCI(tc.s, tc.sub)
		if got != tc.want {
			t.Errorf("containsCI(%q, %q) = %v, want %v", tc.s, tc.sub, got, tc.want)
		}
	}
}

// TestReadTailSmallFile verifies readTail returns the entire file when it fits
// within the tail window.
func TestReadTailSmallFile(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewFileStore(dir)
	writeEntry(t, s, "ag", "s1", ScopeSession, "tiny")

	path := s.sessionPath("ag", "s1")
	data, err := readTail(path, readTailBytes)
	if err != nil {
		t.Fatalf("readTail: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("readTail returned empty for non-empty file")
	}
}
