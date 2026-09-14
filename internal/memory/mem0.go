// mem0.go — Story E31: Mem0 as a drop-in adaptive memory provider.
//
// Mem0 performs its own extraction and conflict resolution server-side, so
// this adapter maps Soulacy's Adaptive contract onto Mem0's REST API and lets
// the remote engine do the thinking. Two API flavours are supported:
//
//   - "platform": the hosted service at api.mem0.ai (Token auth, /v1 paths)
//   - "server":   the open-source REST server you can run as a sidecar
//
// The flavour is inferred from the base URL and can be forced in config.
// Scope maps to Mem0's user_id (owner) and agent_id; the workspace travels in
// metadata and is filtered client-side. Graph relations, per-memory history,
// expiration and custom instructions/categories map onto the matching Mem0
// features where the flavour supports them.
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Mem0Config configures the adapter.
type Mem0Config struct {
	BaseURL     string
	APIKey      string
	EnableGraph bool
	// APIStyle is "platform", "server" or "" (infer from BaseURL).
	APIStyle string
	Timeout  time.Duration
	// Instructions and CustomCategories are forwarded to the provider's
	// extraction where supported (hosted platform).
	Instructions     string
	CustomCategories []string
}

// Mem0Adaptive implements Adaptive over Mem0's HTTP API.
type Mem0Adaptive struct {
	cfg    Mem0Config
	client *http.Client
	style  string
}

// NewMem0Adaptive validates the configuration and returns the adapter. It
// does not call the network; a misconfigured provider fails fast here so the
// host can fall back to the local engine.
func NewMem0Adaptive(cfg Mem0Config) (*Mem0Adaptive, error) {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.mem0.ai"
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("adaptive memory: mem0 base_url must be an http(s) URL")
	}
	style := strings.ToLower(strings.TrimSpace(cfg.APIStyle))
	if style == "" {
		if strings.Contains(u.Host, "mem0.ai") {
			style = "platform"
		} else {
			style = "server"
		}
	}
	if style != "platform" && style != "server" {
		return nil, fmt.Errorf("adaptive memory: mem0 api_style must be platform or server")
	}
	if style == "platform" && strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("adaptive memory: mem0 api_key is required for the hosted platform")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	cfg.CustomCategories = NormalizeCategories(cfg.CustomCategories)
	return &Mem0Adaptive{cfg: cfg, client: &http.Client{Timeout: cfg.Timeout}, style: style}, nil
}

func (m *Mem0Adaptive) Provider() string { return "mem0" }
func (m *Mem0Adaptive) Close() error     { return nil }

// Categories implements Adaptive.
func (m *Mem0Adaptive) Categories() []string {
	out := make([]string, 0, len(BuiltinCategories)+len(m.cfg.CustomCategories))
	for _, c := range BuiltinCategories {
		out = append(out, string(c))
	}
	return append(out, m.cfg.CustomCategories...)
}

// Style reports the resolved API flavour ("platform" or "server").
func (m *Mem0Adaptive) Style() string { return m.style }

func (m *Mem0Adaptive) path(p string) string {
	if m.style == "platform" {
		return m.cfg.BaseURL + "/v1" + p + "/"
	}
	return m.cfg.BaseURL + p
}

func (m *Mem0Adaptive) do(ctx context.Context, method, rawURL string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if m.cfg.APIKey != "" {
		if m.style == "platform" {
			req.Header.Set("Authorization", "Token "+m.cfg.APIKey)
		} else {
			req.Header.Set("Authorization", "Bearer "+m.cfg.APIKey)
		}
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProviderFailed, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrFactNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: mem0 %s %s returned %d", ErrProviderFailed, method, req.URL.Path, resp.StatusCode)
	}
	return data, nil
}

// mem0Memory is the subset of Mem0's memory object we read. Field names vary
// slightly between flavours; both are tolerated.
type mem0Memory struct {
	ID         string            `json:"id"`
	Memory     string            `json:"memory"`
	Text       string            `json:"text"`
	Score      float64           `json:"score"`
	Event      string            `json:"event"`
	UserID     string            `json:"user_id"`
	AgentID    string            `json:"agent_id"`
	Categories []string          `json:"categories"`
	Metadata   map[string]any    `json:"metadata"`
	CreatedAt  string            `json:"created_at"`
	UpdatedAt  string            `json:"updated_at"`
	ExpiresAt  string            `json:"expiration_date"`
	Data       map[string]string `json:"data"`
}

