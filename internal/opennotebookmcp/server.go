// Package opennotebookmcp exposes a local Open Notebook REST API as a small,
// read/write MCP server. It deliberately accepts loopback URLs only: the
// gateway and Open Notebook are expected to run on the same machine.
package opennotebookmcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/soulacy/soulacy/internal/netguard"
	"github.com/soulacy/soulacy/internal/opennotebook"
)

const (
	DefaultBaseURL       = opennotebook.DefaultBaseURL
	defaultProtocol      = "2025-11-25"
	maxRequestBytes      = 1 << 20
	maxResponseBytes     = 8 << 20
	maxTextContentBytes  = 2 << 20
	defaultRequestTimout = 2 * time.Minute
)

var supportedProtocols = map[string]bool{
	"2024-11-05": true, "2025-03-26": true, "2025-06-18": true, "2025-11-25": true,
}

// Server is an MCP stdio adapter for Open Notebook.
type Server struct {
	baseURL *url.URL
	token   string
	client  *http.Client
	version string
	writeMu sync.Mutex
}

// New validates the local boundary and constructs a server.
func New(baseURL, token, version string, client *http.Client) (*Server, error) {
	u, err := opennotebook.ValidateBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: defaultRequestTimout}
	}
	if strings.TrimSpace(version) == "" {
		version = "dev"
	}
	return &Server{baseURL: u, token: strings.TrimSpace(token), client: client, version: version}, nil
}

// ValidateBaseURL remains exported here for adapter callers while the shared
// connection policy lives outside the MCP server implementation.
var ValidateBaseURL = opennotebook.ValidateBaseURL

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

// Serve runs the newline-delimited MCP stdio transport.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxRequestBytes)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if data, ok := s.Handle(ctx, scanner.Bytes()); ok {
			s.writeMu.Lock()
			_, err := w.Write(append(data, '\n'))
			s.writeMu.Unlock()
			if err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if strings.Contains(err.Error(), "token too long") {
			return fmt.Errorf("MCP request exceeds %d bytes", maxRequestBytes)
		}
		return err
	}
	return nil
}

// Handle processes one MCP JSON-RPC message.
func (s *Server) Handle(ctx context.Context, message []byte) ([]byte, bool) {
	var req rpcRequest
	if err := json.Unmarshal(message, &req); err != nil {
		return marshal(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}}), true
	}
	if req.ID == nil || strings.TrimSpace(req.Method) == "" {
		return nil, false
	}
	result, err := s.dispatch(ctx, req)
	if err != nil {
		return marshal(rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: err.Error()}}), true
	}
	return marshal(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result}), true
}

func (s *Server) dispatch(ctx context.Context, req rpcRequest) (any, error) {
	switch req.Method {
	case "initialize":
		version := defaultProtocol
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(req.Params, &p) == nil && supportedProtocols[p.ProtocolVersion] {
			version = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "soulacy-open-notebook", "version": s.version},
			"instructions":    "Use these tools to organize local Open Notebook research, search it, ask questions, and generate podcasts. Call open_notebook_list_models and the profile-list tools before ask or podcast generation when IDs are unknown.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolSpecs()}, nil
	case "tools/call":
		return s.callTool(ctx, req.Params), nil
	default:
		return nil, fmt.Errorf("method not found: %s", req.Method)
	}
}

func (s *Server) callTool(ctx context.Context, raw json.RawMessage) map[string]any {
	var call struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &call); err != nil {
		return toolError("invalid tools/call parameters")
	}
	if call.Arguments == nil {
		call.Arguments = map[string]any{}
	}
	result, err := s.execute(ctx, strings.TrimSpace(call.Name), call.Arguments)
	if err != nil {
		return toolError(err.Error())
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, result, "", "  "); err != nil {
		pretty.Write(result)
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": pretty.String()}}, "isError": false}
}

func toolError(message string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": message}}, "isError": true}
}

