package gateway

// Workspace MCP installation is deliberately two-phase. Inspection resolves a
// moving Git URL to a commit and derives a closed execution plan. Approval is
// bound to the workspace, actor and plan fingerprint; it cannot be repurposed
// to install a different image or command.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/pelletier/go-toml/v2"

	"github.com/soulacy/soulacy/internal/dockerutil"
	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/internal/mcppackage"
	"github.com/soulacy/soulacy/internal/mcpstore"
	"github.com/soulacy/soulacy/internal/plugininstall"
	"github.com/soulacy/soulacy/internal/redact"
	"github.com/soulacy/soulacy/internal/wsroot"
)

const (
	mcpInstallStageTTL       = 15 * time.Minute
	mcpRepositoryScanTimeout = 2 * time.Minute
	mcpContainerPullTimeout  = 5 * time.Minute
	mcpContainerBuildTimeout = 12 * time.Minute
	mcpNodeRunnerImage       = "node:22-alpine"
	mcpPythonRunnerImage     = "ghcr.io/astral-sh/uv:python3.12-alpine"
)

type mcpInstallPermissions struct {
	Network   string `json:"network"`
	Workspace string `json:"workspace"`
}

type stagedMCPInstall struct {
	Token       string
	Fingerprint string
	WorkspaceID string
	Actor       string
	SourceURL   string
	Revision    string
	ServerID    string
	Name        string
	Description string
	Image       string
	Args        []string
	Runtime     string
	Permissions mcpInstallPermissions
	Env         []mcpInstallEnv
	Findings    []mcpInstallFinding
	ExpiresAt   time.Time
}

type mcpInstallEnv struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required"`
	Secret      bool   `json:"secret"`
}

type mcpInstallFinding struct {
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
}

var safeMCPEnvironmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func validMCPEnvironmentName(name string) bool {
	if !safeMCPEnvironmentName.MatchString(name) {
		return false
	}
	switch strings.ToUpper(name) {
	case "HOME", "PATH", "SHELL", "USER", "LOGNAME", "DOCKER_HOST", "LD_PRELOAD", "LD_LIBRARY_PATH", "XDG_CACHE_HOME", "XDG_CONFIG_HOME":
		return false
	default:
		return true
	}
}

type mcpInstallManifest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Homepage    string `json:"homepage"`
	Repository  struct {
		URL string `json:"url"`
	} `json:"repository"`
	Packages []struct {
		RegistryType     string   `json:"registryType"`
		Identifier       string   `json:"identifier"`
		Version          string   `json:"version"`
		RuntimeArguments []string `json:"runtimeArguments"`
		Transport        struct {
			Type string `json:"type"`
		} `json:"transport"`
		EnvironmentVariables []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			IsRequired  bool   `json:"isRequired"`
			IsSecret    bool   `json:"isSecret"`
		} `json:"environmentVariables"`
	} `json:"packages"`
}

var safeOCIReference = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:@-]{2,250}$`)
var immutableOCIReference = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:@-]*@sha256:[0-9a-f]{64}$`)
var immutableLocalImage = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func immutableContainerReference(value string) bool {
	value = strings.TrimSpace(value)
	return immutableOCIReference.MatchString(value) || immutableLocalImage.MatchString(value)
}

func (s *Server) handleInspectWorkspaceMCP(c *fiber.Ctx) error {
	var body struct {
		SourceURL   string                `json:"source_url"`
		Permissions mcpInstallPermissions `json:"permissions"`
	}
	if err := c.BodyParser(&body); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	source, err := canonicalGitHubRepository(body.SourceURL)
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}

	dir, err := os.MkdirTemp("", "soulacy-mcp-inspect-")
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(c.UserContext(), mcpRepositoryScanTimeout)
	defer cancel()
	revision, err := plugininstall.GitClone(ctx, source, filepath.Join(dir, "source"))
	if err != nil {
		return s.errMsg(c, fiber.StatusUnprocessableEntity, "repository inspection failed: "+err.Error())
	}
	stage, err := inspectMCPRepository(filepath.Join(dir, "source"), source, revision)
	if err != nil {
		return s.errMsg(c, fiber.StatusUnprocessableEntity, err.Error())
	}
	if stage.Runtime == "oci" && !ociManifestAvailable(ctx, stage.Image) {
		if fallback, fallbackErr := inspectPythonSourceRepository(filepath.Join(dir, "source"), source, revision); fallbackErr == nil {
			fallback.Env = stage.Env
			fallback.Findings = append([]mcpInstallFinding{{Severity: "warning", Title: "Declared artifact unavailable", Detail: "The repository's OCI tag is not published. Soulacy will build the locked Python source in a disposable builder instead."}}, fallback.Findings...)
			stage = fallback
		}
	}
	stage.Permissions, err = normalizeMCPInstallPermissions(body.Permissions)
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}
	if err := validateMCPRuntimePermissions(stage.Runtime, stage.Permissions); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}
	stage.Findings = append(stage.Findings, permissionFindings(stage.Permissions)...)
	stage.Token = randomInstallToken()
	stage.WorkspaceID = mcpWorkspace(c)
	stage.Actor = s.auditActor(c)
	stage.ExpiresAt = time.Now().UTC().Add(mcpInstallStageTTL)
	stage.Fingerprint = mcpInstallFingerprint(stage)

	s.mcpInstallMu.Lock()
	if s.mcpInstallStages == nil {
		s.mcpInstallStages = map[string]stagedMCPInstall{}
	}
	now := time.Now().UTC()
	for token, old := range s.mcpInstallStages {
		if now.After(old.ExpiresAt) {
			delete(s.mcpInstallStages, token)
		}
	}
	s.mcpInstallStages[stage.Token] = stage
	s.mcpInstallMu.Unlock()

	s.recordAdminAudit(c, "mcp.install.inspect", "mcp", stage.ServerID, "ok", map[string]any{
		"source_url": stage.SourceURL, "revision": stage.Revision, "fingerprint": stage.Fingerprint,
	})
	return c.JSON(fiber.Map{"ok": true, "approval_token": stage.Token, "report": installReport(stage)})
}

