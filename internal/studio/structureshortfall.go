package studio

// structureshortfall.go — did the graph actually build the SHAPE the user described?
//
// CoverageShortfall answers "did it use the capability you named". This answers
// the other half: "did it build the structure you named". They are different
// failures and the second had nothing watching it.
//
// The failure, observed live and twice over. Given a prompt naming three
// specialists working in parallel and an editor combining them, the same model
// on the same build produced:
//
//	run A:  fetch_data → check_data → run_reviews(parallel) → 3 reviewers → writer
//	run B:  retrieve_data → produce                                    (2 nodes, 1 agent)
//
// Run B is not a wrong answer the contract can catch — it is structurally
// valid, passes every check, and silently does a fraction of the job. Nothing
// distinguished it from run A except reading the nodes.
//
// So: extract the shape the intent asks for, compare it to what was built, and
// say what is missing. The pipeline retries on that sentence exactly as it
// retries on a coverage shortfall, and the contract reports it when the retry
// does not close the gap.
//
// DELIBERATELY CONSERVATIVE. A false positive costs a wasted model call and an
// unearned warning on a graph that was fine, so this only fires when the intent
// is explicit: it must name a parallel/simultaneous arrangement AND enumerate
// distinct roles. "Summarise my email" names no roles and is never flagged.

import (
	"fmt"
	"regexp"
	"strings"
)

// StructurePlan is the shape an intent asks for, as far as we can tell from the
// words. Zero value means "nothing specific was asked for", which is the case
// for most prompts and must never produce a shortfall.
type StructurePlan struct {
	// WantsFanOut is set when the intent describes work happening at the same
	// time across several workers.
	WantsFanOut bool
	// Roles are the distinct specialist roles named, lowercased and deduped
	// ("fundamentals analyst", "risk analyst", "sentiment analyst").
	Roles []string
	// WantsJoin is set when something combines/merges the parallel results.
	WantsJoin bool
}

// fanOutCue matches an explicit statement that work happens concurrently.
var fanOutCue = regexp.MustCompile(`(?i)\b(in parallel|parallel|simultaneous(ly)?|at the same time|concurrent(ly)?|side by side)\b`)

// joinCue matches an explicit statement that the branches are recombined.
var joinCue = regexp.MustCompile(`(?i)\b(combine[sd]?|combining|merge[sd]?|merging|consolidat\w*|synthesi[sz]\w*|bring\w* together|into one|into a single)\b`)

// roleCue matches "a <adjective> analyst/reviewer/…" — the way a person names a
// specialist. The noun list is deliberately short: these are the words that
// actually mean "a worker with a job", not any noun at all.
var roleCue = regexp.MustCompile(`(?i)\b([a-z][a-z-]*)\s+(analyst|analysts|reviewer|reviewers|specialist|specialists|researcher|researchers|checker|checkers|editor|editors|writer|writers|critic|critics|auditor|auditors)\b`)

// roleStopWords are words that can sit immediately before a role noun without
// naming the role. Without this, "run three reviewers in parallel" yielded the
// role "three reviewer" and inflated the count — caught by the test using the
// real live prompt as its fixture.
var roleStopWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "of": true,
	"my": true, "your": true, "our": true,
	"each": true, "these": true, "those": true, "other": true, "same": true,
	"new": true, "run": true, "have": true, "using": true, "with": true,
	"into": true, "over": true, "then": true, "three": true, "two": true,
	"four": true, "five": true, "six": true, "several": true, "separate": true,
	"parallel": true, "specialist": true,
}

// countCue catches "three specialists" / "3 reviewers" — a stated number of
// workers, which is a role count even when the roles are not each named.
var countCue = regexp.MustCompile(`(?i)\b(two|three|four|five|six|\d+)\s+(analysts|reviewers|specialists|researchers|checkers|editors|writers|critics|auditors|agents)\b`)

var numberWords = map[string]int{
	"two": 2, "three": 3, "four": 4, "five": 5, "six": 6,
}

// PlanFromIntent reads the structure an intent asks for.
func PlanFromIntent(intent string) StructurePlan {
	var p StructurePlan
	if strings.TrimSpace(intent) == "" {
		return p
	}
	p.WantsFanOut = fanOutCue.MatchString(intent)
	p.WantsJoin = joinCue.MatchString(intent)

	seen := map[string]bool{}
	for _, m := range roleCue.FindAllStringSubmatch(intent, -1) {
		qualifier := strings.ToLower(strings.TrimSpace(m[1]))
		noun := strings.ToLower(strings.TrimSpace(m[2]))
		// "three reviewers" is a count, not a role name; countCue reads that.
		if qualifier == "" || roleStopWords[qualifier] {
			continue
		}
		role := strings.TrimSpace(qualifier + " " + strings.TrimSuffix(noun, "s"))
		if !seen[role] {
			seen[role] = true
			p.Roles = append(p.Roles, role)
		}
	}
	return p
}

// wantedWorkers is how many workers run IN THE FAN-OUT.
//
// An explicit count wins over the number of named roles, and is not maxed with
// it. "run three reviewers in parallel … then a scorecard writer combines them"
// names four roles but only three of them run concurrently; the fourth is the
// join. Taking the larger number demanded a four-way fan-out and flagged the
// graph that had built the right thing — which is exactly what the live-fixture
// test caught.
func (p StructurePlan) wantedWorkers(intent string) int {
	for _, m := range countCue.FindAllStringSubmatch(intent, -1) {
		c := numberWords[strings.ToLower(m[1])]
		if c == 0 {
			fmt.Sscanf(m[1], "%d", &c)
		}
		if c >= 2 {
			return c
		}
	}
	return len(p.Roles)
}

