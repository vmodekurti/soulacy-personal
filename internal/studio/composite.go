package studio

import (
	"fmt"
	"sort"
	"strings"

	"github.com/soulacy/soulacy/internal/studio/codeclass"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// CompositeBlock is a COARSE, reusable workflow block: a single node that
// encapsulates a whole multi-step "dance" (e.g. NotebookLM create → add sources
// → poll → generate audio) behind ONE typed input/output contract.
//
// It is the heart of the Phase-2 authoring simplification (docs/STUDIO_HANDOFF.md
// §6): instead of the LLM wiring 4+ fine-grained nodes together with brittle Go
// templates ({{ .notebook.id }}) — the single biggest source of generation bugs —
// the multi-step logic lives INSIDE one inline-Python block, written and TESTED
// ONCE here. The author drops one block, wires its ports, and never touches the
// internal handoffs. Polling, retries, and error handling are CODE, not a fragile
// max_iterations back-edge.
//
// A CompositeBlock materialises into a normal kind=python FlowNode (typed ports +
// inline Code), so it rides the existing runtime, classifier, consent, and
// test-bench machinery with zero new execution primitives.
type CompositeBlock struct {
	// ID is the stable catalog id (e.g. "notebooklm_podcast").
	ID string `json:"id"`
	// Name is the human-facing palette label.
	Name string `json:"name"`
	// Summary is a one-line description of what the block accomplishes.
	Summary string `json:"summary"`
	// Keywords trigger composite-block grounding when they appear in the intent
	// (lowercased, substring match).
	Keywords []string `json:"keywords"`
	// Requirements is the human-readable note about what the block needs to run
	// (e.g. a CLI installed, an env var). Surfaced so the author understands the
	// consent prompt the block will trigger.
	Requirements string `json:"requirements,omitempty"`
	// NodeID is the default flow-node id used when the block is dropped onto a
	// canvas (verb-style, e.g. "make_podcast").
	NodeID string `json:"node_id"`
	// OutputVar is the flow var the materialised node writes its result object to.
	OutputVar string `json:"output_var"`
	// Inputs / Outputs are the typed ports that form the block's PUBLIC contract.
	// Everything between them is encapsulated.
	Inputs  []sdkr.FlowPort `json:"inputs"`
	Outputs []sdkr.FlowPort `json:"outputs"`
	// Code is the inline Python (def run(inputs)) that implements the whole dance.
	Code string `json:"code"`
}

// MaterializeNode builds the single kind=python FlowNode that realises this
// composite block: typed ports + inline Code + classifier-derived Requires. The
// node passes reasoning.CompileFlow on its own and wires into a larger graph via
// its declared ports, exactly like any other node. X/Y default to 0 (the caller
// or canvas positions it).
func (b CompositeBlock) MaterializeNode() sdkr.FlowNode {
	return sdkr.FlowNode{
		ID:          b.NodeID,
		Kind:        sdkr.FlowNodePython,
		Description: b.Summary,
		Code:        b.Code,
		Output:      b.OutputVar,
		Inputs:      append([]sdkr.FlowPort(nil), b.Inputs...),
		Outputs:     append([]sdkr.FlowPort(nil), b.Outputs...),
		// Be honest about capabilities up front (the dance shells out to a CLI →
		// "system"), so the canvas chips and the save/consent gate are correct the
		// moment the block is dropped, before any edit.
		Requires: codeclass.Classify(b.Code).Requires,
	}
}

// CompositeBlocks returns the curated composite-block catalog. Pure +
// deterministic. Phase 2 ships ONE proven block (the NotebookLM podcast) as the
// template for the library; more coarse blocks slot in here.
func CompositeBlocks() []CompositeBlock {
	return []CompositeBlock{
		{
			ID:           "notebooklm_podcast",
			Name:         "NotebookLM Podcast",
			Summary:      "Turn a list of source URLs into a NotebookLM audio overview (podcast) and return the audio link.",
			Keywords:     []string{"notebooklm", "notebook lm", "audio overview", "podcast", "audio summary"},
			Requirements: "Requires the NotebookLM CLI installed (binary `nlm`, or set NLM_BIN). Shells out to it, so the block is consent-gated (system capability).",
			NodeID:       "make_podcast",
			OutputVar:    "podcast",
			Inputs: []sdkr.FlowPort{
				{Name: "urls", Type: "string[]", Label: "Source URLs"},
				{Name: "title", Type: "string", Label: "Notebook title"},
			},
			Outputs: []sdkr.FlowPort{
				// Field decouples the wire name from the result field so a downstream
				// step can wire `audio_url` straight from result.audio_url with no
				// template (Phase-1 typed ports).
				{Name: "audio_url", Type: "string", Label: "Podcast audio URL", Field: "audio_url"},
			},
			Code: notebookLMPodcastCode,
		},
	}
}

// CompositeBlockByID returns the catalog block with the given id (case/space
// tolerant), or ok=false when none matches.
func CompositeBlockByID(id string) (CompositeBlock, bool) {
	want := strings.ToLower(strings.TrimSpace(id))
	for _, b := range CompositeBlocks() {
		if strings.ToLower(b.ID) == want {
			return b, true
		}
	}
	return CompositeBlock{}, false
}

// MatchCompositeBlocks returns the catalog blocks whose keywords appear in the
// intent, ranked by keyword-hit count. Pure + deterministic; returns at most
// `max` (<=0 = no limit).
func MatchCompositeBlocks(intent string, max int) []CompositeBlock {
	li := strings.ToLower(intent)
	type scored struct {
		b     CompositeBlock
		score int
	}
	var hits []scored
	for _, b := range CompositeBlocks() {
		score := 0
		for _, kw := range b.Keywords {
			if kw != "" && strings.Contains(li, strings.ToLower(kw)) {
				score++
			}
		}
		if score == 0 {
			continue
		}
		hits = append(hits, scored{b: b, score: score})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	var out []CompositeBlock
	for _, h := range hits {
		out = append(out, h.b)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}

// portContract renders a block's public port contract for prompt grounding,
// e.g. "inputs: urls (string[]), title (string) → output: audio_url (string)".
func (b CompositeBlock) portContract() string {
	render := func(ports []sdkr.FlowPort) string {
		parts := make([]string, 0, len(ports))
		for _, p := range ports {
			if t := strings.TrimSpace(p.Type); t != "" {
				parts = append(parts, fmt.Sprintf("%s (%s)", p.Name, t))
			} else {
				parts = append(parts, p.Name)
			}
		}
		return strings.Join(parts, ", ")
	}
	return fmt.Sprintf("inputs: %s → output: %s", render(b.Inputs), render(b.Outputs))
}

// writeCompositeBlockGrounding steers the compiler to emit ONE coarse python
// block for a well-known multi-step job instead of hand-wiring the fragile
// fine-grained dance. No-op when no block matches the intent.
func writeCompositeBlockGrounding(sb *strings.Builder, intent string) {
	matched := MatchCompositeBlocks(intent, 2)
	if len(matched) == 0 {
		return
	}
	sb.WriteString("\nCOARSE COMPOSITE BLOCKS — for this request, PREFER a single self-contained block over hand-wiring many fine-grained steps:\n")
	for _, b := range matched {
		sb.WriteString("• ")
		sb.WriteString(b.Name)
		sb.WriteString(" — ")
		sb.WriteString(b.Summary)
		sb.WriteString("\n    Emit ONE kind=python node (id e.g. \"")
		sb.WriteString(b.NodeID)
		sb.WriteString("\") whose code performs the WHOLE multi-step dance internally; declare its typed ports — ")
		sb.WriteString(b.portContract())
		sb.WriteString(" — and wire upstream URLs into `urls` and the downstream delivery from its `")
		if len(b.Outputs) > 0 {
			sb.WriteString(b.Outputs[0].Name)
		}
		sb.WriteString("` output port. Do NOT decompose it into separate create/add/poll/generate tool nodes glued with {{ }} templates — that handoff is exactly what this block exists to remove. ")
		if b.Requirements != "" {
			sb.WriteString(b.Requirements)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
}

// notebookLMPodcastCode is the inline Python for the NotebookLM Podcast composite
// block. It encapsulates the create → add-sources → generate → poll-until-ready
// dance behind one def run(inputs): URLs + title in, audio_url out. All the
// fragile parts — sequencing, the notebook-id handoff, polling, transient
// retries, and graceful failure — live HERE, in code, tested once (composite_test
// .go), instead of being relearned by the model and wired with templates every
// time.
//
// It drives a NotebookLM CLI (binary `nlm`, overridable via $NLM_BIN). The CLI
// contract it assumes (kept deliberately small + JSON-first so it is mockable):
//
//	nlm create-notebook --title <title> --json     -> {"id": "<notebook_id>"}
//	nlm add-source --notebook <id> --url <url>      -> (exit 0 on success)
//	nlm generate-audio --notebook <id> --json       -> {"status": "pending"|...}
//	nlm audio-status  --notebook <id> --json        -> {"status": "ready",
//	                                                    "audio_url": "<url>"}
//
// Because it shells out (subprocess), the Studio classifier marks it `system`
// and the save/consent gate fires — by design (docs/STUDIO_PYTHON_TOOLS.md §13).
const notebookLMPodcastCode = `def run(inputs):
    """NotebookLM Podcast: source URLs in -> audio_url out.

    Encapsulates the whole create->add->generate->poll dance. Never raises:
    on any failure it returns {"error": "...", ...} so the workflow can branch
    on it instead of crashing the run.
    """
    import os, json, time, subprocess

    if not isinstance(inputs, dict):
        inputs = {}

    urls = inputs.get("urls") or []
    if isinstance(urls, str):
        urls = [urls]
    urls = [u for u in urls if isinstance(u, str) and u.strip()]
    title = (inputs.get("title") or "NotebookLM Podcast")
    if not isinstance(title, str) or not title.strip():
        title = "NotebookLM Podcast"

    nlm = os.environ.get("NLM_BIN", "nlm")
    try:
        poll_interval = float(inputs.get("poll_interval_s", 3))
    except (TypeError, ValueError):
        poll_interval = 3.0
    try:
        max_wait = float(inputs.get("max_wait_s", 600))
    except (TypeError, ValueError):
        max_wait = 600.0

    def _nlm(args, retries=2):
        """Run the CLI; return (ok, parsed_json_or_text, stderr). Retries on
        transient non-zero exits with a short backoff."""
        last = (False, None, "")
        for attempt in range(retries + 1):
            try:
                p = subprocess.run([nlm] + args, capture_output=True, text=True, timeout=120)
            except FileNotFoundError:
                return (False, None, "NotebookLM CLI not found (set NLM_BIN)")
            except Exception as e:
                last = (False, None, str(e))
                time.sleep(min(poll_interval, 2))
                continue
            out = (p.stdout or "").strip()
            parsed = out
            if out:
                try:
                    parsed = json.loads(out)
                except (ValueError, TypeError):
                    parsed = out
            if p.returncode == 0:
                return (True, parsed, (p.stderr or "").strip())
            last = (False, parsed, (p.stderr or "").strip())
            time.sleep(min(poll_interval, 2))
        return last

    if not urls:
        return {"error": "no source urls provided", "notebook_id": "", "audio_url": ""}

    # 1. Create the notebook FIRST and capture its id (the handoff that used to
    #    be a brittle {{ .notebook.id }} template is now a local variable).
    ok, res, err = _nlm(["create-notebook", "--title", title, "--json"])
    if not ok:
        return {"error": "create-notebook failed: " + (err or "unknown"), "audio_url": ""}
    notebook_id = ""
    if isinstance(res, dict):
        notebook_id = res.get("id") or res.get("notebook_id") or res.get("notebookId") or ""
    notebook_id = str(notebook_id).strip()
    if not notebook_id:
        return {"error": "could not determine notebook id from create-notebook", "audio_url": ""}

    # 2. Add every source to THAT notebook (same id, no template).
    added = 0
    failed = []
    for u in urls:
        ok, _res, err = _nlm(["add-source", "--notebook", notebook_id, "--url", u])
        if ok:
            added += 1
        else:
            failed.append({"url": u, "error": err or "add-source failed"})
    if added == 0:
        return {"error": "no sources could be added", "notebook_id": notebook_id,
                "failed_sources": failed, "audio_url": ""}

    # 3. Kick off audio generation for the SAME notebook.
    ok, _res, err = _nlm(["generate-audio", "--notebook", notebook_id, "--json"])
    if not ok:
        return {"error": "generate-audio failed: " + (err or "unknown"),
                "notebook_id": notebook_id, "sources_added": added, "audio_url": ""}

    # 4. Poll until ready (this is the loop that a max_iterations back-edge used
    #    to approximate badly; here it is just a bounded loop in code).
    deadline = time.time() + max_wait
    audio_url = ""
    status = "pending"
    while time.time() < deadline:
        ok, res, err = _nlm(["audio-status", "--notebook", notebook_id, "--json"])
        if ok and isinstance(res, dict):
            status = str(res.get("status") or res.get("state") or "").lower()
            audio_url = res.get("audio_url") or res.get("audioUrl") or res.get("url") or ""
            if status in ("ready", "done", "completed", "complete", "succeeded") and audio_url:
                return {"audio_url": audio_url, "notebook_id": notebook_id,
                        "sources_added": added, "failed_sources": failed,
                        "status": "ready"}
            if status in ("failed", "error", "cancelled", "canceled"):
                return {"error": "audio generation failed (status=" + status + ")",
                        "notebook_id": notebook_id, "sources_added": added,
                        "audio_url": ""}
        time.sleep(poll_interval)

    return {"error": "timed out waiting for audio (last status=" + (status or "unknown") + ")",
            "notebook_id": notebook_id, "sources_added": added, "audio_url": audio_url}
`
