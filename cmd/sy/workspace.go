// workspace.go — `sy workspace` commands: inspect the resolved workspace
// ("soulspace") and migrate a legacy flat ~/.soulacy installation into it.
package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/wsmigrate"
)

func buildWorkspaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspace",
		Short: "Inspect and manage the Soulacy workspace (soulspace)",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "info",
		Short: "Show the resolved workspace layout",
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := config.ResolveWorkspace()
			if err != nil {
				return err
			}
			mode := "soulspace"
			if ws.Legacy {
				mode = "legacy (flat ~/.soulacy — run `sy workspace migrate` to organize)"
			}
			fmt.Printf("Workspace: %s\nLayout:    %s\n\n", ws.Root, mode)
			rows := [][2]string{
				{"config", ws.ConfigFile},
				{"agents", ws.Agents},
				{"skills", ws.Skills},
				{"plugins", ws.Plugins},
				{"templates", ws.Templates},
				{"tools", ws.Tools},
				{"memory", ws.Memory},
				{"databases", ws.Data},
				{"logs", ws.Logs},
				{"audit", ws.Audit},
				{"secrets", ws.Secrets},
				{"registry", ws.Registry},
			}
			for _, r := range rows {
				fmt.Printf("  %-10s %s\n", r[0], r[1])
			}
			return nil
		},
	})

	var assumeYes, dryRun bool
	migrateCmd := &cobra.Command{
		Use:   "migrate",
		Short: "Move a legacy flat ~/.soulacy installation into the organized soulspace layout",
		Long: `Move a legacy flat ~/.soulacy installation into ~/.soulacy/soulspace.

Everything known moves to its organized location (databases under data/,
the credential vault under secrets/); unknown files stay where they are
and are listed. Absolute legacy paths inside config.yaml are rewritten so
configured locations follow their files.

STOP THE GATEWAY FIRST — databases move as files.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, err := wsmigrate.Plan()
			if err != nil {
				return err
			}
			fmt.Printf("Migration: %s → %s\n\nPlanned moves (%d):\n", plan.From, plan.To, len(plan.Moves))
			for _, m := range plan.Moves {
				fmt.Printf("  %s\n    → %s\n", m.From, m.To)
			}
			if len(plan.LeftInPlace) > 0 {
				fmt.Printf("\nLeft in place (unrecognized — move manually if needed):\n")
				for _, l := range plan.LeftInPlace {
					fmt.Printf("  %s\n", l)
				}
			}
			if dryRun {
				fmt.Println("\n--dry-run: nothing moved.")
				return nil
			}
			fmt.Println("\n⚠ Stop the gateway before continuing — databases move as files.")
			if !assumeYes {
				fmt.Print("Proceed with the migration? [y/N] ")
				var answer string
				_, _ = fmt.Scanln(&answer)
				a := strings.ToLower(strings.TrimSpace(answer))
				if a != "y" && a != "yes" {
					return fmt.Errorf("migration aborted by user")
				}
			}
			if err := wsmigrate.Apply(plan); err != nil {
				return err
			}
			fmt.Printf("✓ Migrated to %s — restart the gateway.\n", plan.To)
			if len(plan.LeftInPlace) > 0 {
				fmt.Printf("  (%d unrecognized entr%s left in %s)\n",
					len(plan.LeftInPlace), pluralYIes(len(plan.LeftInPlace)), plan.From)
			}
			return nil
		},
	}
	migrateCmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "Skip the confirmation prompt")
	migrateCmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the plan without moving anything")
	cmd.AddCommand(migrateCmd)

	return cmd
}
