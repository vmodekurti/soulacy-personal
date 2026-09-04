package gateway

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/workboard"
	"github.com/soulacy/soulacy/pkg/message"
)

func toolCallEvent(sessionID, name string, args map[string]any) message.Event {
	return message.Event{
		Type: "tool.call", AgentID: "agent-1", SessionID: sessionID,
		Payload:   message.ToolCall{ID: "tc", Name: name, Arguments: args},
		Timestamp: time.Now().UTC(),
	}
}

// mapToolCallEvent simulates an event round-tripped through the JSONL action
// log, where the payload deserialises as map[string]any.
func mapToolCallEvent(sessionID, name string, args map[string]any) message.Event {
	return message.Event{
		Type: "tool.call", AgentID: "agent-1", SessionID: sessionID,
		Payload:   map[string]any{"id": "tc", "name": name, "arguments": args},
		Timestamp: time.Now().UTC(),
	}
}

// ---------------------------------------------------------------------------
// detectArtifactPaths (pure)
// ---------------------------------------------------------------------------

func TestDetectArtifactPaths_WriteFile(t *testing.T) {
	events := []message.Event{
		toolCallEvent("wb-1", "write_file", map[string]any{"path": "/tmp/out.txt", "content": "x"}),
		toolCallEvent("wb-1", "read_file", map[string]any{"path": "/etc/hosts"}), // reads are not artifacts
		toolCallEvent("other-session", "write_file", map[string]any{"path": "/tmp/other.txt", "content": "x"}),
	}
	got := detectArtifactPaths(events, "wb-1")
	if len(got) != 1 {
		t.Fatalf("paths = %v, want 1", got)
	}
	if got[0].Path != "/tmp/out.txt" || got[0].Tool != "write_file" {
		t.Fatalf("artifact = %+v", got[0])
	}
}

func TestDetectArtifactPaths_DownloadFile(t *testing.T) {
	events := []message.Event{
		toolCallEvent("chat-1", "download_file", map[string]any{"url": "https://example.com/report.csv", "dest_path": "/tmp/report.csv"}),
	}
	got := detectArtifactPaths(events, "chat-1")
	if len(got) != 1 || got[0].Path != "/tmp/report.csv" || got[0].Tool != "download_file" {
		t.Fatalf("artifacts = %+v", got)
	}
}

func TestDetectArtifactPaths_MapPayload(t *testing.T) {
	events := []message.Event{
		mapToolCallEvent("wb-1", "write_file", map[string]any{"path": "/tmp/from-log.txt"}),
	}
	got := detectArtifactPaths(events, "wb-1")
	if len(got) != 1 || got[0].Path != "/tmp/from-log.txt" {
		t.Fatalf("artifacts = %+v", got)
	}
}

func TestDetectArtifactPaths_DedupesKeepingLastTool(t *testing.T) {
	events := []message.Event{
		toolCallEvent("wb-1", "write_file", map[string]any{"path": "/tmp/x.txt"}),
		toolCallEvent("wb-1", "write_file", map[string]any{"path": "/tmp/x.txt", "append": true}),
	}
	got := detectArtifactPaths(events, "wb-1")
	if len(got) != 1 {
		t.Fatalf("artifacts = %+v, want deduped 1", got)
	}
}

func TestDetectArtifactPaths_ExpandsHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	events := []message.Event{
		toolCallEvent("wb-1", "write_file", map[string]any{"path": "~/notes/a.md"}),
	}
	got := detectArtifactPaths(events, "wb-1")
	if len(got) != 1 || got[0].Path != filepath.Join(home, "notes/a.md") {
		t.Fatalf("artifacts = %+v", got)
	}
}

func TestDetectArtifactPaths_IgnoresEmptyAndNonWriteTools(t *testing.T) {
	events := []message.Event{
		toolCallEvent("wb-1", "write_file", map[string]any{"content": "no path"}),
		toolCallEvent("wb-1", "list_dir", map[string]any{"path": "/tmp"}),
		{Type: "tool.result", SessionID: "wb-1", Payload: map[string]any{"name": "write_file"}},
	}
	if got := detectArtifactPaths(events, "wb-1"); len(got) != 0 {
		t.Fatalf("artifacts = %+v, want none", got)
	}
}

