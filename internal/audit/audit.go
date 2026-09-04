// Package audit provides an OPTIONAL append-only tool-call audit log in JSONL
// form. When enabled, every built-in tool execution is recorded as a JSON line
// in <audit_dir>/<YYYY-MM-DD>/<sessionID>.jsonl.
//
// DOC-4: this JSONL log is debug/convenience output and is DISABLED BY DEFAULT
// (runtime.audit_dir defaults to ""). It is NOT the authoritative record. The
// authoritative incident-reconstruction record is the SQLite action log in
// package internal/actionlog, which is always on. Treat these JSONL files as a
// redundant, best-effort convenience tail — see docs/security/audit.md.
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/soulacy/soulacy/internal/redact"
)

// Entry is one line in the audit log.
type Entry struct {
	Timestamp  time.Time      `json:"ts"`
	SessionID  string         `json:"session"`
	AgentID    string         `json:"agent"`
	Tool       string         `json:"tool"`
	Args       map[string]any `json:"args"`
	ResultLen  int            `json:"result_len"`
	DurationMS int64          `json:"duration_ms"`
	Denied     bool           `json:"denied,omitempty"`
	Error      string         `json:"error,omitempty"`
	// Action is the kind of audited event, e.g. "tool" (default, a tool call)
	// or "approval" (a human approved/denied a gated tool). Empty means "tool".
	Action string `json:"action,omitempty"`
	// Approver is the identity that approved or denied a gated action, recorded
	// so the activity log can answer "who approved what". Empty for unattended
	// tool calls.
	Approver string `json:"approver,omitempty"`
}

// secretPattern matches common secret field names so their values can be
// redacted before they are written to disk.
var secretPattern = regexp.MustCompile(`(?i)(api[_-]?key|password|secret|token|credential|auth)`)

// maxRedactDepth bounds the walk below. Tool arguments are JSON the model
// produced, so a pathological nesting depth is possible; 12 is far past anything
// a real tool call uses.
const maxRedactDepth = 12

// redactArgs copies args with secret-looking values replaced by "[REDACTED]".
//
// It used to inspect only TOP-LEVEL keys, on the stated reasoning that
// deep-copying large structures was wasteful. But arguments carrying credentials
// are almost never flat: an http_request call puts its bearer token in
// {"headers":{"Authorization":"…"}}, and an MCP tool call nests everything under
// an object. The redaction therefore ran, matched nothing, and wrote the token
// into the audit log — which is precisely the file an operator ships to someone
// else when asking for help.
func redactArgs(args map[string]any) map[string]any {
	if len(args) == 0 {
		return args
	}
	return redactMap(args, 0)
}

func redactMap(in map[string]any, depth int) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		if secretPattern.MatchString(k) {
			out[k] = "[REDACTED]"
			continue
		}
		out[k] = redactValue(v, depth)
	}
	return out
}

func redactValue(v any, depth int) any {
	if depth >= maxRedactDepth {
		// Too deep to keep walking. Returning the value unexamined would defeat
		// the point, so the subtree is dropped instead.
		return "[REDACTED: too deeply nested to inspect]"
	}
	switch t := v.(type) {
	case map[string]any:
		return redactMap(t, depth+1)
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = redactValue(item, depth+1)
		}
		return out
	default:
		return v
	}
}

// Logger writes audit entries to a per-session JSONL file under dir.
// It is safe for concurrent use; each Log call acquires a per-file mutex.
type Logger struct {
	dir       string
	mu        sync.Mutex
	fmu       sync.Map // path → *sync.Mutex
	retention time.Duration
	lastPrune time.Time
}

// New creates a Logger that writes to dir (created if it doesn't exist).
// Pass "" to disable audit logging (Log becomes a no-op).
func New(dir string) *Logger {
	return &Logger{dir: dir, retention: 30 * 24 * time.Hour}
}

// NewWithRetention creates a logger with age-based deletion. A zero duration
// keeps audit files indefinitely.
func NewWithRetention(dir string, retention time.Duration) *Logger {
	return &Logger{dir: dir, retention: retention}
}

// Log appends e to the audit file for e.SessionID.
// If the Logger was created with an empty dir, this is a no-op.
func (l *Logger) Log(e Entry) {
	if l == nil || l.dir == "" {
		return
	}
	l.pruneExpired(e.Timestamp)

	e.Args = redact.Map(e.Args)

	day := e.Timestamp.UTC().Format("2006-01-02")
	dir := filepath.Join(l.dir, day)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return // best-effort; don't crash the agent run over audit I/O
	}

	// Sanitise session ID to make it a safe filename.
	safe := sessionSafe(e.SessionID)
	path := filepath.Join(dir, safe+".jsonl")

	// Per-file mutex prevents interleaved writes from concurrent tool calls.
	raw, _ := l.fmu.LoadOrStore(path, &sync.Mutex{})
	mu := raw.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()

	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(f, "%s\n", data)
}

func (l *Logger) pruneExpired(now time.Time) {
	if l.retention <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.lastPrune.IsZero() && now.Sub(l.lastPrune) < time.Hour {
		return
	}
	l.lastPrune = now
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return
	}
	cutoff := now.Add(-l.retention)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		day, err := time.Parse("2006-01-02", entry.Name())
		if err == nil && day.Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(l.dir, entry.Name()))
		}
	}
}

// sessionSafe replaces any character that is not alphanumeric, dash, or
// underscore with an underscore so the session ID can be used as a filename.
var nonSafe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func sessionSafe(id string) string {
	s := nonSafe.ReplaceAllString(id, "_")
	if len(s) > 128 {
		s = s[:128]
	}
	if s == "" {
		s = "unknown"
	}
	return s
}
