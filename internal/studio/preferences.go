package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// PreferenceObservation retains only user-authored additions to behavioral
// fields, not the full generated or saved agent definition.
type PreferenceObservation struct {
	Owner     string    `json:"owner,omitempty"`
	AgentID   string    `json:"agent_id"`
	Additions []string  `json:"additions"`
	CreatedAt time.Time `json:"created_at"`
}

type GlobalPreference struct {
	Owner     string    `json:"owner,omitempty"`
	ID        string    `json:"id"`
	Rule      string    `json:"rule"`
	Evidence  int       `json:"evidence"`
	UpdatedAt time.Time `json:"updated_at"`
}

type preferenceData struct {
	Observations []PreferenceObservation `json:"observations"`
	Rules        []GlobalPreference      `json:"rules"`
}

type PreferenceStore struct {
	path string
	mu   sync.Mutex
}

func NewPreferenceStore(path string) *PreferenceStore { return &PreferenceStore{path: path} }

func (s *PreferenceStore) AddObservation(observation PreferenceObservation) error {
	if strings.TrimSpace(observation.AgentID) == "" || len(observation.Additions) == 0 {
		return nil
	}
	var additions []string
	for _, addition := range observation.Additions {
		if safe, ok := sanitizeLearnedText(addition, 600); ok {
			additions = append(additions, safe)
		}
	}
	if len(additions) == 0 {
		return nil
	}
	observation.Additions = compactStrings(additions)
	observation.CreatedAt = time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	data := s.loadLocked()
	data.Observations = append(data.Observations, observation)
	if len(data.Observations) > 500 {
		data.Observations = data.Observations[len(data.Observations)-500:]
	}
	return s.writeLocked(data)
}

func (s *PreferenceStore) Rules() []GlobalPreference { return s.RulesFor("") }

func (s *PreferenceStore) RulesFor(owner string) []GlobalPreference {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []GlobalPreference
	for _, rule := range s.loadLocked().Rules {
		if strings.EqualFold(strings.TrimSpace(rule.Owner), strings.TrimSpace(owner)) {
			out = append(out, rule)
		}
	}
	return out
}

func (s *PreferenceStore) DeleteRule(owner, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data := s.loadLocked()
	out := data.Rules[:0]
	for _, rule := range data.Rules {
		if rule.ID != id || !strings.EqualFold(strings.TrimSpace(rule.Owner), strings.TrimSpace(owner)) {
			out = append(out, rule)
		}
	}
	data.Rules = out
	return s.writeLocked(data)
}

func (s *PreferenceStore) RepeatedCandidates(minAgents int) []string {
	return s.RepeatedCandidatesFor("", minAgents)
}

func (s *PreferenceStore) RepeatedCandidatesFor(owner string, minAgents int) []string {
	if minAgents < 2 {
		minAgents = 2
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data := s.loadLocked()
	type cluster struct {
		representative string
		agents         map[string]bool
	}
	var clusters []cluster
	for _, observation := range data.Observations {
		if !strings.EqualFold(strings.TrimSpace(observation.Owner), strings.TrimSpace(owner)) {
			continue
		}
		for _, addition := range observation.Additions {
			terms := semanticTerms(addition)
			matched := -1
			for i := range clusters {
				if jaccard(terms, semanticTerms(clusters[i].representative)) >= 0.55 {
					matched = i
					break
				}
			}
			if matched < 0 {
				clusters = append(clusters, cluster{representative: addition, agents: map[string]bool{observation.AgentID: true}})
			} else {
				clusters[matched].agents[observation.AgentID] = true
			}
		}
	}
	var out []string
	for _, cluster := range clusters {
		if len(cluster.agents) >= minAgents {
			alreadyLearned := false
			for _, rule := range data.Rules {
				if !strings.EqualFold(strings.TrimSpace(rule.Owner), strings.TrimSpace(owner)) {
					continue
				}
				if jaccard(semanticTerms(rule.Rule), semanticTerms(cluster.representative)) >= 0.55 {
					alreadyLearned = true
					break
				}
			}
			if !alreadyLearned {
				out = append(out, cluster.representative)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (s *PreferenceStore) MergeRules(rules []string, evidence int) error {
	return s.MergeRulesFor("", rules, evidence)
}

func (s *PreferenceStore) MergeRulesFor(owner string, rules []string, evidence int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data := s.loadLocked()
	now := time.Now().UTC()
	for _, rule := range compactStrings(rules) {
		var safe bool
		rule, safe = sanitizeLearnedText(rule, 400)
		if !safe || len(rule) < 8 {
			continue
		}
		id := preferenceID(rule)
		found := false
		for i := range data.Rules {
			if strings.EqualFold(strings.TrimSpace(data.Rules[i].Owner), strings.TrimSpace(owner)) && (data.Rules[i].ID == id || jaccard(semanticTerms(data.Rules[i].Rule), semanticTerms(rule)) >= 0.8) {
				if evidence > data.Rules[i].Evidence {
					data.Rules[i].Evidence = evidence
				}
				data.Rules[i].UpdatedAt = now
				found = true
				break
			}
		}
		if !found {
			data.Rules = append(data.Rules, GlobalPreference{Owner: owner, ID: id, Rule: rule, Evidence: evidence, UpdatedAt: now})
		}
	}
	return s.writeLocked(data)
}

func preferenceID(rule string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.Join(strings.Fields(rule), " "))))
	return hex.EncodeToString(sum[:10])
}

func (s *PreferenceStore) loadLocked() preferenceData {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return preferenceData{}
	}
	var value preferenceData
	if json.Unmarshal(data, &value) != nil {
		return preferenceData{}
	}
	return value
}

func (s *PreferenceStore) writeLocked(value preferenceData) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".studio-preferences-*")
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

