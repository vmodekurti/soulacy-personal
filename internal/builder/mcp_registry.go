// mcp_registry.go — client for the official MCP server registry at
// registry.modelcontextprotocol.io.
//
// The registry exposes two endpoints used here:
//
//	GET  /v0/servers?q=<query>&limit=<n>&cursor=<cursor>   — paginated search
//	GET  /v0/servers/<name>/versions/latest                 — server detail
//
// SearchMCPRegistry proxies the search endpoint for the GUI (avoiding CORS).
// FetchMCPRegistryServer fetches a server's installation spec and translates it
// into a GlamaProvisionSpec so the existing provision pipeline can install it
// without any new save/hot-connect logic.
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
	mcpRegistryBase    = "https://registry.modelcontextprotocol.io"
	mcpRegistryTimeout = 8 * time.Second
	mcpRegistryMax     = 20
)

// MCPRegistryServer is a summarized entry returned by SearchMCPRegistry.
// It flattens the registry's nested JSON into a flat view suitable for
// rendering in the GUI's server picker.
type MCPRegistryServer struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Publisher   string `json:"publisher"` // namespace before the slash in name (e.g. "io.modelcontextprotocol")
	Runtime     string `json:"runtime"`   // npm | pypi | docker | go (derived from first package)
}

// ── wire types ────────────────────────────────────────────────────────────────

type mcpRegListResponse struct {
	Servers []struct {
		// Official registry wraps each entry under a "server" key.
		Server *mcpRegEntry `json:"server"`
		// Flat format (some sub-registries):
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Version     string `json:"version"`
	} `json:"servers"`
	Metadata struct {
		NextCursor string `json:"nextCursor"`
	} `json:"metadata"`
}

type mcpRegEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
}

type mcpRegDetailResponse struct {
	Server      *mcpRegServerDetail `json:"server"`
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Title       string              `json:"title"`
	Description string              `json:"description"`
	Version     string              `json:"version"`
	Packages    []mcpRegPkg         `json:"packages"`
	Remotes     []mcpRegRemote      `json:"remotes"`
}

type mcpRegServerDetail struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Version     string         `json:"version"`
	Packages    []mcpRegPkg    `json:"packages"`
	Remotes     []mcpRegRemote `json:"remotes"`
}

type mcpRegPkg struct {
	RegistryName         string         `json:"registry_name"` // npm | pypi | docker | go
	RegistryType         string         `json:"registryType"`  // official server.json spelling
	Name                 string         `json:"name"`
	Identifier           string         `json:"identifier"`
	Version              string         `json:"version"`
	RuntimeArguments     []string       `json:"runtime_arguments"`
	EnvironmentVariables []mcpRegEnvVar `json:"environment_variables"`
}

type mcpRegRemote struct {
	Type      string                    `json:"type"` // streamable-http | sse
	URL       string                    `json:"url"`
	Variables map[string]mcpRegVariable `json:"variables"`
	Headers   []mcpRegEnvVar            `json:"headers"`
}

type mcpRegEnvVar struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	IsRequired  bool   `json:"isRequired"`
	IsSecret    bool   `json:"isSecret"`
}

type mcpRegVariable struct {
	Description string   `json:"description"`
	Required    bool     `json:"required"`
	IsRequired  bool     `json:"isRequired"`
	Default     string   `json:"default"`
	Choices     []string `json:"choices"`
}

// ── Search ────────────────────────────────────────────────────────────────────

