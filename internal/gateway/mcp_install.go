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
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/mcpstore"
	"github.com/soulacy/soulacy/internal/plugininstall"
)

const (
	mcpInstallStageTTL       = 15 * time.Minute
	mcpRepositoryScanTimeout = 2 * time.Minute
	mcpContainerPullTimeout  = 5 * time.Minute
)

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

type mcpInstallManifest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Repository  struct {
		URL string `json:"url"`
	} `json:"repository"`
	Packages []struct {
		RegistryType string `json:"registryType"`
		Identifier   string `json:"identifier"`
		Transport    struct {
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

func (s *Server) handleInspectWorkspaceMCP(c *fiber.Ctx) error {
	var body struct {
		SourceURL string `json:"source_url"`
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

func (s *Server) handleApproveWorkspaceMCP(c *fiber.Ctx) error {
	if s.mcpServers == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace MCP servers are not available")
	}
	var body struct {
		ApprovalToken string `json:"approval_token"`
		Fingerprint   string `json:"fingerprint"`
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

	ctx, cancel := context.WithTimeout(c.UserContext(), mcpContainerPullTimeout)
	defer cancel()
	pinned, err := pullAndPinOCI(ctx, stage.Image)
	if err != nil {
		s.recordAdminAudit(c, "mcp.install.approve", "mcp", stage.ServerID, "failed", map[string]any{"reason": err.Error()})
		return s.errMsg(c, fiber.StatusBadGateway, "container image could not be installed: "+err.Error())
	}
	if err := s.mcpServers.Put(c.UserContext(), mcpstore.Server{
		WorkspaceID: stage.WorkspaceID,
		ID:          stage.ServerID,
		Transport:   "container",
		Command:     pinned,
		Args:        append([]string(nil), stage.Args...),
		CreatedBy:   stage.Actor,
	}); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.mcpInstallMu.Lock()
	delete(s.mcpInstallStages, stage.Token)
	s.mcpInstallMu.Unlock()
	s.invalidateMCPWorkspace(stage.WorkspaceID)
	s.recordAdminAudit(c, "mcp.install.approve", "mcp", stage.ServerID, "ok", map[string]any{
		"source_url": stage.SourceURL, "revision": stage.Revision, "image": pinned, "fingerprint": stage.Fingerprint,
	})
	return c.JSON(fiber.Map{"ok": true, "id": stage.ServerID, "image": pinned, "message": "MCP server installed in an isolated container."})
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
		return stagedMCPInstall{}, fmt.Errorf("server.json is required for workspace installation")
	}
	var manifest mcpInstallManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return stagedMCPInstall{}, fmt.Errorf("invalid server.json: %w", err)
	}
	declared, err := canonicalGitHubRepository(manifest.Repository.URL)
	if err != nil || declared != source {
		return stagedMCPInstall{}, fmt.Errorf("server.json repository identity does not match the requested repository")
	}
	stage := stagedMCPInstall{SourceURL: source, Revision: revision, Name: manifest.Name, Description: manifest.Description}
	stage.ServerID = safeMCPInstallID(manifest.Name)
	var stdioPackage string
	for _, pkg := range manifest.Packages {
		if strings.EqualFold(pkg.Transport.Type, "stdio") && stdioPackage == "" {
			stdioPackage = pkg.Identifier
			for _, item := range pkg.EnvironmentVariables {
				stage.Env = append(stage.Env, mcpInstallEnv{Name: item.Name, Description: item.Description, Required: item.IsRequired, Secret: item.IsSecret})
			}
		}
		if strings.EqualFold(pkg.RegistryType, "oci") && strings.EqualFold(pkg.Transport.Type, "stdio") {
			stage.Image = strings.TrimSpace(pkg.Identifier)
		}
	}
	if stage.ServerID == "" || stage.Image == "" || !safeOCIReference.MatchString(stage.Image) || strings.Contains(stage.Image, "@") {
		return stagedMCPInstall{}, fmt.Errorf("a valid, tagged OCI package with stdio transport is required; Soulacy will not build repository code on the gateway")
	}
	stage.Findings = append(stage.Findings,
		mcpInstallFinding{Severity: "info", Title: "Immutable source", Detail: "Approval is bound to Git commit " + revision + "."},
		mcpInstallFinding{Severity: "info", Title: "No host install scripts", Detail: "Soulacy will pull the declared OCI image; repository build and package scripts are not executed on the gateway."},
		mcpInstallFinding{Severity: "warning", Title: "Public network access", Detail: "The container can call public APIs. It receives no host filesystem mounts, gateway environment, Docker socket, or privileged capabilities."},
		mcpInstallFinding{Severity: "warning", Title: "Publisher image", Detail: "The publisher-provided image will be resolved and stored by immutable SHA-256 digest at approval time."},
	)
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
	// Inventory limits prevent a repository from turning inspection into a
	// disk/CPU denial of service even though none of its contents are run.
	files, bytes := 0, int64(0)
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not accepted in install sources")
		}
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
		Source, Revision, ID, Image string
		Args                        []string
	}{stage.SourceURL, stage.Revision, stage.ServerID, stage.Image, stage.Args})
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func installReport(stage stagedMCPInstall) fiber.Map {
	return fiber.Map{
		"fingerprint": stage.Fingerprint, "expires_at": stage.ExpiresAt,
		"server_id": stage.ServerID, "name": stage.Name, "description": stage.Description,
		"source_url": stage.SourceURL, "revision": stage.Revision, "image": stage.Image,
		"command": stage.Args, "environment": stage.Env, "findings": stage.Findings,
		"isolation": fiber.Map{"read_only": true, "capabilities": "none", "host_mounts": false, "docker_socket": false, "resource_limits": true, "network": "public bridge"},
	}
}

func pullAndPinOCI(ctx context.Context, image string) (string, error) {
	if !safeOCIReference.MatchString(image) || strings.Contains(image, "@") {
		return "", fmt.Errorf("invalid OCI image reference")
	}
	if out, err := exec.CommandContext(ctx, "docker", "pull", image).CombinedOutput(); err != nil {
		return "", fmt.Errorf("docker pull: %v: %s", err, strings.TrimSpace(string(out)))
	}
	out, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{json .RepoDigests}}", image).Output()
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
