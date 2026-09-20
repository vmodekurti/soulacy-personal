package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/mcpserver"
)

const maxMCPGatewayResponseBytes = 16 << 20

// validateMCPOrigin implements Streamable HTTP's DNS-rebinding protection.
// Native MCP clients generally omit Origin; browser callers must come from the
// gateway itself or an origin explicitly allowed for this deployment.
func (s *Server) validateMCPOrigin(c *fiber.Ctx) error {
	origin := strings.TrimSpace(c.Get(fiber.HeaderOrigin))
	if origin == "" {
		return c.Next()
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "MCP request origin is not allowed"})
	}
	if strings.EqualFold(u.Host, c.Get(fiber.HeaderHost)) {
		return c.Next()
	}
	allowed := append([]string(nil), s.cfg.Server.AllowedOrigins...)
	if len(s.cfg.Server.AllowedOrigins) == 0 {
		allowed = append(allowed, "http://localhost:3000", "http://localhost:5173")
	}
	for _, candidate := range allowed {
		if strings.EqualFold(strings.TrimRight(strings.TrimSpace(candidate), "/"), strings.TrimRight(origin, "/")) {
			return c.Next()
		}
	}
	return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "MCP request origin is not allowed"})
}

// handleMCPServeHTTP exposes Soulacy itself as a stateless Streamable HTTP MCP
// server. Each request gets a client bound to the caller's credential; the
// client goes through the gateway's own API routes so existing auth, RBAC,
// ownership, and rate-limit checks remain authoritative.
func (s *Server) handleMCPServeHTTP(c *fiber.Ctx) error {
	contentType := strings.ToLower(strings.TrimSpace(c.Get(fiber.HeaderContentType)))
	if contentType == "" || !strings.HasPrefix(contentType, fiber.MIMEApplicationJSON) {
		return c.Status(fiber.StatusUnsupportedMediaType).JSON(fiber.Map{
			"error": "MCP requests require Content-Type: application/json",
		})
	}
	if accept := strings.ToLower(c.Get(fiber.HeaderAccept)); accept != "" && accept != "*/*" && !strings.Contains(accept, fiber.MIMEApplicationJSON) {
		return c.Status(fiber.StatusNotAcceptable).JSON(fiber.Map{
			"error": "this MCP endpoint returns application/json",
		})
	}
	if version := strings.TrimSpace(c.Get("MCP-Protocol-Version")); version != "" && !mcpserver.SupportsProtocolVersion(version) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error":     "unsupported MCP protocol version",
			"requested": version,
		})
	}

	srv := &mcpserver.Server{
		Client:        &gatewayMCPClient{server: s, authorization: c.Get(fiber.HeaderAuthorization)},
		Name:          "soulacy",
		Version:       config.Version,
		ToolPrefix:    "soulacy_agent_",
		UserID:        "mcp-user",
		SessionPrefix: "mcp",
	}
	data, responds := srv.Handle(c.UserContext(), c.Body())
	if !responds {
		return c.Status(fiber.StatusAccepted).Send(nil)
	}
	c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSONCharsetUTF8)
	return c.Status(fiber.StatusOK).Send(data)
}

func (s *Server) handleMCPServeUnsupported(c *fiber.Ctx) error {
	c.Set(fiber.HeaderAllow, fiber.MethodPost)
	return c.SendStatus(fiber.StatusMethodNotAllowed)
}

type gatewayMCPClient struct {
	server        *Server
	authorization string
}

func (m *gatewayMCPClient) request(ctx context.Context, method, path string, body []byte) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://soulacy.internal/api/v1"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if len(body) > 0 {
		req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	}
	if m.authorization != "" {
		req.Header.Set(fiber.HeaderAuthorization, m.authorization)
	}
	resp, err := m.server.app.Test(req, -1)
	if err != nil {
		return nil, fmt.Errorf("gateway request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxMCPGatewayResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read gateway response: %w", err)
	}
	if len(raw) > maxMCPGatewayResponseBytes {
		return nil, fmt.Errorf("gateway response exceeded %d bytes", maxMCPGatewayResponseBytes)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		var payload struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &payload)
		if payload.Error == "" {
			payload.Error = http.StatusText(resp.StatusCode)
		}
		return nil, fmt.Errorf("gateway API returned %d: %s", resp.StatusCode, payload.Error)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage(`{}`), nil
	}
	return json.RawMessage(raw), nil
}

