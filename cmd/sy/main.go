// main.go — Soulacy CLI (sy) entry point.
// The CLI communicates with a running Soulacy gateway over its REST API.
// Every action available in the GUI is also available as a CLI command.
// Config is read from ~/.soulacy/config.yaml or SOULACY_CONFIG_PATH.
//
// Usage:
//
//	sy agent list
//	sy agent create --file soul.yaml
//	sy agent enable support-bot
//	sy chat --agent support-bot "Hello, world!"
//	sy channel list
//	sy memory list --agent support-bot
//	sy schedule list
//	sy logs --agent support-bot --follow
//	sy server start
//	sy server status
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/pkgregistry"
)

var (
	gatewayURL string
	apiKey     string
	outputJSON bool
	// activeWorkspaceID is only ever a *selector*. The gateway independently
	// verifies membership before establishing workspace context, so sending it
	// grants nothing on its own.
	activeWorkspaceID string
	// idempotencyKey makes a mutation safe to retry: the gateway replays the
	// original response instead of performing the change twice.
	idempotencyKey string
)

func main() {
	root := buildRoot()
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func buildRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "sy",
		Short: "Soulacy CLI — manage your agentic framework from the terminal",
		Long: `sy is the command-line interface for Soulacy.

Every GUI action is available here. All commands communicate with the
Soulacy gateway over its REST API.

Quick start:
  sy server start              # start the gateway
  sy agent list                # list loaded agents
  sy chat --agent my-agent "Hello!"   # chat with an agent
  sy logs --follow             # stream live event log`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Skip config loading for setup — it creates the config
			if cmd.Name() == "setup" {
				return nil
			}
			viper.SetConfigName("config")
			viper.SetConfigType("yaml")
			home, _ := os.UserHomeDir()
			if ws, werr := config.ResolveWorkspace(); werr == nil {
				viper.AddConfigPath(ws.Root)
			}
			viper.AddConfigPath(home + "/.soulacy")
			viper.AddConfigPath(".")
			viper.SetEnvPrefix("SOULACY")
			viper.AutomaticEnv()
			_ = viper.ReadInConfig()

			// A named context is a stored default: an explicit --gateway or
			// --workspace flag still wins, and the loopback fallback below
			// still applies when neither is set.
			applyActiveContext()

			if gatewayURL == "" {
				gatewayURL = viper.GetString("cli.gateway_url")
			}
			if gatewayURL == "" {
				port := viper.GetInt("server.port")
				if port == 0 {
					port = 1947
				}
				gatewayURL = fmt.Sprintf("http://localhost:%d", port)
			}
			if apiKey == "" {
				// CI supplies a credential through the environment rather than
				// a flag, so it never reaches shell history or the process
				// table where any other user on the host could read it.
				apiKey = strings.TrimSpace(os.Getenv(EnvAPIKey))
			}
			if apiKey == "" {
				if session, err := loadCLISession(gatewayURL); err == nil {
					apiKey = session.AccessToken
				}
			}
			if apiKey == "" {
				// The local server key remains a backwards-compatible fallback for
				// Personal deployments. A stored interactive identity takes
				// precedence for Team/Scale so CLI requests remain attributable.
				apiKey = viper.GetString("server.api_key")
			}
			return nil
		},
	}

	root.PersistentFlags().StringVar(&gatewayURL, "gateway", "", "Gateway URL (default: http://localhost:1947)")
	root.PersistentFlags().StringVar(&apiKey, "api-key", "", "API key for gateway authentication")
	root.PersistentFlags().StringVar(&activeWorkspaceID, "workspace", "", "Workspace ID to target (default: the current context's workspace)")
	root.PersistentFlags().StringVar(&idempotencyKey, "idempotency-key", "", "Make this mutation safe to retry; a repeat replays the original response")
	root.PersistentFlags().BoolVar(&outputJSON, "json", false, "Output raw JSON")

	// Sub-commands
	root.AddCommand(
		buildOnboardCmd(), // sy onboard — guided first-run wizard
		buildSetupCmd(),   // sy setup — legacy from-scratch config writer
		buildAgentCmd(),
		buildChatCmd(),
		buildChannelCmd(),
		buildScheduleCmd(),
		buildMemoryCmd(),
		buildSkillCmd(),
		buildPackageCmd(), // sy package install — install a Skill or MCP server from one URL
		buildLogsCmd(),
		buildServerCmd(),
		buildDoctorCmd(),
		buildDaemonCmd(),       // sy daemon — install/uninstall/status/logs as a background service
		buildWorkspaceCmd(),    // sy workspace — soulspace info + migration
		buildSeedExamplesCmd(), // sy seed-examples — copy example agents + scaffold tool files
		buildSupportCmd(),      // sy support — redacted support bundles
		buildPullCmd(),         // sy pull — agent marketplace
		buildEvalCmd(),         // sy eval — evaluation framework
		buildRegistryCmd(),     // sy registry — review + manage skill sources (E26)
		buildSecretsCmd(),      // sy secrets — manage the gateway-global secrets store
		buildCredentialCmd(),   // sy credential — scoped personal/service credentials
		buildConnectionCmd(),   // sy connection — capture and manage authenticated website sessions
		buildContextCmd(),      // sy context — named server/workspace targets
		buildWhoamiCmd(),       // sy whoami — the identity the server resolves
		buildMCPCmd(),          // sy mcp — manage MCP servers
		buildLaunchCmd(),       // sy launch — production readiness checks
		buildUpdateCmd(),       // sy update — release update checks
		buildUpgradeCmd(),      // sy upgrade — self-upgrade binaries
		buildVoiceCmd(),        // sy voice — provider-neutral speech sidecars
		buildLoginCmd(),        // sy login — browser-assisted OIDC with PKCE
		buildLogoutCmd(),       // sy logout — revoke and remove local session
		buildCompatVersionCmd(),
	)
	return root
}

// ── Agent commands ──────────────────────────────────────────────────────────

func buildAgentCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "agent", Short: "Manage agents"}

	cmd.AddCommand(&cobra.Command{
		Use: "list", Short: "List all agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			return listAgents()
		},
	})

	// get
	cmd.AddCommand(&cobra.Command{
		Use: "get <id>", Short: "Show agent details",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiGet("/agents/"+args[0], "")
		},
	})

	// create
	var createFile string
	createCmd := &cobra.Command{
		Use: "create", Short: "Create an agent from a SOUL.yaml file",
		RunE: func(cmd *cobra.Command, args []string) error {
			if createFile == "" {
				return fmt.Errorf("--file is required")
			}
			data, err := os.ReadFile(createFile)
			if err != nil {
				return err
			}
			return apiPost("/agents", data)
		},
	}
	createCmd.Flags().StringVarP(&createFile, "file", "f", "", "Path to SOUL.yaml")
	cmd.AddCommand(createCmd)
	cmd.AddCommand(buildAgentValidateCmd())
	cmd.AddCommand(buildAgentTierCmd())
	cmd.AddCommand(buildAgentPackageCmd())

	// enable / disable
	cmd.AddCommand(&cobra.Command{
		Use: "enable <id>", Short: "Enable an agent",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiPost("/agents/"+args[0]+"/enable", nil)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "disable <id>", Short: "Disable an agent",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiPost("/agents/"+args[0]+"/disable", nil)
		},
	})

	// delete
	cmd.AddCommand(&cobra.Command{
		Use: "delete <id>", Short: "Delete an agent",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiDelete("/agents/" + args[0])
		},
	})

	// trigger (manual run)
	cmd.AddCommand(&cobra.Command{
		Use: "trigger <id>", Short: "Manually trigger a scheduled agent",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiPost("/agents/"+args[0]+"/trigger", nil)
		},
	})

	return cmd
}

func buildAgentPackageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "package",
		Aliases: []string{"pkg"},
		Short:   "Export, inspect, and import shareable agent packages",
		Long: `Export, inspect, and import .soulacy-agent.json packages.

Packages include redacted SOUL.yaml, a setup requirements checklist, and safe
local tool files when they can be bundled. Imported agents are disabled by
default so they can be reviewed before running.`,
	}

	var outPath string
	var signingKeyFile string
	exportCmd := &cobra.Command{
		Use:   "export <agent-id>",
		Short: "Export an agent as a .soulacy-agent.json package",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exportAgentPackage(args[0], outPath, signingKeyFile)
		},
	}
	exportCmd.Flags().StringVarP(&outPath, "out", "o", "", "Output file path (default: <agent-id>.soulacy-agent.json)")
	exportCmd.Flags().StringVar(&signingKeyFile, "signing-key-file", "", "Hex Ed25519 private key file used to sign the package checksum")
	cmd.AddCommand(exportCmd)

	inspectCmd := &cobra.Command{
		Use:   "inspect <package.json>",
		Short: "Inspect package requirements before importing",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return inspectAgentPackageFile(args[0])
		},
	}
	cmd.AddCommand(inspectCmd)

	var overwrite bool
	var enable bool
	var idOverride string
	var acknowledgeMissing bool
	importCmd := &cobra.Command{
		Use:   "import <package.json>",
		Short: "Import an agent package",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return importAgentPackageFile(args[0], overwrite, enable, idOverride, acknowledgeMissing)
		},
	}
	importCmd.Flags().BoolVar(&overwrite, "overwrite", false, "Overwrite an existing agent with the same ID")
	importCmd.Flags().BoolVar(&enable, "enable", false, "Enable the imported agent immediately (default: import disabled for review)")
	importCmd.Flags().StringVar(&idOverride, "id", "", "Import with a different agent ID")
	importCmd.Flags().BoolVar(&acknowledgeMissing, "acknowledge-missing", false,
		"Import despite one or more v2 `requires` entries being missing on this workspace (secrets, providers, channels, peer agents). Use only after reviewing the requirements list.")
	cmd.AddCommand(importCmd)

	// Story 7 Bucket 7A: local-only structural validation of a package
	// file. No gateway hit — just parses the JSON, checks the schema, the
	// calendar-versioning regex, the namespaced package_id, and (when
	// present) the signature. Useful for publishers before shipping.
	validateCmd := &cobra.Command{
		Use:   "validate <package.json>",
		Short: "Validate a package's structural correctness locally (no import)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return validateAgentPackageFile(args[0])
		},
	}
	cmd.AddCommand(validateCmd)

	return cmd
}

func exportAgentPackage(agentID, outPath, signingKeyFile string) error {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return fmt.Errorf("agent id is required")
	}
	data, err := apiCall("GET", "/agents/"+agentID+"/package", nil)
	if err != nil {
		return err
	}
	if strings.TrimSpace(signingKeyFile) != "" {
		data, err = signAgentPackageBytes(data, signingKeyFile)
		if err != nil {
			return err
		}
	}
	if outputJSON {
		fmt.Println(string(data))
		return nil
	}
	if strings.TrimSpace(outPath) == "" {
		outPath = safeCLIFileName(agentID) + ".soulacy-agent.json"
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Clean(outPath)), 0755); err != nil && filepath.Dir(filepath.Clean(outPath)) != "." {
		return err
	}
	if err := os.WriteFile(outPath, data, 0644); err != nil {
		return err
	}
	fmt.Printf("exported package: %s\n", outPath)
	return nil
}

