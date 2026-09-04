package studio

// buildspec.go — ST-01 "Intent To Build Spec".
//
// The trust problem this addresses: today a user types a paragraph, presses
// Generate, and a graph appears. If the graph is wrong they cannot tell whether
// Studio misunderstood the intent or built the misunderstanding correctly —
// those are different bugs with different fixes, and the UI collapses them.
//
// A BuildSpec sits between the two. It states, in the user's own vocabulary,
// exactly what Studio believes it was asked for: when this runs, what it reads,
// what it does, where the result goes, what it needs access to. The user
// confirms or corrects THAT, before a single node exists.
//
// Extraction is deliberately deterministic. A model may refine the prose (see
// Refine), but the structured reading of that prose is rule-based, so the same
// intent always yields the same spec and a user can learn what Studio keys on.
// The screens render this directly: "Studio understood" is a BuildSpec.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// SpecStage is one processing step the user described, before it becomes nodes.
type SpecStage struct {
	// Name is a short label ("search sources", "generate audio").
	Name string `json:"name"`
	// Detail is the phrase from the intent this came from, so the user can see
	// WHY Studio believes this stage exists rather than trusting a summary.
	Detail string `json:"detail,omitempty"`
	// Parallel marks a stage the planner intends to fan out.
	Parallel bool `json:"parallel,omitempty"`
}

// SpecQuestion is a required detail the intent did not supply. It is a focused
// question rather than a validation error because at this point nothing is
// wrong — the user simply hasn't said yet.
type SpecQuestion struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	Why      string `json:"why"`
	// Blocker marks a detail without which nothing can be built (as opposed to
	// one where a sensible default exists).
	Blocker bool `json:"blocker"`
	// Field names the spec field an answer populates, so the UI can bind an
	// inline control instead of re-parsing free text.
	Field string `json:"field,omitempty"`
	// Options, when set, are the only valid answers — the UI renders a picker
	// instead of a free-text box. Used for channel choices, where a typo is
	// indistinguishable from a channel that isn't installed.
	Options []string `json:"options,omitempty"`
}

// BuildSpec is Studio's structured reading of an intent.
type BuildSpec struct {
	// Intent is the prose this was read from. Always preserved and editable —
	// ST-01 requires the original prompt never be lost behind its refinement.
	Intent string `json:"intent"`

	Trigger      string      `json:"trigger,omitempty"`  // schedule | channel | webhook | manual
	Schedule     string      `json:"schedule,omitempty"` // cron, when trigger=schedule
	ScheduleText string      `json:"schedule_text,omitempty"`
	Inputs       []string    `json:"inputs,omitempty"`
	Stages       []SpecStage `json:"stages,omitempty"`
	Outputs      []string    `json:"outputs,omitempty"`
	Delivery     []string    `json:"delivery,omitempty"`
	Integrations []string    `json:"integrations,omitempty"`
	// Security is the access this will need, stated plainly ("reads the web",
	// "sends messages on your behalf") so consent is an informed decision.
	Security  []string       `json:"security,omitempty"`
	Questions []SpecQuestion `json:"questions,omitempty"`
}

// Ready reports whether the spec has everything required to generate.
// Ready is exactly "nothing is blocking", and deliberately nothing more.
//
// It used to also require len(Stages) > 0, which is a second, invisible gate:
// the UI lists Blockers to tell the user what to fix, so a spec with an empty
// blocker list and Ready() == false is a disabled button with nothing to click
// and no explanation. Any reason to refuse must be expressed as a question, so
// the user can see it and act on it. deriveQuestions owns that judgement now.
func (s BuildSpec) Ready() bool {
	for _, q := range s.Questions {
		if q.Blocker {
			return false
		}
	}
	return true
}

// Blockers returns only the questions that prevent generation.
func (s BuildSpec) Blockers() []SpecQuestion {
	var out []SpecQuestion
	for _, q := range s.Questions {
		if q.Blocker {
			out = append(out, q)
		}
	}
	return out
}