func (m *gatewayMCPClient) ListAgents(ctx context.Context) ([]mcpserver.Agent, error) {
	data, err := m.request(ctx, http.MethodGet, "/agents", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Agents []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Enabled     bool   `json:"enabled"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("decode agents: %w", err)
	}
	agents := make([]mcpserver.Agent, 0, len(resp.Agents))
	for _, agent := range resp.Agents {
		agents = append(agents, mcpserver.Agent{
			ID: agent.ID, Name: agent.Name, Description: agent.Description, Enabled: agent.Enabled,
		})
	}
	return agents, nil
}

func (m *gatewayMCPClient) Chat(ctx context.Context, req mcpserver.ChatRequest) (mcpserver.ChatResponse, error) {
	body, err := json.Marshal(map[string]any{
		"agent_id": req.AgentID, "session_id": req.SessionID, "user_id": req.UserID,
		"username": req.UserID, "text": req.Text,
	})
	if err != nil {
		return mcpserver.ChatResponse{}, err
	}
	data, err := m.request(ctx, http.MethodPost, "/chat", body)
	if err != nil {
		return mcpserver.ChatResponse{}, err
	}
	var resp struct {
		Reply string `json:"reply"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return mcpserver.ChatResponse{}, fmt.Errorf("decode chat: %w", err)
	}
	return mcpserver.ChatResponse{Reply: resp.Reply}, nil
}

func (m *gatewayMCPClient) ListSchedule(ctx context.Context) (json.RawMessage, error) {
	return m.request(ctx, http.MethodGet, "/schedule", nil)
}

func (m *gatewayMCPClient) ScheduleStatus(ctx context.Context) (json.RawMessage, error) {
	return m.request(ctx, http.MethodGet, "/schedule/status", nil)
}

func (m *gatewayMCPClient) ListWorkboardTasks(ctx context.Context, status, agentID string) (json.RawMessage, error) {
	query := url.Values{}
	if status = strings.TrimSpace(status); status != "" {
		query.Set("status", status)
	}
	if agentID = strings.TrimSpace(agentID); agentID != "" {
		query.Set("agent_id", agentID)
	}
	path := "/workboard/tasks"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return m.request(ctx, http.MethodGet, path, nil)
}

func (m *gatewayMCPClient) RunWorkboardTask(ctx context.Context, id string) (json.RawMessage, error) {
	return m.request(ctx, http.MethodPost, "/workboard/tasks/"+url.PathEscape(id)+"/run", nil)
}

func (m *gatewayMCPClient) ListKnowledgeBases(ctx context.Context) (json.RawMessage, error) {
	return m.request(ctx, http.MethodGet, "/knowledge", nil)
}

func (m *gatewayMCPClient) SearchKnowledge(ctx context.Context, kb, query string, topK int) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]any{"query": query, "top_k": topK})
	if err != nil {
		return nil, err
	}
	return m.request(ctx, http.MethodPost, "/knowledge/"+url.PathEscape(kb)+"/search", body)
}

func (m *gatewayMCPClient) ListQueues(ctx context.Context) (json.RawMessage, error) {
	return m.request(ctx, http.MethodGet, "/queues", nil)
}

func (m *gatewayMCPClient) CreateQueue(ctx context.Context, queue string) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]any{"queue": queue})
	if err != nil {
		return nil, err
	}
	return m.request(ctx, http.MethodPost, "/queues", body)
}

func (m *gatewayMCPClient) ListQueueItems(ctx context.Context, queue string, limit int) (json.RawMessage, error) {
	query := url.Values{}
	if queue = strings.TrimSpace(queue); queue != "" {
		query.Set("queue", queue)
	}
	if limit > 0 {
		query.Set("limit", fmt.Sprint(limit))
	}
	path := "/queues/items"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return m.request(ctx, http.MethodGet, path, nil)
}

func (m *gatewayMCPClient) PutQueueItem(ctx context.Context, queue string, item json.RawMessage, ttlSeconds int) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]any{"queue": queue, "item": item, "ttl_seconds": ttlSeconds})
	if err != nil {
		return nil, err
	}
	return m.request(ctx, http.MethodPost, "/queues/items", body)
}

func (m *gatewayMCPClient) TakeQueueItem(ctx context.Context, queue string) (json.RawMessage, error) {
	path := "/queues/take"
	if queue = strings.TrimSpace(queue); queue != "" {
		path += "?queue=" + url.QueryEscape(queue)
	}
	return m.request(ctx, http.MethodPost, path, nil)
}

func (m *gatewayMCPClient) ClearQueue(ctx context.Context, queue string) (json.RawMessage, error) {
	path := "/queues/items"
	if queue = strings.TrimSpace(queue); queue != "" {
		path += "?queue=" + url.QueryEscape(queue)
	}
	return m.request(ctx, http.MethodDelete, path, nil)
}
