// engine.go — the agent execution loop.
// The Engine is the heart of Soulacy. It receives a message, assembles the
// full context (system prompt + memory + history + tools), fires the LLM, and
// if the LLM requests tool calls, executes them in a sandboxed Python subprocess
// before re-entering the loop. This continues until the LLM produces a plain
// text response or the max_turns limit is hit.
package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/soulacy/soulacy/pkg/agent"
)

// EventSink receives structured events as they happen during agent execution.
func (e *Engine) buildBuiltins() []BuiltinTool {
	var tools []BuiltinTool

	// web_search — Ollama Web Search API. Available to ALL agents regardless of
	// which LLM provider they use: the search endpoint at ollama.com/api/web_search
	// only needs OLLAMA_API_KEY (env var or llm.providers.ollama.api_key). A
	// claude / openai / gemini agent can call this exactly like an Ollama agent
	// can — the model running inference and the search service are independent.
	// The handler returns a clear error if the key isn't configured, and agents
	// can opt out of all built-ins via `builtins: []` in SOUL.yaml.
	tools = append(tools, BuiltinTool{
		Name:        "web_search",
		Description: "Search the web for current, up-to-date information. Use for facts, news, prices, or anything beyond the model's training data. Returns a JSON object {\"query\":..., \"result_count\":N, \"results\":[{\"title\",\"url\",\"content\"},...]}. Supports Ollama, Tavily, and Serper backends.",
		Gate:        "",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "The search query",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "Maximum number of results (default 5)",
				},
				"timeout_s": map[string]any{
					"type":        "integer",
					"description": "Seconds to wait for the search provider before giving up. Omit to use the server's search.timeout (default 30). Raise it for a slow provider or a large fan-out; capped at 600.",
				},
			},
			"required": []string{"query"},
		},
		Handler: e.webSearch,
	})

	// generate_chart — render an interactive data chart for the user. Always
	// available (Gate ""), so any agent can visualize numbers without the user
	// wiring a tool. The handler builds + validates a Chart.js spec and the
	// engine appends it to the final reply, where the GUI renders it as a live
	// chart. Agents can opt out via `builtins: []` in SOUL.yaml.
	tools = append(tools, e.buildChartBuiltin())

	// channel.send — generic outbound delivery through a registered channel
	// adapter. Studio already emits this for Deliver steps; backing it with the
	// registry makes generated workflows executable instead of merely plausible.
	tools = append(tools, e.buildChannelSendBuiltin())
	tools = append(tools, e.buildChannelStatusBuiltin())

	// queue_* — safe, in-memory handoff for interactive agents and Studio
	// workflows. These tools avoid write_file/system access for ephemeral
	// intermediate state.
	tools = append(tools, e.buildQueueBuiltins()...)

	if e.actionLog != nil {
		tools = append(tools, e.buildSessionSearchBuiltin())
	}

	// Skill built-ins are only added when a skill loader is configured;
	// other built-ins (kb_search, …) are appended below regardless.
	// Gated on skills being configured at all, not on a particular workspace's
	// catalog: the tool list is built once at startup, and which skills a run
	// can actually see is resolved per request inside the handlers.
	if e.skillLoader != nil || e.skillLoaders != nil {
		tools = e.appendSkillBuiltins(tools)
	}

	// kb_search — see buildKBSearchBuiltin below. Gated by `def.Knowledge`.
	if e.knowledge != nil {
		tools = append(tools, e.buildKBSearchBuiltin())
		tools = append(tools, e.buildKBWriteBuiltin())
	}

	// semantic_memory_search — embedding-based long-term memory retrieval.
	// Only added when a VectorStore is configured.
	if e.vectorStore != nil {
		tools = append(tools, e.buildSemanticMemoryBuiltin())
	}

	// System tools are NOT pre-built into e.builtins — they are injected
	// per-request in allToolSchemas, gated by both def.SystemTools and the
	// inbound channel. See allToolSchemas for the enforcement logic.

	return tools
}

