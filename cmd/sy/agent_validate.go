package main

import (
	"encoding/json"
	"fmt"

	"github.com/soulacy/soulacy/internal/agentvalidate"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func buildAgentValidateCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "validate [SOUL.yaml]",
		Short: "Validate an agent SOUL.yaml file",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if file == "" && len(args) > 0 {
				file = args[0]
			}
			if file == "" {
				return fmt.Errorf("provide a SOUL.yaml path or --file")
			}
			report, err := agentvalidate.File(file, agentValidationOptions())
			if err != nil {
				return err
			}
			if outputJSON {
				data, _ := json.MarshalIndent(report, "", "  ")
				fmt.Println(string(data))
			} else {
				printValidationReport(report)
			}
			if !report.Valid {
				return fmt.Errorf("validation failed with %d error(s)", report.Errors)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "Path to SOUL.yaml")
	return cmd
}

func agentValidationOptions() agentvalidate.Options {
	cfg := &config.Config{
		LLM: config.LLMConfig{
			DefaultProvider: viper.GetString("llm.default_provider"),
			Providers:       map[string]config.ProviderConfig{},
		},
	}
	providerIDs := map[string]bool{}
	for _, id := range []string{"ollama", "openai", "anthropic", "google", "groq", "mistral", "openrouter"} {
		providerIDs[id] = true
	}
	for id := range viper.GetStringMap("llm.providers") {
		providerIDs[id] = true
	}
	for id := range providerIDs {
		base := "llm.providers." + id
		if !viper.IsSet(base) {
			continue
		}
		cfg.LLM.Providers[id] = config.ProviderConfig{
			BaseURL: viper.GetString(base + ".base_url"),
			APIKey:  viper.GetString(base + ".api_key"),
			Model:   viper.GetString(base + ".model"),
		}
	}

	models := map[string][]string{}
	if pc, ok := cfg.LLM.Providers["ollama"]; ok {
		baseURL := pc.BaseURL
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		if names := fetchOllamaModels(baseURL); len(names) > 0 {
			models["ollama"] = names
		}
	}
	return agentvalidate.Options{
		Config:         cfg,
		ProviderModels: models,
	}
}

func printValidationReport(report agentvalidate.Report) {
	status := "[ok]"
	if report.Errors > 0 {
		status = "[fail]"
	} else if report.Warnings > 0 {
		status = "[warn]"
	}
	path := report.Path
	if path == "" {
		path = report.AgentID
	}
	fmt.Printf("%s %s\n", status, path)
	if len(report.Findings) == 0 {
		fmt.Println("No findings.")
		return
	}
	for _, f := range report.Findings {
		fmt.Printf("[%s] %-18s %s\n", f.Severity, f.Field, f.Message)
		if f.Suggestion != "" {
			fmt.Printf("      %-18s %s\n", "suggestion:", f.Suggestion)
		}
		if len(f.Alternatives) > 0 {
			fmt.Printf("      %-18s %s\n", "alternatives:", joinValidationValues(f.Alternatives))
		}
	}
}

func joinValidationValues(values []string) string {
	out := ""
	for i, value := range values {
		if i > 0 {
			out += ", "
		}
		out += value
	}
	return out
}
