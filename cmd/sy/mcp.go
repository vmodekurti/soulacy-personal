package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/mcpserver"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func buildMCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Manage MCP servers",
	}

	var name, transport, command, pipSpec string
	var args []string

	addCmd := &cobra.Command{
		Use:   "add",
		Short: "Install (optional) and register an MCP server in config.yaml",
		Long: `Register an MCP server in the live config.yaml under mcp.servers.

With --pip, the package is first installed into an isolated venv UNDER THE
WORKSPACE (mcp-servers/<name>/venv) so it survives container restarts, and a
bare --command is resolved to that venv's bin/. The config edit preserves
comments and writes 0600 (the file holds secrets).

Examples:
  sy mcp add --name weather --command weather-mcp --transport stdio
  sy mcp add --name notebooklm --pip notebooklm-mcp-cli --command notebooklm-mcp-cli`,
		RunE: func(_ *cobra.Command, _ []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			ws, err := config.ResolveWorkspace()
			if err != nil {
				return fmt.Errorf("resolve workspace: %w", err)
			}

			// Optional: install the pip package into a persistent, isolated venv
			// under the workspace volume so it survives restarts (build tools and
			// python ship in the runtime image).
			if pipSpec != "" {
				venvDir := filepath.Join(ws.Root, "mcp-servers", name, "venv")
				if err := os.MkdirAll(filepath.Dir(venvDir), 0o755); err != nil {
					return fmt.Errorf("create mcp dir: %w", err)
				}
				fmt.Printf("→ Creating venv at %s\n", venvDir)
				if out, verr := exec.Command("python3", "-m", "venv", venvDir).CombinedOutput(); verr != nil {
					return fmt.Errorf("create venv: %v\n%s", verr, out)
				}
				pip := filepath.Join(venvDir, "bin", "pip")
				fmt.Printf("→ Installing %q into the venv\n", pipSpec)
				ic := exec.Command(pip, "install", pipSpec)
				ic.Stdout, ic.Stderr = os.Stdout, os.Stderr
				if rerr := ic.Run(); rerr != nil {
					return fmt.Errorf("pip install: %w", rerr)
				}
				// A bare command name resolves to the venv's bin/, so the gateway
				// spawns the persistent install rather than a PATH lookup.
				if command != "" && !strings.ContainsRune(command, '/') {
					command = filepath.Join(venvDir, "bin", command)
				}
			}

			if command == "" {
				return fmt.Errorf("--command is required")
			}

			// Register in the live config, preserving comments (yaml.Node — the
			// same path the onboarding wizard uses, so this never corrupts the file).
			configPath := ws.ConfigFile
			doc, root, err := loadConfigDoc(configPath)
			if err != nil {
				return fmt.Errorf("read config: %w", err)
			}
			servers := ensureMapping(ensureMapping(root, "mcp"), "servers")
			srv := ensureMapping(servers, name)
			setScalar(srv, "transport", transport, 0)
			setScalar(srv, "command", command, yaml.DoubleQuotedStyle)
			if len(args) > 0 {
				setSequence(srv, "args", args)
			}
			if err := saveConfigDoc(configPath, doc); err != nil {
				return fmt.Errorf("write config: %w", err)
			}

			fmt.Printf("✓ Registered MCP server %q in %s\n", name, configPath)
			fmt.Println("  Restart the gateway (or it hot-reloads on config change) to connect.")
			return nil
		},
	}

	addCmd.Flags().StringVar(&name, "name", "", "Name/id of the MCP server")
	addCmd.Flags().StringVar(&transport, "transport", "stdio", "Transport type (stdio or http)")
	addCmd.Flags().StringVar(&command, "command", "", "Command to run the server (a bare name resolves to the venv bin when --pip is used)")
	addCmd.Flags().StringSliceVar(&args, "args", nil, "Arguments for the command")
	addCmd.Flags().StringVar(&pipSpec, "pip", "", "Optional pip package/spec to install into a persistent venv before registering")

	cmd.AddCommand(addCmd)
	cmd.AddCommand(buildMCPServeCmd())
	return cmd
}

func buildMCPServeCmd() *cobra.Command {
	var exposeDisabled bool
	var agentIDs []string
	var toolPrefix, userID, sessionPrefix string

	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Expose Soulacy agents as an MCP stdio server",
		Long: `Expose the running Soulacy gateway as a Model Context Protocol server.

Each enabled Soulacy agent is listed as a tool, plus a generic soulacy_chat
tool that accepts agent_id explicitly. Configure this command in Claude,
Codex, or any MCP client using stdio transport.

Example MCP command:
  sy --gateway http://localhost:18789 mcp serve`,
		RunE: func(cmd *cobra.Command, args []string) error {
			allowed := map[string]bool{}
			for _, id := range agentIDs {
				id = strings.TrimSpace(id)
				if id != "" {
					allowed[id] = true
				}
			}
			if len(allowed) == 0 {
				allowed = nil
			}
			srv := &mcpserver.Server{
				Client:          &mcpGatewayClient{},
				Name:            "soulacy",
				Version:         "dev",
				ToolPrefix:      toolPrefix,
				UserID:          userID,
				SessionPrefix:   sessionPrefix,
				ExposeDisabled:  exposeDisabled,
				AllowedAgentIDs: allowed,
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			return srv.Serve(ctx, os.Stdin, os.Stdout)
		},
	}
	serveCmd.Flags().BoolVar(&exposeDisabled, "include-disabled", false, "Expose disabled Soulacy agents as MCP tools")
	serveCmd.Flags().StringSliceVar(&agentIDs, "agent", nil, "Restrict exposed tools to one or more agent IDs")
	serveCmd.Flags().StringVar(&toolPrefix, "tool-prefix", "soulacy_agent_", "Prefix for generated per-agent MCP tool names")
	serveCmd.Flags().StringVar(&userID, "user-id", "mcp-user", "User id sent to Soulacy chat runs")
	serveCmd.Flags().StringVar(&sessionPrefix, "session-prefix", "mcp", "Session id prefix used when the MCP caller does not provide session_id")
	return serveCmd
}