var (
	reTimeOfDay  = regexp.MustCompile(`(?i)\b(?:at\s+)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)?\b`)
	reDomain     = regexp.MustCompile(`\b([a-z0-9][a-z0-9\-]{1,}\.(?:com|org|net|io|ai|co|dev|news|gov|edu))\b`)
	reEveryday   = regexp.MustCompile(`(?i)\b(?:every|each)\s+(weekday|day|morning|monday|tuesday|wednesday|thursday|friday|saturday|sunday|week|hour)\b`)
	reWhitespace = regexp.MustCompile(`\s+`)
	// A destination is a chat/channel id, an @handle, a #channel, or an email —
	// deliberately NOT "any digit", which a time of day satisfies.
	reDestination = regexp.MustCompile(`(?i)(chat\s*(id)?\s*[:#]?\s*-?\d{3,}|@[a-z0-9_.]{2,}|#[a-z0-9\-_]{2,}|-100\d+|\b[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}\b)`)
)

// ExtractBuildSpec reads an intent into a structured spec. Pure and
// deterministic: the same text always yields the same spec, so a user can learn
// what Studio keys on instead of guessing at a model's mood.
// ExtractBuildSpec reads an intent with no knowledge of the installation.
// Prefer ExtractBuildSpecFrom, which can also report the capabilities this
// workspace actually has.
func ExtractBuildSpec(intent string) BuildSpec {
	return ExtractBuildSpecFrom(intent, Catalog{})
}

// ExtractBuildSpecFrom reads an intent AGAINST the installed catalogue.
//
// Without the catalogue, "Capabilities" could only ever report a hardcoded list
// of seven brand names, so a prompt saying "using the trvl MCP travel tool"
// showed "not specified" — and then a question demanded to know what the agent
// should do, for a prompt that had just said. The panel exists to prove Studio
// understood the request; reporting "not specified" for something stated
// outright does the opposite.
func ExtractBuildSpecFrom(intent string, cat Catalog) BuildSpec {
	spec := BuildSpec{Intent: strings.TrimSpace(intent)}
	low := strings.ToLower(spec.Intent)
	if strings.TrimSpace(low) == "" {
		spec.Questions = append(spec.Questions, SpecQuestion{
			ID: "intent", Question: "What should this agent do?",
			Why: "Nothing can be built without a description of the job.", Blocker: true, Field: "intent",
		})
		return spec
	}

	spec.Trigger, spec.Schedule, spec.ScheduleText = extractTrigger(low)
	spec.Inputs = extractInputs(spec.Intent, low)
	spec.Stages = extractStages(low, spec.Intent)
	spec.Outputs = extractOutputs(low)
	spec.Delivery = extractDelivery(low)
	spec.Integrations = extractIntegrations(low, spec.Intent, cat)
	spec.Security = deriveSecurity(spec)
	spec.Questions = deriveQuestions(spec, low, cat)
	return spec
}

func extractTrigger(low string) (kind, cron, text string) {
	switch {
	case reEveryday.MatchString(low) || strings.Contains(low, "schedule") ||
		strings.Contains(low, "daily") || strings.Contains(low, "weekly"):
		kind = "schedule"
	// ConversationalIntent instead of another hand-rolled phrase list. The four
	// literals it replaces missed "answers user travel questions" — a wording so
	// close to "answers questions" that the panel calling it manually-triggered
	// looked arbitrary. One matcher, used by the trigger, the strategy advisor
	// and the planner, cannot drift out of agreement with itself.
	case ConversationalIntent(low):
		return "channel", "", "when a message arrives"
	case strings.Contains(low, "webhook") || strings.Contains(low, "http post"):
		return "webhook", "", "on an inbound HTTP request"
	default:
		return "manual", "", "when you run it"
	}

	// Resolve the day set and the time of day into a real cron expression, so
	// the spec commits to something checkable rather than repeating the prose.
	days := "*"
	dayText := "every day"
	if m := reEveryday.FindStringSubmatch(low); m != nil {
		switch strings.ToLower(m[1]) {
		case "weekday":
			days, dayText = "1-5", "every weekday"
		case "monday":
			days, dayText = "1", "every Monday"
		case "tuesday":
			days, dayText = "2", "every Tuesday"
		case "wednesday":
			days, dayText = "3", "every Wednesday"
		case "thursday":
			days, dayText = "4", "every Thursday"
		case "friday":
			days, dayText = "5", "every Friday"
		case "saturday":
			days, dayText = "6", "every Saturday"
		case "sunday":
			days, dayText = "0", "every Sunday"
		}
	}
	hour, minute, haveTime := extractTimeOfDay(low)
	if !haveTime {
		return "schedule", "", dayText + " (time not specified)"
	}
	cron = fmt.Sprintf("%d %d * * %s", minute, hour, days)
	text = fmt.Sprintf("%s at %02d:%02d (local time)", dayText, hour, minute)
	return "schedule", cron, text
}

