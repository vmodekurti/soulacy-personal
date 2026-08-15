package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// credential mirrors the gateway's apikeys.APIKey JSON shape. The plaintext
// secret is deliberately absent: it exists only in the create/rotate response
// body and is never stored, echoed, or re-fetchable.
type credential struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Prefix         string     `json:"prefix"`
	Kind           string     `json:"kind"`
	SubjectID      string     `json:"subject_id"`
	OrganizationID string     `json:"organization_id"`
	WorkspaceIDs   []string   `json:"workspace_ids"`
	Role           string     `json:"role"`
	Scopes         []string   `json:"scopes"`
	Issuer         string     `json:"issuer"`
	Status         string     `json:"status"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	RotatedFromID  string     `json:"rotated_from_id,omitempty"`
}

const (
	credentialKindPersonal = "personal_access_token"
	credentialKindService  = "service_account"
)

// buildCredentialCmd assembles `sy credential`, the scoped-credential surface
// for scripts and CI. It is deliberately separate from `sy secrets`: secrets
// are workspace data, credentials are authority to act on that data.
func buildCredentialCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "credential",
		Aliases: []string{"credentials", "token"},
		Short:   "Issue and manage scoped personal and service-account credentials",
		Long: `Manage scoped credentials for scripts, CI, and automation.

A credential is bound to one organization and to an explicit list of
workspaces, and carries resource:action scopes, an expiry, an issuer, and a
stable credential ID. Automation should use a service-account credential so
audit records name the service account rather than a human operator.

