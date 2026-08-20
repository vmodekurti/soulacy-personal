// Package workspaceexport builds a portable copy of one workspace's data.
//
// MU-032 criterion 2: "a workspace owner can export the workspace's data in a
// documented, self-describing format."
//
// THE SHAPE OF THIS PACKAGE IS THE POINT. It imports the ownership catalog and
// the standard library, and NOTHING ELSE — not knowledge, not runs, not
// credentials. The bytes for each resource class arrive as an injected Source.
//
// That is not decoupling for its own sake. An exporter that imported every
// store would be a second inventory of what a workspace contains, and the
// failure mode of a second inventory is silent in the direction that matters:
// a resource class nobody added an import for is simply absent, and an absent
// class is indistinguishable from a class that had nothing to export. Here the
// PLAN comes from the catalog and every plan entry starts life as
// "not-exported" — so a class with no registered Source appears in the
// manifest, by name, saying it was not exported. The customer reading the
// manifest learns what they did not get. That is the loud failure.
package workspaceexport

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/ownership"
)

// SchemaVersion identifies the manifest format.
//
// Namespaced and versioned in one string so a reader that encounters an
// unfamiliar value can tell "this is a Soulacy export I am too old to read"
// apart from "this is not a Soulacy export", which a bare "1" cannot.
const SchemaVersion = "soulacy.workspace-export/v1"

// EntryStatus is what happened to one resource class in one export.
//
// Four values rather than a bool because the three non-success outcomes have
// genuinely different meanings to somebody holding the archive, and collapsing
// them loses exactly the information they need:
//
//   - empty: we looked, the workspace has none. Nothing is missing.
//   - not-exported: this build cannot export the class. Data exists that the
//     archive does not contain.
//   - failed: we tried and could not. Retrying may help.
type EntryStatus string

const (
	StatusExported    EntryStatus = "exported"
	StatusEmpty       EntryStatus = "empty"
	StatusNotExported EntryStatus = "not-exported"
	StatusFailed      EntryStatus = "failed"
)

// Complete reports whether an export contains everything the workspace has.
// "empty" counts as complete: there was nothing to carry.
func (s EntryStatus) Complete() bool {
	return s == StatusExported || s == StatusEmpty
}

// Entry is one resource class's line in the manifest.
type Entry struct {
	Resource string          `json:"resource"`
	Class    ownership.Class `json:"class"`
	// Path inside the archive. Empty unless Status is "exported".
	Path   string      `json:"path,omitempty"`
	Bytes  int64       `json:"bytes"`
	SHA256 string      `json:"sha256,omitempty"`
	Status EntryStatus `json:"status"`
	// Promise is the ownership catalog's own export sentence, carried verbatim.
	// Not re-worded: the sentence a reviewer reads in the catalog and the
	// sentence a customer reads in the manifest have to be the same sentence,
	// or one of them is documentation of something that is not happening.
	Promise string `json:"promise"`
	// SecretsExcluded repeats the catalog's explicit refusal to export secret
	// material for this class, so the archive itself states the guarantee
	// rather than the guarantee living only in a document nobody ships.
	SecretsExcluded bool `json:"secrets_excluded"`
	// Detail explains a non-exported or failed entry. Never carries a value
	// read out of the resource, only a reason.
	Detail string `json:"detail,omitempty"`
}

// Manifest is the archive's self-description.
type Manifest struct {
	SchemaVersion string    `json:"schema_version"`
	ExportID      string    `json:"export_id"`
	WorkspaceID   string    `json:"workspace_id"`
	WorkspaceName string    `json:"workspace_name,omitempty"`
	RequestedBy   string    `json:"requested_by"`
	CreatedAt     time.Time `json:"created_at"`
	Entries       []Entry   `json:"entries"`
	// Checksum covers the ENTRIES ONLY — deliberately not CreatedAt or
	// RequestedBy.
	//
	// The question a checksum has to answer here is "are these two exports the
	// same data", which is what somebody asks when reconciling a re-run against
	// an archive they already hold. Folding the timestamp in would make every
	// export of unchanged data a different checksum, which answers a question
	// nobody asked and makes the useful one unanswerable.
	Checksum string `json:"checksum"`
}

