package mcpinstall

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/soulacy/soulacy/internal/netguard"
	"github.com/soulacy/soulacy/internal/plugininstall"
	"gopkg.in/yaml.v3"
)

// Method identifies where an MCP server should run. It is deliberately about
// execution placement rather than protocol: both a companion and a hosted
// server are registered in Soulacy over HTTP, but they have different setup
// and lifecycle requirements.
type Method string

const (
	MethodRemote    Method = "remote"
	MethodGateway   Method = "gateway_process"
	MethodRunner    Method = "connected_runner"
	MethodCompanion Method = "companion_deployment"
	MethodReview    Method = "manual_review"
)

type Recommendation struct {
	Source         string   `json:"source"`
	Name           string   `json:"name,omitempty"`
	Method         Method   `json:"method"`
	Title          string   `json:"title"`
	Summary        string   `json:"summary"`
	Reasons        []string `json:"reasons"`
	Steps          []string `json:"steps"`
	Command        string   `json:"command,omitempty"`
	Endpoint       string   `json:"endpoint,omitempty"`
	CanInstallHere bool     `json:"can_install_here"`
	Alternative    string   `json:"alternative,omitempty"`
	Evidence       Evidence `json:"evidence"`
}

// Evidence is the bounded repository material supplied to the System agent.
// READMEExcerpt is explicitly untrusted documentation: it informs planning but
// is never executed or treated as an instruction to bypass Soulacy policy.
type Evidence struct {
	Files                []string `json:"files"`
	Runtimes             []string `json:"runtimes,omitempty"`
	Requirements         []string `json:"requirements,omitempty"`
	EnvironmentVariables []string `json:"environment_variables,omitempty"`
	ComposeServices      []string `json:"compose_services,omitempty"`
	ContainerImage       string   `json:"container_image,omitempty"`
	READMEExcerpt        string   `json:"readme_excerpt,omitempty"`
}

