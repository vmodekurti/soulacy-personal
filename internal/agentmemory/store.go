// Package agentmemory implements the three-layer long-term memory system for
// Soulacy agents: episodic (task history), semantic (retrieved knowledge), and
// procedural (agent-specific operating rules).
//
// Storage layout on disk:
//
//	<baseDir>/
//	├── <agentID>/
//	│   ├── episodic.jsonl   — one JSON record per line, append-only
//	│   └── procedural.md    — agent operating rules, overwritten on update
//
// The InMemoryVectorStore is the dev-mode semantic backend.
// TODO: swap for Chroma or pgvector when semantic retrieval becomes a bottleneck.
package agentmemory

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MemoryType classifies the tier a Record belongs to.
type MemoryType string

const (
	MemoryTypeEpisodic   MemoryType = "episodic"
	MemoryTypeSemantic   MemoryType = "semantic"
	MemoryTypeProcedural MemoryType = "procedural"
)

// Record is a single unit of long-term memory.
type Record struct {
	ID        string            `json:"id"`
	AgentID   string            `json:"agent_id"`
	Type      MemoryType        `json:"type"`
	Timestamp time.Time         `json:"timestamp"`
	Content   string            `json:"content"`
	Tags      []string          `json:"tags,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
}

// RetrieveQuery specifies what to retrieve for a given agent and task.
type RetrieveQuery struct {
	AgentID     string
	TaskInput   string
	Types       []MemoryType
	MaxEpisodic int
	MaxSemantic int
}

// RetrieveResult carries the three tiers of retrieved memory.
type RetrieveResult struct {
	EpisodicSummary []Record
	SemanticChunks  []Record
	ProceduralRules string
}

// Store is the uniform interface for all memory backends.
type Store interface {
	Write(r Record) error
	Retrieve(q RetrieveQuery) (RetrieveResult, error)
	UpdateProcedural(agentID, rules string) error
}

// ─── Episodic ────────────────────────────────────────────────────────────────

// EpisodicStore persists task records to a per-agent JSONL file.
// Each line is a valid JSON-encoded Record. Records are appended on write and
// returned newest-first on read.
type EpisodicStore struct {
	baseDir string
	mu      sync.Mutex
}

// NewEpisodicStore creates an EpisodicStore rooted at baseDir.
func NewEpisodicStore(baseDir string) *EpisodicStore {
	return &EpisodicStore{baseDir: baseDir}
}

func (s *EpisodicStore) agentDir(agentID string) string {
	return filepath.Join(s.baseDir, agentID)
}

func (s *EpisodicStore) episodicPath(agentID string) string {
	return filepath.Join(s.agentDir(agentID), "episodic.jsonl")
}

// Write appends a record to the agent's episodic.jsonl file.
// Creates the per-agent directory on first write.
func (s *EpisodicStore) Write(r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if r.Content == "" {
		return fmt.Errorf("agentmemory: episodic Write: content is required")
	}
	if len(r.Content) > 32*1024 {
		return fmt.Errorf("agentmemory: episodic Write: content exceeds 32KB (%d bytes)", len(r.Content))
	}
	if r.ID == "" {
		r.ID = uuid.New().String()
	}
	if r.Timestamp.IsZero() {
		r.Timestamp = time.Now().UTC()
	}
	r.Type = MemoryTypeEpisodic

	if err := os.MkdirAll(s.agentDir(r.AgentID), 0755); err != nil {
		return fmt.Errorf("agentmemory: mkdir %s: %w", s.agentDir(r.AgentID), err)
	}

	path := s.episodicPath(r.AgentID)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("agentmemory: open episodic: %w", err)
	}
	defer f.Close()

	line, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("agentmemory: marshal record: %w", err)
	}
	if _, err := fmt.Fprintf(f, "%s\n", line); err != nil {
		return err
	}
	return s.rotateLocked(path)
}

// maxEpisodicBytes is the point at which the log is rolled.
//
// The file was append-only with no rotation, and ReadRecent parsed ALL of it and
// sorted ALL of it before keeping the newest five. That read happens on every
// agent run, to build the system prompt — so an agent that had been running for
// months paid for its whole history on every single turn, and the file itself
// grew without limit from a memory:write endpoint at 32 KB a request.
const maxEpisodicBytes = 8 << 20

// maxEpisodicScanRecords bounds the window ReadRecent keeps in memory when the
// caller asks for "everything". Callers ask for 5.
const maxEpisodicScanRecords = 500

// rotateLocked rolls episodic.jsonl to episodic.jsonl.1 once it passes the cap,
// discarding whatever the previous .1 held. Caller holds s.mu.
//
// One generation, not many: episodic memory is a recency store — ReadRecent asks
// for the newest handful — so keeping deep history costs disk to serve nothing.
func (s *EpisodicStore) rotateLocked(path string) error {
	info, err := os.Stat(path)
	if err != nil || info.Size() <= maxEpisodicBytes {
		return nil //nolint:nilerr // a failed stat is not a failed write
	}
	return os.Rename(path, path+".1")
}

// ReadRecent returns up to max records for agentID, newest first.
// Returns nil (not an error) if no episodic file exists yet.
func (s *EpisodicStore) ReadRecent(agentID string, max int) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.episodicPath(agentID)
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("agentmemory: open episodic: %w", err)
	}
	defer f.Close()

	// Keep only a bounded window of the most recent records while scanning,
	// rather than accumulating the whole file and sorting it afterwards. This
	// read is on the hot path — it runs on every agent turn to build the system
	// prompt — and the file is append-ordered, so the tail is what matters.
	keep := max
	if keep <= 0 || keep > maxEpisodicScanRecords {
		keep = maxEpisodicScanRecords
	}
	ring := make([]Record, 0, keep)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20) // 1 MB per line
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue // skip malformed lines rather than failing the whole read
		}
		if len(ring) == keep {
			copy(ring, ring[1:])
			ring = ring[:keep-1]
		}
		ring = append(ring, r)
	}
	records := ring
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("agentmemory: scan episodic: %w", err)
	}

	// Newest first
	sort.Slice(records, func(i, j int) bool {
		return records[i].Timestamp.After(records[j].Timestamp)
	})

	if max > 0 && len(records) > max {
		records = records[:max]
	}
	return records, nil
}

// ─── Procedural ──────────────────────────────────────────────────────────────

// ProceduralStore persists per-agent operating rules as a Markdown file.
// The file is overwritten on each update — the LLM generates a complete
// revised ruleset after each task (see MEM-05).
type ProceduralStore struct {
	baseDir string
	mu      sync.Mutex
}

// NewProceduralStore creates a ProceduralStore rooted at baseDir.
func NewProceduralStore(baseDir string) *ProceduralStore {
	return &ProceduralStore{baseDir: baseDir}
}

func (s *ProceduralStore) path(agentID string) string {
	return filepath.Join(s.baseDir, agentID, "procedural.md")
}

// Read returns the current procedural rules for agentID, or "" if none exist.
func (s *ProceduralStore) Read(agentID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path(agentID))
	if err != nil {
		return ""
	}
	return string(b)
}

// Update overwrites the procedural rules file for agentID.
func (s *ProceduralStore) Update(agentID, rules string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.baseDir, agentID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("agentmemory: mkdir %s: %w", dir, err)
	}
	return os.WriteFile(s.path(agentID), []byte(rules), 0644)
}

// ─── Semantic (in-memory dev stub) ───────────────────────────────────────────

// InMemoryVectorStore is a dev-mode semantic backend that uses keyword
// frequency scoring to approximate semantic similarity.
//
// TODO: replace with Chroma or pgvector for production semantic retrieval.
type InMemoryVectorStore struct {
	mu      sync.RWMutex
	records []Record
}

// NewInMemoryVectorStore creates an empty in-memory semantic store.
func NewInMemoryVectorStore() *InMemoryVectorStore {
	return &InMemoryVectorStore{}
}

// Write adds a semantic record to the in-memory store.
func (v *InMemoryVectorStore) Write(r Record) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if r.ID == "" {
		r.ID = uuid.New().String()
	}
	if r.Timestamp.IsZero() {
		r.Timestamp = time.Now().UTC()
	}
	r.Type = MemoryTypeSemantic
	v.records = append(v.records, r)
	return nil
}

// Search returns up to max records whose content overlaps with query words.
// Results are ranked by keyword hit count, then recency.
func (v *InMemoryVectorStore) Search(agentID, query string, max int) []Record {
	v.mu.RLock()
	defer v.mu.RUnlock()

	words := strings.Fields(strings.ToLower(query))

	type scored struct {
		r     Record
		score int
	}

	var candidates []scored
	for _, r := range v.records {
		if r.AgentID != agentID {
			continue
		}
		content := strings.ToLower(r.Content)
		score := 0
		for _, w := range words {
			if strings.Contains(content, w) {
				score++
			}
		}
		if score > 0 {
			candidates = append(candidates, scored{r, score})
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].r.Timestamp.After(candidates[j].r.Timestamp)
	})

	out := make([]Record, 0, min(max, len(candidates)))
	for i, c := range candidates {
		if max > 0 && i >= max {
			break
		}
		out = append(out, c.r)
	}
	return out
}

// ─── Composite ───────────────────────────────────────────────────────────────

// CompositeStore orchestrates the three memory layers under a single Store
// interface. It is the primary entry point for agent memory access.
type CompositeStore struct {
	episodic   *EpisodicStore
	semantic   *InMemoryVectorStore
	procedural *ProceduralStore
	// rulelog versions every rulebook write (Story E23). nil when the
	// rulebook db failed to open — writes then degrade to unversioned
	// (never block boot), reads report no history.
	rulelog *RuleLog
}

// NewCompositeStore creates a CompositeStore rooted at baseDir.
// If vectorStore is nil, a fresh InMemoryVectorStore is created.
func NewCompositeStore(baseDir string, vectorStore *InMemoryVectorStore) *CompositeStore {
	if vectorStore == nil {
		vectorStore = NewInMemoryVectorStore()
	}
	rl, err := OpenRuleLog(filepath.Join(baseDir, "rulebook.db"))
	if err != nil {
		rl = nil // degrade: rules still work, just unversioned
	}
	return &CompositeStore{
		episodic:   NewEpisodicStore(baseDir),
		semantic:   vectorStore,
		procedural: NewProceduralStore(baseDir),
		rulelog:    rl,
	}
}

// Close releases the rulebook database (the other backends are files).
func (c *CompositeStore) Close() error {
	if c.rulelog != nil {
		return c.rulelog.Close()
	}
	return nil
}

// Write dispatches the record to the appropriate backend based on r.Type.
// Episodic is the default when Type is empty or unrecognised.
func (c *CompositeStore) Write(r Record) error {
	if len(r.Content) > 32*1024 {
		return fmt.Errorf("agentmemory: Write: content exceeds 32KB limit (%d bytes)", len(r.Content))
	}
	switch r.Type {
	case MemoryTypeSemantic:
		return c.semantic.Write(r)
	case MemoryTypeProcedural:
		return c.procedural.Update(r.AgentID, r.Content)
	default:
		return c.episodic.Write(r)
	}
}

// Retrieve fetches recent episodic records, relevant semantic chunks, and
// current procedural rules for the agent described by q.
func (c *CompositeStore) Retrieve(q RetrieveQuery) (RetrieveResult, error) {
	maxEp := q.MaxEpisodic
	if maxEp <= 0 {
		maxEp = 5
	}
	maxSem := q.MaxSemantic
	if maxSem <= 0 {
		maxSem = 8
	}

	var result RetrieveResult
	var err error

	result.EpisodicSummary, err = c.episodic.ReadRecent(q.AgentID, maxEp)
	if err != nil {
		return result, fmt.Errorf("agentmemory: Retrieve episodic: %w", err)
	}

	if q.TaskInput != "" {
		result.SemanticChunks = c.semantic.Search(q.AgentID, q.TaskInput, maxSem)
	}

	result.ProceduralRules = c.procedural.Read(q.AgentID)
	return result, nil
}

// UpdateProcedural overwrites the procedural rules for agentID through the
// versioned path with source "manual" (the GUI PUT route lands here).
func (c *CompositeStore) UpdateProcedural(agentID, rules string) error {
	_, err := c.UpdateProceduralVersioned(agentID, rules, "manual")
	return err
}

// UpdateProceduralVersioned writes rules AND appends an immutable version
// (Story E23). Locked agents refuse every write — auto_update and manual
// alike — with ErrRulebookLocked. Returns the new version number (0 when
// the rule log is unavailable and the write degraded to unversioned).
func (c *CompositeStore) UpdateProceduralVersioned(agentID, rules, source string) (int, error) {
	if c.rulelog != nil && c.rulelog.Locked(agentID) {
		return 0, ErrRulebookLocked
	}
	if err := c.procedural.Update(agentID, rules); err != nil {
		return 0, err
	}
	if c.rulelog == nil {
		return 0, nil
	}
	return c.rulelog.Append(agentID, rules, source)
}

// RulebookVersions lists an agent's rule history, newest first.
func (c *CompositeStore) RulebookVersions(agentID string) ([]RuleVersion, error) {
	if c.rulelog == nil {
		return nil, nil
	}
	return c.rulelog.Versions(agentID)
}

// RulebookVersion returns the rules text of one historical version.
func (c *CompositeStore) RulebookVersion(agentID string, version int) (string, error) {
	if c.rulelog == nil {
		return "", fmt.Errorf("agentmemory: rulebook history unavailable")
	}
	return c.rulelog.Get(agentID, version)
}

// RulebookLocked reports whether agentID's rules are frozen.
func (c *CompositeStore) RulebookLocked(agentID string) bool {
	return c.rulelog != nil && c.rulelog.Locked(agentID)
}

// SetRulebookLocked freezes/unfreezes an agent's rules.
func (c *CompositeStore) SetRulebookLocked(agentID string, locked bool) error {
	if c.rulelog == nil {
		return fmt.Errorf("agentmemory: rulebook history unavailable")
	}
	return c.rulelog.SetLocked(agentID, locked)
}

// RollbackProcedural re-applies a historical version as a NEW version with
// source "rollback" — history is never rewritten. Locked agents refuse.
func (c *CompositeStore) RollbackProcedural(agentID string, version int) (int, error) {
	if c.rulelog == nil {
		return 0, fmt.Errorf("agentmemory: rulebook history unavailable")
	}
	rules, err := c.rulelog.Get(agentID, version)
	if err != nil {
		return 0, err
	}
	return c.UpdateProceduralVersioned(agentID, rules, "rollback")
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// BuildContextBlock formats a RetrieveResult into a system prompt injection
// block. The returned string is intended to be prepended to the agent's system
// prompt before any LLM call so the model has full memory context.
func BuildContextBlock(r RetrieveResult) string {
	var sb strings.Builder

	if len(r.EpisodicSummary) > 0 {
		sb.WriteString("## Recent task history\n")
		for _, ep := range r.EpisodicSummary {
			sb.WriteString(fmt.Sprintf("- [%s] %s\n",
				ep.Timestamp.Format("2006-01-02 15:04"), ep.Content))
		}
		sb.WriteString("\n")
	}

	if len(r.SemanticChunks) > 0 {
		sb.WriteString("## Relevant knowledge\n")
		for _, ch := range r.SemanticChunks {
			sb.WriteString(fmt.Sprintf("- %s\n", ch.Content))
		}
		sb.WriteString("\n")
	}

	if r.ProceduralRules != "" {
		sb.WriteString("## Operating rules\n")
		sb.WriteString(r.ProceduralRules)
		sb.WriteString("\n")
	}

	return sb.String()
}

// ResultToEpisodicRecord converts a completed reasoning loop result into an
// episodic memory Record ready to be written via CompositeStore.Write().
func ResultToEpisodicRecord(agentID, taskInput, output string, tags []string) Record {
	content := fmt.Sprintf("Task: %s\nOutput: %s", taskInput, output)
	if len(content) > 32*1024 {
		content = content[:32*1024]
	}
	return Record{
		ID:        uuid.New().String(),
		AgentID:   agentID,
		Type:      MemoryTypeEpisodic,
		Timestamp: time.Now().UTC(),
		Content:   content,
		Tags:      tags,
	}
}

// ─── CompositeStore public accessors (used by gateway handlers) ──────────────

// EpisodicRecords returns up to max episodic records for agentID, newest first.
// Pass max=0 to return all records.
func (c *CompositeStore) EpisodicRecords(agentID string, max int) ([]Record, error) {
	return c.episodic.ReadRecent(agentID, max)
}

// ProceduralRules returns the current procedural rules for agentID, or "".
func (c *CompositeStore) ProceduralRules(agentID string) string {
	return c.procedural.Read(agentID)
}

// ClearEpisodic removes the episodic JSONL file for agentID entirely.
func (c *CompositeStore) ClearEpisodic(agentID string) error {
	path := c.episodic.episodicPath(agentID)
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil // idempotent
	}
	return err
}

// ─────────────────────────────────────────────────────────────────────────────

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