func signAgentPackageBytes(data []byte, signingKeyFile string) ([]byte, error) {
	var pkg struct {
		Integrity struct {
			SHA256 string `json:"sha256"`
		} `json:"integrity"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, err
	}
	if strings.TrimSpace(pkg.Integrity.SHA256) == "" {
		return nil, fmt.Errorf("package has no integrity.sha256 to sign")
	}
	priv, err := loadPackageSigningPrivateKey(signingKeyFile)
	if err != nil {
		return nil, err
	}
	sig, err := pkgregistry.SignChecksum(priv, pkg.Integrity.SHA256)
	if err != nil {
		return nil, err
	}
	pub := priv.Public().(ed25519.PublicKey)
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	integrity, _ := raw["integrity"].(map[string]any)
	if integrity == nil {
		integrity = map[string]any{}
		raw["integrity"] = integrity
	}
	integrity["signature"] = sig
	integrity["public_key"] = hex.EncodeToString(pub)
	return json.MarshalIndent(raw, "", "  ")
}

func loadPackageSigningPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("signing key must be hex: %w", err)
	}
	switch len(raw) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(raw), nil
	default:
		return nil, fmt.Errorf("signing key must be %d-byte seed or %d-byte private key (got %d bytes)", ed25519.SeedSize, ed25519.PrivateKeySize, len(raw))
	}
}

func inspectAgentPackageFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	resp, err := apiCall("POST", "/agents/package/inspect", data)
	if err != nil {
		return err
	}
	if outputJSON {
		fmt.Println(string(resp))
		return nil
	}
	var inspected agentPackageInspectCLI
	if err := json.Unmarshal(resp, &inspected); err != nil {
		fmt.Println(string(resp))
		return nil
	}
	printAgentPackageInspection(inspected)
	return nil
}

func importAgentPackageFile(path string, overwrite, enable bool, idOverride string, acknowledgeMissing bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var raw json.RawMessage = data
	body := map[string]any{
		"package":   raw,
		"overwrite": overwrite,
		"disabled":  !enable,
	}
	if strings.TrimSpace(idOverride) != "" {
		body["id_override"] = strings.TrimSpace(idOverride)
	}
	if acknowledgeMissing {
		body["acknowledge_missing"] = true
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := apiCall("POST", "/agents/package/import", payload)
	if err != nil {
		return err
	}
	if outputJSON {
		fmt.Println(string(resp))
		return nil
	}
	var imported struct {
		Agent struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		} `json:"agent"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(resp, &imported); err != nil {
		fmt.Println(string(resp))
		return nil
	}
	state := "disabled for review"
	if imported.Agent.Enabled {
		state = "enabled"
	}
	fmt.Printf("imported agent: %s (%s) - %s\n", imported.Agent.ID, imported.Agent.Name, state)
	for _, warning := range imported.Warnings {
		fmt.Printf("warning: %s\n", warning)
	}
	return nil
}

// validateAgentPackageFile runs Bucket-7A structural validation on a package
// file locally — schema tag, calendar-versioning regex, namespaced package_id.
// Signature verification and integrity checksum recomputation stay local so
// publishers can validate before pushing.
func validateAgentPackageFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var pkg struct {
		SchemaVersion string `json:"schema_version"`
		Manifest      struct {
			PackageID string `json:"package_id"`
			AgentID   string `json:"agent_id"`
			Version   string `json:"version"`
		} `json:"manifest"`
		Integrity struct {
			Algorithm string `json:"algorithm"`
			SHA256    string `json:"sha256"`
			Signature string `json:"signature"`
			PublicKey string `json:"public_key"`
		} `json:"integrity"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	var issues []string
	switch pkg.SchemaVersion {
	case "soulacy.agent.package/v1":
		issues = append(issues, "schema is v1 (deprecated; v2 required after 2027-06-01) — re-publish as v2")
	case "soulacy.agent.package/v2":
		// per §4.1 regex — kept inline so this command has no import from
		// the gateway package.
		re := regexp.MustCompile(`^(\d{4})\.(\d{2})\.(\d{2})(?:\.(\d+))?$`)
		if !re.MatchString(strings.TrimSpace(pkg.Manifest.Version)) {
			issues = append(issues, "manifest.version must match YYYY.MM.DD[.PATCH] — got "+strconv.Quote(pkg.Manifest.Version))
		}
		nsRE := regexp.MustCompile(`^([a-z0-9-]{2,32})/([a-z0-9][a-z0-9-]{0,62})$`)
		if !nsRE.MatchString(strings.TrimSpace(pkg.Manifest.PackageID)) {
			issues = append(issues, "manifest.package_id must be `<publisher>/<package>` — got "+strconv.Quote(pkg.Manifest.PackageID))
		}
		if strings.TrimSpace(pkg.Manifest.AgentID) == "" {
			issues = append(issues, "manifest.agent_id is required")
		}
	default:
		issues = append(issues, "unknown schema_version "+strconv.Quote(pkg.SchemaVersion))
	}
	if pkg.Integrity.Signature != "" && pkg.Integrity.PublicKey == "" {
		issues = append(issues, "integrity.signature is set but integrity.public_key is missing")
	}
	if outputJSON {
		fmt.Println(string(mustMarshalJSON(map[string]any{
			"path":           path,
			"schema_version": pkg.SchemaVersion,
			"package_id":     pkg.Manifest.PackageID,
			"version":        pkg.Manifest.Version,
			"issues":         issues,
			"ok":             len(issues) == 0,
		})))
		return nil
	}
	fmt.Printf("package: %s\n", path)
	fmt.Printf("  schema:     %s\n", valueOrDash(pkg.SchemaVersion))
	fmt.Printf("  package_id: %s\n", valueOrDash(pkg.Manifest.PackageID))
	fmt.Printf("  version:    %s\n", valueOrDash(pkg.Manifest.Version))
	if len(issues) == 0 {
		fmt.Println("  status:     ok")
		return nil
	}
	fmt.Println("  status:     issues")
	for _, iss := range issues {
		fmt.Printf("    - %s\n", iss)
	}
	return fmt.Errorf("package has %d issue(s)", len(issues))
}

func valueOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func mustMarshalJSON(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return b
}

type agentPackageInspectCLI struct {
	SchemaVersion string `json:"schema_version"`
	Manifest      struct {
		AgentID     string   `json:"agent_id"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Trigger     string   `json:"trigger"`
		Surfaces    []string `json:"surfaces"`
		Providers   []string `json:"providers"`
		Models      []string `json:"models"`
		Channels    []string `json:"channels"`
		Skills      []string `json:"skills"`
		Knowledge   []string `json:"knowledge"`
		PeerAgents  []string `json:"peer_agents"`
		Builtins    []string `json:"builtins"`
		MCPServers  []string `json:"mcp_servers"`
		Files       []string `json:"files_required"`
		EvalSuites  []string `json:"eval_suites"`
		Samples     []string `json:"sample_prompts"`
	} `json:"manifest"`
	Agent struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Trigger string `json:"trigger"`
		LLM     struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"llm"`
	} `json:"agent"`
	Validation struct {
		Valid    bool `json:"valid"`
		Errors   int  `json:"errors"`
		Warnings int  `json:"warnings"`
		Findings []struct {
			Severity string `json:"severity"`
			Message  string `json:"message"`
			Path     string `json:"path"`
		} `json:"findings"`
	} `json:"validation"`
	Requirements []struct {
		Kind        string `json:"kind"`
		Name        string `json:"name"`
		Status      string `json:"status"`
		Description string `json:"description"`
	} `json:"requirements"`
	Warnings   []string `json:"warnings"`
	Importable bool     `json:"importable"`
	Integrity  struct {
		SHA256    string `json:"sha256"`
		Signature string `json:"signature"`
		PublicKey string `json:"public_key"`
		Verified  bool   `json:"verified"`
	} `json:"integrity"`
}

func printAgentPackageInspection(inspected agentPackageInspectCLI) {
	name := inspected.Manifest.Name
	if name == "" {
		name = inspected.Agent.Name
	}
	id := inspected.Manifest.AgentID
	if id == "" {
		id = inspected.Agent.ID
	}
	fmt.Printf("Agent package: %s (%s)\n", name, id)
	fmt.Printf("  schema:     %s\n", inspected.SchemaVersion)
	fmt.Printf("  trigger:    %s\n", firstNonEmpty(inspected.Manifest.Trigger, inspected.Agent.Trigger))
	fmt.Printf("  llm:        %s/%s\n", firstNonEmpty(firstString(inspected.Manifest.Providers), inspected.Agent.LLM.Provider, "default"), firstNonEmpty(firstString(inspected.Manifest.Models), inspected.Agent.LLM.Model, "?"))
	fmt.Printf("  importable: %v\n", inspected.Importable)
	if inspected.Integrity.SHA256 != "" {
		status := "checksum ok"
		if inspected.Integrity.Signature != "" && inspected.Integrity.Verified {
			status = "signed and verified"
		} else if inspected.Integrity.Signature != "" {
			status = "signed but not verified"
		}
		fmt.Printf("  integrity:  %s (%s)\n", status, inspected.Integrity.SHA256)
	}
	if len(inspected.Manifest.EvalSuites) > 0 || len(inspected.Manifest.Samples) > 0 {
		fmt.Printf("  harness:    %d eval suite(s), %d sample prompt(s)\n", len(inspected.Manifest.EvalSuites), len(inspected.Manifest.Samples))
	}
	if inspected.Validation.Errors > 0 || inspected.Validation.Warnings > 0 {
		fmt.Printf("  validation: %d error(s), %d warning(s)\n", inspected.Validation.Errors, inspected.Validation.Warnings)
	}
	if len(inspected.Validation.Findings) > 0 {
		fmt.Println("\nValidation findings:")
		for _, finding := range inspected.Validation.Findings {
			loc := finding.Path
			if loc != "" {
				loc = " (" + loc + ")"
			}
			fmt.Printf("  - [%s] %s%s\n", finding.Severity, finding.Message, loc)
		}
	}
	if len(inspected.Requirements) > 0 {
		fmt.Println("\nRequirements:")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "KIND\tNAME\tSTATUS\tNOTE")
		for _, req := range inspected.Requirements {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", req.Kind, req.Name, req.Status, req.Description)
		}
		_ = w.Flush()
	}
	if len(inspected.Warnings) > 0 {
		fmt.Println("\nWarnings:")
		for _, warning := range inspected.Warnings {
			fmt.Printf("  - %s\n", warning)
		}
	}
}

func safeCLIFileName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "agent"
	}
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", " ", "-")
	return replacer.Replace(s)
}

func firstString(values []string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// ── Chat ────────────────────────────────────────────────────────────────────

func buildChatCmd() *cobra.Command {
	var agentID, userID string
	cmd := &cobra.Command{
		Use:   "chat <text>",
		Short: "Send a message to an agent and print the reply",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if agentID == "" {
				return fmt.Errorf("--agent is required")
			}
			body, _ := json.Marshal(map[string]string{
				"agent_id": agentID,
				"user_id":  userID,
				"text":     args[0],
			})
			resp, err := apiCallWithTimeout("POST", "/chat", body, 10*time.Minute)
			if err != nil {
				return err
			}
			var result struct {
				Reply string `json:"reply"`
			}
			if err := json.Unmarshal(resp, &result); err != nil {
				fmt.Println(string(resp))
				return nil
			}
			fmt.Println(result.Reply)
			return nil
		},
	}
	cmd.Flags().StringVar(&agentID, "agent", "", "Agent ID to chat with")
	cmd.Flags().StringVar(&userID, "user", "cli-user", "User ID")
	return cmd
}

// ── Channel ─────────────────────────────────────────────────────────────────

func buildChannelCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "channel", Short: "Manage channel adapters"}
	cmd.AddCommand(&cobra.Command{
		Use: "list", Short: "List channel adapter status",
		RunE: func(cmd *cobra.Command, args []string) error {
			return listChannels()
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "status <id>",
		Short: "Show one channel adapter status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ch, err := getChannel(args[0])
			if err != nil {
				return err
			}
			if outputJSON {
				return printJSON(ch)
			}
			printChannelSummary(ch)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "enable <id>",
		Short: "Enable a configured channel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiPost("/channels/"+args[0]+"/enable", nil)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "disable <id>",
		Short: "Disable a channel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiPost("/channels/"+args[0]+"/disable", nil)
		},
	})

	var setFields []string
	updateCmd := &cobra.Command{
		Use:   "update <id> --set key=value",
		Short: "Update channel settings",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(setFields) == 0 {
				return fmt.Errorf("at least one --set key=value is required")
			}
			settings := map[string]any{}
			for _, item := range setFields {
				key, value, ok := strings.Cut(item, "=")
				key = strings.TrimSpace(key)
				if !ok || key == "" {
					return fmt.Errorf("invalid --set %q; expected key=value", item)
				}
				settings[key] = parseCLIValue(value)
			}
			body, _ := json.Marshal(map[string]any{"settings": settings})
			return apiPatch("/channels/"+args[0], body)
		},
	}
	updateCmd.Flags().StringArrayVar(&setFields, "set", nil, "Setting assignment, repeatable: --set key=value")
	cmd.AddCommand(updateCmd)
	cmd.AddCommand(
		buildHTTPChannelCmd(),
		buildTelegramChannelCmd(),
		buildSlackChannelCmd(),
		buildDiscordChannelCmd(),
		buildWhatsAppChannelCmd(),
		buildWhatsAppWebChannelCmd(),
	)
	return cmd
}

func buildHTTPChannelCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "http", Short: "Inspect the always-on HTTP channel"}
	addAdapterCommonCommands(cmd, "http", false)
	return cmd
}

func buildTelegramChannelCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "telegram", Short: "Configure Telegram channel"}
	var token, agentID string
	safety := addSafetyFlags(cmd, "groups")
	configure := &cobra.Command{
		Use:   "configure",
		Short: "Configure Telegram credentials, routing, and activation safety",
		RunE: func(cmd *cobra.Command, args []string) error {
			settings := map[string]any{}
			addChangedString(cmd, settings, "token", token)
			addChangedString(cmd, settings, "agent", agentID, "agent_id")
			addSafetySettings(cmd, settings, safety)
			return patchChannelSettings("telegram", settings)
		},
	}
	configure.Flags().StringVar(&token, "token", "", "Telegram bot token")
	configure.Flags().StringVar(&agentID, "agent", "", "Default agent ID")
	addSafetyFlagsTo(configure, &safety, "groups")
	cmd.AddCommand(configure)
	addAdapterCommonCommands(cmd, "telegram", false)
	return cmd
}

func buildSlackChannelCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "slack", Short: "Configure Slack channel"}
	var botToken, appToken, agentID string
	safety := addSafetyFlags(cmd, "channels")
	configure := &cobra.Command{
		Use:   "configure",
		Short: "Configure Slack credentials, routing, and activation safety",
		RunE: func(cmd *cobra.Command, args []string) error {
			settings := map[string]any{}
			addChangedString(cmd, settings, "bot-token", botToken, "bot_token")
			addChangedString(cmd, settings, "app-token", appToken, "app_token")
			addChangedString(cmd, settings, "agent", agentID, "agent_id")
			addSafetySettings(cmd, settings, safety)
			return patchChannelSettings("slack", settings)
		},
	}
	configure.Flags().StringVar(&botToken, "bot-token", "", "Slack bot token (xoxb-...)")
	configure.Flags().StringVar(&appToken, "app-token", "", "Slack app token (xapp-...)")
	configure.Flags().StringVar(&agentID, "agent", "", "Default agent ID")
	addSafetyFlagsTo(configure, &safety, "channels")
	cmd.AddCommand(configure)
	addAdapterCommonCommands(cmd, "slack", false)
	return cmd
}

func buildDiscordChannelCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "discord", Short: "Configure Discord channel"}
	var token, agentID, guildID string
	safety := addSafetyFlags(cmd, "servers")
	configure := &cobra.Command{
		Use:   "configure",
		Short: "Configure Discord credentials, routing, and activation safety",
		RunE: func(cmd *cobra.Command, args []string) error {
			settings := map[string]any{}
			addChangedString(cmd, settings, "token", token)
			addChangedString(cmd, settings, "agent", agentID, "agent_id")
			addChangedString(cmd, settings, "guild", guildID, "guild_id")
			addSafetySettings(cmd, settings, safety)
			return patchChannelSettings("discord", settings)
		},
	}
	configure.Flags().StringVar(&token, "token", "", "Discord bot token")
	configure.Flags().StringVar(&agentID, "agent", "", "Default agent ID")
	configure.Flags().StringVar(&guildID, "guild", "", "Optional Discord guild ID")
	addSafetyFlagsTo(configure, &safety, "servers")
	cmd.AddCommand(configure)
	addAdapterCommonCommands(cmd, "discord", false)
	return cmd
}

func buildWhatsAppChannelCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "whatsapp", Short: "Configure official WhatsApp Cloud API channel"}
	var phoneNumberID, accessToken, verifyToken, appSecret, agentID string
	safety := addSafetyFlags(cmd, "groups")
	configure := &cobra.Command{
		Use:   "configure",
		Short: "Configure WhatsApp Cloud API credentials, routing, and activation safety",
		RunE: func(cmd *cobra.Command, args []string) error {
			settings := map[string]any{}
			addChangedString(cmd, settings, "phone-number-id", phoneNumberID, "phone_number_id")
			addChangedString(cmd, settings, "access-token", accessToken, "access_token")
			addChangedString(cmd, settings, "verify-token", verifyToken, "verify_token")
			addChangedString(cmd, settings, "app-secret", appSecret, "app_secret")
			addChangedString(cmd, settings, "agent", agentID, "agent_id")
			addSafetySettings(cmd, settings, safety)
			return patchChannelSettings("whatsapp", settings)
		},
	}
	configure.Flags().StringVar(&phoneNumberID, "phone-number-id", "", "Meta WhatsApp phone number ID")
	configure.Flags().StringVar(&accessToken, "access-token", "", "Meta WhatsApp access token")
	configure.Flags().StringVar(&verifyToken, "verify-token", "", "Meta webhook verify token")
	configure.Flags().StringVar(&appSecret, "app-secret", "", "Meta app secret for webhook HMAC verification")
	configure.Flags().StringVar(&agentID, "agent", "", "Default agent ID")
	addSafetyFlagsTo(configure, &safety, "groups")
	cmd.AddCommand(configure)
	addAdapterCommonCommands(cmd, "whatsapp", false)
	return cmd
}

func buildWhatsAppWebChannelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "whatsapp-web",
		Aliases: []string{"wa-web", "whatsappweb"},
		Short:   "Configure and pair WhatsApp Web",
	}

	var agentID, triggerPhrase, allowedChats, allowedSenders, command, args, sessionDir, accountID string
	var allowGroups bool
	var waitSeconds int
	pairCmd := &cobra.Command{
		Use:   "pair",
		Short: "Start WhatsApp Web pairing and print the QR payload",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(agentID) == "" {
				return fmt.Errorf("--agent is required")
			}
			body, _ := json.Marshal(map[string]any{
				"agent_id":           agentID,
				"trigger_phrase":     triggerPhrase,
				"ignore_groups":      !allowGroups,
				"allowed_chat_ids":   allowedChats,
				"allowed_sender_ids": allowedSenders,
			})
			if _, err := apiCall("POST", "/channels/whatsapp_web/pair", body); err != nil {
				return err
			}
			if !outputJSON {
				fmt.Println("WhatsApp Web pairing started.")
				fmt.Printf("Safety: trigger phrase %q, groups %s.\n", triggerPhrase, groupPolicyLabel(!allowGroups))
			}
			ch, err := waitForChannelQR("whatsapp_web", waitSeconds)
			if err != nil {
				return err
			}
			if outputJSON {
				return printJSON(ch)
			}
			printWhatsAppWebQR(ch)
			return nil
		},
	}
	pairCmd.Flags().StringVar(&agentID, "agent", "", "Agent ID to route WhatsApp messages to")
	pairCmd.Flags().StringVar(&triggerPhrase, "trigger", "!soulacy", "Only messages starting with this phrase trigger the agent")
	pairCmd.Flags().BoolVar(&allowGroups, "allow-groups", false, "Allow group chats to trigger the agent")
	pairCmd.Flags().StringVar(&allowedChats, "allowed-chats", "", "Comma-separated WhatsApp chat JIDs to allow")
	pairCmd.Flags().StringVar(&allowedSenders, "allowed-senders", "", "Comma-separated WhatsApp sender JIDs to allow")
	pairCmd.Flags().IntVar(&waitSeconds, "wait", 20, "Seconds to wait for a QR payload")
	cmd.AddCommand(pairCmd)
	configure := &cobra.Command{
		Use:   "configure",
		Short: "Configure WhatsApp Web sidecar, routing, and activation safety",
		RunE: func(cmd *cobra.Command, argsList []string) error {
			settings := map[string]any{}
			addChangedString(cmd, settings, "agent", agentID, "agent_id")
			addChangedString(cmd, settings, "command", command)
			addChangedString(cmd, settings, "args", args)
			addChangedString(cmd, settings, "session-dir", sessionDir, "session_dir")
			addChangedString(cmd, settings, "account-id", accountID, "account_id")
			addChangedString(cmd, settings, "trigger", triggerPhrase, "trigger_phrase")
			if cmd.Flags().Changed("allow-groups") {
				settings["ignore_groups"] = !allowGroups
			}
			addChangedString(cmd, settings, "allowed-chats", allowedChats, "allowed_chat_ids")
			addChangedString(cmd, settings, "allowed-senders", allowedSenders, "allowed_sender_ids")
			return patchChannelSettings("whatsapp_web", settings)
		},
	}
	configure.Flags().StringVar(&agentID, "agent", "", "Default agent ID")
	configure.Flags().StringVar(&command, "command", "", "Runtime executable; defaults to node")
	configure.Flags().StringVar(&args, "args", "", "Sidecar args, e.g. scripts/whatsapp-web-sidecar.mjs")
	configure.Flags().StringVar(&sessionDir, "session-dir", "", "Where QR-linked auth state is stored")
	configure.Flags().StringVar(&accountID, "account-id", "", "Session subdirectory for this linked account")
	configure.Flags().StringVar(&triggerPhrase, "trigger", "!soulacy", "Only messages starting with this phrase trigger the agent")
	configure.Flags().BoolVar(&allowGroups, "allow-groups", false, "Allow group chats to trigger the agent")
	configure.Flags().StringVar(&allowedChats, "allowed-chats", "", "Comma-separated WhatsApp chat JIDs to allow")
	configure.Flags().StringVar(&allowedSenders, "allowed-senders", "", "Comma-separated WhatsApp sender JIDs to allow")
	cmd.AddCommand(configure)

	addAdapterCommonCommands(cmd, "whatsapp_web", true)
	return cmd
}

type channelSafetyFlags struct {
	trigger         string
	allowGroups     bool
	allowedChats    string
	allowedUsers    string
	groupNounPlural string
}

func addSafetyFlags(_ *cobra.Command, groupNounPlural string) channelSafetyFlags {
	return channelSafetyFlags{
		trigger:         "!soulacy",
		groupNounPlural: groupNounPlural,
	}
}

func addSafetyFlagsTo(cmd *cobra.Command, safety *channelSafetyFlags, groupNounPlural string) {
	cmd.Flags().StringVar(&safety.trigger, "trigger", safety.trigger, "Only messages starting with this phrase trigger the agent")
	cmd.Flags().BoolVar(&safety.allowGroups, "allow-groups", false, "Allow "+groupNounPlural+" to trigger the agent")
	cmd.Flags().StringVar(&safety.allowedChats, "allowed-chats", "", "Comma-separated platform chat/channel IDs to allow")
	cmd.Flags().StringVar(&safety.allowedUsers, "allowed-users", "", "Comma-separated platform user/sender IDs to allow")
}

func addSafetySettings(cmd *cobra.Command, settings map[string]any, safety channelSafetyFlags) {
	addChangedString(cmd, settings, "trigger", safety.trigger, "trigger_phrase")
	if cmd.Flags().Changed("allow-groups") {
		settings["ignore_groups"] = !safety.allowGroups
	}
	addChangedString(cmd, settings, "allowed-chats", safety.allowedChats, "allowed_chat_ids")
	addChangedString(cmd, settings, "allowed-users", safety.allowedUsers, "allowed_user_ids")
}

func addAdapterCommonCommands(cmd *cobra.Command, channelID string, includeQR bool) {
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show channel adapter status",
		RunE: func(cmd *cobra.Command, args []string) error {
			ch, err := getChannel(channelID)
			if err != nil {
				return err
			}
			if outputJSON {
				return printJSON(ch)
			}
			printChannelSummary(ch)
			if includeQR {
				printWhatsAppWebQR(ch)
			}
			return nil
		},
	})
	if channelID == "http" {
		return
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "enable",
		Short: "Enable this channel",
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiPost("/channels/"+channelID+"/enable", nil)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "disable",
		Short: "Disable this channel",
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiPost("/channels/"+channelID+"/disable", nil)
		},
	})
}

func addChangedString(cmd *cobra.Command, settings map[string]any, flagName string, value string, settingName ...string) {
	if !cmd.Flags().Changed(flagName) {
		return
	}
	key := flagName
	if len(settingName) > 0 {
		key = settingName[0]
	}
	settings[key] = value
}

func patchChannelSettings(channelID string, settings map[string]any) error {
	if len(settings) == 0 {
		return fmt.Errorf("no settings provided")
	}
	body, _ := json.Marshal(map[string]any{"settings": settings})
	return apiPatch("/channels/"+channelID, body)
}

// ── Schedule ─────────────────────────────────────────────────────────────────

func buildScheduleCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "schedule", Short: "Manage scheduled agents"}
	cmd.AddCommand(&cobra.Command{
		Use: "list", Short: "List all scheduled agent entries",
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiGet("/schedule", "schedule")
		},
	})
	return cmd
}

// ── Memory ───────────────────────────────────────────────────────────────────

func buildMemoryCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "memory", Short: "Inspect and manage agent memory"}
	var agentID string
	listCmd := &cobra.Command{
		Use: "list", Short: "List memory entries for an agent",
		RunE: func(cmd *cobra.Command, args []string) error {
			if agentID == "" {
				return fmt.Errorf("--agent is required")
			}
			return apiGet("/memory/"+agentID, "")
		},
	}
	listCmd.Flags().StringVar(&agentID, "agent", "", "Agent ID")
	cmd.AddCommand(listCmd)
	cmd.AddCommand(buildMemoryReindexCmd())
	return cmd
}

// ── Skills ───────────────────────────────────────────────────────────────────

func buildSkillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Manage Agent Skills (agentskills.io format)",
	}

	// list
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List all loaded Agent Skills",
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiGet("/skills", "skills")
		},
	})

	// get
	cmd.AddCommand(&cobra.Command{
		Use:   "get <name>",
		Short: "Show full skill instructions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiGet("/skills/"+args[0], "")
		},
	})

	// install — local directory (original behaviour) OR remote package slug
	// resolved through the configured registries (Story E18).
	var assumeYes bool
	var allowUnverified bool
	installCmd := &cobra.Command{
		Use:   "install <path|slug>",
		Short: "Install a skill from a local directory, a registry slug, or a git source",
		Long: `Install a skill into ~/.soulacy/skills/.

Local directory:  sy skill install ./my-skill
Registry slug:    sy skill install self-improving-agent
Git source:       sy skill install github.com/user/my-skill

Remote installs resolve through the registries: block in config.yaml
(falling back to a bare git provider), run the safety introspection
pipeline (static scan + sandboxed dry-run), display a consent prompt,
and hot-load the skill through the gateway's /skills/rescan API.

Authenticity (SEC-7): installs from a registry with a configured
signing_key verify the package's ed25519 signature before proceeding.
Installs that cannot be verified — an unsigned package, a registry with
no signing_key, or a raw git source — are BLOCKED unless you pass
--allow-unverified to accept the risk explicitly.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Local directory keeps the original behaviour exactly.
			if st, err := os.Stat(args[0]); err == nil && st.IsDir() {
				return installSkill(args[0])
			}
			return runRemoteSkillInstall(cmd.Context(), args[0], assumeYes, allowUnverified)
		},
	}
	installCmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "Skip the consent prompt (never bypasses a danger verdict)")
	installCmd.Flags().BoolVar(&allowUnverified, "allow-unverified", false, "Allow installing packages whose authenticity cannot be cryptographically verified (unsigned, no signing_key, or raw git source)")
	cmd.AddCommand(installCmd)

	return cmd
}

