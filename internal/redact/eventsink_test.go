// eventsink_test.go — every consumer of a message.Event is classified.
//
// THE RECURRING BUG THIS EXISTS TO CATCH is not "somebody forgot to redact".
// It is "there are two implementations of one sink and only one of them
// redacts". It has now happened three times in the same codebase:
//
//   - internal/actionlog redacted; internal/storage/postgres, the SAME store
//     with the SAME contract and the backend a TEAM deployment selects, did
//     not. Adding colleagues turned masked tool arguments into clear ones in
//     a table shared by every workspace.
//   - EventHub.Emit fans out to four consumers. The action log — a file on
//     the operator's own disk — redacted. The WebSocket projection and the
//     NATS publisher, both of which leave the process, did not.
//   - internal/introspect ran an UNINSTALLED plugin's hook with os.Environ()
//     while every other subprocess boundary used the sandbox filter.
//
// In all three the protection was present where somebody had recently thought
// about it and absent on the path the data actually took. A comment cannot
// catch that. A list of "the sinks I know about" cannot catch it either,
// because the whole failure is a sink nobody listed.
//
// So the guard DISCOVERS its own subjects: it parses the repo and finds every
// function that accepts a message.Event, then requires each to either redact
// or be classified below with the reason it does not need to. A new consumer
// fails the build until somebody decides which it is — which is the only
// moment anyone reliably thinks about this.
package redact

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// eventReaders are functions that take a message.Event and do NOT redact it,
// each with the reason that is correct.
//
// The distinction that matters is EGRESS, not sensitivity: a function that
// reads two named fields out of a payload and drops the rest has already
// discarded everything secret, whereas a function that carries the payload
// onward has not, however briefly.
var eventReaders = map[string]string{
	// ── fan-out and transport: they hand the event to the sinks below, which
	// redact at their own boundary. Redacting here as well would normalise
	// typed payloads into maps for in-process consumers, which is a different
	// change from a security fix.
	"internal/runtime/engine.go:emit":           "stamps the workspace and forwards to the sink; every sink redacts at its own boundary",
	"internal/gateway/events.go:Emit":           "fans out to the action log, the publisher and the projection, each of which redacts",
	"internal/gateway/events.go:broadcastEvent": "receives already-projected bytes; the event value is used only for the authorization check",
	"internal/events/events.go:PublishEvent":    "enqueues; the envelope constructor is the redaction point",
	"internal/runtime/engine.go:Emit":           "the no-op sink discards the event",

	// ── authorization and routing: they read metadata, never the payload.
	"internal/gateway/session_auth.go:authorizeEvent":                  "compares workspace and session identity; never touches the payload",
	"internal/gateway/session_activity.go:Note":                        "bumps a heartbeat keyed by session; never touches the payload",
	"internal/gateway/runledger.go:runLedgerIsBrowserEvent":            "tests the event type string",
	"internal/gateway/workboard_artifacts.go:observeTerminalArtifacts": "tests terminal metadata and asynchronously materializes paths from the already-redacted action log; never reads this event's payload",

	// ── field extractors: they pull named, non-secret fields and drop the
	// rest, so the payload does not survive the call.
	"internal/studio/strategyfit.go:Observe":                   "extracts run id, provider, model, strategy and success",
	"internal/studio/macros.go:Observe":                        "extracts tool name, workflow step and intent text, each sanitised",
	"internal/gateway/studio.go:studioEventEvidenceLine":       "extracts a single evidence string",
	"internal/gateway/studio.go:studioEventErrorText":          "extracts a single error string",
	"internal/gateway/studio.go:summarizeActionEvents":         "counts and summarises event types for a history row",
	"internal/gateway/runledger.go:runLedgerHasBrowserTrace":   "counts browser events",
	"internal/gateway/runledger.go:runLedgerBrowserEventCount": "counts browser events",
	"internal/gateway/support.go:supportFlowRunLedgerRows":     "builds ledger rows from events already read back out of the redacted action log",
	"internal/browsertrace/browsertrace.go:Build":              "builds a browser trace from navigation and screenshot events",

	// ── downstream of a redaction that already happened on the way in.
	"internal/actionlog/actionlog.go:flush":           "writes the batch Append already redacted",
	"internal/actionlog/actionlog.go:writeFileBatch":  "writes the batch Append already redacted",
	"internal/actionlog/actionlog.go:writeDBBatch":    "writes the batch Append already redacted",
	"internal/storage/postgres/postgres.go:flush":     "writes the batch Append already redacted",
	"internal/storage/postgres/postgres.go:writeFile": "writes the batch Append already redacted",

	// ── the replay buffer stores the PROJECTED bytes for delivery and keeps
	// the event value only so a reconnecting client can be re-authorized
	// against it. What ResumeSince returns is entry.data, which project()
	// already redacted; the retained event never reaches a socket.
	"internal/gateway/eventcursor.go:Append": "retains projected bytes for delivery; the event value is only re-authorized against",

	// ── readers of history. These take events that came BACK OUT of the
	// action log, which redacted them on the way in, so there is no
	// unredacted payload here to protect. Redacting again would be harmless
	// but would also imply the input might be unredacted, which is precisely
	// the belief that produced the postgres gap.
	"internal/learning/evidence.go:BuildEvidence":                 "reads back from the redacted action log; counts skill and outcome events",
	"internal/learning/evidence.go:buildAgentEvidence":            "reads back from the redacted action log",
	"internal/learning/evidence.go:buildEvidenceTrend":            "reads back from the redacted action log",
	"internal/learning/evidence.go:latestEvidenceTime":            "reads timestamps only",
	"internal/learning/runs.go:RunFromEvents":                     "reconstructs run timing and outcome from the redacted history",
	"internal/learning/runs.go:RunsFromRecentEvents":              "reconstructs run timing and outcome from the redacted history",
	"internal/proactive/proactive.go:Detect":                      "counts manual runs and failures per agent",
	"internal/gateway/runledger.go:buildRunLedger":                "builds the run ledger from redacted history",
	"internal/gateway/api.go:replaySourceMessage":                 "re-parses a message.in payload to re-run it; a replay must send what was sent",
	"internal/gateway/chat_feedback.go:feedbackRunExists":         "checks a run id exists",
	"internal/gateway/studio.go:studioSessionFailingInput":        "extracts the input that failed, from redacted history",
	"internal/gateway/studio.go:studioSessionEvidence":            "extracts evidence lines from redacted history",
	"internal/gateway/workboard_artifacts.go:detectArtifactPaths": "extracts file paths written during a run",
}