func (s *Server) execute(ctx context.Context, name string, a map[string]any) (json.RawMessage, error) {
	q := url.Values{}
	var method, endpoint string
	var body map[string]any
	switch name {
	case "open_notebook_health":
		method, endpoint = http.MethodGet, "/health"
	case "open_notebook_list_notebooks":
		method, endpoint = http.MethodGet, "/api/notebooks"
	case "open_notebook_get_notebook":
		method, endpoint = http.MethodGet, "/api/notebooks/"+pathEscape(requiredString(a, "notebook_id"))
	case "open_notebook_create_notebook":
		method, endpoint = http.MethodPost, "/api/notebooks"
		body = pick(a, "name", "description")
		if err := require(body, "name"); err != nil {
			return nil, err
		}
	case "open_notebook_list_sources":
		method, endpoint = http.MethodGet, "/api/sources"
		queryString(q, a, "notebook_id")
		queryInt(q, a, "limit", 1, 100)
	case "open_notebook_get_source":
		method, endpoint = http.MethodGet, "/api/sources/"+pathEscape(requiredString(a, "source_id"))
	case "open_notebook_add_url_source":
		method, endpoint = http.MethodPost, "/api/sources/json"
		body = pick(a, "url", "title", "notebooks", "transformations", "embed", "async_processing")
		body["type"] = "link"
		if err := require(body, "url"); err != nil {
			return nil, err
		}
		if err := netguard.CheckPublic(stringValue(body["url"])); err != nil {
			return nil, fmt.Errorf("source URL must resolve to a public HTTP(S) address: %w", err)
		}
	case "open_notebook_add_text_source":
		method, endpoint = http.MethodPost, "/api/sources/json"
		body = pick(a, "content", "title", "notebooks", "transformations", "embed", "async_processing")
		body["type"] = "text"
		if err := require(body, "content"); err != nil {
			return nil, err
		}
		if len(stringValue(body["content"])) > maxTextContentBytes {
			return nil, fmt.Errorf("content exceeds %d bytes", maxTextContentBytes)
		}
	case "open_notebook_get_source_status":
		method, endpoint = http.MethodGet, "/api/sources/"+pathEscape(requiredString(a, "source_id"))+"/status"
	case "open_notebook_search":
		method, endpoint = http.MethodPost, "/api/search"
		body = pick(a, "query", "type", "limit", "search_sources", "search_notes", "minimum_score")
		if err := require(body, "query"); err != nil {
			return nil, err
		}
	case "open_notebook_list_models":
		method, endpoint = http.MethodGet, "/api/models"
		queryString(q, a, "type")
	case "open_notebook_ask":
		method, endpoint = http.MethodPost, "/api/search/ask/simple"
		body = pick(a, "question", "strategy_model", "answer_model", "final_answer_model")
		for _, k := range []string{"question", "strategy_model", "answer_model", "final_answer_model"} {
			if err := require(body, k); err != nil {
				return nil, err
			}
		}
	case "open_notebook_list_notes":
		method, endpoint = http.MethodGet, "/api/notes"
		queryString(q, a, "notebook_id")
	case "open_notebook_create_note":
		method, endpoint = http.MethodPost, "/api/notes"
		body = pick(a, "title", "content", "note_type", "notebook_id")
		if err := require(body, "content"); err != nil {
			return nil, err
		}
		if len(stringValue(body["content"])) > maxTextContentBytes {
			return nil, fmt.Errorf("content exceeds %d bytes", maxTextContentBytes)
		}
	case "open_notebook_list_episode_profiles":
		method, endpoint = http.MethodGet, "/api/episode-profiles"
	case "open_notebook_list_speaker_profiles":
		method, endpoint = http.MethodGet, "/api/speaker-profiles"
	case "open_notebook_generate_podcast":
		method, endpoint = http.MethodPost, "/api/podcasts/generate"
		body = pick(a, "episode_profile", "speaker_profile", "episode_name", "content", "notebook_id", "briefing_suffix")
		for _, k := range []string{"episode_profile", "speaker_profile", "episode_name"} {
			if err := require(body, k); err != nil {
				return nil, err
			}
		}
		// Open Notebook's profile-list endpoints expose record IDs, but its
		// podcast generation endpoint looks profiles up by name. Accept either
		// form at the MCP boundary so agents can safely pass the IDs returned by
		// the discovery tools.
		episodeProfile, err := s.resolvePodcastProfileName(ctx, "/api/episode-profiles", "episode_profile", stringValue(body["episode_profile"]))
		if err != nil {
			return nil, err
		}
		speakerProfile, err := s.resolvePodcastProfileName(ctx, "/api/speaker-profiles", "speaker_profile", stringValue(body["speaker_profile"]))
		if err != nil {
			return nil, err
		}
		body["episode_profile"] = episodeProfile
		body["speaker_profile"] = speakerProfile
	case "open_notebook_get_podcast_job":
		method, endpoint = http.MethodGet, "/api/podcasts/jobs/"+pathEscape(requiredString(a, "job_id"))
	case "open_notebook_list_podcast_episodes":
		method, endpoint = http.MethodGet, "/api/podcasts/episodes"
	case "open_notebook_get_podcast_episode":
		method, endpoint = http.MethodGet, "/api/podcasts/episodes/"+pathEscape(requiredString(a, "episode_id"))
	case "open_notebook_get_podcast_audio":
		id := requiredString(a, "episode_id")
		if id == "" {
			return nil, fmt.Errorf("episode_id is required")
		}
		u := *s.baseURL
		u.Path = joinURLPath(u.Path, "/api/podcasts/episodes/"+pathEscape(id)+"/audio")
		return json.Marshal(map[string]any{"episode_id": id, "audio_url": u.String(), "note": "This URL is reachable only on the Soulacy host."})
	default:
		return nil, fmt.Errorf("unknown Open Notebook MCP tool %q", name)
	}
	if strings.Contains(endpoint, "//") || strings.HasSuffix(endpoint, "/") && endpoint != "/health" {
		return nil, fmt.Errorf("a required identifier is missing")
	}
	return s.request(ctx, method, endpoint, q, body)
}

