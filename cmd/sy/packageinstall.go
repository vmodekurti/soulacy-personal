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
	"io/fs"
	"net/http"
	"net/url"
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
		Dependencies   []string          `toml:"dependencies"`
		Scripts        map[string]string `toml:"scripts"`
	} `toml:"project"`
}

type pythonVersion struct {
	Major int
	Minor int
	Patch int
}

type nodeProject struct {
	Name    string            `json:"name"`
	Bin     json.RawMessage   `json:"bin"`
	Scripts map[string]string `json:"scripts"`
}

// mcpPackage is one independently runnable server inside a Git repository.
// RelDir is always slash-separated and relative to the cloned repository.
// Conventional MCP monorepos commonly keep these under servers/*.
type mcpPackage struct {
	Name       string
	RelDir     string
	SourceDir  string
	Entrypoint string
	Manifest   mcpServerManifest
	EnvVars    []string
}

func buildPackageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "package",
		Short: "Install a Skill or MCP server from one URL",
	}
	var kind string
	var assumeYes, allowUnverified, allowHostBuild bool
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
			if isRemoteGateway() {
				return installRemoteURLPackage(cmd.Context(), args[0], urlPackageKind(kind), allowUnverified, allowHostBuild)
			}
			return installURLPackage(cmd.Context(), args[0], urlPackageKind(kind), assumeYes, allowUnverified)
		},
	}
	install.Flags().StringVar(&kind, "kind", string(urlPackageAuto), "Package kind: auto, skill, or mcp")
	install.Flags().BoolVarP(&assumeYes, "yes", "y", false, "Use the approval already collected by the caller")
	install.Flags().BoolVar(&allowUnverified, "allow-unverified", false, "Allow an unsigned/raw Git source after explicit operator approval")
	install.Flags().BoolVar(&allowHostBuild, "allow-host-build", false, "Allow the remote gateway to build source and create package environments")
	cmd.AddCommand(install)
	return cmd
}

type remotePackageInstallStatus struct {
	JobID    string   `json:"job_id"`
	Status   string   `json:"status"`
	Messages []string `json:"messages"`
	Error    string   `json:"error"`
}

func installRemoteURLPackage(ctx context.Context, source string, kind urlPackageKind, allowUnverified, allowHostBuild bool) error {
	source = strings.TrimSpace(source)
	if !strings.HasPrefix(strings.ToLower(source), "https://") {
		return fmt.Errorf("source must be an HTTPS Git repository URL")
	}
	switch kind {
	case urlPackageAuto, urlPackageSkill, urlPackageMCP:
	default:
		return fmt.Errorf("--kind must be auto, skill, or mcp")
	}
	body, err := json.Marshal(map[string]any{
		"source": source, "kind": kind,
		"allow_unverified": allowUnverified,
		"allow_host_build": allowHostBuild,
	})
	if err != nil {
		return err
	}
	data, err := apiCallWithTimeout(http.MethodPost, "/packages/install", body, 30*time.Second)
	if err != nil {
		return err
	}
	var status remotePackageInstallStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return fmt.Errorf("decode remote install response: %w", err)
	}
	if status.JobID == "" {
		return fmt.Errorf("gateway did not return an installation job id")
	}
	seen := 0
	for {
		for _, message := range status.Messages[seen:] {
			fmt.Printf("→ [Remote] %s\n", message)
		}
		seen = len(status.Messages)
		switch status.Status {
		case "succeeded":
			fmt.Printf("✓ [%s] Package installed successfully.\n", targetDescription())
			return nil
		case "failed":
			if status.Error == "" {
				status.Error = "remote package installation failed"
			}
			return fmt.Errorf("remote package installation failed: %s", status.Error)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		data, err = apiCallWithTimeout(http.MethodGet, "/packages/install/"+url.PathEscape(status.JobID), nil, 30*time.Second)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &status); err != nil {
			return fmt.Errorf("decode remote install status: %w", err)
		}
		if seen > len(status.Messages) {
			seen = 0
		}
	}
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
	packages, err := discoverMCPPackages(dir)
	if err != nil {
		return "", err
	}
	if len(packages) > 0 {
		return urlPackageMCP, nil
	}
	return "", unsupportedMCPRepositoryError()
}