// appendSkillBuiltins adds the skills tier-2/tier-3 built-ins (read_skill,
// read_skill_file). Split out so buildBuiltins can layer other capabilities
// (knowledge, …) on top without an early return blocking them.
func (e *Engine) appendSkillBuiltins(tools []BuiltinTool) []BuiltinTool {
	// read_skill — tier 2 activation: load full SKILL.md body for a named skill
	tools = append(tools, BuiltinTool{
		Gate:        "skills",
		Name:        "read_skill",
		Description: "Load the full instructions for an Agent Skill by name. Call this when a task matches a skill description from the available_skills catalog.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"skill_name": map[string]any{
					"type":        "string",
					"description": "The skill name as listed in the available_skills catalog",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Legacy alias for skill_name",
				},
			},
			"required": []string{"skill_name"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			name := argString(args, "skill_name")
			if name == "" {
				name = argString(args, "name")
			}
			if name == "" {
				return "", fmt.Errorf("read_skill: skill_name is required")
			}
			loader := e.skills(ctx)
			if loader == nil {
				return "", fmt.Errorf("read_skill: no skills are available in this workspace")
			}
			s := loader.Get(name)
			if s == nil {
				return "", fmt.Errorf("read_skill: skill %q not found. Available skills: %s", name, e.skillNamesCSV(ctx))
			}
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("<skill_content name=%q>\n", s.Name))
			sb.WriteString(s.Body)
			sb.WriteString(fmt.Sprintf("\n\nSkill directory: %s\n", s.Dir))
			// List bundled resources if any
			resources := s.ResourceFiles()
			if len(resources) > 0 {
				sb.WriteString("\n<skill_resources>\n")
				for _, r := range resources {
					sb.WriteString(fmt.Sprintf("  <file>%s</file>\n", r))
				}
				sb.WriteString("</skill_resources>\n")
			}
			sb.WriteString("</skill_content>")
			return sb.String(), nil
		},
	})

	// read_skill_file — tier 3 resource loading: read a specific file from a skill directory
	tools = append(tools, BuiltinTool{
		Gate:        "skills",
		Name:        "read_skill_file",
		Description: "Read a specific resource file (script, reference, or asset) from a skill directory. Use when skill instructions reference a file like scripts/extract.py or references/guide.md.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"skill_name": map[string]any{
					"type":        "string",
					"description": "The name of the skill that owns the file",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Relative path from the skill directory (e.g. scripts/run.py)",
				},
			},
			"required": []string{"skill_name", "path"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			skillName := argString(args, "skill_name")
			relPath := argString(args, "path")
			if skillName == "" || relPath == "" {
				return "", fmt.Errorf("read_skill_file: skill_name and path are required")
			}

			loader := e.skills(ctx)
			if loader == nil {
				return "", fmt.Errorf("read_skill_file: no skills are available in this workspace")
			}
			s := loader.Get(skillName)
			if s == nil {
				return "", fmt.Errorf("read_skill_file: skill %q not found", skillName)
			}

			// Safety: prevent path traversal outside the skill directory
			absPath := filepath.Join(s.Dir, relPath)
			if !strings.HasPrefix(absPath, s.Dir+string(filepath.Separator)) {
				return "", fmt.Errorf("read_skill_file: path traversal not allowed")
			}

			data, err := os.ReadFile(absPath)
			if err != nil {
				return "", fmt.Errorf("read_skill_file: %w", err)
			}
			return string(data), nil
		},
	})

	return tools
}

