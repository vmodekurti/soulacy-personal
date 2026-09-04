package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

type launchReadiness struct {
	Summary struct {
		Status          string `json:"status"`
		Score           int    `json:"score"`
		ReadyItems      int    `json:"ready_items"`
		WarningItems    int    `json:"warning_items"`
		BlockerItems    int    `json:"blocker_items"`
		TotalItems      int    `json:"total_items"`
		ProvidersReady  int    `json:"providers_ready"`
		ChannelsReady   int    `json:"channels_ready"`
		Agents          int    `json:"agents"`
		EnabledAgents   int    `json:"enabled_agents"`
		ChatAgents      int    `json:"chat_agents"`
		ScheduledAgents int    `json:"scheduled_agents"`
		LearningAgents  int    `json:"learning_agents"`
		Templates       int    `json:"templates"`
		UpdatesReady    bool   `json:"updates_ready"`
	} `json:"summary"`
	Journey     []launchReadinessItem `json:"journey"`
	NextActions []launchReadinessItem `json:"next_actions"`
	Parity      struct {
		Score   int                `json:"score"`
		Areas   []launchParityArea `json:"areas"`
		TopGaps []launchParityArea `json:"top_gaps"`
	} `json:"parity"`
	Release struct {
		Version        string `json:"version"`
		UpdateManifest string `json:"update_manifest"`
		UpdatesReady   bool   `json:"updates_ready"`
		UpdateHint     string `json:"update_hint"`
		DryRunCommand  string `json:"dry_run_command"`
		InstallCommand string `json:"install_command"`
	} `json:"release"`
	Deployment struct {
		Profile string `json:"profile"`
		Label   string `json:"label"`
		Status  string `json:"status"`
		Score   int    `json:"score"`
		Ready   int    `json:"ready"`
		Total   int    `json:"total"`
		Strict  bool   `json:"strict"`
		Owner   string `json:"owner"`
		Region  string `json:"region"`
	} `json:"deployment"`
	LaunchChecklist []launchChecklistItem `json:"launch_checklist"`
}

type launchParityArea struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Status    string `json:"status"`
	Score     int    `json:"score"`
	Detail    string `json:"detail"`
	Next      string `json:"next"`
	Benchmark string `json:"benchmark"`
	Href      string `json:"href,omitempty"`
}

type launchReadinessItem struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Href   string `json:"href,omitempty"`
}

type launchChecklistItem struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Remedy string `json:"remedy,omitempty"`
	Href   string `json:"href,omitempty"`
}

func buildLaunchCmd() *cobra.Command {
	var strict bool
	var minScore int
	cmd := &cobra.Command{
		Use:   "launch",
		Short: "Check production launch readiness",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "check",
		Short: "Show the launch-readiness score and next actions",
		Long: `Show the same production-readiness journey used by the Dashboard.

The default mode is advisory and exits 0 when the gateway responds. Use
--strict in CI or before a production rollout to fail unless Soulacy reports
status=ready. Use --min-score to enforce a minimum readiness score while
still showing the full operator checklist.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLaunchCheck(strict, minScore)
		},
	})
	cmd.Commands()[0].Flags().BoolVar(&strict, "strict", false, "Exit non-zero unless launch readiness is ready")
	cmd.Commands()[0].Flags().IntVar(&minScore, "min-score", 0, "Exit non-zero unless launch readiness score is at least this value")
	cmd.AddCommand(buildLaunchCertifyCmd())
	cmd.AddCommand(buildLaunchProofCmd())
	return cmd
}

type launchProofOptions struct {
	OutputDir string
	Strict    bool
	MinScore  int
}

type launchCertifyOptions struct {
	ReportDir     string
	Quick         bool
	LiveChannels  bool
	BrowserMCP    bool
	BrowserRender bool
	StudioLive    bool
}

func buildLaunchCertifyCmd() *cobra.Command {
	var opts launchCertifyOptions
	cmd := &cobra.Command{
		Use:   "certify",
		Short: "Run the production parity certification harness",
		Long: `Run Soulacy's production certification harness and write JSON/Markdown
reports under .cache/production-parity by default.

Use --quick for a daily confidence run. Omit it for the full release gate,
including full Go/GUI tests, vulnerability checks, docs, SDK checks, and race
testing. Optional live checks stay off unless explicitly enabled.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLaunchCertify(opts)
		},
	}
	cmd.Flags().StringVar(&opts.ReportDir, "report-dir", "", "Directory where certification reports should be written")
	cmd.Flags().BoolVar(&opts.Quick, "quick", false, "Run the shorter daily certification profile")
	cmd.Flags().BoolVar(&opts.LiveChannels, "live-channels", false, "Run live Telegram/Slack/Discord delivery checks")
	cmd.Flags().BoolVar(&opts.BrowserMCP, "browser-mcp", false, "Run browser MCP sidecar checks")
	cmd.Flags().BoolVar(&opts.BrowserRender, "browser-render", false, "Run browser route screenshot/render checks")
	cmd.Flags().BoolVar(&opts.StudioLive, "studio-live", false, "Run optional Studio build/live workflow UAT")
	return cmd
}