func repositorySupportsPackageKind(dir string, kind urlPackageKind) bool {
	switch kind {
	case urlPackageSkill:
		return fileExists(filepath.Join(dir, "SKILL.md"))
	case urlPackageMCP:
		packages, err := discoverMCPPackages(dir)
		return err == nil && len(packages) > 0
	default:
		return false
	}
}

func unsupportedMCPRepositoryError() error {
	return fmt.Errorf("repository is neither a Soulacy Skill (SKILL.md) nor a recognizable MCP server; expected a root MCP package or independently runnable packages under servers/*")
}

// discoverMCPPackages deliberately checks only the repository root and direct
// children of servers/. Bounded discovery supports normal MCP monorepos without
// turning every nested package, fixture, or vendored dependency into executable
// code. A candidate must have a supported manifest and an unambiguous entrypoint.
func discoverMCPPackages(root string) ([]mcpPackage, error) {
	var rootErr error
	if repositoryLooksLikeMCP(root) {
		pkg, err := inspectMCPPackage(root, ".")
		if err == nil {
			return []mcpPackage{pkg}, nil
		}
		rootErr = err
	}

	serversDir := filepath.Join(root, "servers")
	entries, err := os.ReadDir(serversDir)
	if err != nil {
		if os.IsNotExist(err) {
			if rootErr != nil {
				return nil, rootErr
			}
			return nil, nil
		}
		return nil, fmt.Errorf("inspect MCP bundle: %w", err)
	}
	if len(entries) > 64 {
		return nil, fmt.Errorf("MCP bundle contains %d entries under servers/; maximum is 64", len(entries))
	}
	var packages []mcpPackage
	var rejected []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		dir := filepath.Join(serversDir, entry.Name())
		if !repositoryLooksLikeMCP(dir) {
			continue
		}
		pkg, inspectErr := inspectMCPPackage(dir, filepath.ToSlash(filepath.Join("servers", entry.Name())))
		if inspectErr != nil {
			rejected = append(rejected, fmt.Sprintf("%s: %v", filepath.ToSlash(filepath.Join("servers", entry.Name())), inspectErr))
			continue
		}
		packages = append(packages, pkg)
	}
	if len(rejected) > 0 {
		sort.Strings(rejected)
		return nil, fmt.Errorf("detected an MCP bundle, but some servers are not safely runnable:\n  - %s", strings.Join(rejected, "\n  - "))
	}
	if len(packages) == 0 && rootErr != nil {
		return nil, rootErr
	}
	sort.Slice(packages, func(i, j int) bool { return packages[i].RelDir < packages[j].RelDir })
	return packages, nil
}

func repositoryLooksLikeMCP(dir string) bool {
	if fileExists(filepath.Join(dir, "server.json")) || fileExists(filepath.Join(dir, "mcp_server.py")) {
		return true
	}
	return (fileExists(filepath.Join(dir, "pyproject.toml")) || fileExists(filepath.Join(dir, "package.json"))) && repositoryMentionsMCP(dir)
}

func inspectMCPPackage(dir, relDir string) (mcpPackage, error) {
	pkg := mcpPackage{RelDir: relDir, SourceDir: dir}
	if manifest, err := readMCPServerManifest(filepath.Join(dir, "server.json")); err == nil {
		pkg.Manifest = manifest
		pkg.Name = manifest.Name
	}
	if fileExists(filepath.Join(dir, "pyproject.toml")) {
		var project pythonProject
		b, err := os.ReadFile(filepath.Join(dir, "pyproject.toml"))
		if err != nil || toml.Unmarshal(b, &project) != nil {
			return pkg, fmt.Errorf("could not read pyproject.toml")
		}
		if pkg.Name == "" {
			pkg.Name = project.Project.Name
		}
		if len(project.Project.Scripts) == 0 {
			entrypoint, err := discoverPythonMCPEntrypoint(dir)
			if err != nil {
				return pkg, err
			}
			pkg.Entrypoint = entrypoint
		}
	}
	if pkg.Name == "" && fileExists(filepath.Join(dir, "package.json")) {
		var project nodeProject
		b, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err != nil || json.Unmarshal(b, &project) != nil {
			return pkg, fmt.Errorf("could not read package.json")
		}
		pkg.Name = project.Name
		if firstNodeBin(project.Bin) == "" {
			if _, ok := project.Scripts["start"]; !ok {
				return pkg, fmt.Errorf("node MCP package has neither a bin entry nor a start script")
			}
		}
	}
	if pkg.Name == "" {
		pkg.Name = filepath.Base(dir)
	}
	envSource := dir
	if pkg.Entrypoint != "" {
		envSource = filepath.Join(dir, pkg.Entrypoint)
	}
	pkg.EnvVars = discoverMCPEnvironmentReferences(envSource)
	return pkg, nil
}