func validateMCPRuntimePermissions(runtime string, permissions mcpInstallPermissions) error {
	if (runtime == "npm" || runtime == "pypi" || runtime == "source-npm" || runtime == "source-python") && permissions.Network != "public" {
		return fmt.Errorf("%s MCP packages require Internet access so the exact package version can be loaded inside its disposable runner; choose Internet access or use a reviewed OCI image", runtime)
	}
	return nil
}

func (s *Server) handleApproveWorkspaceMCP(c *fiber.Ctx) error {
	if s.mcpServers == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace MCP servers are not available")
	}
	var body struct {
		ApprovalToken string            `json:"approval_token"`
		Fingerprint   string            `json:"fingerprint"`
		Settings      map[string]string `json:"settings"`
	}
	if err := c.BodyParser(&body); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	s.mcpInstallMu.Lock()
	stage, ok := s.mcpInstallStages[strings.TrimSpace(body.ApprovalToken)]
	s.mcpInstallMu.Unlock()
	if !ok || time.Now().UTC().After(stage.ExpiresAt) {
		return s.errMsg(c, fiber.StatusGone, "security review expired; inspect the repository again")
	}
	if stage.WorkspaceID != mcpWorkspace(c) || stage.Actor != s.auditActor(c) {
		return s.errMsg(c, fiber.StatusForbidden, "approval is bound to the inspecting workspace administrator")
	}
	if body.Fingerprint == "" || body.Fingerprint != stage.Fingerprint {
		return s.errMsg(c, fiber.StatusConflict, "security report changed; inspect the repository again")
	}

	timeout := mcpContainerPullTimeout
	if stage.Runtime == "source-npm" || stage.Runtime == "source-python" {
		timeout = mcpContainerBuildTimeout
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), timeout)
	defer cancel()
	var pinned string
	var err error
	if stage.Runtime == "source-npm" || stage.Runtime == "source-python" {
		pinned, err = buildAndPinSource(ctx, stage)
	} else {
		pinned, err = pullAndPinOCI(ctx, stage.Image)
	}
	if err != nil {
		s.recordAdminAudit(c, "mcp.install.approve", "mcp", stage.ServerID, "failed", map[string]any{"reason": err.Error()})
		return s.errMsg(c, fiber.StatusBadGateway, "container image could not be installed: "+err.Error())
	}
	env, secretCount, err := s.storeMCPInstallSettings(c.UserContext(), stage, body.Settings)
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}
	if err := s.mcpServers.Put(c.UserContext(), mcpstore.Server{
		WorkspaceID:        stage.WorkspaceID,
		ID:                 stage.ServerID,
		Transport:          "container",
		Command:            pinned,
		Args:               append([]string(nil), stage.Args...),
		Env:                env,
		ContainerNetwork:   stage.Permissions.Network,
		ContainerWorkspace: stage.Permissions.Workspace,
		Environment:        storedMCPEnvironment(stage.Env),
		CreatedBy:          stage.Actor,
	}); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.mcpInstallMu.Lock()
	delete(s.mcpInstallStages, stage.Token)
	s.mcpInstallMu.Unlock()
	s.invalidateMCPWorkspace(stage.WorkspaceID)
	s.recordAdminAudit(c, "mcp.install.approve", "mcp", stage.ServerID, "ok", map[string]any{
		"source_url": stage.SourceURL, "revision": stage.Revision, "image": pinned, "fingerprint": stage.Fingerprint,
		"network": stage.Permissions.Network, "workspace_access": stage.Permissions.Workspace, "secrets_stored": secretCount,
	})
	return c.JSON(fiber.Map{"ok": true, "id": stage.ServerID, "image": pinned, "message": "MCP server installed. Its discovered tools are now available in Studio and to agents granted this server."})
}

