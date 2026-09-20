// engine_tools_shell.go — SYSTEM-partition shell/process built-ins.
//
// ARCH-2: mechanically extracted from engine.go (no behaviour change). These
// are the privileged "SYSTEM" tools (see privilegedSystemTools in engine.go):
// shell_exec, run_script, install_library. Offered only via the SEC-3 double
// opt-in (runtime.allow_system_tools + capabilities: [system]).
package runtime

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/internal/mcpinstall"
	"github.com/soulacy/soulacy/internal/netguard"
	"github.com/soulacy/soulacy/internal/sandbox"
)

var managedMCPIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// SetAgentShellEnv sets extra KEY=VALUE entries exposed to shell_exec /
// run_script subprocesses (canonical install-path hints). Called at boot.
func (e *Engine) SetAgentShellEnv(extra []string) {
	e.agentShellEnv = extra
}

// shellEnviron returns the environment for an agent shell subprocess: the
// SEC-5 allowlist (PATH/HOME/LANG/TMPDIR) plus the canonical-path hints.
//
// It used to return os.Environ() — or nil, which exec.Cmd treats as "inherit
// everything" — so shell_exec, run_script, python_eval and install_library all
// ran with the gateway's full environment. `env` was a working exfiltration
// primitive for ANTHROPIC_API_KEY, OPENAI_API_KEY, database URLs and anything
// else the operator exported.
//
// The allowlist it now uses is the same one internal/sandbox/env.go already
// applied to the agent Python path, whose comment states the goal outright:
// "most importantly gateway secrets such as ANTHROPIC_API_KEY … is withheld".
// One half of the codebase enforced that and the other did not.
//
// agentShellEnv entries are KEY=VALUE pairs the gateway sets deliberately
// (SOULACY_WORKSPACE and friends), so they are appended after filtering rather
// than looked up in the parent environment.
func (e *Engine) shellEnviron() []string {
	env := sandbox.FilteredEnv(os.Environ(), nil)
	return append(env, e.agentShellEnv...)
}