The plaintext secret is displayed exactly once, at creation or rotation. The
gateway stores only a strong hash and cannot show it again.`,
	}
	cmd.AddCommand(buildCredentialCreateCmd())
	cmd.AddCommand(buildCredentialListCmd())
	cmd.AddCommand(buildCredentialRotateCmd())
	cmd.AddCommand(buildCredentialRevokeCmd())
	cmd.AddCommand(buildCredentialStatusCmd())
	return cmd
}

func buildCredentialCreateCmd() *cobra.Command {
	var (
		kind       string
		subject    string
		workspaces []string
		role       string
		scopes     []string
		expiresIn  string
		org        string
	)
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Issue a credential and print its secret once",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			normalizedKind, err := normalizeCredentialKind(kind)
			if err != nil {
				return err
			}
			if normalizedKind == credentialKindService && strings.TrimSpace(subject) == "" {
				return fmt.Errorf("--subject is required for a service-account credential: pass the service account ID")
			}
			body := map[string]any{"name": args[0], "kind": normalizedKind}
			if len(scopes) > 0 {
				body["scopes"] = splitCommaValues(scopes)
			}
			if len(workspaces) > 0 {
				body["workspace_ids"] = splitCommaValues(workspaces)
			}
			if s := strings.TrimSpace(subject); s != "" {
				body["subject_id"] = s
			}
			if r := strings.TrimSpace(role); r != "" {
				body["role"] = strings.ToLower(r)
			}
			if o := strings.TrimSpace(org); o != "" {
				body["organization_id"] = o
			}
			if strings.TrimSpace(expiresIn) != "" {
				expiry, err := parseCredentialExpiry(expiresIn)
				if err != nil {
					return err
				}
				body["expires_at"] = expiry.Format(time.RFC3339)
			}
			payload, err := json.Marshal(body)
			if err != nil {
				return err
			}
			data, err := apiCall("POST", "/admin/api-keys", payload)
			if err != nil {
				return err
			}
			if outputJSON {
				fmt.Println(string(data))
				return nil
			}
			var created struct {
				credential
				Key string `json:"key"`
			}
			if err := json.Unmarshal(data, &created); err != nil {
				fmt.Println(string(data))
				return nil
			}
			printCredentialDetail(created.credential)
			fmt.Println()
			fmt.Println("Secret (shown once — it cannot be retrieved again):")
			fmt.Println("  " + created.Key)
			fmt.Fprintln(os.Stderr, "\nStore this in your CI secret store now. Rotate with 'sy credential rotate "+created.ID+"'.")
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "personal", "Credential kind: personal or service")
	cmd.Flags().StringVar(&subject, "subject", "", "Service account ID (required for --kind service)")
	cmd.Flags().StringSliceVar(&workspaces, "workspace", nil, "Workspace ID to bind (repeatable; defaults to the active workspace)")
	cmd.Flags().StringVar(&role, "role", "", "Role granted in the bound workspaces (never exceeds your own)")
	cmd.Flags().StringSliceVar(&scopes, "scope", nil, "resource:action scope (repeatable, e.g. agents:read)")
	cmd.Flags().StringVar(&expiresIn, "expires-in", "", "Lifetime, e.g. 30d, 12h, 90d (default: server policy)")
	cmd.Flags().StringVar(&org, "organization", "", "Organization ID (defaults to the active organization)")
	return cmd
}

func buildCredentialListCmd() *cobra.Command {
	var includeRevoked bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List credentials visible in the active workspace",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "/admin/api-keys"
			if includeRevoked {
				path += "?include_revoked=true"
			}
			data, err := apiCall("GET", path, nil)
			if err != nil {
				return err
			}
			if outputJSON {
				fmt.Println(string(data))
				return nil
			}
			var result struct {
				Keys []credential `json:"keys"`
			}
			if err := json.Unmarshal(data, &result); err != nil {
				return fmt.Errorf("decode credentials response: %w", err)
			}
			if len(result.Keys) == 0 {
				fmt.Println("No credentials in this workspace.")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tPRINCIPAL\tROLE\tWORKSPACES\tSCOPES\tSTATUS\tEXPIRES")
			for _, key := range result.Keys {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					key.ID, key.Name, credentialPrincipal(key), orNone(key.Role),
					joinOrNone(key.WorkspaceIDs), joinOrNone(key.Scopes),
					orNone(key.Status), formatCredentialExpiry(key.ExpiresAt))
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&includeRevoked, "include-revoked", false, "Include revoked and suspended credentials")
	return cmd
}

func buildCredentialRotateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rotate <id>",
		Short: "Revoke a credential and issue a replacement with identical authority",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := apiCall("POST", "/admin/api-keys/"+args[0]+"/rotate", []byte("{}"))
			if err != nil {
				return err
			}
			if outputJSON {
				fmt.Println(string(data))
				return nil
			}
			var rotated struct {
				Key        string     `json:"key"`
				Credential credential `json:"credential"`
			}
			if err := json.Unmarshal(data, &rotated); err != nil {
				fmt.Println(string(data))
				return nil
			}
			fmt.Printf("Rotated %s. The previous credential is revoked and is already being rejected.\n\n", args[0])
			printCredentialDetail(rotated.Credential)
			fmt.Println()
			fmt.Println("Secret (shown once — it cannot be retrieved again):")
			fmt.Println("  " + rotated.Key)
			return nil
		},
	}
}

func buildCredentialRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <id>",
		Short: "Revoke a credential immediately",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := apiCall("DELETE", "/admin/api-keys/"+args[0], nil); err != nil {
				return err
			}
			fmt.Printf("Revoked %s. It is rejected from the next request onward; no restart is required.\n", args[0])
			return nil
		},
	}
}

func buildCredentialStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <id> <active|suspended|revoked|deleted>",
		Short: "Change a credential's status without restarting the gateway",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			status := strings.ToLower(strings.TrimSpace(args[1]))
			switch status {
			case "active", "suspended", "revoked", "deleted":
			default:
				return fmt.Errorf("status must be active, suspended, revoked, or deleted")
			}
			payload, err := json.Marshal(map[string]string{"status": status})
			if err != nil {
				return err
			}
			return apiPatch("/admin/api-keys/"+args[0]+"/status", payload)
		},
	}
}

// credentialPrincipal names the acting principal the way audit records do, so
// a service account is never reported as a generic API user.
func credentialPrincipal(key credential) string {
	subject := strings.TrimSpace(key.SubjectID)
	if subject == "" {
		subject = "(unbound)"
	}
	switch strings.TrimSpace(key.Kind) {
	case credentialKindService:
		return "service-account " + subject
	case credentialKindPersonal:
		return "user " + subject
	default:
		return subject
	}
}

func printCredentialDetail(key credential) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "ID:\t%s\n", key.ID)
	fmt.Fprintf(w, "Name:\t%s\n", key.Name)
	fmt.Fprintf(w, "Principal:\t%s\n", credentialPrincipal(key))
	fmt.Fprintf(w, "Organization:\t%s\n", orNone(key.OrganizationID))
	fmt.Fprintf(w, "Workspaces:\t%s\n", joinOrNone(key.WorkspaceIDs))
	fmt.Fprintf(w, "Role:\t%s\n", orNone(key.Role))
	fmt.Fprintf(w, "Scopes:\t%s\n", joinOrNone(key.Scopes))
	fmt.Fprintf(w, "Issuer:\t%s\n", orNone(key.Issuer))
	fmt.Fprintf(w, "Status:\t%s\n", orNone(key.Status))
	fmt.Fprintf(w, "Expires:\t%s\n", formatCredentialExpiry(key.ExpiresAt))
	_ = w.Flush()
}

func normalizeCredentialKind(kind string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "personal", "pat", credentialKindPersonal:
		return credentialKindPersonal, nil
	case "service", "service-account", credentialKindService:
		return credentialKindService, nil
	default:
		return "", fmt.Errorf("unknown credential kind %q: use personal or service", kind)
	}
}

// parseCredentialExpiry accepts Go durations plus a day suffix, because CI
// lifetimes are naturally expressed in days and time.ParseDuration is not.
func parseCredentialExpiry(value string) (time.Time, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return time.Time{}, fmt.Errorf("expiry is required")
	}
	var duration time.Duration
	if strings.HasSuffix(value, "d") {
		var days float64
		if _, err := fmt.Sscanf(strings.TrimSuffix(value, "d"), "%g", &days); err != nil || days <= 0 {
			return time.Time{}, fmt.Errorf("invalid expiry %q: use a form like 30d, 12h, or 90d", value)
		}
		duration = time.Duration(days * float64(24*time.Hour))
	} else {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			return time.Time{}, fmt.Errorf("invalid expiry %q: use a form like 30d, 12h, or 90d", value)
		}
		duration = parsed
	}
	return time.Now().UTC().Add(duration), nil
}

// splitCommaValues lets --scope agents:read,agents:run behave like repeating
// the flag, which is what people reach for first in a CI script.
func splitCommaValues(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if _, duplicate := seen[part]; duplicate {
				continue
			}
			seen[part] = struct{}{}
			out = append(out, part)
		}
	}
	return out
}

func formatCredentialExpiry(at *time.Time) string {
	if at == nil {
		return "never"
	}
	utc := at.UTC()
	if !utc.After(time.Now().UTC()) {
		return utc.Format(time.RFC3339) + " (expired)"
	}
	return utc.Format(time.RFC3339)
}

func joinOrNone(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	return strings.Join(values, ",")
}

func orNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(none)"
	}
	return value
}
