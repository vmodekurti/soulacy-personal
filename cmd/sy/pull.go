package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/soulacy/soulacy/internal/config"
)

func buildPullCmd() *cobra.Command {
	var outputDir string
	var force bool

	cmd := &cobra.Command{
		Use:   "pull <url-or-id>",
		Short: "Pull an agent definition from a URL or the Soulacy registry",
		Long: `Pull a SOUL.yaml agent definition and save it locally.

Examples:
  sy pull my-agent                    # pull from Soulacy registry by ID
  sy pull https://raw.githubusercontent.com/org/repo/main/agent.yaml
  sy pull --dir ~/.soulacy/agents my-agent`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPull(args[0], outputDir, force)
		},
	}
	homeDir, _ := os.UserHomeDir()
	defaultAgents := filepath.Join(homeDir, ".soulacy", "agents")
	if ws, werr := config.ResolveWorkspace(); werr == nil {
		defaultAgents = ws.Agents
	}
	cmd.Flags().StringVarP(&outputDir, "dir", "d", defaultAgents,
		"Directory to save the pulled agent YAML")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Overwrite existing file without prompt")
	return cmd
}

const registryManifestURL = "https://raw.githubusercontent.com/soulacy/registry/main/index.json"

type registryEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

type registryManifest struct {
	Agents []registryEntry `json:"agents"`
}

func fetchURL(rawURL string) ([]byte, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, rawURL)
	}
	return io.ReadAll(resp.Body)
}

func runPull(ref, outputDir string, force bool) error {
	var yamlBytes []byte
	var resolvedURL string

	switch {
	case strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://"):
		// Direct URL fetch
		resolvedURL = ref
		data, err := fetchURL(resolvedURL)
		if err != nil {
			return err
		}
		yamlBytes = data

	case strings.Contains(ref, "/"):
		// owner/repo shorthand → GitHub raw URL
		resolvedURL = "https://raw.githubusercontent.com/" + ref + "/main/SOUL.yaml"
		data, err := fetchURL(resolvedURL)
		if err != nil {
			return fmt.Errorf("failed to fetch agent from GitHub shorthand %q: %w", ref, err)
		}
		yamlBytes = data

	default:
		// Plain ID — look up in registry manifest
		manifestData, err := fetchURL(registryManifestURL)
		if err != nil {
			return fmt.Errorf("failed to fetch registry manifest: %w", err)
		}
		var manifest registryManifest
		if err := json.Unmarshal(manifestData, &manifest); err != nil {
			return fmt.Errorf("failed to parse registry manifest: %w", err)
		}
		var found *registryEntry
		for i := range manifest.Agents {
			if manifest.Agents[i].ID == ref {
				found = &manifest.Agents[i]
				break
			}
		}
		if found == nil {
			ids := make([]string, 0, len(manifest.Agents))
			for _, a := range manifest.Agents {
				ids = append(ids, a.ID)
			}
			fmt.Fprintf(os.Stderr, "Agent ID %q not found in registry.\nAvailable agents: %s\n", ref, strings.Join(ids, ", "))
			return fmt.Errorf("agent %q not found in registry", ref)
		}
		resolvedURL = found.URL
		data, err := fetchURL(resolvedURL)
		if err != nil {
			return fmt.Errorf("failed to fetch agent %q from %s: %w", ref, resolvedURL, err)
		}
		yamlBytes = data
	}

	// Basic validation: must be valid UTF-8 and contain "id:"
	if !utf8.Valid(yamlBytes) {
		return fmt.Errorf("fetched content is not valid UTF-8 — expected a YAML file")
	}
	if !strings.Contains(string(yamlBytes), "id:") {
		return fmt.Errorf("fetched content does not look like a SOUL.yaml (missing 'id:' field)")
	}

	// Extract agent ID from YAML
	agentID := extractYAMLID(yamlBytes)
	if agentID == "" {
		// Fall back to last path segment of URL or ref
		agentID = lastPathSegment(resolvedURL)
		if agentID == "" {
			agentID = lastPathSegment(ref)
		}
		// Strip .yaml extension if present
		agentID = strings.TrimSuffix(agentID, ".yaml")
		agentID = strings.TrimSuffix(agentID, ".yml")
	}
	if agentID == "" {
		agentID = "agent"
	}

	// Ensure output directory exists
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory %s: %w", outputDir, err)
	}

	destPath := filepath.Join(outputDir, agentID+".yaml")

	// Check if file exists and prompt if needed
	if !force {
		if _, err := os.Stat(destPath); err == nil {
			fmt.Printf("File exists. Overwrite? [y/N]: ")
			scanner := bufio.NewScanner(os.Stdin)
			scanner.Scan()
			answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
			if answer != "y" && answer != "yes" {
				fmt.Println("Aborted.")
				return nil
			}
		}
	}

	if err := os.WriteFile(destPath, yamlBytes, 0644); err != nil {
		return fmt.Errorf("failed to write file %s: %w", destPath, err)
	}

	fmt.Printf("✓ Pulled agent '%s' → %s\n", agentID, destPath)
	return nil
}

// extractYAMLID finds the first line matching `^id:\s*(\S+)` and returns the ID value.
func extractYAMLID(data []byte) string {
	re := regexp.MustCompile(`(?m)^id:\s*(\S+)`)
	matches := re.FindSubmatch(data)
	if len(matches) >= 2 {
		return string(matches[1])
	}
	return ""
}

// lastPathSegment returns the last non-empty segment of a URL or path.
func lastPathSegment(s string) string {
	// Strip query string and fragment
	if idx := strings.IndexAny(s, "?#"); idx >= 0 {
		s = s[:idx]
	}
	s = strings.TrimRight(s, "/")
	if idx := strings.LastIndexAny(s, "/\\"); idx >= 0 {
		return s[idx+1:]
	}
	return s
}
