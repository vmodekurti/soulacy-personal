package gateway

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/session"
)

func TestChatAttachmentUploadListAndPromptExpansion(t *testing.T) {
	s, provider := newTestGatewayWithLLM(t, "secret")
	resStore, err := session.NewSQLiteStore(filepath.Join(t.TempDir(), "resources.db"), session.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resStore.Close() })
	s.SetResourceStore(resStore)
	s.engine.SetResourceStore(resStore)

	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret",
		`{"id":"attach-agent","name":"Attach Agent","trigger":"channel","channels":["http"],"llm":{"provider":"test","model":"m"},"system_prompt":"Use uploaded files.","enabled":true}`)
	if status != http.StatusCreated {
		t.Fatalf("create status=%d body=%v", status, body)
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("agent_id", "attach-agent")
	_ = mw.WriteField("session_id", "sess-attach")
	fw, err := mw.CreateFormFile("file", "notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write([]byte("alpha beta attachment content"))
	_ = mw.Close()

	req, err := http.NewRequest(http.MethodPost, "/api/v1/chat/attachments", &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := s.app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload status=%d", resp.StatusCode)
	}
	var upload struct {
		Attachment struct {
			ID string `json:"id"`
		} `json:"attachment"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&upload); err != nil {
		t.Fatal(err)
	}
	if upload.Attachment.ID == "" {
		t.Fatal("upload returned empty attachment id")
	}

	status, body = gatewayJSON(t, s, http.MethodGet, "/api/v1/chat/attachments?agent_id=attach-agent&session_id=sess-attach", "secret", "")
	if status != http.StatusOK || body["count"] != float64(1) {
		t.Fatalf("list status=%d body=%v", status, body)
	}

	chatBody := `{"agent_id":"attach-agent","session_id":"sess-attach","text":"Summarize this.","attachment_ids":["` + upload.Attachment.ID + `"]}`
	status, body = gatewayJSON(t, s, http.MethodPost, "/api/v1/chat", "secret", chatBody)
	if status != http.StatusOK {
		t.Fatalf("chat status=%d body=%v", status, body)
	}
	reqLLM := provider.lastRequest()
	if len(reqLLM.Messages) == 0 || !strings.Contains(reqLLM.Messages[len(reqLLM.Messages)-1].Content, "alpha beta attachment content") {
		t.Fatalf("LLM messages did not include attachment text: %#v", reqLLM.Messages)
	}
	if !strings.Contains(reqLLM.Messages[len(reqLLM.Messages)-1].Content, `id="`+upload.Attachment.ID+`"`) {
		t.Fatalf("LLM messages did not include attachment id: %#v", reqLLM.Messages)
	}
}

func TestChatAttachmentPromptExpansionCapsLargeAttachmentText(t *testing.T) {
	s, provider := newTestGatewayWithLLM(t, "secret")
	resStore, err := session.NewSQLiteStore(filepath.Join(t.TempDir(), "resources.db"), session.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resStore.Close() })
	s.SetResourceStore(resStore)
	s.engine.SetResourceStore(resStore)

	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret",
		`{"id":"attach-large-agent","name":"Attach Agent","trigger":"channel","channels":["http"],"llm":{"provider":"test","model":"m"},"system_prompt":"Use uploaded files.","enabled":true}`)
	if status != http.StatusCreated {
		t.Fatalf("create status=%d body=%v", status, body)
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("agent_id", "attach-large-agent")
	_ = mw.WriteField("session_id", "sess-large")
	fw, err := mw.CreateFormFile("file", "large.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write([]byte(strings.Repeat("large attachment content ", 2000)))
	_ = mw.Close()

	req, err := http.NewRequest(http.MethodPost, "/api/v1/chat/attachments", &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := s.app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload status=%d", resp.StatusCode)
	}
	var upload struct {
		Attachment struct {
			ID string `json:"id"`
		} `json:"attachment"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&upload); err != nil {
		t.Fatal(err)
	}

	chatBody := `{"agent_id":"attach-large-agent","session_id":"sess-large","text":"Summarize this.","attachment_ids":["` + upload.Attachment.ID + `"]}`
	status, body = gatewayJSON(t, s, http.MethodPost, "/api/v1/chat", "secret", chatBody)
	if status != http.StatusOK {
		t.Fatalf("chat status=%d body=%v", status, body)
	}
	content := provider.lastRequest().Messages[len(provider.lastRequest().Messages)-1].Content
	if len([]rune(content)) > chatAttachmentMessageMaxRunes+chatAttachmentPromptTotalRunes+1000 {
		t.Fatalf("expanded prompt too large: %d chars", len([]rune(content)))
	}
	if !strings.Contains(content, "[preview truncated]") {
		t.Fatalf("expected truncation marker in prompt: %q", content)
	}
}
