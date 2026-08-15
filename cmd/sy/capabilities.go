// capabilities.go — client-side compatibility negotiation.
//
// The CLI asks the gateway what it can do before attempting an operation the
// gateway may not implement. The alternative — send the request and interpret
// whatever comes back — cannot distinguish "this server is older than you
// think" from "your credentials are wrong" from "that resource is gone", and
// an upgrade that guesses wrong corrupts resources instead of failing.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/soulacy/soulacy/internal/apiversion"
	"github.com/soulacy/soulacy/internal/config"
)

var (
	capabilitiesOnce sync.Once
	cachedCaps       apiversion.Capabilities
	cachedCapsErr    error
)

// serverCapabilities fetches and caches the gateway's capability document for
// the life of the process. One handshake per invocation is enough; refetching
// per command would add a round trip to every operation.
func serverCapabilities() (apiversion.Capabilities, error) {
	capabilitiesOnce.Do(func() {
		data, err := apiCall("GET", "/capabilities", nil)
		if err != nil {
			cachedCapsErr = err
			return
		}
		if err := json.Unmarshal(data, &cachedCaps); err != nil {
			cachedCapsErr = fmt.Errorf("decode server capabilities: %w", err)
		}
	})
	return cachedCaps, cachedCapsErr
}

// requireServerFeature is the handshake to call at the top of a command that
// depends on a capability. A server that predates the capability endpoint
// entirely is allowed through: refusing there would break every older
// deployment on the day this ships, and the operation itself will still fail
// safely with its own error.
func requireServerFeature(feature, operation string) error {
	caps, err := serverCapabilities()
	if err != nil {
		return nil
	}
	if strings.TrimSpace(caps.APIVersion) == "" {
		return nil
	}
	if err := caps.CheckAPIVersion(apiversion.APIVersion); err != nil {
		return err
	}
	if err := caps.CheckClient(config.Version); err != nil {
		return err
	}
	return caps.RequireFeature(feature, operation)
}

// describeIncompatibility renders a typed incompatibility the way an operator
// needs to read it: what is wrong, and what to run about it.
func describeIncompatibility(err error) (string, bool) {
	var typed *apiversion.IncompatibleError
	if !errors.As(err, &typed) {
		return "", false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", typed.Message)
	if typed.ClientVersion != "" || typed.ServerVersion != "" {
		fmt.Fprintf(&b, "  client: %s\n  server: %s\n", orNone(typed.ClientVersion), orNone(typed.ServerVersion))
	}
	fmt.Fprintf(&b, "  fix: %s", typed.Remedy)
	return b.String(), true
}

func buildCompatVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print client and gateway version and compatibility information",
		RunE: func(cmd *cobra.Command, args []string) error {
			caps, err := serverCapabilities()
			if outputJSON {
				out := map[string]any{
					"client_version":     config.Version,
					"client_api_version": apiversion.APIVersion,
					"gateway":            gatewayURL,
				}
				if err != nil {
					out["server_error"] = err.Error()
				} else {
					out["server"] = caps
					out["compatible"] = caps.CheckClient(config.Version) == nil && caps.CheckAPIVersion(apiversion.APIVersion) == nil
				}
				return emitJSON(out)
			}
			fmt.Printf("Soulacy CLI %s (API %s)\n", config.Version, apiversion.APIVersion)
			fmt.Printf("Gateway:    %s\n", gatewayURL)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\nCould not reach the gateway to check compatibility: %v\n", err)
				return nil
			}
			fmt.Printf("Server:     %s (API %s, %s mode)\n", orNone(caps.ServerVersion), orNone(caps.APIVersion), orNone(caps.DeploymentMode))
			fmt.Printf("Features:   %s\n", joinOrNone(caps.Features))
			for _, check := range []error{caps.CheckAPIVersion(apiversion.APIVersion), caps.CheckClient(config.Version)} {
				if description, ok := describeIncompatibility(check); ok {
					fmt.Fprintf(os.Stderr, "\nIncompatible: %s\n", description)
					return check
				}
			}
			fmt.Println("Compatible: yes")
			return nil
		},
	}
}
