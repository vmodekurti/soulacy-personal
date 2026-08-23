// workboard_artifacts.go — artifact tracking for workboard runs (Story 13).
//
// After a run finishes, the run's tool-call trail (action log) is scanned
// for file-writing tools; produced files are stat'ed and attached to the
// task as artifacts. Each artifact emits a `run.artifact` event through the
// E1 event layer so webhooks and observers see produced files.
package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/artifactstore"
	"github.com/soulacy/soulacy/internal/workboard"
	"github.com/soulacy/soulacy/pkg/message"
)

// artifactTools maps tool names that produce files to the argument carrying
// the output path. Grow this map as new file-producing builtins land.
var artifactTools = map[string]string{
	"write_file":    "path",
	"download_file": "dest_path",
}

const chatArtifactEventType = "chat.artifact"

// artifactCandidate is one detected (path, tool) pair before stat/persist.
type artifactCandidate struct {
	Path string
	Tool string
}

// detectArtifactPaths scans events for file-writing tool calls belonging to
// sessionID. Pure function; payloads may be typed (message.ToolCall) or
// JSON-decoded maps (action-log round trip). Duplicate paths are deduped
// (last call wins). ~/ paths are expanded.
func detectArtifactPaths(events []message.Event, sessionID string) []artifactCandidate {
	seen := map[string]int{} // path → index in out
	var out []artifactCandidate
	for _, ev := range events {
		if ev.Type != "tool.call" || ev.SessionID != sessionID {
			continue
		}
		name, args := toolCallPayload(ev.Payload)
		argKey, ok := artifactTools[name]
		if !ok {
			continue
		}
		path, _ := args[argKey].(string)
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if strings.HasPrefix(path, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				path = filepath.Join(home, path[2:])
			}
		}
		cand := artifactCandidate{Path: path, Tool: name}
		if i, dup := seen[path]; dup {
			out[i] = cand
			continue
		}
		seen[path] = len(out)
		out = append(out, cand)
	}
	return out
}

// toolCallPayload extracts (name, arguments) from either payload shape.
func toolCallPayload(p any) (string, map[string]any) {
	switch v := p.(type) {
	case message.ToolCall:
		return v.Name, v.Arguments
	case *message.ToolCall:
		if v != nil {
			return v.Name, v.Arguments
		}
	case map[string]any:
		name, _ := v["name"].(string)
		args, _ := v["arguments"].(map[string]any)
		return name, args
	}
	return "", nil
}

// wbStoreCtx is a short store-write context detached from request lifetimes.
func wbStoreCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// recordRunArtifacts detects, stats, persists, and announces the files a
// finished run produced. Best-effort: failures log warnings, never fail
// the run.
func (s *Server) recordRunArtifacts(run workboard.Run, task workboard.Task) {
	store := s.workboardStore
	if store == nil || s.actions == nil {
		return
	}
	events, err := s.actionLogForWorkspace(task.WorkspaceID).Tail(task.AgentID, 2000)
	if err != nil {
		s.log.Warn("workboard: artifact detection tail failed",
			zap.Int64("run", run.ID), zap.Error(err))
		return
	}
	var arts []workboard.Artifact
	for _, cand := range detectArtifactPaths(events, run.SessionID) {
		st, err := os.Stat(cand.Path)
		if err != nil || st.IsDir() {
			continue // never materialised (tool failed) or not a file
		}
		artifactPath := cand.Path
		if s.artifactObjects != nil {
			key := sharedArtifactKey(task.WorkspaceID, run.SessionID, cand.Path)
			file, openErr := os.Open(cand.Path)
			if openErr != nil {
				continue
			}
			putCtx, cancel := wbStoreCtx()
			putErr := s.artifactObjects.Put(putCtx, key, file, st.Size(), mime.TypeByExtension(filepath.Ext(cand.Path)))
			cancel()
			_ = file.Close()
			if putErr != nil {
				s.log.Warn("workboard: shared artifact upload failed", zap.String("path", cand.Path), zap.Error(putErr))
				continue
			}
			artifactPath = "artifact://" + key
		}
		arts = append(arts, workboard.Artifact{
			Path:      artifactPath,
			SizeBytes: st.Size(),
			Tool:      cand.Tool,
			CreatedAt: st.ModTime().UTC(),
		})
	}
	if len(arts) == 0 {
		return
	}
	fctx, cancel := wbStoreCtx()
	defer cancel()
	if err := store.AddArtifacts(fctx, task.WorkspaceID, task.ID, run.ID, arts); err != nil {
		s.log.Warn("workboard: artifact persist failed",
			zap.Int64("run", run.ID), zap.Error(err))
		return
	}
	for _, a := range arts {
		s.emitArtifactEvent(run, task, a)
	}
	s.log.Info("workboard: artifacts recorded",
		zap.Int64("task", task.ID), zap.Int64("run", run.ID), zap.Int("count", len(arts)))
}

