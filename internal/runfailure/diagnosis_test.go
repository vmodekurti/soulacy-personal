package runfailure

import (
	"github.com/soulacy/soulacy/internal/trust"
	"strings"
	"testing"
)

func TestJobFailureIsNotHiddenBySuccessfulStatusLookup(t *testing.T) {
	detail := ToolError(trust.Wrap(trust.Untrusted, "status", `{"status":"failed","error_message":"model 'missing' not found"}`), false)
	d := Observed("open_notebook_get_podcast_job", detail)
	if !strings.Contains(d.NextAction, "Open Notebook") || !strings.Contains(d.Detail, "missing") {
		t.Fatalf("%#v", d)
	}
	if ToolError(`[{"status":"failed","error_message":"old error"}]`, false) != "" {
		t.Fatal("historical episode list is not this run's failure")
	}
}

func TestRecorderClearsRecoveredErrorsAndPreservesActualFailure(t *testing.T) {
	var r Recorder
	r.Observe("fetch_url", "403 forbidden")
	r.Observe("fetch_url", "")
	r.Observe("start_website_action", "could not derive schema-valid arguments: invalid JSON: unexpected end of JSON input")
	d := r.Diagnosis("Open session: required tool was not observed; Return audio: final output did not match")
	if d.Step != "start_website_action" || len(d.Checks) != 2 || !strings.Contains(d.Detail, "unexpected end") {
		t.Fatalf("%#v", d)
	}
	r.Observe("start_website_action", "")
	if r.Diagnosis("missing result").Step != "" {
		t.Fatal("recovered error presented as cause")
	}
}
