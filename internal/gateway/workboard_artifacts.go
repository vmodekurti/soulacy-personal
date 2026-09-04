// workboard_artifacts.go — artifact tracking for workboard runs (Story 13).
//
// After a run finishes, the run's tool-call trail (action log) is scanned
// for file-writing tools; produced files are stat'ed and attached to the
// task as artifacts. Each artifact emits a `run.artifact` event through the
// E1 event layer so webhooks and observers see produced files.
package gateway

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/workboard"
	"github.com/soulacy/soulacy/pkg/message"
)

// artifactTools maps tool names that produce files to the argument carrying
// the output path. Grow this map as new file-producing builtins land.
var artifactTools = map[string]string{
	"write_file":    "path",
	"download_file": "dest_path",
}

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
	events, err := s.actions.Tail(task.AgentID, 2000)
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
		arts = append(arts, workboard.Artifact{
			Path:      cand.Path,
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
	if err := store.AddArtifacts(fctx, task.ID, run.ID, arts); err != nil {
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
	arts, err := store.ListArtifacts(c.Context(), id)
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
	a, err := store.GetArtifact(c.Context(), id)
	if err != nil {
		return s.wbError(c, err)
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

func (s *Server) listChatArtifacts(agentID, sessionID string) ([]chatArtifact, error) {
	if s.actions == nil {
		return nil, errChatArtifactsUnavailable
	}
	events, err := s.actions.Tail(agentID, 2000)
	if err != nil {
		return nil, err
	}
	out := []chatArtifact{}
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
	arts, err := s.listChatArtifacts(agentID, sessionID)
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
	arts, err := s.listChatArtifacts(agentID, sessionID)
	if err != nil {
		if fe, ok := err.(*fiber.Error); ok {
			return s.errMsg(c, fe.Code, fe.Message)
		}
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	for _, a := range arts {
		if filepath.Clean(a.Path) != filepath.Clean(requested) {
			continue
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