// runRemoteSkillInstall assembles the E18 flow from the CLI environment:
// registries from the loaded viper config, consent on stdin, hot-load via
// the gateway API.
func runRemoteSkillInstall(ctx context.Context, slug string, assumeYes, allowUnverified bool) error {
	var regs []config.RegistryConfig
	if err := viper.UnmarshalKey("registries", &regs); err != nil {
		fmt.Fprintf(os.Stderr, "warning: registries config unreadable: %v\n", err)
	}
	log := zap.NewNop()
	eng := registriesFromConfig(regs, log)

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	return remoteSkillInstall(ctx, eng, slug, remoteInstallOpts{
		SkillsDir:       workspaceSkills(home),
		AssumeYes:       assumeYes,
		AllowUnverified: allowUnverified,
		Log:             log,
		Confirm: func(prompt string) bool {
			fmt.Print(prompt)
			var answer string
			_, _ = fmt.Scanln(&answer)
			a := strings.ToLower(strings.TrimSpace(answer))
			return a == "y" || a == "yes"
		},
		Rescan: func() error {
			req, rerr := http.NewRequest(http.MethodPost, gatewayURL+"/api/v1/skills/rescan", nil)
			if rerr != nil {
				return rerr
			}
			if apiKey != "" {
				req.Header.Set("Authorization", "Bearer "+apiKey)
			}
			resp, rerr := (&http.Client{Timeout: 10 * time.Second}).Do(req)
			if rerr != nil {
				return rerr
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("gateway returned %s", resp.Status)
			}
			return nil
		},
	})
}