// ---------------------------------------------------------------------------
// recordRunArtifacts: stat + persist + (best-effort) events
// ---------------------------------------------------------------------------

// fakeTailBackend satisfies the small Tail surface recordRunArtifacts needs.
type fakeTailBackend struct {
	events []message.Event
}

func (f *fakeTailBackend) Append(ev message.Event)     { f.events = append(f.events, ev) }
func (f *fakeTailBackend) EventFilePath(string) string { return "" }
func (f *fakeTailBackend) Close() error                { return nil }
func (f *fakeTailBackend) Tail(agentID string, limit int) ([]message.Event, error) {
	return f.events, nil
}
func (f *fakeTailBackend) QueryFiltered(agentID string, limit int, allowed map[string]bool) ([]message.Event, error) {
	out := make([]message.Event, 0, len(f.events))
	for _, ev := range f.events {
		if ev.AgentID != agentID {
			continue
		}
		if len(allowed) > 0 && !allowed[ev.Type] {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}
func (f *fakeTailBackend) QueryEvents(agentID, sessionID string, limit int, allowed map[string]bool) ([]message.Event, error) {
	out := make([]message.Event, 0, len(f.events))
	for _, ev := range f.events {
		if agentID != "" && ev.AgentID != agentID {
			continue
		}
		if sessionID != "" && ev.SessionID != sessionID {
			continue
		}
		if len(allowed) > 0 && !allowed[ev.Type] {
			continue
		}
		out = append(out, ev)
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}
func (f *fakeTailBackend) IncompleteMessageIns(time.Time) ([][]byte, error) { return nil, nil }
func (f *fakeTailBackend) CountMessageInAttempts(string, string, time.Time) (int, error) {
	return 0, nil
}
func (f *fakeTailBackend) MarkDeadLetter(string, string, string) error { return nil }

func wbArtifactGateway(t *testing.T, events []message.Event) (*Server, *workboard.Store, workboard.Task, workboard.Run) {
	t.Helper()
	s := newTestGateway(t, "")
	store, err := workboard.NewStore(filepath.Join(t.TempDir(), "wb.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s.SetWorkboardStore(store)
	s.actions = &fakeTailBackend{events: events}

	task, err := store.Create(t.Context(), workboard.Task{Title: "t", AgentID: "agent-1"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.StartRun(t.Context(), task.ID, "agent-1", "wb-art-1", "")
	if err != nil {
		t.Fatal(err)
	}
	return s, store, task, run
}

func TestRecordRunArtifacts_PersistsWithMetadata(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "report.md")
	if err := os.WriteFile(f1, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	events := []message.Event{
		toolCallEvent("wb-art-1", "write_file", map[string]any{"path": f1}),
		toolCallEvent("wb-art-1", "write_file", map[string]any{"path": filepath.Join(dir, "never-created.txt")}),
	}
	s, store, task, run := wbArtifactGateway(t, events)

	s.recordRunArtifacts(run, task)

	got, err := store.ListArtifacts(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("artifacts = %+v, want only the file that exists on disk", got)
	}
	a := got[0]
	if a.Path != f1 || a.SizeBytes != int64(len("hello world")) || a.Tool != "write_file" {
		t.Fatalf("artifact = %+v", a)
	}
}

func TestChatArtifacts_ListAndDownload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chat-report.md")
	if err := os.WriteFile(path, []byte("hello from chat"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestGateway(t, "")
	s.actions = &fakeTailBackend{events: []message.Event{
		toolCallEvent("chat-art-1", "write_file", map[string]any{"path": path}),
	}}

	status, body := gatewayJSON(t, s, "GET", "/api/v1/chat/artifacts?agent_id=agent-1&session_id=chat-art-1", "", "")
	if status != 200 {
		t.Fatalf("status=%d body=%v", status, body)
	}
	if body["count"].(float64) != 1 {
		t.Fatalf("body=%v, want one artifact", body)
	}
	arts := body["artifacts"].([]any)
	first := arts[0].(map[string]any)
	if first["name"] != "chat-report.md" || first["tool"] != "write_file" {
		t.Fatalf("artifact=%v", first)
	}

	status, raw := gatewayRaw(t, s, "GET", "/api/v1/chat/artifacts/download?agent_id=agent-1&session_id=chat-art-1&path="+url.QueryEscape(path), "", "")
	if status != 200 || !strings.Contains(raw, "hello from chat") {
		t.Fatalf("download status=%d raw=%q", status, raw)
	}
}

func TestChatArtifactDownload_RejectsPathOutsideSession(t *testing.T) {
	dir := t.TempDir()
	allowed := filepath.Join(dir, "allowed.txt")
	other := filepath.Join(dir, "other.txt")
	if err := os.WriteFile(allowed, []byte("allowed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestGateway(t, "")
	s.actions = &fakeTailBackend{events: []message.Event{
		toolCallEvent("chat-art-2", "write_file", map[string]any{"path": allowed}),
	}}

	status, body := gatewayJSON(t, s, "GET", "/api/v1/chat/artifacts/download?agent_id=agent-1&session_id=chat-art-2&path="+url.QueryEscape(other), "", "")
	if status != 404 {
		t.Fatalf("status=%d body=%v, want 404", status, body)
	}
}

func TestRecordRunArtifacts_NoEventsNoRows(t *testing.T) {
	s, store, task, run := wbArtifactGateway(t, nil)
	s.recordRunArtifacts(run, task)
	got, _ := store.ListArtifacts(t.Context(), task.ID)
	if len(got) != 0 {
		t.Fatalf("artifacts = %+v", got)
	}
}

// ---------------------------------------------------------------------------
// API: list + download
// ---------------------------------------------------------------------------

func TestWorkboardArtifactsAPI_ListAndDownload(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(f1, []byte("artifact-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	events := []message.Event{toolCallEvent("wb-art-1", "write_file", map[string]any{"path": f1})}
	s, store, task, run := wbArtifactGateway(t, events)
	s.recordRunArtifacts(run, task)

	status, body := gatewayJSON(t, s, "GET", fmt.Sprintf("/api/v1/workboard/tasks/%d/artifacts", task.ID), "", "")
	if status != 200 {
		t.Fatalf("list status = %d body=%v", status, body)
	}
	arts, _ := body["artifacts"].([]any)
	if len(arts) != 1 {
		t.Fatalf("artifacts = %v", body)
	}
	a := arts[0].(map[string]any)
	id := int64(a["id"].(float64))
	if a["path"] != f1 || a["tool"] != "write_file" {
		t.Fatalf("artifact = %v", a)
	}

	dstatus, dbody := gatewayRaw(t, s, "GET", fmt.Sprintf("/api/v1/workboard/artifacts/%d/download", id), "", "")
	if dstatus != 200 || dbody != "artifact-content" {
		t.Fatalf("download status=%d body=%q", dstatus, dbody)
	}
	_ = store
}

func TestWorkboardArtifactsAPI_Missing(t *testing.T) {
	s, _, task, _ := wbArtifactGateway(t, nil)
	status, body := gatewayJSON(t, s, "GET", fmt.Sprintf("/api/v1/workboard/tasks/%d/artifacts", task.ID), "", "")
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	if arts, _ := body["artifacts"].([]any); len(arts) != 0 {
		t.Fatalf("artifacts = %v", body)
	}
	if st, _ := gatewayJSON(t, s, "GET", "/api/v1/workboard/artifacts/99999/download", "", ""); st != 404 {
		t.Fatalf("download missing = %d, want 404", st)
	}
}

func TestWorkboardArtifactsAPI_DownloadFileGone410(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "gone.txt")
	if err := os.WriteFile(f1, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	events := []message.Event{toolCallEvent("wb-art-1", "write_file", map[string]any{"path": f1})}
	s, store, task, run := wbArtifactGateway(t, events)
	s.recordRunArtifacts(run, task)
	list, _ := store.ListArtifacts(t.Context(), task.ID)
	if err := os.Remove(f1); err != nil {
		t.Fatal(err)
	}
	st, _ := gatewayJSON(t, s, "GET", fmt.Sprintf("/api/v1/workboard/artifacts/%d/download", list[0].ID), "", "")
	if st != 410 {
		t.Fatalf("download deleted file = %d, want 410 Gone", st)
	}
}

func TestWorkboardArtifactsAPI_NoStore503(t *testing.T) {
	s := newTestGateway(t, "")
	if st, _ := gatewayJSON(t, s, "GET", "/api/v1/workboard/tasks/1/artifacts", "", ""); st != 503 {
		t.Fatalf("status = %d, want 503", st)
	}
}
