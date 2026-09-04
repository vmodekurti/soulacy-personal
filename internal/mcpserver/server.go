package mcpserver

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode"
)

const protocolVersion = "2024-11-05"

type Agent struct {
	ID          string
	Name        string
	Description string
	Enabled     bool
}

type ChatRequest struct {
	AgentID   string
	SessionID string
	UserID    string
	Text      string
}

type ChatResponse struct {
	Reply string
}

type GatewayClient interface {
	ListAgents(ctx context.Context) ([]Agent, error)
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}

type ScheduleClient interface {
	ListSchedule(ctx context.Context) (json.RawMessage, error)
	ScheduleStatus(ctx context.Context) (json.RawMessage, error)
}

type WorkboardClient interface {
	ListWorkboardTasks(ctx context.Context, status, agentID string) (json.RawMessage, error)
	RunWorkboardTask(ctx context.Context, id string) (json.RawMessage, error)
}

type KnowledgeClient interface {
	ListKnowledgeBases(ctx context.Context) (json.RawMessage, error)
	SearchKnowledge(ctx context.Context, kb, query string, topK int) (json.RawMessage, error)
}

type QueueClient interface {
	ListQueues(ctx context.Context) (json.RawMessage, error)
	CreateQueue(ctx context.Context, queue string) (json.RawMessage, error)
	ListQueueItems(ctx context.Context, queue string, limit int) (json.RawMessage, error)
	PutQueueItem(ctx context.Context, queue string, item json.RawMessage, ttlSeconds int) (json.RawMessage, error)
	TakeQueueItem(ctx context.Context, queue string) (json.RawMessage, error)
	ClearQueue(ctx context.Context, queue string) (json.RawMessage, error)
}

type Server struct {
	Client          GatewayClient
	Name            string
	Version         string
	ToolPrefix      string
	UserID          string
	SessionPrefix   string
	ExposeDisabled  bool
	AllowedAgentIDs map[string]bool

	writeMu sync.Mutex
}

func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	if s.Client == nil {
		return fmt.Errorf("mcp server: client is required")
	}
	br := bufio.NewReader(r)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line, err := br.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			s.handleLine(ctx, line, w)
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *Server) handleLine(ctx context.Context, line []byte, w io.Writer) {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		s.writeResponse(w, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error: " + err.Error()}})
		return
	}
	if req.ID == nil {
		return
	}
	result, err := s.dispatch(ctx, req)
	if err != nil {
		s.writeResponse(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32000, Message: err.Error()}})
		return
	}
	s.writeResponse(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
}

func (s *Server) dispatch(ctx context.Context, req rpcRequest) (any, error) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    defaultString(s.Name, "soulacy"),
				"version": defaultString(s.Version, "dev"),
			},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return s.listTools(ctx)
	case "tools/call":
		return s.callTool(ctx, req.Params)
	default:
		return nil, fmt.Errorf("method not found: %s", req.Method)
	}
}

func (s *Server) listTools(ctx context.Context) (map[string]any, error) {
	agents, err := s.filteredAgents(ctx)
	if err != nil {
		return nil, err
	}
	tools := []map[string]any{
		{
			"name":        "soulacy_chat",
			"description": "Send a message to a Soulacy agent by agent_id and return the reply.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent_id":   map[string]any{"type": "string", "description": "Soulacy agent ID to call."},
					"text":       map[string]any{"type": "string", "description": "Message to send to the agent."},
					"session_id": map[string]any{"type": "string", "description": "Optional stable session id."},
					"user_id":    map[string]any{"type": "string", "description": "Optional user id for the run."},
				},
				"required": []string{"agent_id", "text"},
			},
		},
	}
	tools = append(tools, s.platformTools()...)
	for _, ag := range agents {
		tools = append(tools, map[string]any{
			"name":        s.toolName(ag.ID),
			"description": agentDescription(ag),
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text":       map[string]any{"type": "string", "description": "Message to send to this Soulacy agent."},
					"session_id": map[string]any{"type": "string", "description": "Optional stable session id."},
					"user_id":    map[string]any{"type": "string", "description": "Optional user id for the run."},
				},
				"required": []string{"text"},
			},
		})
	}
	return map[string]any{"tools": tools}, nil
}