// installSkill copies a skill directory into ~/.soulacy/skills/<name>/.
func installSkill(src string) error {
	// Validate that src contains a SKILL.md
	skillMD := src + "/SKILL.md"
	if _, err := os.Stat(skillMD); err != nil {
		return fmt.Errorf("not a valid skill directory: SKILL.md not found in %s", src)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	// Derive skill name from directory name
	name := src
	if name[len(name)-1] == '/' {
		name = name[:len(name)-1]
	}
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '/' || name[i] == '\\' {
			name = name[i+1:]
			break
		}
	}

	skillsRoot := workspaceSkills(home)
	dest := filepath.Join(skillsRoot, name)
	if err := os.MkdirAll(skillsRoot, 0755); err != nil {
		return err
	}

	fmt.Printf("Installing skill %q → %s\n", name, dest)
	if err := copyDir(src, dest); err != nil {
		return err
	}
	fmt.Printf("✓ Installed. Restart the gateway (or it will hot-reload on next skill scan).\n")
	return nil
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0644)
	})
}

// ── Logs ─────────────────────────────────────────────────────────────────────

func buildLogsCmd() *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Stream live event logs from the gateway",
		RunE: func(cmd *cobra.Command, args []string) error {
			if follow {
				fmt.Printf("Connecting to %s/ws/events ...\n", gatewayURL)
				fmt.Println("(WebSocket streaming — press Ctrl+C to stop)")
				// WebSocket client would go here; simplified for skeleton
				return streamEvents()
			}
			fmt.Println("Use --follow to stream live events")
			return nil
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Stream events in real-time")
	return cmd
}

