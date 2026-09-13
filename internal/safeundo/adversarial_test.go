package safeundo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSafeUndoPartialRefusalResumesOnlyPendingAndUndoReverses(t *testing.T) {
	for _, status := range []int{401, 403, 404, 405, 409, 412, 415, 422} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f := newFixture(t)
			s, _ := openTestStore(t, f.config())
			f.failPath, f.failStatus = "/document", status
			j := execute(t, s, review(t, s, prepare(t, s, changes()), "apply"))
			if j.Status != "partial" || j.Actions[0].Status != "applied" || j.Actions[1].Status != "pending" {
				t.Fatal(j)
			}
			f.failPath = ""
			j = execute(t, s, review(t, s, j, "apply"))
			if j.Status != "applied" || f.writes["/record"] != 1 || f.writes["/document"] != 1 {
				t.Fatal(j, f.writes)
			}
			j = execute(t, s, review(t, s, j, "undo"))
			if j.Status != "undone" {
				t.Fatal(j)
			}
		})
	}
}

func TestSafeUndoLedgerFailureAfterWriteIsQuarantinedWithoutRestart(t *testing.T) {
	f := newFixture(t)
	s, _ := openTestStore(t, f.config())
	j := prepare(t, s, changes())
	r := review(t, s, j, "apply")
	_, err := s.db.Exec(`CREATE TRIGGER fail_receipt BEFORE UPDATE ON safe_undo_jobs WHEN json_extract(NEW.payload,'$.actions[0].status')='applied' BEGIN SELECT RAISE(ABORT,'simulated disk failure'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Execute(context.Background(), "owner", "agent", j.ID, r.Token, allow)
	if !errors.Is(err, ErrUncertain) || f.writes["/record"] != 1 || f.writes["/document"] != 0 {
		t.Fatal(err, f.writes)
	}
	if _, err = s.db.Exec(`DROP TRIGGER fail_receipt`); err != nil {
		t.Fatal(err)
	}
	j, err = s.Get(context.Background(), "owner", "agent", j.ID)
	if err != nil || j.Status != "needs_review" {
		t.Fatal(j, err)
	}
	if _, err = s.Preview(context.Background(), "owner", "agent", j.ID, "apply", allow); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	r = review(t, s, j, "reconcile")
	if r.Steps[0].Result != "applied" {
		t.Fatal(r.Steps)
	}
	j = execute(t, s, r)
	if !j.Actions[0].Reconciled || f.writes["/record"] != 1 {
		t.Fatal(j)
	}
}

func TestSafeUndoReadBoundary(t *testing.T) {
	for _, kind := range []string{"weak", "missing-etag", "multiple-etags", "bad-type", "encoding", "oversized", "duplicate-keys", "invalid-utf8", "array", "null", "deep", "html-expansion"} {
		t.Run(kind, func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `"1"`)
				w.Header().Set("Content-Type", "application/json")
				body := `{"status":"new"}`
				switch kind {
				case "weak":
					w.Header().Set("ETag", `W/"1"`)
				case "missing-etag":
					w.Header().Del("ETag")
				case "multiple-etags":
					w.Header().Add("ETag", `"2"`)
				case "bad-type":
					w.Header().Set("Content-Type", "text/html")
				case "encoding":
					w.Header().Set("Content-Encoding", "gzip")
				case "oversized":
					body = strings.Repeat(" ", MaxBody) + body
				case "duplicate-keys":
					body = `{"status":"new","status":"other"}`
				case "invalid-utf8":
					body = "{\"status\":\"\xff\"}"
				case "array":
					body = "[]"
				case "null":
					body = "null"
				case "deep":
					body = `{"status":` + strings.Repeat("[", 34) + "0" + strings.Repeat("]", 34) + "}"
				case "html-expansion":
					body = `{"status":"` + strings.Repeat("<", 20000) + `"}`
				}
				io.WriteString(w, body)
			}))
			defer remote.Close()
			cfg := Config{Resources: []Resource{{ID: "record", AgentID: "agent", Name: "Record", Kind: "json_record", Fields: []string{"status"}, URL: remote.URL, AllowLoopbackHTTP: true, ConditionalWrites: true}}}
			s, _ := openTestStore(t, cfg)
			_, err := s.Prepare(context.Background(), "owner", "agent", "", PrepareRequest{Title: "test", Changes: changes().Changes[:1]}, allow)
			if !errors.Is(err, ErrUnavailable) {
				t.Fatal(err)
			}
		})
	}
}

func TestSafeUndoRedirectNeverReceivesCredentialAndEnvironmentRevocation(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	cfg := Config{Resources: []Resource{{ID: "record", AgentID: "agent", Name: "Record", Kind: "json_record", Fields: []string{"status"}, URL: source.URL, AllowLoopbackHTTP: true, ConditionalWrites: true, TokenEnv: "TEST_UNDO_TOKEN"}}}
	s, _ := openTestStore(t, cfg)
	s.token = func(string) string { return "private-fixture-token" }
	if _, err := s.read(context.Background(), cfg.Resources[0]); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if received.Load() != 0 {
		t.Fatal("redirect followed")
	}
	if err := s.mutate(context.Background(), cfg.Resources[0], Action{}, "apply", `"1"`); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	if received.Load() != 0 {
		t.Fatal("mutation redirect followed")
	}
	s.token = func(string) string { return "" }
	if _, err := s.read(context.Background(), cfg.Resources[0]); !errors.Is(err, ErrPermission) {
		t.Fatal(err)
	}
	if err := s.mutate(context.Background(), cfg.Resources[0], Action{}, "apply", `"1"`); !errors.Is(err, ErrPermission) {
		t.Fatal(err)
	}
}

func TestSafeUndoRejectsOversizedResultAndLosslessPublicView(t *testing.T) {
	f := newFixture(t)
	s, _ := openTestStore(t, f.config())
	for _, content := range []string{strings.Repeat("x", MaxBody-2), strings.Repeat("<", 20000)} {
		raw, _ := json.Marshal(content)
		_, err := s.Prepare(context.Background(), "owner", "agent", "", PrepareRequest{Title: "Too big", Changes: []ChangeInput{{ResourceID: "record", Fields: map[string]json.RawMessage{"added": raw}}}}, allow)
		if err == nil {
			t.Fatal("oversized result accepted")
		}
	}
	j := prepare(t, s, PrepareRequest{Title: "Exact", Changes: []ChangeInput{{ResourceID: "record", Fields: map[string]json.RawMessage{"added": json.RawMessage(`9007199254740993123456789`)}}}})
	if j.Actions[0].Fields[0].After.Display != "9007199254740993123456789" || len(j.Actions[0].Fields[0].After.JSON) != 0 {
		t.Fatal(j)
	}
	private, err := s.get(context.Background(), "owner", "agent", j.ID)
	if err != nil || string(private.Actions[0].Fields[0].After.JSON) != "9007199254740993123456789" {
		t.Fatal(private, err)
	}
}

func TestSafeUndoRequestAmbiguityAndETagGrammar(t *testing.T) {
	for _, raw := range []string{`{"confirmed":false,"confirmed":true}`, `{"a":{"x":1,"x":2}}`, `{} {}`, string([]byte{0xff}), strings.Repeat("[", 34) + "0" + strings.Repeat("]", 34)} {
		if ValidateRequest([]byte(raw)) == nil {
			t.Fatal(raw)
		}
	}
	for _, tag := range []string{"*", `W/"v1"`, `"a", "b"`, `"a b"`, `"a\"b"`, "unquoted", strings.Repeat("a", 513)} {
		h := http.Header{}
		h.Set("ETag", tag)
		if _, ok := strongETag(h); ok {
			t.Fatal(tag)
		}
	}
}

func TestSafeUndoPreservesMarkdownMediaAndRefusesMetadataChanges(t *testing.T) {
	f := newFixture(t)
	f.textMedia = "text/markdown; charset=utf-8"
	s, _ := openTestStore(t, f.config())
	j := prepare(t, s, changes())
	j = execute(t, s, review(t, s, j, "apply"))
	if j.Status != "applied" {
		t.Fatal(j)
	}
	f.textMedia = "text/plain"
	if _, err := s.Preview(context.Background(), "owner", "agent", j.ID, "undo", allow); !errors.Is(err, ErrConflict) {
		t.Fatal("metadata changed", err)
	}
	f.textMedia = "text/markdown; charset=utf-8"
	j = execute(t, s, review(t, s, j, "undo"))
	if j.Status != "undone" || f.textMedia != "text/markdown; charset=utf-8" {
		t.Fatal(j)
	}
}

func TestSafeUndoRechecksProjectedSizeAfterUnrelatedEdits(t *testing.T) {
	f := newFixture(t)
	s, _ := openTestStore(t, f.config())
	raw, _ := json.Marshal(strings.Repeat("x", 4096))
	j := prepare(t, s, PrepareRequest{Title: "Grow one field", Changes: []ChangeInput{{ResourceID: "record", Fields: map[string]json.RawMessage{"added": raw}}}})
	f.record["note"] = strings.Repeat("y", MaxBody-500)
	f.versions["/record"]++
	if _, err := s.Preview(context.Background(), "owner", "agent", j.ID, "apply", allow); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	if len(f.writes) != 0 {
		t.Fatal("oversized projection dispatched")
	}
}

func TestSafeUndoSuccessAcknowledgementStillRequiresVerifiableReadback(t *testing.T) {
	for _, mode := range []string{"unchanged-version", "wrong-value", "weak-version"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			s, _ := openTestStore(t, f.config())
			j := prepare(t, s, changes())
			r := review(t, s, j, "apply")
			f.afterWrite = func(path string) {
				switch mode {
				case "unchanged-version":
					f.versions[path]--
				case "wrong-value":
					f.record["status"] = "unexpected"
				case "weak-version":
					f.weak = true
				}
			}
			j = execute(t, s, r)
			if j.Status != "needs_review" || f.writes["/record"] != 1 || f.writes["/document"] != 0 {
				t.Fatal(j, f.writes)
			}
		})
	}
}

func TestSafeUndoExactRequestKeysPreserveCaseSensitiveRecordFields(t *testing.T) {
	for _, raw := range []string{`{"Title":"test","changes":[]}`, `{"title":"test","changes":[{"Resource_ID":"record","fields":{"x":1}}]}`} {
		var req PrepareRequest
		if DecodeRequest([]byte(raw), &req) == nil {
			t.Fatal("case alias accepted", raw)
		}
	}
	var req PrepareRequest
	if err := DecodeRequest([]byte(`{"title":"test","changes":[{"resource_id":"record","fields":{"x":1,"X":2}}]}`), &req); err != nil || len(req.Changes[0].Fields) != 2 {
		t.Fatal(req, err)
	}
	if DecodeRequest([]byte(`{}`), nil) == nil {
		t.Fatal("nil decode target")
	}
}

func TestSafeUndoNumericEqualityIsLosslessAndExponentBounded(t *testing.T) {
	for _, pair := range [][2]string{{"1", "1.0"}, {"1000", "1e3"}, {"-0.000", "0"}, {"0.0010", "1e-3"}, {"1e999999999999999999999999999", "10e999999999999999999999999998"}, {`{"n":[1,9007199254740993]}`, `{"n":[1.0,9007199254740993.0]}`}} {
		if !equalValue(Value{Exists: true, JSON: json.RawMessage(pair[0])}, Value{Exists: true, JSON: json.RawMessage(pair[1])}) {
			t.Fatal(pair)
		}
	}
	for _, pair := range [][2]string{{"9007199254740992", "9007199254740993"}, {"1e999999999999999999999999999", "1e999999999999999999999999998"}, {"0.01", "0.001"}, {`"1"`, `1`}, {`{"n":1}`, `{"N":1}`}} {
		if equalValue(Value{Exists: true, JSON: json.RawMessage(pair[0])}, Value{Exists: true, JSON: json.RawMessage(pair[1])}) {
			t.Fatal(pair)
		}
	}
	f := newFixture(t)
	s, _ := openTestStore(t, f.config())
	j := prepare(t, s, PrepareRequest{Title: "Set numeric field", Changes: []ChangeInput{{ResourceID: "record", Fields: map[string]json.RawMessage{"added": json.RawMessage("1.0")}}}})
	f.afterWrite = func(string) { f.record["added"] = json.Number("1") }
	j = execute(t, s, review(t, s, j, "apply"))
	if j.Status != "applied" {
		t.Fatal("normal JSON number serialization was not accepted", j)
	}
}

func FuzzSafeUndoNumberNormalization(f *testing.F) {
	for _, seed := range []string{"1.0", "-0.000e100", "9007199254740993123456789", "1e999999999999999999999999999", "0.001000", "-100.20e-12"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		v, err := parseJSON([]byte(raw))
		if err != nil {
			return
		}
		n, ok := v.(json.Number)
		if !ok {
			return
		}
		canonical := normalizedNumber(n)
		value, err := parseJSON([]byte(canonical))
		if err != nil {
			t.Fatal(err)
		}
		other, ok := value.(json.Number)
		if !ok || normalizedNumber(other) != canonical || !equalJSON(n, other) {
			t.Fatal("normalization is not stable")
		}
	})
}