func (s *Server) callTool(ctx context.Context, raw json.RawMessage) (map[string]any, error) {
	var req struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("invalid tools/call params: %w", err)
	}
	if strings.TrimSpace(req.Name) == "" {
		return nil, fmt.Errorf("tool name is required")
	}
	if req.Arguments == nil {
		req.Arguments = map[string]any{}
	}
	if resp, handled, err := s.callPlatformTool(ctx, req.Name, req.Arguments); handled {
		return resp, err
	}
	agentID := stringArg(req.Arguments, "agent_id")
	if req.Name != "soulacy_chat" {
		agent, ok, err := s.agentForTool(ctx, req.Name)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("unknown Soulacy MCP tool %q", req.Name)
		}
		agentID = agent.ID
	}
	if strings.TrimSpace(agentID) == "" {
		return nil, fmt.Errorf("agent_id is required")
	}
	text := stringArg(req.Arguments, "text")
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("text is required")
	}
	userID := stringArg(req.Arguments, "user_id")
	if userID == "" {
		userID = defaultString(s.UserID, "mcp-user")
	}
	sessionID := stringArg(req.Arguments, "session_id")
	if sessionID == "" {
		sessionID = fmt.Sprintf("%s-%s", defaultString(s.SessionPrefix, "mcp"), safeID(agentID))
	}
	resp, err := s.Client.Chat(ctx, ChatRequest{
		AgentID:   agentID,
		SessionID: sessionID,
		UserID:    userID,
		Text:      text,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": resp.Reply}},
		"isError": false,
	}, nil
}

func (s *Server) platformTools() []map[string]any {
	var out []map[string]any
	if _, ok := s.Client.(ScheduleClient); ok {
		out = append(out,
			toolSpec("soulacy_schedule_list", "List Soulacy scheduled agents and their next run metadata.", map[string]any{}, nil),
			toolSpec("soulacy_schedule_status", "Show currently running scheduled/manual agents and next scheduled fire times.", map[string]any{}, nil),
		)
	}
	if _, ok := s.Client.(WorkboardClient); ok {
		out = append(out,
			toolSpec("soulacy_workboard_tasks", "List Soulacy Workboard tasks, optionally filtered by status or agent_id.", map[string]any{
				"status":   map[string]any{"type": "string", "description": "Optional task status filter."},
				"agent_id": map[string]any{"type": "string", "description": "Optional assigned agent ID filter."},
			}, nil),
			toolSpec("soulacy_workboard_run_task", "Start a Soulacy Workboard task by task id.", map[string]any{
				"id": map[string]any{"type": "string", "description": "Workboard task id."},
			}, []string{"id"}),
		)
	}
	if _, ok := s.Client.(KnowledgeClient); ok {
		out = append(out,
			toolSpec("soulacy_knowledge_list", "List Soulacy knowledge bases.", map[string]any{}, nil),
			toolSpec("soulacy_knowledge_search", "Search a Soulacy knowledge base.", map[string]any{
				"kb":    map[string]any{"type": "string", "description": "Knowledge base name."},
				"query": map[string]any{"type": "string", "description": "Search query."},
				"top_k": map[string]any{"type": "integer", "description": "Maximum result count. Defaults to 5."},
			}, []string{"kb", "query"}),
		)
	}
	if _, ok := s.Client.(QueueClient); ok {
		out = append(out,
			toolSpec("soulacy_queue_names", "List Soulacy ephemeral queues and item counts.", map[string]any{}, nil),
			toolSpec("soulacy_queue_create", "Create a Soulacy ephemeral queue.", map[string]any{
				"queue": map[string]any{"type": "string", "description": "Queue name. Defaults to default."},
			}, nil),
			toolSpec("soulacy_queue_list", "List items in a Soulacy ephemeral queue without removing them.", map[string]any{
				"queue": map[string]any{"type": "string", "description": "Queue name. Defaults to default."},
				"limit": map[string]any{"type": "integer", "description": "Maximum items. Defaults to 25."},
			}, nil),
			toolSpec("soulacy_queue_put", "Put a JSON item into a Soulacy ephemeral queue.", map[string]any{
				"queue":       map[string]any{"type": "string", "description": "Queue name. Defaults to default."},
				"item":        map[string]any{"description": "Any JSON value to enqueue."},
				"ttl_seconds": map[string]any{"type": "integer", "description": "Optional TTL in seconds."},
			}, []string{"item"}),
			toolSpec("soulacy_queue_take", "Take and remove the oldest item from a Soulacy ephemeral queue.", map[string]any{
				"queue": map[string]any{"type": "string", "description": "Queue name. Defaults to default."},
			}, nil),
			toolSpec("soulacy_queue_clear", "Clear a Soulacy ephemeral queue.", map[string]any{
				"queue": map[string]any{"type": "string", "description": "Queue name. Defaults to default."},
			}, nil),
		)
	}
	return out
}