var (
	pythonEnvCallPattern  = regexp.MustCompile(`(?i)(?:os\.)?(?:getenv|environ\.get)\(\s*["']([A-Z][A-Z0-9_]*)["']`)
	pythonEnvIndexPattern = regexp.MustCompile(`(?i)os\.environ\s*\[\s*["']([A-Z][A-Z0-9_]*)["']\s*\]`)
	nodeEnvPattern        = regexp.MustCompile(`(?i)process\.env\.([A-Z][A-Z0-9_]*)`)
)

func discoverMCPEnvironmentReferences(path string) []string {
	seen := map[string]bool{}
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	var files []string
	if info.IsDir() {
		entries, readErr := os.ReadDir(path)
		if readErr != nil {
			return nil
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				files = append(files, filepath.Join(path, entry.Name()))
			}
		}
	} else {
		files = []string{path}
	}
	for _, file := range files {
		ext := strings.ToLower(filepath.Ext(file))
		if ext != ".py" && ext != ".js" && ext != ".mjs" && ext != ".cjs" && ext != ".ts" {
			continue
		}
		b, readErr := os.ReadFile(file)
		if readErr != nil {
			continue
		}
		for _, pattern := range []*regexp.Regexp{pythonEnvCallPattern, pythonEnvIndexPattern, nodeEnvPattern} {
			for _, match := range pattern.FindAllSubmatch(b, -1) {
				if len(match) > 1 {
					seen[strings.ToUpper(string(match[1]))] = true
				}
			}
		}
	}
	return sortedKeys(seen)
}