// buildKBSearchBuiltin returns the RAG retrieval tool. Gate "knowledge": only
// offered when the agent has declared at least one KB in its SOUL.yaml
// `knowledge:` list. Caller must ensure e.knowledge is non-nil.
func (e *Engine) buildKBSearchBuiltin() BuiltinTool {
	return BuiltinTool{
		Gate:        "knowledge",
		Name:        "kb_search",
		Description: "Search a knowledge base for passages relevant to a query. Returns the top-K most semantically similar chunks with their source document and similarity score. Use this whenever the user's question might be answered by indexed reference material.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kb": map[string]any{
					"type":        "string",
					"description": "The knowledge base name to search (must be listed in this agent's available knowledge bases).",
				},
				"knowledge_base": map[string]any{
					"type":        "string",
					"description": "Compatibility alias for kb.",
				},
				"kb_name": map[string]any{
					"type":        "string",
					"description": "Compatibility alias for kb.",
				},
				"query": map[string]any{
					"type":        "string",
					"description": "The natural-language search query. Be specific — use the user's actual terms when possible.",
				},
				"text": map[string]any{
					"type":        "string",
					"description": "Compatibility alias for query.",
				},
				"q": map[string]any{
					"type":        "string",
					"description": "Compatibility alias for query.",
				},
				"top_k": map[string]any{
					"type":        "integer",
					"description": "How many passages to return (default 10, max 20). Use 10+ for broad questions or when the corpus has multiple related documents that might compete for the top spots.",
				},
			},
			"required": []string{},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			kbName := argStringFirst(args, "kb", "knowledge_base", "kb_name", "collection")
			query := argStringFirst(args, "query", "q", "text", "input")
			topK := argInt(args, "top_k", 10)
			if topK > 20 {
				topK = 20
			}
			if kbName == "" {
				return "", fmt.Errorf("kb_search: kb is required")
			}
			return e.knowledge.Search(ctx, WorkspaceFromContext(ctx), kbName, query, topK)
		},
	}
}

// buildKBWriteBuiltin stores content in an attached knowledge base through the
// RAG service. Unlike write_file, this cannot write arbitrary host paths and
// does not require the system capability.
func (e *Engine) buildKBWriteBuiltin() BuiltinTool {
	return BuiltinTool{
		Gate:        "knowledge",
		Name:        "kb_write",
		Description: "Ingest text content into one of this agent's attached knowledge bases. Use this to store fetched URLs, uploaded documents, notes, summaries, or tagged artifacts for future kb_search retrieval. This is scoped to knowledge storage and does not write arbitrary files.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kb": map[string]any{
					"type":        "string",
					"description": "The knowledge base name to write to. Must be listed in this agent's available knowledge bases.",
				},
				"knowledge_base": map[string]any{
					"type":        "string",
					"description": "Compatibility alias for kb.",
				},
				"kb_name": map[string]any{
					"type":        "string",
					"description": "Compatibility alias for kb.",
				},
				"title": map[string]any{
					"type":        "string",
					"description": "Human-readable document title.",
				},
				"source": map[string]any{
					"type":        "string",
					"description": "Original source URI, filename, or note describing where the content came from.",
				},
				"mime_type": map[string]any{
					"type":        "string",
					"description": "Optional MIME type such as text/plain or text/markdown.",
				},
				"content": map[string]any{
					"description": "The text content to ingest. Structured JSON values are accepted and stored as readable JSON text.",
				},
				"text": map[string]any{
					"description": "Compatibility alias for content.",
				},
				"document": map[string]any{
					"description": "Compatibility alias for content.",
				},
				"artifact": map[string]any{
					"description": "Compatibility alias for content.",
				},
			},
			"required": []string{},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			kbName := argStringFirst(args, "kb", "knowledge_base", "kb_name", "collection")
			content := argContentTextFirst(args, "content", "text", "document", "artifact", "data")
			if kbName == "" {
				return "", fmt.Errorf("kb_write: kb is required")
			}
			if strings.TrimSpace(content) == "" {
				return "", fmt.Errorf("kb_write: content is required")
			}
			doc, err := e.knowledge.IngestText(ctx, WorkspaceFromContext(ctx), kbName, argString(args, "title"), argString(args, "source"), argString(args, "mime_type"), content)
			if err != nil {
				return "", err
			}
			payload, _ := json.Marshal(map[string]any{
				"status":      "stored",
				"kb":          kbName,
				"document_id": doc.ID,
				"title":       doc.Title,
				"source":      doc.Source,
				"chunk_count": doc.ChunkCount,
			})
			return string(payload), nil
		},
	}
}

