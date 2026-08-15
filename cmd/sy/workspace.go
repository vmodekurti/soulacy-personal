// workspace.go — `sy workspace` commands: inspect the resolved workspace
// ("soulspace") and migrate a legacy flat ~/.soulacy installation into it.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/tenancy"
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

	cmd.AddCommand(buildWorkspaceListCmd())
	cmd.AddCommand(buildWorkspaceUseCmd())

	var assumeYes, dryRun, tenantPlan bool
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
			if tenantPlan {
				ws, err := config.ResolveWorkspace()
				if err != nil {
					return err
				}
				plan, err := tenancy.PlanPersonalMigration(ws)
				if err != nil {
					return err
				}
				fmt.Printf("Personal tenant migration plan\nCatalog: %s\nSchema version: %d\n", plan.DatabasePath, plan.Version)
				if plan.AlreadyDone {
					fmt.Println("Status: already applied (re-running is idempotent)")
				} else {
					fmt.Println("Status: pending; the gateway applies this additive migration on startup")
				}
				fmt.Printf("\nExisting stores to assign (%d):\n", len(plan.Resources))
				for _, resource := range plan.Resources {
					fmt.Printf("  %-18s %s\n", resource.Kind, resource.Path)
				}
				fmt.Println("\n--plan: read-only; no files or databases were changed.")
				return nil
			}
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
	migrateCmd.Flags().BoolVar(&tenantPlan, "plan", false, "Show the read-only implicit tenant migration plan")
	cmd.AddCommand(migrateCmd)

	return cmd
}

// buildWorkspaceListCmd asks the server which workspaces this principal may
// act in. The answer comes from stored memberships on every call, so a
// suspended or removed member stops seeing a workspace immediately rather than
// when a cached token happens to expire.
func buildWorkspaceListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the workspaces you can act in on the current server",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := apiCall("GET", "/workspace/workspaces", nil)
			if err != nil {
				return err
			}
			var result struct {
				Workspaces        []workspaceView `json:"workspaces"`
				ActiveWorkspaceID string          `json:"active_workspace_id"`
			}
			if err := json.Unmarshal(data, &result); err != nil {
				return fmt.Errorf("decode workspaces: %w", err)
			}
			if outputJSON {
				return emitJSON(result)
			}
			if len(result.Workspaces) == 0 {
				fmt.Println("No workspaces available to this principal.")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			fmt.Fprintln(w, "\tWORKSPACE\tNAME\tORGANIZATION\tROLE\tPRINCIPAL")
			for _, ws := range result.Workspaces {
				marker := " "
				if ws.WorkspaceID == result.ActiveWorkspaceID {
					marker = "*"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", marker, ws.WorkspaceID,
					orNone(ws.WorkspaceName), orNone(ws.OrganizationName), orNone(ws.Role), orNone(ws.PrincipalKind))
			}
			return w.Flush()
		},
	}
}

// buildWorkspaceUseCmd asks the server to verify a workspace selection before
// storing it. The CLI never assumes a workspace ID is usable just because the
// user typed it: an unauthorized workspace is reported as not found.
func buildWorkspaceUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <workspace-id>",
		Short: "Target a workspace in the current context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			requested := strings.TrimSpace(args[0])
			body, err := json.Marshal(map[string]string{"workspace_id": requested})
			if err != nil {
				return err
			}
			data, err := apiCall("POST", "/workspace/select", body)
			if err != nil {
				return err
			}
			var verified workspaceView
			if err := json.Unmarshal(data, &verified); err != nil {
				return fmt.Errorf("decode workspace selection: %w", err)
			}
			file, err := loadContexts()
			if err != nil {
				return err
			}
			current := strings.TrimSpace(file.Current)
			if current == "" {
				// Nothing to persist into, but the selection is still valid for
				// this process, and saying so beats a silent no-op.
				activeWorkspaceID = verified.WorkspaceID
				if outputJSON {
					return emitJSON(map[string]any{"workspace": verified, "persisted": false})
				}
				fmt.Fprintln(os.Stderr, "No current context, so this selection applies to this command only.")
				fmt.Fprintln(os.Stderr, "Create one with 'sy context add <name> --server "+gatewayURL+"'.")
				return nil
			}
			ctx := file.Contexts[current]
			ctx.WorkspaceID, ctx.OrganizationID, ctx.Role = verified.WorkspaceID, verified.OrganizationID, verified.Role
			file.Contexts[current] = ctx
			if err := saveContexts(file); err != nil {
				return err
			}
			if outputJSON {
				return emitJSON(map[string]any{"workspace": verified, "persisted": true, "context": ctx})
			}
			fmt.Printf("Context %s now targets workspace %s (%s) as %s.\n", current, verified.WorkspaceID, orNone(verified.WorkspaceName), orNone(verified.Role))
			return nil
		},
	}
}
