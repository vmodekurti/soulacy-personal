// Package confighot classifies every configuration section by whether a change
// to it reaches the running system, and enforces one rule:
//
//	NOTHING TENANT-SCOPED MAY REQUIRE A RESTART.
//
// WHY THIS IS A CATALOG AND NOT A SET OF FIXES. The state that produced it was
// not "a few settings need hot-reload". It was that nobody could say which
// settings were live and which were not, so the answer was discovered one
// support conversation at a time — and the user-facing text drifted in both
// directions. A dozen places told operators to restart after saving a provider
// key, which has been hot for some time. The channel handlers said the same
// thing and were telling the truth. `ReloadConfig` re-applied exactly one
// subsystem out of thirty. There is no way to be right about that by memory,
// and fixing ten settings does not stop the eleventh.
//
// WHAT SINGLE-TENANT MADE ACCEPTABLE. In a Personal deployment the operator,
// the tenant and the person restarting the process are one person. "Save it and
// restart" costs them ten seconds of their own time, so it was a reasonable
// default and it spread. In a Team deployment the three are different people:
// one workspace's admin saving a Slack token would be asking every other
// workspace to accept a service interruption. That is not a worse version of
// the same trade — it is a different trade, and the answer flips.
//
// THE CLASSIFICATION IS THE DELIVERABLE. Each section says which scope it
// belongs to and, when it is boot-only, WHY — in a sentence that has to survive
// somebody reading it and disagreeing. "It is hard" is not a reason.
// "Rebinding a listening socket cannot be done without dropping connections" is.
package confighot

import (
	"fmt"
	"sort"
	"strings"
)

// Scope says whose setting this is, which is what decides whether a restart is
// acceptable.
type Scope string

const (
	// ScopeTenant is a setting one workspace changes for itself. A restart to
	// apply it would make one tenant's routine edit an outage for every other
	// tenant, so these MUST be hot. The guard enforces it.
	ScopeTenant Scope = "tenant"

	// ScopePlatform is a setting the operator changes for the whole
	// deployment. A restart is acceptable here because the person changing it
	// is the person who owns the downtime — but it should still be rare, so
	// each boot-only entry carries a reason.
	ScopePlatform Scope = "platform"
)

// Apply says what actually happens when the setting changes.
type Apply string

const (
	// ApplyLive means a change reaches the running system with no restart.
	ApplyLive Apply = "live"

	// ApplyBoot means the running system keeps the old value until the process
	// restarts. Legal only for ScopePlatform, and only with a Reason.
	ApplyBoot Apply = "boot"
)

// Section is one top-level configuration area.
//
// Classified by SECTION rather than by leaf field, deliberately. A per-field
// catalog would be more precise and would be wrong within a month: config
// structs grow leaves constantly, and a catalog that has to be edited for every
// new bool is a catalog people bypass. A section is the unit somebody actually
// wires, and the unit whose wiring either exists or does not.
type Section struct {
	// Field is the Go field name on config.Config. Checked against the struct
	// by reflection, so a renamed or added section fails the build.
	Field string
	// Key is the YAML key, for messages an operator will read.
	Key string
	// Scope is whose setting this is.
	Scope Scope
	// Apply is what happens today.
	Apply Apply
	// Reason is required when Apply is ApplyBoot: why a restart is genuinely
	// necessary rather than merely unimplemented.
	Reason string
	// AppliedBy names EVERY function that re-applies a live section, so the
	// claim can be checked rather than trusted.
	//
	// A list rather than one name because a section can need more than one
	// applier, and naming only the first is how a section comes to be half
	// live: `costs` named the quota recomposition and stopped there, so the
	// provider allow-lists and per-provider policies in the same section went
	// on being boot snapshots while the catalog called the section live.
	AppliedBy []string
}

