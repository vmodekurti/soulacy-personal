// Package recovery defines the production recovery contract and the automated
// drill that proves a release can satisfy it.
package recovery

import (
	"fmt"
	"sort"
	"time"
)

// Target is one durable subsystem's maximum acceptable data loss and service
// restoration time. These values are product commitments, not defaults hidden
// in an operator guide, so tests and tooling consume the same source.
type Target struct {
	Subsystem string        `json:"subsystem"`
	RPO       time.Duration `json:"rpo"`
	RTO       time.Duration `json:"rto"`
	Method    string        `json:"method"`
}

// ProductionTargets is the baseline for Team and Scale. Qdrant is rebuildable
// from relational source records, hence its wider RTO but identical source RPO.
func ProductionTargets() []Target {
	return []Target{
		{Subsystem: "postgresql", RPO: 5 * time.Minute, RTO: 60 * time.Minute, Method: "continuous WAL archive plus daily base backup and point-in-time recovery"},
		{Subsystem: "object-storage", RPO: 15 * time.Minute, RTO: 2 * time.Hour, Method: "versioning plus cross-region replication or immutable snapshot"},
		{Subsystem: "credential-vault", RPO: 5 * time.Minute, RTO: 60 * time.Minute, Method: "encrypted snapshot with external KMS availability and restore probe"},
		{Subsystem: "vector-index", RPO: 5 * time.Minute, RTO: 4 * time.Hour, Method: "snapshot restore or deterministic rebuild from relational source references"},
	}
}

func ValidateTargets(targets []Target) error {
	seen := map[string]bool{}
	for _, target := range targets {
		if target.Subsystem == "" || target.RPO <= 0 || target.RTO <= 0 || target.Method == "" {
			return fmt.Errorf("recovery: incomplete target for %q", target.Subsystem)
		}
		if seen[target.Subsystem] {
			return fmt.Errorf("recovery: duplicate target %q", target.Subsystem)
		}
		seen[target.Subsystem] = true
	}
	for _, required := range []string{"postgresql", "object-storage", "credential-vault", "vector-index"} {
		if !seen[required] {
			return fmt.Errorf("recovery: no target for %s", required)
		}
	}
	return nil
}

func SortedTargets() []Target {
	targets := ProductionTargets()
	sort.Slice(targets, func(i, j int) bool { return targets[i].Subsystem < targets[j].Subsystem })
	return targets
}
