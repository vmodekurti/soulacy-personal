package app

import (
	"fmt"
	"strings"

	"github.com/soulacy/soulacy/internal/config"
)

// isolation_policy.go — MU-021 criterion 1: "Team mode defaults to container or
// equivalent isolation."
//
// The default was already docker. What "default" is worth depends entirely on
// what happens when an operator overrides it, and the override was a warning
// line. Left there, one config key —
//
//	runtime: { sandbox: { mode: unsandboxed } }
//
// — would run every tenant's shell_exec as the gateway user, on one shared
// filesystem, with the gateway's ambient credentials. That is not a weaker
// version of multi-tenant isolation; it is its exact negation, reachable
// without touching any code.
//
// Extracted as a pure decision so the policy is testable without booting a
// gateway. The wiring reads config, creates directories and opens Docker; the
// question "may this deployment run tools on the host at all" is none of that.

// Isolation decisions.
const (
	// IsolationContainer: run privileged tools in a disposable, unnetworked
	// container. The default, and the only option in Team/Scale.
	IsolationContainer = "container"
	// IsolationHost: run them as the gateway user with no boundary. Personal
	// mode only, and only when the operator asked for it explicitly.
	IsolationHost = "host"
	// IsolationRefused: privileged tools are unavailable. The configuration
	// and the deployment mode disagree, and guessing which one the operator
	// meant is worse than refusing.
	IsolationRefused = "refused"
)

// privilegedIsolationFor decides how privileged builtins may execute.
//
// The refusal is deliberately not a silent upgrade to container isolation. An
// operator who wrote mode=unsandboxed and got sandboxing anyway would debug the
// wrong thing for as long as it took them to read this file; one who gets
// "privileged tools disabled, here is why, here is the fix" reads the log line
// once. Fail closed AND fail loudly — quiet correctness is its own bug report.
func privilegedIsolationFor(sandboxEnabled bool, sandboxMode, deploymentMode string) (decision, reason string) {
	unsandboxed := !sandboxEnabled || strings.EqualFold(strings.TrimSpace(sandboxMode), "unsandboxed")
	if !unsandboxed {
		return IsolationContainer, ""
	}
	if config.IsMultiUserMode(deploymentMode) {
		return IsolationRefused, fmt.Sprintf(
			"runtime.sandbox.mode=unsandboxed would run every tenant's tools as the gateway user; %s mode requires container isolation",
			deploymentMode)
	}
	return IsolationHost, "commands run as the gateway user without isolation"
}
