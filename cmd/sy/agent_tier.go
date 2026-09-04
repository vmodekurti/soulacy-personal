// agent_tier.go — `sy agent tier [id]` subcommand.
//
// Surfaces the capability classification from internal/tier so operators
// don't have to grep /tmp/soulacy.log or mentally walk the SOUL.yaml.
// Without an ID, prints every loaded agent in a one-line-per-agent
// table. With an ID, prints the tier plus the reasons that justified it
// — useful for debugging "why is this agent privileged?" before binding
// it to a non-web channel that needs accept_privileged_exposure.
//
// Reads agents directly from disk via runtime.Loader, same as `sy
// agent validate`. Does not call the gateway API, so it works on a host
// that hasn't started the server.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/tier"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func buildAgentTierCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tier [id]",
		Short: "Show an agent's capability tier (read_only | active | privileged)",
		Long: `Classifies an agent by what it can actually do — including transitive
peer reach. Use this before binding an agent to Telegram/Slack/Discord/
WhatsApp to see whether accept_privileged_exposure: true is required on
the binding.

With no id, prints every loaded agent in a table. With an id, prints the
tier plus the concrete reasons that produced it.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			loader, err := loadAgentsFromDisk()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				return printOneAgentTier(loader, args[0])
			}
			return printAllAgentTiers(loader)
		},
	}
	return cmd
}

// loadAgentsFromDisk constructs a runtime.Loader from the configured
// agent_dirs and triggers one LoadAll pass. Mirrors how the gateway
// initialises the loader at boot, just without the long-running watcher
// or scheduler. Errors from individual agent files are reported but
// don't abort the run — we want the operator to see classification for
// every agent that parses, even if one file is malformed.
func loadAgentsFromDisk() (*runtime.Loader, error) {
	// Resolution order, mirroring what the gateway does so `sy agent tier`
	// sees exactly the same agents the running gateway would:
	//
	//   1. Explicit agent_dirs in viper-loaded config.yaml — operator
	//      override, used when an install points at a non-default agent
	//      directory.
	//   2. config.ResolveWorkspace() — the canonical resolver. Handles
	//      the soulspace layout (~/.soulacy/soulspace/agents), legacy
	//      flat layout (~/.soulacy/agents), and the SOULACY_WORKSPACE
	//      env override in one place. This is the same call the gateway
	//      uses at boot.
	//
	// Hardcoding ~/.soulacy/agents here (as a prior version did) breaks
	// installs that use the soulspace layout — the actual install puts
	// agents at ~/.soulacy/soulspace/agents.
	dirs := viper.GetStringSlice("agent_dirs")
	if len(dirs) == 0 {
		ws, err := config.ResolveWorkspace()
		if err != nil {
			return nil, fmt.Errorf("resolve workspace: %w", err)
		}
		dirs = []string{ws.Agents}
	}
	loader := runtime.NewLoader(dirs)
	if errs := loader.LoadAll(); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "warning: %v\n", e)
		}
	}
	return loader, nil
}

func printOneAgentTier(loader *runtime.Loader, id string) error {
	def := loader.Get(id)
	if def == nil {
		return fmt.Errorf("agent %q not loaded — try `sy agent list`", id)
	}
	exp := tier.Explain(def, loader.Get)
	if outputJSON {
		// Reuse a tiny anonymous struct so the JSON shape matches the
		// API endpoint (internal/gateway returns {tier, reasons}).
		out := map[string]any{
			"agent_id": def.ID,
			"tier":     exp.Tier.String(),
			"reasons":  exp.Reasons,
		}
		data, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	fmt.Printf("Agent:  %s (%s)\n", def.ID, def.Name)
	fmt.Printf("Tier:   %s\n", exp.Tier)
	if len(exp.Reasons) == 0 {
		fmt.Println("Reason: (no privileged or active capabilities declared)")
		return nil
	}
	fmt.Println("Reasons:")
	for _, r := range exp.Reasons {
		fmt.Printf("  • %s\n", r)
	}
	return nil
}

func printAllAgentTiers(loader *runtime.Loader) error {
	defs := loader.All()
	if len(defs) == 0 {
		fmt.Println("No agents loaded.")
		return nil
	}
	// Stable order so successive runs produce identical output (useful
	// for diff'ing tier changes when an operator edits a YAML).
	sort.Slice(defs, func(i, j int) bool { return defs[i].ID < defs[j].ID })

	if outputJSON {
		out := make([]map[string]any, 0, len(defs))
		for _, d := range defs {
			exp := tier.Explain(d, loader.Get)
			out = append(out, map[string]any{
				"agent_id": d.ID,
				"tier":     exp.Tier.String(),
				"reasons":  exp.Reasons,
			})
		}
		data, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	fmt.Printf("%-32s %-12s %s\n", "AGENT", "TIER", "PRIMARY REASON")
	fmt.Printf("%-32s %-12s %s\n", "-----", "----", "--------------")
	for _, d := range defs {
		exp := tier.Explain(d, loader.Get)
		primary := "—"
		if len(exp.Reasons) > 0 {
			primary = exp.Reasons[0]
		}
		fmt.Printf("%-32s %-12s %s\n", d.ID, exp.Tier, primary)
	}
	return nil
}
