package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/soulacy/soulacy/internal/config"
)

// buildSeedExamplesCmd copies the bundled example agents into the workspace and
// scaffolds any missing python tool files so they load and run (as stubs) on a
// fresh install, instead of failing because a referenced tool file, skill, or
// MCP server isn't present. It prints what still needs configuring per agent.
func buildSeedExamplesCmd() *cobra.Command {
	var from string
	var force bool
	cmd := &cobra.Command{
		Use:   "seed-examples",
		Short: "Copy example agents into your workspace and scaffold their tool files",
		Long: `Copy the bundled example agents into your workspace agents directory and
scaffold any python tool files they reference with helpful stubs.

The example agents in the repo are references, not turnkey: several assume tool
files, skills, MCP servers, or specific models that a fresh install does not
have. This command makes them LOAD and run as safe stubs, and reports what each
one still needs before it does real work.

By default it reads from ./examples/agents (run it from a repo checkout). On an
install without the repo, point it at a downloaded copy:

    sy seed-examples --from /path/to/soulacy/examples/agents`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSeedExamples(from, force)
		},
	}
	cmd.Flags().StringVar(&from, "from", "examples/agents", "directory of example agents to seed from")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an agent that already exists in the workspace")
	return cmd
}

var pythonFileRe = regexp.MustCompile(`(?m)python_file:\s*(\S+)`)

func runSeedExamples(from string, force bool) error {
	srcInfo, err := os.Stat(from)
	if err != nil || !srcInfo.IsDir() {
		return fmt.Errorf("example source %q not found — run from a repo checkout or pass --from <dir>", from)
	}
	ws, err := config.ResolveWorkspace()
	if err != nil {
		return fmt.Errorf("cannot resolve workspace: %w", err)
	}
	if err := os.MkdirAll(ws.Agents, 0o755); err != nil {
		return fmt.Errorf("create agents dir: %w", err)
	}

	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}

	seeded, skipped, scaffolded := 0, 0, 0
	var notes []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		srcDir := filepath.Join(from, name)
		soul := filepath.Join(srcDir, "SOUL.yaml")
		if _, statErr := os.Stat(soul); statErr != nil {
			continue // not an agent dir
		}
		dstDir := filepath.Join(ws.Agents, name)
		if _, statErr := os.Stat(dstDir); statErr == nil && !force {
			skipped++
			fmt.Printf("• skip   %s (already in workspace; use --force to overwrite)\n", name)
			continue
		}
		if err := copyTree(srcDir, dstDir); err != nil {
			return fmt.Errorf("copy %s: %w", name, err)
		}
		seeded++
		fmt.Printf("✓ seed   %s\n", name)

		body, _ := os.ReadFile(soul)
		text := string(body)

		// Scaffold missing python tool files referenced by the agent.
		for _, m := range pythonFileRe.FindAllStringSubmatch(text, -1) {
			p := expandHome(strings.Trim(m[1], `"'`))
			if p == "" {
				continue
			}
			if _, statErr := os.Stat(p); statErr == nil {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				notes = append(notes, fmt.Sprintf("%s: could not create %s: %v", name, filepath.Dir(p), err))
				continue
			}
			if err := os.WriteFile(p, []byte(toolStub(name, p)), 0o644); err != nil {
				notes = append(notes, fmt.Sprintf("%s: could not write stub %s: %v", name, p, err))
				continue
			}
			scaffolded++
			fmt.Printf("    ↳ stubbed tool: %s\n", p)
		}

		// Report prerequisites this agent still needs.
		var needs []string
		if strings.Contains(text, "skills:") {
			needs = append(needs, "skills (install via Skills page or `sy skill`)")
		}
		if strings.Contains(text, "mcp_servers:") || strings.Contains(text, "mcp_tools:") {
			needs = append(needs, "MCP server(s) configured (MCP page or `sy mcp`)")
		}
		if strings.Contains(text, "system_tools: true") || strings.Contains(text, "allow_shell: true") {
			needs = append(needs, "runtime.allow_system_tools + operator review")
		}
		if prov := firstProvider(text); prov != "" && prov != "ollama" {
			needs = append(needs, "API key for provider "+prov)
		}
		if len(needs) > 0 {
			notes = append(notes, fmt.Sprintf("%s still needs: %s", name, strings.Join(needs, "; ")))
		}
	}

	fmt.Printf("\nSeeded %d agent(s), skipped %d, scaffolded %d tool stub(s) into %s\n", seeded, skipped, scaffolded, ws.Agents)
	if len(notes) > 0 {
		fmt.Println("\nBefore these do real work:")
		for _, n := range notes {
			fmt.Printf("  - %s\n", n)
		}
	}
	fmt.Println("\nTool stubs return a placeholder message — edit them to add real logic.")
	fmt.Println("Restart the gateway (or it will hot-reload) to pick up the new agents.")
	return nil
}

func toolStub(agentName, path string) string {
	fn := "run"
	return fmt.Sprintf(`"""Auto-generated stub for the %q agent's tool.

This file was scaffolded by `+"`sy seed-examples`"+` so the agent loads and runs.
Replace the body of %s() with your real implementation. The tool receives its
arguments as a single dict and should return a string or JSON-serializable value.
"""


def %s(args):
    return (
        "[stub] Tool %s is not implemented yet. "
        "Edit this file to add real logic."
    )
`, agentName, fn, fn, filepath.Base(path))
}

// copyTree recursively copies src into dst.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

func expandHome(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				return home
			}
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

var providerLineRe = regexp.MustCompile(`(?m)^\s*provider:\s*(\S+)`)

func firstProvider(text string) string {
	if m := providerLineRe.FindStringSubmatch(text); m != nil {
		return strings.Trim(m[1], `"'`)
	}
	return ""
}