func streamEvents() error {
	// Simple polling fallback; real implementation uses gorilla/websocket
	for {
		resp, err := apiCall("GET", "/health", nil)
		if err != nil {
			return err
		}
		fmt.Printf("[%s] gateway alive: %s\n", time.Now().Format(time.RFC3339), string(resp))
		time.Sleep(5 * time.Second)
	}
}

// ── Server ────────────────────────────────────────────────────────────────────

func buildServerCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "server", Short: "Control the Soulacy gateway server"}
	var binary string
	start := &cobra.Command{
		Use:   "start",
		Short: "Start the gateway server in the foreground",
		RunE: func(cmd *cobra.Command, args []string) error {
			gatewayBinary, err := resolveGatewayBinary(binary)
			if err != nil {
				return err
			}
			fmt.Printf("Starting Soulacy gateway with %s...\n", gatewayBinary)
			gateway := exec.Command(gatewayBinary, "serve")
			gateway.Stdin, gateway.Stdout, gateway.Stderr = os.Stdin, os.Stdout, os.Stderr
			gateway.Env = os.Environ()
			return gateway.Run()
		},
	}
	start.Flags().StringVar(&binary, "binary", "", "Path to the soulacy gateway binary")
	cmd.AddCommand(start)
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Check gateway health",
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiGet("/health", "")
		},
	})
	return cmd
}