// buildSemanticMemoryBuiltin returns the semantic_memory_search tool backed by
// sqlite-vec. Only added when e.vectorStore is non-nil.
func (e *Engine) buildSemanticMemoryBuiltin() BuiltinTool {
	return BuiltinTool{
		Name:        "semantic_memory_search",
		Gate:        "",
		Description: "Search long-term semantic memory for past conversations, facts, and context that match a natural-language query. Use when the current conversation lacks background that the agent may have seen in previous sessions.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Natural language query to search past memory for",
				},
				"top_k": map[string]any{
					"type":        "integer",
					"description": "Number of memories to return (default 5, max 20)",
				},
			},
			"required": []string{"query"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			query := argString(args, "query")
			topK := argInt(args, "top_k", 5)
			if topK <= 0 {
				topK = 5
			}
			// semantic_memory_search is a model-facing tool: the query is
			// whatever the model asks for, so the run's own workspace is the
			// only thing standing between it and another tenant's memories.
			results, err := e.vectorStore.Search(ctx, WorkspaceFromContext(ctx), query, topK)
			if err != nil {
				return "", fmt.Errorf("semantic_memory_search: %w", err)
			}
			if len(results) == 0 {
				return "No relevant memories found.", nil
			}
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Semantic memory results for %q:\n\n", query))
			for i, r := range results {
				sb.WriteString(fmt.Sprintf("%d. [%s | %.3f similarity]\n%s\n\n",
					i+1, r.Entry.CreatedAt.Format("2006-01-02 15:04"), 1-r.Distance, r.Entry.Content))
			}
			return sb.String(), nil
		},
	}
}

// privilegedSystemTools is the set of OS-level built-ins that can mutate the
// host or execute arbitrary code (SEC-3 "SYSTEM" partition). These are offered
// ONLY when the server permits (runtime.allow_system_tools) AND the agent
// declares the "system" capability. Everything else returned by
// buildSystemTools is treated as a read-only "SAFE" tool, always available.
//
// SYSTEM (privileged):
//
//	shell_exec      — arbitrary /bin/sh -c
//	run_script      — execute a script file with an interpreter
//	install_library — pip/npm/brew/apt package installs
//	write_file      — create/overwrite/append host files
//	download_file   — write arbitrary bytes from a URL to disk
//
// SAFE (read-only, always on): read_file, list_dir, find_files, fetch_url,
//
//	http_request, sys_info. (http_request can POST, but it cannot
//	touch the local filesystem or spawn processes; it is governed instead by
//	SSRF protection + per-agent confirm_tools, so it stays in the SAFE set.)
//
// isPrivilegedSystemTool reports whether name is in the SEC-3 SYSTEM partition.
func isPrivilegedSystemTool(name string) bool { return toolSecurityClasses[name].Privileged }

// safeSystemTools returns only the read-only OS-level built-ins (the SAFE
// partition). Always available regardless of allow_system_tools / capabilities.
func (e *Engine) safeSystemTools() []BuiltinTool {
	all := e.buildSystemTools()
	out := all[:0:0]
	for _, b := range all {
		if !isPrivilegedSystemTool(b.Name) {
			out = append(out, b)
		}
	}
	return out
}

// IsSystemAgentAllowed checks if the given agent is explicitly allowed by the server
// to access destructive OS-level tools. It checks the global allowSystemAgents list.
func (e *Engine) IsSystemAgentAllowed(def *agent.Definition) bool {
	if def == nil || e.allowSystemAgents == nil {
		return false
	}
	for _, id := range e.allowSystemAgents {
		if id == "*" || id == "all" || id == def.ID {
			return true
		}
	}
	return false
}

// SetManagedInstallExempt controls whether package_install stays available to
// the built-in System agent when arbitrary shell access is disabled.
//
// The exemption exists so a single-user operator has a safe install path
// without enabling shell_exec, and in that setting it is right. In a
// multi-user deployment it is not: package_install runs the installer OUTSIDE
// the privileged-command sandbox (it needs network and must persist), and it
// writes the deployment-wide config and a process-global install directory.
// Those are operator actions, and the System agent is only present in
// multi-user at all under an explicit acknowledgement — which restores a chat
// agent, not the right to rewrite the deployment.
//
// Withdrawing the exemption does not remove the tool. It puts it back behind
// runtime.allow_system_agents, so an operator who genuinely wants it there
// says so a second time, deliberately.
//
// A boolean, not a deployment mode: internal/runtime knows nothing about
// modes and is better for it.
func (e *Engine) SetManagedInstallExempt(exempt bool) { e.managedInstallExempt = exempt }

