package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/supportbundle"
)

func buildSupportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "support",
		Short: "Create redacted diagnostics for support",
	}
	cmd.AddCommand(buildSupportBundleCmd())
	return cmd
}

func buildDoctorBundleCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "bundle",
		Short: "Create a redacted support bundle",
		Long: `Create a redacted ZIP archive with doctor output, workspace metadata,
masked config, masked agent manifests, and recent log tails.

The bundle is designed for bug reports and production support. It never copies
the secrets directory, database files, attachments, or full action logs.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := writeSupportBundle(out)
			if err != nil {
				return err
			}
			if outputJSON {
				resp := map[string]any{"ok": true, "path": path}
				data, _ := json.MarshalIndent(resp, "", "  ")
				fmt.Println(string(data))
				return nil
			}
			fmt.Println("Support bundle written:", path)
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "out", "o", "", "Output .zip path (default: ./soulacy-support-<timestamp>.zip)")
	return cmd
}

func buildSupportBundleCmd() *cobra.Command {
	return buildDoctorBundleCmd()
}

func writeSupportBundle(out string) (string, error) {
	path, _, err := supportbundle.WriteFile(out, supportBundleOptions())
	return path, err
}

func supportBundleOptions() supportbundle.Options {
	ws, _ := config.ResolveWorkspace()
	agentDirs := viper.GetStringSlice("agent_dirs")
	if len(agentDirs) == 0 && ws.Agents != "" {
		agentDirs = []string{ws.Agents}
	}
	logDirs := []string{}
	if ws.Logs != "" {
		logDirs = append(logDirs, ws.Logs)
	}
	if home, err := os.UserHomeDir(); err == nil {
		logDirs = append(logDirs, filepath.Join(home, ".soulacy", "logs"))
	}
	return supportbundle.Options{
		GatewayURL: gatewayURL,
		ConfigPath: viperConfigPath(),
		AgentDirs:  agentDirs,
		LogDirs:    logDirs,
		Workspace:  supportWorkspaceMap(ws),
		Doctor:     collectDoctorReport(),
		ExtraJSON: map[string]any{
			"operator_evidence": supportOperatorEvidence(gatewayURL),
			"release": map[string]any{
				"version":         config.Version,
				"update_manifest": resolveUpdateManifestSource(""),
				"updates_ready":   resolveUpdateManifestSource("") != "",
				"dry_run_command": "sy update install --dry-run",
				"install_command": "sy update install --yes",
			},
		},
	}
}

func supportOperatorEvidence(baseURL string) map[string]any {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	endpoint := func(path string) string {
		if baseURL == "" {
			return path
		}
		return baseURL + path
	}
	return map[string]any{
		"source": "cli",
		"note":   "This bundle was created from the CLI. Live browser, mobile, chat, run-ledger, and admin-audit evidence is richer when downloaded from the running gateway.",
		"live_gateway_artifacts": []map[string]string{
			{"name": "readiness.json", "endpoint": endpoint("/api/v1/readiness")},
			{"name": "browser_status.json", "endpoint": endpoint("/api/v1/browser/status")},
			{"name": "mobile_status.json", "endpoint": endpoint("/api/v1/mobile/status")},
			{"name": "chat_status.json", "endpoint": endpoint("/api/v1/chat/status")},
			{"name": "support_bundle.zip", "endpoint": endpoint("/api/v1/support/bundle")},
		},
		"bundle_command": "sy support bundle",
	}
}

func viperConfigPath() string {
	if p := strings.TrimSpace(viper.ConfigFileUsed()); p != "" {
		return p
	}
	if ws, err := config.ResolveWorkspace(); err == nil {
		if _, statErr := os.Stat(ws.ConfigFile); statErr == nil {
			return ws.ConfigFile
		}
	}
	return ""
}

func supportWorkspaceMap(ws config.Paths) map[string]string {
	if ws.Root == "" {
		return nil
	}
	return map[string]string{
		"root":       ws.Root,
		"agents":     ws.Agents,
		"logs":       ws.Logs,
		"skills":     ws.Skills,
		"mcpServers": filepath.Join(ws.Root, "mcp-servers"),
	}
}
