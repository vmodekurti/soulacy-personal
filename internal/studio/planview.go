package studio

// planview.go — ST-03 "Plain-Language Plan View" and ST-06's join policy.
//
// The canvas shows nodes, ports and templates. That is the right view for
// someone debugging a wire and the wrong view for someone deciding whether the
// workflow does what they asked. A user who cannot read a graph cannot approve
// one, and approving-without-reading is exactly how an agent reaches production
// doing something nobody intended.
//
// PlanView projects the compiled graph into three parts a non-developer can
// read — when it starts, what work happens, where the result goes — while
// staying a projection rather than a second source of truth: it is derived from
// the graph on every call, so Plan / Canvas / SOUL.yaml cannot disagree.
//
// Parallelism is made explicit here because it is the part users most often get
// silently wrong: "search three sites" either runs three searches at once and
// joins them, or it doesn't, and the graph is the only place that says which.

import (
	"fmt"
	"sort"
	"strings"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// Join policies for a parallel group (ST-06).
const (
	// JoinAll — every branch must succeed. The default, because silently
	// dropping a failed branch is how a "successful" run delivers two of three
	// sources with nothing to say it was short.
	JoinAll = "all"
	// JoinAny — the first success wins; remaining branches are abandoned.
	JoinAny = "any"
	// JoinQuorum — a stated minimum must succeed.
	JoinQuorum = "quorum"
	// JoinBestEffort — proceed with whatever succeeded, including nothing.
	// Legitimate for "check these five feeds", dangerous as a default, which is
	// why it must be chosen rather than inherited.
	JoinBestEffort = "best_effort"
)

// PlanStage is one readable step. It carries the operational facts a reviewer
// needs — what goes in, what comes out, what happens on failure, when it is
// considered finished — because a plan that omits those is a diagram, not a
// specification.
type PlanStage struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
	Kind     string `json:"kind"`
	Input    string `json:"input,omitempty"`
	Output   string `json:"output,omitempty"`
	Retry    string `json:"retry"`
	Complete string `json:"complete"`
	// Branches is set on a parallel group: the concurrent stages it contains.
	Branches []PlanStage `json:"branches,omitempty"`
	// Join is the group's join policy (ST-06), empty for a single stage.
	Join string `json:"join,omitempty"`
	// JoinDetail explains that policy in words, since "quorum" alone tells a
	// reviewer nothing about what happens when a branch dies.
	JoinDetail string `json:"join_detail,omitempty"`
	Parallel   bool   `json:"parallel,omitempty"`
}

// PlanView is the readable projection of a workflow.
type PlanView struct {
	Trigger  PlanTrigger `json:"trigger"`
	Work     []PlanStage `json:"work"`
	Delivery []PlanStage `json:"delivery"`
	// Warnings are plan-level observations a reviewer should see before
	// approving — an unjoined parallel group, an unreachable stage.
	Warnings []string `json:"warnings,omitempty"`
}

// PlanTrigger describes when the workflow starts.
type PlanTrigger struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// BuildPlanView projects a draft into the readable plan. Pure: same graph in,
// same plan out, so Plan and Canvas can never disagree about what will run.
func BuildPlanView(draft Draft) PlanView {
	pv := PlanView{Trigger: planTrigger(draft)}

	order := planOrder(draft.Flow)
	groups := parallelGroups(draft.Flow, order)
	consumed := map[string]bool{}
	for _, g := range groups {
		for _, id := range g.Members {
			consumed[id] = true
		}
	}

	byID := map[string]sdkr.FlowNode{}
	for _, n := range draft.Flow.Nodes {
		byID[n.ID] = n
	}

	emitted := map[string][]string{} // group leader → member ids
	for _, id := range order {
		n, ok := byID[id]
		if !ok || sdkr.IsStructuralKind(n.Kind) {
			continue
		}
		if leader, inGroup := groupLeaderFor(groups, id); inGroup {
			if _, done := emitted[leader]; done {
				continue // the whole group was emitted at its leader
			}
			g, _ := groupFor(groups, leader)
			emitted[leader] = g.Members
			stage := parallelStage(byID, g)
			pv.Work = append(pv.Work, stage)
			continue
		}
		stage := planStage(n)
		if isOutcomeDeliveryNode(n) {
			pv.Delivery = append(pv.Delivery, stage)
			continue
		}
		pv.Work = append(pv.Work, stage)
	}

	pv.Warnings = planWarnings(pv, draft)
	return pv
}

