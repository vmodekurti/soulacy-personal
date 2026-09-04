package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/updates"
)

func buildUpdateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Check and install release updates",
	}
	cmd.AddCommand(buildUpdateCheckCmd())
	cmd.AddCommand(buildUpdateInstallCmd())
	return cmd
}

func buildUpdateCheckCmd() *cobra.Command {
	var manifestSource string
	var currentVersion string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check whether a newer Soulacy release is available",
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := updates.CheckForUpdate(cmd.Context(), resolveUpdateManifestSource(manifestSource), currentVersion)
			if err != nil {
				return err
			}
			if outputJSON {
				data, _ := json.MarshalIndent(res, "", "  ")
				fmt.Println(string(data))
				return nil
			}
			fmt.Println(res.Message)
			if res.Artifact != nil {
				fmt.Printf("Artifact: %s (%s/%s, sha256 %s)\n", res.Artifact.Name, res.Artifact.OS, res.Artifact.Arch, res.Artifact.SHA256)
				if res.Artifact.URL != "" {
					fmt.Println("URL:", res.Artifact.URL)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&manifestSource, "manifest", "", "Release manifest URL or local file path")
	cmd.Flags().StringVar(&currentVersion, "current", "", "Override current version for testing")
	return cmd
}

func buildUpdateInstallCmd() *cobra.Command {
	var manifestSource string
	var currentVersion string
	var installDir string
	var dryRun bool
	var yes bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Download, verify, and install a newer Soulacy release",
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := updates.InstallUpdate(cmd.Context(), updates.UpdateInstallOptions{
				ManifestSource: resolveUpdateManifestSource(manifestSource),
				CurrentVersion: currentVersion,
				InstallDir:     installDir,
				DryRun:         dryRun,
				Yes:            yes,
			})
			if err != nil {
				return err
			}
			if outputJSON {
				data, _ := json.MarshalIndent(res, "", "  ")
				fmt.Println(string(data))
				return nil
			}
			fmt.Println(res.Message)
			if len(res.Backups) > 0 {
				fmt.Println("Backups:")
				for _, b := range res.Backups {
					fmt.Println(" -", b)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&manifestSource, "manifest", "", "Release manifest URL or local file path")
	cmd.Flags().StringVar(&currentVersion, "current", "", "Override current version for testing")
	cmd.Flags().StringVar(&installDir, "install-dir", "", "Directory to install soulacy and sy into (default: current executable directory)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Verify what would be installed without replacing binaries")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm installation without an interactive prompt")
	return cmd
}

func buildUpgradeCmd() *cobra.Command {
	var manifestSource string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Self-upgrade the running Soulacy and sy binaries to the latest release",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Checking for updates...")
			res, err := updates.InstallUpdate(cmd.Context(), updates.UpdateInstallOptions{
				ManifestSource: resolveUpdateManifestSource(manifestSource),
				CurrentVersion: config.Version,
				DryRun:         dryRun,
				Yes:            true, // auto-approve upgrade from the simple cmd
			})
			if err != nil {
				return err
			}
			fmt.Println(res.Message)
			if len(res.Backups) > 0 {
				fmt.Println("Backups created:")
				for _, b := range res.Backups {
					fmt.Println(" -", b)
				}
			}
			if res.Installed {
				fmt.Println("✓ Upgrade complete. Restart the gateway server (soulacy serve) to run the new version.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&manifestSource, "manifest", "", "Release manifest URL or local file path")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Verify what would be installed without replacing binaries")
	return cmd
}

func resolveUpdateManifestSource(explicit string) string {
	if s := strings.TrimSpace(explicit); s != "" {
		return s
	}
	if s := strings.TrimSpace(os.Getenv("SOULACY_UPDATE_MANIFEST")); s != "" {
		return s
	}
	return strings.TrimSpace(viper.GetString("updates.manifest_url"))
}