// extractTimeOfDay finds a wall-clock time, correctly handling the am/pm cases
// that a naive parser gets wrong (12am is hour 0; 12pm is hour 12).
func extractTimeOfDay(low string) (hour, minute int, ok bool) {
	for _, m := range reTimeOfDay.FindAllStringSubmatch(low, -1) {
		h := atoiSafe(m[1])
		if h < 0 || h > 24 {
			continue
		}
		mm := 0
		if m[2] != "" {
			mm = atoiSafe(m[2])
		}
		switch strings.ToLower(m[3]) {
		case "pm":
			if h < 12 {
				h += 12
			}
		case "am":
			if h == 12 {
				h = 0
			}
		case "":
			// A bare number is only a time when it was written like one
			// ("7:00"); a bare "5" in "top 5 stories" must not become 05:00.
			if m[2] == "" {
				continue
			}
		}
		if h >= 0 && h < 24 && mm >= 0 && mm < 60 {
			return h, mm, true
		}
	}
	return 0, 0, false
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return -1
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func extractInputs(original, low string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" || seen[strings.ToLower(v)] {
			return
		}
		seen[strings.ToLower(v)] = true
		out = append(out, v)
	}
	for _, m := range reDomain.FindAllStringSubmatch(strings.ToLower(original), -1) {
		add(m[1])
	}
	// An explicit "Sources: A, B, C" list names publications that are not
	// domains ("MIT Technology Review"). Reading only domains silently dropped
	// a third of what the user asked for — and the spec exists precisely so
	// that kind of omission is visible before anything is built.
	for _, item := range sourcesListItems(original) {
		add(item)
	}
	if len(out) == 0 {
		for phrase, label := range map[string]string{
			"the web":      "web search results",
			"web search":   "web search results",
			"my calendar":  "your calendar",
			"my email":     "your email",
			"the database": "the database",
			"rss":          "RSS feeds",
		} {
			if strings.Contains(low, phrase) && !seen[label] {
				seen[label] = true
				out = append(out, label)
			}
		}
	}
	sort.Strings(out)
	return out
}

// stageMarkers map an observable phrase onto the stage it implies. Ordered
// scanning keeps the stage list in the order the user described the work.
var stageMarkers = []struct {
	markers []string
	name    string
}{
	{[]string{"search", "find", "look up", "gather", "collect"}, "search sources"},
	{[]string{"fetch", "download", "read the article", "scrape"}, "fetch content"},
	{[]string{"filter", "curate", "select the", "pick the", "rank"}, "curate results"},
	{[]string{"summar", "brief", "digest"}, "summarize"},
	{[]string{"translate"}, "translate"},
	{[]string{"podcast", "audio", "voice", "narrat"}, "generate audio"},
	{[]string{"chart", "graph", "visuali"}, "build a chart"},
	{[]string{"analy", "compare", "evaluate"}, "analyze"},
	{[]string{"write", "draft", "compose", "report"}, "write the output"},
}

// sourcesListItems extracts the comma-separated items of an explicit
// "sources:" / "from:" clause, stopping at sentence end.
func sourcesListItems(original string) []string {
	low := strings.ToLower(original)
	var start int
	for _, marker := range []string{"sources:", "source:", "from these sources:"} {
		if i := strings.Index(low, marker); i >= 0 {
			start = i + len(marker)
			break
		}
	}
	if start == 0 {
		return nil
	}
	rest := original[start:]
	if i := strings.IndexAny(rest, ".\n"); i >= 0 {
		// A domain's dot must not end the clause — only a dot followed by a
		// space or end-of-text is a sentence boundary.
		for i >= 0 && i+1 < len(rest) && rest[i] == '.' && rest[i+1] != ' ' {
			next := strings.IndexAny(rest[i+1:], ".\n")
			if next < 0 {
				i = -1
				break
			}
			i = i + 1 + next
		}
		if i >= 0 {
			rest = rest[:i]
		}
	}
	var out []string
	for _, part := range strings.Split(rest, ",") {
		part = strings.TrimSpace(strings.Trim(part, " \t\"'"))
		if part != "" && len(part) < 60 {
			out = append(out, part)
		}
	}
	return out
}