// newManifest seeds one entry per planned resource class, all "not-exported".
//
// Seeding rather than appending as we go is the guard: an export that ran
// halfway, or a build with no Source for a class, still produces a manifest
// naming every class the catalog says a workspace owns. There is no path that
// produces a short manifest, so a short manifest cannot be mistaken for a
// complete one.
func newManifest(exportID, workspaceID, workspaceName, requestedBy string, plan []ownership.PlanEntry, at time.Time) *Manifest {
	entries := make([]Entry, 0, len(plan))
	for _, p := range plan {
		entries = append(entries, Entry{
			Resource:        p.Resource,
			Class:           p.Class,
			Status:          StatusNotExported,
			Promise:         p.Export,
			SecretsExcluded: p.SecretsExcluded,
			Detail:          "this build has no exporter registered for this resource class",
		})
	}
	return &Manifest{
		SchemaVersion: SchemaVersion,
		ExportID:      exportID,
		WorkspaceID:   workspaceID,
		WorkspaceName: workspaceName,
		RequestedBy:   requestedBy,
		CreatedAt:     at.UTC(),
		Entries:       entries,
	}
}

// set records an outcome for one resource class, replacing the seeded entry.
// Unknown resources are ignored rather than appended: a Source naming a class
// the catalog does not list is a bug in the caller, and silently growing the
// manifest to accommodate it would hide the one case ValidateSources catches.
func (m *Manifest) set(resource string, mutate func(*Entry)) bool {
	for i := range m.Entries {
		if m.Entries[i].Resource == resource {
			mutate(&m.Entries[i])
			return true
		}
	}
	return false
}

// Incomplete lists the resource classes this archive does not fully contain.
func (m *Manifest) Incomplete() []string {
	var missing []string
	for _, e := range m.Entries {
		if !e.Status.Complete() {
			missing = append(missing, e.Resource)
		}
	}
	sort.Strings(missing)
	return missing
}

// seal computes the checksum. Called once, after every entry is final.
//
// The digest is over a canonical rendering rather than the JSON encoding,
// because encoding/json's output is stable today by accident of field order
// and not by promise — a checksum that changes when somebody reorders a struct
// field is a checksum that reports data corruption for a refactor.
func (m *Manifest) seal() {
	sorted := append([]Entry(nil), m.Entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Resource < sorted[j].Resource })
	sum := sha256.New()
	fmt.Fprintf(sum, "%s\n%s\n", SchemaVersion, m.WorkspaceID)
	for _, e := range sorted {
		fmt.Fprintf(sum, "%s\x00%s\x00%s\x00%d\x00%s\x00%t\n",
			e.Resource, e.Class, e.Status, e.Bytes, e.SHA256, e.SecretsExcluded)
	}
	m.Checksum = hex.EncodeToString(sum.Sum(nil))
}

// ValidateSources reports Sources that name a resource class the ownership
// catalog does not carry.
//
// This is the guard in the other direction from the seeded manifest. A typo in
// a Source's Resource name would otherwise be perfectly quiet: the Source runs,
// its bytes go nowhere, and the class it was meant to fill stays
// "not-exported" — an export silently missing a resource whose exporter
// somebody wrote, reviewed, and shipped.
func ValidateSources(sources []Source) error {
	known := map[string]bool{}
	for _, p := range ownership.WorkspaceExportPlan() {
		known[p.Resource] = true
	}
	var unknown []string
	for _, s := range sources {
		if !known[strings.TrimSpace(s.Resource)] {
			unknown = append(unknown, s.Resource)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf("workspaceexport: sources name resource classes absent from the ownership catalog: %s", strings.Join(unknown, ", "))
}
