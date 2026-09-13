package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/soulacy/soulacy/internal/auth/apikeys"
	"github.com/soulacy/soulacy/internal/httptestutil"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/safeundo"
	"github.com/soulacy/soulacy/pkg/agent"
	"go.uber.org/zap"
)

type undoHTTPFixture struct {
	mu         sync.Mutex
	bodies     map[string]string
	versions   map[string]int
	writes     int
	afterWrite func()
}

func undoGateway(t *testing.T, key string) (*Server, *undoHTTPFixture) {
	t.Helper()
	s := newTestGateway(t, key)
	s.loader.Register(&agent.Definition{ID: "undo-agent", Name: "Undo"})
	s.loader.Register(&agent.Definition{ID: "other", Name: "Other"})
	f := &undoHTTPFixture{bodies: map[string]string{"/a": "before a", "/b": "before b"}, versions: map[string]int{"/a": 1, "/b": 1}}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method == "GET" {
			w.Header().Set("ETag", fmt.Sprintf(`"v%d"`, f.versions[r.URL.Path]))
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, f.bodies[r.URL.Path])
			return
		}
		if r.Method != "PUT" || r.Header.Get("If-Match") != fmt.Sprintf(`"v%d"`, f.versions[r.URL.Path]) {
			w.WriteHeader(412)
			return
		}
		b, _ := io.ReadAll(r.Body)
		f.bodies[r.URL.Path] = string(b)
		f.versions[r.URL.Path]++
		f.writes++
		if f.afterWrite != nil {
			f.afterWrite()
		}
		w.WriteHeader(204)
	}))
	t.Cleanup(remote.Close)
	cfg := safeundo.Config{}
	for _, id := range []string{"a", "b"} {
		cfg.Resources = append(cfg.Resources, safeundo.Resource{ID: id, AgentID: "undo-agent", Name: "Document " + id, Kind: "webdav_text", URL: remote.URL + "/" + id, ConditionalWrites: true, AllowLoopbackHTTP: true})
	}
	store, err := safeundo.NewStore(filepath.Join(t.TempDir(), "safe-undo.db"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	s.SetSafeUndoStore(store)
	return s, f
}

const undoPath = "/api/v1/agents/undo-agent/safe-undo"
const undoBody = `{"title":"Two documents","changes":[{"resource_id":"a","text":"after a"},{"resource_id":"b","text":"after b"}]}`

func undoRequest(t *testing.T, s *Server, method, path, key, body string, want int) map[string]any {
	t.Helper()
	status, result := gatewayJSON(t, s, method, path, key, body)
	if status != want {
		t.Fatalf("%s %s = %d %v, want %d", method, path, status, result, want)
	}
	return result
}
func TestSafeUndoHTTPReviewApplyUndoAndPrivateBoundaries(t *testing.T) {
	s, f := undoGateway(t, "secret")
	undoRequest(t, s, "GET", undoPath+"/jobs", "", "", 401)
	undoRequest(t, s, "GET", undoPath+"/jobs?api_key=secret", "", "", 403)
	draft := undoRequest(t, s, "POST", undoPath+"/jobs", "secret", undoBody, 201)
	id := draft["id"].(string)
	path := undoPath + "/jobs/" + id
	if f.writes != 0 {
		t.Fatal("prepare mutated external documents")
	}
	list := undoRequest(t, s, "GET", undoPath+"/jobs", "secret", "", 200)
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "before a") || strings.Contains(string(raw), "before_text") {
		t.Fatal("private contents in list")
	}
	for _, fragment := range []string{"owner", "binding", "version", "plan", "last_token"} {
		if _, ok := draft[fragment]; ok {
			t.Fatal("private field leaked:", fragment)
		}
	}
	undoRequest(t, s, "GET", "/api/v1/agents/other/safe-undo/jobs/"+id, "secret", "", 404)
	undoRequest(t, s, "GET", undoPath+"/jobs/resources", "secret", "", 404)
	preview := undoRequest(t, s, "POST", path+"/preview", "secret", `{"direction":"apply"}`, 200)
	token := preview["token"].(string)
	for _, bad := range []string{`{"token":"` + token + `"}`, `{"token":"` + token + `","confirmed":false}`, `{"token":"` + token + `","confirmed":false,"confirmed":true}`, `{"token":"` + token + `","confirmed":true,"url":"https://other"}`} {
		undoRequest(t, s, "POST", path+"/execute", "secret", bad, 400)
	}
	body := `{"token":"` + token + `","confirmed":true}`
	undoRequest(t, s, "POST", path+"/execute", "secret", `{"token":"`+token+`","confirmed":false,"Confirmed":true}`, 400)
	result := undoRequest(t, s, "POST", path+"/execute", "secret", body, 200)
	if result["status"] != "applied" || f.writes != 2 {
		t.Fatal(result, f.writes)
	}
	undoRequest(t, s, "POST", path+"/execute", "secret", body, 200)
	if f.writes != 2 {
		t.Fatal("duplicate replayed")
	}
	preview = undoRequest(t, s, "POST", path+"/preview", "secret", `{"direction":"undo"}`, 200)
	steps := preview["steps"].([]any)
	if steps[0].(map[string]any)["index"] != float64(1) {
		t.Fatal("not reverse order")
	}
	result = undoRequest(t, s, "POST", path+"/execute", "secret", `{"token":"`+preview["token"].(string)+`","confirmed":true}`, 200)
	if result["status"] != "undone" || f.bodies["/a"] != "before a" || f.bodies["/b"] != "before b" {
		t.Fatal(result)
	}
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := s.app.Test(httptestutil.WithHost(req))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("receipt cacheable")
	}
}