func toolSpec(name, description string, properties map[string]any, required []string) map[string]any {
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return map[string]any{
		"name":        name,
		"description": description,
		"inputSchema": schema,
	}
}

func (s *Server) callPlatformTool(ctx context.Context, name string, args map[string]any) (map[string]any, bool, error) {
	switch name {
	case "soulacy_schedule_list":
		c, ok := s.Client.(ScheduleClient)
		if !ok {
			return nil, true, fmt.Errorf("schedule API is unavailable")
		}
		raw, err := c.ListSchedule(ctx)
		return rawToolResult(raw), true, err
	case "soulacy_schedule_status":
		c, ok := s.Client.(ScheduleClient)
		if !ok {
			return nil, true, fmt.Errorf("schedule API is unavailable")
		}
		raw, err := c.ScheduleStatus(ctx)
		return rawToolResult(raw), true, err
	case "soulacy_workboard_tasks":
		c, ok := s.Client.(WorkboardClient)
		if !ok {
			return nil, true, fmt.Errorf("workboard API is unavailable")
		}
		raw, err := c.ListWorkboardTasks(ctx, stringArg(args, "status"), stringArg(args, "agent_id"))
		return rawToolResult(raw), true, err
	case "soulacy_workboard_run_task":
		c, ok := s.Client.(WorkboardClient)
		if !ok {
			return nil, true, fmt.Errorf("workboard API is unavailable")
		}
		id := stringArg(args, "id")
		if id == "" {
			return nil, true, fmt.Errorf("id is required")
		}
		raw, err := c.RunWorkboardTask(ctx, id)
		return rawToolResult(raw), true, err
	case "soulacy_knowledge_list":
		c, ok := s.Client.(KnowledgeClient)
		if !ok {
			return nil, true, fmt.Errorf("knowledge API is unavailable")
		}
		raw, err := c.ListKnowledgeBases(ctx)
		return rawToolResult(raw), true, err
	case "soulacy_knowledge_search":
		c, ok := s.Client.(KnowledgeClient)
		if !ok {
			return nil, true, fmt.Errorf("knowledge API is unavailable")
		}
		kb := stringArg(args, "kb")
		query := stringArg(args, "query")
		if kb == "" {
			return nil, true, fmt.Errorf("kb is required")
		}
		if query == "" {
			return nil, true, fmt.Errorf("query is required")
		}
		raw, err := c.SearchKnowledge(ctx, kb, query, intArg(args, "top_k", 5))
		return rawToolResult(raw), true, err
	case "soulacy_queue_names":
		c, ok := s.Client.(QueueClient)
		if !ok {
			return nil, true, fmt.Errorf("queue API is unavailable")
		}
		raw, err := c.ListQueues(ctx)
		return rawToolResult(raw), true, err
	case "soulacy_queue_create":
		c, ok := s.Client.(QueueClient)
		if !ok {
			return nil, true, fmt.Errorf("queue API is unavailable")
		}
		raw, err := c.CreateQueue(ctx, stringArg(args, "queue"))
		return rawToolResult(raw), true, err
	case "soulacy_queue_list":
		c, ok := s.Client.(QueueClient)
		if !ok {
			return nil, true, fmt.Errorf("queue API is unavailable")
		}
		raw, err := c.ListQueueItems(ctx, stringArg(args, "queue"), intArg(args, "limit", 25))
		return rawToolResult(raw), true, err
	case "soulacy_queue_put":
		c, ok := s.Client.(QueueClient)
		if !ok {
			return nil, true, fmt.Errorf("queue API is unavailable")
		}
		item, err := json.Marshal(args["item"])
		if err != nil {
			return nil, true, fmt.Errorf("item must be JSON-serializable: %w", err)
		}
		raw, err := c.PutQueueItem(ctx, stringArg(args, "queue"), item, intArg(args, "ttl_seconds", 0))
		return rawToolResult(raw), true, err
	case "soulacy_queue_take":
		c, ok := s.Client.(QueueClient)
		if !ok {
			return nil, true, fmt.Errorf("queue API is unavailable")
		}
		raw, err := c.TakeQueueItem(ctx, stringArg(args, "queue"))
		return rawToolResult(raw), true, err
	case "soulacy_queue_clear":
		c, ok := s.Client.(QueueClient)
		if !ok {
			return nil, true, fmt.Errorf("queue API is unavailable")
		}
		raw, err := c.ClearQueue(ctx, stringArg(args, "queue"))
		return rawToolResult(raw), true, err
	default:
		return nil, false, nil
	}
}