// discoverPythonMCPEntrypoint accepts only a top-level Python file that both
// constructs an MCP server and runs it. The directory-matching filename is a
// deterministic convention used by many uv-created MCP monorepos. Otherwise
// there must be exactly one runnable candidate; ambiguity is an error.
func discoverPythonMCPEntrypoint(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("inspect Python MCP entrypoint: %w", err)
	}
	var candidates []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".py" {
			continue
		}
		b, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		if readErr != nil {
			return "", fmt.Errorf("read Python MCP candidate %s: %w", entry.Name(), readErr)
		}
		compact := strings.ToLower(strings.ReplaceAll(string(b), " ", ""))
		constructsServer := strings.Contains(compact, "fastmcp(") || strings.Contains(compact, "server(")
		runsServer := strings.Contains(compact, ".run(")
		if constructsServer && runsServer {
			candidates = append(candidates, entry.Name())
		}
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("python MCP package has no [project.scripts] entry and no statically recognizable server runner")
	}
	sort.Strings(candidates)
	preferred := []string{filepath.Base(dir) + ".py", "mcp_server.py", "server.py"}
	for _, name := range preferred {
		for _, candidate := range candidates {
			if candidate == name {
				return candidate, nil
			}
		}
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return "", fmt.Errorf("python MCP package has multiple possible entrypoints (%s); add [project.scripts] to select one", strings.Join(candidates, ", "))
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
	packages, err := discoverMCPPackages(probe)
	if err != nil {
		return err
	}
	if len(packages) == 0 {
		return unsupportedMCPRepositoryError()
	}

	doc, root, err := loadConfigDoc(ws.ConfigFile)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	servers := ensureMapping(ensureMapping(root, "mcp"), "servers")
	type installPlan struct {
		pkg            mcpPackage
		id             string
		dest           string
		envRefs        map[string]string
		repairLauncher string
	}
	seenIDs := map[string]string{}
	var plans []installPlan
	for _, pkg := range packages {
		id := packageInstallID(pkg.Name)
		if id == "" && pkg.RelDir == "." {
			id = packageInstallID(source)
		}
		if id == "" {
			return fmt.Errorf("could not derive a safe MCP server id for %s", pkg.RelDir)
		}
		if first, duplicate := seenIDs[id]; duplicate {
			return fmt.Errorf("MCP bundle server id %q is duplicated by %s and %s", id, first, pkg.RelDir)
		}
		seenIDs[id] = pkg.RelDir
		dest := filepath.Join(ws.Root, "mcp-servers", id)
		if existing := yamlMapValue(servers, id); existing != nil {
			command := ""
			if commandNode := yamlMapValue(existing, "command"); commandNode != nil {
				command = commandNode.Value
			}
			if kind := legacyManagedLauncherKind(dest, command); kind != "" {
				plans = append(plans, installPlan{pkg: pkg, id: id, dest: dest, repairLauncher: kind})
				continue
			}
			fmt.Printf("✓ MCP server %q is already registered; skipping it.\n", id)
			continue
		}
		if _, statErr := os.Stat(dest); statErr == nil {
			return fmt.Errorf("MCP install directory already exists at %s but the server is not registered; inspect or remove that partial installation before retrying", dest)
		} else if !os.IsNotExist(statErr) {
			return fmt.Errorf("inspect MCP install directory %s: %w", dest, statErr)
		}
		envNames, envErr := mcpPackageEnvironmentNames(pkg)
		if envErr != nil {
			return fmt.Errorf("inspect MCP environment for %s: %w", pkg.RelDir, envErr)
		}
		envRefs := make(map[string]string, len(envNames))
		for _, name := range envNames {
			envRefs[name] = name
		}
		plans = append(plans, installPlan{pkg: pkg, id: id, dest: dest, envRefs: envRefs})
	}
	if len(plans) == 0 {
		fmt.Println("✓ Every MCP server in this repository is already registered; no packages were reinstalled.")
		return nil
	}

	for _, plan := range plans {
		fmt.Printf("→ Inspecting MCP server %q from %s\n", plan.id, plan.pkg.RelDir)
		report := (&introspect.Pipeline{DryRun: &introspect.DryRunConfig{Timeout: 5 * time.Second}}).Run(ctx, plan.pkg.SourceDir, nil)
		printSecurityReport(os.Stdout, report)
		if report.Verdict == introspect.VerdictDanger {
			return fmt.Errorf("refusing MCP bundle before making changes: safety introspection returned DANGER for %s", plan.pkg.RelDir)
		}
	}
	if !assumeYes {
		ids := make([]string, 0, len(plans))
		for _, plan := range plans {
			ids = append(ids, plan.id)
		}
		fmt.Printf("Install and register %d MCP server(s) (%s) from %s? [y/N] ", len(plans), strings.Join(ids, ", "), source)
		var answer string
		_, _ = fmt.Scanln(&answer)
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			return fmt.Errorf("install aborted by user")
		}
	}

	var created []string
	var createdFiles []string
	committed := false
	defer func() {
		if !committed {
			for _, path := range createdFiles {
				_ = os.Remove(path)
			}
			for _, dest := range created {
				_ = os.RemoveAll(dest)
			}
		}
	}()

	for _, plan := range plans {
		if plan.repairLauncher != "" {
			line := `exec "${0%/*}/../venv/bin/python" "$@"`
			if plan.repairLauncher != "python" {
				line = `exec ` + plan.repairLauncher + ` "$@"`
			}
			launcher, repairErr := writeManagedMCPLauncher(plan.dest, plan.repairLauncher, line)
			if repairErr != nil {
				return fmt.Errorf("repair MCP server %q launcher: %w", plan.id, repairErr)
			}
			createdFiles = append(createdFiles, launcher)
			setScalar(yamlMapValue(servers, plan.id), "command", launcher, yaml.DoubleQuotedStyle)
			continue
		}
		sourceDir := filepath.Join(plan.dest, "source")
		if err := os.MkdirAll(plan.dest, 0o755); err != nil {
			return err
		}
		created = append(created, plan.dest)
		if err := copyPackageDir(plan.pkg.SourceDir, sourceDir); err != nil {
			return fmt.Errorf("persist MCP source for %s: %w", plan.pkg.RelDir, err)
		}

		command, args, installErr := installMCPRuntime(ctx, plan.dest, sourceDir, plan.pkg.Entrypoint)
		if installErr != nil {
			return fmt.Errorf("install MCP server %q from %s: %w", plan.id, plan.pkg.RelDir, installErr)
		}
		if plan.pkg.Entrypoint != "" {
			if verifyErr := verifyScriptMCPStartup(ctx, command, args); verifyErr != nil {
				return fmt.Errorf("verify MCP server %q from %s: %w", plan.id, plan.pkg.RelDir, verifyErr)
			}
		}
		if !fileExists(command) && !executableOnPath(command) {
			return fmt.Errorf("registered command %q for %s could not be verified", command, plan.id)
		}
		srv := ensureMapping(servers, plan.id)
		setScalar(srv, "transport", "stdio", 0)
		setScalar(srv, "command", command, yaml.DoubleQuotedStyle)
		setBoolScalar(srv, "managed_only", true)
		if len(args) > 0 {
			setSequence(srv, "args", args)
		}
		if len(plan.envRefs) > 0 {
			setStringMap(srv, "env_secret_refs", plan.envRefs)
		}
	}
	if err := saveConfigDoc(ws.ConfigFile, doc); err != nil {
		return fmt.Errorf("register MCP servers: %w", err)
	}
	committed = true

	for _, plan := range plans {
		if plan.repairLauncher != "" {
			fmt.Printf("✓ Repaired managed launcher for MCP server %q.\n", plan.id)
			continue
		}
		fmt.Printf("✓ Installed and registered MCP server %q from %s.\n", plan.id, plan.pkg.RelDir)
		if len(plan.envRefs) > 0 {
			fmt.Printf("  Vault-backed environment; configure missing values in Secrets: %s\n", strings.Join(sortedKeys(plan.envRefs), ", "))
		}
	}
	fmt.Printf("✓ Registered %d MCP server(s) atomically in %s.\n", len(plans), ws.ConfigFile)
	fmt.Println("✓ Config was written atomically; the gateway will hot-reload it.")
	return nil
}