// Sections is the classification.
var Sections = []Section{
	// ── Tenant-scoped. All of these must be live. ────────────────────────────
	{
		Field: "Channels", Key: "channels", Scope: ScopeTenant, Apply: ApplyLive,
		AppliedBy: []string{"gateway.Server.applyChannelsLive"},
	},
	{
		Field: "MCP", Key: "mcp", Scope: ScopeTenant, Apply: ApplyLive,
		AppliedBy: []string{"gateway.Server.mcpReplaceTemplate"},
	},
	{
		Field: "PluginsConfig", Key: "plugins_config", Scope: ScopeTenant, Apply: ApplyLive,
		AppliedBy: []string{"gateway.Server.applyPluginsConfigLive"},
	},
	{
		Field: "Registries", Key: "registries", Scope: ScopeTenant, Apply: ApplyLive,
		AppliedBy: []string{"re-read from the config file on every search and install"},
	},
	{
		Field: "Security", Key: "security", Scope: ScopeTenant, Apply: ApplyLive,
		AppliedBy: []string{"runtime.Engine.SetIntentGateDefault"},
	},
	{
		Field: "Costs", Key: "costs", Scope: ScopeTenant, Apply: ApplyLive,
		AppliedBy: []string{"gateway.Server.reloadQuotaPolicy", "gateway.Server.applyGovernanceLive"},
	},
	{
		Field: "RateLimit", Key: "rate_limit", Scope: ScopeTenant, Apply: ApplyLive,
		AppliedBy: []string{"ratelimit.Manager.SetConfig"},
	},
	{
		Field: "Search", Key: "search", Scope: ScopeTenant, Apply: ApplyLive,
		AppliedBy: []string{"runtime.Engine.SetSearchConfig"},
	},
	{
		Field: "LLM", Key: "llm", Scope: ScopeTenant, Apply: ApplyLive,
		AppliedBy: []string{"gateway.Server.applyLLMLive", "gateway.Server.applyGovernanceLive"},
	},

	// ── Platform-scoped and live anyway. ─────────────────────────────────────
	// Being platform-scoped permits a restart; it does not require one, and
	// where the wiring already exists there is no reason to spend it.
	{
		Field: "UI", Key: "ui", Scope: ScopePlatform, Apply: ApplyLive,
		AppliedBy: []string{"read from the config snapshot per request"},
	},
	{
		Field: "Ops", Key: "ops", Scope: ScopePlatform, Apply: ApplyLive,
		AppliedBy: []string{"read from the config snapshot per request"},
	},
	{
		Field: "Updates", Key: "updates", Scope: ScopePlatform, Apply: ApplyLive,
		AppliedBy: []string{"read from the config snapshot per check"},
	},
	{
		Field: "Packages", Key: "packages", Scope: ScopePlatform, Apply: ApplyLive,
		AppliedBy: []string{"read from the config snapshot per install"},
	},
	{
		Field: "SchemaVersion", Key: "schema_version", Scope: ScopePlatform, Apply: ApplyLive,
		AppliedBy: []string{"stamped by config.Load; read at boot for a diagnostic only"},
	},

	// ── Platform-scoped and genuinely boot-only. ─────────────────────────────
	// Each reason has to answer "why can this not be done live", not "why has
	// nobody done it".
	{
		Field: "Server", Key: "server", Scope: ScopePlatform, Apply: ApplyBoot,
		Reason: "host, port and TLS are properties of a bound listening socket; changing them means " +
			"closing it, and every connection on it, which is a restart by another name",
	},
	{
		Field: "Auth", Key: "auth", Scope: ScopePlatform, Apply: ApplyBoot,
		Reason: "swapping the signing key or the auth mode under live sessions would invalidate every " +
			"token in flight at a moment nobody chose; a restart makes that a deliberate, announced event",
	},
	{
		Field: "Deployment", Key: "deployment", Scope: ScopePlatform, Apply: ApplyBoot,
		Reason: "the mode decides whether tenancy, durable schedules and approval enforcement exist at " +
			"all; changing it live would leave half the process in one mode and half in the other",
	},
	{
		Field: "Storage", Key: "storage", Scope: ScopePlatform, Apply: ApplyBoot,
		Reason: "an open connection pool with in-flight transactions cannot be redirected to a different " +
			"database without deciding what happens to those transactions",
	},
	{
		Field: "Queue", Key: "queue", Scope: ScopePlatform, Apply: ApplyBoot,
		Reason: "subscriptions are held open per backend; switching brokers live would drop messages " +
			"the old one had delivered and was still awaiting acknowledgement for",
	},
	{
		Field: "Vector", Key: "vector", Scope: ScopePlatform, Apply: ApplyBoot,
		Reason: "embeddings written under one backend are not readable by another, so a live switch " +
			"would silently make existing memory unsearchable rather than fail",
	},
	{
		Field: "Memory", Key: "memory", Scope: ScopePlatform, Apply: ApplyBoot,
		Reason: "the file store and archive hold open handles to a directory chosen at boot",
	},
	{
		Field: "Knowledge", Key: "knowledge", Scope: ScopePlatform, Apply: ApplyBoot,
		Reason: "the ingest worker and its embedding client are constructed once against a database " +
			"whose path is part of its identity",
	},
	{
		Field: "Executor", Key: "executor", Scope: ScopePlatform, Apply: ApplyBoot,
		Reason: "a warm process pool is the executor; changing the backend means discarding it, and " +
			"discarding it mid-run is the failure the pool exists to avoid",
	},
	{
		Field: "Runtime", Key: "runtime", Scope: ScopePlatform, Apply: ApplyBoot,
		Reason: "worker count, sandbox limits and filesystem roots are read once into the engine and " +
			"into every running goroutine's closure; a live change would apply to new runs only, which " +
			"is worse than not applying at all because nobody could tell which runs got which limits",
	},
	{
		Field: "Credentials", Key: "credentials", Scope: ScopePlatform, Apply: ApplyBoot,
		Reason: "the vault's key material is established at open; rekeying live is what the explicit " +
			"rotation endpoint is for, and doing it implicitly on a config write would be a data-loss " +
			"risk taken by accident",
	},
	{
		Field: "Billing", Key: "billing", Scope: ScopePlatform, Apply: ApplyBoot,
		Reason: "the billing provider, webhook verifier, and entitlement store are wired into the " +
			"HTTP authorization chain at process start; changing them live could admit an event under " +
			"one trust root and enforce it under another",
	},
	{
		Field: "Telemetry", Key: "telemetry", Scope: ScopePlatform, Apply: ApplyBoot,
		Reason: "the trace provider is installed globally at process start and spans in flight hold " +
			"references to it",
	},
	{
		Field: "Hooks", Key: "hooks", Scope: ScopePlatform, Apply: ApplyBoot,
		Reason: "the dispatcher holds queue subscriptions per hook; re-subscribing live risks delivering " +
			"an event twice or not at all across the swap",
	},
	{
		Field: "Voice", Key: "voice", Scope: ScopePlatform, Apply: ApplyBoot,
		Reason: "the speech sidecar is a subprocess with an active session protocol; swapping providers " +
			"under a live call is not a config change, it is a disconnection",
	},
	{
		Field: "Log", Key: "log", Scope: ScopePlatform, Apply: ApplyBoot,
		Reason: "the logger is built before the application exists, and every subsystem holds a child " +
			"of it",
	},
	{
		Field: "AgentDirs", Key: "agent_dirs", Scope: ScopePlatform, Apply: ApplyBoot,
		Reason: "the filesystem watcher recurses from the roots it was given, so a new root needs a new " +
			"watcher; agent FILES under an existing root are already hot",
	},
	{
		Field: "SkillDirs", Key: "skill_dirs", Scope: ScopePlatform, Apply: ApplyBoot,
		Reason: "the platform scan list is composed once and frozen into every workspace's layered " +
			"store; skill FILES under an existing dir are already hot",
	},
	{
		Field: "PluginDirs", Key: "plugin_dirs", Scope: ScopePlatform, Apply: ApplyBoot,
		Reason: "same as skill_dirs: the scan roots are frozen into the per-workspace stores, while " +
			"plugins INSIDE those roots are hot",
	},
}