func TestSafeUndoHTTPFailsClosedOnDisabledAuthObjectDenyAndMalformedInput(t *testing.T) {
	s, _ := undoGateway(t, "")
	undoRequest(t, s, "GET", undoPath+"/resources", "", "", 503)
	s, f := undoGateway(t, "secret")
	for _, bad := range []string{"null", "[]", undoBody + "{}", `{"title":"t","changes":[{"resource_id":"a","resource_id":"b","text":"test"}]}`, `{"title":"t","changes":[{"resource_id":"a","text":"test","url":"https://other"}]}`, `{"title":"t","changes":[{"resource_id":"a","text":"` + strings.Repeat("a", 65537) + `"}]}`} {
		undoRequest(t, s, "POST", undoPath+"/jobs", "secret", bad, 400)
	}
	undoRequest(t, s, "POST", undoPath+"/jobs", "secret", `{"title":"t","changes":[{"resource_id":"not-configured","text":"x"}]}`, 400)
	s.SetRBAC(rbac.NewManager(publishedDenyStore{}, zap.NewNop()))
	for _, suffix := range []string{"/resources", "/jobs", "/jobs/id"} {
		undoRequest(t, s, "GET", undoPath+suffix, "secret", "", 403)
	}
	for _, suffix := range []string{"/jobs", "/jobs/id/preview", "/jobs/id/execute"} {
		undoRequest(t, s, "POST", undoPath+suffix, "secret", "{}", 403)
	}
	if f.writes != 0 {
		t.Fatal("denied request made a write")
	}
}

func TestSafeUndoHTTPManagedKeyOwnerScopesAndMidFlightRevocation(t *testing.T) {
	s, f := undoGateway(t, "secret")
	engine := lateWiredTestEngine(t, "secret")
	keys, err := apikeys.NewSQLiteStore(filepath.Join(t.TempDir(), "keys.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { keys.Close() })
	engine.SetAPIKeyStore(keys)
	s.SetAuth(engine)
	ctx := context.Background()
	key, record, err := keys.Create(ctx, "Owner", []string{"agents"})
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := keys.Create(ctx, "Other", []string{"agents"})
	if err != nil {
		t.Fatal(err)
	}
	narrow, _, err := keys.Create(ctx, "Reader", []string{"chat", "agents:read"})
	if err != nil {
		t.Fatal(err)
	}
	undoRequest(t, s, "POST", undoPath+"/jobs", narrow, undoBody, 403)
	draft := undoRequest(t, s, "POST", undoPath+"/jobs", key, undoBody, 201)
	path := undoPath + "/jobs/" + draft["id"].(string)
	undoRequest(t, s, "GET", path, other, "", 404)
	undoRequest(t, s, "GET", path, "secret", "", 404)
	preview := undoRequest(t, s, "POST", path+"/preview", key, `{"direction":"apply"}`, 200)
	f.afterWrite = func() {
		if err := keys.Revoke(ctx, record.ID); err != nil {
			panic(err)
		}
		f.afterWrite = nil
	}
	result := undoRequest(t, s, "POST", path+"/execute", key, `{"token":"`+preview["token"].(string)+`","confirmed":true}`, 200)
	if result["status"] != "partial" || f.writes != 1 || f.bodies["/b"] != "before b" {
		t.Fatal("revoked key reached second write", result, f.writes)
	}
	undoRequest(t, s, "GET", path, key, "", 401)
}
