package builder

import "strings"

// ToolEntry describes a single installed tool visible to agents.
type ToolEntry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Keywords    []string `json:"keywords"`
	Source      string   `json:"source"` // "python", "mcp", "builtin"
}

// Registry is the live tool catalog.
type Registry struct {
	tools []ToolEntry
}

func NewRegistry() *Registry {
	return &Registry{}
}

// Add adds a tool entry to the catalog.
func (r *Registry) Add(t ToolEntry) {
	r.tools = append(r.tools, t)
}

// All returns all registered tools.
func (r *Registry) All() []ToolEntry {
	out := make([]ToolEntry, len(r.tools))
	copy(out, r.tools)
	return out
}

// Search returns tools whose Name, Description, or Keywords contain query
// (case-insensitive).
func (r *Registry) Search(query string) []ToolEntry {
	q := strings.ToLower(query)
	var out []ToolEntry
	for _, t := range r.tools {
		if strings.Contains(strings.ToLower(t.Name), q) ||
			strings.Contains(strings.ToLower(t.Description), q) {
			out = append(out, t)
			continue
		}
		for _, kw := range t.Keywords {
			if strings.Contains(strings.ToLower(kw), q) {
				out = append(out, t)
				break
			}
		}
	}
	return out
}