func resolveGatewayBinary(explicit string) (string, error) {
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		if info, err := os.Stat(explicit); err != nil || info.IsDir() {
			return "", fmt.Errorf("soulacy gateway binary not found: %s", explicit)
		}
		return explicit, nil
	}
	if configured := strings.TrimSpace(os.Getenv("SOULACY_GATEWAY_BINARY")); configured != "" {
		return resolveGatewayBinary(configured)
	}
	if current, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(current), "soulacy")
		if info, statErr := os.Stat(sibling); statErr == nil && !info.IsDir() {
			return sibling, nil
		}
	}
	if found, err := exec.LookPath("soulacy"); err == nil {
		return found, nil
	}
	return "", errors.New("soulacy gateway binary was not found; install it, set SOULACY_GATEWAY_BINARY, or pass --binary")
}

// ── Version ───────────────────────────────────────────────────────────────────

// ── HTTP helpers ──────────────────────────────────────────────────────────────

func apiCall(method, path string, body []byte) ([]byte, error) {
	return apiCallWithTimeout(method, path, body, 30*time.Second)
}

func apiCallWithTimeout(method, path string, body []byte, timeout time.Duration) ([]byte, error) {
	return apiCallWithTimeoutRetry(method, path, body, timeout, true)
}

func apiCallWithTimeoutRetry(method, path string, body []byte, timeout time.Duration, allowRefresh bool) ([]byte, error) {
	url := gatewayURL + "/api/v1" + path
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if workspace := strings.TrimSpace(activeWorkspaceID); workspace != "" {
		req.Header.Set("X-Soulacy-Workspace", workspace)
	}
	if key := strings.TrimSpace(idempotencyKey); key != "" && method != http.MethodGet {
		req.Header.Set("Idempotency-Key", key)
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		if os.IsTimeout(err) {
			return nil, fmt.Errorf("gateway request timed out after %s: %s %s", timeout, method, path)
		}
		return nil, fmt.Errorf("cannot reach gateway at %s — is it running?\n  hint: run 'sy server start'", gatewayURL)
	}
	data, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized && allowRefresh && refreshCLISession() {
		return apiCallWithTimeoutRetry(method, path, body, timeout, false)
	}

	if warning := resp.Header.Get("X-Soulacy-Warning"); warning != "" {
		fmt.Fprintf(os.Stderr, "\n\033[1;33m%s\033[0m\n\n", warning)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("gateway error %d: %s", resp.StatusCode, string(data))
	}
	return data, nil
}

func apiGet(path, field string) error {
	data, err := apiCall("GET", path, nil)
	if err != nil {
		return err
	}
	if outputJSON || field == "" {
		fmt.Println(string(data))
		return nil
	}
	// Pretty-print the named field
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		fmt.Println(string(data))
		return nil
	}
	if val, ok := result[field]; ok {
		pretty, _ := json.MarshalIndent(val, "", "  ")
		fmt.Println(string(pretty))
	} else {
		pretty, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(pretty))
	}
	return nil
}

