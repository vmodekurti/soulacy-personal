package studio

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// RepairWiring runs the full deterministic data-flow repair over a draft: fill
// empty required tool args from same-named upstream outputs (AutoWire), then
// reconcile dangling template-variable references to the right upstream output
// (ReconcileVars) — e.g. a node referencing {{ .date_str }} when the producing
// step actually outputs date_info. Returns the total number of fixes applied.
// Pure except for mutating draft.
func RepairWiring(draft *Draft, cat Catalog) int {
	if draft == nil {
		return 0
	}
	// Output vars first: every executable node must have one, or its result is
	// dropped (applyFlowResult skips Output=="") and downstream wires/templates
	// read null. The other passes rely on these names, so assign before them.
	n := ensureOutputVars(&draft.Flow)
	// Where a fan-out reconverges. Without it each branch walks to the end of
	// the graph, so the shared node runs once PER BRANCH and each copy sees only
	// its own branch's variables — the failure that made a generated
	// three-specialist workflow die on <.risk_analysis> inside the fundamentals
	// branch, after passing every static check. See joinnode.go.
	n += InferJoinNodes(&draft.Flow)
	n += normalizePlatformToolChoices(draft, cat)
	n += normalizeKBWriteInputs(draft, cat)
	n += AutoWire(draft, cat) + ReconcileVars(draft) + reconcileFieldRefs(draft) +
		fixDoubledSegmentPaths(draft) + fixWholeValueInterpolations(draft) + fixTemplateTypos(draft)
	// Auto-set a block's timeout from the wait it already declares (e.g. a poll
	// node with max_wait:1200) so long-running steps work without the developer
	// also setting the per-node timeout by hand. Runs before portization so it
	// reads the node's original argument object. Only fills an empty Timeout.
	n += deriveNodeTimeouts(draft)
	// Final step: lower the now-correct whole-value handoff templates to typed
	// port wires (the default, template-free handoff). The reconcile passes above
	// have already pointed each ref at the right producer/field; PortizeHandoffs
	// turns those references into structural wires the runtime resolves without
	// templating, retiring the whole template-handoff bug class on the default
	// authoring path. Idempotent, so safe on every RepairWiring call.
	n += PortizeHandoffs(draft, cat)
	return n
}

// normalizeKBWriteInputs turns common LLM-generated "store this in KB" handoffs
// into the real kb_write contract. kb_write is intentionally strict at runtime:
// it accepts JSON args with at least {"kb": "...", "content": "..."}. Models
// often wire a tagger/agent reply directly as `{{ .tagged_data }}` or a fenced
// JSON blob, which can never parse as tool args. This deterministic repair makes
// that class of generated workflow runnable without weakening the tool.
func normalizeKBWriteInputs(draft *Draft, cat Catalog) int {
	if draft == nil || len(draft.Flow.Nodes) == 0 {
		return 0
	}
	kb := defaultKnowledgeBase(*draft, cat)
	fixed := 0
	for i := range draft.Flow.Nodes {
		n := &draft.Flow.Nodes[i]
		if strings.TrimSpace(n.Kind) != sdkr.FlowNodeTool || strings.TrimSpace(n.Tool) != "kb_write" {
			continue
		}
		raw := strings.TrimSpace(n.Input)
		obj, ok := decodeInputObject(raw)
		if !ok {
			if raw == "" {
				raw = "{{ .trigger.text }}"
			}
			obj = map[string]any{"content": raw}
		}
		changed := !ok
		if strings.TrimSpace(stringish(obj["kb"])) == "" && kb != "" {
			obj["kb"] = kb
			changed = true
		}
		if strings.TrimSpace(stringish(obj["content"])) == "" {
			if raw != "" && !ok {
				obj["content"] = json.RawMessage(jsonSafeTemplate(raw))
			} else if s := bestUpstreamContentRef(*draft, i); s != "" {
				obj["content"] = json.RawMessage(jsonSafeTemplate(s))
			}
			changed = true
		}
		if strings.TrimSpace(stringish(obj["title"])) == "" {
			obj["title"] = "Stored artifact"
			changed = true
		}
		if strings.TrimSpace(stringish(obj["source"])) == "" {
			obj["source"] = "{{ .trigger.text }}"
			changed = true
		}
		if changed {
			b, err := json.Marshal(obj)
			if err == nil {
				n.Input = string(b)
				fixed++
			}
		}
	}
	return fixed
}

