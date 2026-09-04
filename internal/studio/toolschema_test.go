package studio

import (
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/agent"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func mcpCatalog(tools map[string]string) Catalog {
	var srv CatalogMCPServer
	srv.Server = "notebooklm"
	for name, params := range tools {
		srv.Tools = append(srv.Tools, CatalogMCPTool{Name: name, Params: params})
	}
	return Catalog{MCP: []CatalogMCPServer{srv}}
}

func toolFlow() Flow {
	return Flow{Nodes: []sdkr.FlowNode{
		{ID: "trigger", Kind: "trigger"},
		{ID: "create", Kind: "tool", Tool: "mcp__notebooklm__notebook_create"},
		{ID: "audio", Kind: "tool", Tool: "mcp__notebooklm__studio_create"},
	}}
}

var liveTools = map[string]string{
	"mcp__notebooklm__notebook_create": "title*:string, summary:string",
	"mcp__notebooklm__studio_create":   "notebook_id*:string, voice:string",
}

func TestCaptureToolSchemas(t *testing.T) {
	at := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	snap := CaptureToolSchemas(toolFlow(), mcpCatalog(liveTools), at)
	if !snap.HasTools() || len(snap.Tools) != 2 {
		t.Fatalf("expected both called tools recorded: %+v", snap)
	}
	// Deterministic ordering — a re-save must not churn the file.
	if snap.Tools[0].Tool > snap.Tools[1].Tool {
		t.Error("records must be sorted by tool name")
	}
	rec := snap.Record("mcp__notebooklm__studio_create")
	if rec == nil || rec.Hash == "" {
		t.Fatal("expected a hashed record")
	}
	if len(rec.Nodes) != 1 || rec.Nodes[0] != "audio" {
		t.Errorf("record should name the calling node: %+v", rec.Nodes)
	}

	// A tool with no discoverable schema is deliberately not recorded — an
	// empty hash would later read as "the schema changed to nothing".
	unknown := Flow{Nodes: []sdkr.FlowNode{{ID: "x", Kind: "tool", Tool: "mcp__ghost__thing"}}}
	if snap := CaptureToolSchemas(unknown, mcpCatalog(liveTools), at); snap != nil {
		t.Errorf("an undiscoverable tool must not be snapshotted: %+v", snap)
	}
}

func TestHashIgnoresCosmeticDifferences(t *testing.T) {
	// Order and whitespace carry no contract meaning — a server listing the
	// same parameters differently has not changed what a call must supply.
	a := agent.HashToolSignature("title*:string, summary:string")
	b := agent.HashToolSignature("summary:string,  title*:string")
	if a != b {
		t.Error("parameter order/whitespace must not read as drift")
	}
	// Requiredness DOES matter.
	if agent.HashToolSignature("title:string") == agent.HashToolSignature("title*:string") {
		t.Error("a parameter becoming required must change the hash")
	}
}

