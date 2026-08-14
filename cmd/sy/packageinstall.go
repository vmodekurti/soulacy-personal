package main

// Unified URL installer for the System agent and CLI.
//
// A model should never have to infer whether to run `skill install`, clone a
// repository, create a venv, or edit config.yaml. This command performs that
// decision deterministically from repository manifests and keeps installation
// policy in one auditable place.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/introspect"
	"github.com/soulacy/soulacy/internal/plugininstall"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type urlPackageKind string

const (
	urlPackageAuto  urlPackageKind = "auto"
	urlPackageSkill urlPackageKind = "skill"
	urlPackageMCP   urlPackageKind = "mcp"
)

type mcpServerManifest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Packages    []struct {
		RegistryType         string `json:"registryType"`
		Identifier           string `json:"identifier"`
		Version              string `json:"version"`
		EnvironmentVariables []struct {
			Name       string `json:"name"`
			IsRequired bool   `json:"isRequired"`
		} `json:"environmentVariables"`
	} `json:"packages"`
}

type pythonProject struct {
	Project struct {
		Name           string            `toml:"name"`
		RequiresPython string            `toml:"requires-python"`
		Scripts        map[string]string `toml:"scripts"`
	} `toml:"project"`
}

type nodeProject struct {
	Name    string            `json:"name"`
	Bin     json.RawMessage   `json:"bin"`
	Scripts map[string]string `json:"scripts"`
}

func buildPackageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "package",
		Short: "Install a Skill or MCP server from one URL",
	}
	var kind string
	var assumeYes, allowUnverified bool
	install := &cobra.Command{
		Use:   "install <https-git-url>",
		Short: "Detect, inspect, install, register, and verify a Skill or MCP server",
		Long: `Install a Soulacy Skill or MCP server from one HTTPS Git URL.

The installer detects SKILL.md or MCP package manifests, runs safety
introspection, installs into the persistent Soulacy workspace, registers MCP
servers in the live config, and skips packages that are already installed.
Raw Git sources are not cryptographically signed, so non-interactive callers
must explicitly pass --allow-unverified.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return installURLPackage(cmd.Context(), args[0], urlPackageKind(kind), assumeYes, allowUnverified)
		},
	}
	install.Flags().StringVar(&kind, "kind", string(urlPackageAuto), "Package kind: auto, skill, or mcp")
	install.Flags().BoolVarP(&assumeYes, "yes", "y", false, "Use the approval already collected by the caller")
	install.Flags().BoolVar(&allowUnverified, "allow-unverified", false, "Allow an unsigned/raw Git source after explicit operator approval")
	cmd.AddCommand(install)
	return cmd
}

func installURLPackage(ctx context.Context, source string, kind urlPackageKind, assumeYes, allowUnverified bool) error {
	source = strings.TrimSpace(source)
	if !strings.HasPrefix(strings.ToLower(source), "https://") {
		return fmt.Errorf("source must be an HTTPS Git repository URL")
	}
	switch kind {
	case urlPackageAuto, urlPackageSkill, urlPackageMCP:
	default:
		return fmt.Errorf("--kind must be auto, skill, or mcp")
	}
	if !allowUnverified {
		return fmt.Errorf("refusing unsigned/raw Git source %q; review it and pass --allow-unverified to continue", source)
	}

	probe, err := os.MkdirTemp("", "soulacy-package-probe-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(probe)
	if err := plugininstall.GitClone(ctx, source, probe); err != nil {
		return fmt.Errorf("inspect source: %w", err)
	}

	detected, err := detectURLPackageKind(probe)
	if err != nil {
		return err
	}
	if kind == urlPackageAuto {
		kind = detected
	} else if !repositorySupportsPackageKind(probe, kind) {
		return fmt.Errorf("requested kind %q does not match repository contents (detected %q)", kind, detected)
	}

	if kind == urlPackageSkill {
		if ws, werr := config.ResolveWorkspace(); werr == nil {
			dest := filepath.Join(ws.Root, "skills", skillDirName(source))
			if fileExists(filepath.Join(dest, "SKILL.md")) {
				fmt.Printf("✓ Skill %q is already installed; no files were downloaded again.\n", skillDirName(source))
				return nil
			}
		}
		// Reuse the mature registry installer: signature policy, safety scan,
		// atomic activation, and gateway rescan all remain in one place.
		return runRemoteSkillInstall(ctx, source, assumeYes, allowUnverified)
	}
	return installMCPFromRepository(ctx, source, probe, assumeYes)
}

func detectURLPackageKind(dir string) (urlPackageKind, error) {
	if fileExists(filepath.Join(dir, "SKILL.md")) {
		return urlPackageSkill, nil
	}
	if fileExists(filepath.Join(dir, "mcp_server.py")) || fileExists(filepath.Join(dir, "server.json")) || fileExists(filepath.Join(dir, "pyproject.toml")) || fileExists(filepath.Join(dir, "package.json")) {
		if fileExists(filepath.Join(dir, "server.json")) || repositoryMentionsMCP(dir) {
			return urlPackageMCP, nil
		}
	}
	return "", fmt.Errorf("repository is neither a Soulacy Skill (SKILL.md) nor a recognizable MCP server (server.json or MCP package manifest)")
}

func repositorySupportsPackageKind(dir string, kind urlPackageKind) bool {
	switch kind {
	case urlPackageSkill:
		return fileExists(filepath.Join(dir, "SKILL.md"))
	case urlPackageMCP:
		if fileExists(filepath.Join(dir, "server.json")) || fileExists(filepath.Join(dir, "mcp_server.py")) {
			return true
		}
		return (fileExists(filepath.Join(dir, "pyproject.toml")) || fileExists(filepath.Join(dir, "package.json"))) && repositoryMentionsMCP(dir)
	default:
		return false
	}
}

func repositoryMentionsMCP(dir string) bool {
	for _, name := range []string{"pyproject.toml", "package.json", "README.md"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err == nil && strings.Contains(strings.ToLower(string(b)), "mcp") {
			return true
		}
	}
	return false
}

func installMCPFromRepository(ctx context.Context, source, probe string, assumeYes bool) error {
	ws, err := config.ResolveWorkspace()
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}
	manifest, _ := readMCPServerManifest(filepath.Join(probe, "server.json"))
	id := packageInstallID(manifest.Name)
	if id == "" {
		id = packageInstallID(source)
	}
	if id == "" {
		return fmt.Errorf("could not derive a safe MCP server id")
	}

	doc, root, err := loadConfigDoc(ws.ConfigFile)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	servers := ensureMapping(ensureMapping(root, "mcp"), "servers")
	if yamlMapValue(servers, id) != nil {
		fmt.Printf("✓ MCP server %q is already registered; no packages were reinstalled.\n", id)
		return nil
	}

	report := (&introspect.Pipeline{DryRun: &introspect.DryRunConfig{Timeout: 5 * time.Second}}).Run(ctx, probe, nil)
	printSecurityReport(os.Stdout, report)
	if report.Verdict == introspect.VerdictDanger {
		return fmt.Errorf("refusing MCP install because safety introspection returned DANGER")
	}
	if !assumeYes {
		fmt.Printf("Install and register MCP server %q from %s? [y/N] ", id, source)
		var answer string
		_, _ = fmt.Scanln(&answer)
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			return fmt.Errorf("install aborted by user")
		}
	}

	dest := filepath.Join(ws.Root, "mcp-servers", id)
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("MCP install directory already exists at %s but the server is not registered; inspect or remove that partial installation before retrying", dest)
	}
	sourceDir := filepath.Join(dest, "source")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	activated := false
	defer func() {
		if !activated {
			_ = os.RemoveAll(dest)
		}
	}()
	if err := copyDir(probe, sourceDir); err != nil {
		return fmt.Errorf("persist source: %w", err)
	}

	command, args, installErr := installMCPRuntime(ctx, dest, sourceDir)
	if installErr != nil {
		return installErr
	}
	srv := ensureMapping(servers, id)
	setScalar(srv, "transport", "stdio", 0)
	setScalar(srv, "command", command, yaml.DoubleQuotedStyle)
	if len(args) > 0 {
		setSequence(srv, "args", args)
	}
	if !fileExists(command) && !executableOnPath(command) {
		return fmt.Errorf("registered command %q could not be verified", command)
	}
	if err := saveConfigDoc(ws.ConfigFile, doc); err != nil {
		return fmt.Errorf("register MCP server: %w", err)
	}
	activated = true

	fmt.Printf("✓ Installed and registered MCP server %q.\n", id)
	fmt.Printf("  Source: %s\n  Command: %s %s\n  Config: %s\n", sourceDir, command, strings.Join(args, " "), ws.ConfigFile)
	if required := requiredMCPEnvironment(manifest); len(required) > 0 {
		fmt.Printf("  Required environment still to configure: %s\n", strings.Join(required, ", "))
	} else {
		fmt.Println("✓ Manifest has no required environment variables.")
	}
	fmt.Println("✓ Config was written atomically; the gateway will hot-reload it.")
	return nil
}

func installMCPRuntime(ctx context.Context, dest, sourceDir string) (string, []string, error) {
	if fileExists(filepath.Join(sourceDir, "pyproject.toml")) {
		var project pythonProject
		b, err := os.ReadFile(filepath.Join(sourceDir, "pyproject.toml"))
		if err != nil || toml.Unmarshal(b, &project) != nil {
			return "", nil, fmt.Errorf("read pyproject.toml")
		}
		scripts := sortedKeys(project.Project.Scripts)
		if len(scripts) == 0 {
			return "", nil, fmt.Errorf("Python MCP repository has no [project.scripts] entrypoint")
		}
		venv := filepath.Join(dest, "venv")
		if out, err := exec.CommandContext(ctx, "python3", "-m", "venv", venv).CombinedOutput(); err != nil {
			return "", nil, fmt.Errorf("create MCP venv: %v: %s", err, strings.TrimSpace(string(out)))
		}
		pip := filepath.Join(venv, "bin", "pip")
		installCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		if out, err := exec.CommandContext(installCtx, pip, "install", sourceDir).CombinedOutput(); err != nil {
			return "", nil, fmt.Errorf("install Python MCP dependencies: %v: %s", err, tailText(string(out), 4000))
		}
		return filepath.Join(venv, "bin", scripts[0]), nil, nil
	}

	if fileExists(filepath.Join(sourceDir, "package.json")) {
		var project nodeProject
		b, err := os.ReadFile(filepath.Join(sourceDir, "package.json"))
		if err != nil || json.Unmarshal(b, &project) != nil {
			return "", nil, fmt.Errorf("read package.json")
		}
		installCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		if out, err := exec.CommandContext(installCtx, "npm", "install", "--omit=dev", "--prefix", sourceDir).CombinedOutput(); err != nil {
			return "", nil, fmt.Errorf("install Node MCP dependencies: %v: %s", err, tailText(string(out), 4000))
		}
		if bin := firstNodeBin(project.Bin); bin != "" {
			return "node", []string{filepath.Join(sourceDir, bin)}, nil
		}
		if _, ok := project.Scripts["start"]; ok {
			return "npm", []string{"--prefix", sourceDir, "start"}, nil
		}
		return "", nil, fmt.Errorf("Node MCP repository has neither a bin entry nor a start script")
	}

	// Some small MCP repositories predate packaging manifests and ship a
	// standalone mcp_server.py plus human-readable installation instructions.
	// Support these without executing README shell snippets: extract only plain
	// PyPI requirement tokens, pass them as fixed pip argv, and reject flags,
	// URLs, paths, and shell metacharacters.
	if server := filepath.Join(sourceDir, "mcp_server.py"); fileExists(server) {
		venv := filepath.Join(dest, "venv")
		if out, err := exec.CommandContext(ctx, "python3", "-m", "venv", venv).CombinedOutput(); err != nil {
			return "", nil, fmt.Errorf("create MCP venv: %v: %s", err, strings.TrimSpace(string(out)))
		}
		pip := filepath.Join(venv, "bin", "pip")
		installCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		if requirements := filepath.Join(sourceDir, "requirements.txt"); fileExists(requirements) {
			if out, err := exec.CommandContext(installCtx, pip, "install", "-r", requirements).CombinedOutput(); err != nil {
				return "", nil, fmt.Errorf("install legacy Python MCP dependencies: %v: %s", err, tailText(string(out), 4000))
			}
		} else {
			deps, err := readLegacyMCPDependencies(filepath.Join(sourceDir, "README.md"))
			if err != nil {
				return "", nil, err
			}
			args := append([]string{"install"}, deps...)
			if out, err := exec.CommandContext(installCtx, pip, args...).CombinedOutput(); err != nil {
				return "", nil, fmt.Errorf("install legacy Python MCP dependencies: %v: %s", err, tailText(string(out), 4000))
			}
		}
		return filepath.Join(venv, "bin", "python"), []string{server}, nil
	}
	return "", nil, fmt.Errorf("MCP repository has no supported Python or Node package manifest")
}

var legacyRequirementPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(?:\[[A-Za-z0-9_,.-]+\])?(?:(?:==|!=|~=|>=|<=|>|<)[A-Za-z0-9.*+!_-]+)?$`)

