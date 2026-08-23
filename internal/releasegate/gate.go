// Package releasegate is the machine-checked inventory behind MU-037: the
// surfaces a multi-tenant release must be adversarially tested on, and which
// test covers each.
//
// THE FINDING THAT PRODUCED THIS PACKAGE. There was already a great deal of
// cross-tenant testing — thirty-odd packages with tests that stand up two
// workspaces and try to reach across. And `make security`, the target a
// release actually runs, executed exactly three things: one filtered run of
// internal/runtime, plus internal/ownership and internal/runs.
//
// So the coverage was broad and the GATE was narrow, which is the worst
// arrangement of the two: every one of those suites could rot, or be deleted,
// or start passing for the wrong reason, and the release gate would stay green.
// Coverage nobody runs at the moment of release is documentation.
//
// MU-037 criterion 1 enumerates the surfaces — "every route, repository, event
// type, queue, cache, vector query, filesystem operation, artifact link, and
// export" — and that list is the thing to be machine-checked. A release gate
// whose contents are decided by whoever last edited a Makefile is not an
// objective gate, which is the criterion's own word for what it is asking for.
//
// TWO FAILURE DIRECTIONS, and both matter:
//
//   - A declared surface whose test file does not exist. That is a claim with
//     nothing behind it, and it is how an inventory becomes fiction.
//   - A package the gate does not RUN. `Packages` is compared against the
//     Makefile's security target, so extending the inventory without
//     extending the gate fails the build.
//
// This package deliberately holds no test logic. The adversarial tests live
// with the code they attack, where the author of a change will see them.
package releasegate

import (
	"fmt"
	"sort"
	"strings"
)

// Kind distinguishes the shapes an adversarial test takes here, because the
// shapes are checkable and the check differs per shape.
//
//   - TwoTenant stands up two workspaces and tries to reach across.
//   - BuildGuard reads the SOURCE and fails when a new surface appears
//     unclassified — the ownership catalog's discovery scan, the metric-label
//     guards, the event-consumer classification. It has no second workspace to
//     mention, and requiring one would mean writing a fake tenant into a test
//     that parses Go files.
//   - FaultInjection abandons the production sequence part-way and asserts what
//     recovery does. Its adversary is a crash rather than a neighbour.
//   - Attestation validates external human evidence that code cannot produce.
//
// FaultInjection was added because the chaos suite was neither of the first
// two and the guard said so. Mislabelling it TwoTenant to get a green build
// would have been the moment this taxonomy stopped meaning anything — a kind
// nobody can be wrong about is a kind that checks nothing.
type Kind string

const (
	KindTwoTenant      Kind = "two-tenant"
	KindBuildGuard     Kind = "build-guard"
	KindFaultInjection Kind = "fault-injection"
	KindAttestation    Kind = "attestation"
)

// Surface is one attack surface MU-037 criterion 1 names, and the coverage
// claimed for it.
type Surface struct {
	// ID is the surface's stable name, used in failure messages.
	ID string
	// Kind says which shape the named tests take, so the right check applies.
	Kind Kind
	// Criterion is the phrase from MU-037 this surface answers to, quoted so a
	// reviewer can check the mapping rather than trusting it.
	Criterion string
	// Package is the Go package whose tests cover this surface. Compared
	// against the release gate's actual command list.
	Package string
	// Tests are the files that do the covering. Existence is checked; a file
	// that is not there is a claim with nothing behind it.
	Tests []string
	// Why records what the adversary in those tests is trying to do. Not
	// decoration: "internal/x is tested" is unfalsifiable, and "an agent in
	// workspace A names workspace B's file by absolute path" is not.
	Why string
	// Gap, when non-empty, states what this surface does NOT yet cover. A
	// surface with a Gap is reported by Blockers and does not block the build,
	// because a gate that cannot admit an incomplete surface gets its
	// incomplete surfaces deleted instead of recorded.
	Gap string
}