type mcpGatewayClient struct{}

func (m *mcpGatewayClient) ListAgents(ctx context.Context) ([]mcpserver.Agent, error) {
	data, err := apiCallWithTimeout(http.MethodGet, "/agents", nil, 10*time.Second)
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
	for _, ag := range resp.Agents {
		agents = append(agents, mcpserver.Agent{
			ID:          ag.ID,
			Name:        ag.Name,
			Description: ag.Description,
			Enabled:     ag.Enabled,
		})
	}
	return agents, nil
}

func (m *mcpGatewayClient) Chat(ctx context.Context, req mcpserver.ChatRequest) (mcpserver.ChatResponse, error) {
	body, err := json.Marshal(map[string]any{
		"agent_id":   req.AgentID,
		"session_id": req.SessionID,
		"user_id":    req.UserID,
		"username":   req.UserID,
		"text":       req.Text,
	})
	if err != nil {
		return mcpserver.ChatResponse{}, err
	}
	data, err := apiCallWithTimeout(http.MethodPost, "/chat", body, 10*time.Minute)
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

func (m *mcpGatewayClient) ListSchedule(ctx context.Context) (json.RawMessage, error) {
	return apiCallWithTimeout(http.MethodGet, "/schedule", nil, 15*time.Second)
}

func (m *mcpGatewayClient) ScheduleStatus(ctx context.Context) (json.RawMessage, error) {
	return apiCallWithTimeout(http.MethodGet, "/schedule/status", nil, 15*time.Second)
}

func (m *mcpGatewayClient) ListWorkboardTasks(ctx context.Context, status, agentID string) (json.RawMessage, error) {
	q := url.Values{}
	if status = strings.TrimSpace(status); status != "" {
		q.Set("status", status)
	}
	if agentID = strings.TrimSpace(agentID); agentID != "" {
		q.Set("agent_id", agentID)
	}
	path := "/workboard/tasks"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return apiCallWithTimeout(http.MethodGet, path, nil, 20*time.Second)
}

func (m *mcpGatewayClient) RunWorkboardTask(ctx context.Context, id string) (json.RawMessage, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("task id is required")
	}
	return apiCallWithTimeout(http.MethodPost, "/workboard/tasks/"+url.PathEscape(id)+"/run", nil, 30*time.Second)
}

func (m *mcpGatewayClient) ListKnowledgeBases(ctx context.Context) (json.RawMessage, error) {
	return apiCallWithTimeout(http.MethodGet, "/knowledge", nil, 20*time.Second)
}

func (m *mcpGatewayClient) SearchKnowledge(ctx context.Context, kb, query string, topK int) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]any{"query": query, "top_k": topK})
	if err != nil {
		return nil, err
	}
	return apiCallWithTimeout(http.MethodPost, "/knowledge/"+url.PathEscape(kb)+"/search", body, 60*time.Second)
}

func (m *mcpGatewayClient) ListQueues(ctx context.Context) (json.RawMessage, error) {
	return apiCallWithTimeout(http.MethodGet, "/queues", nil, 15*time.Second)
}

func (m *mcpGatewayClient) CreateQueue(ctx context.Context, queue string) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]any{"queue": queue})
	if err != nil {
		return nil, err
	}
	return apiCallWithTimeout(http.MethodPost, "/queues", body, 15*time.Second)
}

func (m *mcpGatewayClient) ListQueueItems(ctx context.Context, queue string, limit int) (json.RawMessage, error) {
	q := url.Values{}
	if queue = strings.TrimSpace(queue); queue != "" {
		q.Set("queue", queue)
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	path := "/queues/items"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return apiCallWithTimeout(http.MethodGet, path, nil, 15*time.Second)
}

func (m *mcpGatewayClient) PutQueueItem(ctx context.Context, queue string, item json.RawMessage, ttlSeconds int) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]any{
		"queue":       queue,
		"item":        json.RawMessage(item),
		"ttl_seconds": ttlSeconds,
	})
	if err != nil {
		return nil, err
	}
	return apiCallWithTimeout(http.MethodPost, "/queues/items", body, 15*time.Second)
}

func (m *mcpGatewayClient) TakeQueueItem(ctx context.Context, queue string) (json.RawMessage, error) {
	path := "/queues/take"
	if queue = strings.TrimSpace(queue); queue != "" {
		path += "?queue=" + url.QueryEscape(queue)
	}
	return apiCallWithTimeout(http.MethodPost, path, nil, 15*time.Second)
}

func (m *mcpGatewayClient) ClearQueue(ctx context.Context, queue string) (json.RawMessage, error) {
	path := "/queues/items"
	if queue = strings.TrimSpace(queue); queue != "" {
		path += "?queue=" + url.QueryEscape(queue)
	}
	return apiCallWithTimeout(http.MethodDelete, path, nil, 15*time.Second)
}

// setSequence sets m[key] to a flow-style YAML sequence of strings, creating or
// replacing the entry. Reuses yamlMapValue from onboard.go (same package).
func setSequence(m *yaml.Node, key string, values []string) {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, v := range values {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v})
	}
	if existing := yamlMapValue(m, key); existing != nil {
		existing.Kind = seq.Kind
		existing.Tag = seq.Tag
		existing.Style = seq.Style
		existing.Value = ""
		existing.Content = seq.Content
		return
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		seq,
	)
}
