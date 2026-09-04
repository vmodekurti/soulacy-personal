package agent

// toolschema.go — the tool-contract snapshot a saved agent carries (P0-3).
//
// MCP tool schemas are discovered live at connect time and used to ground
// generation. But nothing recorded WHICH schema a workflow was built against,
// so when a server shipped a breaking change — a renamed argument, a parameter
// becoming required — the agent kept running against a contract that no longer
// existed. The failure surfaced at 07:00 as an argument-validation error two
// nodes deep, and nothing could distinguish "this was always wrong" from "this
// changed under us".
//
// A snapshot fixes that: at save, each tool the workflow calls is recorded with
// a hash of its schema. Comparing the snapshot against live schemas answers
// "did the ground move", names the node affected, and marks the agent as
// needing recertification. Purely additive — an agent without a snapshot
// behaves exactly as before.

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// ToolSchemaRecord is one tool's contract as it stood when the agent was saved.
type ToolSchemaRecord struct {
	// Tool is the exact tool name a node invokes (e.g. mcp__notebooklm__studio_create).
	Tool string `yaml:"tool" json:"tool"`
	// Hash is the first 12 hex chars of sha256 over the tool's normalized
	// signature. A change in parameter names, types, or requiredness changes it;
	// a change in description or parameter ORDER does not, because neither
	// affects whether an existing call still validates.
	Hash string `yaml:"hash" json:"hash"`
	// Params is the human-readable signature the hash was taken over, kept so a
	// drift report can show what the contract used to be rather than only that
	// it differs.
	Params string `yaml:"params,omitempty" json:"params,omitempty"`
	// Nodes lists the workflow node ids that call this tool, so drift points at
	// the blocks to fix instead of at a bare tool name.
	Nodes []string `yaml:"nodes,omitempty" json:"nodes,omitempty"`
}

// ToolSchemaSnapshot is every tool contract an agent was built against.
type ToolSchemaSnapshot struct {
	// CapturedAt is an RFC3339 timestamp; informational.
	CapturedAt string             `yaml:"captured_at,omitempty" json:"captured_at,omitempty"`
	Tools      []ToolSchemaRecord `yaml:"tools,omitempty" json:"tools,omitempty"`
}

// HashToolSignature returns the stable content hash for a tool signature. The
// signature is normalized first (see NormalizeToolSignature) so cosmetic
// differences don't read as drift.
func HashToolSignature(params string) string {
	sum := sha256.Sum256([]byte(NormalizeToolSignature(params)))
	return hex.EncodeToString(sum[:])[:12]
}

// NormalizeToolSignature canonicalises a compact param hint
// ("title*:string, summary:string") so equivalent signatures hash identically:
// entries are trimmed, lower-cased, and sorted. Order carries no contract
// meaning — a server that lists the same parameters differently has not
// changed what a call must supply.
func NormalizeToolSignature(params string) string {
	parts := strings.Split(params, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// Record returns the snapshot entry for a tool, or nil.
func (s *ToolSchemaSnapshot) Record(tool string) *ToolSchemaRecord {
	if s == nil {
		return nil
	}
	for i := range s.Tools {
		if s.Tools[i].Tool == tool {
			return &s.Tools[i]
		}
	}
	return nil
}

// HasTools reports whether the snapshot records anything.
func (s *ToolSchemaSnapshot) HasTools() bool { return s != nil && len(s.Tools) > 0 }
