package builder

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed offline_registry.json
var offlineRegistryJSON []byte

// offlineRegistry is the parsed offline MCP registry loaded at init time.
var offlineRegistry []MCPRef

func init() {
	_ = json.Unmarshal(offlineRegistryJSON, &offlineRegistry)
}

// SearchOffline returns MCPRef entries matching query (case-insensitive substring
// match on Name + Description). Returns up to 5 results.
func SearchOffline(query string) []MCPRef {
	q := strings.ToLower(query)
	var out []MCPRef
	for _, ref := range offlineRegistry {
		if strings.Contains(strings.ToLower(ref.Name), q) ||
			strings.Contains(strings.ToLower(ref.Description), q) {
			out = append(out, ref)
			if len(out) == 5 {
				break
			}
		}
	}
	return out
}