// legacyManagedLauncherKind identifies registrations written by older package
// installers that pointed directly at a venv interpreter, node, or npm. Those
// commands cannot pass the managed-root process-start check. A repeat install
// upgrades only this installer-owned command path and preserves args, secrets,
// source, and every other server setting.
func legacyManagedLauncherKind(dest, command string) string {
	switch filepath.Clean(command) {
	case filepath.Join(dest, "venv", "bin", "python"):
		return "python"
	case "node":
		return "node"
	case "npm":
		return "npm"
	default:
		return ""
	}
}

func installMCPRuntime(ctx context.Context, dest, sourceDir, discoveredEntrypoint string) (string, []string, error) {
	if fileExists(filepath.Join(sourceDir, "pyproject.toml")) {
		var project pythonProject
		b, err := os.ReadFile(filepath.Join(sourceDir, "pyproject.toml"))
		if err != nil || toml.Unmarshal(b, &project) != nil {
			return "", nil, fmt.Errorf("read pyproject.toml")
		}
		scripts := sortedKeys(project.Project.Scripts)
		python, err := selectPythonInterpreter(ctx, project.Project.RequiresPython)
		if err != nil {
			return "", nil, err
		}
		venv := filepath.Join(dest, "venv")
		if out, err := exec.CommandContext(ctx, python, "-m", "venv", venv).CombinedOutput(); err != nil {
			return "", nil, fmt.Errorf("create MCP venv: %v: %s", err, strings.TrimSpace(string(out)))
		}
		pip := filepath.Join(venv, "bin", "pip")
		installCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		if len(scripts) > 0 {
			if out, err := exec.CommandContext(installCtx, pip, "install", sourceDir).CombinedOutput(); err != nil {
				return "", nil, fmt.Errorf("install Python MCP package: %v: %s", err, tailText(string(out), 4000))
			}
			return filepath.Join(venv, "bin", scripts[0]), nil, nil
		}
		if discoveredEntrypoint == "" {
			return "", nil, fmt.Errorf("python MCP repository has no [project.scripts] entrypoint")
		}
		dependencies, err := validatedPythonRegistryDependencies(project.Project.Dependencies)
		if err != nil {
			return "", nil, err
		}
		dependencies, err = applyKnownPythonMCPCompatibility(filepath.Join(sourceDir, discoveredEntrypoint), dependencies)
		if err != nil {
			return "", nil, err
		}
		if len(dependencies) == 0 {
			return "", nil, fmt.Errorf("script-style Python MCP package declares no registry dependencies")
		}
		pipArgs := append([]string{"install"}, dependencies...)
		if out, err := exec.CommandContext(installCtx, pip, pipArgs...).CombinedOutput(); err != nil {
			return "", nil, fmt.Errorf("install Python MCP dependencies: %v: %s", err, tailText(string(out), 4000))
		}
		launcher, err := writeManagedMCPLauncher(dest, "python", `exec "${0%/*}/../venv/bin/python" "$@"`)
		if err != nil {
			return "", nil, err
		}
		return launcher, []string{filepath.Join(sourceDir, discoveredEntrypoint)}, nil
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
			launcher, err := writeManagedMCPLauncher(dest, "node", `exec node "$@"`)
			if err != nil {
				return "", nil, err
			}
			return launcher, []string{filepath.Join(sourceDir, bin)}, nil
		}
		if _, ok := project.Scripts["start"]; ok {
			launcher, err := writeManagedMCPLauncher(dest, "npm", `exec npm "$@"`)
			if err != nil {
				return "", nil, err
			}
			return launcher, []string{"--prefix", sourceDir, "start"}, nil
		}
		return "", nil, fmt.Errorf("node MCP repository has neither a bin entry nor a start script")
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
		launcher, err := writeManagedMCPLauncher(dest, "python", `exec "${0%/*}/../venv/bin/python" "$@"`)
		if err != nil {
			return "", nil, err
		}
		return launcher, []string{server}, nil
	}
	return "", nil, fmt.Errorf("MCP repository has no supported Python or Node package manifest")
}

