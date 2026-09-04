package builder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	glamaSearchURL = "https://glama.ai/api/mcp/v1/servers"
	glamaTimeout   = 4 * time.Second
	glamaMaxResult = 5
)

// glamaServer is the shape returned by the Glama search API.
type glamaServer struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Repository  struct {
		URL string `json:"url"`
	} `json:"repository"`
	Attributes []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"attributes"`
}

// glamaResponse wraps the top-level Glama API response.
type glamaResponse struct {
	Servers []glamaServer `json:"servers"`
}

// SearchGlama queries the live Glama MCP registry for servers matching query.
// It returns up to glamaMaxResult results. On any network or parse error it
// returns nil silently — the caller falls back to the offline registry.
func SearchGlama(query string) []MCPRef {
	ctx, cancel := context.WithTimeout(context.Background(), glamaTimeout)
	defer cancel()

	endpoint := fmt.Sprintf("%s?q=%s&limit=%d", glamaSearchURL, url.QueryEscape(query), glamaMaxResult)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "soulacy-builder/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var gr glamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return nil
	}

	var refs []MCPRef
	for _, s := range gr.Servers {
		if len(refs) >= glamaMaxResult {
			break
		}
		id := s.ID
		if id == "" {
			id = slugify(s.Name)
		}

		// Build an install command from available attributes or repo URL.
		installCmd := extractInstallCmd(s)

		refs = append(refs, MCPRef{
			ID:          id,
			Name:        s.Name,
			Description: s.Description,
			InstallCmd:  installCmd,
			RegistryURL: fmt.Sprintf("https://glama.ai/mcp/servers/%s", id),
		})
	}
	return refs
}

// extractInstallCmd tries to derive an npx install command from server attributes.
func extractInstallCmd(s glamaServer) string {
	for _, a := range s.Attributes {
		if strings.EqualFold(a.Name, "install_cmd") || strings.EqualFold(a.Name, "npm_package") {
			return "npx -y " + a.Value
		}
	}
	// Fall back: if the repo is an npm package URL we can derive npx from it.
	if strings.Contains(s.Repository.URL, "npmjs.com/package/") {
		pkg := s.Repository.URL[strings.LastIndex(s.Repository.URL, "/")+1:]
		return "npx -y " + pkg
	}
	// Last resort: clone from GitHub.
	if s.Repository.URL != "" {
		return "# see " + s.Repository.URL
	}
	return ""
}

// slugify converts a display name to a lowercase-dash identifier.
func slugify(name string) string {
	s := strings.ToLower(name)
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, s)
	// Collapse repeated dashes.
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}
