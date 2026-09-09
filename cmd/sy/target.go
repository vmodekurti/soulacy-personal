package main

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

type executionTarget int

const (
	targetLocal executionTarget = iota
	targetRemoteGateway
)

// targetMode is the single authority for deciding whether a command may touch
// the local workspace. Keep this decision independent from reachability: a
// temporarily unavailable remote gateway must never make a command fall back
// to mutating local files.
func targetMode(rawGateway string) executionTarget {
	rawGateway = strings.TrimSpace(rawGateway)
	if rawGateway == "" || strings.HasPrefix(rawGateway, "unix://") || strings.HasPrefix(rawGateway, "unix:") {
		return targetLocal
	}
	u, err := url.Parse(rawGateway)
	if err != nil || u.Hostname() == "" {
		// Invalid gateway values are treated as remote. The HTTP layer will return
		// the useful parse error, while local state remains protected.
		return targetRemoteGateway
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || host == "localhost.localdomain" {
		return targetLocal
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return targetLocal
	}
	return targetRemoteGateway
}

func isRemoteGateway() bool { return targetMode(gatewayURL) == targetRemoteGateway }

func targetDescription() string {
	if isRemoteGateway() {
		return "Remote: " + strings.TrimRight(gatewayURL, "/")
	}
	return "Local: ~/.soulacy"
}

func requireRemoteDelegation(feature string) error {
	return fmt.Errorf("%s requires remote gateway API delegation when targeting %s; no local files were changed", feature, gatewayURL)
}

// guardRemoteCommand blocks commands whose implementation operates on the
// client host. Read-only client diagnostics remain available; explicit client
// maintenance (update/upgrade/support output) also remains local by design.
// The guarded set is limited to commands whose wording could otherwise imply
// that the remote gateway was changed.
func guardRemoteCommand(commandPath string, dryRun bool) error {
	if !isRemoteGateway() {
		return nil
	}
	path := strings.TrimSpace(strings.TrimPrefix(commandPath, "sy "))
	if path == "workspace migrate" && dryRun {
		return nil
	}
	switch path {
	case "onboard", "setup", "server start", "daemon install", "daemon uninstall", "daemon start", "daemon stop",
		"workspace migrate", "seed-examples", "pull", "voice configure", "voice disable", "voice install", "voice start", "voice enable":
		return requireRemoteDelegation(path)
	default:
		return nil
	}
}