// writeManagedMCPLauncher creates an installer-owned executable beneath the
// managed MCP directory. Python virtual environments commonly implement
// bin/python as a symlink to the host interpreter; registering that symlink
// directly is correctly rejected by the runtime's anti-escape check. The
// fixed launcher remains beneath the managed root while delegating to the
// environment that the installer created. launchLine is always a hard-coded
// installer value, never package-controlled input.
func writeManagedMCPLauncher(dest, name, launchLine string) (string, error) {
	binDir := filepath.Join(dest, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", fmt.Errorf("create managed MCP launcher directory: %w", err)
	}
	path := filepath.Join(binDir, "soulacy-mcp-"+name)
	body := []byte("#!/bin/sh\nset -eu\n" + launchLine + "\n")
	if err := os.WriteFile(path, body, 0o700); err != nil {
		return "", fmt.Errorf("write managed MCP launcher: %w", err)
	}
	return path, nil
}

var pythonMinimumPattern = regexp.MustCompile(`(?:^|,)\s*>=\s*([0-9]+)\.([0-9]+)(?:\.([0-9]+))?`)

func pythonRequirementMinimum(requirement string) (pythonVersion, bool) {
	match := pythonMinimumPattern.FindStringSubmatch(strings.TrimSpace(requirement))
	if len(match) == 0 {
		return pythonVersion{}, false
	}
	var version pythonVersion
	if _, err := fmt.Sscanf(match[1]+"."+match[2], "%d.%d", &version.Major, &version.Minor); err != nil {
		return pythonVersion{}, false
	}
	if match[3] != "" {
		if _, err := fmt.Sscanf(match[3], "%d", &version.Patch); err != nil {
			return pythonVersion{}, false
		}
	}
	return version, true
}

func selectPythonInterpreter(ctx context.Context, requirement string) (string, error) {
	minimum, constrained := pythonRequirementMinimum(requirement)
	candidates := []string{"python3"}
	if constrained {
		minimumName := fmt.Sprintf("python%d.%d", minimum.Major, minimum.Minor)
		candidates = append([]string{minimumName}, candidates...)
		for minor := minimum.Minor + 1; minor <= minimum.Minor+4; minor++ {
			candidates = append(candidates, fmt.Sprintf("python%d.%d", minimum.Major, minor))
		}
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate)
		if err != nil || seen[path] {
			continue
		}
		seen[path] = true
		versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		out, runErr := exec.CommandContext(versionCtx, path, "-c", "import sys; print('%d.%d.%d' % sys.version_info[:3])").CombinedOutput()
		cancel()
		if runErr != nil {
			continue
		}
		var actual pythonVersion
		if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d.%d.%d", &actual.Major, &actual.Minor, &actual.Patch); err != nil {
			continue
		}
		if !constrained || !pythonVersionLess(actual, minimum) {
			return path, nil
		}
	}
	if constrained {
		return "", fmt.Errorf("python MCP package requires Python %s, but no compatible python3 interpreter was found", requirement)
	}
	return "", fmt.Errorf("python MCP package requires python3, but it was not found")
}