func planTrigger(draft Draft) PlanTrigger {
	switch strings.ToLower(strings.TrimSpace(draft.Trigger.Type)) {
	case "schedule", "cron":
		detail := strings.TrimSpace(triggerCron(draft.Trigger))
		if detail == "" {
			detail = "on a schedule (not yet set)"
		} else {
			detail = "on the schedule " + detail
		}
		return PlanTrigger{Kind: "schedule", Detail: detail}
	case "channel":
		return PlanTrigger{Kind: "channel", Detail: "when a message arrives"}
	case "webhook", "http":
		return PlanTrigger{Kind: "webhook", Detail: "when an HTTP request arrives"}
	default:
		return PlanTrigger{Kind: "manual", Detail: "when you run it"}
	}
}

// planStage renders one node as a readable step.
func planStage(n sdkr.FlowNode) PlanStage {
	s := PlanStage{
		ID:     n.ID,
		Title:  humanStageTitle(n),
		Detail: strings.TrimSpace(firstNonBlank(n.Description, n.Intent)),
		Kind:   n.Kind,
		Input:  describePortSet(n.Inputs, n.Input),
		Output: describePortSet(n.Outputs, n.Output),
		Retry:  describeRetry(n),
	}
	s.Complete = describeCompletion(n)
	if n.ForEach != "" {
		s.Parallel = true
		s.Join = JoinAll
		s.JoinDetail = describeJoin(JoinAll, 0, forEachWidth(n))
	}
	return s
}

// humanStageTitle prefers what the author said this step does over the tool it
// happens to call — "add the sources" reads better than "mcp__notebooklm__add".
func humanStageTitle(n sdkr.FlowNode) string {
	if d := strings.TrimSpace(n.Description); d != "" {
		return d
	}
	if i := strings.TrimSpace(n.Intent); i != "" {
		return i
	}
	if t := strings.TrimSpace(n.Tool); t != "" {
		return prettyToolName(t)
	}
	if a := strings.TrimSpace(n.Agent); a != "" {
		return "ask the " + a + " agent"
	}
	return strings.ReplaceAll(n.ID, "_", " ")
}

func prettyToolName(tool string) string {
	t := tool
	if strings.HasPrefix(t, "mcp__") {
		parts := strings.SplitN(strings.TrimPrefix(t, "mcp__"), "__", 2)
		if len(parts) == 2 {
			t = parts[1] + " (" + parts[0] + ")"
		}
	}
	return strings.ReplaceAll(strings.ReplaceAll(t, "_", " "), ".", " ")
}

func describePortSet(ports []sdkr.FlowPort, fallback string) string {
	if len(ports) == 0 {
		if strings.TrimSpace(fallback) == "" {
			return ""
		}
		return "(untyped)"
	}
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		label := p.Name
		if p.Type != "" {
			label += ": " + p.Type
		}
		if portCardinalityLabel(p) == "many" {
			label += " (many)"
		}
		if p.Required {
			label += "*"
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, ", ")
}

func portCardinalityLabel(p sdkr.FlowPort) string {
	c := strings.ToLower(strings.TrimSpace(p.Cardinality))
	if c == "many" {
		return "many"
	}
	t := strings.ToLower(strings.TrimSpace(p.Type))
	if strings.HasSuffix(t, "[]") || strings.HasPrefix(t, "[]") || t == "array" || t == "list" {
		return "many"
	}
	return "one"
}

func describeRetry(n sdkr.FlowNode) string {
	switch strings.ToLower(strings.TrimSpace(n.OnError)) {
	case "retry":
		return "retries once, then stops the workflow"
	case "skip":
		return "skips this step and carries on"
	case "escalate":
		return "hands off to the escalation step"
	default:
		return "stops the workflow"
	}
}