func storedMCPEnvironment(items []mcpInstallEnv) []mcpstore.EnvironmentItem {
	out := make([]mcpstore.EnvironmentItem, 0, len(items))
	for _, item := range items {
		out = append(out, mcpstore.EnvironmentItem{
			Name: item.Name, Description: item.Description, Required: item.Required, Secret: item.Secret,
		})
	}
	return out
}

func canonicalGitHubRepository(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Scheme, "https") || !strings.EqualFold(u.Hostname(), "github.com") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Contains(u.EscapedPath(), "%") {
		return "", fmt.Errorf("enter a public HTTPS GitHub repository URL")
	}
	path := strings.Trim(u.Path, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(strings.Join(parts, ""), "\\\x00") {
		return "", fmt.Errorf("GitHub URL must identify one owner/repository")
	}
	return "https://github.com/" + parts[0] + "/" + parts[1], nil
}

func inspectMCPRepository(root, source, revision string) (stagedMCPInstall, error) {
	manifestPath := filepath.Join(root, "server.json")
	data, err := readInstallFile(manifestPath, 1<<20)
	if err != nil {
		return inspectNPMSourceRepository(root, source, revision)
	}
	var manifest mcpInstallManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return stagedMCPInstall{}, fmt.Errorf("invalid server.json: %w", err)
	}
	if !manifestRepositoryMatches(manifest, source) {
		return stagedMCPInstall{}, fmt.Errorf("server.json repository identity does not match the requested repository")
	}
	stage := stagedMCPInstall{SourceURL: source, Revision: revision, Name: manifest.Name, Description: manifest.Description}
	stage.ServerID = safeMCPInstallID(manifest.Name)
	var stdioPackage string
	var packageFallback *struct {
		registry, identifier, version string
		args                          []string
	}
	for _, pkg := range manifest.Packages {
		if strings.EqualFold(pkg.Transport.Type, "stdio") && stdioPackage == "" {
			stdioPackage = pkg.Identifier
			for _, item := range pkg.EnvironmentVariables {
				if !validMCPEnvironmentName(item.Name) {
					return stagedMCPInstall{}, fmt.Errorf("MCP manifest requests unsafe environment name %q", item.Name)
				}
				stage.Env = append(stage.Env, mcpInstallEnv{Name: item.Name, Description: item.Description, Required: item.IsRequired, Secret: item.IsSecret})
			}
		}
		registry := strings.ToLower(strings.TrimSpace(pkg.RegistryType))
		if packageFallback == nil && strings.EqualFold(pkg.Transport.Type, "stdio") && (registry == "npm" || registry == "pypi") {
			version := strings.TrimSpace(pkg.Version)
			if version == "" {
				version = strings.TrimSpace(manifest.Version)
			}
			packageFallback = &struct {
				registry, identifier, version string
				args                          []string
			}{registry, strings.TrimSpace(pkg.Identifier), version, append([]string(nil), pkg.RuntimeArguments...)}
		}
		if strings.EqualFold(pkg.RegistryType, "oci") && strings.EqualFold(pkg.Transport.Type, "stdio") {
			stage.Image = strings.TrimSpace(pkg.Identifier)
			stage.Runtime = "oci"
		}
	}
	if stage.ServerID == "" {
		return stagedMCPInstall{}, fmt.Errorf("the MCP manifest does not declare a valid server name")
	}
	if stage.Image == "" && packageFallback != nil {
		image, args, err := packageRunnerPlan(packageFallback.registry, packageFallback.identifier, packageFallback.version, packageFallback.args)
		if err != nil {
			return stagedMCPInstall{}, err
		}
		stage.Image, stage.Args, stage.Runtime = image, args, packageFallback.registry
		stage.Findings = append(stage.Findings, mcpInstallFinding{Severity: "warning", Title: "Isolated package runtime", Detail: "This package is installed only inside Soulacy's disposable, read-only runner container. No package or install script executes on the gateway host."})
	}
	if stage.Image == "" || !safeOCIReference.MatchString(stage.Image) || strings.Contains(stage.Image, "@") {
		return stagedMCPInstall{}, fmt.Errorf("a tagged OCI, exact-version npm, or exact-version PyPI stdio package is required; Soulacy will not build repository code on the gateway")
	}
	stage.Findings = append(stage.Findings,
		mcpInstallFinding{Severity: "info", Title: "Immutable source", Detail: "Approval is bound to Git commit " + revision + "."},
		mcpInstallFinding{Severity: "info", Title: "No host install scripts", Detail: "Soulacy pulls a container image; repository builds and package install scripts never execute on the gateway."},
	)
	if stage.Runtime == "oci" {
		stage.Findings = append(stage.Findings, mcpInstallFinding{Severity: "warning", Title: "Publisher image", Detail: "The publisher-provided image will be resolved and stored by immutable SHA-256 digest at approval time."})
		if dockerfile, err := readInstallFile(filepath.Join(root, "Dockerfile"), 2<<20); err == nil {
			text := strings.ToLower(string(dockerfile))
			if !strings.Contains(text, "\nuser ") {
				stage.Findings = append(stage.Findings, mcpInstallFinding{Severity: "warning", Title: "Image user not declared", Detail: "Dockerfile does not declare a non-root USER; runtime capabilities remain dropped."})
			}
			if strings.Contains(text, `"--transport", "http"`) || strings.Contains(text, "--transport http") {
				if command := discoverPythonScript(root, stdioPackage); command != "" {
					stage.Args = []string{"uv", "run", command, "--transport", "stdio"}
					stage.Findings = append(stage.Findings, mcpInstallFinding{Severity: "info", Title: "Transport override", Detail: "The image's HTTP default is replaced with its declared stdio entry point."})
				} else {
					return stagedMCPInstall{}, fmt.Errorf("OCI image defaults to HTTP but no reviewed stdio entry point was found")
				}
			}
		}
	} else {
		stage.Findings = append(stage.Findings, mcpInstallFinding{Severity: "info", Title: "Pinned runner", Detail: "Soulacy pins its runner image by SHA-256 and launches only the manifest's exact package version inside it."})
	}
	// Inventory limits prevent a repository from turning inspection into a
	// disk/CPU denial of service even though none of its contents are run.
	files, bytes := 0, int64(0)
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		// WalkDir never follows a symbolic link. Repository-owned convenience
		// links therefore contribute only their small directory-entry size and
		// cannot escape the clone or affect inspection. Files that influence the
		// execution plan (server.json, Dockerfile, and pyproject.toml) are still
		// opened through readInstallFile, whose Lstat requires a regular file.
		if !d.IsDir() {
			files++
			if info, infoErr := d.Info(); infoErr == nil {
				bytes += info.Size()
			}
		}
		if files > 20000 || bytes > 512<<20 {
			return fmt.Errorf("repository exceeds inspection limits")
		}
		return nil
	})
	if err != nil {
		return stagedMCPInstall{}, err
	}
	sort.Slice(stage.Env, func(i, j int) bool { return stage.Env[i].Name < stage.Env[j].Name })
	return stage, nil
}