// Surfaces is the inventory. Ordered by surface ID for readability; the gate
// does not depend on the order.
var Surfaces = []Surface{
	{
		ID:        "routes",
		Kind:      KindTwoTenant,
		Criterion: "every route ... for cross-tenant access",
		Package:   "internal/gateway",
		Tests: []string{
			"internal/gateway/route_authorization_test.go",
			"internal/gateway/scoped_reads_test.go",
			"internal/gateway/workspace_context_test.go",
			"internal/gateway/agent_scope_test.go",
		},
		Why: "every mutating route must authorize itself; every shared-store read must name a tenant; " +
			"no handler may trust a workspace from a body, query or header",
	},
	{
		ID:        "repositories",
		Kind:      KindBuildGuard,
		Criterion: "every ... repository ... for cross-tenant access",
		Package:   "internal/ownership",
		Tests: []string{
			"internal/ownership/catalog_test.go",
			"internal/ownership/exportplan_test.go",
		},
		Why: "the catalog is discovered from source, so a new durable table or repository fails the " +
			"build until it is classified and names a real isolation test",
	},
	{
		ID:        "events",
		Kind:      KindTwoTenant,
		Criterion: "every ... event type ... for cross-tenant access",
		Package:   "internal/gateway",
		Tests: []string{
			"internal/gateway/event_leakage_test.go",
			"internal/gateway/eventcursor_test.go",
			"internal/gateway/eventprojection_redaction_test.go",
		},
		Why: "a subscriber receives only its own workspace's events, a reconnect is re-authorized " +
			"rather than replayed, and the wire projection carries no credential values",
	},
	{
		ID:        "queue",
		Kind:      KindTwoTenant,
		Criterion: "every ... queue ... for cross-tenant access",
		Package:   "internal/queue/dlq",
		Tests:     []string{"internal/queue/dlq/isolation_test.go"},
		Why:       "one tenant's parked job cannot be read, overwritten or discarded by another",
	},
	{
		ID:        "filesystem",
		Kind:      KindTwoTenant,
		Criterion: "every ... filesystem operation ... for cross-tenant access",
		Package:   "internal/runtime",
		Tests: []string{
			"internal/runtime/security_isolation_test.go",
			"internal/runtime/workspace_roots_isolation_test.go",
		},
		Why: "absolute paths, traversal, planted symlinks, a widened container mount, a privileged " +
			"working directory, a tool subprocess's working directory, and a finished run's scratch",
	},
	{
		ID:        "approvals",
		Kind:      KindTwoTenant,
		Criterion: "membership revocation and role change during ... approvals",
		Package:   "internal/runtime",
		Tests:     []string{"internal/runtime/approval_isolation_test.go"},
		Why:       "a decider in the wrong workspace gets ErrNotFound, and eligibility is resolved from current state",
	},
	{
		ID:        "checkpoints",
		Kind:      KindTwoTenant,
		Criterion: "every ... repository ... for cross-tenant access",
		Package:   "internal/runtime",
		Tests:     []string{"internal/runtime/checkpoint_isolation_test.go"},
		Why:       "a run cannot resume from another workspace's checkpoint",
	},
	{
		ID:        "extensions",
		Kind:      KindTwoTenant,
		Criterion: "every ... repository ... for cross-tenant access",
		Package:   "internal/plugins",
		Tests: []string{
			"internal/plugins/isolation_test.go",
			"internal/plugins/delegation_workspace_test.go",
			"internal/plugins/invalidate_test.go",
		},
		Why: "a plugin installed in one workspace contributes tools only there, receives only its own " +
			"workspace's credentials, and stops being callable when revoked",
	},
	{
		ID:        "skills",
		Kind:      KindTwoTenant,
		Criterion: "every ... repository ... for cross-tenant access",
		Package:   "internal/skills",
		Tests:     []string{"internal/skills/isolation_test.go"},
		Why:       "a workspace's own skill shadows a platform one without modifying it, and is invisible to other tenants",
	},
	{
		ID:        "credentials",
		Kind:      KindTwoTenant,
		Criterion: "every ... repository ... for cross-tenant access",
		Package:   "internal/credentials",
		Tests:     []string{"internal/credentials/envelope_test.go"},
		Why:       "each workspace's data key is distinct and each ciphertext is bound by AAD to its own row",
	},
	{
		ID:        "identity",
		Kind:      KindTwoTenant,
		Criterion: "membership revocation and role change during active runs",
		Package:   "internal/auth/apikeys",
		Tests: []string{
			"internal/auth/apikeys/isolation_test.go",
			"internal/auth/apikeys/api_test.go",
		},
		Why: "a credential authenticates in exactly one workspace, and revocation takes effect on the next request",
	},
	{
		ID:        "fair-share",
		Kind:      KindTwoTenant,
		Criterion: "Load tests demonstrate fair scheduling under a noisy tenant",
		Package:   "internal/quota",
		Tests: []string{
			"internal/quota/fairshare_test.go",
			"internal/quota/fairshare_load_test.go",
			"internal/quota/limits_test.go",
		},
		Why: "one tenant saturating the deployment cannot starve another's share; the release workflow " +
			"also runs the open-loop real-gateway profile and publishes its p50/p95/p99/error evidence",
	},
	{
		ID:        "export",
		Kind:      KindTwoTenant,
		Criterion: "every ... export for cross-tenant access",
		Package:   "internal/workspaceexport",
		Tests:     []string{"internal/workspaceexport/export_test.go"},
		Why:       "an export contains one workspace's data and its manifest names every resource it did NOT include",
	},
	{
		ID:        "deletion",
		Kind:      KindTwoTenant,
		Criterion: "every ... export for cross-tenant access",
		Package:   "internal/workspacepurge",
		Tests:     []string{"internal/workspacepurge/purge_test.go"},
		Why:       "purging one workspace leaves every other intact, and the coverage list names what is not yet purged",
	},
	{
		ID:        "durable-runs",
		Kind:      KindTwoTenant,
		Criterion: "membership revocation and role change during active runs",
		Package:   "internal/runs",
		Tests: []string{
			"internal/runs/store_test.go",
			"internal/runs/recovery_test.go",
			"internal/runs/lease_test.go",
		},
		Why: "run IDs collide across workspaces without colliding in storage, and recovery cannot " +
			"re-queue a run a live worker holds",
	},
	{
		ID:        "schedules",
		Kind:      KindTwoTenant,
		Criterion: "every ... repository ... for cross-tenant access",
		Package:   "internal/schedules",
		Tests:     []string{"internal/schedules/store_test.go"},
		Why:       "two workspaces' same-named schedules are two schedules, and an occurrence claim is per workspace",
	},
	{
		ID:        "memory",
		Kind:      KindTwoTenant,
		Criterion: "every ... repository ... for cross-tenant access",
		Package:   "internal/memory",
		Tests:     []string{"internal/memory/workspace_test.go"},
		Why:       "conversation and archive reads resolve within the caller's workspace",
	},
	{
		ID:        "knowledge",
		Kind:      KindTwoTenant,
		Criterion: "every ... vector query ... for cross-tenant access",
		Package:   "internal/knowledge",
		Tests:     []string{"internal/knowledge/workspace_test.go"},
		Why:       "a knowledge base and its chunks are reachable only from the workspace that owns them",
	},
	{
		ID:        "vectors",
		Kind:      KindTwoTenant,
		Criterion: "every ... vector query ... for cross-tenant access",
		Package:   "internal/vector/qdrant",
		Tests: []string{
			"internal/vector/qdrant/workspace_test.go",
			"internal/vector/qdrant/workspace_live_test.go",
		},
		Why: "request-shape tests prove the pre-filter is sent, and the mandatory live release test " +
			"proves Qdrant executes it without returning a neighbouring tenant's identical vector",
	},
	{
		ID:        "rate-limits",
		Kind:      KindTwoTenant,
		Criterion: "Load tests demonstrate fair scheduling under a noisy tenant",
		Package:   "internal/ratelimit",
		Tests:     []string{"internal/ratelimit/tenancy_test.go"},
		Why:       "a limiter bucket is keyed per workspace, so one tenant's burst does not throttle another",
	},
	{
		ID:        "metrics",
		Kind:      KindBuildGuard,
		Criterion: "every route ... for cross-tenant access",
		Package:   "internal/metrics",
		Tests: []string{
			"internal/metrics/labels_guard_test.go",
			"internal/metrics/label_callsite_guard_test.go",
		},
		Why: "the Prometheus exposition is one global document, so no label may carry a tenant-authored value",
	},
	{
		ID:        "redaction",
		Kind:      KindBuildGuard,
		Criterion: "Prompt, response, tool arguments, secret values ... absent from default logs",
		Package:   "internal/redact",
		Tests: []string{
			"internal/redact/keyname_test.go",
			"internal/redact/eventsink_test.go",
		},
		Why: "one predicate decides what looks secret, and every consumer of an event is classified as " +
			"redacting or as not carrying the payload onward",
	},
	{
		ID:        "channels",
		Kind:      KindTwoTenant,
		Criterion: "every route ... for cross-tenant access",
		Package:   "internal/channels",
		Tests:     []string{"internal/channels/ownership_test.go"},
		Why:       "an inbound message is stamped with the CONNECTION's workspace, not one the sender named",
	},
	{
		ID:   "chaos",
		Kind: KindFaultInjection,
		Criterion: "Chaos tests cover crashes before/after claim, provider call, tool side effect, " +
			"event publish, and result commit",
		Package: "internal/app",
		Tests:   []string{"internal/app/chaos_test.go", "internal/app/runlease_test.go"},
		Why: "a crashed worker is a worker that stops renewing, so each crash point is the production " +
			"sequence run up to that point and then abandoned; recovery must re-queue what never acted, " +
			"fail what did with the tool named, bound the retries, and leave a live worker's run alone",
	},
	{
		ID:   "config-hot-reload",
		Kind: KindBuildGuard,
		Criterion: "API, CLI, GUI, configuration, migration, and operational documentation are " +
			"updated where applicable",
		Package: "internal/confighot",
		Tests:   []string{"internal/confighot/catalog_test.go"},
		Why: "every section of config.Config is classified tenant or platform by reflection over the " +
			"real struct, and a tenant-scoped section that requires a restart fails the build — one " +
			"workspace's routine edit must not be a service interruption for every other workspace",
	},
	{
		ID:        "backup-integrity",
		Kind:      KindFaultInjection,
		Criterion: "Backup restoration and worker/gateway chaos tests pass",
		Package:   "internal/recovery",
		Tests:     []string{"internal/recovery/recovery_test.go"},
		Why: "the mandatory release drill destroys and restores PostgreSQL relational rows, object bytes, " +
			"vector references, encrypted secrets and schema versions, then checks every recovered value",
	},
	{
		ID:        "migration-backup",
		Kind:      KindFaultInjection,
		Criterion: "Backup restoration and worker/gateway chaos tests pass",
		Package:   "internal/sqlitex",
		Tests:     []string{"internal/sqlitex/backupgate_test.go"},
		Why:       "a destructive schema migration refuses to run without a snapshot that opens and contains what it will drop",
	},
	{
		ID:        "independent-security-review",
		Kind:      KindAttestation,
		Criterion: "Threat modeling and an independent security review have no unresolved critical or high findings",
		Package:   "internal/releasegate",
		Tests:     []string{"internal/releasegate/independent_review_test.go"},
		Why: "the release workflow requires a dated HTTPS review attestation and refuses publication when " +
			"the reviewer reports any unresolved critical or high finding",
	},
}