type serverManifest struct {
	Name     string `json:"name"`
	Packages []struct {
		RegistryType         string `json:"registryType"`
		Identifier           string `json:"identifier"`
		EnvironmentVariables []struct {
			Name string `json:"name"`
		} `json:"environmentVariables"`
	} `json:"packages"`
	Remotes []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"remotes"`
}

type composeFile struct {
	Services map[string]struct {
		Image string `yaml:"image"`
		Build any    `yaml:"build"`
	} `yaml:"services"`
}

var safeNamePattern = regexp.MustCompile(`[^a-z0-9_-]+`)

// AnalyzeRepository clones a public HTTPS Git repository into a temporary
// directory and derives an installation recommendation only from bounded,
// declarative project files. It never runs repository code or README commands.
func AnalyzeRepository(ctx context.Context, source string) (Recommendation, error) {
	source = strings.TrimSpace(source)
	u, err := url.ParseRequestURI(source)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return Recommendation{}, fmt.Errorf("source must be an HTTPS Git repository URL without embedded credentials")
	}
	if err := netguard.CheckPublic(source); err != nil {
		return Recommendation{}, fmt.Errorf("source failed the public-network check: %w", err)
	}
	probe, err := os.MkdirTemp("", "soulacy-mcp-guide-")
	if err != nil {
		return Recommendation{}, err
	}
	defer os.RemoveAll(probe)
	if err := plugininstall.GitClone(ctx, source, probe); err != nil {
		return Recommendation{}, fmt.Errorf("inspect source: %w", err)
	}
	return AnalyzeDirectory(probe, source)
}

// AnalyzeDirectory classifies an already checked-out repository. Keeping this
// pure makes the rules directly testable and reusable by API and agent paths.
func AnalyzeDirectory(root, source string) (Recommendation, error) {
	manifest := readManifest(filepath.Join(root, "server.json"))
	name := strings.TrimSpace(manifest.Name)
	if name == "" {
		name = repositoryName(source)
	}
	id := safeID(name)
	if id == "" {
		id = "mcp-server"
	}
	evidence := collectEvidence(root)

	if endpoint := publicRemote(manifest); endpoint != "" {
		return Recommendation{
			Source: source, Name: name, Method: MethodRemote,
			Title:    "Connect to the hosted MCP endpoint",
			Summary:  "This project publishes a remote MCP endpoint, so Soulacy only needs to register the URL. No package or companion service is required.",
			Reasons:  []string{"server.json publishes a non-local remote endpoint", "The remote provider owns runtime updates and availability"},
			Steps:    []string{"Obtain any API key required by the MCP provider", "Register the endpoint in Soulacy", "Test the connection and review the tools it exposes"},
			Command:  fmt.Sprintf("sy --gateway <SOULACY_URL> --api-key \"$SOULACY_API_KEY\" mcp add --name %s --transport http --url %s", id, endpoint),
			Endpoint: endpoint, Evidence: evidence,
		}, nil
	}

	text := repositoryEvidence(root)
	localReasons := localAccessReasons(name, text)
	if len(localReasons) > 0 {
		return Recommendation{
			Source: source, Name: name, Method: MethodRunner,
			Title:       "Run it on a connected device",
			Summary:     "This server needs resources on a user-controlled machine. A connected runner is the intended placement; until runner installation is available, expose the server's HTTP transport securely from that device.",
			Reasons:     localReasons,
			Steps:       []string{"Install the MCP server on the device that owns the required data or application", "If it supports HTTP, expose it on a private VPN, tunnel, or authenticated endpoint; if it is stdio-only, run a Soulacy gateway on that device as the bridge", "Register the reachable endpoint in Soulacy and verify the discovered tools"},
			Command:     fmt.Sprintf("sy --gateway <SOULACY_URL> --api-key \"$SOULACY_API_KEY\" mcp add --name %s --transport http --url <DEVICE_MCP_URL>", id),
			Alternative: "Automated connected-runner installation is not available in this release. If the server can use cloud data instead of device-local resources, deploy it as a companion service.", Evidence: evidence,
		}, nil
	}

	composeReasons := companionReasons(root, text)
	if len(composeReasons) > 0 {
		image := discoverContainerImage(root, text)
		steps := []string{"Create a separate service in the same cloud project as Soulacy", "Deploy the project's container image or Dockerfile and attach persistent storage for state", "Keep the service private, then register its Streamable HTTP /mcp endpoint in Soulacy"}
		if image != "" {
			steps[0] = "Create a separate service from " + image + " in the same cloud project as Soulacy"
		}
		return Recommendation{
			Source: source, Name: name, Method: MethodCompanion,
			Title:       "Deploy it as a companion service",
			Summary:     "This server has its own runtime or state dependencies. A separate service gives it durable storage, health checks, and independent upgrades.",
			Reasons:     composeReasons,
			Steps:       steps,
			Command:     fmt.Sprintf("sy --gateway <SOULACY_URL> --api-key \"$SOULACY_API_KEY\" mcp add --name %s --transport http --url http://<PRIVATE-SERVICE>:<PORT>/mcp", id),
			Alternative: fmt.Sprintf("For evaluation, force a gateway-local source install with: sy package install %s --kind mcp --allow-unverified", source), Evidence: evidence,
		}, nil
	}

	if gatewayRunnable(root, manifest) {
		reasons := []string{"The repository exposes a runnable Python or Node MCP package", "No dedicated multi-service or device-local requirement was detected"}
		return Recommendation{
			Source: source, Name: name, Method: MethodGateway,
			Title:          "Install in the Soulacy gateway",
			Summary:        "Soulacy can manage this server as a persistent local process and register its stdio transport automatically.",
			Reasons:        reasons,
			Steps:          []string{"Review and approve the source installation", "Let Soulacy create the isolated package environment in persistent storage", "Add any requested credentials in Secrets and verify the discovered tools"},
			Command:        fmt.Sprintf("sy --gateway <SOULACY_URL> --api-key \"$SOULACY_API_KEY\" package install %s --kind mcp --allow-unverified --allow-host-build", source),
			CanInstallHere: true, Evidence: evidence,
		}, nil
	}

	return Recommendation{
		Source: source, Name: name, Method: MethodReview,
		Title:       "Review the project setup",
		Summary:     "Soulacy could not find a hosted endpoint, a supported package entrypoint, or a complete container deployment definition.",
		Reasons:     []string{"The repository does not expose enough machine-readable installation metadata"},
		Steps:       []string{"Review the project's official deployment documentation", "Identify whether it runs over stdio or Streamable HTTP", "Add a package manifest, server.json remote, or container definition before installing"},
		Alternative: "Do not copy README shell commands into the gateway until their effects and persistence requirements are understood.", Evidence: evidence,
	}, nil
}

// PlanningText gives an LLM the baseline classification plus the repository
// facts needed to confirm or revise it. README content is fenced and labelled
// as untrusted so documentation cannot become an instruction channel.
func (r Recommendation) PlanningText() string {
	var b strings.Builder
	b.WriteString("Automated baseline:\n")
	b.WriteString(r.Text())
	b.WriteString("\n\nRepository evidence:\n")
	if len(r.Evidence.Files) > 0 {
		b.WriteString("- Top-level files: ")
		b.WriteString(strings.Join(r.Evidence.Files, ", "))
		b.WriteByte('\n')
	}
	if len(r.Evidence.Runtimes) > 0 {
		b.WriteString("- Detected runtimes: ")
		b.WriteString(strings.Join(r.Evidence.Runtimes, ", "))
		b.WriteByte('\n')
	}
	if len(r.Evidence.Requirements) > 0 {
		b.WriteString("- Runtime requirements: ")
		b.WriteString(strings.Join(r.Evidence.Requirements, ", "))
		b.WriteByte('\n')
	}
	if len(r.Evidence.EnvironmentVariables) > 0 {
		b.WriteString("- Referenced environment variables: ")
		b.WriteString(strings.Join(r.Evidence.EnvironmentVariables, ", "))
		b.WriteByte('\n')
	}
	if len(r.Evidence.ComposeServices) > 0 {
		b.WriteString("- Compose services: ")
		b.WriteString(strings.Join(r.Evidence.ComposeServices, ", "))
		b.WriteByte('\n')
	}
	if r.Evidence.ContainerImage != "" {
		b.WriteString("- Container image: ")
		b.WriteString(r.Evidence.ContainerImage)
		b.WriteByte('\n')
	}
	if r.Evidence.READMEExcerpt != "" {
		b.WriteString("\n<untrusted_repository_readme>\n")
		b.WriteString(r.Evidence.READMEExcerpt)
		b.WriteString("\n</untrusted_repository_readme>\n")
	}
	b.WriteString("\nDecide the final method from the evidence. Prefer a published remote endpoint; use a connected runner for device-local resources; use a companion service for containers, persistent state, native dependencies, or multiple services; use a gateway process only for a self-contained supported Python/Node package. If none is viable, name the missing requirement and the files or facts that led to that conclusion.")
	return strings.TrimSpace(b.String())
}

func (r Recommendation) Text() string {
	var b strings.Builder
	b.WriteString(r.Title)
	b.WriteString("\n\n")
	b.WriteString(r.Summary)
	if len(r.Reasons) > 0 {
		b.WriteString("\n\nWhy this method:\n")
		for _, reason := range r.Reasons {
			b.WriteString("- ")
			b.WriteString(reason)
			b.WriteByte('\n')
		}
	}
	if len(r.Steps) > 0 {
		b.WriteString("\nSteps:\n")
		for i, step := range r.Steps {
			fmt.Fprintf(&b, "%d. %s\n", i+1, step)
		}
	}
	if r.Command != "" {
		b.WriteString("\nCommand:\n")
		b.WriteString(r.Command)
		b.WriteByte('\n')
	}
	if r.Alternative != "" {
		b.WriteString("\nAlternative: ")
		b.WriteString(r.Alternative)
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func readManifest(path string) serverManifest {
	var manifest serverManifest
	b, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(b, &manifest)
	}
	return manifest
}

func publicRemote(manifest serverManifest) string {
	for _, remote := range manifest.Remotes {
		u, err := url.Parse(strings.TrimSpace(remote.URL))
		if err != nil || u.Scheme != "https" || u.Hostname() == "" {
			continue
		}
		host := strings.ToLower(u.Hostname())
		if host == "localhost" || host == "127.0.0.1" || host == "::1" {
			continue
		}
		return u.String()
	}
	return ""
}

func repositoryEvidence(root string) string {
	names := []string{"README.md", "README.MD", "readme.md", "Dockerfile", "docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml", "pyproject.toml", "package.json", "server.json"}
	var b strings.Builder
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		if len(data) > 256*1024 {
			data = data[:256*1024]
		}
		b.WriteByte('\n')
		b.Write(data)
	}
	return strings.ToLower(b.String())
}

func collectEvidence(root string) Evidence {
	evidence := Evidence{ComposeServices: composeServices(root)}
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		evidence.Files = append(evidence.Files, entry.Name())
	}
	sort.Strings(evidence.Files)
	if hasFile(root, "pyproject.toml") || hasFile(root, "requirements.txt") || hasFile(root, "mcp_server.py") {
		evidence.Runtimes = append(evidence.Runtimes, "python")
	}
	if hasFile(root, "package.json") {
		evidence.Runtimes = append(evidence.Runtimes, "node")
	}
	if hasFile(root, "Dockerfile") {
		evidence.Runtimes = append(evidence.Runtimes, "container")
	}
	if data, err := os.ReadFile(filepath.Join(root, "pyproject.toml")); err == nil {
		if match := regexp.MustCompile(`(?m)^requires-python\s*=\s*["']([^"']+)["']`).FindSubmatch(data); len(match) == 2 {
			evidence.Requirements = append(evidence.Requirements, "python "+string(match[1]))
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		var project struct {
			Engines map[string]string `json:"engines"`
		}
		if json.Unmarshal(data, &project) == nil {
			for runtime, version := range project.Engines {
				evidence.Requirements = append(evidence.Requirements, runtime+" "+version)
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "Dockerfile")); err == nil {
		for _, match := range regexp.MustCompile(`(?mi)^\s*EXPOSE\s+([0-9]+)`).FindAllSubmatch(data, -1) {
			if len(match) == 2 {
				evidence.Requirements = append(evidence.Requirements, "container port "+string(match[1]))
			}
		}
	}
	manifest := readManifest(filepath.Join(root, "server.json"))
	for _, pkg := range manifest.Packages {
		for _, variable := range pkg.EnvironmentVariables {
			evidence.EnvironmentVariables = append(evidence.EnvironmentVariables, variable.Name)
		}
	}
	for _, name := range []string{".env.example", ".env.sample"} {
		if data, err := os.ReadFile(filepath.Join(root, name)); err == nil {
			for _, match := range regexp.MustCompile(`(?m)^([A-Z][A-Z0-9_]*)\s*=`).FindAllSubmatch(data, -1) {
				if len(match) == 2 {
					evidence.EnvironmentVariables = append(evidence.EnvironmentVariables, string(match[1]))
				}
			}
		}
	}
	evidence.Requirements = unique(evidence.Requirements)
	sort.Strings(evidence.Requirements)
	evidence.EnvironmentVariables = unique(evidence.EnvironmentVariables)
	sort.Strings(evidence.EnvironmentVariables)
	if len(evidence.EnvironmentVariables) > 50 {
		evidence.EnvironmentVariables = evidence.EnvironmentVariables[:50]
	}
	for _, name := range []string{"README.md", "README.MD", "readme.md"} {
		if data, err := os.ReadFile(filepath.Join(root, name)); err == nil {
			if len(data) > 6*1024 {
				prefix := strings.ToValidUTF8(string(data[:6*1024]), "�")
				data = []byte(prefix + "\n… README truncated by Soulacy …")
			}
			evidence.READMEExcerpt = string(data)
			break
		}
	}
	evidence.ContainerImage = discoverContainerImage(root, repositoryEvidence(root))
	return evidence
}

func localAccessReasons(name, text string) []string {
	var reasons []string
	lowerName := strings.ToLower(name)
	if strings.Contains(lowerName, "filesystem") || strings.Contains(lowerName, "desktop") {
		reasons = append(reasons, "The server is designed to access files or applications on the machine where it runs")
	}
	patterns := []struct{ needle, reason string }{
		{"macos only", "The project declares a macOS-only runtime"},
		{"requires macos", "The project requires macOS"},
		{"requires a desktop", "The project requires a desktop session"},
		{"local filesystem", "The project is designed to access a local filesystem"},
		{"browser extension", "The project depends on a browser extension on the local device"},
	}
	for _, pattern := range patterns {
		if strings.Contains(text, pattern.needle) {
			reasons = append(reasons, pattern.reason)
		}
	}
	return unique(reasons)
}

func companionReasons(root, text string) []string {
	var reasons []string
	if services := composeServices(root); len(services) > 1 {
		reasons = append(reasons, fmt.Sprintf("The deployment definition contains %d cooperating services", len(services)))
	}
	if hasFile(root, "Dockerfile") && (strings.Contains(text, "streamable http") || strings.Contains(text, "transport=http") || strings.Contains(text, "transport http") || strings.Contains(text, "/mcp")) {
		reasons = append(reasons, "The project provides a containerized HTTP MCP runtime")
	}
	stateSignals := []string{"postgresql", "paradedb", "redis", "sqlite", "persistent volume", "database_url"}
	var state []string
	for _, signal := range stateSignals {
		if strings.Contains(text, signal) {
			state = append(state, signal)
		}
	}
	if len(state) >= 2 || (len(state) >= 1 && hasFile(root, "Dockerfile")) {
		sort.Strings(state)
		reasons = append(reasons, "The server declares durable or external state dependencies: "+strings.Join(state, ", "))
	}
	if hasFile(root, "Dockerfile") && !gatewayRunnable(root, readManifest(filepath.Join(root, "server.json"))) {
		reasons = append(reasons, "A container definition is available but no supported gateway-local package entrypoint was found")
	}
	return unique(reasons)
}

func composeServices(root string) []string {
	for _, name := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"} {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		var compose composeFile
		if yaml.Unmarshal(b, &compose) == nil {
			out := make([]string, 0, len(compose.Services))
			for service := range compose.Services {
				out = append(out, service)
			}
			sort.Strings(out)
			return out
		}
	}
	return nil
}

func discoverContainerImage(root, text string) string {
	manifest := readManifest(filepath.Join(root, "server.json"))
	for _, pkg := range manifest.Packages {
		kind := strings.ToLower(pkg.RegistryType)
		if (kind == "oci" || kind == "docker") && strings.TrimSpace(pkg.Identifier) != "" {
			return pkg.Identifier
		}
	}
	imagePattern := regexp.MustCompile(`(?m)(?:docker run\s+|image:\s*)(ghcr\.io/[a-z0-9_./-]+(?::[a-z0-9_.-]+)?)`)
	if match := imagePattern.FindStringSubmatch(text); len(match) == 2 {
		return match[1]
	}
	return ""
}

func gatewayRunnable(root string, manifest serverManifest) bool {
	if hasFile(root, "pyproject.toml") || hasFile(root, "package.json") || hasFile(root, "mcp_server.py") {
		return true
	}
	for _, pkg := range manifest.Packages {
		switch strings.ToLower(pkg.RegistryType) {
		case "npm", "pypi":
			return true
		}
	}
	return false
}

func repositoryName(source string) string {
	u, err := url.Parse(source)
	if err == nil {
		base := strings.TrimSuffix(filepath.Base(u.Path), ".git")
		if base != "." && base != "/" && base != "" {
			return base
		}
	}
	return "MCP server"
}

func safeID(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.Trim(safeNamePattern.ReplaceAllString(name, "-"), "-_")
	if len(name) > 64 {
		name = strings.TrimRight(name[:64], "-_")
	}
	return name
}

func hasFile(root, name string) bool {
	info, err := os.Stat(filepath.Join(root, name))
	return err == nil && !info.IsDir()
}

func unique(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