func sharedArtifactKey(workspaceID, runID, sourcePath string) string {
	digest := sha256.Sum256([]byte(sourcePath))
	name := strings.ReplaceAll(filepath.Base(sourcePath), "\\", "_")
	if name == "" || name == "." {
		name = "artifact"
	}
	return fmt.Sprintf("%sruns/%s/%x-%s", artifactstore.WorkspacePrefix(workspaceID), url.PathEscape(runID), digest[:8], name)
}

func sharedChatArtifactKey(workspaceID, agentID, sessionID, sourcePath string) string {
	digest := sha256.Sum256([]byte(sourcePath))
	name := strings.ReplaceAll(filepath.Base(sourcePath), "\\", "_")
	if name == "" || name == "." {
		name = "artifact"
	}
	return fmt.Sprintf("%schats/%s/%s/%x-%s", artifactstore.WorkspacePrefix(workspaceID),
		url.PathEscape(agentID), url.PathEscape(sessionID), digest[:8], name)
}

func artifactObjectKey(ref string) (string, bool) {
	key, ok := strings.CutPrefix(ref, "artifact://")
	return key, ok && key != ""
}

// emitArtifactEvent publishes run.artifact through the EventHub (E1 layer:
// WebSocket stream, action log, queue publisher → webhooks).
func (s *Server) emitArtifactEvent(run workboard.Run, task workboard.Task, a workboard.Artifact) {
	if s.hub == nil {
		return
	}
	s.hub.Emit(message.Event{
		Type:      "run.artifact",
		AgentID:   task.AgentID,
		SessionID: run.SessionID,
		Payload: map[string]any{
			"task_id":    task.ID,
			"task_title": task.Title,
			"run_id":     run.ID,
			"attempt":    run.Attempt,
			"path":       a.Path,
			"size_bytes": a.SizeBytes,
			"tool":       a.Tool,
		},
		Timestamp: time.Now().UTC(),
	})
}

// handleWorkboardArtifacts lists a task's artifacts.
//
//	GET /api/v1/workboard/tasks/:id/artifacts
func (s *Server) handleWorkboardArtifacts(c *fiber.Ctx) error {
	store := s.workboardStore
	if store == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workboard store not configured")
	}
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid task id")
	}
	arts, err := store.ListArtifacts(c.Context(), s.wbWorkspace(c), id)
	if err != nil {
		return s.wbError(c, err)
	}
	return c.JSON(fiber.Map{"artifacts": arts, "count": len(arts)})
}

