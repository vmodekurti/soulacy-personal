package gateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/knowledge"
	"github.com/soulacy/soulacy/internal/session"
)

const (
	chatAttachmentStoredTextMaxRunes  = 16_000
	chatAttachmentPromptPreviewRunes  = 1_200
	chatAttachmentPromptTotalRunes    = 4_000
	chatAttachmentMessageMaxRunes     = 8_000
	chatAttachmentExtractWarnMinRunes = 2_000
)

type attachmentStore interface {
	session.ResourceStore
	PutAttachment(ctx context.Context, att session.Attachment, data []byte, ttl time.Duration) error
	ListAttachments(ctx context.Context, agentID, sessionID string) ([]session.Attachment, error)
	GetAttachment(ctx context.Context, id string) (session.Attachment, []byte, error)
}

type chatAttachmentView struct {
	ID          string    `json:"id"`
	SessionID   string    `json:"session_id"`
	AgentID     string    `json:"agent_id"`
	Filename    string    `json:"filename"`
	MIMEType    string    `json:"mime_type"`
	SizeBytes   int64     `json:"size_bytes"`
	TextPreview string    `json:"text_preview,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	DownloadURL string    `json:"download_url"`
}

func (s *Server) chatAttachmentStore() (attachmentStore, bool) {
	st, ok := s.resourceStore.(attachmentStore)
	return st, ok
}

func attachmentView(a session.Attachment) chatAttachmentView {
	return chatAttachmentView{
		ID:          a.ID,
		SessionID:   a.SessionID,
		AgentID:     a.AgentID,
		Filename:    a.Filename,
		MIMEType:    a.MIMEType,
		SizeBytes:   a.SizeBytes,
		TextPreview: textPreview(a.Text, 700),
		CreatedAt:   a.CreatedAt,
		ExpiresAt:   a.ExpiresAt,
		DownloadURL: "/api/v1/chat/attachments/" + a.ID + "/download",
	}
}

func textPreview(s string, max int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= max {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:max])) + "..."
}

// handleChatAttachmentUpload stores one file for a chat session and extracts
// best-effort text so a later /chat request can include it in the prompt.
//
//	POST /api/v1/chat/attachments multipart:
//	  file, agent_id, session_id
func (s *Server) handleChatAttachmentUpload(c *fiber.Ctx) error {
	st, ok := s.chatAttachmentStore()
	if !ok {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "session resource store not configured")
	}
	agentID := strings.TrimSpace(c.FormValue("agent_id"))
	sessionID := strings.TrimSpace(c.FormValue("session_id"))
	if agentID == "" || sessionID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "agent_id and session_id are required")
	}
	if err := s.claimSession(c, agentID, sessionID); err != nil {
		return err
	}
	fh, err := c.FormFile("file")
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, fmt.Sprintf("multipart upload missing 'file': %v", err))
	}
	f, err := fh.Open()
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}
	data, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}
	filename := filepath.Base(fh.Filename)
	mimeType := knowledge.MimeFromFilename(filename)
	if mimeType == "" {
		mimeType = fh.Header.Get("Content-Type")
	}
	text, extractErr := knowledge.ExtractText(mimeType, data)
	if extractErr != nil {
		text = ""
	}
	text, textTruncated := truncateRunes(strings.TrimSpace(text), chatAttachmentStoredTextMaxRunes)
	att := session.Attachment{
		ID:        uuid.New().String(),
		SessionID: sessionID,
		AgentID:   agentID,
		Filename:  filename,
		MIMEType:  mimeType,
		Text:      text,
	}
	if err := st.PutAttachment(c.UserContext(), att, data, session.DefaultAttachmentTTL); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	s.log.Info("chat attachment uploaded",
		zap.String("agent", agentID), zap.String("session", sessionID), zap.String("filename", filename),
		zap.Bool("text_truncated", textTruncated))
	created, _, err := st.GetAttachment(c.UserContext(), att.ID)
	if err != nil {
		created = att
		created.SizeBytes = int64(len(data))
		created.CreatedAt = time.Now().UTC()
		created.ExpiresAt = created.CreatedAt.Add(session.DefaultAttachmentTTL)
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"attachment": attachmentView(created)})
}

// handleChatAttachments lists attachments for one chat session.
//
//	GET /api/v1/chat/attachments?agent_id=<agent>&session_id=<session>
func (s *Server) handleChatAttachments(c *fiber.Ctx) error {
	st, ok := s.chatAttachmentStore()
	if !ok {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "session resource store not configured")
	}
	agentID := strings.TrimSpace(c.Query("agent_id"))
	sessionID := strings.TrimSpace(c.Query("session_id"))
	if agentID == "" || sessionID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "agent_id and session_id are required")
	}
	if err := s.requireSession(c, agentID, sessionID); err != nil {
		return err
	}
	attachments, err := st.ListAttachments(c.UserContext(), agentID, sessionID)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	views := make([]chatAttachmentView, 0, len(attachments))
	for _, a := range attachments {
		views = append(views, attachmentView(a))
	}
	return c.JSON(fiber.Map{"attachments": views, "count": len(views)})
}

// handleChatAttachmentDownload streams one uploaded attachment after checking
// the caller-provided agent/session still match the stored metadata.
//
//	GET /api/v1/chat/attachments/:id/download?agent_id=<agent>&session_id=<session>
func (s *Server) handleChatAttachmentDownload(c *fiber.Ctx) error {
	st, ok := s.chatAttachmentStore()
	if !ok {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "session resource store not configured")
	}
	agentID := strings.TrimSpace(c.Query("agent_id"))
	sessionID := strings.TrimSpace(c.Query("session_id"))
	if agentID == "" || sessionID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "agent_id and session_id are required")
	}
	if err := s.requireSession(c, agentID, sessionID); err != nil {
		return err
	}
	att, data, err := st.GetAttachment(c.UserContext(), c.Params("id"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), sql.ErrNoRows.Error()) {
			return s.errMsg(c, fiber.StatusNotFound, "attachment not found")
		}
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if att.AgentID != agentID || att.SessionID != sessionID {
		return s.errMsg(c, fiber.StatusNotFound, "attachment not found for this chat session")
	}
	c.Set(fiber.HeaderContentType, att.MIMEType)
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="`+att.Filename+`"`)
	return c.Send(data)
}

