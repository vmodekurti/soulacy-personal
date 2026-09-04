package learning

import (
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

// This file closes the last remaining gap in the learning-loop story called out
// in the competitive report: longitudinal *evidence* that accepted learnings are
// actually paying off over time. It answers two product questions the Brain
// Memory UI could not previously answer:
//
//  1. "Are the skills I accepted actually being reused?" — SkillReuse counts how
//     often each accepted learned skill was loaded (via read_skill/read_skill_file)
//     in real runs *after* it was accepted.
//  2. "Are we making the same mistakes less often?" — RepeatedErrors compares how
//     often each recurring error signature happened before vs after learning was
//     switched on for the agent (the first accepted proposal marks that moment).
//
// Everything here is a pure read-only aggregation over action-log events plus the
// proposals already in the learning store, so it adds no cost to the hot path.

// skillReadTools are the built-in tools that load a full SKILL.md body. A call to
// one of these naming an accepted learned skill is treated as a reuse of that
// skill in planning/execution.
var skillReadTools = map[string]bool{
	"read_skill":      true,
	"read_skill_file": true,
}

// SkillReuse is per-skill evidence that an accepted learned skill is being used
// again in real runs after it was accepted.
type SkillReuse struct {
	SkillName  string     `json:"skill_name"`
	ProposalID string     `json:"proposal_id,omitempty"`
	AcceptedAt *time.Time `json:"accepted_at,omitempty"`
	Uses       int        `json:"uses"`     // total qualifying read_skill calls
	Sessions   int        `json:"sessions"` // distinct sessions that reused it
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// ErrorTrend is per-signature evidence of whether a recurring failure is
// happening less often after learning was enabled. Reduction is in [0,1]: 1.0
// means the error stopped entirely after the reference point.
type ErrorTrend struct {
	Signature string  `json:"signature"`
	Sample    string  `json:"sample,omitempty"`
	Before    int     `json:"before"`
	After     int     `json:"after"`
	Reduction float64 `json:"reduction"`
}

// AgentEvidence is the cross-agent evidence rollup used by the product UI to
// show which agents are benefiting from learning without opening each agent.
type AgentEvidence struct {
	AgentID        string     `json:"agent_id"`
	Accepted       int        `json:"accepted"`
	AcceptedSkills int        `json:"accepted_skills"`
	ReusedSkills   int        `json:"reused_skills"`
	TotalSkillUses int        `json:"total_skill_uses"`
	ErrorsBefore   int        `json:"errors_before"`
	ErrorsAfter    int        `json:"errors_after"`
	ErrorReduction float64    `json:"error_reduction"`
	LastActivity   *time.Time `json:"last_activity,omitempty"`
}

// EvidenceBucket captures weekly learning evidence so improvement is visible as
// a trend instead of a single snapshot.
type EvidenceBucket struct {
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Runs      int       `json:"runs"`
	SkillUses int       `json:"skill_uses"`
	Errors    int       `json:"errors"`
	Accepted  int       `json:"accepted"`
}

// Evidence is the product-facing snapshot proving the learning loop reduces
// repeat work and repeat failures over time.
type Evidence struct {
	AgentID        string           `json:"agent_id,omitempty"`
	ReferenceAt    *time.Time       `json:"reference_at,omitempty"` // first accepted proposal = "learning on"
	SkillReuse     []SkillReuse     `json:"skill_reuse"`
	ReusedSkills   int              `json:"reused_skills"`
	AcceptedSkills int              `json:"accepted_skills"`
	TotalSkillUses int              `json:"total_skill_uses"`
	RepeatedErrors []ErrorTrend     `json:"repeated_errors"`
	ErrorsBefore   int              `json:"errors_before"`
	ErrorsAfter    int              `json:"errors_after"`
	ErrorReduction float64          `json:"error_reduction"` // aggregate over repeated signatures
	Agents         []AgentEvidence  `json:"agents"`
	Trend          []EvidenceBucket `json:"trend"`
}

// BuildEvidence aggregates reuse and error-reduction evidence for one agent from
// its recorded events and accepted learning proposals. Passing an empty agentID
// aggregates across all agents present in the inputs. Events may arrive in any
// order; only their Timestamp is trusted for before/after classification.
func BuildEvidence(agentID string, events []message.Event, accepted []Proposal) Evidence {
	out := Evidence{AgentID: agentID}

	// accepted skill name -> acceptance time (earliest wins if duplicated).
	type acceptedSkill struct {
		proposalID string
		acceptedAt time.Time
	}
	skillAccept := map[string]acceptedSkill{}
	var refAt time.Time
	for _, p := range accepted {
		if agentID != "" && p.AgentID != agentID {
			continue
		}
		if p.Status != StatusAccepted {
			continue
		}
		at := p.UpdatedAt
		if at.IsZero() {
			at = p.CreatedAt
		}
		if refAt.IsZero() || at.Before(refAt) {
			refAt = at
		}
		if strings.EqualFold(strings.TrimSpace(p.Kind), "skill") {
			name := ""
			if p.Meta != nil {
				name = strings.TrimSpace(p.Meta["skill_name"])
			}
			if name == "" {
				continue
			}
			cur, ok := skillAccept[name]
			if !ok || at.Before(cur.acceptedAt) {
				skillAccept[name] = acceptedSkill{proposalID: p.ID, acceptedAt: at}
			}
		}
	}
	if !refAt.IsZero() {
		r := refAt
		out.ReferenceAt = &r
	}
	out.AcceptedSkills = len(skillAccept)

	// Per-skill reuse accumulation.
	type reuseAcc struct {
		uses     int
		sessions map[string]bool
		lastUsed time.Time
	}
	reuse := map[string]*reuseAcc{}
	for name := range skillAccept {
		reuse[name] = &reuseAcc{sessions: map[string]bool{}}
	}

	// Error signature accumulation.
	errs := map[string]*errAcc{}

	for _, ev := range events {
		if agentID != "" && ev.AgentID != agentID {
			continue
		}
		switch ev.Type {
		case "tool.call":
			name := payloadString(ev.Payload, "name")
			if !skillReadTools[name] {
				continue
			}
			skillName := toolArgString(ev.Payload, "skill_name")
			acc, tracked := reuse[skillName]
			if !tracked {
				continue
			}
			// Only count uses at/after the skill was accepted — earlier reads
			// predate the learning and are not evidence of reuse.
			accepted := skillAccept[skillName].acceptedAt
			if !accepted.IsZero() && ev.Timestamp.Before(accepted) {
				continue
			}
			acc.uses++
			if sid := strings.TrimSpace(ev.SessionID); sid != "" {
				acc.sessions[sid] = true
			}
			if ev.Timestamp.After(acc.lastUsed) {
				acc.lastUsed = ev.Timestamp
			}
		case "error":
			text := payloadString(ev.Payload, "error")
			if text == "" {
				continue
			}
			recordError(errs, text, ev.Timestamp, refAt)
		case "tool.result":
			if !toolResultIsError(ev.Payload) {
				continue
			}
			text := payloadString(ev.Payload, "content")
			if text == "" {
				continue
			}
			recordError(errs, text, ev.Timestamp, refAt)
		}
	}

	// Materialise skill reuse (sorted: most-used first, then name).
	for name, at := range skillAccept {
		acc := reuse[name]
		sr := SkillReuse{
			SkillName:  name,
			ProposalID: at.proposalID,
			Uses:       acc.uses,
			Sessions:   len(acc.sessions),
		}
		if !at.acceptedAt.IsZero() {
			a := at.acceptedAt
			sr.AcceptedAt = &a
		}
		if !acc.lastUsed.IsZero() {
			l := acc.lastUsed
			sr.LastUsedAt = &l
		}
		if sr.Uses > 0 {
			out.ReusedSkills++
			out.TotalSkillUses += sr.Uses
		}
		out.SkillReuse = append(out.SkillReuse, sr)
	}
	sort.SliceStable(out.SkillReuse, func(i, j int) bool {
		if out.SkillReuse[i].Uses != out.SkillReuse[j].Uses {
			return out.SkillReuse[i].Uses > out.SkillReuse[j].Uses
		}
		return out.SkillReuse[i].SkillName < out.SkillReuse[j].SkillName
	})

	// Materialise repeated-error trends. A signature only counts as "repeated"
	// (and thus meaningful evidence) when it occurred at least twice overall.
	for sig, acc := range errs {
		total := acc.before + acc.after
		if total < 2 {
			continue
		}
		trend := ErrorTrend{Signature: sig, Sample: acc.sample, Before: acc.before, After: acc.after}
		if acc.before > 0 {
			trend.Reduction = float64(acc.before-acc.after) / float64(acc.before)
			if trend.Reduction < 0 {
				trend.Reduction = 0
			}
		}
		out.ErrorsBefore += acc.before
		out.ErrorsAfter += acc.after
		out.RepeatedErrors = append(out.RepeatedErrors, trend)
	}
	sort.SliceStable(out.RepeatedErrors, func(i, j int) bool {
		bi := out.RepeatedErrors[i].Before + out.RepeatedErrors[i].After
		bj := out.RepeatedErrors[j].Before + out.RepeatedErrors[j].After
		if bi != bj {
			return bi > bj
		}
		return out.RepeatedErrors[i].Signature < out.RepeatedErrors[j].Signature
	})
	if out.ErrorsBefore > 0 {
		out.ErrorReduction = float64(out.ErrorsBefore-out.ErrorsAfter) / float64(out.ErrorsBefore)
		if out.ErrorReduction < 0 {
			out.ErrorReduction = 0
		}
	}

	if out.SkillReuse == nil {
		out.SkillReuse = []SkillReuse{}
	}
	if out.RepeatedErrors == nil {
		out.RepeatedErrors = []ErrorTrend{}
	}
	out.Trend = buildEvidenceTrend(agentID, events, accepted)
	if agentID == "" {
		out.Agents = buildAgentEvidence(events, accepted)
	} else {
		out.Agents = []AgentEvidence{}
	}
	return out
}

func buildAgentEvidence(events []message.Event, accepted []Proposal) []AgentEvidence {
	ids := map[string]bool{}
	lastActivity := map[string]time.Time{}
	acceptedCount := map[string]int{}
	for _, p := range accepted {
		if p.Status != StatusAccepted || strings.TrimSpace(p.AgentID) == "" {
			continue
		}
		ids[p.AgentID] = true
		acceptedCount[p.AgentID]++
		at := proposalTime(p)
		if at.After(lastActivity[p.AgentID]) {
			lastActivity[p.AgentID] = at
		}
	}
	for _, ev := range events {
		if strings.TrimSpace(ev.AgentID) == "" {
			continue
		}
		ids[ev.AgentID] = true
		if ev.Timestamp.After(lastActivity[ev.AgentID]) {
			lastActivity[ev.AgentID] = ev.Timestamp
		}
	}
	out := make([]AgentEvidence, 0, len(ids))
	for id := range ids {
		ev := BuildEvidence(id, events, accepted)
		item := AgentEvidence{
			AgentID:        id,
			Accepted:       acceptedCount[id],
			AcceptedSkills: ev.AcceptedSkills,
			ReusedSkills:   ev.ReusedSkills,
			TotalSkillUses: ev.TotalSkillUses,
			ErrorsBefore:   ev.ErrorsBefore,
			ErrorsAfter:    ev.ErrorsAfter,
			ErrorReduction: ev.ErrorReduction,
		}
		if !lastActivity[id].IsZero() {
			t := lastActivity[id]
			item.LastActivity = &t
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TotalSkillUses != out[j].TotalSkillUses {
			return out[i].TotalSkillUses > out[j].TotalSkillUses
		}
		if out[i].Accepted != out[j].Accepted {
			return out[i].Accepted > out[j].Accepted
		}
		return out[i].AgentID < out[j].AgentID
	})
	return out
}

func buildEvidenceTrend(agentID string, events []message.Event, accepted []Proposal) []EvidenceBucket {
	latest := latestEvidenceTime(agentID, events, accepted)
	if latest.IsZero() {
		return []EvidenceBucket{}
	}
	latestWeek := weekStart(latest.UTC())
	start := latestWeek.AddDate(0, 0, -7*7)
	buckets := make([]EvidenceBucket, 8)
	for i := range buckets {
		st := start.AddDate(0, 0, i*7)
		buckets[i] = EvidenceBucket{Start: st, End: st.AddDate(0, 0, 7)}
	}
	runSessions := make([]map[string]bool, len(buckets))
	for i := range runSessions {
		runSessions[i] = map[string]bool{}
	}
	for _, p := range accepted {
		if agentID != "" && p.AgentID != agentID {
			continue
		}
		if p.Status != StatusAccepted {
			continue
		}
		if idx := bucketIndex(proposalTime(p), start); idx >= 0 && idx < len(buckets) {
			buckets[idx].Accepted++
		}
	}
	for _, ev := range events {
		if agentID != "" && ev.AgentID != agentID {
			continue
		}
		idx := bucketIndex(ev.Timestamp, start)
		if idx < 0 || idx >= len(buckets) {
			continue
		}
		if sid := strings.TrimSpace(ev.SessionID); sid != "" {
			runSessions[idx][sid] = true
		}
		switch ev.Type {
		case "tool.call":
			if skillReadTools[payloadString(ev.Payload, "name")] {
				buckets[idx].SkillUses++
			}
		case "error":
			buckets[idx].Errors++
		case "tool.result":
			if toolResultIsError(ev.Payload) {
				buckets[idx].Errors++
			}
		}
	}
	for i := range buckets {
		buckets[i].Runs = len(runSessions[i])
	}
	return buckets
}

func latestEvidenceTime(agentID string, events []message.Event, accepted []Proposal) time.Time {
	var latest time.Time
	for _, p := range accepted {
		if agentID != "" && p.AgentID != agentID {
			continue
		}
		if at := proposalTime(p); at.After(latest) {
			latest = at
		}
	}
	for _, ev := range events {
		if agentID != "" && ev.AgentID != agentID {
			continue
		}
		if ev.Timestamp.After(latest) {
			latest = ev.Timestamp
		}
	}
	return latest
}

func proposalTime(p Proposal) time.Time {
	if !p.UpdatedAt.IsZero() {
		return p.UpdatedAt.UTC()
	}
	return p.CreatedAt.UTC()
}

func weekStart(t time.Time) time.Time {
	y, m, d := t.Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	offset := (int(day.Weekday()) + 6) % 7
	return day.AddDate(0, 0, -offset)
}

func bucketIndex(t, start time.Time) int {
	if t.IsZero() {
		return -1
	}
	return int(t.UTC().Sub(start) / (7 * 24 * time.Hour))
}

// errAcc accumulates before/after counts for one normalized error signature.
type errAcc struct {
	before int
	after  int
	sample string
}

func recordError(errs map[string]*errAcc, text string, at, refAt time.Time) {
	sig := errorSignature(text)
	if sig == "" {
		return
	}
	acc := errs[sig]
	if acc == nil {
		acc = &errAcc{sample: collapseWhitespace(text)}
		errs[sig] = acc
	}
	// Before the reference point (or no reference at all) counts as "before".
	if refAt.IsZero() || at.Before(refAt) {
		acc.before++
	} else {
		acc.after++
	}
}

var (
	numRe   = regexp.MustCompile(`\d+`)
	hexRe   = regexp.MustCompile(`\b[0-9a-f]{6,}\b`)
	spaceRe = regexp.MustCompile(`\s+`)
	quoteRe = regexp.MustCompile(`["'` + "`" + `]`)
)

// errorSignature normalizes a raw error string into a stable bucket so that the
// same failure with different ids/paths/numbers is grouped together. Returns ""
// for empty input.
func errorSignature(text string) string {
	s := strings.ToLower(collapseWhitespace(text))
	if s == "" {
		return ""
	}
	s = hexRe.ReplaceAllString(s, "#")
	s = numRe.ReplaceAllString(s, "#")
	s = quoteRe.ReplaceAllString(s, "")
	s = strings.TrimSpace(s)
	if len(s) > 160 {
		s = s[:160]
	}
	return s
}

func collapseWhitespace(s string) string {
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

// toolArgString extracts a string argument from a tool.call payload. It handles
// both the typed message.ToolCall (in-process) and the map form produced after
// a JSONL round-trip, where Arguments lives under "arguments".
func toolArgString(payload any, key string) string {
	if tc, ok := payload.(message.ToolCall); ok {
		if tc.Arguments != nil {
			if v, ok := tc.Arguments[key].(string); ok {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	var m map[string]any
	if !payloadMap(payload, &m) {
		return ""
	}
	args, ok := m["arguments"].(map[string]any)
	if !ok {
		return ""
	}
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// toolResultIsError reports whether a tool.result payload was flagged as an
// error, tolerating both the typed struct and the map round-trip form.
func toolResultIsError(payload any) bool {
	if tr, ok := payload.(message.ToolResult); ok {
		return tr.IsError
	}
	var m map[string]any
	if !payloadMap(payload, &m) {
		return false
	}
	if v, ok := m["is_error"].(bool); ok {
		return v
	}
	return false
}