// manifestRepositoryMatches accepts both MCP registry schema generations:
// older manifests use repository.url and current manifests use homepage.
// When both are present they must both identify the requested repository;
// accepting either side of a conflict would make provenance ambiguous.
func manifestRepositoryMatches(manifest mcpInstallManifest, source string) bool {
	identities := []string{strings.TrimSpace(manifest.Repository.URL), strings.TrimSpace(manifest.Homepage)}
	found := false
	for _, identity := range identities {
		if identity == "" {
			continue
		}
		found = true
		canonical, err := canonicalGitHubRepository(identity)
		if err != nil || canonical != source {
			return false
		}
	}
	return found
}

type npmSourceManifest struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Description  string            `json:"description"`
	Main         string            `json:"main"`
	Bin          json.RawMessage   `json:"bin"`
	Scripts      map[string]string `json:"scripts"`
	Dependencies map[string]string `json:"dependencies"`
}

func npmMCPEntrypoint(pkg npmSourceManifest) string {
	var candidate string
	if len(pkg.Bin) > 0 && string(pkg.Bin) != "null" {
		var single string
		if json.Unmarshal(pkg.Bin, &single) == nil {
			candidate = single
		} else {
			var bins map[string]string
			if json.Unmarshal(pkg.Bin, &bins) == nil {
				candidate = bins[pkg.Name]
				if candidate == "" && len(bins) == 1 {
					for _, entry := range bins {
						candidate = entry
					}
				}
			}
		}
	}
	if candidate == "" {
		candidate = pkg.Main
	}
	candidate = filepath.ToSlash(strings.TrimPrefix(strings.TrimSpace(candidate), "./"))
	if candidate == "" || filepath.IsAbs(candidate) || candidate == ".." || strings.HasPrefix(candidate, "../") || strings.Contains(candidate, "/../") || !strings.HasSuffix(candidate, ".js") {
		return ""
	}
	return candidate
}