// extractStages matches against `low` but quotes from `orig`: matching wants a
// case-folded haystack, while the excerpt shown to the user should read back in
// the words and capitalisation they actually wrote. The two strings are the
// same length because ToLower is applied per byte to ASCII here, so an index
// into one is valid in the other — verified by the caller passing both from the
// same source string.
func extractStages(low, orig string) []SpecStage {
	// If casing changed the length (non-ASCII), indexes are not transferable;
	// quoting from `low` is then the only safe option and is merely less pretty.
	quote := orig
	if len(orig) != len(low) {
		quote = low
	}
	var out []SpecStage
	seen := map[string]bool{}
	for _, sm := range stageMarkers {
		for _, marker := range sm.markers {
			idx := strings.Index(low, marker)
			if idx < 0 || seen[sm.name] {
				continue
			}
			seen[sm.name] = true
			out = append(out, SpecStage{Name: sm.name, Detail: excerptAround(quote, idx)})
			break
		}
	}
	// Naming sources implies gathering them, even when no verb says so:
	// "Sources: A, B, C. Summarize the top stories" describes a fetch the user
	// considered too obvious to state.
	if !seen["search sources"] && (strings.Contains(low, "source") || len(reDomain.FindAllString(low, -1)) > 0) {
		out = append([]SpecStage{{Name: "search sources", Detail: "implied by the sources you named"}}, out...)
		seen["search sources"] = true
	}
	// Multiple named sources mean the first gathering stage fans out — the
	// user described parallel work even if they didn't use the word.
	if len(out) > 0 && (len(reDomain.FindAllString(low, -1)) > 1 || len(sourcesListItems(low)) > 1) {
		for i := range out {
			if out[i].Name == "search sources" || out[i].Name == "fetch content" {
				out[i].Parallel = true
				break
			}
		}
	}
	return out
}

