package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/soulacy/soulacy/internal/eval"
	"github.com/soulacy/soulacy/internal/studio"
	"github.com/spf13/cobra"
)

func buildEvalCmd() *cobra.Command {
	var agentID string
	var suiteFile string
	var jsonOut bool
	var tags []string
	var repeat int
	var failFast bool

	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Run an evaluation suite against an agent",
		Long: `Run a structured eval suite against a live agent and report pass/fail.

Suite file format (JSON):
  {
    "name": "smoke test",
    "cases": [
      {"name":"greeting","input":"Hello!","expected_contains":["hello","hi"]},
      {"name":"math","input":"What is 2+2?","expected_contains":["4"]}
    ]
  }

Examples:
  sy eval --agent my-agent --suite tests/smoke.json
  sy eval --agent my-agent --suite tests/smoke.json --json
  sy eval --agent my-agent --suite evals/golden --tag weather --repeat 3
  sy eval --agent my-agent --suite evals/golden --tag channel --fail-fast`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if agentID == "" {
				return fmt.Errorf("--agent is required")
			}
			if suiteFile == "" {
				return fmt.Errorf("--suite is required")
			}
			return runEval(agentID, suiteFile, eval.RunOptions{
				Tags:     tags,
				Repeat:   repeat,
				FailFast: failFast,
			}, jsonOut)
		},
	}
	cmd.Flags().StringVar(&agentID, "agent", "", "Agent ID to evaluate")
	cmd.Flags().StringVarP(&suiteFile, "suite", "s", "", "Path to an eval suite (JSON or YAML) or a directory of suites")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output results as JSON")
	cmd.Flags().StringArrayVar(&tags, "tag", nil, "Run only cases with this tag (repeatable or comma-separated)")
	cmd.Flags().IntVar(&repeat, "repeat", 1, "Run each selected case N times to expose flakiness and latency variance")
	cmd.Flags().BoolVar(&failFast, "fail-fast", false, "Stop after the first failed or errored case")
	cmd.AddCommand(buildEvalGenerationCmd())
	return cmd
}

// buildEvalGenerationCmd runs the deterministic Studio generation-robustness
// corpus (offline, no gateway/LLM): each sample is a raw generated draft that
// the normalize+validate pipeline should repair (or, for the controls, flag).
// Exits non-zero on any regression so it runs in CI.
func buildEvalGenerationCmd() *cobra.Command {
	var extraCorpus string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "generation",
		Short: "Score Studio workflow-generation robustness (deterministic, offline)",
		Long: `Run the generation-robustness corpus: raw generated drafts covering known
failure classes are pushed through the deterministic normalize+validate pipeline
and scored on whether they become valid, renderable flows — no gateway or LLM.

Add cases distilled from accepted repairs with --corpus (a JSON array of
{"name","raw","recoverable"}).

Examples:
  sy eval generation
  sy eval generation --corpus ~/.soulacy/studio-corpus.json --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			samples := studio.BuiltinGenerationCorpus()
			if extraCorpus != "" {
				data, err := os.ReadFile(extraCorpus)
				if err != nil {
					return fmt.Errorf("read corpus %s: %w", extraCorpus, err)
				}
				extra, err := studio.LoadGenSamples(data)
				if err != nil {
					return fmt.Errorf("parse corpus %s: %w", extraCorpus, err)
				}
				samples = append(samples, extra...)
			}
			rep := studio.RunGenerationCorpus(samples)
			if jsonOut {
				out, _ := json.MarshalIndent(rep, "", "  ")
				fmt.Println(string(out))
			} else {
				fmt.Printf("Generation robustness: %d/%d recoverable drafts repaired deterministically (%.0f%%)\n",
					rep.Recovered, rep.RecoverableTotal, rep.Rate())
				for _, f := range rep.Failures {
					if f.Expected {
						fmt.Printf("  ✗ %s — should have become valid: %v\n", f.Name, f.Errors)
					} else {
						fmt.Printf("  ✗ %s — invalid draft slipped through as valid\n", f.Name)
					}
				}
			}
			if len(rep.Failures) > 0 {
				return fmt.Errorf("%d generation-robustness regression(s)", len(rep.Failures))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&extraCorpus, "corpus", "", "Path to an extra corpus JSON file (array of {name,raw,recoverable})")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output the report as JSON")
	return cmd
}

func runEval(agentID, suitePath string, opts eval.RunOptions, jsonOut bool) error {
	suiteFiles, err := collectSuiteFiles(suitePath)
	if err != nil {
		return err
	}

	runner := eval.NewRunner(gatewayURL, apiKey, agentID)
	var allResults []eval.Result
	failures := 0

	for _, sf := range suiteFiles {
		data, err := os.ReadFile(sf)
		if err != nil {
			return fmt.Errorf("failed to read suite file %s: %w", sf, err)
		}
		// LoadSuite accepts both JSON and YAML.
		suite, err := eval.LoadSuite(data)
		if err != nil {
			return fmt.Errorf("%s: %w", sf, err)
		}
		results, err := runner.RunWithOptions(context.Background(), suite, opts)
		if err != nil {
			return fmt.Errorf("eval run failed for %s: %w", sf, err)
		}
		if !jsonOut && len(suiteFiles) > 1 {
			fmt.Printf("\n=== %s (%s) ===\n", suite.Name, filepath.Base(sf))
		}
		if !jsonOut {
			eval.PrintReport(results, os.Stdout)
		}
		for _, r := range results {
			// Skipped cases (e.g. missing secrets) are not failures.
			if r.Skipped {
				continue
			}
			if !r.Passed || r.Error != nil {
				failures++
			}
		}
		allResults = append(allResults, results...)
	}

	if jsonOut {
		out, err := json.MarshalIndent(allResults, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal results: %w", err)
		}
		fmt.Println(string(out))
	}

	if failures > 0 {
		return fmt.Errorf("%d case(s) failed", failures)
	}
	return nil
}

// collectSuiteFiles returns the suite files to run: a single file, or every
// .json/.yaml/.yml file directly inside a directory (sorted for determinism).
func collectSuiteFiles(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read suite path %s: %w", path, err)
	}
	if !info.IsDir() {
		return []string{path}, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read suite dir %s: %w", path, err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".json", ".yaml", ".yml":
			files = append(files, filepath.Join(path, e.Name()))
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no eval suites (*.json/*.yaml) found in %s", path)
	}
	sort.Strings(files)
	return files, nil
}