func inspectNPMSourceRepository(root, source, revision string) (stagedMCPInstall, error) {
	data, err := readInstallFile(filepath.Join(root, "package.json"), 1<<20)
	if err != nil {
		return stagedMCPInstall{}, fmt.Errorf("no supported MCP manifest found (expected server.json or a locked Node MCP package)")
	}
	if _, err := readInstallFile(filepath.Join(root, "package-lock.json"), 8<<20); err != nil {
		return stagedMCPInstall{}, fmt.Errorf("source-based Node MCP installation requires package-lock.json")
	}
	var pkg npmSourceManifest
	if err := json.Unmarshal(data, &pkg); err != nil {
		return stagedMCPInstall{}, fmt.Errorf("invalid package.json: %w", err)
	}
	if pkg.Dependencies["@modelcontextprotocol/sdk"] == "" {
		return stagedMCPInstall{}, fmt.Errorf("package.json does not declare the Model Context Protocol SDK")
	}
	main := npmMCPEntrypoint(pkg)
	if main == "" {
		return stagedMCPInstall{}, fmt.Errorf("package.json must declare a safe JavaScript MCP entry point")
	}
	build := strings.TrimSpace(pkg.Scripts["build"])
	if build == "" || len(build) > 4096 || strings.ContainsRune(build, '\x00') {
		return stagedMCPInstall{}, fmt.Errorf("source-based Node MCP installation requires a build script")
	}
	stage := stagedMCPInstall{
		SourceURL: source, Revision: revision, Name: pkg.Name, Description: pkg.Description,
		ServerID: safeMCPInstallID(pkg.Name), Image: mcpNodeRunnerImage,
		Args: []string{"node", "/app/" + main}, Runtime: "source-npm",
	}
	if stage.ServerID == "" || !safePackageVersion.MatchString(strings.TrimSpace(pkg.Version)) {
		return stagedMCPInstall{}, fmt.Errorf("package.json must declare a safe name and exact version")
	}
	stage.Env = discoverDotEnv(filepath.Join(root, "env.example"))
	stage.Findings = append(stage.Findings,
		mcpInstallFinding{Severity: "warning", Title: "Isolated source build", Detail: "Soulacy will install locked dependencies with lifecycle scripts disabled, then execute the repository build script without network access as a non-root user inside a disposable Docker builder."},
		mcpInstallFinding{Severity: "info", Title: "Immutable source", Detail: "Approval is bound to Git commit " + revision + "."},
		mcpInstallFinding{Severity: "info", Title: "No host execution", Detail: "Repository code and package commands never execute in the gateway process."},
	)
	return stage, nil
}

type pythonSourceManifest struct {
	Project struct {
		Name        string            `toml:"name"`
		Version     string            `toml:"version"`
		Description string            `toml:"description"`
		Scripts     map[string]string `toml:"scripts"`
	} `toml:"project"`
}

func inspectPythonSourceRepository(root, source, revision string) (stagedMCPInstall, error) {
	data, err := readInstallFile(filepath.Join(root, "pyproject.toml"), 2<<20)
	if err != nil {
		return stagedMCPInstall{}, fmt.Errorf("source-based Python MCP installation requires pyproject.toml")
	}
	if _, err := readInstallFile(filepath.Join(root, "uv.lock"), 32<<20); err != nil {
		return stagedMCPInstall{}, fmt.Errorf("source-based Python MCP installation requires uv.lock")
	}
	var pkg pythonSourceManifest
	if err := toml.Unmarshal(data, &pkg); err != nil {
		return stagedMCPInstall{}, fmt.Errorf("invalid pyproject.toml: %w", err)
	}
	scripts := make([]string, 0, len(pkg.Project.Scripts))
	for name := range pkg.Project.Scripts {
		if safePackageIdentifier.MatchString(name) && !strings.Contains(name, "/") {
			scripts = append(scripts, name)
		}
	}
	if len(scripts) == 0 {
		return stagedMCPInstall{}, fmt.Errorf("Python MCP package has no safe project script")
	}
	sort.Strings(scripts)
	stage := stagedMCPInstall{
		SourceURL: source, Revision: revision, Name: pkg.Project.Name, Description: pkg.Project.Description,
		ServerID: safeMCPInstallID(pkg.Project.Name), Image: mcpPythonRunnerImage,
		Args: []string{"uv", "run", "--offline", "--no-sync", scripts[0]}, Runtime: "source-python",
	}
	if stage.ServerID == "" || !safePackageVersion.MatchString(strings.TrimSpace(pkg.Project.Version)) {
		return stagedMCPInstall{}, fmt.Errorf("pyproject.toml must declare a safe name and exact version")
	}
	stage.Env = discoverDotEnv(filepath.Join(root, ".env.example"))
	stage.Findings = append(stage.Findings,
		mcpInstallFinding{Severity: "warning", Title: "Isolated Python source build", Detail: "Soulacy installs the frozen uv dependency graph in a disposable builder, then installs the approved project with networking disabled."},
		mcpInstallFinding{Severity: "info", Title: "Immutable source", Detail: "Approval is bound to Git commit " + revision + "."},
		mcpInstallFinding{Severity: "info", Title: "No host execution", Detail: "Python build and package commands never execute in the gateway process."},
	)
	return stage, nil
}