func defaultKnowledgeBase(draft Draft, cat Catalog) string {
	for _, kb := range draft.Knowledge {
		if strings.TrimSpace(kb) != "" {
			return strings.TrimSpace(kb)
		}
	}
	for _, kb := range cat.KnowledgeBases {
		if strings.TrimSpace(kb.Name) != "" {
			return strings.TrimSpace(kb.Name)
		}
	}
	return ""
}

func bestUpstreamContentRef(draft Draft, consumerIdx int) string {
	nodes := draft.Flow.Nodes
	if consumerIdx < 0 || consumerIdx >= len(nodes) {
		return ""
	}
	consumer := nodes[consumerIdx]
	anc := ancestors(draft.Flow.Edges, nodes)
	preferred := []string{"tagged", "tagged_data", "tagged_artifacts", "content", "contents", "fetched_content", "text", "message"}
	outputByName := map[string]string{}
	for _, n := range nodes {
		out := strings.TrimSpace(n.Output)
		if out == "" || n.ID == consumer.ID {
			continue
		}
		if len(draft.Flow.Edges) > 0 {
			if a := anc[consumer.ID]; a == nil || !a[n.ID] {
				continue
			}
		}
		outputByName[strings.ToLower(out)] = out
	}
	for _, p := range preferred {
		if out := outputByName[p]; out != "" {
			return "{{ ." + out + " }}"
		}
	}
	for _, n := range nodes {
		if strings.TrimSpace(n.Output) == "" || n.ID == consumer.ID {
			continue
		}
		if len(draft.Flow.Edges) > 0 {
			if a := anc[consumer.ID]; a == nil || !a[n.ID] {
				continue
			}
		} else if consumerIdx > 0 {
			for j := 0; j < consumerIdx; j++ {
				if nodes[j].ID == n.ID {
					return "{{ ." + strings.TrimSpace(n.Output) + " }}"
				}
			}
			continue
		}
		return "{{ ." + strings.TrimSpace(n.Output) + " }}"
	}
	return ""
}