func (mm mem0Memory) content() string {
	if mm.Memory != "" {
		return mm.Memory
	}
	if mm.Text != "" {
		return mm.Text
	}
	return mm.Data["memory"]
}

// mem0Relation is a graph edge as returned by Mem0 with graph memory on.
type mem0Relation struct {
	Source       string `json:"source"`
	Relationship string `json:"relationship"`
	Target       string `json:"target"`
	// server flavour
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Object    string `json:"object"`
}

type mem0Envelope struct {
	Results   []mem0Memory   `json:"results"`
	Memories  []mem0Memory   `json:"memories"`
	Relations []mem0Relation `json:"relations"`
}

// decodeEnvelope accepts a bare array, {"results":[...]}, or {"memories":[...]},
// with optional "relations".
func decodeEnvelope(data []byte) (mem0Envelope, error) {
	data = bytes.TrimSpace(data)
	var env mem0Envelope
	if len(data) == 0 {
		return env, nil
	}
	if data[0] == '[' {
		return env, json.Unmarshal(data, &env.Results)
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return env, err
	}
	if len(env.Results) == 0 && len(env.Memories) > 0 {
		env.Results = env.Memories
	}
	return env, nil
}

func decodeMemories(data []byte) ([]mem0Memory, error) {
	env, err := decodeEnvelope(data)
	return env.Results, err
}

