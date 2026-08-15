// context.go — named server/workspace contexts for the `sy` CLI.
//
// A context records *where* a command goes and *which* workspace it targets.
// It never records how to authenticate. Access tokens, refresh tokens, and API
// keys live in the operating system credential store (see auth.go) or arrive
// through the environment for CI; the context file on disk is safe to read,
// diff, and back up.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// EnvContext selects a context non-interactively. EnvAPIKey supplies a
// credential without placing it in a command line, where it would be captured
// by shell history and by the process table of every other user on the host.
const (
	EnvContext = "SOULACY_CONTEXT"
	EnvAPIKey  = "SOULACY_API_KEY"
)

// syContext is deliberately a record of non-secret selectors only. Adding a
// token-bearing field here would silently downgrade every user's disk from
// "safe to back up" to "contains live credentials"; TestContextFileNeverHolds
// Secrets in context_test.go fails the build if that happens.
type syContext struct {
	Name           string `json:"name"`
	Server         string `json:"server"`
	OrganizationID string `json:"organization_id,omitempty"`
	WorkspaceID    string `json:"workspace_id,omitempty"`
	Subject        string `json:"subject,omitempty"`
	PrincipalKind  string `json:"principal_kind,omitempty"`
	Role           string `json:"role,omitempty"`
}

type contextFile struct {
	Current  string               `json:"current"`
	Contexts map[string]syContext `json:"contexts"`
}

// identityView mirrors the gateway's identityResponse. The CLI reports what
// the server resolved rather than what the local token claims, so `sy whoami`
// cannot disagree with what the next request will actually be allowed to do.
type identityView struct {
	Subject        string   `json:"subject"`
	PrincipalKind  string   `json:"principal_kind"`
	CredentialID   string   `json:"credential_id,omitempty"`
	OrganizationID string   `json:"organization_id"`
	WorkspaceID    string   `json:"workspace_id"`
	MembershipID   string   `json:"membership_id"`
	Role           string   `json:"role"`
	Scopes         []string `json:"scopes"`
	DeploymentMode string   `json:"deployment_mode"`
}

type workspaceView struct {
	OrganizationID   string `json:"organization_id"`
	OrganizationName string `json:"organization_name,omitempty"`
	WorkspaceID      string `json:"workspace_id"`
	WorkspaceName    string `json:"workspace_name,omitempty"`
	MembershipID     string `json:"membership_id"`
	Role             string `json:"role"`
	PrincipalKind    string `json:"principal_kind"`
}

func contextFilePath() string {
	return filepath.Join(syWorkspace().Root, "cli", "contexts.json")
}

func loadContexts() (contextFile, error) {
	file := contextFile{Contexts: map[string]syContext{}}
	raw, err := os.ReadFile(contextFilePath())
	if errors.Is(err, os.ErrNotExist) {
		return file, nil
	}
	if err != nil {
		return file, fmt.Errorf("read contexts: %w", err)
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return file, fmt.Errorf("parse %s: %w", contextFilePath(), err)
	}
	if file.Contexts == nil {
		file.Contexts = map[string]syContext{}
	}
	return file, nil
}

func saveContexts(file contextFile) error {
	path := contextFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create context directory: %w", err)
	}
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	// 0600: the file holds no secrets, but it does describe which production
	// servers this machine talks to, which is not everyone's business.
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}

// activeContext resolves the context in force for this invocation.
// SOULACY_CONTEXT wins so CI can select a target without mutating shared state
// in a checkout that other jobs may be using concurrently.
func activeContext() (syContext, bool) {
	file, err := loadContexts()
	if err != nil {
		return syContext{}, false
	}
	name := strings.TrimSpace(os.Getenv(EnvContext))
	if name == "" {
		name = strings.TrimSpace(file.Current)
	}
	if name == "" {
		return syContext{}, false
	}
	ctx, ok := file.Contexts[name]
	return ctx, ok
}

// applyActiveContext seeds the gateway target and workspace selector before a
// command runs. Explicit flags always win over the stored context, and the
// stored context wins over the built-in loopback default.
func applyActiveContext() {
	ctx, ok := activeContext()
	if !ok {
		return
	}
	if gatewayURL == "" && strings.TrimSpace(ctx.Server) != "" {
		gatewayURL = strings.TrimRight(strings.TrimSpace(ctx.Server), "/")
	}
	if activeWorkspaceID == "" && strings.TrimSpace(ctx.WorkspaceID) != "" {
		activeWorkspaceID = strings.TrimSpace(ctx.WorkspaceID)
	}
}

func buildContextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Manage named server and workspace targets",
		Long: `Manage named contexts so local, staging, and production are never ambiguous.

A context stores a server URL and a workspace selection. It never stores access
tokens, refresh tokens, or API keys: interactive sessions live in your operating
system credential store, and CI supplies a credential through ` + EnvAPIKey + `
rather than a command-line flag, which would leak into shell history and the
process table.

Select a context without changing shared state by setting ` + EnvContext + `.`,
	}
	cmd.AddCommand(buildContextAddCmd())
	cmd.AddCommand(buildContextListCmd())
	cmd.AddCommand(buildContextUseCmd())
	cmd.AddCommand(buildContextShowCmd())
	cmd.AddCommand(buildContextDeleteCmd())
	return cmd
}

func buildContextAddCmd() *cobra.Command {
	var server, workspace, organization string
	var use bool
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add or update a named context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if name == "" {
				return errors.New("context name is required")
			}
			if strings.TrimSpace(server) == "" {
				return errors.New("--server is required, e.g. --server https://soulacy.example.com")
			}
			file, err := loadContexts()
			if err != nil {
				return err
			}
			file.Contexts[name] = syContext{
				Name:           name,
				Server:         strings.TrimRight(strings.TrimSpace(server), "/"),
				OrganizationID: strings.TrimSpace(organization),
				WorkspaceID:    strings.TrimSpace(workspace),
			}
			if use || file.Current == "" {
				file.Current = name
			}
			if err := saveContexts(file); err != nil {
				return err
			}
			return emitContext(file.Contexts[name], file.Current == name)
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "Gateway base URL")
	cmd.Flags().StringVar(&workspace, "workspace", "", "Workspace ID to target")
	cmd.Flags().StringVar(&organization, "organization", "", "Organization ID to target")
	cmd.Flags().BoolVar(&use, "use", false, "Make this the current context")
	return cmd
}

func buildContextListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured contexts",
		RunE: func(cmd *cobra.Command, args []string) error {
			file, err := loadContexts()
			if err != nil {
				return err
			}
			names := make([]string, 0, len(file.Contexts))
			for name := range file.Contexts {
				names = append(names, name)
			}
			sort.Strings(names)
			if outputJSON {
				ordered := make([]syContext, 0, len(names))
				for _, name := range names {
					ordered = append(ordered, file.Contexts[name])
				}
				return emitJSON(map[string]any{"current": file.Current, "contexts": ordered})
			}
			if len(names) == 0 {
				fmt.Println("No contexts configured. Add one with 'sy context add <name> --server <url>'.")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			fmt.Fprintln(w, "\tNAME\tSERVER\tORGANIZATION\tWORKSPACE")
			for _, name := range names {
				marker := " "
				if name == file.Current {
					marker = "*"
				}
				ctx := file.Contexts[name]
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", marker, ctx.Name, ctx.Server, orNone(ctx.OrganizationID), orNone(ctx.WorkspaceID))
			}
			return w.Flush()
		},
	}
}

func buildContextUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Make a context current",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file, err := loadContexts()
			if err != nil {
				return err
			}
			ctx, ok := file.Contexts[strings.TrimSpace(args[0])]
			if !ok {
				return fmt.Errorf("no context named %q — run 'sy context list'", args[0])
			}
			file.Current = ctx.Name
			if err := saveContexts(file); err != nil {
				return err
			}
			return emitContext(ctx, true)
		},
	}
}

func buildContextShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show [name]",
		Short: "Show a context and the identity the server resolves for it",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file, err := loadContexts()
			if err != nil {
				return err
			}
			name := strings.TrimSpace(file.Current)
			if len(args) == 1 {
				name = strings.TrimSpace(args[0])
			} else if env := strings.TrimSpace(os.Getenv(EnvContext)); env != "" {
				name = env
			}
			ctx, ok := file.Contexts[name]
			if !ok {
				// No stored context is a legitimate state: Personal mode talks
				// to the loopback gateway without ever adding one.
				ctx = syContext{Name: "(none)", Server: gatewayURL}
			}
			identity, identityErr := fetchIdentity()
			if outputJSON {
				out := map[string]any{"context": ctx, "current": ctx.Name == file.Current}
				if identityErr == nil {
					out["identity"] = identity
				} else {
					out["identity_error"] = identityErr.Error()
				}
				return emitJSON(out)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "Context:\t%s\n", ctx.Name)
			fmt.Fprintf(w, "Server:\t%s\n", orNone(ctx.Server))
			if identityErr != nil {
				_ = w.Flush()
				fmt.Fprintf(os.Stderr, "\nCould not resolve identity from the server: %v\n", identityErr)
				return nil
			}
			fmt.Fprintf(w, "Organization:\t%s\n", orNone(identity.OrganizationID))
			fmt.Fprintf(w, "Workspace:\t%s\n", orNone(identity.WorkspaceID))
			fmt.Fprintf(w, "Principal:\t%s\n", describePrincipal(identity))
			fmt.Fprintf(w, "Role:\t%s\n", orNone(identity.Role))
			fmt.Fprintf(w, "Scopes:\t%s\n", joinOrNone(identity.Scopes))
			fmt.Fprintf(w, "Deployment:\t%s\n", orNone(identity.DeploymentMode))
			return w.Flush()
		},
	}
}