// handleWorkboardArtifactDownload streams one artifact file.
//
//	GET /api/v1/workboard/artifacts/:id/download
//
// 404 unknown artifact; 410 Gone when the recorded file no longer exists.
func (s *Server) handleWorkboardArtifactDownload(c *fiber.Ctx) error {
	store := s.workboardStore
	if store == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workboard store not configured")
	}
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid artifact id")
	}
	a, err := store.GetArtifact(c.Context(), s.wbWorkspace(c), id)
	if err != nil {
		return s.wbError(c, err)
	}
	if key, shared := artifactObjectKey(a.Path); shared {
		if s.artifactObjects == nil {
			return s.errMsg(c, fiber.StatusServiceUnavailable, "shared artifact store is unavailable")
		}
		object, openErr := s.artifactObjects.Open(c.UserContext(), key)
		if openErr != nil {
			return c.Status(fiber.StatusGone).JSON(fiber.Map{"error": "artifact object no longer exists"})
		}
		name := filepath.Base(key)
		c.Set(fiber.HeaderContentDisposition, `attachment; filename="`+name+`"`)
		if object.ContentType != "" {
			c.Set(fiber.HeaderContentType, object.ContentType)
		}
		c.Context().SetBodyStream(object.Body, int(object.Size))
		return nil
	}
	if st, err := os.Stat(a.Path); err != nil || st.IsDir() {
		return c.Status(fiber.StatusGone).JSON(fiber.Map{
			"error": "artifact file no longer exists on disk",
			"path":  a.Path,
		})
	}
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="`+filepath.Base(a.Path)+`"`)
	return c.SendFile(a.Path)
}

type chatArtifact struct {
	Path        string    `json:"path"`
	Name        string    `json:"name"`
	SizeBytes   int64     `json:"size_bytes"`
	Tool        string    `json:"tool"`
	CreatedAt   time.Time `json:"created_at"`
	DownloadURL string    `json:"download_url"`
}

func (s *Server) observeTerminalArtifacts(event message.Event) {
	if s.artifactObjects == nil || (event.Type != "message.out" && event.Type != "error") ||
		strings.TrimSpace(event.AgentID) == "" || strings.TrimSpace(event.SessionID) == "" {
		return
	}
	go func() {
		if err := s.materializeChatArtifacts(event.WorkspaceID, event.AgentID, event.SessionID); err != nil {
			s.log.Warn("chat: shared artifact materialization failed", zap.String("agent", event.AgentID),
				zap.String("session", event.SessionID), zap.Error(err))
		}
	}()
}

// materializeChatArtifacts runs on the replica that completed the chat turn.
// It copies produced files into shared object storage before another replica
// needs to list or download them, then records opaque references in the shared
// action log. Existing references make retries idempotent.
func (s *Server) materializeChatArtifacts(workspaceID, agentID, sessionID string) error {
	if s.artifactObjects == nil {
		return nil
	}
	actions := s.actionLogForWorkspace(workspaceID)
	events, err := actions.Tail(agentID, 2000)
	if err != nil {
		return err
	}
	existing := make(map[string]bool)
	for _, ev := range events {
		if ev.Type != chatArtifactEventType || ev.SessionID != sessionID {
			continue
		}
		if payload, ok := ev.Payload.(map[string]any); ok {
			if source, _ := payload["source_path"].(string); source != "" {
				existing[source] = true
			}
		}
	}
	for _, cand := range detectArtifactPaths(events, sessionID) {
		if existing[cand.Path] {
			continue
		}
		st, statErr := os.Stat(cand.Path)
		if statErr != nil || st.IsDir() {
			continue
		}
		file, openErr := os.Open(cand.Path)
		if openErr != nil {
			continue
		}
		key := sharedChatArtifactKey(actions.workspaceID, agentID, sessionID, cand.Path)
		putCtx, cancel := wbStoreCtx()
		putErr := s.artifactObjects.Put(putCtx, key, file, st.Size(), mime.TypeByExtension(filepath.Ext(cand.Path)))
		cancel()
		_ = file.Close()
		if putErr != nil {
			return putErr
		}
		if s.hub != nil {
			s.hub.Emit(message.Event{
				Type: chatArtifactEventType, WorkspaceID: actions.workspaceID, AgentID: agentID, SessionID: sessionID,
				Payload: map[string]any{"source_path": cand.Path, "ref": "artifact://" + key, "name": filepath.Base(cand.Path),
					"size_bytes": st.Size(), "tool": cand.Tool, "created_at": st.ModTime().UTC()},
				Timestamp: time.Now().UTC(),
			})
		}
	}
	return nil
}

func (s *Server) listChatArtifacts(actions actionScope, agentID, sessionID string) ([]chatArtifact, error) {
	if !actions.Available() {
		return nil, errChatArtifactsUnavailable
	}
	events, err := actions.Tail(agentID, 2000)
	if err != nil {
		return nil, err
	}
	out := []chatArtifact{}
	if s.artifactObjects != nil {
		for _, ev := range events {
			if ev.Type != chatArtifactEventType || ev.SessionID != sessionID {
				continue
			}
			payload, ok := ev.Payload.(map[string]any)
			if !ok {
				continue
			}
			ref, _ := payload["ref"].(string)
			name, _ := payload["name"].(string)
			tool, _ := payload["tool"].(string)
			size := artifactInt64(payload["size_bytes"])
			createdAt := artifactTime(payload["created_at"])
			if ref == "" {
				continue
			}
			out = append(out, chatArtifact{Path: ref, Name: name, SizeBytes: size, Tool: tool, CreatedAt: createdAt,
				DownloadURL: "/api/v1/chat/artifacts/download?agent_id=" + url.QueryEscape(agentID) +
					"&session_id=" + url.QueryEscape(sessionID) + "&path=" + url.QueryEscape(ref)})
		}
		return out, nil
	}
	for _, cand := range detectArtifactPaths(events, sessionID) {
		st, err := os.Stat(cand.Path)
		if err != nil || st.IsDir() {
			continue
		}
		out = append(out, chatArtifact{
			Path:      cand.Path,
			Name:      filepath.Base(cand.Path),
			SizeBytes: st.Size(),
			Tool:      cand.Tool,
			CreatedAt: st.ModTime().UTC(),
			DownloadURL: "/api/v1/chat/artifacts/download?agent_id=" + url.QueryEscape(agentID) +
				"&session_id=" + url.QueryEscape(sessionID) +
				"&path=" + url.QueryEscape(cand.Path),
		})
	}
	return out, nil
}

func artifactInt64(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	case json.Number:
		out, _ := typed.Int64()
		return out
	default:
		return 0
	}
}

func artifactTime(value any) time.Time {
	if typed, ok := value.(time.Time); ok {
		return typed
	}
	if raw, ok := value.(string); ok {
		out, _ := time.Parse(time.RFC3339Nano, raw)
		return out
	}
	return time.Time{}
}

var errChatArtifactsUnavailable = fiber.NewError(fiber.StatusServiceUnavailable, "action log is not configured")

// handleChatArtifacts lists files produced during a chat session.
//
//	GET /api/v1/chat/artifacts?agent_id=<agent>&session_id=<session>
func (s *Server) handleChatArtifacts(c *fiber.Ctx) error {
	agentID := strings.TrimSpace(c.Query("agent_id"))
	sessionID := strings.TrimSpace(c.Query("session_id"))
	if agentID == "" || sessionID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "agent_id and session_id are required")
	}
	if err := s.requireSession(c, agentID, sessionID); err != nil {
		return err
	}
	arts, err := s.listChatArtifacts(s.actionLog(c), agentID, sessionID)
	if err != nil {
		if fe, ok := err.(*fiber.Error); ok {
			return s.errMsg(c, fe.Code, fe.Message)
		}
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.JSON(fiber.Map{"artifacts": arts, "count": len(arts)})
}

// handleChatArtifactDownload streams a chat artifact after proving that the
// requested path belongs to the given agent/session trail. This avoids turning
// the chat API into a general-purpose filesystem reader.
//
//	GET /api/v1/chat/artifacts/download?agent_id=<agent>&session_id=<session>&path=<path>
func (s *Server) handleChatArtifactDownload(c *fiber.Ctx) error {
	agentID := strings.TrimSpace(c.Query("agent_id"))
	sessionID := strings.TrimSpace(c.Query("session_id"))
	requested := strings.TrimSpace(c.Query("path"))
	if agentID == "" || sessionID == "" || requested == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "agent_id, session_id, and path are required")
	}
	if err := s.requireSession(c, agentID, sessionID); err != nil {
		return err
	}
	arts, err := s.listChatArtifacts(s.actionLog(c), agentID, sessionID)
	if err != nil {
		if fe, ok := err.(*fiber.Error); ok {
			return s.errMsg(c, fe.Code, fe.Message)
		}
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	for _, a := range arts {
		if a.Path != requested && filepath.Clean(a.Path) != filepath.Clean(requested) {
			continue
		}
		if key, shared := artifactObjectKey(a.Path); shared {
			if s.artifactObjects == nil {
				return s.errMsg(c, fiber.StatusServiceUnavailable, "shared artifact store is unavailable")
			}
			object, openErr := s.artifactObjects.Open(c.UserContext(), key)
			if openErr != nil {
				return c.Status(fiber.StatusGone).JSON(fiber.Map{"error": "artifact object no longer exists"})
			}
			c.Set(fiber.HeaderContentDisposition, `attachment; filename="`+a.Name+`"`)
			if object.ContentType != "" {
				c.Set(fiber.HeaderContentType, object.ContentType)
			}
			c.Context().SetBodyStream(object.Body, int(object.Size))
			return nil
		}
		if st, err := os.Stat(a.Path); err != nil || st.IsDir() {
			return c.Status(fiber.StatusGone).JSON(fiber.Map{
				"error": "artifact file no longer exists on disk",
				"path":  a.Path,
			})
		}
		c.Set(fiber.HeaderContentDisposition, `attachment; filename="`+filepath.Base(a.Path)+`"`)
		return c.SendFile(a.Path)
	}
	return s.errMsg(c, fiber.StatusNotFound, "artifact was not produced by this chat session")
}