func (s *Server) expandChatAttachments(ctx context.Context, agentID, sessionID, text string, ids []string) (string, error) {
	st, ok := s.chatAttachmentStore()
	if !ok {
		return "", fmt.Errorf("session resource store not configured")
	}
	var b strings.Builder
	base, baseTruncated := truncateRunes(strings.TrimSpace(text), chatAttachmentMessageMaxRunes)
	b.WriteString(base)
	if baseTruncated {
		b.WriteString("\n\n[user message truncated before attachment context]")
	}
	b.WriteString("\n\n<attachments>\n")
	b.WriteString("Attachment bodies are intentionally summarized here to keep chat and workflow execution bounded. Use the attachment id/file record for full ingestion.\n")
	remaining := chatAttachmentPromptTotalRunes
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		att, _, err := st.GetAttachment(ctx, id)
		if err != nil {
			return "", fmt.Errorf("attachment %q not found", id)
		}
		if att.AgentID != agentID || att.SessionID != sessionID {
			return "", fmt.Errorf("attachment %q does not belong to this chat session", id)
		}
		excerpt := strings.TrimSpace(att.Text)
		if excerpt == "" {
			excerpt = "(no extractable text; use the filename and MIME type as context)"
		}
		perAttachmentMax := chatAttachmentPromptPreviewRunes
		if remaining < perAttachmentMax {
			perAttachmentMax = remaining
		}
		if perAttachmentMax <= 0 {
			break
		}
		excerpt, truncated := truncateRunes(excerpt, perAttachmentMax)
		b.WriteString("\n<attachment filename=\"")
		b.WriteString(strings.ReplaceAll(att.Filename, `"`, `'`))
		b.WriteString("\" mime_type=\"")
		b.WriteString(strings.ReplaceAll(att.MIMEType, `"`, `'`))
		b.WriteString("\" id=\"")
		b.WriteString(strings.ReplaceAll(att.ID, `"`, `'`))
		b.WriteString("\" size_bytes=\"")
		b.WriteString(fmt.Sprint(att.SizeBytes))
		b.WriteString("\">\n")
		b.WriteString("preview:\n")
		b.WriteString(excerpt)
		if truncated {
			b.WriteString("\n[preview truncated]")
		}
		if len([]rune(att.Text)) >= chatAttachmentExtractWarnMinRunes {
			b.WriteString("\n[note: full extracted text is stored with the attachment metadata; do not ask the LLM to process the full body inline]")
		}
		b.WriteString("\n</attachment>\n")
		remaining -= len([]rune(excerpt))
		if remaining <= 0 {
			break
		}
	}
	b.WriteString("</attachments>")
	return b.String(), nil
}

func truncateRunes(s string, max int) (string, bool) {
	s = strings.TrimSpace(s)
	if max <= 0 {
		return "", s != ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s, false
	}
	return strings.TrimSpace(string(r[:max])) + "...", true
}