func readLegacyMCPDependencies(readmePath string) ([]string, error) {
	b, err := os.ReadFile(readmePath)
	if err != nil {
		return nil, fmt.Errorf("legacy Python MCP server has no requirements.txt or readable README.md")
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		for i := 0; i+2 < len(fields); i++ {
			if fields[i] != "pip" && fields[i] != "pip3" {
				continue
			}
			if fields[i+1] != "install" {
				continue
			}
			var deps []string
			for _, field := range fields[i+2:] {
				field = strings.Trim(field, "`'\";,.")
				if !legacyRequirementPattern.MatchString(field) {
					return nil, fmt.Errorf("legacy Python MCP README contains an unsafe or unsupported dependency token %q", field)
				}
				deps = append(deps, field)
			}
			if len(deps) > 0 && len(deps) <= 32 {
				return deps, nil
			}
		}
	}
	return nil, fmt.Errorf("legacy Python MCP server has no requirements.txt and no simple 'pip install' dependency line in README.md")
}

func readMCPServerManifest(path string) (mcpServerManifest, error) {
	var out mcpServerManifest
	b, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	return out, json.Unmarshal(b, &out)
}

func requiredMCPEnvironment(m mcpServerManifest) []string {
	seen := map[string]bool{}
	var out []string
	for _, pkg := range m.Packages {
		for _, env := range pkg.EnvironmentVariables {
			if env.IsRequired && env.Name != "" && !seen[env.Name] {
				seen[env.Name] = true
				out = append(out, env.Name)
			}
		}
	}
	sort.Strings(out)
	return out
}

func packageInstallID(value string) string {
	value = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(value), "/"), ".git")
	if i := strings.LastIndexAny(value, "/."); i >= 0 {
		value = value[i+1:]
	}
	value = strings.ToLower(value)
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func firstNodeBin(raw json.RawMessage) string {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single
	}
	var bins map[string]string
	if json.Unmarshal(raw, &bins) == nil {
		keys := sortedKeys(bins)
		if len(keys) > 0 {
			return bins[keys[0]]
		}
	}
	return ""
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func executableOnPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func tailText(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[len(s)-max:]
}