// SearchMCPRegistry queries registry.modelcontextprotocol.io for MCP servers
// matching query. Pagination is cursor-based: pass cursor="" for the first
// page and use the returned nextCursor for subsequent pages (empty string
// indicates the last page). limit is capped at mcpRegistryMax (20).
func SearchMCPRegistry(query, cursor string, limit int) ([]MCPRegistryServer, string, error) {
	if limit <= 0 || limit > mcpRegistryMax {
		limit = mcpRegistryMax
	}

	endpoint := fmt.Sprintf("%s/v0/servers?limit=%d", mcpRegistryBase, limit)
	if query != "" {
		endpoint += "&q=" + url.QueryEscape(query)
	}
	if cursor != "" {
		endpoint += "&cursor=" + url.QueryEscape(cursor)
	}

	ctx, cancel := context.WithTimeout(context.Background(), mcpRegistryTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "soulacy/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("registry search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("registry returned HTTP %d", resp.StatusCode)
	}

	var body mcpRegListResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, "", fmt.Errorf("decode registry response: %w", err)
	}

	var out []MCPRegistryServer
	for _, item := range body.Servers {
		id, name, desc, ver := item.ID, item.Name, item.Description, item.Version
		if item.Server != nil {
			if item.Server.Name != "" {
				name = item.Server.Name
			}
			if item.Server.ID != "" {
				id = item.Server.ID
			}
			if item.Server.Description != "" {
				desc = item.Server.Description
			}
			if item.Server.Version != "" {
				ver = item.Server.Version
			}
		}
		if name == "" {
			continue
		}
		if id == "" {
			id = slugify(name)
		}
		out = append(out, MCPRegistryServer{
			ID:          id,
			Name:        name,
			Description: desc,
			Version:     ver,
			Publisher:   mcpPublisher(name),
		})
	}
	return out, body.Metadata.NextCursor, nil
}

// ── Provision ─────────────────────────────────────────────────────────────────

// FetchMCPRegistryServer fetches the latest published version of an MCP server
// from the official registry and translates it into a GlamaProvisionSpec.
// Reusing GlamaProvisionSpec means the existing provision pipeline
// (handleProvisionMCPRegistry) can save and hot-connect registry servers
// without any new storage or connection logic.
//
// The first package entry in the registry response is used to derive the
// install command; remaining packages contribute environment variable schemas.
func FetchMCPRegistryServer(name string) (*GlamaProvisionSpec, error) {
	encoded := url.PathEscape(name)
	endpoint := fmt.Sprintf("%s/v0/servers/%s/versions/latest", mcpRegistryBase, encoded)

	ctx, cancel := context.WithTimeout(context.Background(), mcpRegistryTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "soulacy/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("registry fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("server %q not found in official registry", name)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry returned HTTP %d for %q", resp.StatusCode, name)
	}

	var detail mcpRegDetailResponse
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		return nil, fmt.Errorf("decode registry detail: %w", err)
	}
	return mcpRegistrySpecFromDetail(name, detail)
}