func (s *Server) resolvePodcastProfileName(ctx context.Context, endpoint, recordPrefix, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(ref, recordPrefix+":") {
		return ref, nil
	}

	data, err := s.request(ctx, http.MethodGet, endpoint, nil, nil)
	if err != nil {
		return "", fmt.Errorf("resolve Open Notebook %s %q: %w", recordPrefix, ref, err)
	}
	var profiles []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &profiles); err != nil {
		return "", fmt.Errorf("resolve Open Notebook %s %q: invalid profile list", recordPrefix, ref)
	}
	for _, profile := range profiles {
		if profile.ID == ref && strings.TrimSpace(profile.Name) != "" {
			return profile.Name, nil
		}
	}
	return "", fmt.Errorf("%s %q was not found in Open Notebook", recordPrefix, ref)
}

func (s *Server) request(ctx context.Context, method, endpoint string, q url.Values, body map[string]any) (json.RawMessage, error) {
	u := *s.baseURL
	u.Path = joinURLPath(u.Path, endpoint)
	u.RawQuery = q.Encode()
	var r io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode Open Notebook request: %w", err)
		}
		r = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), r)
	if err != nil {
		return nil, fmt.Errorf("build Open Notebook request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the Open Notebook API is unavailable at %s: %s", s.baseURL.String(), safeNetworkError(err))
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, maxResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read Open Notebook response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return nil, fmt.Errorf("the Open Notebook response exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("the Open Notebook API returned HTTP %d%s", resp.StatusCode, safeAPIDetail(data))
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("the Open Notebook API returned an invalid JSON response")
	}
	return json.RawMessage(data), nil
}

func joinURLPath(base, endpoint string) string {
	if base == "" || base == "/" {
		return endpoint
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(endpoint, "/")
}

func pathEscape(v string) string { return url.PathEscape(strings.TrimSpace(v)) }

func requiredString(a map[string]any, key string) string {
	return strings.TrimSpace(stringValue(a[key]))
}

func stringValue(v any) string { s, _ := v.(string); return s }

func pick(a map[string]any, keys ...string) map[string]any {
	out := make(map[string]any, len(keys))
	for _, k := range keys {
		if v, ok := a[k]; ok && v != nil {
			out[k] = v
		}
	}
	return out
}

func require(m map[string]any, key string) error {
	if strings.TrimSpace(stringValue(m[key])) == "" {
		return fmt.Errorf("%s is required", key)
	}
	return nil
}

func queryString(q url.Values, a map[string]any, key string) {
	if v := strings.TrimSpace(stringValue(a[key])); v != "" {
		q.Set(key, v)
	}
}

func queryInt(q url.Values, a map[string]any, key string, min, max int) {
	v, ok := a[key]
	if !ok {
		return
	}
	n := 0
	switch x := v.(type) {
	case float64:
		n = int(x)
	case int:
		n = x
	case json.Number:
		n, _ = strconv.Atoi(x.String())
	}
	if n < min {
		n = min
	}
	if n > max {
		n = max
	}
	q.Set(key, strconv.Itoa(n))
}

func safeNetworkError(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		if errors.Is(ue.Err, context.DeadlineExceeded) {
			return "request timed out"
		}
		if op, ok := ue.Err.(*net.OpError); ok {
			return op.Op + " failed"
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "request timed out"
	}
	return "connection failed"
}

func safeAPIDetail(data []byte) string {
	var obj map[string]any
	if json.Unmarshal(data, &obj) != nil {
		return ""
	}
	for _, key := range []string{"detail", "message", "error"} {
		if text, ok := obj[key].(string); ok {
			text = strings.Join(strings.Fields(text), " ")
			if len(text) > 500 {
				text = text[:500] + "…"
			}
			if text != "" {
				return ": " + text
			}
		}
	}
	return ""
}

func marshal(v any) []byte { b, _ := json.Marshal(v); return b }