// buildShellTools returns the shell-domain OS-level built-in tools. Extracted
// from buildSystemTools (ARCH-2) — identical definitions, no behaviour change.
func (e *Engine) buildShellTools() []BuiltinTool {
	return []BuiltinTool{
		{
			Name:        "mcp_install_inspect",
			Gate:        "",
			Description: "Inspect an MCP server repository before choosing how to install it. Reads bounded README and manifest content without executing repository code, then returns deployment evidence and a baseline recommendation. Always call this first for an MCP installation URL; reason over the evidence before installing or giving directions.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"source_url": map[string]any{
						"type":        "string",
						"description": "Public HTTPS Git repository URL containing an MCP server",
					},
				},
				"required": []string{"source_url"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				sourceURL := strings.TrimSpace(argString(args, "source_url"))
				if sourceURL == "" {
					return "", fmt.Errorf("mcp_install_inspect: source_url is required")
				}
				recommendation, err := mcpinstall.AnalyzeRepository(ctx, sourceURL)
				if err != nil {
					return "", fmt.Errorf("mcp_install_inspect: %w", err)
				}
				return recommendation.PlanningText(), nil
			},
		},
		{
			Name:        "mcp_register_remote",
			Gate:        "",
			Description: "Register a provider-hosted HTTPS MCP endpoint that does not require credentials. Use it after repository inspection confirms remote connection is best. This typed config change requires operator approval. If authentication, URL variables, or the final endpoint are unresolved, provide setup directions instead.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "Stable Soulacy server id containing letters, numbers, hyphens, or underscores",
					},
					"url": map[string]any{
						"type":        "string",
						"description": "Final public HTTPS Streamable HTTP MCP endpoint",
					},
				},
				"required": []string{"name", "url"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				name := strings.TrimSpace(argString(args, "name"))
				if !managedMCPIDPattern.MatchString(name) {
					return "", fmt.Errorf("mcp_register_remote: name must be 1-64 letters, numbers, hyphens, or underscores")
				}
				endpoint := strings.TrimSpace(argString(args, "url"))
				u, err := url.ParseRequestURI(endpoint)
				if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
					return "", fmt.Errorf("mcp_register_remote: url must be a public HTTPS endpoint without embedded credentials")
				}
				if err := netguard.CheckPublic(endpoint); err != nil {
					return "", fmt.Errorf("mcp_register_remote: endpoint failed the public-network check: %w", err)
				}
				result, err := e.runManagedMCPRegistration(ctx, name, endpoint)
				if err != nil {
					return "", fmt.Errorf("mcp_register_remote: %w", err)
				}
				if e.mcpClient != nil {
					if err := e.mcpClient.AddServer(name, mcp.ServerConfig{Transport: "http", URL: endpoint}); err != nil {
						return "", fmt.Errorf("mcp_register_remote: saved registration, but connection verification failed: %w", err)
					}
					for _, server := range e.mcpClient.ServersSnapshot() {
						if server.ID == name {
							result += fmt.Sprintf("\nVerified live connection: %s.", server.Detail)
							break
						}
					}
				}
				return result, nil
			},
		},
		{
			Name:        "package_install",
			Gate:        "",
			Description: "Install and register a Soulacy Skill or a gateway-compatible MCP server from an HTTPS Git repository URL. For MCP requests, call mcp_install_inspect first and use this tool only when the evidence supports a self-contained gateway process. The installer performs safety inspection, uses persistent workspace paths, updates config, and verifies the result. Never substitute shell_exec for this task.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"source_url": map[string]any{
						"type":        "string",
						"description": "HTTPS Git repository URL containing a Soulacy Skill or MCP server",
					},
					"kind": map[string]any{
						"type":        "string",
						"enum":        []string{"auto", "skill", "mcp"},
						"description": "Package type. Use auto unless the operator explicitly identifies it (default: auto)",
					},
				},
				"required": []string{"source_url"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				sourceURL := strings.TrimSpace(argString(args, "source_url"))
				if sourceURL == "" {
					return "", fmt.Errorf("package_install: source_url is required")
				}
				if !strings.HasPrefix(strings.ToLower(sourceURL), "https://") {
					return "", fmt.Errorf("package_install: only HTTPS repository URLs are accepted")
				}
				kind := strings.ToLower(strings.TrimSpace(argString(args, "kind")))
				if kind == "" {
					kind = "auto"
				}
				if kind != "auto" && kind != "skill" && kind != "mcp" {
					return "", fmt.Errorf("package_install: kind must be auto, skill, or mcp")
				}
				result, err := e.runManagedPackageInstaller(ctx, sourceURL, kind)
				if err != nil {
					return "", fmt.Errorf("package_install: %w", err)
				}
				return result, nil
			},
		},
		{
			Name:        "shell_exec",
			Gate:        "",
			Description: "Execute a shell command inside the configured isolated runtime and return stdout + stderr combined. The workspace is mounted at /workspace; host credentials and networking are unavailable.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "Shell command to execute (passed to /bin/sh -c)",
					},
					"working_dir": map[string]any{
						"type":        "string",
						"description": "Optional working directory (must be a valid absolute path on the host. Do NOT guess paths like /home/user. Omit this parameter entirely to run in the default home directory.)",
					},
					"timeout_seconds": map[string]any{
						"type":        "integer",
						"description": "Max seconds to wait (default 60, max 600)",
					},
				},
				"required": []string{"command"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				command := strings.TrimSpace(argString(args, "command"))
				if command == "" {
					return "", fmt.Errorf("shell_exec: command is required")
				}
				workDir := e.defaultPrivilegedWorkDir()
				if requested := argString(args, "working_dir"); requested != "" {
					var err error
					workDir, err = e.resolveFilesystemPath(requested, false)
					if err != nil {
						return "", fmt.Errorf("shell_exec: working_dir: %w", err)
					}
				}
				timeoutSecs := argInt(args, "timeout_seconds", 60)
				if timeoutSecs <= 0 {
					timeoutSecs = 60
				}
				if timeoutSecs > 600 {
					timeoutSecs = 600
				}
				tctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSecs)*time.Second)
				defer cancel()
				return e.runPrivilegedCommand(tctx, PrivilegedCommand{Argv: []string{"/bin/sh", "-c", command}, WorkingDir: workDir, Env: e.shellEnviron()}, 8000)
			},
		},
		{
			Name:        "run_script",
			Gate:        "",
			Description: "Run a workspace script inside the configured isolated runtime. Interpreter is inferred from the extension or can be specified explicitly.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to the script (absolute or ~/)",
					},
					"args": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Command-line arguments to pass to the script",
					},
					"interpreter": map[string]any{
						"type":        "string",
						"description": "Override interpreter (e.g. 'python3', 'bash', 'node')",
					},
					"working_dir": map[string]any{
						"type":        "string",
						"description": "Working directory (defaults to script's directory)",
					},
				},
				"required": []string{"path"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				path, err := e.resolveFilesystemPath(argString(args, "path"), false)
				if err != nil {
					return "", fmt.Errorf("run_script: %w", err)
				}
				interp := argString(args, "interpreter")
				if interp == "" {
					switch strings.ToLower(filepath.Ext(path)) {
					case ".py":
						interp = "python3"
					case ".sh":
						interp = "bash"
					case ".js":
						interp = "node"
					case ".rb":
						interp = "ruby"
					default:
						interp = "bash"
					}
				}
				argv := []string{interp, path}
				for _, a := range argStringSlice(args, "args") {
					argv = append(argv, a)
				}
				workDir := filepath.Dir(path)
				if requested := argString(args, "working_dir"); requested != "" {
					workDir, err = e.resolveFilesystemPath(requested, false)
					if err != nil {
						return "", fmt.Errorf("run_script: working_dir: %w", err)
					}
				}
				tctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				defer cancel()
				return e.runPrivilegedCommand(tctx, PrivilegedCommand{Argv: argv, WorkingDir: workDir, Env: e.shellEnviron()}, 8000)
			},
		},
		{
			Name:        "python_eval",
			Gate:        "",
			Description: "Execute inline Python code dynamically. Useful for data munging, JSON parsing, algorithms, and formatting that are easier done in code. Returns the stdout/stderr of the execution.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"code": map[string]any{
						"type":        "string",
						"description": "Python code to execute. Can be a script with multiple lines and functions.",
					},
				},
				"required": []string{"code"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				code := argString(args, "code")
				tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				return e.runPrivilegedCommand(tctx, PrivilegedCommand{Argv: []string{"python3", "-c", code}, WorkingDir: e.defaultPrivilegedWorkDir(), Env: e.shellEnviron()}, 8000)
			},
		},
		{
			Name:        "install_library",
			Gate:        "",
			Description: "Install a library or package using pip (Python), npm (Node.js), brew (macOS), or apt (Linux). Returns installation output.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"package": map[string]any{
						"type":        "string",
						"description": "Package name (e.g. 'requests', 'lodash', 'pandas')",
					},
					"manager": map[string]any{
						"type":        "string",
						"enum":        []string{"pip", "pip3", "npm", "brew", "apt"},
						"description": "Package manager (default: pip3)",
					},
					"version": map[string]any{
						"type":        "string",
						"description": "Specific version to install (e.g. '2.0.1')",
					},
					"global": map[string]any{
						"type":        "boolean",
						"description": "Install globally for npm (npm -g flag)",
					},
				},
				"required": []string{"package"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				pkg := strings.TrimSpace(argString(args, "package"))
				if pkg == "" {
					return "", fmt.Errorf("install_library: package is required")
				}
				manager := argStringDefault(args, "manager", "pip3")
				version := argString(args, "version")
				if version != "" {
					pkg = pkg + "==" + version
				}
				isGlobal := argBool(args, "global")
				var argv []string
				switch manager {
				case "pip", "pip3":
					argv = []string{manager, "install", pkg, "--break-system-packages", "--quiet"}
				case "npm":
					argv = []string{"npm", "install"}
					if isGlobal {
						argv = append(argv, "-g")
					}
					argv = append(argv, pkg)
				case "brew":
					argv = []string{"brew", "install", pkg}
				case "apt":
					argv = []string{"apt-get", "install", "-y", pkg}
				default:
					return "", fmt.Errorf("install_library: unsupported manager %q", manager)
				}
				tctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				defer cancel()
				result, err := e.runPrivilegedCommand(tctx, PrivilegedCommand{Argv: argv, WorkingDir: e.defaultPrivilegedWorkDir(), Env: e.shellEnviron()}, 4000)
				if strings.Contains(result, "exit_code: non-zero") {
					return "Installation failed:\n" + result, nil
				}
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Successfully installed %s:\n%s", pkg, result), nil
			},
		},
	}
}