func buildContextDeleteCmd() *cobra.Command {
	var assumeYes bool
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a named context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file, err := loadContexts()
			if err != nil {
				return err
			}
			name := strings.TrimSpace(args[0])
			ctx, ok := file.Contexts[name]
			if !ok {
				return fmt.Errorf("no context named %q", name)
			}
			if !assumeYes && !confirmTarget("Delete context "+name, ctx.Server, ctx.WorkspaceID) {
				fmt.Fprintln(os.Stderr, "Aborted.")
				return nil
			}
			delete(file.Contexts, name)
			if file.Current == name {
				file.Current = ""
			}
			if err := saveContexts(file); err != nil {
				return err
			}
			if outputJSON {
				return emitJSON(map[string]any{"deleted": name, "current": file.Current})
			}
			fmt.Printf("Deleted context %s.\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "Skip the confirmation prompt")
	return cmd
}

func buildWhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the identity, workspace, and role the server resolves for you",
		RunE: func(cmd *cobra.Command, args []string) error {
			identity, err := fetchIdentity()
			if err != nil {
				return err
			}
			if outputJSON {
				return emitJSON(identity)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "Server:\t%s\n", gatewayURL)
			fmt.Fprintf(w, "Principal:\t%s\n", describePrincipal(identity))
			fmt.Fprintf(w, "Organization:\t%s\n", orNone(identity.OrganizationID))
			fmt.Fprintf(w, "Workspace:\t%s\n", orNone(identity.WorkspaceID))
			fmt.Fprintf(w, "Role:\t%s\n", orNone(identity.Role))
			fmt.Fprintf(w, "Scopes:\t%s\n", joinOrNone(identity.Scopes))
			fmt.Fprintf(w, "Deployment:\t%s\n", orNone(identity.DeploymentMode))
			return w.Flush()
		},
	}
}

func fetchIdentity() (identityView, error) {
	data, err := apiCall("GET", "/workspace/identity", nil)
	if err != nil {
		return identityView{}, err
	}
	var identity identityView
	if err := json.Unmarshal(data, &identity); err != nil {
		return identityView{}, fmt.Errorf("decode identity: %w", err)
	}
	return identity, nil
}

func describePrincipal(identity identityView) string {
	subject := strings.TrimSpace(identity.Subject)
	if subject == "" {
		return "(unauthenticated)"
	}
	switch strings.TrimSpace(identity.PrincipalKind) {
	case "service_account":
		return "service-account " + subject
	case "personal_access_token":
		return "user " + subject + " (personal access token)"
	case "static_api_key":
		return "static server key"
	default:
		return "user " + subject
	}
}

func emitContext(ctx syContext, current bool) error {
	if outputJSON {
		return emitJSON(map[string]any{"context": ctx, "current": current})
	}
	fmt.Printf("Context %s → %s", ctx.Name, ctx.Server)
	if ctx.WorkspaceID != "" {
		fmt.Printf(" (workspace %s)", ctx.WorkspaceID)
	}
	if current {
		fmt.Print(" [current]")
	}
	fmt.Println()
	return nil
}

// emitJSON is the single writer for --json output. Everything else a command
// says goes to stderr, so `sy ... --json | jq` never chokes on a progress line.
func emitJSON(value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}

// confirmTarget names the server and workspace in the prompt. "Are you sure?"
// is not a safeguard when the operator has four terminals open and one of them
// is production.
func confirmTarget(action, server, workspace string) bool {
	target := server
	if strings.TrimSpace(target) == "" {
		target = "(no server)"
	}
	if strings.TrimSpace(workspace) != "" {
		target += ", workspace " + workspace
	}
	return confirm(fmt.Sprintf("%s on %s?", action, target), false)
}
