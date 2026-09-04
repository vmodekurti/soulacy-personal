// tool_registry.go — ToolExecutor implementation for the reasoning loop.
//
// Security constraints (from spec section 6):
//   - No tool handler may execute shell commands or spawn subprocesses.
//   - Input keys are validated against a per-tool allowlist before use.
//   - All execution respects ctx.Done() (per-step StepTimeout).
//   - Observation content is capped at 8192 bytes by Loop.boundObservation().
//   - memory_write rejects records where Content > 32KB (enforced in CompositeStore).
//
// The Python subprocess model from the existing Soulacy engine is NOT used here.
// Existing Python tools remain on their own path and do not route through this registry.
package reasoning

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/soulacy/soulacy/internal/agentmemory"
)

// HandlerFunc is the signature for individual tool implementations.
// It must respect ctx.Done() and must not spawn subprocesses.
// Input keys have already been validated against the tool's allowlist.
type HandlerFunc func(ctx context.Context, input map[string]string) (string, error)

// ToolSpec declares a tool's name and its input key allowlist.
// Any key not in AllowedKeys is stripped from the input before the handler runs.
type ToolSpec struct {
	Name        string
	AllowedKeys []string
	Handler     HandlerFunc
}

// Registry is a ToolExecutor that dispatches to registered handlers.
// Register tools before passing the Registry to Loop.New().
type Registry struct {
	mu    sync.RWMutex
	tools map[string]ToolSpec
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]ToolSpec)}
}

// Register adds a tool to the registry. Panics on duplicate names.
func (r *Registry) Register(spec ToolSpec) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[spec.Name]; exists {
		panic(fmt.Sprintf("reasoning: Registry.Register: duplicate tool %q", spec.Name))
	}
	r.tools[spec.Name] = spec
}

// Execute dispatches a ToolCall to the registered handler.
// Unknown tools return an Observation with an error — they do not panic.
// Input keys not in the tool's allowlist are stripped before the handler runs.
func (r *Registry) Execute(ctx context.Context, call ToolCall) Observation {
	r.mu.RLock()
	spec, ok := r.tools[call.Tool]
	r.mu.RUnlock()

	if !ok {
		return Observation{
			Error:   fmt.Errorf("unknown tool %q", call.Tool),
			Content: fmt.Sprintf("tool error: unknown tool %q", call.Tool),
		}
	}

	// Validate and filter input keys against the allowlist.
	safeInput := make(map[string]string, len(call.Input))
	if len(spec.AllowedKeys) > 0 {
		allowed := make(map[string]bool, len(spec.AllowedKeys))
		for _, k := range spec.AllowedKeys {
			allowed[k] = true
		}
		for k, v := range call.Input {
			if allowed[k] {
				safeInput[k] = v
			}
		}
	} else {
		safeInput = call.Input
	}

	// Respect the step context deadline.
	select {
	case <-ctx.Done():
		return Observation{
			Error:   ctx.Err(),
			Content: fmt.Sprintf("tool error: step timeout (%s)", ctx.Err()),
		}
	default:
	}

	out, err := spec.Handler(ctx, safeInput)
	if err != nil {
		return Observation{Error: err, Content: fmt.Sprintf("tool error: %s", err)}
	}
	return Observation{Content: out, Source: call.Tool}
}

// ─── Built-in tool handlers ───────────────────────────────────────────────────

// NewMemoryToolHandlers returns ToolSpec entries for memory_read and memory_write,
// wired to the given CompositeStore (RL-06, RL-07).
func NewMemoryToolHandlers(store *agentmemory.CompositeStore) []ToolSpec {
	return []ToolSpec{
		{
			Name:        "memory_read",
			AllowedKeys: []string{"agent_id", "query", "max_episodic", "max_semantic"},
			Handler:     memoryReadHandler(store),
		},
		{
			Name:        "memory_write",
			AllowedKeys: []string{"agent_id", "content", "tags"},
			Handler:     memoryWriteHandler(store),
		},
	}
}

func memoryReadHandler(store *agentmemory.CompositeStore) HandlerFunc {
	return func(ctx context.Context, input map[string]string) (string, error) {
		agentID := input["agent_id"]
		if agentID == "" {
			return "", fmt.Errorf("memory_read: agent_id is required")
		}

		maxEp := parseIntInput(input["max_episodic"], 5)
		maxSem := parseIntInput(input["max_semantic"], 8)

		result, err := store.Retrieve(agentmemory.RetrieveQuery{
			AgentID:     agentID,
			TaskInput:   input["query"],
			MaxEpisodic: maxEp,
			MaxSemantic: maxSem,
		})
		if err != nil {
			return "", fmt.Errorf("memory_read: %w", err)
		}

		block := agentmemory.BuildContextBlock(result)
		if block == "" {
			return "(no memory records found)", nil
		}
		return block, nil
	}
}

func memoryWriteHandler(store *agentmemory.CompositeStore) HandlerFunc {
	return func(ctx context.Context, input map[string]string) (string, error) {
		agentID := input["agent_id"]
		content := input["content"]
		if agentID == "" {
			return "", fmt.Errorf("memory_write: agent_id is required")
		}
		if content == "" {
			return "", fmt.Errorf("memory_write: content is required")
		}

		var tags []string
		if t := input["tags"]; t != "" {
			for _, tag := range strings.Split(t, ",") {
				tag = strings.TrimSpace(tag)
				if tag != "" {
					tags = append(tags, tag)
				}
			}
		}

		err := store.Write(agentmemory.Record{
			AgentID: agentID,
			Type:    agentmemory.MemoryTypeEpisodic,
			Content: content,
			Tags:    tags,
		})
		if err != nil {
			return "", fmt.Errorf("memory_write: %w", err)
		}
		return "memory written", nil
	}
}