func parseMem0Time(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999", "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func (m *Mem0Adaptive) toFact(scope FactScope, mm mem0Memory) Fact {
	cat := FactPreference
	if mdCat, ok := mm.Metadata["category"].(string); ok && AllowedCategory(mdCat, m.cfg.CustomCategories) {
		cat = FactCategory(strings.ToLower(mdCat))
	} else if len(mm.Categories) > 0 && AllowedCategory(mm.Categories[0], m.cfg.CustomCategories) {
		cat = FactCategory(strings.ToLower(mm.Categories[0]))
	}
	agent := scope.AgentID
	if mm.AgentID != "" {
		agent = mm.AgentID
	}
	f := Fact{
		ID: mm.ID, Workspace: scope.Workspace, Owner: scope.Owner, AgentID: agent,
		Category: cat, Content: strings.TrimSpace(mm.content()), Confidence: 1,
		Status: FactStatusActive, Source: "provider",
		CreatedAt: parseMem0Time(mm.CreatedAt), UpdatedAt: parseMem0Time(mm.UpdatedAt),
	}
	if src, ok := mm.Metadata["source"].(string); ok && src == "manual" {
		f.Source = "manual"
	}
	if t := parseMem0Time(mm.ExpiresAt); !t.IsZero() {
		f.ExpiresAt = &t
	}
	if f.UpdatedAt.IsZero() {
		f.UpdatedAt = f.CreatedAt
	}
	return f
}

func (m *Mem0Adaptive) toRelation(scope FactScope, r mem0Relation) (Relation, bool) {
	s, p, o := r.Source, r.Relationship, r.Target
	if s == "" {
		s, p, o = r.Subject, r.Predicate, r.Object
	}
	rel, err := normalizeRelation(Relation{Workspace: scope.Workspace, Owner: scope.Owner, AgentID: scope.AgentID, Subject: s, Predicate: strings.ReplaceAll(p, "_", " "), Object: o, Source: "provider"})
	if err != nil {
		return Relation{}, false
	}
	return rel, true
}

func (m *Mem0Adaptive) inWorkspace(scope FactScope, mm mem0Memory) bool {
	ws, ok := mm.Metadata["workspace"].(string)
	if !ok || ws == "" {
		// Memories written before workspaces existed belong to the default.
		return scope.Workspace == DefaultWorkspace
	}
	return ws == scope.Workspace
}

func (m *Mem0Adaptive) scopeBody(scope FactScope) map[string]any {
	body := map[string]any{"user_id": scope.Owner}
	if scope.AgentID != "" {
		body["agent_id"] = scope.AgentID
	}
	return body
}

// Remember hands the turn to Mem0, which extracts and reconciles facts itself.
func (m *Mem0Adaptive) Remember(ctx context.Context, scope FactScope, turn Turn) (RememberOutcome, error) {
	out := RememberOutcome{Provider: "mem0", ExtractedAt: time.Now().UTC()}
	scope = scope.Normalize()
	if scope.Owner == "" {
		return out, ErrInvalidScope
	}
	if !ShouldExtract(turn) {
		return out, nil
	}
	msgs := []map[string]string{{"role": "user", "content": turn.User}}
	if strings.TrimSpace(turn.Assistant) != "" {
		msgs = append(msgs, map[string]string{"role": "assistant", "content": clip(turn.Assistant, 2000)})
	}
	body := m.scopeBody(scope)
	body["messages"] = msgs
	body["metadata"] = map[string]any{"workspace": scope.Workspace, "session_id": turn.SessionID, "run_id": turn.RunID}
	if m.cfg.EnableGraph {
		body["enable_graph"] = true
	}
	if m.style == "platform" {
		body["version"] = "v2"
		if ins := strings.TrimSpace(m.cfg.Instructions); ins != "" {
			body["custom_instructions"] = ins
		}
		if len(m.cfg.CustomCategories) > 0 {
			cats := map[string]string{}
			for _, c := range m.cfg.CustomCategories {
				cats[c] = "Custom category: " + c
			}
			body["custom_categories"] = cats
		}
	}
	data, err := m.do(ctx, http.MethodPost, m.path("/memories"), body)
	if err != nil {
		return out, err
	}
	env, _ := decodeEnvelope(data)
	out.Candidates = len(env.Results) + len(env.Relations)
	out.RelationsAdded = len(env.Relations)
	for _, r := range env.Results {
		switch strings.ToUpper(r.Event) {
		case "ADD":
			out.Added = append(out.Added, r.ID)
		case "UPDATE":
			out.Superseded = append(out.Superseded, r.ID)
			out.Added = append(out.Added, r.ID)
		case "DELETE":
			out.Retracted = append(out.Retracted, r.ID)
		case "NONE":
			out.Skipped++
		default:
			if r.ID != "" {
				out.Added = append(out.Added, r.ID)
			}
		}
	}
	return out, nil
}

// Recall runs Mem0's semantic search.
func (m *Mem0Adaptive) Recall(ctx context.Context, scope FactScope, query string, limit int) ([]ScoredFact, error) {
	scope = scope.Normalize()
	if scope.Owner == "" {
		return nil, ErrInvalidScope
	}
	if limit <= 0 {
		limit = 5
	}
	if strings.TrimSpace(query) == "" {
		facts, err := m.List(ctx, scope, FactStatusActive, limit)
		if err != nil {
			return nil, err
		}
		out := make([]ScoredFact, 0, len(facts))
		for _, f := range facts {
			out = append(out, ScoredFact{Fact: f, Score: 1})
		}
		return out, nil
	}
	env, err := m.search(ctx, scope, query, limit)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	out := make([]ScoredFact, 0, len(env.Results))
	for _, r := range env.Results {
		if !m.inWorkspace(scope, r) {
			continue
		}
		f := m.toFact(scope, r)
		if f.Content == "" || f.Expired(now) {
			continue
		}
		out = append(out, ScoredFact{Fact: f, Score: clamp01(r.Score)})
	}
	SortScored(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *Mem0Adaptive) search(ctx context.Context, scope FactScope, query string, limit int) (mem0Envelope, error) {
	body := m.scopeBody(scope)
	body["query"] = query
	body["limit"] = limit
	if m.cfg.EnableGraph {
		body["enable_graph"] = true
	}
	if m.style == "platform" {
		body["version"] = "v2"
		body["filters"] = m.platformFilters(scope)
	}
	data, err := m.do(ctx, http.MethodPost, m.searchPath(), body)
	if err != nil {
		return mem0Envelope{}, err
	}
	env, err := decodeEnvelope(data)
	if err != nil {
		return mem0Envelope{}, fmt.Errorf("%w: decode search: %v", ErrProviderFailed, err)
	}
	return env, nil
}

// Relations returns graph edges from the provider's search response. With
// graph memory disabled the provider returns none.
func (m *Mem0Adaptive) Relations(ctx context.Context, scope FactScope, query string, limit int) ([]Relation, error) {
	scope = scope.Normalize()
	if scope.Owner == "" {
		return nil, ErrInvalidScope
	}
	if !m.cfg.EnableGraph {
		return nil, nil
	}
	if limit <= 0 {
		limit = 3
	}
	if strings.TrimSpace(query) == "" {
		query = "everything about the user"
	}
	env, err := m.search(ctx, scope, query, max(limit, 5))
	if err != nil {
		return nil, err
	}
	out := make([]Relation, 0, len(env.Relations))
	for _, r := range env.Relations {
		if rel, ok := m.toRelation(scope, r); ok {
			out = append(out, rel)
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m *Mem0Adaptive) searchPath() string {
	if m.style == "platform" {
		return m.cfg.BaseURL + "/v2/memories/search/"
	}
	return m.cfg.BaseURL + "/search"
}

func (m *Mem0Adaptive) platformFilters(scope FactScope) map[string]any {
	and := []map[string]any{{"user_id": scope.Owner}}
	if scope.AgentID != "" {
		and = append(and, map[string]any{"agent_id": scope.AgentID})
	}
	return map[string]any{"AND": and}
}

// List returns the owner's memories. Mem0 has no superseded/retracted state
// (it rewrites or deletes in place), so any status other than active yields
// nothing.
func (m *Mem0Adaptive) List(ctx context.Context, scope FactScope, status string, limit int) ([]Fact, error) {
	scope = scope.Normalize()
	if scope.Owner == "" {
		return nil, ErrInvalidScope
	}
	if status != "" && status != FactStatusActive {
		return nil, nil
	}
	if limit <= 0 {
		limit = 500
	}
	var data []byte
	var err error
	if m.style == "platform" {
		body := map[string]any{"filters": m.platformFilters(scope), "page_size": limit}
		data, err = m.do(ctx, http.MethodPost, m.cfg.BaseURL+"/v2/memories/", body)
	} else {
		q := url.Values{"user_id": {scope.Owner}}
		if scope.AgentID != "" {
			q.Set("agent_id", scope.AgentID)
		}
		data, err = m.do(ctx, http.MethodGet, m.path("/memories")+"?"+q.Encode(), nil)
	}
	if err != nil {
		return nil, err
	}
	results, err := decodeMemories(data)
	if err != nil {
		return nil, fmt.Errorf("%w: decode list: %v", ErrProviderFailed, err)
	}
	out := make([]Fact, 0, len(results))
	for _, r := range results {
		if !m.inWorkspace(scope, r) {
			continue
		}
		f := m.toFact(scope, r)
		if f.Content != "" {
			out = append(out, f)
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func expirationField(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02")
}

// Add stores a verbatim fact (no server-side inference).
func (m *Mem0Adaptive) Add(ctx context.Context, scope FactScope, in FactInput) (Fact, error) {
	scope = scope.Normalize()
	if scope.Owner == "" {
		return Fact{}, ErrInvalidScope
	}
	content, err := NormalizeFactContent(in.Content)
	if err != nil {
		return Fact{}, err
	}
	if !AllowedCategory(string(in.Category), m.cfg.CustomCategories) {
		return Fact{}, ErrInvalidFact
	}
	body := m.scopeBody(scope)
	body["messages"] = []map[string]string{{"role": "user", "content": content}}
	body["infer"] = false
	body["metadata"] = map[string]any{"workspace": scope.Workspace, "category": string(in.Category), "source": "manual"}
	if exp := expirationField(in.ExpiresAt); exp != "" {
		body["expiration_date"] = exp
	}
	data, err := m.do(ctx, http.MethodPost, m.path("/memories"), body)
	if err != nil {
		return Fact{}, err
	}
	results, _ := decodeMemories(data)
	now := time.Now().UTC()
	f := Fact{Workspace: scope.Workspace, Owner: scope.Owner, AgentID: scope.AgentID, Category: in.Category, Content: content, Confidence: 1, Status: FactStatusActive, Source: "manual", ExpiresAt: in.ExpiresAt, CreatedAt: now, UpdatedAt: now}
	for _, r := range results {
		if r.ID != "" {
			f.ID = r.ID
			break
		}
	}
	return f, nil
}

// Update rewrites a memory's text in place.
func (m *Mem0Adaptive) Update(ctx context.Context, scope FactScope, id string, in FactInput) (Fact, error) {
	scope = scope.Normalize()
	if scope.Owner == "" {
		return Fact{}, ErrInvalidScope
	}
	content, err := NormalizeFactContent(in.Content)
	if err != nil {
		return Fact{}, err
	}
	if !AllowedCategory(string(in.Category), m.cfg.CustomCategories) {
		return Fact{}, ErrInvalidFact
	}
	body := map[string]any{"text": content, "metadata": map[string]any{"workspace": scope.Workspace, "category": string(in.Category)}}
	if exp := expirationField(in.ExpiresAt); exp != "" {
		body["expiration_date"] = exp
	}
	if _, err := m.do(ctx, http.MethodPut, m.path("/memories/"+url.PathEscape(id)), body); err != nil {
		return Fact{}, err
	}
	return Fact{ID: id, Workspace: scope.Workspace, Owner: scope.Owner, AgentID: scope.AgentID, Category: in.Category, Content: content, Confidence: 1, Status: FactStatusActive, Source: "provider", ExpiresAt: in.ExpiresAt, UpdatedAt: time.Now().UTC()}, nil
}

func (m *Mem0Adaptive) Delete(ctx context.Context, scope FactScope, id string) error {
	scope = scope.Normalize()
	if scope.Owner == "" {
		return ErrInvalidScope
	}
	_, err := m.do(ctx, http.MethodDelete, m.path("/memories/"+url.PathEscape(id)), nil)
	return err
}

// mem0History is one row of Mem0's per-memory history endpoint.
type mem0History struct {
	ID        any    `json:"id"`
	MemoryID  string `json:"memory_id"`
	OldMemory string `json:"old_memory"`
	NewMemory string `json:"new_memory"`
	Event     string `json:"event"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// History maps Mem0's change log onto FactEvents, newest first.
func (m *Mem0Adaptive) History(ctx context.Context, scope FactScope, id string) ([]FactEvent, error) {
	scope = scope.Normalize()
	if scope.Owner == "" {
		return nil, ErrInvalidScope
	}
	data, err := m.do(ctx, http.MethodGet, m.path("/memories/"+url.PathEscape(id)+"/history"), nil)
	if err != nil {
		return nil, err
	}
	var rows []mem0History
	if err := json.Unmarshal(bytes.TrimSpace(data), &rows); err != nil {
		var wrapped struct {
			Results []mem0History `json:"results"`
		}
		if json.Unmarshal(data, &wrapped) != nil {
			return nil, fmt.Errorf("%w: decode history: %v", ErrProviderFailed, err)
		}
		rows = wrapped.Results
	}
	out := make([]FactEvent, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		ev := FactEvent{FactID: id, Actor: "provider", Status: FactStatusActive, Category: FactPreference, CreatedAt: parseMem0Time(r.UpdatedAt)}
		if ev.CreatedAt.IsZero() {
			ev.CreatedAt = parseMem0Time(r.CreatedAt)
		}
		switch strings.ToUpper(r.Event) {
		case "ADD":
			ev.Event, ev.Content = "created", r.NewMemory
		case "UPDATE":
			ev.Event, ev.Content = "updated", r.OldMemory
		case "DELETE":
			ev.Event, ev.Content, ev.Status = "deleted", r.OldMemory, FactStatusRetracted
		default:
			ev.Event, ev.Content = strings.ToLower(r.Event), r.NewMemory
		}
		ev.ID = int64(len(rows) - i)
		out = append(out, ev)
	}
	if len(out) == 0 {
		return nil, ErrFactNotFound
	}
	return out, nil
}

// Purge deletes every memory for the owner (and agent, when scoped).
func (m *Mem0Adaptive) Purge(ctx context.Context, scope FactScope) (int64, error) {
	scope = scope.Normalize()
	if scope.Owner == "" {
		return 0, ErrInvalidScope
	}
	before, _ := m.List(ctx, scope, FactStatusActive, 2000)
	q := url.Values{"user_id": {scope.Owner}}
	if scope.AgentID != "" {
		q.Set("agent_id", scope.AgentID)
	}
	if _, err := m.do(ctx, http.MethodDelete, m.path("/memories")+"?"+q.Encode(), nil); err != nil && !errors.Is(err, ErrFactNotFound) {
		return 0, err
	}
	return int64(len(before)), nil
}