// ManualPreferenceAdditions calculates a text diff over the user-editable Goal,
// Instructions, and system prompt surfaces and returns significant additions.
func ManualPreferenceAdditions(initial, final Draft) []string {
	// The authoritative diff is the same SOUL.yaml representation Save writes.
	// Fall back to the editable fields only for an incomplete draft that cannot
	// yet be converted to an agent definition.
	if before, ok := draftSOULYAML(initial); ok {
		if after, ok := draftSOULYAML(final); ok {
			// Calculate the complete SOUL.yaml diff as the authoritative change
			// detector, then deliberately retain only behavioral-field additions
			// for learning. Provider/tool/trigger changes are not global style.
			if len(ManualYAMLAdditions(before, after)) == 0 {
				return nil
			}
		}
	}
	return manualPreferenceFieldAdditions(initial, final)
}

func draftSOULYAML(draft Draft) (string, bool) {
	def, err := ToAgentDefinition(draft, true)
	if err != nil {
		return "", false
	}
	data, err := yaml.Marshal(def)
	return string(data), err == nil
}

// ManualYAMLAdditions is a line-level text diff: lines present only in the
// final SOUL.yaml are the extraction input. The LLM later removes agent-specific
// content and retains only repeated global behavior/style rules.
func ManualYAMLAdditions(initialYAML, finalYAML string) []string {
	seen := make(map[string]bool)
	for _, line := range normalizedLines(initialYAML) {
		seen[strings.ToLower(line)] = true
	}
	var out []string
	for _, line := range normalizedLines(finalYAML) {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) >= 12 && !seen[strings.ToLower(trimmed)] {
			out = append(out, trimmed)
		}
	}
	return compactStrings(out)
}

func manualPreferenceFieldAdditions(initial, final Draft) []string {
	var beforeGoal, beforeInstructions string
	if initial.Policy != nil && initial.Policy.Contract != nil {
		beforeGoal = initial.Policy.Contract.Goal
		beforeInstructions = initial.Policy.Contract.Instructions
	}
	var afterGoal, afterInstructions string
	if final.Policy != nil && final.Policy.Contract != nil {
		afterGoal = final.Policy.Contract.Goal
		afterInstructions = final.Policy.Contract.Instructions
	}
	var out []string
	for _, pair := range [][2]string{{beforeGoal, afterGoal}, {beforeInstructions, afterInstructions}, {initial.SystemPrompt, final.SystemPrompt}} {
		before := normalizedLines(pair[0])
		seen := make(map[string]bool, len(before))
		for _, line := range before {
			seen[strings.ToLower(line)] = true
		}
		for _, line := range normalizedLines(pair[1]) {
			if len(line) >= 12 && !seen[strings.ToLower(line)] {
				out = append(out, line)
			}
		}
	}
	return compactStrings(out)
}

func normalizedLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	parts := strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == ';' })
	var out []string
	for _, part := range parts {
		if line := strings.TrimSpace(part); line != "" {
			out = append(out, strings.Join(strings.Fields(line), " "))
		}
	}
	return out
}

type PreferenceMiner struct {
	store *PreferenceStore
	model LLM
}

func NewPreferenceMiner(store *PreferenceStore, model LLM) *PreferenceMiner {
	return &PreferenceMiner{store: store, model: model}
}

// Mine is intended to run in a background worker after a successful save.
func (m *PreferenceMiner) Mine(ctx context.Context, agentID string, initial, final Draft) error {
	return m.MineFor(ctx, "", agentID, initial, final)
}

func (m *PreferenceMiner) MineFor(ctx context.Context, owner, agentID string, initial, final Draft) error {
	if m == nil || m.store == nil {
		return nil
	}
	additions := ManualPreferenceAdditions(initial, final)
	if len(additions) == 0 {
		return nil
	}
	if err := m.store.AddObservation(PreferenceObservation{Owner: owner, AgentID: agentID, Additions: additions}); err != nil {
		return err
	}
	candidates := m.store.RepeatedCandidatesFor(owner, 2)
	if len(candidates) == 0 {
		return nil
	}
	// Raw edits are evidence, not safe global instructions. If extraction is
	// unavailable or malformed, retain observations for a later attempt rather
	// than promoting agent-specific text directly into every future prompt.
	if m.model == nil {
		return nil
	}
	payload, _ := json.Marshal(candidates)
	prompt := "Extract durable global user behavior/style preferences shared by these manual agent edits. Return JSON only as {\"rules\":[\"concise imperative rule\"]}. Do not include agent-specific goals, destinations, tools, or data. Edits: " + string(payload)
	raw, err := m.model.Complete(ctx, prompt)
	if err != nil {
		return nil
	}
	var response struct {
		Rules []string `json:"rules"`
	}
	clean := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(raw), "```json"), "```"))
	if json.Unmarshal([]byte(clean), &response) != nil || len(response.Rules) == 0 {
		return nil
	}
	rules := response.Rules
	return m.store.MergeRulesFor(owner, rules, 2)
}

func GlobalPreferencesPromptBlock(rules []GlobalPreference) string {
	if len(rules) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nGLOBAL USER PREFERENCES — repeated manual edits established these defaults. Apply them unless the current request conflicts:\n")
	for _, rule := range rules {
		b.WriteString("- ")
		b.WriteString(learnedData(rule.Rule))
		b.WriteByte('\n')
	}
	return b.String()
}