func buildLaunchProofCmd() *cobra.Command {
	var opts launchProofOptions
	cmd := &cobra.Command{
		Use:   "proof",
		Short: "Export a launch-readiness proof pack",
		Long: `Export the live Dashboard launch-readiness payload as a timestamped proof
pack with JSON and Markdown files. Use this before a rollout, after UAT, or in
CI to attach evidence showing providers, channels, Studio contract health,
competitive parity, launch checklist items, and next actions.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLaunchProof(opts)
		},
	}
	cmd.Flags().StringVar(&opts.OutputDir, "out", "", "Directory where launch proof files should be written")
	cmd.Flags().BoolVar(&opts.Strict, "strict", false, "Exit non-zero unless launch readiness is ready")
	cmd.Flags().IntVar(&opts.MinScore, "min-score", 0, "Exit non-zero unless launch readiness score is at least this value")
	return cmd
}

func runLaunchCertify(opts launchCertifyOptions) error {
	root, err := findProjectRootForCertify()
	if err != nil {
		return err
	}
	script := filepath.Join(root, "scripts", "production-parity.sh")
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("production certification script not found at %s; run this command from a Soulacy source checkout", script)
	}
	cmd := exec.Command("bash", script)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), launchCertifyEnv(opts)...)
	return cmd.Run()
}

func findProjectRootForCertify() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "scripts", "production-parity.sh")); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return "", fmt.Errorf("could not find Soulacy project root from %s", wd)
}

func launchCertifyEnv(opts launchCertifyOptions) []string {
	var env []string
	if strings.TrimSpace(opts.ReportDir) != "" {
		env = append(env, "SOULACY_PARITY_REPORT_DIR="+opts.ReportDir)
	}
	if opts.Quick {
		env = append(env, "SOULACY_PARITY_QUICK=1")
	}
	if opts.LiveChannels {
		env = append(env, "SOULACY_PARITY_LIVE_CHANNELS=1")
	}
	if opts.BrowserMCP {
		env = append(env, "SOULACY_PARITY_BROWSER_MCP=1")
	}
	if opts.BrowserRender {
		env = append(env, "SOULACY_PARITY_BROWSER_RENDER=1")
	}
	if opts.StudioLive {
		env = append(env, "SOULACY_PARITY_STUDIO_LIVE=1")
	}
	return env
}

func runLaunchCheck(strict bool, minScore int) error {
	data, err := apiCall("GET", "/readiness", nil)
	if err != nil {
		return err
	}
	if outputJSON {
		fmt.Println(string(data))
		var r launchReadiness
		if err := json.Unmarshal(data, &r); err != nil {
			return err
		}
		return launchGateError(r, strict, minScore)
	}
	var r launchReadiness
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	printLaunchReadiness(r)
	return launchGateError(r, strict, minScore)
}

func runLaunchProof(opts launchProofOptions) error {
	data, err := apiCall("GET", "/readiness", nil)
	if err != nil {
		return err
	}
	var r launchReadiness
	if err := json.Unmarshal(data, &r); err != nil {
		return fmt.Errorf("decode launch readiness: %w", err)
	}
	dir := strings.TrimSpace(opts.OutputDir)
	if dir == "" {
		dir = filepath.Join(".cache", "launch-proof")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create launch proof directory: %w", err)
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	jsonPath := filepath.Join(dir, "soulacy-launch-proof-"+stamp+".json")
	mdPath := filepath.Join(dir, "soulacy-launch-proof-"+stamp+".md")
	pretty, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("format launch proof json: %w", err)
	}
	if err := os.WriteFile(jsonPath, append(pretty, '\n'), 0o644); err != nil {
		return fmt.Errorf("write launch proof json: %w", err)
	}
	if err := os.WriteFile(mdPath, []byte(renderLaunchProofMarkdown(r, stamp)), 0o644); err != nil {
		return fmt.Errorf("write launch proof markdown: %w", err)
	}
	fmt.Printf("Launch proof written:\n- %s\n- %s\n", jsonPath, mdPath)
	return launchGateError(r, opts.Strict, opts.MinScore)
}

func renderLaunchProofMarkdown(r launchReadiness, stamp string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Soulacy Launch Proof\n\n")
	fmt.Fprintf(&b, "- Generated: `%s`\n", stamp)
	fmt.Fprintf(&b, "- Status: `%s`\n", valueOr(r.Summary.Status, "unknown"))
	fmt.Fprintf(&b, "- Readiness score: `%d%%`\n", r.Summary.Score)
	fmt.Fprintf(&b, "- Competitive parity score: `%d%%`\n", r.Parity.Score)
	fmt.Fprintf(&b, "- Deployment: `%s` / `%s`\n", valueOr(r.Deployment.Label, r.Deployment.Profile), valueOr(r.Deployment.Status, "unknown"))
	fmt.Fprintf(&b, "- Providers ready: `%d`\n", r.Summary.ProvidersReady)
	fmt.Fprintf(&b, "- Channels ready: `%d`\n", r.Summary.ChannelsReady)
	fmt.Fprintf(&b, "- Enabled agents: `%d/%d`\n\n", r.Summary.EnabledAgents, r.Summary.Agents)

	fmt.Fprintf(&b, "## Journey\n\n")
	appendLaunchTable(&b, []string{"Area", "Status", "Detail"}, readinessRows(r.Journey))
	fmt.Fprintf(&b, "\n## Launch Checklist\n\n")
	if len(r.LaunchChecklist) == 0 {
		fmt.Fprintf(&b, "No launch checklist items were returned.\n\n")
	} else {
		appendLaunchTable(&b, []string{"Check", "Status", "Remedy"}, checklistRows(r.LaunchChecklist))
	}
	fmt.Fprintf(&b, "\n## Parity Gaps\n\n")
	if len(r.Parity.TopGaps) == 0 {
		fmt.Fprintf(&b, "No parity gaps were returned.\n\n")
	} else {
		appendLaunchTable(&b, []string{"Gap", "Score", "Next"}, parityRows(r.Parity.TopGaps))
	}
	fmt.Fprintf(&b, "\n## Next Actions\n\n")
	if len(r.NextActions) == 0 {
		fmt.Fprintf(&b, "No next actions. Core launch path is ready.\n")
	} else {
		for i, item := range r.NextActions {
			fmt.Fprintf(&b, "%d. **%s** (`%s`) — %s\n", i+1, item.Label, valueOr(item.Status, "unknown"), item.Detail)
		}
	}
	return b.String()
}

func readinessRows(items []launchReadinessItem) [][]string {
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		rows = append(rows, []string{item.Label, item.Status, item.Detail})
	}
	return rows
}

func checklistRows(items []launchChecklistItem) [][]string {
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		rows = append(rows, []string{item.Label, item.Status, valueOr(item.Remedy, item.Detail)})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })
	return rows
}

func parityRows(items []launchParityArea) [][]string {
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		rows = append(rows, []string{item.Label, fmt.Sprintf("%d%%", item.Score), item.Next})
	}
	return rows
}

func appendLaunchTable(b *strings.Builder, headers []string, rows [][]string) {
	for _, h := range headers {
		fmt.Fprintf(b, "| %s ", escapeMarkdownCell(h))
	}
	fmt.Fprintf(b, "|\n")
	for range headers {
		fmt.Fprintf(b, "| --- ")
	}
	fmt.Fprintf(b, "|\n")
	for _, row := range rows {
		for i := range headers {
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			fmt.Fprintf(b, "| %s ", escapeMarkdownCell(cell))
		}
		fmt.Fprintf(b, "|\n")
	}
}

func escapeMarkdownCell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.TrimSpace(s)
}

func printLaunchReadiness(r launchReadiness) {
	status := strings.ReplaceAll(r.Summary.Status, "_", " ")
	if status == "" {
		status = "unknown"
	}
	fmt.Printf("Soulacy Launch Readiness: %d%% · %s\n", r.Summary.Score, status)
	fmt.Printf("Ready %d/%d · Blockers %d · Warnings %d\n",
		r.Summary.ReadyItems,
		r.Summary.TotalItems,
		r.Summary.BlockerItems,
		r.Summary.WarningItems,
	)
	fmt.Printf("Providers %d · Channels %d · Enabled agents %d/%d · Learning agents %d · Templates %d\n\n",
		r.Summary.ProvidersReady,
		r.Summary.ChannelsReady,
		r.Summary.EnabledAgents,
		r.Summary.Agents,
		r.Summary.LearningAgents,
		r.Summary.Templates,
	)
	if r.Deployment.Profile != "" || r.Deployment.Label != "" {
		mode := "advisory"
		if r.Deployment.Strict {
			mode = "strict"
		}
		label := valueOr(r.Deployment.Label, r.Deployment.Profile)
		fmt.Printf("Deployment: %s · %s · %s · %d/%d checks",
			valueOr(label, "local"),
			mode,
			valueOr(r.Deployment.Status, "unknown"),
			r.Deployment.Ready,
			r.Deployment.Total,
		)
		if r.Deployment.Owner != "" || r.Deployment.Region != "" {
			fmt.Printf(" · %s/%s", valueOr(r.Deployment.Owner, "unowned"), valueOr(r.Deployment.Region, "no-region"))
		}
		fmt.Println()
		fmt.Println()
	}
	if r.Release.Version != "" || r.Release.UpdateManifest != "" || r.Release.UpdateHint != "" {
		fmt.Printf("Release: %s", valueOr(r.Release.Version, "unknown"))
		if r.Release.UpdatesReady {
			fmt.Printf(" · update manifest configured")
		} else {
			fmt.Printf(" · no update manifest")
		}
		if r.Release.UpdateManifest != "" {
			fmt.Printf(" (%s)", r.Release.UpdateManifest)
		}
		fmt.Println()
		if r.Release.UpdateHint != "" {
			fmt.Println("Update hint:", r.Release.UpdateHint)
		}
		fmt.Println()
	}

	tw := tabwriter.NewWriter(stdoutWriter{}, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "AREA\tSTATUS\tDETAIL")
	for _, item := range r.Journey {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", item.Label, item.Status, item.Detail)
	}
	_ = tw.Flush()

	if r.Parity.Score > 0 || len(r.Parity.TopGaps) > 0 {
		fmt.Printf("\nCompetitive parity: %d%%", r.Parity.Score)
		if len(r.Parity.TopGaps) == 0 {
			fmt.Println(" · no reported gaps")
		} else {
			fmt.Println()
			ptw := tabwriter.NewWriter(stdoutWriter{}, 0, 0, 2, ' ', 0)
			fmt.Fprintln(ptw, "GAP\tSCORE\tBENCHMARK\tNEXT")
			for _, gap := range r.Parity.TopGaps {
				fmt.Fprintf(ptw, "%s\t%d%%\t%s\t%s\n", gap.Label, gap.Score, gap.Benchmark, gap.Next)
			}
			_ = ptw.Flush()
		}
	}

	if len(r.LaunchChecklist) > 0 {
		fmt.Println("\nLaunch checklist:")
		ctw := tabwriter.NewWriter(stdoutWriter{}, 0, 0, 2, ' ', 0)
		fmt.Fprintln(ctw, "CHECK\tSTATUS\tDETAIL\tREMEDY")
		for _, item := range r.LaunchChecklist {
			remedy := item.Remedy
			if remedy == "" {
				remedy = "—"
			}
			fmt.Fprintf(ctw, "%s\t%s\t%s\t%s\n", item.Label, item.Status, item.Detail, remedy)
		}
		_ = ctw.Flush()
	}

	if len(r.NextActions) == 0 {
		fmt.Println("\nNext actions: none. Core launch path is ready.")
		return
	}
	fmt.Println("\nNext actions:")
	for i, item := range r.NextActions {
		target := item.Href
		if target != "" {
			target = " (" + target + ")"
		}
		fmt.Printf("%d. %s: %s%s\n", i+1, item.Label, item.Detail, target)
	}
}

func valueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func launchGateError(r launchReadiness, strict bool, minScore int) error {
	if minScore > 0 && r.Summary.Score < minScore {
		return fmt.Errorf("launch readiness score is %d%%, below required %d%%", r.Summary.Score, minScore)
	}
	return launchStrictError(r.Summary.Status, strict)
}

func launchStrictError(status string, strict bool) error {
	if strict && status != "ready" {
		if status == "" {
			status = "unknown"
		}
		return fmt.Errorf("launch readiness is %s", status)
	}
	return nil
}

type stdoutWriter struct{}

func (stdoutWriter) Write(p []byte) (int, error) {
	return fmt.Print(string(p))
}
