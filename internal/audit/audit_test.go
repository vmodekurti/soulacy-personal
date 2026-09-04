// audit_test.go — tests for the audit package.
package audit

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// redactArgs
// ---------------------------------------------------------------------------

func TestRedactArgsEmptyMap(t *testing.T) {
	got := redactArgs(map[string]any{})
	if len(got) != 0 {
		t.Errorf("empty map: got %v", got)
	}
}

func TestRedactArgsNilMap(t *testing.T) {
	got := redactArgs(nil)
	if got != nil {
		t.Errorf("nil map: got %v", got)
	}
}

func TestRedactArgsSecretKeys(t *testing.T) {
	secrets := []string{"api_key", "API-KEY", "password", "token", "credential", "auth", "secret"}
	for _, key := range secrets {
		got := redactArgs(map[string]any{key: "my-secret-value"})
		if got[key] != "[REDACTED]" {
			t.Errorf("key %q: got %v, want [REDACTED]", key, got[key])
		}
	}
}

func TestRedactArgsSafeKeys(t *testing.T) {
	args := map[string]any{"query": "go tutorial", "max_results": 5}
	got := redactArgs(args)
	if got["query"] != "go tutorial" || got["max_results"] != 5 {
		t.Errorf("safe keys altered: %v", got)
	}
}

func TestRedactArgsMixedKeys(t *testing.T) {
	args := map[string]any{"query": "search", "api_key": "sk-secret"}
	got := redactArgs(args)
	if got["query"] != "search" {
		t.Errorf("safe key changed: %v", got["query"])
	}
	if got["api_key"] != "[REDACTED]" {
		t.Errorf("secret key not redacted: %v", got["api_key"])
	}
}

func TestRedactArgsDoesNotMutateSource(t *testing.T) {
	args := map[string]any{"api_key": "original"}
	_ = redactArgs(args)
	if args["api_key"] != "original" {
		t.Error("redactArgs mutated source map")
	}
}

// ---------------------------------------------------------------------------
// sessionSafe
// ---------------------------------------------------------------------------

func TestSessionSafeAlphanumeric(t *testing.T) {
	got := sessionSafe("abc123")
	if got != "abc123" {
		t.Errorf("alphanumeric: got %q", got)
	}
}

func TestSessionSafeDashUnderscore(t *testing.T) {
	got := sessionSafe("sess-abc_def")
	if got != "sess-abc_def" {
		t.Errorf("dash/underscore: got %q", got)
	}
}

func TestSessionSafeSpecialChars(t *testing.T) {
	got := sessionSafe("sess/2024.01:02")
	// Slashes, dots, colons → underscores.
	if strings.ContainsAny(got, "/.:\n") {
		t.Errorf("special chars not replaced: %q", got)
	}
}

func TestSessionSafeTruncatesAt128(t *testing.T) {
	long := strings.Repeat("a", 200)
	got := sessionSafe(long)
	if len(got) != 128 {
		t.Errorf("len = %d, want 128", len(got))
	}
}

func TestSessionSafeEmptyFallback(t *testing.T) {
	// All special chars replaced with underscores → but if we force empty input
	// the function returns "unknown".
	got := sessionSafe("")
	if got != "unknown" {
		t.Errorf("empty: got %q, want unknown", got)
	}
}

// ---------------------------------------------------------------------------
// Logger.Log
// ---------------------------------------------------------------------------

func TestLogNilLogger(t *testing.T) {
	var l *Logger
	l.Log(Entry{AgentID: "ag", Tool: "test"}) // must not panic
}

func TestLogEmptyDir(t *testing.T) {
	l := New("")
	l.Log(Entry{AgentID: "ag", Tool: "test"}) // must not panic, is no-op
}

func TestLogWritesJSONLFile(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	ts := time.Date(2026, 6, 5, 10, 0, 0, 0, time.UTC)
	l.Log(Entry{
		Timestamp: ts,
		SessionID: "sess-1",
		AgentID:   "agent-a",
		Tool:      "web_search",
		Args:      map[string]any{"query": "golang"},
		ResultLen: 42,
	})

	dayDir := filepath.Join(dir, "2026-06-05")
	entries, err := os.ReadDir(dayDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no log file written")
	}
	data, err := os.ReadFile(filepath.Join(dayDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "web_search") {
		t.Errorf("log missing tool name: %s", data)
	}
	if !strings.Contains(string(data), "golang") {
		t.Errorf("log missing query arg: %s", data)
	}
}

func TestLogRedactsSecretArgs(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	ts := time.Now().UTC()
	l.Log(Entry{
		Timestamp: ts,
		SessionID: "sess-secret",
		AgentID:   "ag",
		Tool:      "http_request",
		Args:      map[string]any{"url": "https://api.example.com", "api_key": "sk-real"},
	})

	dayDir := filepath.Join(dir, ts.Format("2006-01-02"))
	files, _ := os.ReadDir(dayDir)
	data, _ := os.ReadFile(filepath.Join(dayDir, files[0].Name()))
	if strings.Contains(string(data), "sk-real") {
		t.Error("secret api_key was not redacted in log output")
	}
	if !strings.Contains(string(data), "[REDACTED]") {
		t.Error("expected [REDACTED] in log output")
	}
}

func TestLogDeniedEntry(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	ts := time.Now().UTC()
	l.Log(Entry{
		Timestamp: ts, SessionID: "s1", AgentID: "ag",
		Tool: "shell_exec", Denied: true,
	})

	dayDir := filepath.Join(dir, ts.Format("2006-01-02"))
	files, _ := os.ReadDir(dayDir)
	data, _ := os.ReadFile(filepath.Join(dayDir, files[0].Name()))
	if !strings.Contains(string(data), `"denied":true`) {
		t.Errorf("denied flag missing from log: %s", data)
	}
}

func TestLogConcurrentWritesSamePath(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	ts := time.Now().UTC()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.Log(Entry{Timestamp: ts, SessionID: "shared", AgentID: "ag", Tool: "tool"})
		}()
	}
	wg.Wait() // all goroutines done before cleanup removes the temp dir
}