// WebSearchSpec returns a ToolSpec for the web_search tool backed by the
// configured Web Search API (Ollama, Tavily, or Serper).
func WebSearchSpec(provider, apiKey string) ToolSpec {
	return ToolSpec{
		Name:        "web_search",
		AllowedKeys: []string{"query", "num_results"},
		Handler: func(ctx context.Context, input map[string]string) (string, error) {
			query := strings.TrimSpace(input["query"])
			if query == "" {
				return "", fmt.Errorf("web_search: query is required")
			}
			maxResults := parseIntInput(input["num_results"], 5)
			p := strings.ToLower(provider)
			if p == "" {
				p = "ollama"
			}
			switch p {
			case "tavily":
				return tavilyWebSearch(ctx, apiKey, query, maxResults)
			case "serper":
				return serperWebSearch(ctx, apiKey, query, maxResults)
			default:
				return ollamaWebSearch(ctx, apiKey, query, maxResults)
			}
		},
	}
}

// tavilyWebSearch calls the Tavily API and returns formatted results.
func tavilyWebSearch(ctx context.Context, apiKey, query string, maxResults int) (string, error) {
	if apiKey == "" {
		apiKey = os.Getenv("TAVILY_API_KEY")
	}
	if apiKey == "" {
		return "web_search unavailable: no TAVILY_API_KEY configured. " +
			"Get a key at https://tavily.com and set TAVILY_API_KEY " +
			"or search.api_key in config.yaml.", nil
	}

	payload, _ := json.Marshal(map[string]any{
		"api_key":     apiKey,
		"query":       query,
		"max_results": maxResults,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.tavily.com/search", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("web_search (tavily): %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("web_search (tavily): request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("web_search (tavily): API returned %d — %s", resp.StatusCode, strings.TrimSpace(string(body))), nil
	}

	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil || len(out.Results) == 0 {
		return fmt.Sprintf("No web results found for %q.", query), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Web search results for %q:\n", query))
	for i, r := range out.Results {
		content := strings.TrimSpace(r.Content)
		if len(content) > 600 {
			content = content[:600] + "…"
		}
		sb.WriteString(fmt.Sprintf("\n%d. %s\n   %s\n   %s\n", i+1, r.Title, r.URL, content))
	}
	return sb.String(), nil
}

// serperWebSearch calls the Serper API and returns formatted results.
func serperWebSearch(ctx context.Context, apiKey, query string, maxResults int) (string, error) {
	if apiKey == "" {
		apiKey = os.Getenv("SERPER_API_KEY")
	}
	if apiKey == "" {
		return "web_search unavailable: no SERPER_API_KEY configured. " +
			"Get a key at https://serper.dev and set SERPER_API_KEY " +
			"or search.api_key in config.yaml.", nil
	}

	payload, _ := json.Marshal(map[string]any{
		"q":   query,
		"num": maxResults,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://google.serper.dev/search", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("web_search (serper): %w", err)
	}
	req.Header.Set("X-API-KEY", apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("web_search (serper): request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("web_search (serper): API returned %d — %s", resp.StatusCode, strings.TrimSpace(string(body))), nil
	}

	var out struct {
		Organic []struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
		} `json:"organic"`
	}
	if err := json.Unmarshal(body, &out); err != nil || len(out.Organic) == 0 {
		return fmt.Sprintf("No web results found for %q.", query), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Web search results for %q:\n", query))
	for i, r := range out.Organic {
		snippet := strings.TrimSpace(r.Snippet)
		if len(snippet) > 600 {
			snippet = snippet[:600] + "…"
		}
		sb.WriteString(fmt.Sprintf("\n%d. %s\n   %s\n   %s\n", i+1, r.Title, r.Link, snippet))
	}
	return sb.String(), nil
}

// ollamaWebSearch calls the Ollama Web Search API and returns formatted results.
// This mirrors the engine's built-in web_search handler so both code paths
// use identical search quality and formatting.
func ollamaWebSearch(ctx context.Context, apiKey, query string, maxResults int) (string, error) {
	if apiKey == "" {
		// Check the standard env var as a last resort.
		apiKey = os.Getenv("OLLAMA_API_KEY")
	}
	if apiKey == "" {
		return "web_search unavailable: no OLLAMA_API_KEY configured. " +
			"Get a key at https://ollama.com/settings/keys and set OLLAMA_API_KEY " +
			"or llm.providers.ollama.api_key in config.yaml.", nil
	}

	payload, _ := json.Marshal(map[string]any{
		"query":       query,
		"max_results": maxResults,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://ollama.com/api/web_search", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("web_search: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("web_search: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("web_search: API returned %d — %s", resp.StatusCode, strings.TrimSpace(string(body))), nil
	}

	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil || len(out.Results) == 0 {
		return fmt.Sprintf("No web results found for %q.", query), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Web search results for %q:\n", query))
	for i, r := range out.Results {
		content := strings.TrimSpace(r.Content)
		if len(content) > 600 {
			content = content[:600] + "…"
		}
		sb.WriteString(fmt.Sprintf("\n%d. %s\n   %s\n   %s\n", i+1, r.Title, r.URL, content))
	}
	return sb.String(), nil
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func parseIntInput(s string, def int) int {
	if s == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n <= 0 {
		return def
	}
	return n
}
