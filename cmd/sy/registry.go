// registry.go — Story E26: review skill-source URLs and manage the
// `registries:` config block from the CLI.
//
//	sy registry list           — configured sources
//	sy registry probe <url>    — review what a URL is (skills.sh directory,
//	                             E19 registry, git host, plain page)
//	sy registry add <url>      — probe + consent + save to config.yaml
//
// Probing runs client-side (no gateway needed). Saving goes through the
// gateway API when reachable so the GUI sees the change immediately; when
// the gateway is down the entry is appended to config.yaml directly.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/pkgregistry"
)

func buildRegistryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "registry",
		Short: "Manage skill sources (package registries)",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List configured skill sources",
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiGet("/registries", "registries")
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "probe <url>",
		Short: "Review a URL as a potential skill source",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			rep, err := pkgregistry.Probe(ctx, args[0])
			if err != nil {
				return err
			}
			if outputJSON {
				data, _ := json.MarshalIndent(rep, "", "  ")
				fmt.Println(string(data))
				return nil
			}
			fmt.Print(formatProbeReport(rep))
			return nil
		},
	})

	var addID string
	var addPriority int
	var addYes bool
	addCmd := &cobra.Command{
		Use:   "add <url>",
		Short: "Review a URL and add it as a skill source",
		Long: `Probe the URL, show what was found, and on consent append the suggested
entry to the registries: block in config.yaml. Example:

  sy registry add https://www.skills.sh/

Afterwards skills resolve by slug:  sy skill install owner/repo/skill`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			rep, err := pkgregistry.Probe(ctx, args[0])
			if err != nil {
				return err
			}
			fmt.Print(formatProbeReport(rep))
			if rep.Suggested == nil {
				return fmt.Errorf("nothing to add — this URL is not a recognisable registry")
			}
			entry := *rep.Suggested
			if addID != "" {
				entry.ID = addID
			}
			if addPriority != 0 {
				entry.Priority = addPriority
			}
			if !addYes {
				fmt.Printf("\nAdd source %q (type %s%s) to config.yaml? [y/N] ",
					entry.ID, entry.Type, baseURLSuffix(entry))
				var answer string
				_, _ = fmt.Scanln(&answer)
				a := strings.ToLower(strings.TrimSpace(answer))
				if a != "y" && a != "yes" {
					fmt.Println("aborted")
					return nil
				}
			}
			return addRegistryEntry(entry)
		},
	}
	addCmd.Flags().StringVar(&addID, "id", "", "Override the suggested source id")
	addCmd.Flags().IntVar(&addPriority, "priority", 0, "Resolution priority (lower runs first)")
	addCmd.Flags().BoolVarP(&addYes, "yes", "y", false, "Skip the confirmation prompt")
	cmd.AddCommand(addCmd)

	return cmd
}

func baseURLSuffix(e config.RegistryConfig) string {
	if e.BaseURL == "" {
		return ""
	}
	return ", " + e.BaseURL
}

// formatProbeReport renders a probe report for the terminal.
func formatProbeReport(rep pkgregistry.ProbeReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "URL:    %s\n", rep.URL)
	fmt.Fprintf(&b, "Kind:   %s\n", rep.Kind)
	fmt.Fprintf(&b, "Review: %s\n", rep.Detail)
	if rep.HasAudits {
		b.WriteString("Audits: third-party security audits available; Soulacy's own introspection still runs on every install\n")
	}
	if len(rep.Samples) > 0 {
		b.WriteString("Found:\n")
		for _, s := range rep.Samples {
			fmt.Fprintf(&b, "  - %s\n", s)
		}
	}
	if rep.Suggested != nil {
		fmt.Fprintf(&b, "Suggested config entry: id=%s type=%s", rep.Suggested.ID, rep.Suggested.Type)
		if rep.Suggested.BaseURL != "" {
			fmt.Fprintf(&b, " base_url=%s", rep.Suggested.BaseURL)
		}
		fmt.Fprintf(&b, " priority=%d\n", rep.Suggested.Priority)
	}
	return b.String()
}

// addRegistryEntry saves the entry via the gateway when reachable, falling
// back to a direct config.yaml append (registry changes are config-only).
func addRegistryEntry(entry config.RegistryConfig) error {
	payload, _ := json.Marshal(map[string]any{
		"id": entry.ID, "type": entry.Type,
		"base_url": entry.BaseURL, "priority": entry.Priority,
	})
	if _, err := apiCall("POST", "/registries", payload); err == nil {
		fmt.Printf("✓ source %q saved via gateway. Try: sy skill install <slug>\n", entry.ID)
		return nil
	}
	// Gateway down → write config.yaml directly.
	ws, err := config.ResolveWorkspace()
	if err != nil {
		return err
	}
	if err := appendRegistryToConfigFile(ws.ConfigFile, entry); err != nil {
		return err
	}
	fmt.Printf("✓ source %q appended to %s (gateway not running). Try: sy skill install <slug>\n",
		entry.ID, ws.ConfigFile)
	return nil
}

// appendRegistryToConfigFile appends one registries: entry to the YAML
// config, preserving every other block (raw-map round-trip, the same
// discipline as the gateway's config writes). Duplicate ids are refused.
func appendRegistryToConfigFile(path string, entry config.RegistryConfig) error {
	current := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(data, &current); err != nil {
			return fmt.Errorf("config unreadable: %w", err)
		}
		if current == nil {
			current = map[string]any{}
		}
	}
	raw, _ := current["registries"].([]any)
	for _, e := range raw {
		if m, ok := e.(map[string]any); ok {
			if id, _ := m["id"].(string); id == entry.ID {
				return fmt.Errorf("a registry with id %q already exists", entry.ID)
			}
		}
	}
	m := map[string]any{"id": entry.ID, "type": entry.Type}
	if entry.BaseURL != "" {
		m["base_url"] = strings.TrimRight(entry.BaseURL, "/")
	}
	if entry.Priority != 0 {
		m["priority"] = entry.Priority
	}
	current["registries"] = append(raw, m)
	data, err := yaml.Marshal(current)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