func describeCompletion(n sdkr.FlowNode) string {
	if isPollLikeID(n.ID) || isPollLikeID(n.Tool) {
		// A polling step's completion condition is the whole point of it, and
		// is invisible on the canvas.
		if t := strings.TrimSpace(n.Timeout); t != "" {
			return "when the job reports a finished state, or after " + t
		}
		return "when the job reports a finished state"
	}
	if t := strings.TrimSpace(n.Timeout); t != "" {
		return "when the step returns, or after " + t
	}
	return "when the step returns"
}

func isPollLikeID(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "poll") || strings.Contains(l, "status") || strings.Contains(l, "wait")
}

func forEachWidth(n sdkr.FlowNode) int {
	if n.MaxParallel > 0 {
		return n.MaxParallel
	}
	return 0
}

// parallelStage renders a group of concurrent stages plus its join.
func parallelStage(byID map[string]sdkr.FlowNode, g parallelGroup) PlanStage {
	members := g.Members
	s := PlanStage{
		ID:       "parallel:" + strings.Join(members, "+"),
		Title:    fmt.Sprintf("%d steps at the same time", len(members)),
		Kind:     "parallel",
		Parallel: true,
		Retry:    "each branch follows its own retry policy",
	}
	skips := 0
	for _, id := range members {
		n := byID[id]
		branch := planStage(n)
		branch.Parallel = true
		s.Branches = append(s.Branches, branch)
		if strings.EqualFold(n.OnError, "skip") {
			skips++
		}
	}

	// A DECLARED join always wins over an inferred one. Once the fan-out point is
	// a kind=parallel node, the engine waits on exactly the policy that node
	// states (sdkr.FlowNode.Join, empty meaning JoinAll) — so inferring a
	// different policy from the branches' on_error would put the plan and the
	// runtime into direct contradiction, which is the one thing a projection must
	// never do. Inference is kept only for the legacy shape, where the fan-out
	// point is an ordinary node and nothing in the graph states a policy at all.
	if declared, need, ok := declaredJoin(byID, g); ok {
		s.Join = declared
		s.JoinDetail = describeJoin(declared, need, len(members))
	} else {
		// The join policy is READ from the branches' own error handling rather than
		// invented: branches that skip on error are best-effort by construction, and
		// claiming "all must succeed" over them would be a lie the canvas contradicts.
		switch {
		case skips == len(members) && skips > 0:
			s.Join = JoinBestEffort
		case skips > 0:
			s.Join = JoinQuorum
		default:
			s.Join = JoinAll
		}
		s.JoinDetail = describeJoin(s.Join, len(members)-skips, len(members))
	}
	s.Complete = "when the join condition is met"
	if barrier := strings.TrimSpace(byID[g.Producer].JoinNode); barrier != "" {
		// Where the branches converge is invisible on the canvas but decides which
		// step sees the combined result, so a reviewer needs it stated.
		s.Complete = "when the join condition is met at “" + barrier + "”"
	}
	return s
}

// declaredJoin reads the join policy a kind=parallel fan-out node DECLARES.
// ok is false when the producer is not a parallel node — i.e. nothing in the
// graph states a policy and the branch-inference fallback is the only answer
// available.
func declaredJoin(byID map[string]sdkr.FlowNode, g parallelGroup) (policy string, need int, ok bool) {
	p, found := byID[g.Producer]
	if !found || p.Kind != sdkr.FlowNodeParallel {
		return "", 0, false
	}
	switch strings.ToLower(strings.TrimSpace(p.Join)) {
	case sdkr.JoinAny:
		return JoinAny, 1, true
	case sdkr.JoinQuorum:
		return JoinQuorum, p.JoinQuorum, true
	case sdkr.JoinBestEffort:
		return JoinBestEffort, 0, true
	default:
		// Empty is not "unspecified" here: the engine treats an empty Join on a
		// parallel node as JoinAll, so the plan must say JoinAll too.
		return JoinAll, len(g.Members), true
	}
}