// Packages returns the deduplicated, sorted package list the gate must run.
//
// This is what the Makefile is checked against. Returning it from code rather
// than maintaining a list in the Makefile is the point: the inventory is the
// source of truth, and a surface added here without the gate being extended
// fails the build instead of being quietly untested.
func Packages() []string {
	seen := map[string]bool{}
	for _, surface := range Surfaces {
		seen[surface.Package] = true
	}
	out := make([]string, 0, len(seen))
	for pkg := range seen {
		out = append(out, pkg)
	}
	sort.Strings(out)
	return out
}

// Blockers returns the surfaces with a recorded gap, formatted for a report.
//
// Mirrors ownership.MultiUserBlockers deliberately, including the part that
// makes it useful: a gap is DECLARED rather than discovered, so the honest
// answer to "is this release ready" is a list somebody wrote down, and the
// build does not punish them for writing it. A gate that failed on any gap
// would be a gate people route around.
func Blockers() []string {
	var out []string
	for _, surface := range Surfaces {
		if strings.TrimSpace(surface.Gap) == "" {
			continue
		}
		out = append(out, fmt.Sprintf("%s: %s", surface.ID, surface.Gap))
	}
	sort.Strings(out)
	return out
}

// Validate checks the inventory's internal consistency.
//
// Existence of the named test files is checked by the package's test, which can
// read the filesystem relative to the repository root. This function checks
// only what is knowable from the declaration itself, so it is callable from a
// running binary — `sy doctor` can report the gate without a source tree.
func Validate() error {
	seen := map[string]bool{}
	for _, surface := range Surfaces {
		switch {
		case strings.TrimSpace(surface.ID) == "":
			return fmt.Errorf("releasegate: a surface has no ID")
		case seen[surface.ID]:
			return fmt.Errorf("releasegate: duplicate surface %q", surface.ID)
		case strings.TrimSpace(surface.Criterion) == "":
			return fmt.Errorf("releasegate: surface %q quotes no criterion", surface.ID)
		case strings.TrimSpace(surface.Package) == "":
			return fmt.Errorf("releasegate: surface %q names no package", surface.ID)
		case len(surface.Tests) == 0:
			return fmt.Errorf("releasegate: surface %q names no tests", surface.ID)
		case surface.Kind != KindTwoTenant && surface.Kind != KindBuildGuard && surface.Kind != KindFaultInjection && surface.Kind != KindAttestation:
			return fmt.Errorf("releasegate: surface %q has unsupported kind %q", surface.ID, surface.Kind)
		case strings.TrimSpace(surface.Why) == "":
			// Enforced because "internal/x is tested" is unfalsifiable. The
			// sentence is what lets a reviewer decide whether the named tests
			// actually cover the criterion, which is the only check a machine
			// cannot make here.
			return fmt.Errorf("releasegate: surface %q does not say what its adversary attempts", surface.ID)
		}
		seen[surface.ID] = true
	}
	return nil
}
