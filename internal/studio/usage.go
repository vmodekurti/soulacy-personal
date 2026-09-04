package studio

import (
	"encoding/json"
	"strings"
)

// usedSkills returns the distinct skill names a flow loads via read_skill tool
// nodes (input {"skill_name":"..."}), in first-seen order. Studio agents must
// carry these in their Definition.Skills, otherwise the engine treats skills as
// disabled and the read_skill node can't resolve — a real correctness fix plus
// the basis for the "which skills, and why" explanation (Story #5).
func usedSkills(flow Flow) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range flow.Nodes {
		if strings.TrimSpace(n.Tool) != "read_skill" {
			continue
		}
		name := skillNameFromInput(n.Input)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// skillNameFromInput pulls skill_name out of a read_skill node's input, which is
// a JSON object template like {"skill_name":"yfinance"}.
func skillNameFromInput(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(input), &m); err == nil {
		if v, ok := m["skill_name"].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// recommendKBs returns the knowledge bases from the catalog whose subject
// overlaps the intent, so Studio can suggest attaching them (Story #7) without
// pulling in unrelated KBs. Pure + deterministic; capped at 3.
func recommendKBs(intent string, cat Catalog) []string {
	if len(cat.KnowledgeBases) == 0 {
		return nil
	}
	terms := tokenize(intent)
	li := strings.ToLower(intent)
	type sc struct {
		name  string
		score int
	}
	var ranked []sc
	for _, kb := range cat.KnowledgeBases {
		score := overlap(terms, tokenize(kb.Name+" "+kb.Description))
		if nameMentioned(li, kb.Name) {
			score += 100
		}
		if score > 0 {
			ranked = append(ranked, sc{kb.Name, score})
		}
	}
	// stable sort by score desc
	for i := 1; i < len(ranked); i++ {
		for j := i; j > 0 && ranked[j].score > ranked[j-1].score; j-- {
			ranked[j], ranked[j-1] = ranked[j-1], ranked[j]
		}
	}
	var out []string
	for i := 0; i < len(ranked) && i < 3; i++ {
		out = append(out, ranked[i].name)
	}
	return out
}