func pythonVersionLess(a, b pythonVersion) bool {
	if a.Major != b.Major {
		return a.Major < b.Major
	}
	if a.Minor != b.Minor {
		return a.Minor < b.Minor
	}
	return a.Patch < b.Patch
}

func applyKnownPythonMCPCompatibility(entrypoint string, dependencies []string) ([]string, error) {
	b, err := os.ReadFile(entrypoint)
	if err != nil {
		return nil, fmt.Errorf("read Python MCP entrypoint for compatibility check: %w", err)
	}
	compact := strings.ToLower(string(b))
	compact = strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "").Replace(compact)
	if !strings.Contains(compact, "frommcp.server.fastmcpimport") {
		return dependencies, nil
	}
	out := append([]string(nil), dependencies...)
	for i, dependency := range out {
		lower := strings.ToLower(dependency)
		if lower != "mcp" && !strings.HasPrefix(lower, "mcp>") && !strings.HasPrefix(lower, "mcp=") && !strings.HasPrefix(lower, "mcp!") && !strings.HasPrefix(lower, "mcp~") && !strings.HasPrefix(lower, "mcp<") {
			continue
		}
		if strings.Contains(lower, "<2") || strings.Contains(lower, "<=1") {
			return out, nil
		}
		if lower == "mcp" {
			out[i] = dependency + "<2"
		} else {
			out[i] = dependency + ",<2"
		}
		return out, nil
	}
	return nil, fmt.Errorf("python MCP entrypoint imports the MCP v1 FastMCP API but pyproject.toml does not declare the mcp dependency")
}

func verifyScriptMCPStartup(ctx context.Context, command string, args []string) error {
	verifyCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(verifyCtx, command, args...)
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.CombinedOutput()
	if verifyCtx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("startup check did not stop after stdin closed")
	}
	if err != nil {
		return fmt.Errorf("startup check failed: %v: %s", err, tailText(string(out), 2000))
	}
	return nil
}

// copyPackageDir persists reviewed source without following symlinks. Git
// repositories can contain symlinks pointing outside the checkout; following
// one here could copy an unrelated host file into a remotely managed package.
func copyPackageDir(src, dst string) error {
	const (
		maxFiles = 10_000
		maxBytes = int64(512 << 20)
	)
	files := 0
	var bytes int64
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("package path escapes source directory: %s", path)
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("package contains unsupported symlink %s", filepath.ToSlash(rel))
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("package contains unsupported non-regular file %s", filepath.ToSlash(rel))
		}
		files++
		bytes += info.Size()
		if files > maxFiles || bytes > maxBytes {
			return fmt.Errorf("package exceeds persistence limit (%d files or %d bytes)", maxFiles, maxBytes)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

var legacyRequirementPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(?:\[[A-Za-z0-9_,.-]+\])?(?:(?:==|!=|~=|>=|<=|>|<)[A-Za-z0-9.*+!_-]+(?:,(?:==|!=|~=|>=|<=|>|<)[A-Za-z0-9.*+!_-]+)*)?$`)

func validatedPythonRegistryDependencies(dependencies []string) ([]string, error) {
	if len(dependencies) > 128 {
		return nil, fmt.Errorf("python MCP package declares %d dependencies; maximum is 128", len(dependencies))
	}
	out := make([]string, 0, len(dependencies))
	for _, dependency := range dependencies {
		dependency = strings.TrimSpace(dependency)
		if !legacyRequirementPattern.MatchString(dependency) {
			return nil, fmt.Errorf("python MCP package contains an unsafe or unsupported dependency %q; only package-registry requirements are allowed", dependency)
		}
		out = append(out, dependency)
	}
	return out, nil
}

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

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func mcpPackageEnvironmentNames(pkg mcpPackage) ([]string, error) {
	seen := map[string]bool{}
	for _, name := range append(requiredMCPEnvironment(pkg.Manifest), pkg.EnvVars...) {
		if !environmentNamePattern.MatchString(name) {
			return nil, fmt.Errorf("invalid environment variable name %q", name)
		}
		seen[name] = true
	}
	return sortedKeys(seen), nil
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