func stringish(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

func jsonSafeTemplate(s string) string {
	s = strings.TrimSpace(s)
	if m := wholeValueTemplateRe.FindStringSubmatch(s); m != nil {
		ref := "." + m[1] + m[2]
		return "{{ toJson " + ref + " }}"
	}
	b, _ := json.Marshal(s)
	return string(b)
}

// dottedPathRe matches a run of dotted field accesses inside a template, e.g.
// `.notebook_id.notebook_id.id`.
var dottedPathRe = regexp.MustCompile(`(?:\.[A-Za-z_][A-Za-z0-9_]*)+`)

// fixDoubledSegmentPaths repairs the systemic "over-reached doubled segment"
// template bug: a model wires a handoff as {{ .notebook_id.notebook_id.id }},
// where `.notebook_id.notebook_id` is already the scalar id and the trailing `.id`
// makes the runtime crash ("can't evaluate field id in type interface {}"). A
// CONSECUTIVE repeated segment (`.X.X`) is the unmistakable signal the model
// duplicated and then kept reaching — so we truncate everything after the doubled
// pair, leaving {{ .notebook_id.notebook_id }}. Go's RE2 has no backreferences, so
// the duplicate is found by walking segments. Applied only inside {{ … }} actions
// (node inputs + edge predicates); a path with no consecutive duplicate is left
// untouched. Returns the number of paths rewritten.
func fixDoubledSegmentPaths(draft *Draft) int {
	if draft == nil {
		return 0
	}
	fixOne := func(s string) (string, bool) {
		if !strings.Contains(s, "{{") {
			return s, false
		}
		changed := false
		out := templateActionRe.ReplaceAllStringFunc(s, func(act string) string {
			return dottedPathRe.ReplaceAllStringFunc(act, func(path string) string {
				segs := strings.Split(strings.TrimPrefix(path, "."), ".")
				for i := 0; i+1 < len(segs); i++ {
					// A consecutive duplicate WITH trailing segments = over-reach.
					if segs[i] == segs[i+1] && i+2 < len(segs) {
						segs = segs[:i+2]
						changed = true
						break
					}
				}
				return "." + strings.Join(segs, ".")
			})
		})
		return out, changed
	}
	fixed := 0
	for i := range draft.Flow.Nodes {
		if v, c := fixOne(draft.Flow.Nodes[i].Input); c {
			draft.Flow.Nodes[i].Input = v
			fixed++
		}
	}
	for i := range draft.Flow.Edges {
		if v, c := fixOne(draft.Flow.Edges[i].If); c {
			draft.Flow.Edges[i].If = v
			fixed++
		}
	}
	return fixed
}

// varSanitizeRe matches characters not allowed in a flow-var identifier.
var varSanitizeRe = regexp.MustCompile(`[^A-Za-z0-9_]+`)

// ensureOutputVars guarantees every EXECUTABLE node (tool/agent/python) has an
// Output var, so its result is stored in the flow vars and any downstream step —
// a {{ .var }} template OR a typed-port wire — can read it. Without this, a node
// with a blank output var produces a value the engine drops (applyFlowResult
// skips Output==""), so a wired consumer receives null (the "{\"urls\": null}"
// handoff failure). Defaults the var to a sanitized form of the node id, kept
// unique; never overwrites a var the node already has; skips structural
// (trigger/exit/branch) nodes, which produce no value. Returns the count assigned.
func ensureOutputVars(f *Flow) int {
	if f == nil {
		return 0
	}
	used := map[string]bool{}
	for _, n := range f.Nodes {
		if v := strings.TrimSpace(n.Output); v != "" {
			used[v] = true
		}
	}
	fixed := 0
	for i := range f.Nodes {
		n := &f.Nodes[i]
		if sdkr.IsStructuralKind(n.Kind) || strings.TrimSpace(n.Output) != "" {
			continue
		}
		base := strings.Trim(varSanitizeRe.ReplaceAllString(strings.TrimSpace(n.ID), "_"), "_")
		if base == "" {
			base = "out"
		}
		name := base
		for k := 2; used[name]; k++ {
			name = base + "_" + itoa(k)
		}
		n.Output = name
		used[name] = true
		fixed++
	}
	return fixed
}

// wholeValueKeyRe matches a JSON object member whose value is EXACTLY one
// template action and nothing else, capturing the key and the inner expression:
// `"urls": "{{ .urls }}"` -> key="urls", inner=".urls". The inner [^{}"]+ forbids
// other text/quotes, so prose like "Summarize {{ .x }}" (surrounding text) and a
// quote immediately before {{ are required, so only WHOLE-value members match.
var wholeValueKeyRe = regexp.MustCompile(`"([A-Za-z_][A-Za-z0-9_]*)"\s*:\s*"\{\{\s*([^{}"]+?)\s*\}\}"`)

// bareVarRe matches a single whole flow var (".urls"), not a field path
// (".notebook.id") — the latter is already a scalar and renders fine quoted.
var bareVarRe = regexp.MustCompile(`^\.[A-Za-z_][A-Za-z0-9_]*$`)

// isScalarKey reports whether a JSON key names a single scalar value (an id,
// name, status, url, …). Passing a whole object into such a key means "that
// object's <key>", which is a FIELD-level fix (.notebook.id), not a JSON dump —
// so fixWholeValueInterpolations leaves these for the field-ref/LLM repair.
func isScalarKey(k string) bool {
	switch strings.ToLower(k) {
	case "id", "name", "title", "status", "url", "uri", "key", "token", "query":
		return true
	}
	lk := strings.ToLower(k)
	return strings.HasSuffix(lk, "_id") || strings.HasSuffix(lk, "id")
}

// fixWholeValueInterpolations is the framework-level fix for the nastiest
// data-handoff bug: a tool/python step whose input passes a WHOLE upstream
// COLLECTION through as a quoted interpolation — `{"urls": "{{ .urls }}"}`. When
// that value is a list/object, Go's text/template renders it as
// `[map[title:… url:…] …]` (Go syntax, NOT JSON), so the consuming step receives
// garbage and fails ("No valid URLs to process"). The user should never have to
// know to type `toJson`/unquote — so we rewrite it automatically:
//
//	"urls": "{{ .urls }}"   ->   "urls": {{ toJson .urls }}
//
// which emits real JSON. It fires ONLY when the value is a bare WHOLE var (".urls",
// not a field path) AND the destination key is NOT scalar-ish (so `"id":
// "{{ .notebook }}"` is left for the field-level repair to make `.notebook.id`).
// Applied only to tool/python inputs (JSON); agent prompts (prose) are untouched.
// Idempotent. Returns the number of members rewritten.
func fixWholeValueInterpolations(draft *Draft) int {
	if draft == nil {
		return 0
	}
	fixed := 0
	for i := range draft.Flow.Nodes {
		n := &draft.Flow.Nodes[i]
		if n.Kind != sdkr.FlowNodeTool && n.Kind != sdkr.FlowNodePython {
			continue
		}
		if !strings.Contains(n.Input, "{{") {
			continue
		}
		out := wholeValueKeyRe.ReplaceAllStringFunc(n.Input, func(m string) string {
			sub := wholeValueKeyRe.FindStringSubmatch(m)
			if sub == nil {
				return m
			}
			key := sub[1]
			inner := strings.TrimSpace(sub[2])
			// Normalize: strip an existing leading toJson/json so we don't double it.
			inner = strings.TrimSpace(strings.TrimPrefix(inner, "toJson"))
			inner = strings.TrimSpace(strings.TrimPrefix(inner, "json"))
			// Only a bare WHOLE var into a NON-scalar key. A field path
			// (.notebook.id) is already a scalar and fine; a scalar key wants a
			// field, not a JSON dump.
			if !bareVarRe.MatchString(inner) || isScalarKey(key) {
				return m
			}
			fixed++
			return `"` + key + `": {{ toJson ` + inner + ` }}`
		})
		n.Input = out
	}
	return fixed
}

// objectFieldRefs are bare field names a model commonly references as a LEADING
// template ref (e.g. {{ .id }}) when it actually means "the <field> of an object
// produced upstream" (e.g. {{ .notebook.id }}). These are the dangling refs that
// hard-block an otherwise-correct create→use sequence. "Format" is deliberately
// excluded — it has no obvious object owner and is usually a hallucination, left
// for the LLM/user.
var objectFieldRefs = map[string]bool{
	"id": true, "url": true, "uri": true, "status": true,
	"title": true, "name": true, "audio_url": true, "audiourl": true,
}

// reconcileFieldRefs rewrites a DANGLING leading bare-field ref ({{ .id }}, with
// no node producing "id") to {{ .<producer>.id }}, choosing <producer> as the
// EARLIEST upstream (ancestor) producer — i.e. the create-style step that owns
// the object. This deterministically fixes the create→add→use template dance
// (where every later step references the created object's {{ .id }}) without the
// LLM. Conservative: only curated object-field names, only when the earliest
// ancestor producer is UNIQUE (no tie), so it never guesses on an ambiguous
// graph. Returns the number of references rewritten.
func reconcileFieldRefs(draft *Draft) int {
	if draft == nil || len(draft.Flow.Nodes) == 0 {
		return 0
	}
	producer := map[string]string{} // output var -> producing node id
	for _, n := range draft.Flow.Nodes {
		if v := strings.TrimSpace(n.Output); v != "" {
			producer[v] = n.ID
		}
	}
	anc := ancestors(draft.Flow.Edges, draft.Flow.Nodes)

	fixed := 0
	for i := range draft.Flow.Nodes {
		n := &draft.Flow.Nodes[i]
		for _, ref := range referencedVars(n.Input) {
			if _, produced := producer[ref]; produced {
				continue // not dangling
			}
			if !objectFieldRefs[strings.ToLower(ref)] {
				continue // not a known object field — leave for LLM/user
			}
			// Candidate ancestor producers, ranked by how early they run (fewest
			// ancestors = closest to the trigger = the create step).
			type cand struct {
				varName string
				depth   int
			}
			var cands []cand
			for v, pid := range producer {
				if a := anc[n.ID]; a != nil && a[pid] {
					cands = append(cands, cand{v, len(anc[pid])})
				}
			}
			if len(cands) == 0 {
				continue
			}
			sort.Slice(cands, func(a, b int) bool {
				if cands[a].depth != cands[b].depth {
					return cands[a].depth < cands[b].depth
				}
				return cands[a].varName < cands[b].varName
			})
			if len(cands) > 1 && cands[0].depth == cands[1].depth {
				continue // ambiguous earliest producer — don't guess
			}
			if v := prefixFieldRef(n.Input, ref, cands[0].varName); v != n.Input {
				n.Input = v
				fixed++
			}
		}
	}
	return fixed
}

// prefixFieldRef rewrites a LEADING field ref `.<field>` to `.<prod>.<field>`,
// only inside {{ … }} actions (so real text/URLs are never touched) and only
// when the dot is at an action boundary (after {{ , whitespace, '(' or '|'),
// which is exactly where a leading ref appears — never the trailing `.id` of an
// existing chain like `.notebook.id`.
func prefixFieldRef(input, field, prod string) string {
	if !strings.Contains(input, "{{") {
		return input
	}
	inner := regexp.MustCompile(`(^|[\s({|])\.` + regexp.QuoteMeta(field) + `\b`)
	return templateActionRe.ReplaceAllStringFunc(input, func(act string) string {
		return inner.ReplaceAllString(act, `${1}.`+prod+`.`+field)
	})
}

// templateActionRe matches a {{ … }} template action (no nested braces).
var templateActionRe = regexp.MustCompile(`\{\{[^{}]*\}\}`)

// doubleDotRe matches a run of 2+ dots — only ever a typo inside a template
// action (e.g. {{ ..date_info }}); valid field chains use single dots.
var doubleDotRe = regexp.MustCompile(`\.\.+`)

// fixTemplateTypos collapses accidental double-dots ({{ ..x }} → {{ .x }}) and
// is applied ONLY inside {{ … }} actions, so real text (URLs, "…", file paths)
// is never touched. Runs over node inputs and edge predicates. Returns the
// number of strings it changed.
func fixTemplateTypos(draft *Draft) int {
	clean := func(s string) (string, bool) {
		if !strings.Contains(s, "{{") {
			return s, false
		}
		out := templateActionRe.ReplaceAllStringFunc(s, func(act string) string {
			return doubleDotRe.ReplaceAllString(act, ".")
		})
		return out, out != s
	}
	fixed := 0
	for i := range draft.Flow.Nodes {
		if v, changed := clean(draft.Flow.Nodes[i].Input); changed {
			draft.Flow.Nodes[i].Input = v
			fixed++
		}
	}
	for i := range draft.Flow.Edges {
		if v, changed := clean(draft.Flow.Edges[i].If); changed {
			draft.Flow.Edges[i].If = v
			fixed++
		}
	}
	return fixed
}

// ReconcileVars rewrites a node's dangling {{ .X }} references — vars that NO
// node produces — to the best-matching variable produced by an upstream
// (ancestor) step. "Best match" is decided by shared name tokens (split on "_"),
// and only applied when exactly ONE ancestor output is the clear winner, so an
// ambiguous case is left for the focused-LLM repair or the user rather than
// guessed wrong. Fixes the common model slip where a step invents a slightly
// different variable name than the one its producer emits. Returns the count of
// references rewritten.
func ReconcileVars(draft *Draft) int {
	if draft == nil || len(draft.Flow.Nodes) == 0 {
		return 0
	}
	producer := map[string]string{} // output var -> producing node id
	for _, n := range draft.Flow.Nodes {
		if v := strings.TrimSpace(n.Output); v != "" {
			producer[v] = n.ID
		}
	}
	anc := ancestors(draft.Flow.Edges, draft.Flow.Nodes)

	fixed := 0
	for i := range draft.Flow.Nodes {
		n := &draft.Flow.Nodes[i]
		for _, ref := range referencedVars(n.Input) {
			if _, produced := producer[ref]; produced {
				continue // the var exists; ordering issues are handled elsewhere
			}
			// Dangling ref: gather candidate vars produced by this node's ancestors.
			var cand []string
			for v, pid := range producer {
				if a := anc[n.ID]; a != nil && a[pid] {
					cand = append(cand, v)
				}
			}
			if best := bestTokenMatch(ref, cand); best != "" {
				n.Input = rewriteVarRef(n.Input, ref, best)
				fixed++
			}
		}
	}
	return fixed
}

// bestTokenMatch returns the single candidate sharing the most name tokens with
// ref (tokens split on "_"), requiring a unique winner with at least one shared
// token. Returns "" when there's no candidate, no overlap, or a tie — those are
// left for the LLM/user rather than guessed.
func bestTokenMatch(ref string, candidates []string) string {
	refTokens := tokenizeVar(ref)

	// First preference: a candidate whose tokens are a SUBSET of the ref's
	// tokens — i.e. the producer name is "contained in" the reference, like
	// producer `notebook` for ref `notebook_id`. This cleanly resolves the
	// common "X_id refers to the X output" case and breaks ties against a
	// sibling like `notebook_params` (which has an extra non-ref token). If
	// exactly one subset candidate has the max overlap, take it.
	subBest, subScore, subTie := "", 0, false
	for _, c := range candidates {
		ct := tokenizeVar(c)
		if !isSubset(ct, refTokens) {
			continue
		}
		score := len(ct)
		if score > subScore {
			subScore, subBest, subTie = score, c, false
		} else if score == subScore && score > 0 {
			subTie = true
		}
	}
	if subScore > 0 && !subTie {
		return subBest
	}

	// Fallback: the candidate sharing the most tokens, if it's a unique winner.
	bestVar, bestScore, tie := "", 0, false
	for _, c := range candidates {
		score := 0
		for t := range tokenizeVar(c) {
			if refTokens[t] {
				score++
			}
		}
		if score > bestScore {
			bestScore, bestVar, tie = score, c, false
		} else if score == bestScore && score > 0 {
			tie = true
		}
	}
	if bestScore == 0 || tie {
		return ""
	}
	return bestVar
}

// isSubset reports whether every token in a is also in b.
func isSubset(a, b map[string]bool) bool {
	if len(a) == 0 {
		return false
	}
	for t := range a {
		if !b[t] {
			return false
		}
	}
	return true
}

func tokenizeVar(s string) map[string]bool {
	out := map[string]bool{}
	for _, t := range strings.Split(strings.ToLower(s), "_") {
		if t != "" {
			out[t] = true
		}
	}
	return out
}

// rewriteVarRef replaces template references to .from with .to in s — matching
// the dotted identifier so ".date_str" becomes ".date_info" inside any
// {{ ... }} expression, without touching unrelated text.
func rewriteVarRef(s, from, to string) string {
	re := regexp.MustCompile(`\.` + regexp.QuoteMeta(from) + `\b`)
	return re.ReplaceAllString(s, "."+to)
}

// AutoWire deterministically connects an upstream step's output into a later
// step's REQUIRED tool argument when that argument is empty/placeholder and an
// earlier step produces an output variable of the same name (local-first pivot,
// Story #10). This is the automatic repair for the most common generation
// failure — a tool called with notebook_id: <no value> — without asking the
// model to regenerate anything. It is conservative: it only fills a required
// arg that is currently empty/placeholder, and only from a producer that runs
// BEFORE the consumer (an ancestor, or any earlier node when no edges exist).
// Returns the number of args it wired. Pure except for mutating draft.
func AutoWire(draft *Draft, cat Catalog) int {
	if draft == nil || len(draft.Flow.Nodes) == 0 {
		return 0
	}

	// Required args per MCP tool, from the catalog param hints.
	required := map[string][]string{}
	for tool, reqs := range builtinRequiredToolArgs() {
		required[tool] = reqs
	}
	for _, srv := range cat.MCP {
		for _, t := range srv.Tools {
			if name := strings.TrimSpace(t.Name); name != "" {
				required[name] = requiredParams(t.Params)
			}
		}
	}

	// producer: output var -> producing node id.
	producer := map[string]string{}
	for _, n := range draft.Flow.Nodes {
		if v := strings.TrimSpace(n.Output); v != "" {
			producer[v] = n.ID
		}
	}
	anc := ancestors(draft.Flow.Edges, draft.Flow.Nodes)
	hasEdges := len(draft.Flow.Edges) > 0

	wired := 0
	for i := range draft.Flow.Nodes {
		n := &draft.Flow.Nodes[i]
		if n.Kind != "tool" {
			continue
		}
		reqs := required[strings.TrimSpace(n.Tool)]
		if len(reqs) == 0 {
			continue
		}
		for _, arg := range reqs {
			if argFilled(n.Input, arg) {
				continue
			}
			prod, ok := producer[arg] // a step that outputs a var named exactly like the arg
			if !ok || prod == n.ID {
				continue
			}
			// Only wire from a step that runs before this one.
			if hasEdges {
				if a := anc[n.ID]; a == nil || !a[prod] {
					continue
				}
			}
			if setInputArg(n, arg, "{{ ."+arg+" }}") {
				wired++
			}
		}
	}
	return wired
}

// setInputArg sets arg -> value in a node's Input JSON object, preserving any
// existing keys. Returns true on success. If the Input isn't a JSON object it is
// replaced with a minimal one containing just this arg (the node had no usable
// input anyway).
func setInputArg(n *sdkr.FlowNode, arg, value string) bool {
	m := map[string]any{}
	if s := strings.TrimSpace(n.Input); s != "" {
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			m = map[string]any{}
		}
	}
	m[arg] = value
	b, err := json.Marshal(m)
	if err != nil {
		return false
	}
	n.Input = string(b)
	return true
}