func discoverDotEnv(path string) []mcpInstallEnv {
	data, err := readInstallFile(path, 1<<20)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []mcpInstallEnv
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		name := strings.TrimSpace(strings.SplitN(line, "=", 2)[0])
		if !seen[name] && validMCPEnvironmentName(name) {
			seen[name] = true
			out = append(out, mcpInstallEnv{Name: name, Secret: redact.SecretKeyName(name)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

var safePackageIdentifier = regexp.MustCompile(`^[A-Za-z0-9@][A-Za-z0-9@._/-]{1,220}$`)
var safePackageVersion = regexp.MustCompile(`^[0-9][0-9A-Za-z._+-]{0,80}$`)

func packageRunnerPlan(registry, identifier, version string, runtimeArgs []string) (string, []string, error) {
	if !safePackageIdentifier.MatchString(identifier) || strings.Contains(identifier, "..") ||
		!safePackageVersion.MatchString(version) {
		return "", nil, fmt.Errorf("npm/PyPI installation requires a safe package name and exact published version")
	}
	for _, arg := range runtimeArgs {
		if strings.ContainsRune(arg, '\x00') || len(arg) > 4096 {
			return "", nil, fmt.Errorf("package runtime arguments are invalid")
		}
	}
	switch registry {
	case "npm":
		return mcpNodeRunnerImage, mcppackage.NodeRunnerArgs(identifier, version, runtimeArgs), nil
	case "pypi":
		spec := identifier + "==" + version
		return mcpPythonRunnerImage, append([]string{"uvx", spec}, runtimeArgs...), nil
	default:
		return "", nil, fmt.Errorf("unsupported isolated package registry %q", registry)
	}
}

func normalizeMCPInstallPermissions(p mcpInstallPermissions) (mcpInstallPermissions, error) {
	p.Network = strings.ToLower(strings.TrimSpace(p.Network))
	p.Workspace = strings.ToLower(strings.TrimSpace(p.Workspace))
	if p.Network == "" {
		p.Network = "public"
	}
	if p.Workspace == "" {
		p.Workspace = "none"
	}
	if !validContainerPermissions(p.Network, p.Workspace) {
		return p, fmt.Errorf("invalid MCP permissions")
	}
	return p, nil
}

func permissionFindings(p mcpInstallPermissions) []mcpInstallFinding {
	network := "Outbound network access is disabled."
	if p.Network == "public" {
		network = "The server may call public APIs through Docker bridge networking; host networking and the Docker socket remain unavailable."
	}
	workspace := "The server receives no workspace files."
	if p.Workspace == "read" {
		workspace = "The current workspace is mounted read-only at /workspace."
	} else if p.Workspace == "write" {
		workspace = "The current workspace is mounted read/write at /workspace; the server can change this workspace's files."
	}
	return []mcpInstallFinding{
		{Severity: map[bool]string{true: "warning", false: "info"}[p.Network == "public"], Title: "Network permission", Detail: network},
		{Severity: map[bool]string{true: "warning", false: "info"}[p.Workspace == "write"], Title: "Workspace permission", Detail: workspace},
	}
}

func discoverPythonScript(root, packageName string) string {
	data, err := readInstallFile(filepath.Join(root, "pyproject.toml"), 2<<20)
	if err != nil {
		return ""
	}
	inScripts := false
	preferred := strings.TrimSpace(packageName)
	first := ""
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			inScripts = line == "[project.scripts]"
			continue
		}
		if !inScripts || !strings.Contains(line, "=") {
			continue
		}
		name := strings.TrimSpace(strings.SplitN(line, "=", 2)[0])
		if first == "" {
			first = name
		}
		if name == preferred {
			return name
		}
	}
	return first
}

func readInstallFile(path string, max int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > max {
		return nil, fmt.Errorf("install metadata must be a regular file no larger than %d bytes", max)
	}
	return os.ReadFile(path)
}

func safeMCPInstallID(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimPrefix(name, "io.github.")
	name = strings.TrimSuffix(name, "-mcp")
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else if b.Len() > 0 {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-_")
}

func randomInstallToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func mcpInstallFingerprint(stage stagedMCPInstall) string {
	payload, _ := json.Marshal(struct {
		Source, Revision, ID, Image, Runtime string
		Args                                 []string
		Permissions                          mcpInstallPermissions
		Environment                          []mcpInstallEnv
	}{stage.SourceURL, stage.Revision, stage.ServerID, stage.Image, stage.Runtime, stage.Args, stage.Permissions, stage.Env})
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func installReport(stage stagedMCPInstall) fiber.Map {
	return fiber.Map{
		"fingerprint": stage.Fingerprint, "expires_at": stage.ExpiresAt,
		"server_id": stage.ServerID, "name": stage.Name, "description": stage.Description,
		"source_url": stage.SourceURL, "revision": stage.Revision, "image": stage.Image,
		"runtime": stage.Runtime, "command": stage.Args, "environment": stage.Env,
		"permissions": stage.Permissions, "findings": stage.Findings,
		"isolation": fiber.Map{"read_only": true, "non_root": true, "capabilities": "none", "host_mounts": stage.Permissions.Workspace != "none", "docker_socket": false, "resource_limits": true, "network": stage.Permissions.Network},
	}
}

func (s *Server) storeMCPInstallSettings(ctx context.Context, stage stagedMCPInstall, settings map[string]string) (map[string]string, int, error) {
	declared := make(map[string]mcpInstallEnv, len(stage.Env))
	for _, item := range stage.Env {
		declared[item.Name] = item
	}
	for key := range settings {
		if _, ok := declared[key]; !ok {
			return nil, 0, fmt.Errorf("setting %q was not declared by the reviewed MCP manifest", key)
		}
	}
	env := make(map[string]string, len(stage.Env))
	stored := 0
	for _, item := range stage.Env {
		value := settings[item.Name]
		generatedLocalDatabase := false
		if stage.Runtime == "source-python" && item.Name == "DATABASE_URL" && strings.TrimSpace(value) == "" {
			value = "sqlite:////data/" + stage.ServerID + ".db"
			generatedLocalDatabase = true
		}
		if item.Required && strings.TrimSpace(value) == "" {
			return nil, 0, fmt.Errorf("%s is required", item.Name)
		}
		// An absent optional scalar is different from an explicitly empty
		// environment variable. Many typed settings loaders reject REDIS_PORT=""
		// or DATABASE_URL="" instead of applying their default.
		if !item.Secret && !item.Required && strings.TrimSpace(value) == "" {
			continue
		}
		// This exact SQLite URL is generated by Soulacy and points only at the
		// server-private /data mount. It contains no credential. User-supplied
		// database URLs still follow the secret path below and enter the vault.
		if generatedLocalDatabase {
			env[item.Name] = value
			continue
		}
		if item.Secret {
			env[item.Name] = ""
			if value == "" {
				continue
			}
			if s.credVault == nil {
				return nil, 0, fmt.Errorf("workspace credential vault is unavailable")
			}
			if err := s.credVault.Set(ctx, wsroot.Normalize(stage.WorkspaceID), mcp.CredentialNamespace(stage.ServerID), item.Name, []byte(value)); err != nil {
				return nil, 0, fmt.Errorf("store %s: %w", item.Name, err)
			}
			stored++
			continue
		}
		env[item.Name] = value
	}
	return env, stored, nil
}

func pullAndPinOCI(ctx context.Context, image string) (string, error) {
	if !safeOCIReference.MatchString(image) || strings.Contains(image, "@") {
		return "", fmt.Errorf("invalid OCI image reference")
	}
	pull, err := dockerutil.CommandContext(ctx, "pull", image)
	if err != nil {
		return "", err
	}
	if out, err := pull.CombinedOutput(); err != nil {
		return "", fmt.Errorf("docker pull: %v: %s", err, strings.TrimSpace(string(out)))
	}
	inspect, err := dockerutil.CommandContext(ctx, "image", "inspect", "--format", "{{json .RepoDigests}}", image)
	if err != nil {
		return "", err
	}
	out, err := inspect.Output()
	if err != nil {
		return "", fmt.Errorf("inspect pulled image: %w", err)
	}
	var digests []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &digests); err != nil || len(digests) == 0 {
		return "", fmt.Errorf("registry did not provide an immutable image digest")
	}
	for _, digest := range digests {
		if strings.Contains(digest, "@sha256:") {
			return digest, nil
		}
	}
	return "", fmt.Errorf("registry returned no SHA-256 image digest")
}

func buildAndPinSource(ctx context.Context, stage stagedMCPInstall) (string, error) {
	buildx, err := dockerutil.CommandContext(ctx, "buildx", "version")
	if err != nil {
		return "", fmt.Errorf("secure MCP source builds require Docker Buildx/BuildKit: %w", err)
	}
	if out, err := buildx.CombinedOutput(); err != nil {
		return "", fmt.Errorf("secure MCP source builds require Docker Buildx/BuildKit; install the Docker Buildx plugin and retry: %v: %s", err, strings.TrimSpace(string(out)))
	}
	root, err := os.MkdirTemp("", "soulacy-mcp-build-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(root)
	sourceDir := filepath.Join(root, "source")
	revision, err := plugininstall.GitClone(ctx, stage.SourceURL+"#"+stage.Revision, sourceDir)
	if err != nil {
		return "", fmt.Errorf("reclone approved source: %w", err)
	}
	if revision != stage.Revision {
		return "", fmt.Errorf("approved Git revision changed")
	}
	var reviewed stagedMCPInstall
	switch stage.Runtime {
	case "source-npm":
		reviewed, err = inspectNPMSourceRepository(sourceDir, stage.SourceURL, revision)
	case "source-python":
		reviewed, err = inspectPythonSourceRepository(sourceDir, stage.SourceURL, revision)
	default:
		return "", fmt.Errorf("unsupported source builder %q", stage.Runtime)
	}
	if err != nil {
		return "", fmt.Errorf("reinspect approved source: %w", err)
	}
	// Official manifests may describe secrets more precisely than .env.example.
	// Preserve exactly the environment contract shown during approval.
	reviewed.Env = stage.Env
	reviewed.Permissions = stage.Permissions
	if mcpInstallFingerprint(reviewed) != stage.Fingerprint {
		return "", fmt.Errorf("source execution plan changed after approval")
	}
	baseImage := mcpNodeRunnerImage
	if stage.Runtime == "source-python" {
		baseImage = mcpPythonRunnerImage
	}
	base, err := pullAndPinOCI(ctx, baseImage)
	if err != nil {
		return "", fmt.Errorf("pin source builder: %w", err)
	}
	var dockerfile string
	if stage.Runtime == "source-npm" {
		dockerfile = npmSourceDockerfile(base, stage.Args[1])
	} else {
		dockerfile = pythonSourceDockerfile(base, stage.Args)
	}
	dockerfilePath := filepath.Join(sourceDir, ".soulacy.Dockerfile")
	if err := os.WriteFile(dockerfilePath, []byte(dockerfile), 0o600); err != nil {
		return "", err
	}
	tag := "soulacy-local/mcp-" + stage.ServerID + ":" + stage.Revision[:12]
	cmd, err := dockerutil.CommandContext(ctx, "build", "--no-cache", "--pull=false", "-f", dockerfilePath, "-t", tag, sourceDir)
	if err != nil {
		return "", err
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("isolated MCP source build: %v: %s", err, tailInstallOutput(string(out), 5000))
	}
	inspectBuilt, err := dockerutil.CommandContext(ctx, "image", "inspect", "--format", "{{.Id}}", tag)
	if err != nil {
		return "", err
	}
	out, err := inspectBuilt.Output()
	if err != nil {
		return "", fmt.Errorf("inspect built image: %w", err)
	}
	id := strings.TrimSpace(string(out))
	if !immutableLocalImage.MatchString(id) {
		return "", fmt.Errorf("builder returned no immutable image id")
	}
	// Keep a content-addressed tag so ordinary dangling-image cleanup does not
	// remove an installed workspace artifact. Runtime still launches the image
	// by immutable ID, never by this mutable convenience tag.
	artifactTag := "soulacy-local/mcp-artifact:" + strings.TrimPrefix(id, "sha256:")[:20]
	tagCmd, err := dockerutil.CommandContext(ctx, "tag", id, artifactTag)
	if err != nil {
		return "", err
	}
	if out, err := tagCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("retain built image: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return id, nil
}

func ociManifestAvailable(ctx context.Context, image string) bool {
	if !safeOCIReference.MatchString(image) || strings.Contains(image, "@") {
		return false
	}
	probe, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd, err := dockerutil.CommandContext(probe, "manifest", "inspect", image)
	return err == nil && cmd.Run() == nil
}

func npmSourceDockerfile(base, entrypoint string) string {
	return fmt.Sprintf(`FROM %s AS build
WORKDIR /app
RUN chown node:node /app
USER node
COPY --chown=node:node package.json package-lock.json ./
RUN npm ci --ignore-scripts
COPY --chown=node:node . .
RUN --network=none npm run build
RUN npm prune --omit=dev --ignore-scripts

FROM %s
WORKDIR /app
COPY --from=build --chown=node:node /app /app
USER node
CMD ["node", %q]
`, base, base, entrypoint)
}

func pythonSourceDockerfile(base string, command []string) string {
	encoded, _ := json.Marshal(command)
	return fmt.Sprintf(`FROM %s AS build
WORKDIR /app
RUN chown 65532:65532 /app
USER 65532:65532
ENV HOME=/tmp UV_CACHE_DIR=/tmp/uv-cache PYTHONPATH=/tmp/build-tools
COPY --chown=65532:65532 pyproject.toml uv.lock ./
RUN uv pip install --target /tmp/build-tools hatchling==1.27.0 editables==0.5
RUN uv sync --frozen --no-dev --no-install-project
COPY --chown=65532:65532 . .
RUN --network=none uv sync --offline --frozen --no-dev --no-build-isolation

FROM %s
WORKDIR /app
ENV HOME=/tmp UV_CACHE_DIR=/tmp/uv-cache
COPY --from=build --chown=65532:65532 /app /app
USER 65532:65532
CMD %s
`, base, base, encoded)
}

func tailInstallOutput(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return value[len(value)-max:]
}
