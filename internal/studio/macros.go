package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/soulacy/soulacy/pkg/message"
)

// WorkflowPattern is procedural macro-memory. It deliberately stores no tool
// arguments, responses, user data, or credentials: only intent and structure.
type WorkflowPattern struct {
	ID        string            `json:"id"`
	Intent    string            `json:"intent"`
	Tools     []string          `json:"tools"`
	Branches  []string          `json:"branches,omitempty"`
	Structure []WorkflowStep    `json:"structure,omitempty"`
	Count     int               `json:"count"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	RunIDs    []string          `json:"run_ids,omitempty"`
	Helpful   int               `json:"helpful,omitempty"`
	Unhelpful int               `json:"unhelpful,omitempty"`
	Ratings   map[string]int    `json:"ratings,omitempty"`
}

// WorkflowStep is a payload-free execution landmark. It preserves ordering,
// node/tool identity and branch membership without retaining arguments/results.
type WorkflowStep struct {
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Branch        string `json:"branch,omitempty"`
	ParallelGroup string `json:"parallel_group,omitempty"`
}

type MacroStore struct {
	path string
	mu   sync.Mutex
}

func NewMacroStore(path string) *MacroStore { return &MacroStore{path: path} }

func (s *MacroStore) All() []WorkflowPattern {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *MacroStore) Add(pattern WorkflowPattern) error {
	intent, safe := sanitizeLearnedText(pattern.Intent, 500)
	if !safe || len(pattern.Tools) < 2 {
		return nil
	}
	pattern.Intent = intent
	pattern.Tools = compactStrings(pattern.Tools)
	pattern.Branches = compactStrings(pattern.Branches)
	if pattern.ID == "" {
		pattern.ID = workflowPatternID(pattern)
	}
	now := time.Now().UTC()
	if pattern.CreatedAt.IsZero() {
		pattern.CreatedAt = now
	}
	pattern.UpdatedAt = now
	if pattern.Count <= 0 {
		pattern.Count = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	patterns := s.loadLocked()
	for i := range patterns {
		if patterns[i].ID == pattern.ID {
			if len(pattern.RunIDs) > 0 {
				for _, seen := range patterns[i].RunIDs {
					if seen == pattern.RunIDs[0] {
						return nil
					}
				}
				patterns[i].RunIDs = append(patterns[i].RunIDs, pattern.RunIDs[0])
				if len(patterns[i].RunIDs) > 1000 {
					patterns[i].RunIDs = patterns[i].RunIDs[len(patterns[i].RunIDs)-1000:]
				}
			}
			patterns[i].Count++
			patterns[i].UpdatedAt = now
			return s.writeLocked(patterns)
		}
	}
	patterns = append(patterns, pattern)
	return s.writeLocked(patterns)
}

func (s *MacroStore) Similar(intent string, limit int) []WorkflowPattern {
	query := semanticTerms(intent)
	if len(query) == 0 {
		return nil
	}
	type scored struct {
		pattern WorkflowPattern
		score   float64
	}
	var matches []scored
	for _, pattern := range s.All() {
		// A pattern supported only by explicitly rejected runs is not proven and
		// must not influence future generation.
		if pattern.Helpful == 0 && pattern.Unhelpful >= pattern.Count {
			continue
		}
		score := jaccard(query, semanticTerms(pattern.Intent+" "+strings.Join(pattern.Tools, " ")))
		if total := pattern.Helpful + pattern.Unhelpful; total > 0 {
			quality := float64(pattern.Helpful+1) / float64(total+2)
			score *= 0.75 + 0.5*quality
		}
		if score >= 0.18 {
			matches = append(matches, scored{pattern: pattern, score: score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		if matches[i].pattern.Count != matches[j].pattern.Count {
			return matches[i].pattern.Count > matches[j].pattern.Count
		}
		return matches[i].pattern.UpdatedAt.After(matches[j].pattern.UpdatedAt)
	})
	if limit <= 0 {
		limit = 3
	}
	if len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]WorkflowPattern, len(matches))
	for i := range matches {
		out[i] = matches[i].pattern
	}
	return out
}

// RecordFeedback applies explicit human evaluation to every workflow pattern
// distilled from runID. Ratings are upserted per run so changing a thumb does
// not double-count. A net-rejected single-run pattern is suppressed by Similar.
func (s *MacroStore) RecordFeedback(runID string, rating int) error {
	runID = strings.TrimSpace(runID)
	if runID == "" || (rating != 1 && rating != -1) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	patterns := s.loadLocked()
	changed := false
	for i := range patterns {
		if !macroContainsString(patterns[i].RunIDs, runID) {
			continue
		}
		if patterns[i].Ratings == nil {
			patterns[i].Ratings = map[string]int{}
		}
		previous := patterns[i].Ratings[runID]
		if previous == rating {
			continue
		}
		if previous == 1 {
			patterns[i].Helpful--
		}
		if previous == -1 {
			patterns[i].Unhelpful--
		}
		if rating == 1 {
			patterns[i].Helpful++
		} else {
			patterns[i].Unhelpful++
		}
		patterns[i].Ratings[runID] = rating
		patterns[i].UpdatedAt = time.Now().UTC()
		changed = true
	}
	if !changed {
		return nil
	}
	return s.writeLocked(patterns)
}

func macroContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (s *MacroStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	patterns := s.loadLocked()
	out := patterns[:0]
	for _, pattern := range patterns {
		if pattern.ID != id {
			out = append(out, pattern)
		}
	}
	return s.writeLocked(out)
}

func (s *MacroStore) loadLocked() []WorkflowPattern {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	var patterns []WorkflowPattern
	if json.Unmarshal(data, &patterns) != nil {
		return nil
	}
	return patterns
}

func (s *MacroStore) writeLocked(patterns []WorkflowPattern) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(patterns, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".studio-macros-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.path)
}

func workflowPatternID(pattern WorkflowPattern) string {
	structure, _ := json.Marshal(pattern.Structure)
	sum := sha256.Sum256([]byte(strings.ToLower(strings.Join(pattern.Tools, "\x00") + "\x00" + strings.Join(pattern.Branches, "\x00") + "\x00" + string(structure))))
	return hex.EncodeToString(sum[:10])
}

func WorkflowPatternsPromptBlock(patterns []WorkflowPattern) string {
	if len(patterns) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nPROVEN WORKFLOW PATTERNS — structural recipes distilled from successful runs. Adapt them to the current intent; never invent stored payloads:\n")
	for _, pattern := range patterns {
		b.WriteString("- Intent family: ")
		b.WriteString(learnedData(pattern.Intent))
		b.WriteString("; tool sequence: ")
		b.WriteString(strings.Join(pattern.Tools, " -> "))
		if len(pattern.Branches) > 0 {
			b.WriteString("; branches: ")
			b.WriteString(strings.Join(pattern.Branches, ", "))
		}
		if len(pattern.Structure) > 0 {
			b.WriteString("; structural steps: ")
			for i, step := range pattern.Structure {
				if i > 0 {
					b.WriteString(" -> ")
				}
				b.WriteString(step.Kind + ":" + step.Name)
				if step.Branch != "" {
					b.WriteString("[branch=" + step.Branch + "]")
				}
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

type distillationRun struct {
	intent    string
	tools     []string
	branches  []string
	structure []WorkflowStep
	runID     string
}

// WorkflowDistiller observes the action stream and dispatches persistence in a
// background worker only after a successful multi-tool terminal event.
type WorkflowDistiller struct {
	store *MacroStore
	mu    sync.Mutex
	runs  map[string]*distillationRun
	wg    sync.WaitGroup
	jobs  chan WorkflowPattern
}

func NewWorkflowDistiller(store *MacroStore) *WorkflowDistiller {
	d := &WorkflowDistiller{store: store, runs: make(map[string]*distillationRun), jobs: make(chan WorkflowPattern, 256)}
	go func() {
		for pattern := range d.jobs {
			_ = d.store.Add(pattern)
			d.wg.Done()
		}
	}()
	return d
}

func (d *WorkflowDistiller) Observe(event message.Event) {
	if d == nil || d.store == nil || event.AgentID == "" || event.SessionID == "" {
		return
	}
	key := event.AgentID + "\x00" + event.SessionID
	d.mu.Lock()
	run := d.runs[key]
	if run == nil {
		run = &distillationRun{}
		d.runs[key] = run
	}
	switch event.Type {
	case "message.in":
		run.intent = eventText(event.Payload)
	case "tool.call":
		if tool := eventToolName(event.Payload); tool != "" {
			run.tools = append(run.tools, tool)
			run.structure = append(run.structure, WorkflowStep{Kind: "tool", Name: tool})
		}
	case "flow.node":
		if step := eventWorkflowStep(event.Payload); step.Name != "" {
			run.structure = append(run.structure, step)
			if step.Branch != "" {
				run.branches = append(run.branches, step.Branch)
			}
		}
	}
	if event.Type != "run.completed" {
		d.mu.Unlock()
		return
	}
	delete(d.runs, key)
	snapshot := *run
	snapshot.tools = append([]string(nil), run.tools...)
	snapshot.branches = append([]string(nil), run.branches...)
	snapshot.structure = append([]WorkflowStep(nil), run.structure...)
	d.mu.Unlock()
	runID, success := eventRunCompletion(event.Payload)
	snapshot.runID = runID
	if !success || len(snapshot.tools) < 2 || strings.TrimSpace(snapshot.intent) == "" {
		return
	}
	pattern := WorkflowPattern{Intent: sanitizeIntent(snapshot.intent), Tools: snapshot.tools, Branches: snapshot.branches, Structure: snapshot.structure, RunIDs: []string{snapshot.runID}}
	d.wg.Add(1)
	select {
	case d.jobs <- pattern:
	default:
		// Preserve learning without spawning unbounded goroutines. Backpressure is
		// only possible after 256 completed runs are already queued.
		_ = d.store.Add(pattern)
		d.wg.Done()
	}
}

func (d *WorkflowDistiller) Wait()  { d.wg.Wait() }
func (d *WorkflowDistiller) Close() { d.Wait(); close(d.jobs) }

func eventToolName(payload any) string {
	if call, ok := payload.(message.ToolCall); ok {
		return strings.TrimSpace(call.Name)
	}
	data, _ := json.Marshal(payload)
	var value struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(data, &value)
	return strings.TrimSpace(value.Name)
}

func eventText(payload any) string {
	if msg, ok := payload.(message.Message); ok {
		var b strings.Builder
		for _, part := range msg.Parts {
			if part.Text != "" {
				b.WriteString(part.Text)
				b.WriteByte(' ')
			}
		}
		return strings.TrimSpace(b.String())
	}
	data, _ := json.Marshal(payload)
	var value struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(data, &value)
	return strings.TrimSpace(value.Text)
}

func eventRunCompletion(payload any) (string, bool) {
	data, _ := json.Marshal(payload)
	var value struct {
		RunID   string `json:"run_id"`
		Success bool   `json:"success"`
	}
	if json.Unmarshal(data, &value) != nil {
		return "", false
	}
	return value.RunID, value.Success
}

func eventWorkflowStep(payload any) WorkflowStep {
	data, _ := json.Marshal(payload)
	var value struct {
		NodeID        string `json:"nodeId"`
		Kind          string `json:"kind"`
		Branch        string `json:"branchId"`
		ParallelGroup string `json:"parallelGroup"`
	}
	_ = json.Unmarshal(data, &value)
	return WorkflowStep{Kind: strings.TrimSpace(value.Kind), Name: strings.TrimSpace(value.NodeID), Branch: strings.TrimSpace(value.Branch), ParallelGroup: strings.TrimSpace(value.ParallelGroup)}
}

func sanitizeIntent(intent string) string {
	intent = strings.Join(strings.Fields(intent), " ")
	safe, _ := sanitizeLearnedText(intent, 500)
	return safe
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || (len(out) > 0 && out[len(out)-1] == value) {
			continue
		}
		out = append(out, value)
	}
	return out
}

func semanticTerms(text string) map[string]bool {
	returnFields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	out := make(map[string]bool, len(returnFields))
	for _, term := range returnFields {
		if len(term) >= 3 {
			out[term] = true
		}
	}
	return out
}

func jaccard(a, b map[string]bool) float64 {
	intersection := 0
	for term := range a {
		if b[term] {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}
