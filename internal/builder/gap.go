package builder

import "sync"

// CapabilityGap describes a single capability that the agent spec requires
// but that is not satisfied by any currently-installed tool.
type CapabilityGap struct {
	Required    string   `json:"required"`    // capability label from the spec (e.g. "web search")
	Available   []string `json:"available"`   // currently installed tools that partially satisfy it
	Suggestions []MCPRef `json:"suggestions"` // MCP tools from the registry that would fill the gap
}

// MCPRef is a reference to an entry in the offline MCP registry or a live registry (Glama).
type MCPRef struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	InstallCmd  string `json:"install_cmd,omitempty"`  // e.g. "npx -y @modelcontextprotocol/server-brave-search"
	RegistryURL string `json:"registry_url,omitempty"` // link to the server's page on glama.ai or similar
}

// GapAnalyzer compares an agent spec's declared capabilities against the live
// tool catalog and the offline MCP registry.
type GapAnalyzer struct {
	catalog *Registry
}

// NewGapAnalyzer creates a GapAnalyzer backed by the given Registry.
func NewGapAnalyzer(r *Registry) *GapAnalyzer {
	return &GapAnalyzer{catalog: r}
}

// Analyze returns the list of gaps for the given capability labels.
// capabilityLabels are free-form strings extracted from the agent spec
// (e.g. ["web search", "send telegram", "read PDF"]).
//
// For each gap it fans out to both the offline registry and the live Glama
// registry concurrently, merges results (deduplicating by ID), and returns
// up to 5 suggestions ranked by source (offline first, then Glama live).
func (a *GapAnalyzer) Analyze(capabilityLabels []string) []CapabilityGap {
	var gaps []CapabilityGap
	for _, label := range capabilityLabels {
		matches := a.catalog.Search(label)
		if len(matches) > 0 {
			// At least one installed tool satisfies this capability — no gap.
			continue
		}

		// Fan out: offline registry + Glama live search in parallel.
		var (
			wg      sync.WaitGroup
			mu      sync.Mutex
			offline []MCPRef
			live    []MCPRef
		)

		wg.Add(2)
		go func() {
			defer wg.Done()
			r := SearchOffline(label)
			mu.Lock()
			offline = r
			mu.Unlock()
		}()
		go func() {
			defer wg.Done()
			r := SearchGlama(label)
			mu.Lock()
			live = r
			mu.Unlock()
		}()
		wg.Wait()

		// Merge: offline first, then Glama results not already present.
		seen := map[string]bool{}
		merged := make([]MCPRef, 0, len(offline)+len(live))
		for _, r := range offline {
			if !seen[r.ID] {
				seen[r.ID] = true
				merged = append(merged, r)
			}
		}
		for _, r := range live {
			if !seen[r.ID] {
				seen[r.ID] = true
				merged = append(merged, r)
			}
		}
		if len(merged) > 5 {
			merged = merged[:5]
		}
		if merged == nil {
			merged = []MCPRef{}
		}

		gaps = append(gaps, CapabilityGap{
			Required:    label,
			Available:   []string{},
			Suggestions: merged,
		})
	}
	return gaps
}