// excerptAround returns a short window of the intent around a match, so the
// spec can show its evidence instead of asserting a stage exists.
//
// The window snaps OUT to whitespace at both ends. Cutting at a fixed offset
// produced quotes like "gent uses the travel mcp travel tool ... to fi", which
// reads as though Studio garbled the prompt — the opposite of the reassurance
// an evidence excerpt is for. Snapping outward also keeps the cut off the
// middle of a multi-byte rune, which fixed offsets do not guarantee.
//
// `s` should be the ORIGINAL text, not a lowercased copy: this is quoted back
// to the person who wrote it, and evidence that doesn't match what they typed
// invites them to doubt the match rather than trust it.
func excerptAround(s string, idx int) string {
	if idx < 0 || idx > len(s) {
		return ""
	}
	start := idx - 40
	if start <= 0 {
		start = 0
	} else {
		// Walk back to the space before the word the window landed inside.
		for start > 0 && !isSpaceByte(s[start-1]) {
			start--
		}
	}
	end := idx + 60
	if end >= len(s) {
		end = len(s)
	} else {
		for end < len(s) && !isSpaceByte(s[end]) {
			end++
		}
	}
	out := strings.TrimSpace(reWhitespace.ReplaceAllString(s[start:end], " "))
	if out == "" {
		return ""
	}
	// Mark the elisions, so a mid-sentence quote is visibly an excerpt rather
	// than looking like a sentence that begins and ends where it does.
	if start > 0 {
		out = "… " + out
	}
	if end < len(s) {
		out += " …"
	}
	return out
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func extractOutputs(low string) []string {
	var out []string
	for phrase, label := range map[string]string{
		"podcast": "an audio podcast", "audio": "an audio file",
		"summary": "a written summary", "brief": "a written briefing",
		"report": "a report", "chart": "a chart", "digest": "a digest",
	} {
		if strings.Contains(low, phrase) {
			out = append(out, label)
		}
	}
	sort.Strings(out)
	return dedupeNonEmpty(out)
}

func extractDelivery(low string) []string {
	var out []string
	for phrase, label := range map[string]string{
		"telegram": "Telegram", "slack": "Slack", "discord": "Discord",
		"email": "email", "webhook": "an HTTP webhook", "whatsapp": "WhatsApp",
	} {
		if strings.Contains(low, phrase) {
			out = append(out, label)
		}
	}
	sort.Strings(out)
	return out
}

// extractIntegrations reports the capabilities this intent will use.
//
// The brand list below is a nicety — it gives a friendly label for services
// people name colloquially. It is NOT the source of truth, because a fixed list
// can only ever recognise what someone thought to add to it. The catalogue is,
// so an MCP server or skill the user actually installed and named is reported
// whether or not anyone anticipated it.
func extractIntegrations(low, intent string, cat Catalog) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" && !seen[strings.ToLower(s)] {
			seen[strings.ToLower(s)] = true
			out = append(out, s)
		}
	}

	for phrase, label := range map[string]string{
		"notebooklm": "NotebookLM", "google drive": "Google Drive",
		"github": "GitHub", "notion": "Notion", "jira": "Jira",
		"spotify": "Spotify", "youtube": "YouTube",
	} {
		if strings.Contains(low, phrase) {
			add(label)
		}
	}

	// Anything the intent names that this workspace genuinely has. Reuses the
	// same matchers the planner uses to SELECT tools, so the spec panel and the
	// generated graph cannot disagree about what was asked for.
	//
	// Reported per SERVER, not per tool. This row is read by a person deciding
	// whether Studio understood them, and they wrote "the trvl travel tool", not
	// "mcp__trvl__travel"; a busy server would also flood the row with twenty
	// entries. It also keeps deriveSecurity's sentence readable, since that
	// renders as "signs in to <name>".
	named := map[string]bool{}
	for _, tool := range namedMCPTools(intent, cat) {
		named[tool] = true
	}
	for _, srv := range cat.MCP {
		for _, tool := range srv.Tools {
			if named[tool.Name] {
				add(strings.TrimSpace(srv.Server))
				break
			}
		}
	}
	for _, skill := range namedSkills(intent, cat) {
		add("skill " + skill)
	}

	sort.Strings(out)
	return out
}

// deriveSecurity states the access this agent will need in plain language.
// Phrased as what it can DO, not which capability token it holds — consent is
// only informed if the user understands the consequence.
func deriveSecurity(s BuildSpec) []string {
	var out []string
	if len(s.Inputs) > 0 || containsAnyStage(s, "search sources", "fetch content") {
		out = append(out, "reads pages from the public web")
	}
	if len(s.Delivery) > 0 {
		out = append(out, "sends messages on your behalf to "+strings.Join(s.Delivery, " and "))
	}
	if len(s.Integrations) > 0 {
		out = append(out, "signs in to "+strings.Join(s.Integrations, ", ")+" using stored credentials")
	}
	return out
}

func containsAnyStage(s BuildSpec, names ...string) bool {
	for _, st := range s.Stages {
		for _, n := range names {
			if st.Name == n {
				return true
			}
		}
	}
	return false
}