func TestDetectToolDrift(t *testing.T) {
	at := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	snap := CaptureToolSchemas(toolFlow(), mcpCatalog(liveTools), at)

	// No change → no drift.
	if d := DetectToolDrift(snap, mcpCatalog(liveTools)); len(d) != 0 {
		t.Fatalf("unchanged schemas must not report drift: %+v", d)
	}

	// A parameter renamed: breaking, and it must name the node to fix.
	renamed := map[string]string{
		"mcp__notebooklm__notebook_create": "title*:string, summary:string",
		"mcp__notebooklm__studio_create":   "notebookId*:string, voice:string",
	}
	drift := DetectToolDrift(snap, mcpCatalog(renamed))
	if len(drift) != 1 {
		t.Fatalf("expected one drifted tool, got %+v", drift)
	}
	d := drift[0]
	if !d.Breaking {
		t.Error("a renamed parameter is breaking")
	}
	if len(d.Nodes) != 1 || d.Nodes[0] != "audio" {
		t.Errorf("drift must name the affected node: %+v", d.Nodes)
	}
	joined := strings.Join(d.Fields, " ")
	if !strings.Contains(joined, "notebook_id (removed)") || !strings.Contains(joined, "notebookid (added, required)") {
		t.Errorf("drift must name the exact fields: %v", d.Fields)
	}
	if !NeedsRecertification(drift) {
		t.Error("breaking drift must require recertification")
	}

	// An added OPTIONAL parameter is reported but not breaking: every call that
	// worked yesterday still validates.
	additive := map[string]string{
		"mcp__notebooklm__notebook_create": "title*:string, summary:string",
		"mcp__notebooklm__studio_create":   "notebook_id*:string, voice:string, speed:number",
	}
	drift = DetectToolDrift(snap, mcpCatalog(additive))
	if len(drift) != 1 || drift[0].Breaking {
		t.Fatalf("an added optional parameter must not be breaking: %+v", drift)
	}
	if NeedsRecertification(drift) {
		t.Error("additive drift must not revoke certification")
	}

	// A newly REQUIRED parameter is breaking.
	nowRequired := map[string]string{
		"mcp__notebooklm__notebook_create": "title*:string, summary*:string",
		"mcp__notebooklm__studio_create":   "notebook_id*:string, voice:string",
	}
	drift = DetectToolDrift(snap, mcpCatalog(nowRequired))
	if len(drift) != 1 || !drift[0].Breaking {
		t.Fatalf("a newly required parameter is breaking: %+v", drift)
	}

	// A tool that disappeared entirely.
	gone := map[string]string{"mcp__notebooklm__notebook_create": "title*:string, summary:string"}
	drift = DetectToolDrift(snap, mcpCatalog(gone))
	if len(drift) != 1 || drift[0].Kind != "removed" || !drift[0].Breaking {
		t.Fatalf("a removed tool must be reported as breaking: %+v", drift)
	}

	// An agent saved before P0-3 has no snapshot and must not report drift.
	if d := DetectToolDrift(nil, mcpCatalog(renamed)); d != nil {
		t.Errorf("no snapshot means no drift claim: %+v", d)
	}
}

func TestScrubSecrets(t *testing.T) {
	// By KEY — whatever the value is.
	got := ScrubValue(map[string]any{"api_key": "short", "Authorization": "x", "title": "Weekly brief"})
	m := got.(map[string]any)
	if m["api_key"] != RedactedMarker || m["Authorization"] != RedactedMarker {
		t.Errorf("secret-named fields must be redacted regardless of length: %+v", m)
	}
	if m["title"] != "Weekly brief" {
		t.Errorf("ordinary fields must survive: %+v", m)
	}

	// By SHAPE — whatever the field is called. A captured third-party example
	// will not use our naming conventions.
	shapes := map[string]string{
		"note":  "eyJ" + strings.Repeat("a", 8) + "." + strings.Repeat("b", 8) + "." + strings.Repeat("c", 8),
		"blob":  "AKIA" + strings.Repeat("A", 16),
		"key":   "sk-" + strings.Repeat("a", 24),
		"hdr":   "Bearer " + strings.Repeat("a", 20),
		"where": "https://user:hunter2@example.com/x",
	}
	for field, val := range shapes {
		out := ScrubValue(map[string]any{field: val}).(map[string]any)
		if !strings.Contains(out[field].(string), RedactedMarker) {
			t.Errorf("%s: credential-shaped value not redacted: %v", field, out[field])
		}
	}

	// Prose and ordinary ids must survive — over-redaction that destroys the
	// shape defeats the purpose of capturing an example.
	safe := []string{
		"The quarterly report is ready for review.",
		"1df07cfc-e052-411f-92fc",
		"https://notebooklm.google.com/notebook/1df0",
		"processing",
	}
	for _, s := range safe {
		if got := ScrubString(s); got != s {
			t.Errorf("ordinary value was mangled: %q → %q", s, got)
		}
	}

	// Nested structures keep their shape.
	nested := ScrubValue(map[string]any{
		"results": []any{
			map[string]any{"url": "https://a", "token": "abc"},
		},
	}).(map[string]any)
	arr := nested["results"].([]any)
	item := arr[0].(map[string]any)
	if item["url"] != "https://a" || item["token"] != RedactedMarker {
		t.Errorf("nested scrub failed: %+v", item)
	}
}