// Validate checks the classification's internal consistency.
//
// The rule that matters is the last case: a tenant-scoped section may not be
// boot-only. Everything else is bookkeeping that keeps this file honest enough
// for that rule to mean something.
func Validate() error {
	seen := map[string]bool{}
	for _, section := range Sections {
		switch {
		case strings.TrimSpace(section.Field) == "":
			return fmt.Errorf("confighot: a section has no Field")
		case seen[section.Field]:
			return fmt.Errorf("confighot: duplicate section %q", section.Field)
		case strings.TrimSpace(section.Key) == "":
			return fmt.Errorf("confighot: section %q has no YAML key", section.Field)
		case section.Scope != ScopeTenant && section.Scope != ScopePlatform:
			return fmt.Errorf("confighot: section %q has scope %q, want tenant or platform", section.Field, section.Scope)
		case section.Apply != ApplyLive && section.Apply != ApplyBoot:
			return fmt.Errorf("confighot: section %q has apply %q, want live or boot", section.Field, section.Apply)
		case section.Apply == ApplyBoot && strings.TrimSpace(section.Reason) == "":
			return fmt.Errorf("confighot: section %q is boot-only with no reason; \"nobody has wired it\" "+
				"is a TODO, not a reason", section.Field)
		case section.Apply == ApplyLive && len(section.AppliedBy) == 0:
			return fmt.Errorf("confighot: section %q claims to be live but names nothing that applies it", section.Field)
		case section.Scope == ScopeTenant && section.Apply == ApplyBoot:
			// THE RULE. A tenant changing their own setting must not require
			// every other tenant to accept a restart.
			return fmt.Errorf("confighot: section %q is tenant-scoped and boot-only. One workspace's "+
				"routine edit would be a service interruption for every other workspace. Either make it "+
				"live, or reclassify it as platform-scoped and say why it is the operator's setting "+
				"rather than the tenant's", section.Field)
		}
		seen[section.Field] = true
	}
	return nil
}

// BootOnly returns the platform sections that still require a restart, sorted,
// for an operator asking what a config edit will and will not do.
func BootOnly() []Section {
	var out []Section
	for _, section := range Sections {
		if section.Apply == ApplyBoot {
			out = append(out, section)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Key < out[b].Key })
	return out
}

// RequiresRestart reports whether changing a YAML section needs a restart.
//
// Used by the config API so the response tells the truth rather than repeating
// a constant. An unknown key returns false: the guard guarantees every section
// is classified, so an unknown key is a leaf inside a section rather than a
// section, and leaves follow their section.
func RequiresRestart(yamlKey string) bool {
	for _, section := range Sections {
		if section.Key == yamlKey {
			return section.Apply == ApplyBoot
		}
	}
	return false
}