func rawToolResult(raw json.RawMessage) map[string]any {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		text = "{}"
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": false,
	}
}

func (s *Server) filteredAgents(ctx context.Context) ([]Agent, error) {
	agents, err := s.Client.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Agent, 0, len(agents))
	for _, ag := range agents {
		if strings.TrimSpace(ag.ID) == "" {
			continue
		}
		if len(s.AllowedAgentIDs) > 0 && !s.AllowedAgentIDs[ag.ID] {
			continue
		}
		if !s.ExposeDisabled && !ag.Enabled {
			continue
		}
		out = append(out, ag)
	}
	return out, nil
}

func (s *Server) agentForTool(ctx context.Context, name string) (Agent, bool, error) {
	agents, err := s.filteredAgents(ctx)
	if err != nil {
		return Agent{}, false, err
	}
	for _, ag := range agents {
		if s.toolName(ag.ID) == name {
			return ag, true, nil
		}
	}
	return Agent{}, false, nil
}

func (s *Server) toolName(agentID string) string {
	prefix := defaultString(s.ToolPrefix, "soulacy_agent_")
	sum := sha1.Sum([]byte(agentID))
	return prefix + safeID(agentID) + "_" + hex.EncodeToString(sum[:])[:8]
}

func (s *Server) writeResponse(w io.Writer, resp rpcResponse) {
	data, _ := json.Marshal(resp)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, _ = w.Write(append(data, '\n'))
}

func agentDescription(ag Agent) string {
	name := strings.TrimSpace(ag.Name)
	if name == "" {
		name = ag.ID
	}
	desc := strings.TrimSpace(ag.Description)
	if desc == "" {
		return "Call the Soulacy agent " + name + "."
	}
	return "Call the Soulacy agent " + name + ". " + desc
}

func stringArg(args map[string]any, key string) string {
	switch v := args[key].(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

func intArg(args map[string]any, key string, fallback int) int {
	switch v := args[key].(type) {
	case int:
		if v > 0 {
			return v
		}
	case int64:
		if v > 0 {
			return int(v)
		}
	case float64:
		if v > 0 {
			return int(v)
		}
	case json.Number:
		if i, err := v.Int64(); err == nil && i > 0 {
			return int(i)
		}
	}
	return fallback
}

func defaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}

func safeID(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			continue
		}
		if r == '_' || r == '-' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	out := strings.Trim(b.String(), "_.-")
	if out == "" {
		out = "agent"
	}
	if len(out) > 80 {
		out = out[:80]
	}
	return out
}