// runManagedPackageInstaller invokes the typed installer directly on the host.
// This is intentionally not run through the generic privileged-command
// sandbox: package installation requires outbound network access and must
// persist changes in the real Soulacy workspace. Safety comes from the fixed
// argv below, HTTPS-only validation in the caller, the installer's own package
// inspection, and the mandatory runtime approval gate. No model-controlled
// value is interpreted by a shell.
func (e *Engine) runManagedPackageInstaller(ctx context.Context, sourceURL, kind string) (string, error) {
	syPath, err := exec.LookPath("sy")
	if err != nil {
		return "", fmt.Errorf("soulacy CLI 'sy' was not found in PATH: %w", err)
	}
	cmd := exec.CommandContext(ctx, syPath,
		"package", "install", sourceURL,
		"--kind", kind,
		"--yes",
		"--allow-unverified",
	)
	cmd.Env = e.shellEnviron()
	out, runErr := cmd.CombinedOutput()
	result := strings.TrimSpace(string(out))
	if len(result) > 16000 {
		result = result[len(result)-16000:]
	}
	if runErr != nil {
		if result == "" {
			result = runErr.Error()
		}
		return "", fmt.Errorf("installer failed: %s", result)
	}
	return result, nil
}

func (e *Engine) runManagedMCPRegistration(ctx context.Context, name, endpoint string) (string, error) {
	syPath, err := exec.LookPath("sy")
	if err != nil {
		return "", fmt.Errorf("soulacy CLI 'sy' was not found in PATH: %w", err)
	}
	argv := []string{"mcp", "add", "--name", name, "--transport", "http", "--url", endpoint}
	cmd := exec.CommandContext(ctx, syPath, argv...)
	cmd.Env = e.shellEnviron()
	out, runErr := cmd.CombinedOutput()
	result := strings.TrimSpace(string(out))
	if len(result) > 8000 {
		result = result[len(result)-8000:]
	}
	if runErr != nil {
		if result == "" {
			result = runErr.Error()
		}
		return "", fmt.Errorf("registration failed: %s", result)
	}
	return result, nil
}

func (e *Engine) defaultPrivilegedWorkDir() string {
	if strings.TrimSpace(e.privilegedWorkDir) != "" {
		return e.privilegedWorkDir
	}
	if len(e.filesystemRoots) > 0 {
		return e.filesystemRoots[0]
	}
	return ""
}