// systemToolsFor returns the OS-level built-ins this agent may use. The SAFE
// partition is always included; the privileged SYSTEM partition is added only
// when the server permits system tools for this agent AND the agent has the "system"
// capability (SEC-3 double opt-in). When privileged tools are excluded the
// caller still won't dispatch them — gating is centralised here.
func (e *Engine) systemToolsFor(def *agent.Definition) []BuiltinTool {
	all := e.buildSystemTools()
	allowPrivileged := e.IsSystemAgentAllowed(def) && def.HasCapability("system")
	out := make([]BuiltinTool, 0, len(all))
	for _, b := range all {
		// package_install is deliberately narrower than arbitrary system tools:
		// it accepts one HTTPS repository URL, executes a fixed argv (never a
		// model-authored shell command), and always passes through the dynamic
		// approval guardrail. Keep it available to Soulacy's built-in System
		// agent even when arbitrary shell access is disabled server-wide. This
		// gives operators a safe install path without requiring them to enable
		// shell_exec, write_file, or other unrestricted host capabilities.
		managedInstall := e.managedInstallExempt && b.Name == "package_install" && def != nil &&
			def.ID == SystemAgentID && def.HasCapability("system")
		if isPrivilegedSystemTool(b.Name) && !allowPrivileged && !managedInstall {
			continue
		}
		out = append(out, b)
	}
	return out
}

// buildSystemTools returns the FULL set of OS-level built-in tools (both the
// SAFE and SYSTEM partitions). This is the canonical catalog; callers that
// need gating use safeSystemTools / systemToolsFor instead of this directly.
//
// WARNING: the SYSTEM-partition tools (see privilegedSystemTools) execute
// arbitrary shell commands and write files on the host. They are only offered
// to agents that pass the SEC-3 double opt-in.
func (e *Engine) buildSystemTools() []BuiltinTool {
	// ARCH-2: the tool definitions now live in per-domain files
	// (engine_tools_shell.go, engine_tools_files.go, engine_tools_http.go,
	// engine_tools_misc.go). This concatenates them into the canonical
	// full catalog. The SEC-3 SAFE/SYSTEM partition is applied by callers
	// (safeSystemTools / systemToolsFor) via privilegedSystemTools, not here.
	out := make([]BuiltinTool, 0, 12)
	out = append(out, e.buildShellTools()...)
	out = append(out, e.buildFileTools()...)
	out = append(out, e.buildHTTPTools()...)
	out = append(out, e.buildMiscTools()...)
	return out
}

// webSearch routes the query to the configured search provider.
// searchResultItem is one normalized web_search hit.
type searchResultItem struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

// marshalSearchResults renders web_search output as a JSON object string —
// {"query":..., "result_count":N, "results":[{title,url,content},...]} — rather
// than prose. A structured shape is the contract downstream Studio flow/python
// nodes rely on (they do inputs["var"]["results"]); LLM agents read the JSON
// equally well. An empty result set still returns a valid object with results:[]
// so callers can branch on result_count without special-casing a "no results"
// sentence. Content is truncated to keep payloads bounded.
func marshalSearchResults(query string, items []searchResultItem) (string, error) {
	if items == nil {
		items = []searchResultItem{}
	}
	for i := range items {
		items[i].Content = strings.TrimSpace(items[i].Content)
		if len(items[i].Content) > 600 {
			items[i].Content = items[i].Content[:600] + "…"
		}
	}
	b, err := json.Marshal(map[string]any{
		"query":        query,
		"result_count": len(items),
		"results":      items,
	})
	if err != nil {
		return "", fmt.Errorf("web_search: encode results: %w", err)
	}
	return string(b), nil
}

func (e *Engine) webSearch(ctx context.Context, args map[string]any) (string, error) {
	provider, _, _ := e.getSearchConfigFor(ctx)
	provider = strings.ToLower(provider)
	if provider == "" {
		provider = "ollama"
	}
	switch provider {
	case "tavily":
		return e.tavilyWebSearch(ctx, args)
	case "serper":
		return e.serperWebSearch(ctx, args)
	default:
		return e.ollamaWebSearch(ctx, args)
	}
}