func apiPost(path string, body []byte) error {
	if body == nil {
		body = []byte("{}")
	}
	data, err := apiCall("POST", path, body)
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func apiPatch(path string, body []byte) error {
	if body == nil {
		body = []byte("{}")
	}
	data, err := apiCall("PATCH", path, body)
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func apiDelete(path string) error {
	_, err := apiCall("DELETE", path, nil)
	if err != nil {
		return err
	}
	fmt.Println("deleted.")
	return nil
}

func getChannel(id string) (map[string]any, error) {
	data, err := apiCall("GET", "/channels", nil)
	if err != nil {
		return nil, err
	}
	var result struct {
		Channels []map[string]any `json:"channels"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	for _, ch := range result.Channels {
		if fmt.Sprint(ch["id"]) == id {
			return ch, nil
		}
	}
	return nil, fmt.Errorf("channel %q not found", id)
}

func waitForChannelQR(id string, seconds int) (map[string]any, error) {
	if seconds < 0 {
		seconds = 0
	}
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	for {
		ch, err := getChannel(id)
		if err != nil {
			return nil, err
		}
		status := mapValue(ch, "status")
		if stringValue(status, "qr_code") != "" || boolValue(status, "connected") || time.Now().After(deadline) {
			return ch, nil
		}
		time.Sleep(time.Second)
	}
}

func printChannelSummary(ch map[string]any) {
	status := mapValue(ch, "status")
	settings := mapValue(ch, "settings")
	fmt.Printf("%s (%s)\n", fmt.Sprint(ch["name"]), fmt.Sprint(ch["id"]))
	fmt.Printf("  enabled:    %v\n", ch["enabled"])
	fmt.Printf("  configured: %v\n", ch["configured"])
	fmt.Printf("  connected:  %v\n", boolValue(status, "connected"))
	if detail := stringValue(status, "detail"); detail != "" {
		fmt.Printf("  detail:     %s\n", detail)
	}
	if fmt.Sprint(ch["id"]) == "whatsapp_web" {
		trigger := stringValue(settings, "trigger_phrase")
		if trigger == "" {
			trigger = "!soulacy"
		}
		fmt.Printf("  trigger:    %s\n", trigger)
		fmt.Printf("  groups:     %s\n", groupPolicyLabel(parseBoolSetting(settings["ignore_groups"], true)))
	}
}

func printWhatsAppWebQR(ch map[string]any) {
	status := mapValue(ch, "status")
	qr := stringValue(status, "qr_code")
	if qr == "" {
		if boolValue(status, "connected") {
			fmt.Println("WhatsApp Web is already connected.")
		} else {
			fmt.Println("No QR payload available yet. Run `sy channel whatsapp-web status` again, or use the Channels GUI for a rendered QR.")
		}
		return
	}
	fmt.Println()
	fmt.Println("QR payload:")
	fmt.Println(qr)
	fmt.Println()
	fmt.Println("Use the Channels GUI for a rendered QR, or render this payload in a trusted terminal QR tool.")
}

func printJSON(v any) error {
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(pretty))
	return nil
}

func parseCLIValue(value string) any {
	value = strings.TrimSpace(value)
	switch strings.ToLower(value) {
	case "true":
		return true
	case "false":
		return false
	default:
		return value
	}
}

func mapValue(m map[string]any, key string) map[string]any {
	raw, ok := m[key]
	if !ok {
		return map[string]any{}
	}
	asMap, ok := raw.(map[string]any)
	if ok {
		return asMap
	}
	return map[string]any{}
}

func stringValue(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		return strings.TrimSpace(fmt.Sprint(v))
	}
	return ""
}

func boolValue(m map[string]any, key string) bool {
	if v, ok := m[key]; ok {
		return parseBoolSetting(v, false)
	}
	return false
}

func parseBoolSetting(v any, fallback bool) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "yes", "1", "on":
			return true
		case "false", "no", "0", "off":
			return false
		}
	}
	return fallback
}

func groupPolicyLabel(ignoreGroups bool) string {
	if ignoreGroups {
		return "ignored"
	}
	return "allowed"
}

// workspaceSkills resolves the workspace skills dir (soulspace layout for
// new installs, flat ~/.soulacy/skills for legacy installations).
func workspaceSkills(home string) string {
	if ws, err := config.ResolveWorkspace(); err == nil {
		return ws.Skills
	}
	return filepath.Join(home, ".soulacy", "skills")
}

func listAgents() error {
	data, err := apiCall("GET", "/agents", nil)
	if err != nil {
		return err
	}
	if outputJSON {
		fmt.Println(string(data))
		return nil
	}
	var result struct {
		Agents []map[string]any `json:"agents"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		fmt.Println(string(data))
		return nil
	}
	agents := result.Agents

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tENABLED\tCHANNELS\tLLM")
	for _, agent := range agents {
		id := stringValue(agent, "id")
		name := stringValue(agent, "name")
		enabled := fmt.Sprintf("%v", agent["enabled"])

		var channels []string
		if chs, ok := agent["channels"].([]any); ok {
			for _, ch := range chs {
				channels = append(channels, fmt.Sprint(ch))
			}
		} else if chs, ok := agent["channels"].([]string); ok {
			channels = chs
		}
		chStr := strings.Join(channels, ", ")
		if chStr == "" {
			chStr = "(none)"
		}

		llmStr := "(none)"
		if llm, ok := agent["llm"].(map[string]any); ok {
			prov := stringValue(llm, "provider")
			model := stringValue(llm, "model")
			if prov != "" || model != "" {
				llmStr = fmt.Sprintf("%s/%s", prov, model)
			}
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", id, name, enabled, chStr, llmStr)
	}
	w.Flush()
	return nil
}

func listChannels() error {
	data, err := apiCall("GET", "/channels", nil)
	if err != nil {
		return err
	}
	if outputJSON {
		fmt.Println(string(data))
		return nil
	}

	var result struct {
		Channels []map[string]any `json:"channels"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		fmt.Println(string(data))
		return nil
	}
	channels := result.Channels

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tENABLED\tCONFIGURED\tCONNECTED\tDETAIL")
	for _, ch := range channels {
		id := stringValue(ch, "id")
		name := stringValue(ch, "name")
		enabled := fmt.Sprintf("%v", ch["enabled"])
		configured := fmt.Sprintf("%v", ch["configured"])

		status := mapValue(ch, "status")
		connected := fmt.Sprintf("%v", boolValue(status, "connected"))
		detail := stringValue(status, "detail")

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", id, name, enabled, configured, connected, detail)
	}
	w.Flush()
	return nil
}
