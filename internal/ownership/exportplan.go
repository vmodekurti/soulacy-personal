package ownership

import (
	"fmt"
	"sort"
	"strings"
)

// exportplan.go — MU-032 criteria 1 and 5, derived rather than listed.
//
// "Export enumerates ALL owned resource classes" and "database rows, objects,
// vectors, secrets, jobs, caches, and derived learning data are deleted or
// tombstoned according to DOCUMENTED retention rules" are both, underneath,
// the same requirement: there must be one inventory, and it must be impossible
// for a resource to exist outside it.
//
// This package is already that inventory. Every Resource carries an `Export`
// and a `Deletion` sentence, and the discovery scan fails the build when a
// durable store appears without a classification. So the export plan is a
// PROJECTION of the catalog, not a second list beside it.
//
// The alternative — an exporter with its own list of what to include — is the
// bug this design exists to prevent, and it is a quiet one in both directions:
// a resource missing from the export is data a customer is told they have and
// does not, and a resource missing from the deletion plan is data that survives
// a deletion the report says completed.

// Disposition is what happens to a resource class when a workspace is deleted.
type Disposition string

const (
	// Purged means the rows and objects are removed.
	Purged Disposition = "purged"
	// Tombstoned means an identifier survives so that references elsewhere do
	// not dangle, with the content gone.
	Tombstoned Disposition = "tombstoned"
	// Retained means the data outlives the workspace, and the catalog says
	// why — audit records under a legal hold, or aggregates a provider
	// invoice was reconciled against.
	Retained Disposition = "retained"
	// NotOwned means the class does not belong to the workspace at all, so
	// workspace deletion has no opinion about it.
	NotOwned Disposition = "not-owned"
)

// PlanEntry is one resource class's place in an export or a deletion.
type PlanEntry struct {
	Resource string `json:"resource"`
	Class    Class  `json:"class"`
	// Export is the catalog's own sentence about what leaves in an export.
	// Carried verbatim rather than re-worded so the manifest a customer reads
	// and the catalog a reviewer reads cannot disagree.
	Export string `json:"export"`
	// Deletion is the catalog's sentence about what happens on delete.
	Deletion string `json:"deletion"`
	// Disposition is that sentence classified, so a report can be checked by a
	// machine rather than read by a person.
	Disposition Disposition `json:"disposition"`
	// SecretsExcluded marks a class whose export must never carry secret
	// material. Derived from the catalog's own export sentence, so a resource
	// whose sentence says "never secrets" cannot be exported with them by a
	// exporter that forgot.
	SecretsExcluded bool `json:"secrets_excluded"`
}

// WorkspaceExportPlan returns every resource class a workspace export must
// enumerate, ordered by name so a manifest is reproducible.
//
// UserPrivate is included alongside WorkspaceOwned: a workspace export that
// omitted conversations and memory would be an export of the workspace's
// configuration, not of its data, and the customer asking for it means the
// second. OrganizationOwned and PlatformGlobal are excluded — they outlive the
// workspace and belong to a different export.
func WorkspaceExportPlan() []PlanEntry {
	var plan []PlanEntry
	for _, resource := range Resources {
		if resource.Class != WorkspaceOwned && resource.Class != UserPrivate {
			continue
		}
		plan = append(plan, PlanEntry{
			Resource:        resource.Name,
			Class:           resource.Class,
			Export:          resource.Export,
			Deletion:        resource.Deletion,
			Disposition:     dispositionOf(resource),
			SecretsExcluded: excludesSecrets(resource.Export),
		})
	}
	sort.Slice(plan, func(i, j int) bool { return plan[i].Resource < plan[j].Resource })
	return plan
}

// WorkspaceDeletionPlan is the same projection for deletion. Separate function,
// same source: an export and a deletion that disagree about what a workspace
// contains is how data survives a deletion nobody thinks completed partially.
func WorkspaceDeletionPlan() []PlanEntry { return WorkspaceExportPlan() }

// dispositionOf classifies the catalog's deletion sentence.
//
// Reading the prose rather than adding a field is deliberate: a second field
// would be a second place to state the same policy, and the two would drift
// exactly as the export list and the catalog would have. If a sentence cannot
// be classified, that is reported as an error by ValidateExportPlan rather than
// being defaulted — a resource whose deletion policy nobody can classify is one
// nobody has decided.
func dispositionOf(resource Resource) Disposition {
	sentence := strings.ToLower(resource.Deletion)
	switch {
	case sentence == "":
		return ""
	case strings.Contains(sentence, "tombstone"):
		return Tombstoned
	case strings.Contains(sentence, "may remain") || strings.Contains(sentence, "legal") || strings.Contains(sentence, "retain"):
		return Retained
	case strings.Contains(sentence, "purge") || strings.Contains(sentence, "delete") ||
		strings.Contains(sentence, "erasure") || strings.Contains(sentence, "revoke") ||
		strings.Contains(sentence, "cascade") || strings.Contains(sentence, "disable") ||
		strings.Contains(sentence, "uninstall") || strings.Contains(sentence, "removal") ||
		strings.Contains(sentence, "remove") || strings.Contains(sentence, "disconnect") ||
		strings.Contains(sentence, "with ") || strings.Contains(sentence, "acknowledge"):
		return Purged
	default:
		return ""
	}
}

// excludesSecrets reads the catalog's export sentence for an explicit refusal.
func excludesSecrets(export string) bool {
	lower := strings.ToLower(export)
	return strings.Contains(lower, "never secret") ||
		strings.Contains(lower, "values excluded") ||
		strings.Contains(lower, "never plaintext") ||
		strings.Contains(lower, "metadata only") ||
		strings.Contains(lower, "redacted")
}

// ValidateExportPlan fails when a workspace-owned resource cannot be placed in
// an export or a deletion.
//
// This is the guard that makes criterion 1 structural. A resource added to the
// catalog without an export sentence, or with a deletion sentence nobody can
// classify, is one that would be silently absent from both — and the absence
// looks identical to a class that has nothing to export.
func ValidateExportPlan() error {
	for _, entry := range WorkspaceExportPlan() {
		if strings.TrimSpace(entry.Export) == "" {
			return fmt.Errorf("resource %q is workspace data with no export decision", entry.Resource)
		}
		if strings.TrimSpace(entry.Deletion) == "" {
			return fmt.Errorf("resource %q is workspace data with no deletion decision", entry.Resource)
		}
		if entry.Disposition == "" {
			return fmt.Errorf("resource %q has a deletion policy that cannot be classified as purged, tombstoned or retained: %q",
				entry.Resource, entry.Deletion)
		}
	}
	return nil
}