// StructureShortfall reports, in one sentence, how the built graph falls short
// of the structure the intent describes. Empty means no shortfall — which
// includes every intent that did not describe a specific structure.
//
// The bar for firing: the intent must explicitly say the work happens in
// parallel AND name (or count) at least two distinct workers. Anything vaguer
// is the model's judgement to make, and second-guessing it would flag correct
// graphs.
func StructureShortfall(intent string, res Result) string {
	p := PlanFromIntent(intent)
	if !p.WantsFanOut {
		return ""
	}
	want := p.wantedWorkers(intent)
	if want < 2 {
		return ""
	}

	agents, hasParallel := graphShape(res.Workflow)

	// A reasoning agent has no graph to judge; its whole design is one loop.
	if res.Workflow.IsAgent() {
		return ""
	}

	switch {
	case agents == 0:
		return fmt.Sprintf("describes %d specialists working in parallel but the graph delegates to no agent at all", want)
	case agents < want && !hasParallel:
		return fmt.Sprintf("describes %d specialists working in parallel but the graph has %s and no parallel step",
			want, pluralAgents(agents))
	case agents < want:
		return fmt.Sprintf("describes %d specialists working in parallel but the graph has only %s",
			want, pluralAgents(agents))
	case !hasParallel:
		return fmt.Sprintf("describes %d specialists working in parallel but the graph runs them one after another with no parallel step", want)
	}
	return ""
}

func pluralAgents(n int) string {
	if n == 1 {
		return "1 agent step"
	}
	return fmt.Sprintf("%d agent steps", n)
}

// graphShape counts the agent nodes and reports whether a parallel fan-out node
// is present.
func graphShape(d Draft) (agents int, hasParallel bool) {
	for _, n := range d.Flow.Nodes {
		switch strings.ToLower(strings.TrimSpace(string(n.Kind))) {
		case "agent":
			agents++
		case "parallel":
			hasParallel = true
		}
	}
	return agents, hasParallel
}

// StructureCorrection is the instruction handed to the builder model on a
// structure retry. Same shape as the coverage retry's MustUse block: name the
// omission rather than restating the brief, because a model that has already
// read the brief once and ignored this is unlikely to read it differently.
func StructureCorrection(intent string) string {
	p := PlanFromIntent(intent)
	if !p.WantsFanOut {
		return ""
	}
	want := p.wantedWorkers(intent)
	if want < 2 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("CORRECTION — YOUR PREVIOUS ATTEMPT COLLAPSED THE STRUCTURE THE USER DESCRIBED.\n")
	fmt.Fprintf(&sb, "The request describes %d separate workers running AT THE SAME TIME, and your graph did not build that.\n", want)
	sb.WriteString("You MUST emit a node of kind \"parallel\" whose edges fan out to one `agent` node per worker")
	// Naming the roles is the most useful part of this correction, so enumerate
	// them — but only as many as run concurrently. When more roles were read
	// than the count, the extras are the step that JOINS the branches, and
	// every natural phrasing introduces the joiner AFTER the workers ("run
	// three reviewers in parallel: A, B, C. Finally D combines them"), so the
	// first `want` are the branches. Worst case a corrective hint names a
	// slightly wrong subset, which is still far better than naming none.
	if len(p.Roles) >= want {
		sb.WriteString(", one for each of: " + strings.Join(p.Roles[:want], ", "))
	} else if len(p.Roles) > 0 {
		sb.WriteString(", including: " + strings.Join(p.Roles, ", "))
	}
	sb.WriteString(".\n")
	sb.WriteString("Define each worker in `new_agents` unless an installed agent already does that exact job.\n")
	if p.WantsJoin {
		sb.WriteString("Every branch MUST then feed the step that combines them, so the combining step reads all of their outputs.\n")
	}
	sb.WriteString("Do NOT merge these workers into one agent or one llm step: the user asked for separate specialists, and one step doing all of it is a different design from the one requested.\n")
	sb.WriteString("Everything else is still your judgement — only the omission above is being corrected.\n\n")
	return sb.String()
}

// RetryForStructure asks the builder model again with the collapsed structure
// named, and returns the better of the two graphs.
//
// One function, because there are two design paths — the streamed pipeline and
// the gateway's studioDesignGraph — and the last rule that lived in two copies
// got fixed in one of them and stayed broken in the other for a whole release.
//
// `build` is the caller's own compile closure, so this does not need to know
// whether it is building a workflow or an agent, or which context and model
// are in play.
//
// Keeps the retry ONLY if it actually closed the gap, matching the coverage
// retry: a second graph that misses too is not evidence of anything, and
// swapping it in would churn the canvas for nothing. Returns the graph to use,
// whether it changed, and a sentence for the transcript.
func RetryForStructure(intent string, first Result, build func(Catalog) (Result, error), cat Catalog) (Result, bool, string) {
	short := StructureShortfall(intent, first)
	if short == "" {
		return first, false, ""
	}
	correction := StructureCorrection(intent)
	if correction == "" {
		return first, false, ""
	}
	retryCat := cat
	retryCat.StructureCorrection = correction

	second, err := build(retryCat)
	if err != nil {
		return first, false, "Retry failed; keeping the first graph."
	}
	if StructureShortfall(intent, second) != "" {
		return first, false, "Retry did not rebuild the structure; keeping the first graph."
	}
	second.Notes = append(second.Notes,
		"The first graph "+short+", so Studio rebuilt it with the separate steps you described.")
	return second, true, "Retry rebuilt the parallel structure you described."
}