// deriveQuestions asks for what is missing. A question is a BLOCKER only when
// no sensible default exists — over-blocking turns a guided flow into an
// interrogation, and every question here has to earn its place.
func deriveQuestions(s BuildSpec, low string, cat Catalog) []SpecQuestion {
	var qs []SpecQuestion

	// A CONVERSATIONAL agent has no compile-time stages, by definition.
	//
	// The stage extractor looks for pipeline verbs — search, fetch, summarise,
	// deliver. An interactive agent decides what to do per message at RUNTIME, so
	// "a conversation agent that provides weather updates for a place or zipcode"
	// yields no stages while describing the job perfectly clearly. Demanding a
	// processing step there tells the user their request was unintelligible and
	// gives them no wording that could satisfy it, because no phrasing of a
	// conversational agent will ever produce pipeline stages.
	//
	// Keyed on the intent reading as interactive OR naming a capability, not on
	// integrations alone: an earlier version required a named capability, so this
	// same blocker returned the moment someone described a chat agent without
	// naming an MCP server — which is the ordinary case, not the exception.
	if len(s.Stages) == 0 && len(s.Integrations) == 0 && !ConversationalIntent(s.Intent) {
		qs = append(qs, SpecQuestion{
			ID: "stages", Question: "What should this agent actually do with the information?",
			Why:     "Studio could not identify any processing step, so there is nothing to build yet.",
			Blocker: true, Field: "intent",
		})
	}
	if s.Trigger == "schedule" && s.Schedule == "" {
		qs = append(qs, SpecQuestion{
			ID: "schedule_time", Question: "What time of day should this run?",
			Why:     "A scheduled agent needs an exact time — otherwise it cannot be put on a calendar.",
			Blocker: true, Field: "schedule",
		})
	}
	if len(s.Delivery) > 0 {
		// Naming a channel is not naming a destination, and this is the single
		// most common cause of a run that "succeeds" and reaches nobody.
		if !hasExplicitDestination(low) {
			qs = append(qs, SpecQuestion{
				ID: "destination", Question: "Where exactly should the result be delivered (which chat, channel, or address)?",
				Why:     "Studio knows the channel but not the destination, and a message with no destination is delivered to nobody.",
				Blocker: true, Field: "delivery",
			})
		}
	}
	if len(s.Delivery) == 0 && len(s.Outputs) > 0 {
		// A BLOCKER, and offered as a choice. This used to be an optional
		// free-text nudge, which meant an intent that named no channel was
		// answered by the generator picking one — in practice always the first
		// configured channel, so every agent delivered to the same place whether
		// or not that was wanted. Asking costs one click; guessing wrong sends
		// someone's output to the wrong audience.
		// Only ask when there is something to choose from. A blocker whose picker
		// is empty is the "disabled button with nothing to click" this file's own
		// Ready() comment warns about.
		if opts := channelOptions(cat); len(opts) > 0 {
			qs = append(qs, SpecQuestion{
				ID: "output_channel", Question: "Which channel should the result be delivered to?",
				Why:     "This produces something, but the intent doesn't say where it goes. Studio will not choose for you.",
				Blocker: true, Field: "delivery",
				Options: deliveryChannelOptions(cat),
			})
		} else {
			qs = append(qs, SpecQuestion{
				ID: "delivery", Question: "Where should the result go when it's ready?",
				Why:     "This produces something, but the intent doesn't say who receives it. No channels are configured yet.",
				Blocker: false, Field: "delivery",
			})
		}
	}
	// The inbound side of the same question: a channel-triggered agent has to
	// listen somewhere, and which one was previously inferred from whichever
	// channel happened to be configured.
	// Asked only when the answer is a real choice. With one channel configured
	// there is nothing to decide, and asking would be interrogation rather than
	// guidance.
	if s.Trigger == "channel" && !namesAnyChannel(low, cat) {
		if opts := channelOptions(cat); len(opts) > 1 {
			qs = append(qs, SpecQuestion{
				ID: "input_channel", Question: "Which channel should this agent listen on?",
				Why:     "It is triggered by incoming messages, but the intent doesn't say which channel they arrive on.",
				Blocker: true, Field: "trigger",
				Options: opts,
			})
		}
	}
	if len(s.Integrations) > 0 {
		qs = append(qs, SpecQuestion{
			ID: "integration_auth", Question: "Confirm the sign-in for " + strings.Join(s.Integrations, ", ") + ".",
			Why:     "These services need stored credentials before the workflow can use them.",
			Blocker: false, Field: "integrations",
		})
	}
	if len(s.Inputs) == 0 && containsAnyStage(s, "search sources", "fetch content") {
		qs = append(qs, SpecQuestion{
			ID: "sources", Question: "Which sources should it read?",
			Why:     "Narrowing the sources makes results far more predictable than an open web search.",
			Blocker: false, Field: "inputs",
		})
	}
	return qs
}

// hasExplicitDestination reports whether the intent names WHERE to deliver, as
// opposed to merely which channel. A naive "does it contain a digit" test is
// satisfied by the schedule ("8:00am"), which is how "delivered to nobody"
// became the most common silent failure.
func hasExplicitDestination(low string) bool {
	return reDestination.MatchString(low)
}

// ── Refinement ──────────────────────────────────────────────────────────────

