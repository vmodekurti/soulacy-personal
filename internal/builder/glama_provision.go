package builder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// GlamaProvisionSpec is the resolved, install-ready spec for one Glama MCP server.
// Command and Args are filled in so the caller can write them directly to config.yaml.
type GlamaProvisionSpec struct {
	ID           string       `json:"id"` // suggested config key (safe slug)
	Name         string       `json:"name"`
	Description  string       `json:"description"`
	SetupSteps   []string     `json:"setup_steps,omitempty"`
	Transport    string       `json:"transport,omitempty"` // stdio (default) or http
	Command      string       `json:"command"`             // e.g. "npx"
	Args         []string     `json:"args"`                // e.g. ["-y", "icloud-mcp"]
	URL          string       `json:"url,omitempty"`       // http transport endpoint
	RegistryURL  string       `json:"registry_url"`
	EnvSchema    []EnvVarSpec `json:"env_schema"`              // required/optional env vars
	HeaderSchema []EnvVarSpec `json:"header_schema,omitempty"` // required/optional HTTP headers
	URLVariables []EnvVarSpec `json:"url_variables,omitempty"` // variables in URL templates
}

// EnvVarSpec describes one environment variable expected by the MCP server.
type EnvVarSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// FetchGlamaServer fetches full details for one Glama server and returns a
// provisioning spec with command/args and env var schema.
//
// slug is either "namespace/server" (e.g. "adamzaidi/icloud-mcp") or just the
// Glama server ID. A full Glama URL is also accepted — only the path suffix
// after "/mcp/servers/" is used.
func FetchGlamaServer(slug string) (*GlamaProvisionSpec, error) {
	// Strip leading URL boilerplate so callers can paste the Glama page URL.
	const glamaPrefix = "glama.ai/mcp/servers/"
	if idx := strings.Index(slug, glamaPrefix); idx != -1 {
		slug = slug[idx+len(glamaPrefix):]
	}
	slug = strings.TrimPrefix(slug, "/")
	slug = strings.TrimSuffix(slug, "/")
	if slug == "" {
		return nil, fmt.Errorf("empty server slug")
	}

	endpoint := fmt.Sprintf("%s/%s", glamaSearchURL, slug)

	ctx, cancel := context.WithTimeout(context.Background(), glamaTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "soulacy-builder/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("glama API returned %d for %q", resp.StatusCode, slug)
	}

	var raw struct {
		Name        string `json:"name"`
		Namespace   string `json:"namespace"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
		URL         string `json:"url"` // canonical glama.ai URL
		Repository  struct {
			URL string `json:"url"`
		} `json:"repository"`
		Attributes                     []string `json:"attributes"` // e.g. ["hosting:local-only"]
		EnvironmentVariablesJSONSchema *struct {
			Properties map[string]struct {
				Description string `json:"description"`
				Type        string `json:"type"`
			} `json:"properties"`
			Required []string `json:"required"`
		} `json:"environmentVariablesJsonSchema"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode glama response: %w", err)
	}

	// Derive npm package name: slug without namespace prefix is usually the package name.
	packageName := raw.Slug
	if packageName == "" {
		packageName = slug[strings.LastIndex(slug, "/")+1:]
	}
	// If the repo is on npmjs.com, extract the package name from the URL.
	if strings.Contains(raw.Repository.URL, "npmjs.com/package/") {
		packageName = raw.Repository.URL[strings.LastIndex(raw.Repository.URL, "/")+1:]
	}

	// Build env schema from JSON Schema properties.
	var envSchema []EnvVarSpec
	if raw.EnvironmentVariablesJSONSchema != nil {
		requiredSet := map[string]bool{}
		for _, r := range raw.EnvironmentVariablesJSONSchema.Required {
			requiredSet[r] = true
		}
		for name, prop := range raw.EnvironmentVariablesJSONSchema.Properties {
			envSchema = append(envSchema, EnvVarSpec{
				Name:        name,
				Description: prop.Description,
				Required:    requiredSet[name],
			})
		}
	}

	id := slugify(raw.Name)
	if id == "" {
		id = packageName
	}

	registryURL := raw.URL
	if registryURL == "" {
		registryURL = fmt.Sprintf("https://glama.ai/mcp/servers/%s/%s", raw.Namespace, raw.Slug)
	}

	return &GlamaProvisionSpec{
		ID:          id,
		Name:        raw.Name,
		Description: raw.Description,
		Command:     "npx",
		Args:        []string{"-y", packageName},
		RegistryURL: registryURL,
		EnvSchema:   envSchema,
	}, nil
}