// describeJoin states a join policy in consequences, not jargon.
func describeJoin(policy string, need, total int) string {
	switch policy {
	case JoinAny:
		return "carries on as soon as any branch succeeds; the rest are abandoned"
	case JoinQuorum:
		if need > 0 && total > 0 {
			return fmt.Sprintf("carries on when at least %d of %d branches succeed; the rest are skipped", need, total)
		}
		return "carries on when enough branches succeed"
	case JoinBestEffort:
		return "carries on with whatever succeeded — including nothing, so make sure a later step checks the count"
	default:
		if total > 0 {
			return fmt.Sprintf("waits for all %d branches; if one fails the workflow stops", total)
		}
		return "waits for every branch; if one fails the workflow stops"
	}
}

// planOrder returns node ids in a stable execution-ish order: entry first, then
// edge order, then any unreferenced nodes so nothing silently disappears.
func planOrder(flow Flow) []string {
	var order []string
	seen := map[string]bool{}
	push := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			order = append(order, id)
		}
	}
	if flow.Entry != "" {
		push(flow.Entry)
	}
	for _, e := range flow.Edges {
		push(e.From)
		push(e.To)
	}
	for _, n := range flow.Nodes {
		push(n.ID)
	}
	// "end" is a terminal marker, not a node.
	out := order[:0]
	for _, id := range order {
		if id != "end" {
			out = append(out, id)
		}
	}
	return out
}

// parallelGroup is one fan-out: the node the branches leave FROM, plus the
// branches. The producer is carried rather than discarded because a
// kind=parallel producer DECLARES the join policy — and a declaration has to
// beat an inference, or the plan can contradict what the engine will do.
type parallelGroup struct {
	Producer string
	Members  []string
}

// parallelGroups finds sets of nodes that fan out from one producer and rejoin.
// Two or more nodes sharing a single upstream, none depending on another, is
// exactly the shape a user means by "at the same time".
func parallelGroups(flow Flow, order []string) []parallelGroup {
	fanout := map[string][]string{}
	for _, e := range flow.Edges {
		if e.To == "" || e.To == "end" {
			continue
		}
		fanout[e.From] = append(fanout[e.From], e.To)
	}
	// A node with an incoming edge from a sibling is sequential, not parallel.
	incoming := map[string]int{}
	for _, e := range flow.Edges {
		incoming[e.To]++
	}

	var groups []parallelGroup
	for _, from := range sortedFanoutKeys(fanout) {
		targets := dedupeNonEmpty(fanout[from])
		if len(targets) < 2 {
			continue
		}
		var members []string
		for _, t := range targets {
			if incoming[t] == 1 { // fed only by the shared producer
				members = append(members, t)
			}
		}
		if len(members) >= 2 {
			sort.Strings(members)
			groups = append(groups, parallelGroup{Producer: from, Members: members})
		}
	}
	return groups
}

// triggerCron reads the cron expression out of the trigger's config bag.
func triggerCron(t Trigger) string {
	if t.Config == nil {
		return ""
	}
	for _, key := range []string{"cron", "schedule", "expression"} {
		if v, ok := t.Config[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func sortedFanoutKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func groupLeaderFor(groups []parallelGroup, id string) (string, bool) {
	for _, g := range groups {
		for _, m := range g.Members {
			if m == id {
				return g.Members[0], true
			}
		}
	}
	return "", false
}

func groupFor(groups []parallelGroup, leader string) (parallelGroup, bool) {
	for _, g := range groups {
		if len(g.Members) > 0 && g.Members[0] == leader {
			return g, true
		}
	}
	return parallelGroup{}, false
}

func planWarnings(pv PlanView, draft Draft) []string {
	var out []string
	for _, s := range pv.Work {
		if s.Join == JoinBestEffort {
			out = append(out, fmt.Sprintf("“%s” continues even if every branch fails — add a check on the result count so an empty run is caught.", s.Title))
		}
	}
	if len(pv.Delivery) == 0 && len(pv.Work) > 0 {
		out = append(out, "This plan produces a result but never delivers it anywhere.")
	}
	if pv.Trigger.Kind == "schedule" && strings.Contains(pv.Trigger.Detail, "not yet set") {
		out = append(out, "The schedule has no time set, so this will not run automatically.")
	}
	if draft.Outcome == nil || len(draft.Outcome.Assertions) == 0 {
		out = append(out, "No success check is defined, so a run that produces nothing would still be reported as successful.")
	}
	return out
}

func firstNonBlank(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
