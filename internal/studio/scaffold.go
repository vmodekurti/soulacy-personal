// scaffold.go — the FRAMEWORK (not an LLM) deterministically writes complete,
// runnable Custom Python node bodies for well-known patterns. A user picks a
// scaffold in Studio and fills in the specifics, or uploads their own *.py.
// No external model ever authors executable code: every byte here is shipped,
// reviewable framework code.
//
// Each scaffold defines `def run(inputs):` (the Studio node contract: `inputs`
// is a dict of upstream node outputs; the return value becomes this node's
// output) and is ready to run once the user edits the marked spots.
package studio

// Scaffold is one named, ready-to-edit Python template.
type Scaffold struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	// Requires is what the classifier will flag this scaffold as needing, shown
	// up-front so the user knows it will require consent (system/network).
	Requires []string `json:"requires,omitempty"`
	Code     string   `json:"code"`
}

// Scaffolds returns the built-in framework scaffolds, in display order.
func Scaffolds() []Scaffold {
	return []Scaffold{
		{
			Kind:     "shell",
			Title:    "Run a shell command (CLI)",
			Requires: []string{"system"},
			Code: `"""Run a local command and return its output. Framework scaffold — edit the
command below. inputs holds upstream node outputs; the return value is this
node's output."""
import subprocess, json


def run(inputs):
    # Build the command. Reference upstream values, e.g. inputs.get("url").
    cmd = ["echo", "hello"]
    proc = subprocess.run(cmd, capture_output=True, text=True)
    if proc.returncode != 0:
        raise RuntimeError(proc.stderr.strip() or ("exit status %d" % proc.returncode))
    out = proc.stdout.strip()
    try:
        return json.loads(out)
    except Exception:
        return out
`,
		},
		{
			Kind:     "http",
			Title:    "Make an HTTP request",
			Requires: []string{"network"},
			Code: `"""Fetch a URL and return the parsed JSON (or text). Framework scaffold — set
the url / method. inputs holds upstream node outputs."""
import json, urllib.request


def run(inputs):
    url = inputs.get("url") or "https://example.com"
    req = urllib.request.Request(url, headers={"User-Agent": "soulacy-studio"})
    with urllib.request.urlopen(req, timeout=30) as resp:
        body = resp.read().decode("utf-8")
    try:
        return json.loads(body)
    except Exception:
        return body
`,
		},
		{
			Kind:  "transform",
			Title: "Transform / reshape data (no I/O)",
			Code: `"""Reshape the inputs into this node's output. Framework scaffold — pure data,
no side effects. inputs holds upstream node outputs keyed by their output var."""


def run(inputs):
    # Example: pick the first 5 items from an upstream "search" result.
    items = (inputs.get("search") or {}).get("results") or []
    return [{"title": it.get("title", ""), "url": it.get("url", "")} for it in items[:5]]
`,
		},
	}
}

// ScaffoldByKind returns the scaffold of the given kind (zero value if unknown).
func ScaffoldByKind(kind string) Scaffold {
	for _, s := range Scaffolds() {
		if s.Kind == kind {
			return s
		}
	}
	return Scaffold{}
}