func TestEveryConsumerOfAnEventIsClassified(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	fset := token.NewFileSet()
	var unclassified []string

	err := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if name := entry.Name(); name == ".git" || name == ".gomodcache" || name == "gui" || name == "node_modules" || name == "vendor" {
				return fs.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)

		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			// A file this guard cannot parse is a file it cannot vouch for.
			// Failing here is noisier than skipping and that is the point:
			// silent coverage gaps are what the guard exists to prevent.
			t.Fatalf("parse %s: %v", rel, parseErr)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Type.Params == nil || fn.Body == nil {
				continue
			}
			if !takesAnEvent(fn.Type.Params) {
				continue
			}
			key := rel + ":" + fn.Name.Name
			if _, classified := eventReaders[key]; classified {
				continue
			}
			// Only a real redact.Value CALL counts. Mentioning the symbol
			// (a reference, an assignment, a comment) does not: the mutation
			// that motivates this guard is a redaction being removed while
			// the import stays behind.
			if bodyMentions(fn.Body, "redact", "Value") {
				continue
			}
			unclassified = append(unclassified, key)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, key := range unclassified {
		t.Errorf("%s consumes a message.Event without redacting its payload and is not in eventReaders — "+
			"either call redact.Value on the payload, or add it with the reason the payload does not survive "+
			"the call. Three sinks in this repo shipped unredacted because a second implementation of an "+
			"already-protected boundary was never listed anywhere", key)
	}
}

// takesAnEvent reports whether any parameter is a message.Event or a slice of
// them. Pointer and slice forms both count: the question is whether the
// payload reaches the body, not how it is spelled.
func takesAnEvent(params *ast.FieldList) bool {
	for _, field := range params.List {
		if eventTypeName(field.Type) {
			return true
		}
	}
	return false
}

func eventTypeName(expr ast.Expr) bool {
	switch typed := expr.(type) {
	case *ast.SelectorExpr:
		pkg, ok := typed.X.(*ast.Ident)
		return ok && pkg.Name == "message" && typed.Sel.Name == "Event"
	case *ast.ArrayType:
		return eventTypeName(typed.Elt)
	case *ast.StarExpr:
		return eventTypeName(typed.X)
	}
	return false
}

// bodyMentions reports whether the body contains a call to pkg.fn.
func bodyMentions(body *ast.BlockStmt, pkg, fn string) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		if found {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if ok && ident.Name == pkg && sel.Sel.Name == fn {
			found = true
		}
		return true
	})
	return found
}
