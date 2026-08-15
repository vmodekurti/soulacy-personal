// Package apiversion is the single source of truth for what this build of the
// Soulacy API can do and which client versions it will work with.
//
// It exists so an upgrade fails loudly instead of quietly corrupting
// resources. A CLI that predates a feature must be told "upgrade", not left to
// send a request the server silently reinterprets — and a server that predates
// a CLI must not be handed fields it will drop on the floor.
package apiversion

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// APIVersion is the wire contract version. It changes only on a breaking
// change to an existing route's request or response shape. Adding a field or a
// route is additive and does not bump it.
const APIVersion = "v1"

// Feature names are stable identifiers a client may check before attempting an
// operation. Removing one is a breaking change; adding one is not.
const (
	FeatureWorkspaceContexts  = "workspace-contexts"
	FeatureScopedCredentials  = "scoped-credentials"
	FeatureServiceAccounts    = "service-accounts"
	FeatureMemberManagement   = "member-management"
	FeatureIdempotentMutation = "idempotent-mutation"
	FeatureConcurrencyControl = "concurrency-control"
	FeatureOIDCLogin          = "oidc-login"
)

// MinCLIVersion is the oldest CLI this server will serve without warning.
// MaxCLIVersion is empty because a newer CLI degrades gracefully: it
// negotiates features rather than assuming them. Set it only if a future
// client is known to be actively incompatible.
const (
	MinCLIVersion = "0.1.0"
	MaxCLIVersion = ""
)

// Capabilities is the negotiation payload. It is deliberately flat and
// additive: an older client that does not know a field ignores it, and a
// newer client that misses a field treats the feature as absent.
type Capabilities struct {
	APIVersion     string   `json:"api_version"`
	ServerVersion  string   `json:"server_version"`
	DeploymentMode string   `json:"deployment_mode"`
	Features       []string `json:"features"`
	MinCLIVersion  string   `json:"min_cli_version"`
	MaxCLIVersion  string   `json:"max_cli_version,omitempty"`
}

// Supported lists every feature this build implements, sorted so the payload
// is byte-stable across restarts and easy to diff in a bug report.
func Supported() []string {
	features := []string{
		FeatureWorkspaceContexts,
		FeatureScopedCredentials,
		FeatureServiceAccounts,
		FeatureMemberManagement,
		FeatureIdempotentMutation,
		FeatureConcurrencyControl,
		FeatureOIDCLogin,
	}
	sort.Strings(features)
	return features
}

func Describe(serverVersion, deploymentMode string) Capabilities {
	return Capabilities{
		APIVersion:     APIVersion,
		ServerVersion:  strings.TrimSpace(serverVersion),
		DeploymentMode: strings.TrimSpace(deploymentMode),
		Features:       Supported(),
		MinCLIVersion:  MinCLIVersion,
		MaxCLIVersion:  MaxCLIVersion,
	}
}

func (c Capabilities) Has(feature string) bool {
	for _, candidate := range c.Features {
		if candidate == feature {
			return true
		}
	}
	return false
}

// IncompatibleError is the typed failure a client can branch on. It always
// carries a Remedy: telling someone their versions are incompatible without
// telling them what to run is a bug report waiting to happen.
type IncompatibleError struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	Remedy        string `json:"remedy"`
	ClientVersion string `json:"client_version,omitempty"`
	ServerVersion string `json:"server_version,omitempty"`
	Feature       string `json:"feature,omitempty"`
}

func (e *IncompatibleError) Error() string { return e.Message }

// Error codes are part of the contract; clients match on these, not on prose.
const (
	CodeClientTooOld    = "client_too_old"
	CodeClientTooNew    = "client_too_new"
	CodeAPIMismatch     = "api_version_mismatch"
	CodeFeatureMissing  = "feature_not_supported"
	CodeStaleWrite      = "stale_resource_version"
	CodeIdempotencyBusy = "idempotency_key_in_flight"
	CodeIdempotencyReus = "idempotency_key_reused"
)

// CheckClient decides whether a CLI at clientVersion may talk to this server.
// A development build ("dev", "", or a non-semver string) is allowed through:
// blocking a developer's own build would make the check hostile to the people
// most likely to hit it.
func (c Capabilities) CheckClient(clientVersion string) error {
	client, ok := parseSemver(clientVersion)
	if !ok {
		return nil
	}
	if minimum, minOK := parseSemver(c.MinCLIVersion); minOK && compare(client, minimum) < 0 {
		return &IncompatibleError{
			Code:          CodeClientTooOld,
			Message:       fmt.Sprintf("this server requires CLI %s or newer; you are running %s", c.MinCLIVersion, clientVersion),
			Remedy:        "run 'sy upgrade', or install a newer release from https://soulacy.dev",
			ClientVersion: clientVersion, ServerVersion: c.ServerVersion,
		}
	}
	if maximum, maxOK := parseSemver(c.MaxCLIVersion); maxOK && compare(client, maximum) > 0 {
		return &IncompatibleError{
			Code:          CodeClientTooNew,
			Message:       fmt.Sprintf("this server supports CLI up to %s; you are running %s", c.MaxCLIVersion, clientVersion),
			Remedy:        "upgrade the Soulacy gateway, or pin the CLI to a compatible release",
			ClientVersion: clientVersion, ServerVersion: c.ServerVersion,
		}
	}
	return nil
}

// RequireFeature is the handshake a client performs before an operation the
// server may not implement. It names the feature so the message is actionable
// even when the operator has never heard of it.
func (c Capabilities) RequireFeature(feature, operation string) error {
	if c.Has(feature) {
		return nil
	}
	return &IncompatibleError{
		Code:          CodeFeatureMissing,
		Message:       fmt.Sprintf("%s requires the %q capability, which this server does not advertise", operation, feature),
		Remedy:        "upgrade the Soulacy gateway to a release that supports " + feature,
		ServerVersion: c.ServerVersion, Feature: feature,
	}
}

// CheckAPIVersion rejects a wire contract this client cannot speak at all.
func (c Capabilities) CheckAPIVersion(clientAPIVersion string) error {
	client := strings.TrimSpace(clientAPIVersion)
	if client == "" || client == c.APIVersion {
		return nil
	}
	return &IncompatibleError{
		Code:          CodeAPIMismatch,
		Message:       fmt.Sprintf("this client speaks API %s; the server speaks %s", client, c.APIVersion),
		Remedy:        "upgrade whichever side is older; the API version changes only on a breaking contract change",
		ServerVersion: c.ServerVersion,
	}
}

type semver struct{ major, minor, patch int }

// parseSemver accepts an optional leading "v" and ignores any pre-release or
// build suffix, so "v0.2.0-rc1+deadbeef" compares as 0.2.0.
func parseSemver(value string) (semver, bool) {
	value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "v"))
	if value == "" {
		return semver{}, false
	}
	if cut := strings.IndexAny(value, "-+"); cut >= 0 {
		value = value[:cut]
	}
	parts := strings.Split(value, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return semver{}, false
	}
	numbers := make([]int, 3)
	for i, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return semver{}, false
		}
		numbers[i] = number
	}
	return semver{numbers[0], numbers[1], numbers[2]}, true
}

func compare(a, b semver) int {
	switch {
	case a.major != b.major:
		return sign(a.major - b.major)
	case a.minor != b.minor:
		return sign(a.minor - b.minor)
	default:
		return sign(a.patch - b.patch)
	}
}

func sign(delta int) int {
	if delta > 0 {
		return 1
	}
	if delta < 0 {
		return -1
	}
	return 0
}