// tavilyWebSearch implements the web_search tool via Tavily API.
func (e *Engine) tavilyWebSearch(ctx context.Context, args map[string]any) (string, error) {
	query := strings.TrimSpace(argString(args, "query"))
	if query == "" {
		return "", fmt.Errorf("web_search: query is required")
	}
	maxResults := argInt(args, "max_results", 5)
	if maxResults <= 0 {
		maxResults = 5
	}

	_, key, _ := e.getSearchConfigFor(ctx)
	if key == "" {
		key = os.Getenv("TAVILY_API_KEY")
	}
	if key == "" {
		return "", fmt.Errorf("web_search: no Tavily API key. Set TAVILY_API_KEY environment variable or search.api_key in config.yaml")
	}

	payload, err := json.Marshal(map[string]any{
		"api_key":     key,
		"query":       query,
		"max_results": maxResults,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/search", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: e.searchTimeoutFor(ctx, args)}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("web_search (tavily): request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("web_search (tavily): API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("web_search (tavily): decode response: %w", err)
	}
	items := make([]searchResultItem, 0, len(out.Results))
	for _, r := range out.Results {
		items = append(items, searchResultItem{Title: r.Title, URL: r.URL, Content: r.Content})
	}
	return marshalSearchResults(query, items)
}

// serperWebSearch implements the web_search tool via Serper API.
func (e *Engine) serperWebSearch(ctx context.Context, args map[string]any) (string, error) {
	query := strings.TrimSpace(argString(args, "query"))
	if query == "" {
		return "", fmt.Errorf("web_search: query is required")
	}
	maxResults := argInt(args, "max_results", 5)
	if maxResults <= 0 {
		maxResults = 5
	}

	_, key, _ := e.getSearchConfigFor(ctx)
	if key == "" {
		key = os.Getenv("SERPER_API_KEY")
	}
	if key == "" {
		return "", fmt.Errorf("web_search: no Serper API key. Set SERPER_API_KEY environment variable or search.api_key in config.yaml")
	}

	payload, err := json.Marshal(map[string]any{
		"q":   query,
		"num": maxResults,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://google.serper.dev/search", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("X-API-KEY", key)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: e.searchTimeoutFor(ctx, args)}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("web_search (serper): request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("web_search (serper): API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out struct {
		Organic []struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
		} `json:"organic"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("web_search (serper): decode response: %w", err)
	}
	items := make([]searchResultItem, 0, len(out.Organic))
	for _, r := range out.Organic {
		items = append(items, searchResultItem{Title: r.Title, URL: r.Link, Content: r.Snippet})
	}
	return marshalSearchResults(query, items)
}

// ollamaWebSearch implements the built-in web_search tool via the Ollama Web
// Search API (https://ollama.com/api/web_search). Requires an Ollama API key.
func (e *Engine) ollamaWebSearch(ctx context.Context, args map[string]any) (string, error) {
	query := strings.TrimSpace(argString(args, "query"))
	if query == "" {
		return "", fmt.Errorf("web_search: query is required")
	}
	maxResults := argInt(args, "max_results", 5)
	if maxResults <= 0 {
		maxResults = 5
	}

	key := os.Getenv("OLLAMA_API_KEY")
	if key == "" {
		key = e.getOllamaAPIKey()
	}
	if key == "" {
		_, searchKey, _ := e.getSearchConfigFor(ctx)
		key = searchKey
	}
	if key == "" {
		return "", fmt.Errorf("web_search: no Ollama API key. Create one at https://ollama.com/settings/keys, then set the OLLAMA_API_KEY environment variable or llm.providers.ollama.api_key in config.yaml")
	}

	payload, _ := json.Marshal(map[string]any{"query": query, "max_results": maxResults})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://ollama.com/api/web_search", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: e.searchTimeoutFor(ctx, args)}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("web_search: request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("web_search: Ollama API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("web_search: decode response: %w", err)
	}
	items := make([]searchResultItem, 0, len(out.Results))
	for _, r := range out.Results {
		items = append(items, searchResultItem{Title: r.Title, URL: r.URL, Content: r.Content})
	}
	return marshalSearchResults(query, items)
}

// Handle processes an inbound message and returns the agent's reply.
// It is safe to call concurrently from multiple goroutines.
//
// Named returns are used here so the failure-notification defer can see
// the final err value without every internal return-path having to
// manually shadow a local. Successful runs leave err == nil → defer's
// notify branch is a no-op.