func mcpRegistrySpecFromDetail(name string, detail mcpRegDetailResponse) (*GlamaProvisionSpec, error) {
	server := mcpRegServerDetail{
		ID:          detail.ID,
		Name:        detail.Name,
		Title:       detail.Title,
		Description: detail.Description,
		Version:     detail.Version,
		Packages:    detail.Packages,
		Remotes:     detail.Remotes,
	}
	if detail.Server != nil {
		server = *detail.Server
	}

	if len(server.Packages) == 0 && len(server.Remotes) == 0 {
		return nil, fmt.Errorf("server %q has neither installable packages nor remote endpoints in registry", name)
	}

	var envSchema []EnvVarSpec
	seen := map[string]bool{}
	for _, p := range server.Packages {
		for _, ev := range p.EnvironmentVariables {
			if seen[ev.Name] {
				continue
			}
			seen[ev.Name] = true
			envSchema = append(envSchema, EnvVarSpec{
				Name:        ev.Name,
				Description: ev.Description,
				Required:    ev.Required || ev.IsRequired,
			})
		}
	}

	displayName := server.Name
	if displayName == "" {
		displayName = name
	}
	id := slugify(displayName)
	if id == "" {
		id = slugify(name)
	}

	spec := &GlamaProvisionSpec{
		ID:          id,
		Name:        displayName,
		Description: server.Description,
		RegistryURL: fmt.Sprintf("%s/#/servers/%s", mcpRegistryBase, url.PathEscape(name)),
		EnvSchema:   envSchema,
	}

	if len(server.Packages) > 0 {
		pkg := selectMCPPackage(server.Packages)
		spec.Command, spec.Args = mcpRuntimeToCommand(pkg)
		spec.SetupSteps = []string{
			fmt.Sprintf("Soulacy will run `%s %s` as a local stdio MCP server.", spec.Command, strings.Join(spec.Args, " ")),
			"Any required environment variables are stored in config.yaml for this MCP server.",
			"After install, Soulacy will hot-connect the server and list its tools.",
		}
		return spec, nil
	}

	remote := selectMCPRemote(server.Remotes)
	if remote.URL == "" {
		return nil, fmt.Errorf("server %q remote endpoint is missing a URL", name)
	}
	spec.Transport = "http"
	spec.URL = remote.URL
	for key, variable := range remote.Variables {
		spec.URLVariables = append(spec.URLVariables, EnvVarSpec{
			Name:        key,
			Description: variable.Description,
			Required:    variable.Required || variable.IsRequired,
		})
	}
	for _, header := range remote.Headers {
		spec.HeaderSchema = append(spec.HeaderSchema, EnvVarSpec{
			Name:        header.Name,
			Description: header.Description,
			Required:    header.Required || header.IsRequired,
		})
	}
	spec.HeaderSchema = append(spec.HeaderSchema, inferredRemoteHeaders(remote.URL)...)
	spec.SetupSteps = []string{
		fmt.Sprintf("Soulacy will connect to the remote MCP endpoint `%s` over HTTP.", spec.URL),
		"Required headers or URL variables are collected before saving the server.",
		"After install, Soulacy will initialize the remote MCP session and fetch its tool list.",
	}
	return spec, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// mcpRuntimeToCommand maps a registry package entry to the (command, args)
// pair that will launch the MCP server as a stdio child process.
//
// Runtime-to-command mapping table:
//
//	npm    → npx -y <name> [runtime_arguments...]
//	pypi   → uvx <name> [runtime_arguments...]
//	docker → docker run -i --rm <name> [runtime_arguments...]
//	go     → go run <name> [runtime_arguments...]
//	other  → <name> [runtime_arguments...]  (treat name as executable)
//
// npx and uvx handle package installation automatically (the -y flag on npx
// suppresses the interactive install prompt). Docker is run with -i so the
// container's stdin/stdout are wired for the stdio MCP transport.
func mcpRuntimeToCommand(pkg mcpRegPkg) (command string, args []string) {
	registryName := pkg.RegistryName
	if registryName == "" {
		registryName = pkg.RegistryType
	}
	name := pkg.Name
	if name == "" {
		name = pkg.Identifier
	}
	extra := pkg.RuntimeArguments
	switch strings.ToLower(registryName) {
	case "npm":
		return "npx", append([]string{"-y", name}, extra...)
	case "pypi":
		return "uvx", append([]string{name}, extra...)
	case "docker", "oci":
		return "docker", append([]string{"run", "-i", "--rm", name}, extra...)
	case "go":
		return "go", append([]string{"run", name}, extra...)
	default:
		return name, extra
	}
}

func selectMCPPackage(pkgs []mcpRegPkg) mcpRegPkg {
	for _, preferred := range []string{"npm", "pypi", "docker", "oci", "go"} {
		for _, pkg := range pkgs {
			kind := pkg.RegistryName
			if kind == "" {
				kind = pkg.RegistryType
			}
			if strings.EqualFold(kind, preferred) {
				return pkg
			}
		}
	}
	return pkgs[0]
}

func selectMCPRemote(remotes []mcpRegRemote) mcpRegRemote {
	for _, preferred := range []string{"streamable-http", "http"} {
		for _, remote := range remotes {
			if strings.EqualFold(remote.Type, preferred) && strings.Contains(strings.ToLower(remote.URL), "/mcp") {
				return remote
			}
		}
		for _, remote := range remotes {
			if strings.EqualFold(remote.Type, preferred) {
				return remote
			}
		}
	}
	return remotes[0]
}

func inferredRemoteHeaders(rawURL string) []EnvVarSpec {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	if host == "api.inference.sh" || host == "sh.inference.ac" || strings.HasSuffix(host, ".inference.sh") {
		return []EnvVarSpec{{
			Name:        "Authorization",
			Description: "Bearer inference.sh API key, for example: Bearer inf_...",
			Required:    true,
		}}
	}
	return nil
}

func mcpPublisher(name string) string {
	if i := strings.Index(name, "/"); i > 0 {
		return name[:i]
	}
	return ""
}
