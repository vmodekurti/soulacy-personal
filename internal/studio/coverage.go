package studio

// coverage.go — does the deterministic planner actually cover this intent?
//
// Studio can use the deterministic planner as an explicit reproducibility mode
// or as the fallback when model-grounded graph design fails. That planner is
// reproducible and auditable, but it must not be treated as complete when it
// silently omits a capability the intent explicitly requested.
//
// But the deterministic planner is blind to most of a workspace's capabilities.
// The workflow skeletons are hardcoded to web_search + an LLM summarise step and
// never reference MCP at all; the agent compiler routes MCP through a switch over
// four domains and selects NO skills whatsoever. So a prompt that names an
// installed MCP tool produced a graph that quietly used none of it — reproducible
// and wrong.
//
// This is the check that stops "reproducible" beating "correct". When the
// deterministic result would ignore a capability the intent actually asked for,
// it declines and the caller falls through to the LLM planner, which can read the
// whole catalogue and wire it.
//
// The bar is deliberately CONCRETE: a capability only counts as required if the
// user named something this workspace actually has. It never declines because of
// a vague topic guess, so the deterministic path keeps every case it genuinely
// handles.

import (
	"sort"
	"strings"
)

// EncodesProcedure reports whether a deterministic draft is a CURATED
// multi-step procedure rather than a generic fallback skeleton.
//
// The deterministic planner produces two very different kinds of thing, and
// treating them the same is what made "model-first" a bad trade:
//
//   - Curated macro-flows: the NotebookLM podcast graph wires create → add
//     sources → generate audio → poll status in the one order that works. That
//     ordering is hard-won knowledge a builder model does not have, and letting
//     a model design it produced a useless single-node web_search graph.
//   - Generic skeletons: the digest patterns are web_search → summarise, matched
//     on keywords, referencing no MCP at all. A model beats these easily.
//
// The signal is whether the draft reaches for real capabilities: an MCP tool or
// a skill means the pattern knew something specific about the job. Builtins
// alone mean it fell back to a shape that fits anything, which is exactly when
// the model should be asked instead.
func EncodesProcedure(res Result) bool {
	if len(res.Workflow.Skills) > 0 {
		return true
	}
	for t := range draftToolSet(res.Workflow) {
		if strings.HasPrefix(strings.ToLower(t), "mcp__") {
			return true
		}
	}
	return false
}

// namedSkills returns the catalogue skills the intent explicitly names. Mirrors
// namedMCPTools: matched against what is installed, never invented.
func namedSkills(intent string, cat Catalog) []string {
	li := strings.ToLower(intent)
	var out []string
	for _, sk := range cat.Skills {
		name := strings.TrimSpace(sk.Name)
		if name == "" {
			continue
		}
		lname := strings.ToLower(name)
		// Skill ids are often snake_case or hyphenated; a user writes them with
		// spaces. Compare on both so "stock performance" matches
		// "stock_performance".
		spaced := strings.NewReplacer("_", " ", "-", " ").Replace(lname)
		if len(lname) >= 3 && (strings.Contains(li, lname) || strings.Contains(li, spaced)) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return uniqueStrings(out)
}

// CoverageShortfall reports what a generated draft FAILS to cover for this
// intent, or "" when it covers everything the user named.
//
// Deliberately NOT specific to the deterministic planner. A model-designed graph
// can miss a named capability too — it just misses it for different reasons
// (context crowding, a plausible-looking generic substitute) — and the failure
// looks identical from the outside: a graph that runs, and does the wrong thing.
// One check, applied to whoever built the draft.
//
// The comparison is against what actually landed in the draft — tools on an
// agent, node tools on a workflow — rather than against what the planner
// intended, because the whole failure mode is a planner believing it handled
// something it did not.
func CoverageShortfall(intent string, cat Catalog, res Result) string {
	var missing []string

	if named := namedMCPTools(intent, cat); len(named) > 0 {
		// draftToolSet lives in security_preflight.go and is case-PRESERVING by
		// design; it is not changed here because other callers depend on exact
		// names. Fold to lower case locally instead, for a tolerant comparison.
		used := draftToolSet(res.Workflow)
		lower := make(map[string]bool, len(used))
		for t := range used {
			lower[strings.ToLower(t)] = true
		}
		for _, tool := range named {
			if !lower[strings.ToLower(tool)] {
				missing = append(missing, tool)
			}
		}
	}

	// The deterministic path never selects skills, so a named skill is always a
	// shortfall — but only report it when the workspace really has that skill.
	if named := namedSkills(intent, cat); len(named) > 0 {
		have := map[string]bool{}
		for _, s := range res.Workflow.Skills {
			have[strings.ToLower(strings.TrimSpace(s))] = true
		}
		for _, s := range named {
			if !have[strings.ToLower(s)] {
				missing = append(missing, "skill "+s)
			}
		}
	}

	if len(missing) == 0 {
		return ""
	}
	return "the deterministic planner cannot wire " + strings.Join(missing, ", ")
}