// SpecChange is one difference between two specs, for the visible change
// summary ST-01 requires. A refinement the user cannot inspect is a refinement
// they cannot trust.
type SpecChange struct {
	Field  string `json:"field"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	Kind   string `json:"kind"` // added | removed | changed
}

// DiffSpecs reports what refinement changed. Used to prove a refine produced a
// materially different spec rather than a reworded one.
func DiffSpecs(before, after BuildSpec) []SpecChange {
	var out []SpecChange
	cmp := func(field, b, a string) {
		if b == a {
			return
		}
		switch {
		case b == "":
			out = append(out, SpecChange{Field: field, After: a, Kind: "added"})
		case a == "":
			out = append(out, SpecChange{Field: field, Before: b, Kind: "removed"})
		default:
			out = append(out, SpecChange{Field: field, Before: b, After: a, Kind: "changed"})
		}
	}
	cmp("trigger", before.Trigger, after.Trigger)
	cmp("schedule", before.Schedule, after.Schedule)
	cmpList := func(field string, b, a []string) {
		cmp(field, strings.Join(b, ", "), strings.Join(a, ", "))
	}
	cmpList("inputs", before.Inputs, after.Inputs)
	cmpList("outputs", before.Outputs, after.Outputs)
	cmpList("delivery", before.Delivery, after.Delivery)
	cmpList("integrations", before.Integrations, after.Integrations)
	cmpList("stages", stageNames(before), stageNames(after))
	return out
}

func stageNames(s BuildSpec) []string {
	out := make([]string, 0, len(s.Stages))
	for _, st := range s.Stages {
		out = append(out, st.Name)
	}
	return out
}

// MateriallyDifferent reports whether a refinement actually changed the BUILD,
// as opposed to only rewording the prose. ST-01 requires refine to produce a
// build-ready spec, and a refine that changes nothing structural should say so
// rather than implying progress.
func MateriallyDifferent(before, after BuildSpec) bool {
	for _, c := range DiffSpecs(before, after) {
		if c.Field != "intent" {
			return true
		}
	}
	// Resolving a blocker is material even when no field changed shape.
	return len(before.Blockers()) != len(after.Blockers())
}

// channelOptions lists the channels this workspace actually has, so the picker
// can never offer something that is not installed.
func channelOptions(cat Catalog) []string {
	out := make([]string, 0, len(cat.Channels))
	for _, ch := range cat.Channels {
		if c := strings.TrimSpace(ch); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// NoDeliveryChannel is the explicit "don't send this anywhere" answer. Present
// as a real choice because "returns its answer to whoever asked" is a perfectly
// good design, and without it the only way past the question is to name a
// channel the user does not want.
const NoDeliveryChannel = "none"

func deliveryChannelOptions(cat Catalog) []string {
	return append(channelOptions(cat), NoDeliveryChannel)
}

// namesAnyChannel reports whether the intent already names one of the installed
// channels, in which case there is nothing to ask.
func namesAnyChannel(low string, cat Catalog) bool {
	for _, ch := range cat.Channels {
		c := strings.ToLower(strings.TrimSpace(ch))
		if c != "" && strings.Contains(low, c) {
			return true
		}
	}
	return false
}

// ApplyChannelAnswers forces the channels the operator explicitly chose onto a
// freshly generated draft.
//
// The answer is applied deterministically rather than left to the builder model.
// The model is TOLD the answer (it appears under "Answers to clarifying
// questions"), but "told" is not "guaranteed": the previous behaviour was that
// whichever channel the model felt like naming became the delivery target, which
// is exactly how every generated agent ended up on the same channel regardless
// of what was asked for.
func ApplyChannelAnswers(d *Draft, answers map[string]string) {
	if d == nil || len(answers) == 0 {
		return
	}
	if v := strings.TrimSpace(answers["output_channel"]); v != "" {
		if strings.EqualFold(v, NoDeliveryChannel) {
			// An explicit "nowhere" — the agent returns its answer to whoever
			// asked instead of broadcasting it.
			d.Channels = nil
		} else {
			d.Channels = []string{strings.ToLower(v)}
		}
	}
	if v := strings.TrimSpace(answers["input_channel"]); v != "" && !strings.EqualFold(v, NoDeliveryChannel) {
		c := strings.ToLower(v)
		if !containsFold(d.Channels, c) {
			d.Channels = append(d.Channels, c)
		}
	}
}
