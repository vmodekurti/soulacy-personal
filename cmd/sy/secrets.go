package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// secretDescriptor mirrors the gateway's secrets.Descriptor JSON shape. Values
// are never transmitted, so there is no value field.
type secretDescriptor struct {
	Name        string `json:"name"`
	Category    string `json:"category"`
	EnvVar      string `json:"env_var"`
	Description string `json:"description"`
	Set         bool   `json:"set"`
}

// buildSecretsCmd assembles the `sy secrets` command group, which manages the
// gateway-global secrets store over the REST API. Values are never printed.
func buildSecretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Manage the gateway-global secrets store",
		Long: `Manage gateway-global secrets (LLM keys, channel tokens, server key,
and custom tool secrets) stored in the encrypted credential vault.

Secret values are write-only over the API: they are never returned or printed.`,
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List known secret slots and whether each is set",
		RunE: func(cmd *cobra.Command, args []string) error {
			return secretsList()
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "set <name> [value]",
		Short: "Set a secret value (prompted without echo if omitted)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			var value string
			if len(args) == 2 {
				value = args[1]
			} else {
				v, err := promptSecret(fmt.Sprintf("Value for %q: ", name))
				if err != nil {
					return err
				}
				value = v
			}
			if value == "" {
				return fmt.Errorf("value is required")
			}
			return secretsSet(name, value)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "rm <name>",
		Short: "Remove a secret",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return secretsRemove(args[0])
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "import",
		Short: "Explain how plaintext config secrets migrate into the vault",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("Secret import is automatic.")
			fmt.Println()
			fmt.Println("On gateway startup, any plaintext secrets still present in config.yaml")
			fmt.Println("(LLM provider api_keys, channel tokens, server.api_key) are migrated")
			fmt.Println("into the encrypted credential vault and blanked from the in-memory config.")
			fmt.Println()
			fmt.Println("To trigger migration, simply restart the gateway:")
			fmt.Println("  sy server restart")
			fmt.Println()
			fmt.Println("Then verify with:")
			fmt.Println("  sy secrets list")
		},
	})

	return cmd
}

// secretsList fetches the catalog and prints it as a table. Values are never
// requested or printed.
func secretsList() error {
	data, err := apiCall("GET", "/secrets", nil)
	if err != nil {
		return err
	}
	if outputJSON {
		fmt.Println(string(data))
		return nil
	}
	var result struct {
		Secrets []secretDescriptor `json:"secrets"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("decode secrets response: %w", err)
	}
	if len(result.Secrets) == 0 {
		fmt.Println("No secret slots found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NAME\tCATEGORY\tSET\tENV FALLBACK")
	for _, d := range result.Secrets {
		set := "✗"
		if d.Set {
			set = "✓"
		}
		env := d.EnvVar
		if env == "" {
			env = "(none)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", d.Name, d.Category, set, env)
	}
	return w.Flush()
}

// secretsSet stores a value via PUT /secrets/:name → 204.
func secretsSet(name, value string) error {
	body, err := json.Marshal(map[string]string{"value": value})
	if err != nil {
		return err
	}
	if _, err := apiCall("PUT", "/secrets/"+url.PathEscape(name), body); err != nil {
		return err
	}
	fmt.Printf("Secret %q set.\n", name)
	return nil
}

// secretsRemove deletes a secret via DELETE /secrets/:name → 204.
func secretsRemove(name string) error {
	if _, err := apiCall("DELETE", "/secrets/"+url.PathEscape(name), nil); err != nil {
		return err
	}
	fmt.Printf("Secret %q removed.\n", name)
	return nil
}

// promptSecret reads a secret from stdin. It avoids echoing when stdin is a
// terminal by toggling the terminal echo via stty; on platforms/streams where
// that is unavailable it falls back to a plain read (still never echoed back by
// us, and the value is never printed).
func promptSecret(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	noEcho := disableEcho()
	defer restoreEcho(noEcho)

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if noEcho {
		fmt.Fprintln(os.Stderr)
	}
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
